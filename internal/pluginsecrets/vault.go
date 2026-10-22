// Package pluginsecrets encrypts plugin options with an OS-kept key. No
// plaintext fallback exists when the credential service is unavailable.
package pluginsecrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
	"gokin/internal/pluginpolicy"
)

type Keyring interface {
	Get(service, account string) (string, error)
	Set(service, account, value string) error
}

type osKeyring struct{}

func (osKeyring) Get(service, account string) (string, error) { return keyring.Get(service, account) }
func (osKeyring) Set(service, account, value string) error {
	return keyring.Set(service, account, value)
}

const Service = "gokin.plugin-options.v1"

// Serialize creation within embedding hosts too. Cross-process writers hold
// the plugin management lease before calling Update.
var keyMu sync.Mutex

func Path(userDir string) string {
	return filepath.Join(userDir, "plugins", "gokin", "options.enc.json")
}
func Account(userDir string) string {
	path, _ := filepath.Abs(userDir)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return hex.EncodeToString(sum[:])
}

func cipherFor(userDir string, store Keyring, create bool) (cipher.AEAD, error) {
	if store == nil {
		store = osKeyring{}
	}
	keyMu.Lock()
	defer keyMu.Unlock()
	text, err := store.Get(Service, Account(userDir))
	if errors.Is(err, keyring.ErrNotFound) && create {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("cannot generate plugin credential key")
		}
		text = base64.StdEncoding.EncodeToString(key)
		if err := store.Set(Service, Account(userDir), text); err != nil {
			return nil, fmt.Errorf("secure plugin credential storage is unavailable; options were not saved")
		}
		// A successful write must be readable before any encrypted state is
		// published. Backend errors and their output never reveal the key.
		text, err = store.Get(Service, Account(userDir))
	}
	if err != nil {
		return nil, fmt.Errorf("secure plugin credential storage is unavailable; unlock the system credential store")
	}
	key, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("invalid plugin credential key; encrypted options were retained")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("invalid plugin credential key")
	}
	return cipher.NewGCM(block)
}

func entries(fields map[string]json.RawMessage) (map[string]string, error) {
	if len(fields) == 0 {
		return map[string]string{}, nil
	}
	var version int
	var plugins map[string]string
	if json.Unmarshal(fields["version"], &version) != nil || version != 1 || json.Unmarshal(fields["plugins"], &plugins) != nil || plugins == nil || len(plugins) > 256 {
		return nil, fmt.Errorf("invalid encrypted plugin option store")
	}
	for _, value := range plugins {
		if len(value) > 128<<10 {
			return nil, fmt.Errorf("encrypted plugin option exceeds limits")
		}
	}
	return plugins, nil
}

func decrypt(id, encoded string, aead cipher.AEAD) (map[string]any, error) {
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(sealed) < aead.NonceSize()+aead.Overhead() {
		return nil, fmt.Errorf("invalid encrypted plugin options")
	}
	data, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(Service+":"+id))
	if err != nil {
		return nil, fmt.Errorf("encrypted plugin option authentication failed")
	}
	var values map[string]any
	if len(data) > 64<<10 || pluginpolicy.ValidateJSON(data) != nil || json.Unmarshal(data, &values) != nil || values == nil || len(values) > 64 {
		return nil, fmt.Errorf("invalid encrypted plugin option values")
	}
	return values, nil
}

func Read(userDir, id string, fields map[string]json.RawMessage, store Keyring) (map[string]any, error) {
	plugins, err := entries(fields)
	if err != nil {
		return nil, err
	}
	encoded, found := plugins[id]
	if !found {
		return map[string]any{}, nil
	}
	aead, err := cipherFor(userDir, store, false)
	if err != nil {
		return nil, err
	}
	return decrypt(id, encoded, aead)
}

func Update(userDir, id string, fields map[string]json.RawMessage, values map[string]any, store Keyring) (map[string]json.RawMessage, error) {
	plugins, err := entries(fields)
	if err != nil {
		return nil, err
	}
	aead, err := cipherFor(userDir, store, len(plugins) == 0)
	if err != nil {
		return nil, err
	}
	all := map[string]any{}
	if encoded, found := plugins[id]; found {
		all, err = decrypt(id, encoded, aead)
		if err != nil {
			return nil, err
		}
	}
	for key, value := range values {
		all[key] = value
	}
	data, err := json.Marshal(all)
	if err != nil || len(data) > 64<<10 || len(all) > 64 {
		return nil, fmt.Errorf("plugin secrets exceed limits")
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("cannot generate plugin credential nonce")
	}
	plugins[id] = base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, data, []byte(Service+":"+id)))
	if len(plugins) > 256 {
		return nil, fmt.Errorf("too many encrypted plugin configurations")
	}
	raw, _ := json.Marshal(plugins)
	if len(raw) > (1<<20)-128 {
		return nil, fmt.Errorf("encrypted plugin option store exceeds 1 MiB")
	}
	return map[string]json.RawMessage{"version": json.RawMessage("1"), "plugins": raw}, nil
}

// Remove needs no credential-store unlock, and leaves all other plugins'
// ciphertext intact. The shared key remains usable by their live sessions.
func Remove(id string, fields map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	plugins, err := entries(fields)
	if err != nil {
		return nil, err
	}
	delete(plugins, id)
	raw, _ := json.Marshal(plugins)
	return map[string]json.RawMessage{"version": json.RawMessage("1"), "plugins": raw}, nil
}

// Rename re-authenticates values for the new identifier. Destination values
// win conflicts. A foreign workspace or live managed alias can retain its old
// ciphertext; no plaintext is ever returned to lifecycle state or diagnostics.
func Rename(userDir, from, to string, fields map[string]json.RawMessage, keepOld bool, store Keyring) (map[string]json.RawMessage, error) {
	plugins, err := entries(fields)
	if err != nil {
		return nil, err
	}
	if _, set := plugins[from]; !set {
		return fields, nil
	}
	if to == "" {
		if keepOld {
			return fields, nil
		}
		return Remove(from, fields)
	}
	values, err := Read(userDir, from, fields, store)
	if err != nil {
		return nil, err
	}
	destination, err := Read(userDir, to, fields, store)
	if err != nil {
		return nil, err
	}
	for key, value := range destination {
		values[key] = value
	}
	next, err := Update(userDir, to, fields, values, store)
	if err != nil || keepOld {
		return next, err
	}
	return Remove(from, next)
}

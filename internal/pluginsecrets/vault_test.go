package pluginsecrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

type testKeyring struct {
	keys   map[string]string
	err    error
	writes int
}

func (s *testKeyring) Get(service, account string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if value, set := s.keys[service+account]; set {
		return value, nil
	}
	return "", keyring.ErrNotFound
}
func (s *testKeyring) Set(service, account, value string) error {
	if s.err != nil {
		return s.err
	}
	s.keys[service+account] = value
	s.writes++
	return nil
}

func TestVaultAuthenticatedRoundTripAndIsolation(t *testing.T) {
	store := &testKeyring{keys: map[string]string{}}
	user := t.TempDir()
	secret := "private-token-value\nюникод"
	first, err := Update(user, "alpha@market", nil, map[string]any{"token": secret, "many": []string{"a", "b"}}, store)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(first)
	if bytes.Contains(data, []byte("private-token-value")) || bytes.Contains(data, []byte("token")) {
		t.Fatal("plaintext in vault")
	}
	value, err := Read(user, "alpha@market", first, store)
	if err != nil || value["token"] != secret {
		t.Fatalf("read=%+v %v", value, err)
	}
	second, err := Update(user, "alpha@market", first, map[string]any{"token": "changed"}, store)
	if err != nil || bytes.Equal(first["plugins"], second["plugins"]) || store.writes != 1 {
		t.Fatalf("update=%v writes=%d", err, store.writes)
	}
	if value, err := Read(user, "alpha@market", first, store); err != nil || value["token"] != secret {
		t.Fatal("old transaction mutated")
	}
	var entries map[string]string
	json.Unmarshal(first["plugins"], &entries)
	entries["beta@market"] = entries["alpha@market"]
	forged := map[string]json.RawMessage{"version": json.RawMessage("1")}
	forged["plugins"], _ = json.Marshal(entries)
	if _, err := Read(user, "beta@market", forged, store); err == nil {
		t.Fatal("ciphertext replay across plugin IDs accepted")
	}
	if _, err := Read(t.TempDir(), "alpha@market", first, store); err == nil {
		t.Fatal("ciphertext replay across config directories accepted")
	}
	store.keys[Service+Account(user)] = strings.Repeat("A", 44)
	if _, err := Read(user, "alpha@market", first, store); err == nil {
		t.Fatal("wrong key accepted")
	}
}

func TestVaultUnavailableAndRemoval(t *testing.T) {
	store := &testKeyring{keys: map[string]string{}, err: errors.New("backend leaked secret-value")}
	if _, err := Update(t.TempDir(), "alpha@market", nil, map[string]any{"token": "secret"}, store); err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatal("backend failure not sanitized")
	}
	store.err = nil
	user := t.TempDir()
	first, err := Update(user, "alpha@market", nil, map[string]any{"token": "secret"}, store)
	if err != nil {
		t.Fatal(err)
	}
	store.err = errors.New("locked")
	removed, err := Remove("alpha@market", first)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := Read(user, "alpha@market", removed, store); err != nil || len(value) != 0 {
		t.Fatal("removal needs unlocked keyring")
	}
}

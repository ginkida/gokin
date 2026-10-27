// Package toolschema controls schema publication, never execution authority.
package toolschema

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"sync"

	"google.golang.org/genai"
)

const maxSnapshotBytes = 128 << 10

// MaxDiscoveryResultBytes bounds complete schemas before generic delivery.
const MaxDiscoveryResultBytes = 28000

// CompleteDiscoveryKey marks bounded discovery before hook/notification
// enrichment. It is result content metadata, never an authorization grant.
const CompleteDiscoveryKey = "complete_tool_schema"

func IsCompleteDiscovery(response map[string]any) bool {
	complete, _ := response[CompleteDiscoveryKey].(bool)
	success, _ := response["success"].(bool)
	return complete && success
}

var core = map[string]bool{
	"read": true, "edit": true, "bash": true, "glob": true,
	"grep": true, "write": true, "todo": true, "run_tests": true,
}

// Snapshot persists presentation only. Current policy must be applied before
// Publish; these declarations cannot authorize a tool after resume.
type Snapshot struct {
	Version      int                          `json:"version"`
	Deferred     bool                         `json:"deferred"`
	Declarations []*genai.FunctionDeclaration `json:"declarations,omitempty"`
}

type Profile struct {
	mu       sync.Mutex
	deferred bool
	frozen   map[string]*genai.FunctionDeclaration
	eligible map[string]bool
}

func New(deferred bool) *Profile {
	return &Profile{deferred: deferred, frozen: make(map[string]*genai.FunctionDeclaration)}
}

func (p *Profile) Deferred() bool { return p != nil && p.deferred }

func (p *Profile) Clone() *Profile {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	copy := New(p.deferred)
	for name, d := range p.frozen {
		copy.frozen[name] = clone(d)
	}
	copy.eligible = maps.Clone(p.eligible)
	return copy
}

func Restore(snapshot *Snapshot) (*Profile, error) {
	if snapshot == nil {
		return New(false), nil
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > maxSnapshotBytes {
		return nil, fmt.Errorf("invalid tool schema snapshot size")
	}
	if snapshot.Version != 1 {
		return nil, fmt.Errorf("unsupported tool schema snapshot version %d", snapshot.Version)
	}
	p := New(snapshot.Deferred)
	if len(snapshot.Declarations) > len(core)+2 {
		return nil, fmt.Errorf("too many frozen tool schemas")
	}
	for _, decl := range snapshot.Declarations {
		if decl == nil || (!core[decl.Name] && decl.Name != "tool_search" && decl.Name != "use_tool") || p.frozen[decl.Name] != nil || !snapshot.Deferred {
			return nil, fmt.Errorf("invalid frozen tool schema")
		}
		p.frozen[decl.Name] = clone(decl)
	}
	return p, nil
}

func (p *Profile) Snapshot() *Snapshot {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s := &Snapshot{Version: 1, Deferred: p.deferred}
	for _, d := range p.frozen {
		s.Declarations = append(s.Declarations, clone(d))
	}
	sort.Slice(s.Declarations, func(i, j int) bool { return s.Declarations[i].Name < s.Declarations[j].Name })
	return s
}

// Publish freezes native descriptions, including schemas with dynamic catalogs.
// Explicit policy changes may remove/re-add a native declaration; discovery and
// deferred catalog changes never add declarations to the wire tools array.
func (p *Profile) Publish(eligible []*genai.Tool) []*genai.Tool {
	if !p.Deferred() {
		return eligible
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.eligible != nil && len(p.eligible) == 0 {
		return nil
	}
	var declarations []*genai.FunctionDeclaration
	seen := map[string]bool{}
	for _, envelope := range eligible {
		if envelope == nil {
			continue
		}
		for _, d := range envelope.FunctionDeclarations {
			if d == nil || seen[d.Name] {
				continue
			}
			if p.eligible != nil && !p.eligible[d.Name] && d.Name != "tool_search" && d.Name != "use_tool" {
				continue
			}
			seen[d.Name] = true
			if core[d.Name] {
				if p.frozen[d.Name] == nil {
					p.frozen[d.Name] = clone(d)
				}
				declarations = append(declarations, clone(p.frozen[d.Name]))
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	// Persist the meta declarations too: a later binary may update their
	// descriptions without rewriting the prefix of an existing conversation.
	if p.frozen["tool_search"] == nil {
		p.frozen["tool_search"] = SearchDeclaration()
	}
	if p.frozen["use_tool"] == nil {
		p.frozen["use_tool"] = UseDeclaration()
	}
	declarations = append(declarations, clone(p.frozen["tool_search"]), clone(p.frozen["use_tool"]))
	sort.Slice(declarations, func(i, j int) bool { return declarations[i].Name < declarations[j].Name })
	return []*genai.Tool{{FunctionDeclarations: declarations}}
}

// Configure binds the current application policy. Raw registry publishers
// (such as MCP refresh) cannot re-advertise capabilities hidden by that policy.
// This runtime policy is deliberately excluded from persisted snapshots.
func (p *Profile) Configure(eligible []*genai.Tool) {
	if p == nil {
		return
	}
	allowed := map[string]bool{}
	for _, env := range eligible {
		if env == nil {
			continue
		}
		for _, d := range env.FunctionDeclarations {
			if d != nil {
				allowed[d.Name] = true
			}
		}
	}
	p.mu.Lock()
	p.eligible = allowed
	p.mu.Unlock()
}

func clone(d *genai.FunctionDeclaration) *genai.FunctionDeclaration {
	encoded, _ := json.Marshal(d)
	var copy genai.FunctionDeclaration
	_ = json.Unmarshal(encoded, &copy)
	return &copy
}

func SearchDeclaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "tool_search",
		Description: "Discover additional tools by exact name or keywords (for example git, task, skill, web, MCP). Returns complete schemas as text. Call discovered tools with use_tool; discovery does not grant permission.",
		Parameters: &genai.Schema{
			Type:     genai.TypeObject,
			Required: []string{"query"},
			Properties: map[string]*genai.Schema{
				"query": {Type: genai.TypeString, Description: "Exact tool name or space-separated keywords."},
			},
		},
	}
}

func UseDeclaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "use_tool",
		Description: "Execute a tool discovered with tool_search. Put its exact name in name and its schema arguments in arguments. Normal permissions and hooks apply. Use native tools directly when available.",
		Parameters: &genai.Schema{
			Type:     genai.TypeObject,
			Required: []string{"name", "arguments"},
			Properties: map[string]*genai.Schema{
				"name":      {Type: genai.TypeString},
				"arguments": {Type: genai.TypeObject, Description: "Arguments matching the discovered tool schema."},
			},
		},
	}
}

// Publisher is embedded by clients so every SetTools path, including MCP refresh,
// uses the same profile. Client clones install their own conversation's profile.
type Publisher struct {
	mu      sync.RWMutex
	profile *Profile
}

func (p *Publisher) SetToolSchemaProfile(profile *Profile) {
	p.mu.Lock()
	p.profile = profile
	p.mu.Unlock()
}

func (p *Publisher) ToolSchemaProfile() *Profile {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.profile
}

func (p *Publisher) PublishTools(eligible []*genai.Tool) []*genai.Tool {
	return p.ToolSchemaProfile().Publish(eligible)
}

func Install(c any, profile *Profile) {
	if target, ok := c.(interface{ SetToolSchemaProfile(*Profile) }); ok {
		target.SetToolSchemaProfile(profile)
	}
}

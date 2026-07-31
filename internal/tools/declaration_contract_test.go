package tools

import (
	"encoding/json"
	"sort"
	"testing"

	"google.golang.org/genai"
)

// Lazy declarations are sent to the model before a tool is instantiated.
// Their argument contract must match the live tool exactly; otherwise a model
// can plan a call that validation later rejects (or never learn about a valid
// argument). Descriptions may legitimately be richer/dynamic, so compare only
// the machine-callable schema.
func TestLazyDeclarationsMatchLiveToolArgumentContracts(t *testing.T) {
	workDir := t.TempDir()
	liveRegistry := DefaultRegistry(workDir)
	lazyRegistry := DefaultLazyRegistry(workDir)

	if got := lazyRegistry.InstantiatedCount(); got != 0 {
		t.Fatalf("lazy registry instantiated %d tools during construction", got)
	}

	for _, lazyDeclaration := range lazyRegistry.Declarations() {
		name := lazyDeclaration.Name
		liveTool, ok := liveRegistry.Get(name)
		if !ok {
			t.Fatalf("lazy declaration %q has no live tool", name)
		}
		liveDeclaration := liveTool.Declaration()
		got := schemaContractJSON(t, liveDeclaration.Parameters)
		want := schemaContractJSON(t, lazyDeclaration.Parameters)
		if got != want {
			t.Errorf("%s declaration contract drifted\nlive: %s\nlazy: %s", name, got, want)
		}
	}

	if got := lazyRegistry.InstantiatedCount(); got != 0 {
		t.Fatalf("reading declarations instantiated %d lazy tools", got)
	}
}

type schemaContract struct {
	Type       genai.Type                `json:"type"`
	Enum       []string                  `json:"enum,omitempty"`
	Required   []string                  `json:"required,omitempty"`
	Properties map[string]schemaContract `json:"properties,omitempty"`
	Items      *schemaContract           `json:"items,omitempty"`
}

func schemaContractJSON(t *testing.T, schema *genai.Schema) string {
	t.Helper()
	contract := normalizeSchemaContract(schema)
	data, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func normalizeSchemaContract(schema *genai.Schema) schemaContract {
	if schema == nil {
		return schemaContract{}
	}
	contract := schemaContract{
		Type:     schema.Type,
		Enum:     append([]string(nil), schema.Enum...),
		Required: append([]string(nil), schema.Required...),
	}
	sort.Strings(contract.Enum)
	sort.Strings(contract.Required)
	if len(schema.Properties) > 0 {
		contract.Properties = make(map[string]schemaContract, len(schema.Properties))
		for name, property := range schema.Properties {
			contract.Properties[name] = normalizeSchemaContract(property)
		}
	}
	if schema.Items != nil {
		items := normalizeSchemaContract(schema.Items)
		contract.Items = &items
	}
	return contract
}

package client

import (
	"bytes"
	"encoding/json"
	"testing"

	"gokin/internal/toolschema"
	"google.golang.org/genai"
)

func TestDeferredClientClonesPreserveProfileWithoutSharingState(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "only-deferred", true: "native-and-deferred"}[native], func(t *testing.T) {
			c, err := NewAnthropicClient(AnthropicConfig{APIKey: "test", Provider: "glm", Model: "glm-test"})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			base := []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "skill", Description: "old catalog"}}}}
			if native {
				base[0].FunctionDeclarations = append(base[0].FunctionDeclarations, &genai.FunctionDeclaration{Name: "read", Description: "frozen"})
			}
			profile := toolschema.New(true)
			profile.Configure(base)
			toolschema.Install(c, profile)
			c.SetTools(base)
			before, _ := json.Marshal(c.tools)
			for _, clone := range []*AnthropicClient{c.WithModel("glm-other").(*AnthropicClient), c.cloneForSession().(*AnthropicClient)} {
				if clone.ToolSchemaProfile() == profile {
					t.Fatal("clone shared mutable publication")
				}
				base[0].FunctionDeclarations[0].Description = "changed catalog"
				clone.SetTools(base)
				after, _ := json.Marshal(clone.tools)
				if !bytes.Equal(before, after) {
					t.Fatal("clone lost frozen publication", string(before), string(after))
				}
				clone.ToolSchemaProfile().Configure(nil)
				clone.SetTools(c.tools)
				if len(clone.tools) != 0 {
					t.Fatal("clone revived meta-tools outside an empty ceiling")
				}
				if parent, _ := json.Marshal(c.tools); !bytes.Equal(before, parent) {
					t.Fatal("child changed parent publication")
				}
				_ = clone.Close()
			}
		})
	}
}

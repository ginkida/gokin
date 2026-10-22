package pluginpolicy

import "testing"

func TestManagedMarketplacePolicyIdentityAndBlockFloor(t *testing.T) {
	policy, err := FromDocuments([][]byte{[]byte(`{"strictKnownMarketplaces":[{"source":"github","repo":"acme/*"}],"blockedMarketplaces":[{"source":"github","repo":"acme/blocked"}],"disableCommandPluginSources":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []Source{{Kind: "github", Repo: "acme/allowed"}, {Kind: "github", Repo: "acme/allowed", Path: "custom/marketplace.json"}} {
		if err := policy.Check(source); err != nil {
			t.Fatalf("equivalent source=%+v %v", source, err)
		}
	}
	for _, source := range []Source{{Kind: "github", Repo: "other/repo"}, {Kind: "git", URL: "https://github.com/acme/blocked.git"}, {Kind: "git", URL: "git@github.com:acme/allowed.git"}, {Kind: "github", Repo: "Acme/allowed"}, {Kind: "github", Repo: "acme/allowed", Ref: "changed"}, {Kind: "skills-dir"}} {
		if err := policy.Check(source); err == nil {
			t.Fatalf("policy accepted %+v", source)
		}
	}
	if !policy.DisabledCommands {
		t.Fatal("command policy omitted")
	}
}
func TestManagedPolicyRejectsMalformedAndDuplicateControls(t *testing.T) {
	for _, data := range []string{`{"strictKnownMarketplaces":"wrong"}`, `{"blockedMarketplaces":[{"source":"hostPattern","hostPattern":"["}]}`, `{"disableCommandPluginSources":"false"}`, `{"strictKnownMarketplaces":[],"strictKnownMarketplaces":null}`} {
		if _, err := FromDocuments([][]byte{[]byte(data)}); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}

func TestManagedNullRemovalsAndImplicitOfficialMarketplace(t *testing.T) {
	policy, err := FromDocuments([][]byte{
		[]byte(`{"enabledPlugins":{"old@acme":true},"extraKnownMarketplaces":{"acme":{"source":{"source":"github","repo":"acme/plugins"}}}}`),
		[]byte(`{"enabledPlugins":null,"extraKnownMarketplaces":null}`),
		[]byte(`{"enabledPlugins":{"review@claude-plugins-official":true}}`),
	})
	if err != nil || len(policy.Enabled) != 1 || len(policy.Required) != 1 || !EqualSource(policy.Required["claude-plugins-official"], OfficialMarketplace()) {
		t.Fatalf("removed/implicit policy=%+v %v", policy, err)
	}
}

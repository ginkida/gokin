package commands

import (
	"gokin/internal/config"
	"testing"
)

func TestModelCommandHaikuUsesConfiguredGatewayWithoutSwitchingProvider(t *testing.T) {
	t.Setenv("GOKIN_SMALL_FAST_MODEL", "")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "gateway-small")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "")
	app := newModelApp("glm", config.DefaultGLMModel)
	_, err := (&ModelCommand{}).Execute(t.Context(), []string{"haiku"}, app)
	if err != nil || app.applied == nil || app.applied.Model.Name != "gateway-small" || app.applied.API.ActiveProvider != "glm" {
		t.Fatalf("haiku was not resolved on its active transport: %+v %v", app.applied, err)
	}
}

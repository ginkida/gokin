package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gokin/internal/config"
)

type failingModelApplyApp struct {
	*fakeAppForModel
}

func (a *failingModelApplyApp) ApplyConfig(*config.Config) error {
	return errors.New("config changed concurrently")
}

func TestModelCommandDoesNotMutateLiveClientBeforeConfigCommit(t *testing.T) {
	base := newModelApp("glm", "glm-5.2")
	setter := base.setter
	app := &failingModelApplyApp{fakeAppForModel: base}

	out, err := (&ModelCommand{}).Execute(context.Background(), []string{"glm-5.1"}, app)
	if err != nil {
		t.Fatalf("model command: %v", err)
	}
	if !strings.Contains(out, "Failed to save") {
		t.Fatalf("failed commit was not reported: %q", out)
	}
	if setter.model != "glm-5.2" {
		t.Fatalf("failed config commit mutated live client model to %q", setter.model)
	}
}

package chat

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestSessionMediaSurvivesSaveLoadAndRestore(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fps, tokens := 2.0, int32(128)
	content := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{
		genai.NewPartFromText("inspect the screenshot"),
		{InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte{1, 2, 3}, DisplayName: "shot.png"}, MediaResolution: &genai.PartMediaResolution{NumTokens: &tokens}},
		{FileData: &genai.FileData{MIMEType: "video/mp4", FileURI: "https://example.test/movie", DisplayName: "clip"}, VideoMetadata: &genai.VideoMetadata{StartOffset: time.Second, EndOffset: 3 * time.Second, FPS: &fps}},
		{ExecutableCode: &genai.ExecutableCode{Code: "print(1)"}},
		{CodeExecutionResult: &genai.CodeExecutionResult{Output: "1"}},
	}}
	session := NewSession()
	session.SetWorkDir(t.TempDir())
	session.AddContent(content)
	history, err := NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := history.SaveFull(session); err != nil {
		t.Fatal(err)
	}
	state, err := history.LoadFull(session.GetID())
	if err != nil {
		t.Fatal(err)
	}
	resumed := NewSession()
	if err := resumed.RestoreFromState(state); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resumed.GetHistory(), session.GetHistory()) {
		t.Fatal("session storage lost media/code payload or metadata")
	}
	snapshot := session.GetState()
	content.Parts[1].InlineData.Data[0] = 9
	content.Parts[1].MediaResolution.NumTokens = new(int32)
	fps = 9
	if snapshot.History[0].Parts[1].Structured.InlineData.Data[0] != 1 || *snapshot.History[0].Parts[2].Structured.VideoMetadata.FPS != 2 {
		t.Fatal("session snapshot retained mutable media input")
	}
}

func TestMissingStructuredPartIsRejected(t *testing.T) {
	var content SerializedContent
	if err := json.Unmarshal([]byte(`{"role":"user","parts":[{"type":"structured"}]}`), &content); err != nil {
		t.Fatal(err)
	}
	if _, err := DeserializeContent(content); err == nil {
		t.Fatal("missing media payload silently became empty text")
	}
}

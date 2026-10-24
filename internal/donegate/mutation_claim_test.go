package donegate

import (
	"reflect"
	"testing"
)

func TestCompletedEditClaimsExcludeSubsequentCitations(t *testing.T) {
	for _, text := range []string{
		"Updated m.go after reading other.go.",
		"Updated m.go after checking other.go.",
		"Updated m.go, verified against other.go.",
		"Updated m.go, leaving other.go untouched.",
	} {
		if got := ClaimedMutationPaths(text); !reflect.DeepEqual(got, []string{"m.go"}) {
			t.Errorf("cited file became a claimed edit: %q => %v", text, got)
		}
	}
}

func TestCompletedEditClaimsRound11Precision(t *testing.T) {
	for _, text := range []string{
		"Implemented `Notifier` implicitly, so the assertion at notifier_test.go:46 passes with no change to `notifier.go`.",
		"The label text is now a pinned UI contract, so any future wording change must be updated in the same commit as `auth-form.js`.",
		"**What was wrong**\n\n- The signature at internal/store/store.go is now Get(ctx, key).\n\n**Changes**\nUpdated m.go.",
		"Updated the import to `example.com/go-import-move/internal/shared` in m.go.",
	} {
		paths := ClaimedMutationPaths(text)
		for _, path := range paths {
			if path != "m.go" {
				t.Errorf("non-claim path %q in %q", path, text)
			}
		}
	}
	for _, text := range []string{
		"Updated m.go so the build passes without editing other.go.",
		"Added the comment so m.go now has the requested documentation.",
		`["**What was wrong**\n- The old signature was stale.", "Updated m.go."]`,
		"**What was wrong**\n\n- The signature was stale.\n\n**Changes**\nUpdated m.go.",
		"Updated m.go to call example.com/pkg.Helper.",
	} {
		if got := ClaimedMutationPaths(text); !reflect.DeepEqual(got, []string{"m.go"}) {
			t.Errorf("lost completed edit or gained a citation: %q => %v", text, got)
		}
	}
}

func TestRequestedEditPresentStateClaims(t *testing.T) {
	for _, text := range []string{
		"The comment `// Add adds two ints.` is in place above `Add` in m.go, but I couldn't run `go build ./...` — the command was denied by the current permission mode, so compilation is unverified.",
		"m.go now reads `// Add adds two ints.` above Add.",
		"The comment in m.go is now above Add.",
		"The comment has been added in m.go (not verified).",
		"The comment in m.go is in place, but I could not run the build.",
		"В m.go теперь есть комментарий над Add, но сборка не запускалась.",
		"Файл m.go теперь содержит комментарий над Add.",
		"Комментарий добавлен в m.go (не проверено).",
		"Комментарий в m.go на месте над Add, но сборка не запускалась.",
	} {
		if len(ClaimedMutationPaths(text)) == 0 {
			t.Errorf("missed present-state completion: %s", text)
		}
	}
	for _, text := range []string{"The comment in m.go is not in place yet.", "The comment in m.go will be in place after the edit.", "If the comment in m.go is in place, run the build.", "Комментарий в m.go не на месте.", "План: комментарий в m.go на месте после правки."} {
		if ClaimsFileMutation(text) {
			t.Errorf("false present-state completion: %s", text)
		}
	}
}

func TestMutationClaimPrecisionReview(t *testing.T) {
	for _, text := range []string{
		"The code in m.go uses a fixed-size buffer.",
		"This function runs when a file is removed.",
		"m.go already has the requested comment.",
		"The comment above Add says it adds two ints in m.go.",
		"Add is defined in m.go.",
		"Я не добавил комментарий в m.go.",
		"Комментарий в m.go не был добавлен.",
		"> Added a comment in m.go.",
		"Plan:\n- Added a comment in m.go.",
		"План:\n- Комментарий добавлен в m.go.",
	} {
		if ClaimsFileMutation(text) {
			t.Errorf("existing code or proposal treated as completed edit: %q", text)
		}
	}
	for _, text := range []string{
		"Added a comment in m.go (not verified).",
		"Updated m.go, but no build was run.",
		"Комментарий добавлен в m.go, но сборка не запускалась.",
	} {
		if !ClaimsFileMutation(text) {
			t.Errorf("verification caveat hid completed edit: %q", text)
		}
	}
}

func TestClaimedMutationPathsDistinguishesCompletionFromPlans(t *testing.T) {
	for _, text := range []string{
		"Done — m.go line 3 now has `// Add adds two ints.` above Add, confirmed via diff review.",
		"Added the one-line comment above Add in `m.go`. Build has not run.",
		"Updated `internal/math.go` to document Add; tests were not run.",
		"Files changed:\n- `m.go`\n- `main_test.go`",
		"Готово. В `m.go` добавлен комментарий над Add: `// Add adds two ints.`",
		"Я добавил комментарий в m.go. Проверка сборки не запускалась.",
		"Файл m.go обновлён: теперь Add документирован.",
	} {
		if len(ClaimedMutationPaths(text)) == 0 {
			t.Errorf("missed completed claim: %s", text)
		}
	}
	for _, text := range []string{
		"I will add a comment in m.go and run go build ./... afterward.",
		"Proposed change: added a comment to m.go in this example.",
		"No changes needed in m.go; Add already has the requested comment.",
		"I did not change m.go or run go build. My earlier completion claim was incorrect.",
		"Reviewed m.go and found no issues. The existing comment describes Add correctly.",
		"The user asked whether m.go changed. It did not.",
		"Which files changed? Is m.go modified?",
		"m.go was changed by the previous commit.",
		"```text\nAdded comment in m.go\n```\nThis is an example response.",
		"План: добавлю комментарий в m.go и запущу сборку.",
		"Изменения в m.go не нужны: комментарий уже есть.",
		"Я не изменял m.go. Ранее я ошибочно сообщил, что добавил комментарий.",
		"В m.go уже добавлен комментарий; менять файл не пришлось.",
		"Added a todo for m.go to the checklist.",
		"Добавление комментария в m.go безопасно и не требует изменения API.",
	} {
		if paths := ClaimedMutationPaths(text); len(paths) != 0 {
			t.Errorf("false completed claim: %s paths=%v", text, paths)
		}
	}
	for _, text := range []string{"Added the requested one-line comment above Add.", "Updated the implementation.", "Комментарий добавлен над Add.", "Код исправлен."} {
		if !ClaimsFileMutation(text) {
			t.Errorf("missed pathless file claim: %s", text)
		}
	}
	for _, text := range []string{"I will add the requested comment.", "No changes needed.", "Added a note to the explanation.", "Добавлю комментарий после подтверждения."} {
		if ClaimsFileMutation(text) {
			t.Errorf("false pathless file claim: %s", text)
		}
	}
}

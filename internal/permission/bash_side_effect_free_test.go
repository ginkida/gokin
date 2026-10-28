package permission

import "testing"

func TestSideEffectFreeBashCommand(t *testing.T) {
	for _, command := range []string{
		"git diff",
		"git status",
		"git status --short",
		"git log -1",
		"git show HEAD",
		"git blame state.go",
		"git rev-parse HEAD",
		"git ls-files",
		"git branch",
		"git branch -avv",
		"git branch --list 'feature/*'",
		"git --no-pager diff --stat",
		"ls",
		"ls -la",
		"cat f | wc -l",
		"grep -rn x .",
		"egrep -n x f",
		"fgrep -n x f",
		"rg -n x .",
		"rg --files",
		"rg -g '*.go' x .",
		"find . -name '*.go'",
		"find . -name '-exec' -print",
		"head -n 5 f",
		"tail -n 5 f",
		"pwd",
		"stat f",
		"file f",
		"echo 'hello world'",
		"printf '%s\\n' 'hello world'",
		"cat 'a b'",
		"cat a\\ b",
		"cat \"a b\"",
		"cat f&&ls",
		"cat f|wc -l",
		"git diff 2>/dev/null",
		"git diff 2>&1",
		"ls >/dev/null",
		"cat f 9>> '/dev/null'",
		"cat f | wc -l && git status --short",
	} {
		t.Run("allow/"+command, func(t *testing.T) {
			if !IsSideEffectFreeBashCommand(command) {
				t.Errorf("inspection rejected: %q", command)
			}
		})
	}
	for _, command := range []string{
		"",
		" ",
		"git diff --output=state.go",
		"git diff --output state.go",
		"git diff -U --output=state.go",
		"git log -n --output=state.go",
		"git diff --out=state.go",
		"git log --output=state.go",
		"git show --output-directory=out",
		"git -c core.fsmonitor=./mutate status",
		"git -ccore.fsmonitor=./mutate status",
		"git --config-env=core.fsmonitor=EVIL status",
		"git --exec-path=./evil status",
		"git -C . status",
		"git --git-dir=.git status",
		"git --work-tree=. status",
		"git diff --ext-diff",
		"git diff --textconv",
		"git show --textconv",
		"git log --show-signature",
		"git branch new",
		"git branch -l new",
		"git branch -D old",
		"git branch --edit-description",
		"git branch -- new --list",
		"git branch --format --list new",
		"git status --made-up-option",
		"git log --format='%G?'",
		"git branch --format='%(signature)'",
		"git log --pretty='%G?'",
		"rg -z x archive.gz",
		"go test ./...",
		"go build ./...",
		"go vet ./...",
		"make",
		"npm test",
		"python -c 'print(1)'",
		"printf ok\ntouch changed",
		"printf 'ok\ntouch changed'",
		"ls\r\ntouch changed",
		"ls; touch changed",
		"ls & touch changed",
		"ls |& cat",
		"ls || cat f",
		"ls | touch changed",
		"ls && touch changed",
		"find . '-exec' touch changed +",
		"find . -ex\"ec\" touch changed +",
		"fi\\nd . -e\\xec touch changed +",
		"find . -execdir touch changed +",
		"find . -ok touch changed +",
		"find . -okdir touch changed +",
		"find . -delete",
		"find . -fprint out",
		"find . -fprint0 out",
		"find . -fprintf out %p",
		"find . -fls out",
		"rg --pre ./mutate x",
		"rg '--pre' ./mutate x",
		"rg --pre-glob='*' x",
		"rg --hostname-bin ./mutate x",
		"file --compile",
		"file -C",
		"file -z archive.gz",
		"printf -v PATH ./evil",
		"printf '%n' count",
		"printf '%n' 'a[$(touch changed)]'",
		"cat f > changed",
		"cat f >> changed",
		"cat f 2> changed",
		"cat f 2>&3",
		"cat f &>/dev/null",
		"cat f < f",
		"cat f <<EOF",
		"cat f <<<x",
		"echo hi >/dev/null",
		"printf hi 2>&1",
		"cat `touch changed`",
		"cat $(touch changed)",
		"cat <(touch changed)",
		"cat >(touch changed)",
		"cat $FILE",
		"cat *.go",
		"cat ~",
		"cat 'unclosed",
		"cat \"unclosed",
		"cat f\\",
		"(ls)",
		"ls # comment",
		"ls &&",
		"| ls",
		"ls ||",
		"./ls",
		"EVIL=1 ls",
	} {
		t.Run("deny/"+command, func(t *testing.T) {
			if IsSideEffectFreeBashCommand(command) {
				t.Errorf("unsafe or unsupported command accepted: %q", command)
			}
		})
	}
}

func TestDontAskUsesSideEffectFreeBashCommands(t *testing.T) {
	manager := NewManager(DefaultRules(), true)
	manager.SetDontAsk(true)
	for _, tc := range []struct {
		command string
		allowed bool
	}{
		{"git diff", true}, {"cat f | wc -l", true}, {"grep -rn x .", true},
		{"git diff --output=state.go", false}, {"git -c core.fsmonitor=./mutate status", false},
		{"go test ./...", false}, {"printf ok\ntouch changed", false}, {"find . '-exec' touch changed +", false},
	} {
		response, err := manager.Check(t.Context(), "bash", map[string]any{"command": tc.command})
		if err != nil || response.Allowed != tc.allowed {
			t.Errorf("%q: response=%+v err=%v", tc.command, response, err)
		}
	}
}

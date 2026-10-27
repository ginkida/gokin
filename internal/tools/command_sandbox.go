package tools

import (
	"context"
	"os/exec"

	"gokin/internal/security"
)

type sandboxPolicySetter interface{ SetSandboxPolicy(*security.SandboxPolicy) }

func SetSandboxPolicyOnRegistry(reg interface{ List() []Tool }, policy *security.SandboxPolicy) {
	for _, tool := range reg.List() {
		if setter, ok := tool.(sandboxPolicySetter); ok {
			setter.SetSandboxPolicy(policy)
		}
	}
}

func (t *BashTool) SetSandboxPolicy(policy *security.SandboxPolicy) {
	t.policyMu.Lock()
	defer t.policyMu.Unlock()
	t.sandboxPolicy = policy
	t.sandboxEnabled = policy.Enabled()
	t.policyRevision++
	t.clampSandboxCwdLocked()
}

func (t *BashTool) SetAllowedDirs(dirs []string) {
	t.policyMu.Lock()
	defer t.policyMu.Unlock()
	t.sandboxDirs = append([]string(nil), dirs...)
	t.policyRevision++
	t.clampSandboxCwdLocked()
}

// A cwd retained from unrestricted execution or a revoked grant must not make
// the next sandbox launch fail before even `cd` can run.
func (t *BashTool) clampSandboxCwdLocked() {
	if t.session == nil || !t.sandboxPolicy.Enabled() {
		return
	}
	allowed := append([]string{t.workDir}, t.sandboxDirs...)
	if ok, err := security.IsPathWithinAny(t.session.WorkDir(), allowed); err != nil || !ok {
		t.session.SetWorkDir(t.workDir)
		t.policyRevision++
	}
}

func (t *BashTool) SandboxStatus(ctx context.Context) security.SandboxStatus {
	t.policyMu.RLock()
	p := t.sandboxPolicy
	t.policyMu.RUnlock()
	return p.Status(ctx)
}

func (t *RunTestsTool) SetSandboxPolicy(policy *security.SandboxPolicy)   { t.sandboxPolicy = policy }
func (t *VerifyCodeTool) SetSandboxPolicy(policy *security.SandboxPolicy) { t.sandboxPolicy = policy }

func prepareProjectCommand(ctx context.Context, cmd *exec.Cmd, policy *security.SandboxPolicy, root string, validator *security.PathValidator) (func(), error) {
	scope := security.CommandScope{WorkDir: root}
	if validator != nil {
		scope.AllowedDirs = validator.AllowedDirs()
	}
	return policy.Prepare(ctx, cmd, scope)
}

// PrepareVerificationCommand uses the registry's bash policy even when the
// caller invokes a compiler directly (delta checks and automatic formatters).
func PrepareVerificationCommand(ctx context.Context, cmd *exec.Cmd, reg ToolRegistry, root string, policy *security.SandboxPolicy) (func(), error) {
	scope := security.CommandScope{WorkDir: root}
	if reg != nil {
		if tool, ok := reg.Get("bash"); ok {
			if bash, ok := tool.(*BashTool); ok {
				bash.policyMu.RLock()
				if policy == nil {
					policy = bash.sandboxPolicy.Snapshot()
				}
				scope.AllowedDirs = append([]string(nil), bash.sandboxDirs...)
				bash.policyMu.RUnlock()
				return policy.Prepare(ctx, cmd, scope)
			}
		}
		for _, name := range []string{"run_tests", "verify_code"} {
			if tool, ok := reg.Get(name); ok {
				switch tool := tool.(type) {
				case *RunTestsTool:
					if policy == nil {
						policy = tool.sandboxPolicy
					}
					scope.AllowedDirs = tool.pathValidator.AllowedDirs()
				case *VerifyCodeTool:
					if policy == nil {
						policy = tool.sandboxPolicy
					}
					scope.AllowedDirs = tool.pathValidator.AllowedDirs()
				}
			}
		}
	}
	if policy == nil {
		policy = security.NewSandboxPolicy(root, security.DefaultSandboxConfig())
	}
	return policy.Prepare(ctx, cmd, scope)
}

func (e *Executor) SetSandboxPolicy(policy *security.SandboxPolicy) { e.sandboxPolicy = policy }

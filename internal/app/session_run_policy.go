package app

import (
	"errors"
	"fmt"
)

var ErrSessionRunPolicySwitch = errors.New("session policy requires a restart")

func sessionRunPolicySwitchError(id string) error {
	return fmt.Errorf("%w: runtime switching cannot safely replace run authority; restart with gokin --resume %s (use --reset-session-policy only to explicitly widen)", ErrSessionRunPolicySwitch, id)
}

// PersistRunPolicy flushes startup authority before any provider/tool call.
func (a *App) PersistRunPolicy() error {
	if a.sessionManager == nil {
		return fmt.Errorf("cannot persist run policy without a session manager")
	}
	return a.sessionManager.Save()
}

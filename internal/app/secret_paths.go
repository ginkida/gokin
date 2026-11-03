package app

import "gokin/internal/tools"

// Credential exceptions are host configuration, never directory-grant or tool
// arguments. Bootstrap after Build so both bare and normal registries exist,
// without coupling the builder's individual tool constructors to this policy.
func (a *App) configureSecretPaths() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.configureSecretPathsLocked()
}

func (a *App) configureSecretPathsLocked() {
	dirs := append([]string(nil), a.config.Tools.AllowedDirs...)
	a.grantedDirsMu.Lock()
	dirs = append(dirs, a.grantedDirs...)
	a.grantedDirsMu.Unlock()
	if a.registry != nil {
		tools.SetAllowedDirsOnRegistry(a.registry, dirs, a.config.Tools.AllowSecretPaths)
	}
	if a.deferredHybrid != nil {
		a.deferredHybrid.SetAllowSecretPaths(a.config.Tools.AllowSecretPaths)
	}
	if scoped, ok := a.replManager.(interface{ SetAllowSecretPaths([]string) }); ok {
		scoped.SetAllowSecretPaths(a.config.Tools.AllowSecretPaths)
	}
}

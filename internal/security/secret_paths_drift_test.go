package security_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Each enforcement consumer must call the shared inventory (directly or via
// its predicate), rather than growing another partial credential list.
func TestSecretPathsConsumersShareInventory(t *testing.T) {
	for _, tc := range []struct{ path, function, helper string }{
		{"bash_policy.go", "sandboxSecrets", "SecretPaths"},
		{"path_validator.go", "IsGrantableDir", "IsSecretPath"},
		{"../tools/ssh.go", "validateSSHLocalPath", "IsSecretPath"},
	} {
		t.Run(tc.function, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), tc.path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name != tc.function {
					continue
				}
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch fun := call.Fun.(type) {
					case *ast.Ident:
						found = found || fun.Name == tc.helper
					case *ast.SelectorExpr:
						found = found || fun.Sel.Name == tc.helper
					}
					return true
				})
			}
			if !found {
				t.Fatalf("%s must use %s", tc.function, tc.helper)
			}
		})
	}
}

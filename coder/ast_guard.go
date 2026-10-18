package coder

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// AllowedImports defines the strict whitelist of standard library packages permitted in generated Wasm runbooks.
var AllowedImports = map[string]bool{
	"fmt":     true,
	"os":      true,
	"unsafe":  true,
	"strings": true,
}

// ForbiddenPackages are explicitly blocked packages that pose security risks in untrusted code.
var ForbiddenPackages = map[string]string{
	"os/exec":   "direct process execution is prohibited; use env.kubectl_exec host call",
	"net":       "network sockets are prohibited inside runbooks",
	"net/http":  "HTTP clients are prohibited inside runbooks",
	"syscall":   "low-level syscalls are prohibited",
	"plugin":    "dynamic plugin loading is prohibited",
	"runtime":   "runtime introspection/modification is prohibited",
	"io/ioutil": "deprecated; use authorized packages",
}

// ValidateSourceAST performs AST-level static security analysis on LLM-generated Go code before compilation.
func ValidateSourceAST(sourceCode string) error {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "runbook.go", sourceCode, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("syntax validation failed: %w", err)
	}

	// 1. Package must be "main"
	if node.Name == nil || node.Name.Name != "main" {
		return fmt.Errorf("package must be 'main', found: %s", node.Name.Name)
	}

	// 2. Validate imports against strict whitelist
	hasWasmImportComment := false
	for _, imp := range node.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		if reason, blocked := ForbiddenPackages[importPath]; blocked {
			return fmt.Errorf("forbidden package %q: %s", importPath, reason)
		}
		if !AllowedImports[importPath] {
			return fmt.Errorf("unauthorized package import: %q (only %v allowed)", importPath, getSortedAllowed())
		}
	}

	// 3. Validate Functions & Host Import Bindings
	hasMain := false
	for _, decl := range node.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}

		if fn.Name.Name == "main" {
			hasMain = true
			if fn.Body == nil {
				return fmt.Errorf("main function cannot be empty or external")
			}
		}

		// Check for external functions (without body)
		if fn.Body == nil {
			// External functions MUST be annotated with //go:wasmimport env kubectl_exec
			validPragma := false
			if fn.Doc != nil {
				for _, comment := range fn.Doc.List {
					if strings.Contains(comment.Text, "go:wasmimport") {
						parts := strings.Fields(comment.Text)
						// Expected format: //go:wasmimport env kubectl_exec
						if len(parts) >= 3 && parts[1] == "env" && parts[2] == "kubectl_exec" {
							validPragma = true
							hasWasmImportComment = true
						} else {
							return fmt.Errorf("unauthorized wasmimport directive: %s", comment.Text)
						}
					}
				}
			}
			if !validPragma {
				return fmt.Errorf("external function %s must be annotated with '//go:wasmimport env kubectl_exec'", fn.Name.Name)
			}
		}
	}

	if !hasMain {
		return fmt.Errorf("missing required 'func main()' entry point")
	}

	if !hasWasmImportComment {
		return fmt.Errorf("runbook must declare the '//go:wasmimport env kubectl_exec' host function")
	}

	return nil
}

func getSortedAllowed() []string {
	var list []string
	for k := range AllowedImports {
		list = append(list, k)
	}
	return list
}

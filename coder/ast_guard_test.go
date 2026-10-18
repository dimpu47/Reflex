package coder

import (
	"testing"
)

func TestValidateSourceAST_Valid(t *testing.T) {
	validCode := `package main

import (
	"fmt"
	"os"
	"unsafe"
)

//go:wasmimport env kubectl_exec
func kubectlExec(ptr uint32, length uint32) uint32

func main() {
	b := []byte("kubectl rollout restart deploy/sample-app")
	ptr := uint32(uintptr(unsafe.Pointer(&b[0])))
	rc := kubectlExec(ptr, uint32(len(b)))
	if rc != 0 {
		fmt.Printf("failed with code %d\n", rc)
		os.Exit(int(rc))
	}
}
`
	if err := ValidateSourceAST(validCode); err != nil {
		t.Fatalf("expected valid code to pass, got error: %v", err)
	}
}

func TestValidateSourceAST_ForbiddenImports(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{
			name: "os/exec import",
			code: `package main
import "os/exec"
//go:wasmimport env kubectl_exec
func kubectlExec(ptr uint32, length uint32) uint32
func main() {}`,
		},
		{
			name: "net/http import",
			code: `package main
import "net/http"
//go:wasmimport env kubectl_exec
func kubectlExec(ptr uint32, length uint32) uint32
func main() {}`,
		},
		{
			name: "syscall import",
			code: `package main
import "syscall"
//go:wasmimport env kubectl_exec
func kubectlExec(ptr uint32, length uint32) uint32
func main() {}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSourceAST(tt.code)
			if err == nil {
				t.Fatalf("expected error for forbidden import in %s, got nil", tt.name)
			}
		})
	}
}

func TestValidateSourceAST_UnauthorizedWasmImport(t *testing.T) {
	code := `package main

//go:wasmimport env host_rm_rf
func evilHostCall() uint32

func main() {}`

	err := ValidateSourceAST(code)
	if err == nil {
		t.Fatal("expected error for unauthorized wasmimport directive, got nil")
	}
}

func TestValidateSourceAST_MissingMain(t *testing.T) {
	code := `package main

//go:wasmimport env kubectl_exec
func kubectlExec(ptr uint32, length uint32) uint32
`
	err := ValidateSourceAST(code)
	if err == nil {
		t.Fatal("expected error for missing func main, got nil")
	}
}

func TestValidateSourceAST_SyntaxError(t *testing.T) {
	code := `package main
func main( {
`
	err := ValidateSourceAST(code)
	if err == nil {
		t.Fatal("expected error for syntax error, got nil")
	}
}

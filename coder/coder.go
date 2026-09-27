package coder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// GenerateAndCompile Wasm Runbook from LLM
func GenerateAndCompile(ctx context.Context, domain string, alertState string) (string, error) {
	fmt.Printf("🧑‍💻 [Coder Agent] Generating Go Wasm Runbook for domain: %s...\n", domain)

	prompt := fmt.Sprintf(`You are an autonomous SRE agent writing a Go runbook that will be compiled to WebAssembly (GOOS=wasip1) and executed inside a sandbox.

Remediate this alert state:
%s

The ONLY capability granted to your runbook is a host function. You MUST declare it exactly like this, including the //go:wasmimport comment, the underscore in the import name, and the module name "env":

//go:wasmimport env kubectl_exec
func kubectlExec(ptr uint32, length uint32) uint32

Without that exact comment the module will fail to link. Do not rename it, do not drop the comment, and do not import os/exec or net.

To pass a command to the host, copy the string into linear memory and hand over the pointer and length:

b := []byte("kubectl rollout restart deploy/my-service")
ptr := uint32(uintptr(unsafe.Pointer(&b[0])))
rc := kubectlExec(ptr, uint32(len(b)))

Requirements:
- Start the file with "package main" and a "//go:build wasip1" constraint.
- Import only "fmt", "os" and "unsafe".
- Derive the kubectl target from the alert state above.
- If the host returns a non-zero code, print the code and call os.Exit with it so the engine triggers a rollback.
- Emit a complete, compilable Go main package.

Output ONLY raw Go source. No markdown fences, no commentary.`, alertState)

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":  "qwen2.5-coder:7b", // Fast local coder model
		"prompt": prompt,
		"stream": false,
	})

	ollamaHost := os.Getenv("OLLAMA_HOST")
	if ollamaHost == "" {
		ollamaHost = "http://localhost:11434"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ollamaHost+"/api/generate", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")
	
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %v", err)
	}
	defer resp.Body.Close()

	var llmResp struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&llmResp); err != nil {
		return "", err
	}

	rawGoCode := strings.TrimSpace(llmResp.Response)
	// Strip markdown if the LLM leaked it
	rawGoCode = strings.TrimPrefix(rawGoCode, "```go")
	rawGoCode = strings.TrimPrefix(rawGoCode, "```")
	rawGoCode = strings.TrimSuffix(rawGoCode, "```")
	rawGoCode = strings.TrimSpace(rawGoCode)

	if !strings.HasPrefix(rawGoCode, "package main") {
		return "", fmt.Errorf("LLM did not return a valid Go main package")
	}

	// 1. Write the Go file to disk
	tmpDir, err := os.MkdirTemp("", "reflex-coder-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	goFilePath := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(goFilePath, []byte(rawGoCode), 0600); err != nil {
		return "", err
	}

	// 2. Compile to WASM using the wasip1 target
	wasmFileName := fmt.Sprintf("%s_%d.wasm", nonAlnum.ReplaceAllString(domain, "-"), time.Now().Unix())
	wasmFilePath := filepath.Join("scripts", wasmFileName) // scripts directory is served as the registry
	
	// Ensure scripts dir exists
	if err := os.MkdirAll("scripts", 0755); err != nil {
		return "", err
	}

	fmt.Printf("🔨 [JIT Compiler] Compiling %s to %s (GOOS=wasip1 GOARCH=wasm)...\n", goFilePath, wasmFilePath)
	
	cmd := exec.Command("go", "build", "-o", wasmFilePath, goFilePath)
	// Set environment variables for the compilation
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(wasmFilePath) // don't leave a truncated artifact in the registry
		return "", fmt.Errorf("WASM compilation failed: %v\nOutput: %s", err, string(out))
	}

	// 3. Sanity-check the artifact before handing it to the engine. A
	// zero-length or non-wasm file would otherwise be served from the
	// registry and fail confusingly at execution time.
	info, err := os.Stat(wasmFilePath)
	if err != nil || info.Size() < 8 {
		return "", fmt.Errorf("JIT compiler produced no usable artifact")
	}
	f, err := os.Open(wasmFilePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != "\x00asm" {
		os.Remove(wasmFilePath)
		return "", fmt.Errorf("JIT compiler output is not a valid Wasm module")
	}

	fmt.Printf("✅ [JIT Compiler] Compiled and verified %s (%d bytes)\n", wasmFileName, info.Size())
	return wasmFilePath, nil
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9._-]+`)

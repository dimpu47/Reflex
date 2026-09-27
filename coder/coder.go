package coder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GenerateAndCompile Wasm Runbook from LLM
func GenerateAndCompile(ctx context.Context, domain string, alertState string) (string, error) {
	fmt.Printf("🧑‍💻 [Coder Agent] Generating Go Wasm Runbook for domain: %s...\n", domain)

	prompt := fmt.Sprintf(`You are an autonomous SRE agent writing a Go runbook to compile to WebAssembly (wasi).
The runbook is meant to remediate the following alert state:
%s

You MUST use the exported host function:
//go:wasmimport env kubectl_exec
func kubectlExec(ptr uint32, len uint32) uint32

Write a complete Go main package that determines the exact kubectl command to run based on the context, calls kubectlExec, and exits. 
Only output valid Go code. Do not output markdown code blocks. Start directly with "package main".`, alertState)

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
	// Note: In production we would clean this up, but for testing we leave it so the engine can read it if needed.
	
	goFilePath := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(goFilePath, []byte(rawGoCode), 0644); err != nil {
		return "", err
	}

	// 2. Compile to WASM using Go 1.21+ wasip1 target
	wasmFileName := fmt.Sprintf("%s_%d.wasm", domain, time.Now().Unix())
	wasmFilePath := filepath.Join("scripts", wasmFileName) // scripts directory is served as the registry
	
	// Ensure scripts dir exists
	os.MkdirAll("scripts", 0755)

	fmt.Printf("🔨 [JIT Compiler] Compiling %s to %s (GOOS=wasip1 GOARCH=wasm)...\n", goFilePath, wasmFilePath)
	
	cmd := exec.Command("go", "build", "-o", wasmFilePath, goFilePath)
	// Set environment variables for the compilation
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("WASM compilation failed: %v\nOutput: %s", err, string(out))
	}

	fmt.Printf("✅ [JIT Compiler] Successfully compiled %s\n", wasmFileName)
	return wasmFilePath, nil
}

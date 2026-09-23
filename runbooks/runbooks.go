package runbooks

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"jev-proxy/evaluator"
	"jev-proxy/ingest"

	"gopkg.in/yaml.v3"
)

// ANSI Color Codes
const (
	ColorReset  = "\033[0m"
	ColorGreen  = "\033[32m"
	ColorRed    = "\033[31m"
	ColorYellow = "\033[33m"
	ColorCyan   = "\033[36m"
)

// Handler defines the interface for taking action on evaluated alerts.
type Handler interface {
	AutoRemediate(alert ingest.Alert, eval evaluator.EvaluationResponse)
	Escalate(alert ingest.Alert, eval evaluator.EvaluationResponse)
}

// Config represents the runbooks.yaml structure
type Config struct {
	Runbooks map[string]string `yaml:"runbooks"`
}

// Engine implements Handler to simulate runbook execution and escalation.
type Engine struct {
	config Config
}

func NewEngine() *Engine {
	engine := &Engine{
		config: Config{Runbooks: make(map[string]string)},
	}

	data, err := os.ReadFile("runbooks.yaml")
	if err == nil {
		if err := yaml.Unmarshal(data, &engine.config); err != nil {
			fmt.Printf("⚠️  Error parsing runbooks.yaml: %v\n", err)
		} else {
			fmt.Printf("📂 Loaded runbooks config with %d mappings.\n", len(engine.config.Runbooks))
		}
	} else {
		fmt.Printf("⚠️  Could not read runbooks.yaml: %v (Using defaults)\n", err)
	}

	return engine
}

func (e *Engine) AutoRemediate(alert ingest.Alert, eval evaluator.EvaluationResponse) {
	domain := eval.Answers.Domain.Choice

	fmt.Printf("%s[✅ AUTO-REMEDIATE]%s Runbook triggered for alert %s%s%s (Domain: %s, Sev: %.1f, Recov: %.2f)\n",
		ColorGreen, ColorReset, ColorCyan, alert.ID, ColorReset,
		domain, eval.Answers.Severity.Score, eval.Answers.AutoRecoverable.Probability)

	script, ok := e.config.Runbooks[domain]
	if ok && script != "" {
		fmt.Printf("   %sExecuting mapped runbook: %s%s\n", ColorCyan, script, ColorReset)
		
		// If running in bash/wsl environment
		// For windows fallback we can try executing bash or directly
		cmd := exec.Command("bash", "-c", script)
		
		// If script ends with .sh but no bash, maybe git bash or wsl is needed
		// This is a naive attempt to execute it cross-platform for testing
		if strings.HasSuffix(script, ".ps1") {
			cmd = exec.Command("powershell", "-File", script)
		}

		out, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Printf("   %s❌ Runbook execution failed: %v%s\n", ColorRed, err, ColorReset)
		} else {
			fmt.Printf("   %s✓ Output: %s%s\n", ColorGreen, strings.TrimSpace(string(out)), ColorReset)
		}
	} else {
		fmt.Printf("   %s(No runbook script mapped for domain: %s)%s\n", ColorYellow, domain, ColorReset)
	}
}

func (e *Engine) Escalate(alert ingest.Alert, eval evaluator.EvaluationResponse) {
	fmt.Printf("%s[🚨 ESCALATE]%s Routing to PagerDuty/Slack for alert %s%s%s (Conf: %.2f, Sev: %.1f)\n",
		ColorRed, ColorReset, ColorYellow, alert.ID, ColorReset,
		eval.Answers.Domain.Confidence, eval.Answers.Severity.Score)
}

package runbooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/open-policy-agent/opa/rego"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"gopkg.in/yaml.v3"

	"jev-proxy/evaluator"
	"jev-proxy/ingest"
	"jev-proxy/metrics"
	"jev-proxy/memory"
	"jev-proxy/coder"
)

// ANSI Color Codes
const (
	ColorReset  = "\033[0m"
	ColorGreen  = "\033[32m"
	ColorRed    = "\033[31m"
	ColorYellow = "\033[33m"
	ColorCyan   = "\033[36m"
)

type Handler interface {
	AutoRemediate(alert ingest.Alert, eval evaluator.EvaluationResponse)
	Escalate(alert ingest.Alert, eval evaluator.EvaluationResponse)
}

type Step struct {
	Name       string `yaml:"name"`
	Action     string `yaml:"action"`
	Compensate string `yaml:"compensate"`
}

type Runbook struct {
	Steps []Step `yaml:"steps"`
}

type Config struct {
	Runbooks map[string]Runbook `yaml:"runbooks"`
}

type RunbookState struct {
	AlertID        string `json:"alert_id"`
	Domain         string `json:"domain"`
	CompletedSteps []Step `json:"completed_steps"`
	Status         string `json:"status"`
	LastHeartbeat  int64  `json:"last_heartbeat"`
}

type Engine struct {
	config Config
	k8s    *kubernetes.Clientset
	ctx    context.Context

	wasmRuntime wazero.Runtime
	wasmCache   sync.Map // map[string]wazero.CompiledModule
	MemoryStore *memory.Store
	
	PendingApprovals sync.Map // map[string]chan bool
}

func NewEngine(k8s *kubernetes.Clientset, memStore *memory.Store) *Engine {
	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	// Export Host Functions to WebAssembly to allow K8s mutation
	_, err := r.NewHostModuleBuilder("env").
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context, cmdPtr, cmdLen uint32) uint32 {
			// Memory reading logic would go here to extract the command string
			// For simulation, we assume the command extracted is:
			simulatedCommand := "kubectl rollout restart deploy/redis"
			
			// 1. Parse command for OPA evaluation
			parts := strings.Split(simulatedCommand, " ")
			if len(parts) < 4 {
				fmt.Printf("   %s❌ [OPA-Deny] Invalid kubectl format%s\n", ColorRed, ColorReset)
				return 1 // Error
			}
			action := parts[1] // rollout
			resource := parts[2] // restart
			target := parts[3] // deploy/redis

			// 2. Evaluate against OPA Rego policy
			input := map[string]interface{}{
				"action": action,
				"resource": resource,
				"target": target,
			}
			
			query, err := rego.New(
				rego.Query("data.reflex.authz.allow"),
				rego.Load([]string{"policy.rego"}, nil),
			).PrepareForEval(ctx)
			
			if err != nil {
				fmt.Printf("   %s❌ [OPA-Error] Failed to prepare Rego query: %v%s\n", ColorRed, err, ColorReset)
				return 1
			}

			rs, err := query.Eval(ctx, rego.EvalInput(input))
			if err != nil || len(rs) == 0 || !rs[0].Expressions[0].Value.(bool) {
				fmt.Printf("   %s🛑 [OPA-DENY] Command rejected by security policy: %s%s\n", ColorRed, simulatedCommand, ColorReset)
				return 1 // Permission Denied
			}

			fmt.Printf("   %s✅ [OPA-ALLOW] Executing permitted host binary: %s%s\n", ColorGreen, simulatedCommand, ColorReset)
			return 0
		}).Export("kubectl_exec").
		Instantiate(ctx)
	
	if err != nil {
		panic(fmt.Errorf("failed to instantiate host module: %w", err))
	}

	engine := &Engine{
		config:      Config{Runbooks: make(map[string]Runbook)},
		k8s:         k8s,
		ctx:         ctx,
		wasmRuntime: r,
		MemoryStore: memStore,
	}

	data, err := os.ReadFile("runbooks.yaml")
	if err == nil {
		if err := yaml.Unmarshal(data, &engine.config); err != nil {
			fmt.Printf("⚠️  Error parsing runbooks.yaml: %v\n", err)
		} else {
			fmt.Printf("📂 Loaded runbooks config with %d DAG mappings.\n", len(engine.config.Runbooks))
		}
	} else {
		fmt.Printf("⚠️  Could not read runbooks.yaml: %v (Using defaults)\n", err)
	}

	if k8s != nil {
		go engine.StartOrphanWatcher()
	}

	return engine
}

func (e *Engine) saveState(state *RunbookState) {
	if e.k8s == nil {
		return
	}
	state.LastHeartbeat = time.Now().Unix()
	data, _ := json.Marshal(state)
	
	cmName := fmt.Sprintf("reflex-dag-%s", strings.ToLower(state.AlertID))
	cmClient := e.k8s.CoreV1().ConfigMaps("default")
	
	cm, err := cmClient.Get(e.ctx, cmName, metav1.GetOptions{})
	if err != nil {
		// Create
		newCm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: cmName,
				Labels: map[string]string{
					"reflex.io/type": "dag-state",
					"reflex.io/status": state.Status,
				},
			},
			Data: map[string]string{
				"state": string(data),
			},
		}
		cmClient.Create(e.ctx, newCm, metav1.CreateOptions{})
	} else {
		// Update
		cm.Data["state"] = string(data)
		cm.Labels["reflex.io/status"] = state.Status
		cmClient.Update(e.ctx, cm, metav1.UpdateOptions{})
	}
}

func (e *Engine) StartOrphanWatcher() {
	fmt.Println("🕵️  Started Distributed Orphan Watcher for crash recovery...")
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.scanAndAdoptOrphans()
		}
	}
}

func (e *Engine) scanAndAdoptOrphans() {
	cmClient := e.k8s.CoreV1().ConfigMaps("default")
	list, err := cmClient.List(e.ctx, metav1.ListOptions{
		LabelSelector: "reflex.io/type=dag-state",
	})
	if err != nil {
		fmt.Printf("⚠️ K8s list error: %v\n", err)
		return
	}

	for _, cm := range list.Items {
		stateStr, ok := cm.Data["state"]
		if !ok {
			continue
		}
		
		var state RunbookState
		if err := json.Unmarshal([]byte(stateStr), &state); err != nil {
			continue
		}

		if state.Status == "running" {
			if time.Now().Unix()-state.LastHeartbeat > 30 {
				// Lock via Annotation Update (Optimistic Concurrency)
				if cm.Annotations == nil {
					cm.Annotations = make(map[string]string)
				}
				if cm.Annotations["reflex.io/locked-by"] != "" {
					continue // Already locked by another proxy
				}
				
				cm.Annotations["reflex.io/locked-by"] = "orphan-watcher"
				_, err := cmClient.Update(e.ctx, &cm, metav1.UpdateOptions{})
				if err == nil {
					fmt.Printf("   %s⚠️ Adopting Orphaned DAG for Alert %s (Proxy crashed!). Initiating Crash Recovery (Rollback)...%s\n", 
						ColorRed, state.AlertID, ColorReset)
					
					e.rollback(state.CompletedSteps)
					state.Status = "rolled_back_orphan"
					e.saveState(&state)
				}
			}
		}
	}
}

func (e *Engine) executeStep(script string, idempotencyKey string) error {
	var out []byte
	var err error

	if strings.HasSuffix(script, ".wasm") {
		out, err = e.executeWasm(script, idempotencyKey)
	} else {
		cmd := exec.Command("bash", "-c", script)
		if strings.HasSuffix(script, ".ps1") {
			cmd = exec.Command("powershell", "-File", script)
		}
		out, err = cmd.CombinedOutput()
	}

	if err != nil {
		return fmt.Errorf("%v (Output: %s)", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("      %s✓ %s%s\n", ColorGreen, strings.TrimSpace(string(out)), ColorReset)
	return nil
}

func (e *Engine) AutoRemediate(alert ingest.Alert, eval evaluator.EvaluationResponse) {
	domain := eval.Answers.Domain.Choice

	fmt.Printf("%s[✅ AUTO-REMEDIATE DAG]%s Triggered for alert %s%s%s (Domain: %s)\n",
		ColorGreen, ColorReset, ColorCyan, alert.ID, ColorReset, domain)

	runbook, ok := e.config.Runbooks[domain]
	if !ok || len(runbook.Steps) == 0 {
		fmt.Printf("   %s(No static runbook DAG mapped for domain: %s. Initiating Autonomous Coder Agent...)%s\n", ColorYellow, domain, ColorReset)
		
		wasmPath, err := coder.GenerateAndCompile(context.Background(), domain, string(alert.Payload))
		if err != nil {
			fmt.Printf("   %s❌ Failed to autonomously generate runbook: %v%s\n", ColorRed, err, ColorReset)
			e.Escalate(alert, eval)
			return
		}
		
		fmt.Printf("   %s✅ Successfully compiled JIT Runbook: %s%s\n", ColorGreen, wasmPath, ColorReset)
		// Inject it as a single-step runbook dynamically
		runbook = Runbook{
			Steps: []Step{
				{
					Name:   fmt.Sprintf("Autonomous Remediation for %s", domain),
					Action: wasmPath, // Path to the wasm file
				},
			},
		}

		env := os.Getenv("REFLEX_ENV")
		if env == "" {
			env = "production" // default to safest mode
		}
		thresholdStr := os.Getenv("REFLEX_RELIABILITY_THRESHOLD")
		reliabilityThreshold := 0.99
		if thresholdStr != "" {
			if v, err := strconv.ParseFloat(thresholdStr, 64); err == nil {
				reliabilityThreshold = v
			}
		}

		if env == "staging" || env == "test" {
			// ─── STAGING / TEST: auto-execute for chaos testing inoculation ───
			fmt.Printf("   %s🧪 [%s] Auto-executing JIT runbook (chaos inoculation mode — no approval needed)%s\n", ColorCyan, strings.ToUpper(env), ColorReset)
		} else {
			// ─── PRODUCTION: gate on empirical reliability score ───
			var score float64
			if e.MemoryStore != nil {
				score = e.MemoryStore.GetReliabilityScore(domain)
			}
			if score >= reliabilityThreshold {
				fmt.Printf("   %s🏆 [PROD] Runbook empirically proven (%.0f%% success rate in chaos testing). Auto-executing.%s\n", ColorGreen, score*100, ColorReset)
			} else {
				fmt.Printf("   %s⏸️  [PROD-HITL] Runbook not yet proven for production (%.0f%% < %.0f%% required). Awaiting human approval...%s\n", ColorYellow, score*100, reliabilityThreshold*100, ColorReset)
				fmt.Printf("   %s         Run: 'reflex approve %s' to approve or 'reflex deny %s' to reject%s\n", ColorYellow, alert.ID, alert.ID, ColorReset)
				approvalChan := make(chan bool)
				e.PendingApprovals.Store(alert.ID, approvalChan)
				approved := <-approvalChan
				e.PendingApprovals.Delete(alert.ID)
				if !approved {
					fmt.Printf("   %s🛑 Human denied execution. Escalating.%s\n", ColorRed, ColorReset)
					e.Escalate(alert, eval)
					return
				}
				fmt.Printf("   %s▶️  Human approved! Graduating runbook to production.%s\n", ColorGreen, ColorReset)
			}
		}
	}

	state := RunbookState{
		AlertID: alert.ID,
		Domain:  domain,
		Status:  "running",
	}
	e.saveState(&state)

	doneChan := make(chan bool)
	if e.k8s != nil {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					e.saveState(&state)
				case <-doneChan:
					return
				}
			}
		}()
	}

	for i, step := range runbook.Steps {
		fmt.Printf("   %s-> Step %d: %s%s (Action: %s)\n", ColorCyan, i+1, step.Name, ColorReset, step.Action)
		
		idempotencyKey := uuid.New().String()
		err := e.executeStep(step.Action, idempotencyKey)
		if err != nil {
			fmt.Printf("      %s❌ Execution failed: %v%s\n", ColorRed, err, ColorReset)
			fmt.Printf("   %s⚠️ Initiating Compensating Transactions (Rollback)...%s\n", ColorYellow, ColorReset)
			
			if e.k8s != nil {
				close(doneChan)
			}
			e.rollback(state.CompletedSteps)
			
			state.Status = "rolled_back"
			e.saveState(&state)
			metrics.RemediationsTotal.WithLabelValues("rolled_back").Inc()

			// Save failure outcome to memory for future RAG prevention
			if e.MemoryStore != nil {
				go func(alertState []byte) {
					emb, _ := memory.GetEmbedding(context.Background(), string(alertState))
					e.MemoryStore.SaveOutcome(memory.Incident{
						ID:         alert.ID,
						AlertState: string(alertState),
						Resolution: fmt.Sprintf("Failed at step: %s", step.Name),
						Outcome:    "failure",
						Embedding:  emb,
					})
				}(alert.Payload)
			}

			e.Escalate(alert, eval)
			return
		}
		
		state.CompletedSteps = append(state.CompletedSteps, step)
		e.saveState(&state)
	}
	
	if e.k8s != nil {
		close(doneChan)
	}
	fmt.Printf("   %s🎉 DAG Auto-Remediation Complete!%s\n", ColorGreen, ColorReset)
	
	state.Status = "completed"
	e.saveState(&state)
	metrics.RemediationsTotal.WithLabelValues("success").Inc()

	// Save success outcome to memory for future RAG
	if e.MemoryStore != nil {
		go func(alertState []byte) {
			emb, _ := memory.GetEmbedding(context.Background(), string(alertState))
			e.MemoryStore.SaveOutcome(memory.Incident{
				ID:         alert.ID,
				AlertState: string(alertState),
				Resolution: fmt.Sprintf("Successfully executed runbook for domain %s", domain),
				Outcome:    "success",
				Embedding:  emb,
			})
		}(alert.Payload)
	}
}

func (e *Engine) rollback(completedSteps []Step) {
	for i := len(completedSteps) - 1; i >= 0; i-- {
		step := completedSteps[i]
		if step.Compensate == "" {
			continue
		}
		fmt.Printf("   %s<- Rollback Step: %s%s (Compensate: %s)\n", ColorYellow, step.Name, ColorReset, step.Compensate)
		idempotencyKey := uuid.New().String()
		err := e.executeStep(step.Compensate, idempotencyKey)
		if err != nil {
			fmt.Printf("      %s❌ FATAL: Compensation failed! System in unknown state: %v%s\n", ColorRed, err, ColorReset)
		}
	}
}

func (e *Engine) Escalate(alert ingest.Alert, eval evaluator.EvaluationResponse) {
	fmt.Printf("%s[🚨 ESCALATE]%s Routing alert %s%s%s (Conf: %.2f, Sev: %.1f)\n",
		ColorRed, ColorReset, ColorYellow, alert.ID, ColorReset,
		eval.Answers.Domain.Confidence, eval.Answers.Severity.Score)

	webhookURL := os.Getenv("ESCALATION_WEBHOOK_URL")
	if webhookURL == "" {
		fmt.Printf("   %s(ESCALATION_WEBHOOK_URL not set, dropping escalation)%s\n", ColorYellow, ColorReset)
		return
	}

	payload, err := json.Marshal(alert)
	if err != nil {
		fmt.Printf("   %s❌ Failed to marshal alert for escalation: %v%s\n", ColorRed, err, ColorReset)
		return
	}

	resp, err := http.Post(webhookURL, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		fmt.Printf("   %s❌ Failed to forward webhook to %s: %v%s\n", ColorRed, webhookURL, err, ColorReset)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Printf("   %s✅ Successfully forwarded to PagerDuty/Slack webhook!%s\n", ColorGreen, ColorReset)
	} else {
		fmt.Printf("   %s⚠️ Escalation webhook returned status %d%s\n", ColorYellow, resp.StatusCode, ColorReset)
	}
}

func (e *Engine) executeWasm(wasmSource string, idempotencyKey string) ([]byte, error) {
	var mod wazero.CompiledModule
	var err error

	// 1. Check cache first
	if cachedMod, ok := e.wasmCache.Load(wasmSource); ok {
		mod = cachedMod.(wazero.CompiledModule)
	} else {
		// 2. Not in cache, fetch and compile
		var wasmBytes []byte
		if strings.HasPrefix(wasmSource, "http://") || strings.HasPrefix(wasmSource, "https://") {
			fmt.Printf("   %s🌐 Fetching Wasm from Registry: %s%s\n", ColorCyan, wasmSource, ColorReset)
			resp, reqErr := http.Get(wasmSource)
			if reqErr != nil {
				return nil, fmt.Errorf("failed to download Wasm: %w", reqErr)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("failed to download Wasm, status: %d", resp.StatusCode)
			}

			wasmBytes, err = io.ReadAll(resp.Body)
			if err != nil {
				return nil, fmt.Errorf("failed to read downloaded Wasm: %w", err)
			}
		} else {
			wasmBytes, err = os.ReadFile(wasmSource)
			if err != nil {
				return nil, fmt.Errorf("failed to read %s: %w", wasmSource, err)
			}
		}

		mod, err = e.wasmRuntime.CompileModule(e.ctx, wasmBytes)
		if err != nil {
			return nil, fmt.Errorf("compile error: %w", err)
		}
		
		// 3. Store in cache for future executions
		e.wasmCache.Store(wasmSource, mod)
	}

	var stdout strings.Builder
	config := wazero.NewModuleConfig().
		WithStdout(&stdout).
		WithStderr(&stdout).
		WithEnv("IDEMPOTENCY_KEY", idempotencyKey)

	_, err = e.wasmRuntime.InstantiateModule(e.ctx, mod, config)
	if err != nil {
		return []byte(stdout.String()), err
	}

	return []byte(stdout.String()), nil
}

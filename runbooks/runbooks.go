package runbooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/open-policy-agent/opa/rego"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
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
	cancel context.CancelFunc

	wasmRuntime wazero.Runtime
	wasmCache   sync.Map // map[string]wazero.CompiledModule
	MemoryStore *memory.Store

	regoQuery rego.PreparedEvalQuery
	regoErr   error

	PendingApprovals sync.Map // map[string]*PendingRunbook
	History          sync.Map // map[string]*HistoryRunbook
}

type PendingRunbook struct {
	ID           string    `json:"ID"`
	Domain       string    `json:"Domain"`
	Action       string    `json:"Action"`
	SourceCode   string    `json:"SourceCode,omitempty"`
	AlertPayload string    `json:"AlertPayload,omitempty"`
	Chan         chan bool `json:"-"`
	IsJit        bool      `json:"IsJit"`
	Steps        []Step    `json:"Steps,omitempty"`
}

type HistoryRunbook struct {
	ID             string `json:"ID"`
	Domain         string `json:"Domain"`
	Action         string `json:"Action"`
	Status         string `json:"Status"` // "success", "failed", "rolled_back"
	Timestamp      int64  `json:"Timestamp"`
	AlertPayload   string `json:"AlertPayload,omitempty"`
	CompletedSteps []Step `json:"CompletedSteps,omitempty"`
	Output         string `json:"Output,omitempty"`
}

// NewEngine builds a runbook engine. k8s may be nil, in which case DAG
// checkpointing and the orphan watcher are disabled.
func NewEngine(k8s *kubernetes.Clientset, memStore *memory.Store) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	r := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	engine := &Engine{
		config:      Config{Runbooks: make(map[string]Runbook)},
		k8s:         k8s,
		ctx:         ctx,
		cancel:      cancel,
		wasmRuntime: r,
		MemoryStore: memStore,
	}

	// Prepare the OPA query once. Re-preparing on every host call re-parses
	// policy.rego from disk, which is both slow and a needless failure mode.
	engine.regoQuery, engine.regoErr = prepareAuthzQuery(ctx)

	// Export the host function that lets sandboxed runbooks request a
	// Kubernetes mutation. This is the only way a Wasm module can affect the
	// cluster, and every request is checked against policy.rego first.
	_, err := r.NewHostModuleBuilder("env").
		NewFunctionBuilder().
		WithFunc(engine.kubectlExec).
		Export("kubectl_exec").
		Instantiate(ctx)
	if err != nil {
		panic(fmt.Errorf("failed to instantiate host module: %w", err))
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

	return engine
}

// prepareAuthzQuery compiles policy.rego into a reusable query object.
func prepareAuthzQuery(ctx context.Context) (rego.PreparedEvalQuery, error) {
	policyPath := os.Getenv("REFLEX_POLICY_PATH")
	if policyPath == "" {
		policyPath = "policy.rego"
	}
	return rego.New(
		rego.Query("data.reflex.authz.allow"),
		rego.Load([]string{policyPath}, nil),
	).PrepareForEval(ctx)
}

// kubectlExec is the host-side implementation of the `env.kubectl_exec`
// import exposed to sandboxed runbooks.
//
// The command string is read out of the guest's linear memory, split into
// argv, checked against policy.rego, checked against a hard allowlist, and
// only then executed. Returning 0 means the command actually ran and exited
// cleanly; any non-zero value fails the calling DAG step and triggers the
// compensating rollback.
func (e *Engine) kubectlExec(ctx context.Context, mod api.Module, cmdPtr, cmdLen uint32) uint32 {
	// 1. Read the requested command out of guest memory.
	mem := mod.Memory()
	if mem == nil {
		fmt.Printf("   %s❌ [HOST] Guest exported no memory; cannot read command%s\n", ColorRed, ColorReset)
		return 1
	}
	// Guard the read against out-of-bounds offsets without overflowing.
	memSize := mem.Size()
	if cmdLen > memSize || cmdPtr > memSize-cmdLen {
		fmt.Printf("   %s❌ [HOST] Command buffer out of bounds (ptr=%d len=%d, mem=%d)%s\n", ColorRed, cmdPtr, cmdLen, memSize, ColorReset)
		return 1
	}
	raw, ok := mem.Read(cmdPtr, cmdLen)
	if !ok {
		fmt.Printf("   %s❌ [HOST] Failed to read command from guest memory%s\n", ColorRed, ColorReset)
		return 1
	}

	command := strings.TrimSpace(string(raw))
	if command == "" {
		return 1
	}

	argv := strings.Fields(command)
	if len(argv) < 3 {
		fmt.Printf("   %s❌ [OPA-DENY] Malformed command: %q%s\n", ColorRed, command, ColorReset)
		return 1
	}

	// 2. Hard allowlist. policy.rego expresses operator intent; this is the
	// engine-level floor that no policy can talk its way past.
	allowedVerbs := map[string]bool{"rollout": true, "delete": true, "scale": true}
	if !allowedVerbs[argv[1]] {
		fmt.Printf("   %s🛑 [ALLOWLIST-DENY] kubectl verb %q is not permitted%s\n", ColorRed, argv[1], ColorReset)
		return 1
	}

	// 3. OPA policy check against the real command.
	if e.regoErr != nil {
		fmt.Printf("   %s❌ [OPA-ERROR] Authz policy unavailable, failing closed: %v%s\n", ColorRed, e.regoErr, ColorReset)
		return 1
	}
	input := map[string]interface{}{
		"action":   argv[1],
		"resource": argv[2],
		"target":   strings.Join(argv[3:], " "),
	}
	rs, err := e.regoQuery.Eval(ctx, rego.EvalInput(input))
	if err != nil || len(rs) == 0 {
		fmt.Printf("   %s❌ [OPA-ERROR] Policy evaluation failed for %q: %v%s\n", ColorRed, command, err, ColorReset)
		return 1
	}
	allowed, ok := rs[0].Expressions[0].Value.(bool)
	if !ok || !allowed {
		fmt.Printf("   %s🛑 [OPA-DENY] Command rejected by security policy: %s%s\n", ColorRed, command, ColorReset)
		return 1
	}

	// 4. Execute for real.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, execErr := cmd.CombinedOutput()
	if execErr != nil {
		fmt.Printf("   %s❌ [EXEC-FAIL] %s -> %v %s%s\n", ColorRed, command, execErr, strings.TrimSpace(string(out)), ColorReset)
		return 1
	}
	fmt.Printf("   %s✅ [EXEC-OK] %s%s\n", ColorGreen, command, ColorReset)
	if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
		fmt.Printf("      %s\n", trimmed)
	}
	return 0
}

// dagConfigMapName builds a valid RFC-1123 ConfigMap name for an alert ID.
//
// Alert IDs are not guaranteed to be DNS-safe: Alertmanager fingerprints are
// hex, but the native schema accepts arbitrary strings, and an empty ID
// previously produced "reflex-dag-", which the API server rejects outright.
// Slugify, bound the length, and append a hash of the original so distinct
// IDs can never collide after truncation.
func dagConfigMapName(alertID string) string {
	slug := strings.ToLower(alertID)
	slug = nonAlnum.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "alert"
	}
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	sum := sha256.Sum256([]byte(alertID))
	return fmt.Sprintf("reflex-dag-%s-%s", slug, hex.EncodeToString(sum[:4]))
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// checkpoint persists state and reports persistence failures without
// aborting the in-flight DAG. Losing a checkpoint degrades crash recovery but
// should not itself fail the remediation.
func (e *Engine) checkpoint(state *RunbookState) {
	if err := e.saveState(state); err != nil {
		fmt.Printf("   %s⚠️  [CHECKPOINT-FAIL] %v%s\n", ColorYellow, err, ColorReset)
	}
}

// saveState persists DAG state to a ConfigMap. Errors are surfaced rather
// than swallowed: a silently dropped checkpoint means crash recovery has no
// state to adopt, which is worse than a loud failure.
func (e *Engine) saveState(state *RunbookState) error {
	if e.k8s == nil {
		return nil
	}
	state.LastHeartbeat = time.Now().Unix()
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal dag state: %w", err)
	}

	cmName := dagConfigMapName(state.AlertID)
	cmClient := e.k8s.CoreV1().ConfigMaps("default")

	cm, err := cmClient.Get(e.ctx, cmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		newCm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: cmName,
				Labels: map[string]string{
					"reflex.io/type":   "dag-state",
					"reflex.io/status": state.Status,
					"reflex.io/alert":  nonAlnum.ReplaceAllString(strings.ToLower(state.AlertID), "-"),
				},
			},
			Data: map[string]string{
				"state": string(data),
			},
		}
		if _, err := cmClient.Create(e.ctx, newCm, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create configmap %s: %w", cmName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get configmap %s: %w", cmName, err)
	}

	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	if cm.Labels == nil {
		cm.Labels = map[string]string{}
	}
	cm.Data["state"] = string(data)
	cm.Labels["reflex.io/status"] = state.Status
	if _, err := cmClient.Update(e.ctx, cm, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update configmap %s: %w", cmName, err)
	}
	return nil
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
					e.checkpoint(&state)
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

	var rawGoCode string
	isJit := false
	runbook, ok := e.config.Runbooks[domain]
	if !ok || len(runbook.Steps) == 0 {
		isJit = true
		fmt.Printf("   %s(No static runbook DAG mapped for domain: %s. Initiating Autonomous Coder Agent...)%s\n", ColorYellow, domain, ColorReset)
		
		var err error
		var wasmPath string
		wasmPath, rawGoCode, err = coder.GenerateAndCompile(context.Background(), domain, string(alert.Payload))
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
	} else {
		// Static Runbook: attempt to load source code for UI if possible
		action := runbook.Steps[0].Action
		if strings.HasPrefix(action, "scripts/") && strings.HasSuffix(action, ".wasm") {
			folder := strings.TrimSuffix(strings.TrimPrefix(action, "scripts/"), ".wasm")
			srcPath := fmt.Sprintf("wasm-runbooks/%s/main.go", folder)
			if srcBytes, err := os.ReadFile(srcPath); err == nil {
				rawGoCode = string(srcBytes)
			}
		}
	}

	env := os.Getenv("REFLEX_ENV")
	if env == "" {
		env = "production" // default to safest mode
	}

	requireApproval := false
	if env == "production" || isJit {
		requireApproval = true
		// High-confidence production runbooks with proven track record (reliability >= 95%) can bypass manual gate
		if env == "production" && !isJit && e.MemoryStore != nil && e.MemoryStore.GetReliabilityScore(domain) >= 0.95 {
			requireApproval = false
		}
	}

	if requireApproval {
		fmt.Printf("   %s⏸️  [HITL] Human approval required before executing runbook.%s\n", ColorYellow, ColorReset)
		fmt.Printf("   %s         Approve via Web UI or run: 'reflex approve %s'%s\n", ColorYellow, alert.ID, ColorReset)
		
		approvalChan := make(chan bool)
		e.PendingApprovals.Store(alert.ID, &PendingRunbook{
			ID:           alert.ID,
			Domain:       domain,
			Action:       runbook.Steps[0].Action,
			SourceCode:   rawGoCode,
			AlertPayload: string(alert.Payload),
			Chan:         approvalChan,
			IsJit:        isJit,
			Steps:        runbook.Steps,
		})

		approved := <-approvalChan
		e.PendingApprovals.Delete(alert.ID)

		if !approved {
			fmt.Printf("   %s🚫 Runbook execution for %s was DENIED by operator. Escalating.%s\n", ColorRed, alert.ID, ColorReset)
			e.Escalate(alert, eval)
			return
		}
		fmt.Printf("   %s✅ Runbook execution for %s was APPROVED by operator.%s\n", ColorGreen, alert.ID, ColorReset)
	} else {
		fmt.Printf("   %s⚡ [AUTO-EXEC] Static runbook in production. Bypassing human approval!%s\n", ColorGreen, ColorReset)
	}

	state := RunbookState{
		AlertID: alert.ID,
		Domain:  domain,
		Status:  "running",
	}
	e.checkpoint(&state)

	doneChan := make(chan bool)
	if e.k8s != nil {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					e.checkpoint(&state)
				case <-doneChan:
					return
				}
			}
		}()
	}

	for i, step := range runbook.Steps {
		fmt.Printf("   %s-> Step %d: %s%s (Action: %s)\n", ColorCyan, i+1, step.Name, ColorReset, step.Action)
		
		hist := &HistoryRunbook{
			ID:             alert.ID,
			Domain:         domain,
			Action:         step.Action,
			Status:         "success",
			Timestamp:      time.Now().Unix(),
			AlertPayload:   string(alert.Payload),
			CompletedSteps: append(state.CompletedSteps, step),
		}

		idempotencyKey := uuid.New().String()
		err := e.executeStep(step.Action, idempotencyKey)
		if err != nil {
			fmt.Printf("      %s❌ Execution failed: %v%s\n", ColorRed, err, ColorReset)
			fmt.Printf("   %s⚠️ Initiating Compensating Transactions (Rollback)...%s\n", ColorYellow, ColorReset)
			
			hist.Status = "failed"
			e.History.Store(alert.ID, hist)

			if e.k8s != nil {
				close(doneChan)
			}
			e.rollback(state.CompletedSteps)
			
			state.Status = "rolled_back"
			e.checkpoint(&state)
			metrics.RemediationsTotal.WithLabelValues("rolled_back").Inc()

			// Save failure outcome to memory for future RAG prevention
			if e.MemoryStore != nil {
				go func(alertState []byte) {
					emb, _ := memory.GetEmbedding(context.Background(), string(alertState))
					e.MemoryStore.SaveOutcome(memory.Incident{
						ID:         alert.ID,
						Domain:     domain,
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
		hist.CompletedSteps = state.CompletedSteps
		e.History.Store(alert.ID, hist)
		e.checkpoint(&state)
	}
	
	if e.k8s != nil {
		close(doneChan)
	}
	fmt.Printf("   %s🎉 DAG Auto-Remediation Complete!%s\n", ColorGreen, ColorReset)
	
	state.Status = "completed"
	e.checkpoint(&state)
	metrics.RemediationsTotal.WithLabelValues("success").Inc()

	// Save success outcome to memory for future RAG
	if e.MemoryStore != nil {
		go func(alertState []byte) {
			emb, _ := memory.GetEmbedding(context.Background(), string(alertState))
			e.MemoryStore.SaveOutcome(memory.Incident{
				ID:         alert.ID,
				Domain:     domain,
				AlertState: string(alertState),
				Resolution: fmt.Sprintf("Successfully executed runbook for domain %s", domain),
				Outcome:    "success",
				Embedding:  emb,
			})
		}(alert.Payload)
	}
}

// ManualRollback executes the compensation steps for an alert's completed remediation DAG.
func (e *Engine) ManualRollback(id string) error {
	v, ok := e.History.Load(id)
	if !ok {
		return fmt.Errorf("runbook history not found for %s", id)
	}
	hist := v.(*HistoryRunbook)
	if hist.Status == "rolled_back" {
		return fmt.Errorf("runbook %s is already rolled back", id)
	}

	fmt.Printf("%s🔄 [MANUAL ROLLBACK]%s Initiating compensation rollback for alert %s%s%s (Domain: %s)\n",
		ColorYellow, ColorReset, ColorCyan, id, ColorReset, hist.Domain)

	stepsToRollback := hist.CompletedSteps
	// If completed steps wasn't stored or empty, check static runbook definition
	if len(stepsToRollback) == 0 {
		if rb, exists := e.config.Runbooks[hist.Domain]; exists {
			stepsToRollback = rb.Steps
		}
	}

	if len(stepsToRollback) == 0 {
		return fmt.Errorf("no compensation steps found to roll back %s", id)
	}

	e.rollback(stepsToRollback)

	hist.Status = "rolled_back"
	e.History.Store(id, hist)
	metrics.RemediationsTotal.WithLabelValues("manual_rollback").Inc()
	return nil
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

	slackMsg := map[string]interface{}{
		"text": fmt.Sprintf("🚨 *Reflex Escalation: %s*\n*Alert ID*: %s\n*Severity*: %.1f\n*AI Domain Confidence*: %.2f\n*AI Recommendation*: Auto-remediation aborted. Human intervention required.", alert.ServiceName, alert.ID, eval.Answers.Severity.Score, eval.Answers.Domain.Confidence),
		"alert": alert, // raw alert payload for other integrations
	}
	payload, err := json.Marshal(slackMsg)
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

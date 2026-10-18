package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"encoding/json"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/util/homedir"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"jev-proxy/evaluator"
	"jev-proxy/ingest"
	"jev-proxy/router"
	"jev-proxy/runbooks"
	"jev-proxy/stormbreaker"
	"jev-proxy/memory"
	"jev-proxy/tui"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

//go:embed ui/*
var uiFS embed.FS

func main() {
	// 0. Intercept CLI commands
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "tui":
			if err := tui.Start(); err != nil {
				fmt.Printf("Error running TUI: %v\n", err)
				os.Exit(1)
			}
			return
		case "pending":
			resp, err := http.Get("http://localhost:8080/pending")
			if err != nil {
				fmt.Printf("Error contacting proxy: %v\n", err)
				os.Exit(1)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			fmt.Printf("Pending Approvals:\n%s\n", string(body))
			return
		case "approve":
			if len(os.Args) < 3 {
				fmt.Println("Usage: reflex approve <alert-id>")
				os.Exit(1)
			}
			req, _ := http.NewRequest("POST", "http://localhost:8080/approve?id="+os.Args[2], nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil || resp.StatusCode != 200 {
				fmt.Printf("Failed to approve (maybe it doesn't exist or proxy is down). Error: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Successfully approved execution.")
			return
		case "deny":
			if len(os.Args) < 3 {
				fmt.Println("Usage: reflex deny <alert-id>")
				os.Exit(1)
			}
			req, _ := http.NewRequest("POST", "http://localhost:8080/deny?id="+os.Args[2], nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil || resp.StatusCode != 200 {
				fmt.Printf("Failed to deny (maybe it doesn't exist or proxy is down). Error: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Successfully denied execution.")
			return
		}
	}

	fmt.Println("🚀 Starting Reflex: Intelligent Observability and Remediation Proxy")

	// 0. Initialize Kubernetes Client (Distributed DAG State via CRDs/ConfigMaps)
	var k8sClient *kubernetes.Clientset
	
	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to local kubeconfig
		kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	
	if err == nil {
		k8sClient, err = kubernetes.NewForConfig(config)
		if err != nil {
			k8sClient = nil
		}
	}

	if k8sClient == nil {
		fmt.Println("⚠️  Could not connect to Kubernetes API. Running in stateless mode (no DAG persistence).")
	} else {
		fmt.Println("🗄️  Connected to Kubernetes API for distributed DAG state (CRDs).")
	}

	// 1. Initialize dependencies
	var aiClient evaluator.Client
	
	decisionAPI := os.Getenv("DECISION_API")
	apiKey := os.Getenv("AI_API_KEY")

	switch decisionAPI {
	case "jev":
		fmt.Println("🌐 Using Jev-Cloud for Decision API (https://api.jev.com/v1/evaluate)")
		aiClient = evaluator.NewRealClient(apiKey, "https://api.jev.com/v1/evaluate")
	case "laya":
		fmt.Println("🧠 Using Laya-Server for Decision API (http://localhost:8081/v1/evaluate)")
		aiClient = evaluator.NewRealClient("", "http://localhost:8081/v1/evaluate")
	default:
		// Fallback to old behavior for backwards compatibility
		customEndpoint := os.Getenv("AI_API_ENDPOINT")
		if customEndpoint != "" || apiKey != "" {
			if customEndpoint != "" {
				fmt.Printf("🌐 AI_API_ENDPOINT detected. Using REAL AI Client pointing to: %s\n", customEndpoint)
			} else {
				fmt.Println("🔑 AI_API_KEY detected. Using REAL AI Client.")
			}
			aiClient = evaluator.NewRealClient(apiKey, customEndpoint)
		} else {
			fmt.Println("⚠️  No DECISION_API, API Key, or Endpoint detected. Using Mock AI Client.")
			aiClient = evaluator.NewMockClient()
		}
	}

	// Initialize Memory Store
	fmt.Println("🧠 Initializing S3 Vector Memory Loader (Local JSONL fallback)")
	memStore, err := memory.NewStore(os.Getenv("S3_MEMORY_BUCKET"), "reflex/memory.jsonl", "data/memory.jsonl")
	if err != nil {
		fmt.Printf("⚠️  Failed to load memory store: %v\n", err)
	}

	fmt.Println("🤖 Wrapping client with Mixture-of-Agents (MoA) Consensus Engine")
	aiClient = evaluator.NewMoAClient(aiClient, memStore)

	runbookEngine := runbooks.NewEngine(k8sClient, memStore)

	// 2. Set up buffered channels to handle burst traffic without deadlocks
	alertChan := make(chan ingest.Alert, 1000)
	dedupeChan := make(chan ingest.Alert, 1000)
	eventChan := make(chan router.Event, 1000)

	var wg sync.WaitGroup

	// 3. Start Orphan Watcher with Leader Election.
	// The watcher is started ONLY here. It used to also be started inside
	// runbooks.NewEngine, which meant every replica ran it and leader
	// election was defeated by its own constructor.
	if k8sClient != nil {
		go func() {
			lock := &resourcelock.LeaseLock{
				LeaseMeta: metav1.ObjectMeta{
					Name:      "reflex-orphan-watcher",
					Namespace: "default",
				},
				Client: k8sClient.CoordinationV1(),
				LockConfig: resourcelock.ResourceLockConfig{
					Identity: os.Getenv("HOSTNAME"), // Or generate a UUID
				},
			}

			leaderelection.RunOrDie(context.Background(), leaderelection.LeaderElectionConfig{
				Lock:            lock,
				ReleaseOnCancel: true,
				LeaseDuration:   15 * time.Second,
				RenewDeadline:   10 * time.Second,
				RetryPeriod:     2 * time.Second,
				Callbacks: leaderelection.LeaderCallbacks{
					OnStartedLeading: func(ctx context.Context) {
						fmt.Println("👑 Acquired Leader Lock! Starting Orphan Watcher...")
						runbookEngine.StartOrphanWatcher() // Blocks
					},
					OnStoppedLeading: func() {
						fmt.Println("❌ Lost Leader Lock!")
					},
				},
			})
		}()
	}

	// 2.5 Start Storm-Breaker Deduplicator
	fmt.Println("🌩️  Initializing Storm-Breaker RAG Deduplicator (5s window)...")
	deduplicator := stormbreaker.NewDeduplicator(alertChan, dedupeChan, &wg, 5*time.Second)
	go deduplicator.Start()

	// 3. Setup Configurable Routing Thresholds
	routerConfig := router.Config{
		MinConfidence:  parseFloatEnv("ROUTING_MIN_CONFIDENCE", 0.60),
		MaxSeverity:    parseFloatEnv("ROUTING_MAX_SEVERITY", 8.0),
		MinRecoverable: parseFloatEnv("ROUTING_MIN_RECOVERABLE", 0.60),
	}

	fmt.Printf("⚙️  Routing Config: MinConf=%.2f, MaxSev=%.1f, MinRecov=%.2f\n",
		routerConfig.MinConfidence, routerConfig.MaxSeverity, routerConfig.MinRecoverable)

	// 4. Start the Router listener
	routerInstance := router.NewRouter(eventChan, runbookEngine, &wg, routerConfig)
	go routerInstance.Start()

	// 5. Start concurrent Ingester / Evaluator workers
	const numWorkers = 10
	for i := 0; i < numWorkers; i++ {
		go func(workerID int) {
			for alert := range dedupeChan {
				req := evaluator.EvaluationRequest{
					State:     alert.Payload,
					Questions: []string{"domain", "severity", "auto_recoverable"},
				}

				ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
				resp, err := aiClient.Evaluate(ctx, req)
				cancel()

				if err != nil {
					fmt.Printf("Worker %d: Error evaluating alert %s: %v\n", workerID, alert.ID, err)
					wg.Done()
					continue
				}

				eventChan <- router.Event{
					Alert: alert,
					Eval:  *resp,
				}
			}
		}(i)
	}

	// 6. Start HTTP Ingress Server
	mux := http.NewServeMux()

	handler := &ingest.Handler{
		AlertChan: alertChan,
		Wg:        &wg,
		K8sClient: k8sClient,
	}
	mux.Handle("/webhook", handler)
	mux.Handle("/metrics", promhttp.Handler())
	
	// UI Dashboard
	uiFolder, _ := fs.Sub(uiFS, "ui")
	mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(uiFolder))))

	mux.HandleFunc("/pending", func(w http.ResponseWriter, r *http.Request) {
		var pending []*runbooks.PendingRunbook
		runbookEngine.PendingApprovals.Range(func(key, value interface{}) bool {
			pending = append(pending, value.(*runbooks.PendingRunbook))
			return true
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pending)
	})

	mux.HandleFunc("/history", func(w http.ResponseWriter, r *http.Request) {
		var history []*runbooks.HistoryRunbook
		runbookEngine.History.Range(func(key, value interface{}) bool {
			history = append(history, value.(*runbooks.HistoryRunbook))
			return true
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(history)
	})

	mux.HandleFunc("/rollback", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if err := runbookEngine.ManualRollback(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Runbook %s rolled back successfully", id)
	})

	resolveApproval := func(ctx context.Context, id string, approved bool) (bool, error) {
		v, ok := runbookEngine.PendingApprovals.Load(id)
		if !ok {
			return false, nil
		}
		pendingRunbook := v.(*runbooks.PendingRunbook)
		select {
		case pendingRunbook.Chan <- approved:
			return true, nil
		case <-time.After(5 * time.Second):
			return false, fmt.Errorf("no DAG is waiting on approval for %s", id)
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}

	mux.HandleFunc("/approve", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		ok, err := resolveApproval(r.Context(), id, true)
		switch {
		case err != nil:
			http.Error(w, err.Error(), http.StatusConflict)
		case !ok:
			http.Error(w, "No pending approval with that id", http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	mux.HandleFunc("/deny", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		ok, err := resolveApproval(r.Context(), id, false)
		switch {
		case err != nil:
			http.Error(w, err.Error(), http.StatusConflict)
		case !ok:
			http.Error(w, "No pending approval with that id", http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	server := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	go func() {
		fmt.Println("🌐 HTTP Server listening on http://localhost:8080/webhook")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("HTTP server error: %v\n", err)
		}
	}()

	// 6.5 Start Mock Wasm Registry Server (For Phase 6 Testing)
	go func() {
		registryMux := http.NewServeMux()
		// Serve the scripts directory as the "Registry"
		registryMux.Handle("/", http.FileServer(http.Dir("./scripts")))
		fmt.Println("📦 Mock Wasm Registry listening on http://localhost:9090")
		http.ListenAndServe(":9090", registryMux)
	}()

	// 7. Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	fmt.Println("\n🛑 Shutting down gracefully...")

	ctxDown, cancelDown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDown()
	server.Shutdown(ctxDown)

	wgWaitChan := make(chan struct{})
	go func() {
		wg.Wait()
		close(wgWaitChan)
	}()

	select {
	case <-wgWaitChan:
		fmt.Println("🏁 All pending alerts processed.")
	case <-time.After(60 * time.Second):
		fmt.Println("⚠️  Timeout waiting for pending alerts.")
	}

	close(alertChan)
	close(dedupeChan)
	close(eventChan)
	fmt.Println("👋 Goodbye!")
}

func parseFloatEnv(key string, fallback float64) float64 {
	valStr := os.Getenv(key)
	if valStr == "" {
		return fallback
	}
	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		fmt.Printf("⚠️  Invalid value for %s, falling back to %.2f\n", key, fallback)
		return fallback
	}
	return val
}

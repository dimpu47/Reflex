package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"jev-proxy/ingest"
	"jev-proxy/evaluator"
	"jev-proxy/router"
	"jev-proxy/runbooks"
)

func main() {
	fmt.Println("🚀 Starting Reflex: Intelligent Observability and Remediation Proxy")

	// 1. Initialize dependencies
	var jevClient evaluator.Client
	apiKey := os.Getenv("JEV_API_KEY")
	customEndpoint := os.Getenv("JEV_API_ENDPOINT")

	if customEndpoint != "" || apiKey != "" {
		if customEndpoint != "" {
			fmt.Printf("🌐 JEV_API_ENDPOINT detected. Using REAL API Client pointing to: %s\n", customEndpoint)
		} else {
			fmt.Println("🔑 JEV_API_KEY detected. Using REAL Jev API Client.")
		}
		jevClient = evaluator.NewRealClient(apiKey, customEndpoint)
	} else {
		fmt.Println("⚠️  No API Key or Endpoint detected. Using Mock Jev API Client.")
		jevClient = evaluator.NewMockClient()
	}

	runbookEngine := runbooks.NewEngine()

	// 2. Set up buffered channels to handle burst traffic without deadlocks
	alertChan := make(chan ingest.Alert, 1000)
	eventChan := make(chan router.Event, 1000)

	var wg sync.WaitGroup

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
			for alert := range alertChan {
				req := evaluator.EvaluationRequest{
					State:     alert.Payload,
					Questions: []string{"domain", "severity", "auto_recoverable"},
				}

				// The Laya microservice (local) can take longer to evaluate than the mocked client
				// so we increased the timeout slightly for local testing without GPU.
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				resp, err := jevClient.Evaluate(ctx, req)
				cancel()

				if err != nil {
					fmt.Printf("Worker %d: Error evaluating alert %s: %v\n", workerID, alert.ID, err)
					wg.Done() // ensure we release the waitgroup
					continue
				}

				eventChan <- router.Event{
					Alert: alert,
					Eval:  *resp,
				}
			}
		}(i)
	}

	// 5. Start HTTP Ingress Server
	mux := http.NewServeMux()

	// Create the HTTP handler passing the waitgroup
	handler := &ingest.Handler{
		AlertChan: alertChan,
		Wg:        &wg,
	}
	mux.Handle("/webhook", handler)

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

	// 6. Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	fmt.Println("\n🛑 Shutting down gracefully...")

	// Stop accepting new requests
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(ctx)

	// Wait for pending alerts to finish processing through the router
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
	close(eventChan)
	fmt.Println("👋 Goodbye!")
}

// parseFloatEnv helper function to parse float environment variables with a fallback
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

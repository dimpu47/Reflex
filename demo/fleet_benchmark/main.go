package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"jev-proxy/ingest"
)

var workloads = []struct {
	service string
	domain  string
	summary string
}{
	{"redis", "database", "Redis memory exhaustion and connection leak"},
	{"sample-app", "application", "Sample app unhandled exception 500 error rate spike"},
	{"auth-service", "application", "Auth service token validation timeout"},
	{"payment-service", "application", "Payment gateway connection refused"},
	{"order-service", "application", "Order processing queue backing up"},
	{"inventory-service", "application", "Inventory sync deadlock detected"},
	{"cart-service", "application", "Cart session cache eviction failure"},
	{"notification-service", "application", "Notification dispatcher worker crashloop"},
}

func main() {
	burstSize := flag.Int("burst", 24, "Total number of alerts to fire concurrently")
	duplicates := flag.Bool("dupes", false, "Simulate identical duplicate alerts to test Storm-Breaker deduplication")
	flag.Parse()

	url := "http://localhost:8080/webhook"
	fmt.Printf("🚀 Starting Multi-Workload Scale Test: %d alerts across %d microservices\n", *burstSize, len(workloads))
	if *duplicates {
		fmt.Printf("🌩️  Storm-Breaker Mode: Generating duplicate alerts within 5s window to test deduplication!\n")
	}

	var wg sync.WaitGroup
	var acceptedCount int64
	var errorCount int64

	start := time.Now()

	for i := 0; i < *burstSize; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			wl := workloads[idx%len(workloads)]

			alertID := fmt.Sprintf("ALERT-FLEET-%03d", idx+1)
			if *duplicates {
				alertID = fmt.Sprintf("ALERT-FLEET-%03d", (idx%len(workloads))+1)
			}

			payloadStr := fmt.Sprintf(`{
				"alertname": "ServiceFailure",
				"service": "%s",
				"status": "firing",
				"severity": "critical",
				"summary": "%s",
				"labels": {
					"namespace": "default",
					"deployment": "%s",
					"job": "%s"
				}
			}`, wl.domain, wl.summary, wl.service, wl.service)

			alert := ingest.Alert{
				ID:          alertID,
				ServiceName: wl.service,
				Payload:     []byte(payloadStr),
			}

			body, err := json.Marshal(alert)
			if err != nil {
				atomic.AddInt64(&errorCount, 1)
				return
			}

			resp, err := http.Post(url, "application/json", bytes.NewBuffer(body))
			if err != nil {
				atomic.AddInt64(&errorCount, 1)
				fmt.Printf("   ❌ HTTP error for %s (%s): %v\n", alertID, wl.service, err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusAccepted {
				atomic.AddInt64(&acceptedCount, 1)
			} else {
				atomic.AddInt64(&errorCount, 1)
				respBody, _ := io.ReadAll(resp.Body)
				fmt.Printf("   ⚠️  Unexpected status %d for %s: %s\n", resp.StatusCode, alertID, string(respBody))
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	throughput := float64(*burstSize) / duration.Seconds()

	fmt.Println("\n📊 === Multi-Workload Scale Test Results ===")
	fmt.Printf("   Total Alerts Sent:  %d\n", *burstSize)
	fmt.Printf("   Accepted (202):     %d (%.1f%%)\n", acceptedCount, float64(acceptedCount)/float64(*burstSize)*100)
	fmt.Printf("   Errors / Dropped:   %d\n", errorCount)
	fmt.Printf("   Elapsed Time:       %v\n", duration)
	fmt.Printf("   Ingress Throughput: %.1f alerts/sec\n", throughput)
	fmt.Println("============================================")
}

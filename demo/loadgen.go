package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"jev-proxy/ingest"
)

func main() {
	const burstSize = 50
	const url = "http://localhost:8080/webhook"
	fmt.Printf("🔫 Firing %d concurrent alerts at %s...\n", burstSize, url)

	var wg sync.WaitGroup
	start := time.Now()

	for i := 1; i <= burstSize; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			var payloadStr string
			if id%2 == 0 {
				// Auto-recoverable transient issue
				payloadStr = fmt.Sprintf(`{"error": "Redis connection timeout", "retries_failed": 3, "service": "cache-layer"}`)
			} else {
				// Critical non-recoverable issue
				payloadStr = fmt.Sprintf(`{"error": "Data corruption detected in users table", "disk_space_free_bytes": 0, "service": "primary-db"}`)
			}

			alert := ingest.Alert{
				ID:          fmt.Sprintf("ALERT-%03d", id),
				ServiceName: "payment-service",
				Payload:     []byte(payloadStr),
			}

			payload, _ := json.Marshal(alert)
			resp, err := http.Post(url, "application/json", bytes.NewBuffer(payload))
			if err != nil {
				fmt.Printf("❌ Failed to send alert %d: %v\n", id, err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusAccepted {
				fmt.Printf("⚠️  Server returned %d for alert %d\n", resp.StatusCode, id)
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)
	fmt.Printf("✅ Burst complete in %v\n", duration)
}

package stormbreaker

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"jev-proxy/ingest"
	"jev-proxy/metrics"
)

// Deduplicator buffers incoming alerts and groups them to prevent thundering herds.
type Deduplicator struct {
	inWindow time.Duration
	cache    map[string]time.Time
	mu       sync.Mutex

	InChan  <-chan ingest.Alert
	OutChan chan<- ingest.Alert
	Wg      *sync.WaitGroup
}

// NewDeduplicator creates a new StormBreaker instance.
func NewDeduplicator(in <-chan ingest.Alert, out chan<- ingest.Alert, wg *sync.WaitGroup, window time.Duration) *Deduplicator {
	return &Deduplicator{
		inWindow: window,
		cache:    make(map[string]time.Time),
		InChan:   in,
		OutChan:  out,
		Wg:       wg,
	}
}

// Start begins listening for alerts and deduplicating them.
func (d *Deduplicator) Start() {
	// Cleanup routine to remove old cache entries to prevent memory leaks
	go func() {
		ticker := time.NewTicker(d.inWindow)
		for range ticker.C {
			d.mu.Lock()
			now := time.Now()
			for k, v := range d.cache {
				if now.Sub(v) > d.inWindow {
					delete(d.cache, k)
				}
			}
			d.mu.Unlock()
		}
	}()

	for alert := range d.InChan {
		// Generate a deterministic signature for the alert based on its payload.
		// In a full RAG implementation, this uses Vector Embeddings and Cosine Similarity.
		sig := fmt.Sprintf("%x", sha256.Sum256([]byte(alert.ServiceName+string(alert.Payload))))

		d.mu.Lock()
		lastSeen, exists := d.cache[sig]
		d.cache[sig] = time.Now()
		d.mu.Unlock()

		if exists && time.Since(lastSeen) < d.inWindow {
			// This alert is part of an ongoing storm cohort. Squash it.
			fmt.Printf("🌩️  Storm-Breaker: Squashed redundant alert %s for service %s\n", alert.ID, alert.ServiceName)
			metrics.AlertsDeduplicated.Inc()
			d.Wg.Done() // We acknowledge it so main doesn't block on shutdown
			continue
		}

		// First time seeing this in the time window, forward it
		d.OutChan <- alert
	}
}

package memory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Incident represents a past remediation outcome
type Incident struct {
	ID          string    `json:"id"`
	Domain      string    `json:"domain"`
	AlertState  string    `json:"alert_state"`
	Resolution  string    `json:"resolution"`
	Outcome     string    `json:"outcome"` // "success" or "failure"
	Embedding   []float64 `json:"embedding"`
}

type Store struct {
	mu        sync.RWMutex
	incidents []Incident
	s3Bucket  string
	s3Key     string
	localFile string
}

// NewStore initializes a memory store. If bucket is empty, falls back to local file.
func NewStore(bucket, key, localFile string) (*Store, error) {
	s := &Store{
		s3Bucket:  bucket,
		s3Key:     key,
		localFile: localFile,
		incidents: make([]Incident, 0),
	}
	
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to load memory: %v", err)
	}
	return s, nil
}

// load reads the JSONL from S3 (mocked locally for now) and populates the store
func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// In a real S3 scenario, we'd use aws-sdk-go-v2 here. 
	// For now, we abstract it behind a local file cache.
	file, err := os.Open(s.localFile)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var inc Incident
		if err := json.Unmarshal(scanner.Bytes(), &inc); err == nil {
			s.incidents = append(s.incidents, inc)
		}
	}
	return scanner.Err()
}

// SaveOutcome appends an incident outcome to the store and persists it
func (s *Store) SaveOutcome(inc Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.incidents = append(s.incidents, inc)

	// Append to local JSONL
	data, err := json.Marshal(inc)
	if err != nil {
		return err
	}
	
	if err := os.MkdirAll(filepath.Dir(s.localFile), 0755); err != nil {
		return err
	}

	f, err := os.OpenFile(s.localFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}

	// TODO: Upload to S3 if s3Bucket is configured
	if s.s3Bucket != "" {
		fmt.Printf("☁️  (Mock) Uploaded updated memory.jsonl to s3://%s/%s\n", s.s3Bucket, s.s3Key)
	}

	return nil
}

// cosineSimilarity computes the cosine similarity between two vectors
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.0
	}
	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0.0
	}
	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// SearchSimilar finds the topK most similar incidents based on the alert embedding
func (s *Store) SearchSimilar(embedding []float64, topK int) []Incident {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type scoredIncident struct {
		inc   Incident
		score float64
	}

	var scored []scoredIncident
	for _, inc := range s.incidents {
		// Only care about successful remediations for RAG context
		if inc.Outcome != "success" {
			continue
		}
		score := cosineSimilarity(embedding, inc.Embedding)
		scored = append(scored, scoredIncident{inc: inc, score: score})
	}

	// Sort descending
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	var results []Incident
	for i := 0; i < len(scored) && i < topK; i++ {
		results = append(results, scored[i].inc)
	}
	return results
}

// GetEmbedding is a utility to fetch embeddings from local Ollama for the RAG pipeline
func GetEmbedding(ctx context.Context, text string) ([]float64, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":  "nomic-embed-text", // Highly efficient local embedding model
		"prompt": text,
	})

	ollamaHost := os.Getenv("OLLAMA_HOST")
	if ollamaHost == "" {
		ollamaHost = "http://localhost:11434"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ollamaHost+"/api/embeddings", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")
	
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	embInterface, ok := res["embedding"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid embedding format returned")
	}

	embedding := make([]float64, len(embInterface))
	for i, v := range embInterface {
		if val, ok := v.(float64); ok {
			embedding[i] = val
		}
	}
	return embedding, nil
}

// GetReliabilityScore returns the empirical success rate for a given domain.
// It scans all stored incidents for the domain and computes successes/total.
// Returns 0.0 if no data exists (unproven).
//
// This previously compared AlertState (the raw alert JSON) against the
// domain string, which could never be equal, so the score was permanently
// 0.0 and the production gate blocked every runbook forever.
func (s *Store) GetReliabilityScore(domain string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var total, successes float64
	for _, inc := range s.incidents {
		if !strings.EqualFold(inc.Domain, domain) {
			continue
		}
		total++
		if inc.Outcome == "success" {
			successes++
		}
	}
	if total == 0 {
		return 0.0
	}
	return successes / total
}


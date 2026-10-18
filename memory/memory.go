package memory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
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

// S3API abstracts S3 operations for testability and runtime flexibility
type S3API interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type Store struct {
	mu        sync.RWMutex
	incidents []Incident
	s3Client  S3API
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

	if bucket != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var optFns []func(*awsconfig.LoadOptions) error
		if region := os.Getenv("AWS_REGION"); region != "" {
			optFns = append(optFns, awsconfig.WithRegion(region))
		}

		cfg, err := awsconfig.LoadDefaultConfig(ctx, optFns...)
		if err == nil {
			var s3Opts []func(*s3.Options)
			if endpoint := os.Getenv("S3_ENDPOINT"); endpoint != "" {
				s3Opts = append(s3Opts, func(o *s3.Options) {
					o.BaseEndpoint = aws.String(endpoint)
					o.UsePathStyle = true
				})
			}
			s.s3Client = s3.NewFromConfig(cfg, s3Opts...)
		} else {
			fmt.Printf("⚠️  Failed to load AWS configuration: %v (falling back to local cache)\n", err)
		}
	}

	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to load memory: %v", err)
	}
	return s, nil
}

// SetS3Client injects a custom or mock S3 client
func (s *Store) SetS3Client(client S3API) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s3Client = client
}

// load reads the JSONL from S3 (if configured) or local file and populates the store
func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. If S3 client is configured, attempt download from S3
	if s.s3Client != nil && s.s3Bucket != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		resp, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(s.s3Bucket),
			Key:    aws.String(s.s3Key),
		})
		if err == nil && resp != nil {
			defer resp.Body.Close()
			bodyBytes, readErr := io.ReadAll(resp.Body)
			if readErr == nil && len(bodyBytes) > 0 {
				fmt.Printf("☁️  Loaded memory store from s3://%s/%s (%d bytes)\n", s.s3Bucket, s.s3Key, len(bodyBytes))
				_ = os.MkdirAll(filepath.Dir(s.localFile), 0755)
				_ = os.WriteFile(s.localFile, bodyBytes, 0644)

				scanner := bufio.NewScanner(bytes.NewReader(bodyBytes))
				for scanner.Scan() {
					var inc Incident
					if err := json.Unmarshal(scanner.Bytes(), &inc); err == nil {
						s.incidents = append(s.incidents, inc)
					}
				}
				return scanner.Err()
			}
		} else if err != nil {
			var noKey *s3types.NoSuchKey
			if errors.As(err, &noKey) {
				fmt.Printf("ℹ️  s3://%s/%s does not exist yet. Starting with local/empty store.\n", s.s3Bucket, s.s3Key)
			} else {
				fmt.Printf("⚠️  Could not fetch from S3: %v. Falling back to local file.\n", err)
			}
		}
	}

	// 2. Fallback to local file cache
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

	// Upload to S3 if s3Bucket is configured
	if s.s3Client != nil && s.s3Bucket != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var buf bytes.Buffer
		for _, item := range s.incidents {
			line, err := json.Marshal(item)
			if err == nil {
				buf.Write(line)
				buf.WriteByte('\n')
			}
		}

		_, err := s.s3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(s.s3Bucket),
			Key:         aws.String(s.s3Key),
			Body:        bytes.NewReader(buf.Bytes()),
			ContentType: aws.String("application/x-ndjson"),
		})
		if err != nil {
			fmt.Printf("⚠️  Failed to upload memory.jsonl to s3://%s/%s: %v\n", s.s3Bucket, s.s3Key, err)
		} else {
			fmt.Printf("☁️  Successfully synced %d incidents to s3://%s/%s\n", len(s.incidents), s.s3Bucket, s.s3Key)
		}
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

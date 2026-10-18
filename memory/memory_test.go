package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type mockS3Client struct {
	getObjectFunc func(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	putObjectFunc func(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	putCalled     bool
	putBody       []byte
}

func (m *mockS3Client) GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if m.getObjectFunc != nil {
		return m.getObjectFunc(ctx, params, optFns...)
	}
	return nil, nil
}

func (m *mockS3Client) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	m.putCalled = true
	if params.Body != nil {
		m.putBody, _ = io.ReadAll(params.Body)
	}
	if m.putObjectFunc != nil {
		return m.putObjectFunc(ctx, params, optFns...)
	}
	return &s3.PutObjectOutput{}, nil
}

func TestStore_LocalSaveAndLoad(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "reflex-memory-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	localFile := filepath.Join(tmpDir, "memory.jsonl")

	store, err := NewStore("", "", localFile)
	if err != nil {
		t.Fatalf("failed to initialize store: %v", err)
	}

	inc1 := Incident{
		ID:         "inc-1",
		Domain:     "database",
		AlertState: `{"alert": "db connection timeout"}`,
		Resolution: "restarted redis",
		Outcome:    "success",
		Embedding:  []float64{1.0, 0.0, 0.0},
	}

	if err := store.SaveOutcome(inc1); err != nil {
		t.Fatalf("failed to save outcome: %v", err)
	}

	// Verify local file exists and contains data
	data, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("failed to read local memory file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty memory file")
	}

	// Reopen store from disk
	store2, err := NewStore("", "", localFile)
	if err != nil {
		t.Fatalf("failed to reload store: %v", err)
	}
	if len(store2.incidents) != 1 {
		t.Fatalf("expected 1 loaded incident, got %d", len(store2.incidents))
	}
	if store2.incidents[0].ID != "inc-1" {
		t.Fatalf("expected ID inc-1, got %s", store2.incidents[0].ID)
	}
}

func TestStore_S3Sync(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "reflex-s3-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	localFile := filepath.Join(tmpDir, "memory.jsonl")

	initialInc := Incident{
		ID:         "inc-s3-init",
		Domain:     "network",
		Outcome:    "success",
		Embedding:  []float64{0.5, 0.5},
	}
	initialBytes, _ := json.Marshal(initialInc)
	initialBytes = append(initialBytes, '\n')

	mockS3 := &mockS3Client{
		getObjectFunc: func(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{
				Body: io.NopCloser(bytes.NewReader(initialBytes)),
			}, nil
		},
	}

	store := &Store{
		s3Bucket:  "test-bucket",
		s3Key:     "memory/memory.jsonl",
		localFile: localFile,
		s3Client:  mockS3,
		incidents: make([]Incident, 0),
	}

	if err := store.load(); err != nil {
		t.Fatalf("failed to load from mock S3: %v", err)
	}

	if len(store.incidents) != 1 || store.incidents[0].ID != "inc-s3-init" {
		t.Fatalf("expected 1 incident from S3, got: %+v", store.incidents)
	}

	// Now save a new outcome and verify S3 PutObject was called
	newInc := Incident{
		ID:        "inc-s3-new",
		Domain:    "network",
		Outcome:   "success",
		Embedding: []float64{0.7, 0.7},
	}

	if err := store.SaveOutcome(newInc); err != nil {
		t.Fatalf("failed to save outcome with S3: %v", err)
	}

	if !mockS3.putCalled {
		t.Fatal("expected S3 PutObject to be called on SaveOutcome")
	}

	if !bytes.Contains(mockS3.putBody, []byte("inc-s3-new")) {
		t.Fatal("expected uploaded S3 body to contain new incident")
	}
}

func TestStore_CosineSimilarityAndSearch(t *testing.T) {
	store := &Store{
		incidents: []Incident{
			{ID: "inc-1", Outcome: "success", Embedding: []float64{1.0, 0.0, 0.0}},
			{ID: "inc-2", Outcome: "success", Embedding: []float64{0.0, 1.0, 0.0}},
			{ID: "inc-3", Outcome: "failure", Embedding: []float64{1.0, 0.0, 0.0}}, // should be ignored
		},
	}

	query := []float64{0.9, 0.1, 0.0}
	results := store.SearchSimilar(query, 2)

	if len(results) != 2 {
		t.Fatalf("expected 2 successful results, got %d", len(results))
	}
	if results[0].ID != "inc-1" {
		t.Fatalf("expected inc-1 to have highest similarity, got %s", results[0].ID)
	}
}

func TestStore_ReliabilityScore(t *testing.T) {
	store := &Store{
		incidents: []Incident{
			{Domain: "database", Outcome: "success"},
			{Domain: "database", Outcome: "success"},
			{Domain: "database", Outcome: "failure"},
			{Domain: "network", Outcome: "success"},
		},
	}

	dbScore := store.GetReliabilityScore("database")
	if dbScore < 0.66 || dbScore > 0.67 {
		t.Fatalf("expected ~0.667 for database, got %f", dbScore)
	}

	netScore := store.GetReliabilityScore("network")
	if netScore != 1.0 {
		t.Fatalf("expected 1.0 for network, got %f", netScore)
	}

	unknownScore := store.GetReliabilityScore("unknown")
	if unknownScore != 0.0 {
		t.Fatalf("expected 0.0 for unknown, got %f", unknownScore)
	}
}

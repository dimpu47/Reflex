package evaluator

import (
	"context"
	"math/rand"
	"time"
)

// MockClient implements the Client interface for local testing without an API key.
type MockClient struct{}

// NewMockClient creates a new MockClient.
func NewMockClient() *MockClient {
	return &MockClient{}
}

// Evaluate simulates a Jev API response.
func (m *MockClient) Evaluate(ctx context.Context, req EvaluationRequest) (*EvaluationResponse, error) {
	// Simulate network latency (between 10ms and 60ms)
	time.Sleep(time.Duration(10+rand.Intn(50)) * time.Millisecond)

	// Add some randomness to test routing conditions
	severityScore := 6.8
	confidence := 0.96
	recoverable := 0.93

	// Randomly trigger escalation conditions ~25% of the time each
	r := rand.Float64()
	if r < 0.25 {
		severityScore = 8.5 // Triggers condition B (severity >= 8.0)
	} else if r < 0.5 {
		confidence = 0.90 // Triggers condition B (confidence < 0.95)
	} else if r < 0.75 {
		recoverable = 0.85 // Falls through Condition A, triggers default Escalation
	}

	return &EvaluationResponse{
		Answers: Answers{
			Domain: Choice{
				Choice:     "database",
				Confidence: confidence,
				Probabilities: map[string]float64{
					"database":    0.96,
					"application": 0.03,
					"network":     0.01,
					"cluster_ops": 0.00,
				},
			},
			Severity: Score{
				Score: severityScore,
			},
			AutoRecoverable: Noul{
				Probability: recoverable,
			},
		},
		Usage: Usage{
			InputTokens:  312,
			OutputTokens: 0,
		},
	}, nil
}

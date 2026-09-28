package evaluator

import (
	"context"
	"testing"
)

// mockClient implements Client for testing
type mockClient struct {
	resp EvaluationResponse
	err  error
}

func (m *mockClient) Evaluate(ctx context.Context, req EvaluationRequest) (EvaluationResponse, error) {
	return m.resp, m.err
}

func TestMoA_Evaluate_FastPath(t *testing.T) {
	fastClient := &mockClient{
		resp: EvaluationResponse{
			Answers: Answers{
				Domain: Choice{Choice: "database", Confidence: 0.96},
			},
		},
	}
	moa := NewMoA(fastClient, []Client{})

	resp, err := moa.Evaluate(context.Background(), EvaluationRequest{})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp.Answers.Domain.Choice != "database" {
		t.Errorf("expected database, got %v", resp.Answers.Domain.Choice)
	}
}

func TestMoA_Evaluate_Consensus(t *testing.T) {
	fastClient := &mockClient{
		resp: EvaluationResponse{
			Answers: Answers{
				Domain: Choice{Choice: "database", Confidence: 0.85}, // triggers consensus
			},
		},
	}
	
	// Create two expert clients that disagree with the fast client but agree with each other
	expert1 := &mockClient{
		resp: EvaluationResponse{
			Answers: Answers{
				Domain: Choice{Choice: "network", Confidence: 0.90},
			},
		},
	}
	expert2 := &mockClient{
		resp: EvaluationResponse{
			Answers: Answers{
				Domain: Choice{Choice: "network", Confidence: 0.95},
			},
		},
	}
	
	moa := NewMoA(fastClient, []Client{expert1, expert2})
	resp, err := moa.Evaluate(context.Background(), EvaluationRequest{})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	
	if resp.Answers.Domain.Choice != "network" {
		t.Errorf("expected consensus to choose 'network', got %v", resp.Answers.Domain.Choice)
	}
}

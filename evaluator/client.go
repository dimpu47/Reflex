package evaluator

import (
	"context"
)

// Client defines the interface for interacting with the AI Provider API (OpenAI-compatible).
type Client interface {
	Evaluate(ctx context.Context, req EvaluationRequest) (*EvaluationResponse, error)
}

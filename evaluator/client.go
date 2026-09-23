package evaluator

import (
	"context"
)

// Client defines the interface for interacting with the Jev API.
type Client interface {
	Evaluate(ctx context.Context, req EvaluationRequest) (*EvaluationResponse, error)
}

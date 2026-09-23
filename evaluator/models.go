package evaluator

import "encoding/json"

// EvaluationRequest is the payload sent to the Jev API.
type EvaluationRequest struct {
	State     json.RawMessage `json:"state"`
	Questions []string        `json:"questions"`
}

// Choice represents a categorical evaluation from evaluator.
type Choice struct {
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// Score represents a numerical evaluation from evaluator.
type Score struct {
	Score float64 `json:"score"`
}

// Noul represents a boolean probability from evaluator.
type Noul struct {
	Probability float64 `json:"probability"`
}

// Answers holds the specific question evaluations.
type Answers struct {
	Domain          Choice `json:"domain"`
	Severity        Score  `json:"severity"`
	AutoRecoverable Noul   `json:"auto_recoverable"`
}

// Usage tracks the token usage for the evaluation.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// EvaluationResponse is the parsed response from the Jev API.
type EvaluationResponse struct {
	Answers Answers `json:"answers"`
	Usage   Usage   `json:"usage"`
}

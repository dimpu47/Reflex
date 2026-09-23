package router

import (
	"jev-proxy/ingest"
	"jev-proxy/evaluator"
	"testing"
)

// mockHandler implements runbooks.Handler for testing.
type mockHandler struct {
	escalated      bool
	autoRemediated bool
}

func (m *mockHandler) AutoRemediate(alert ingest.Alert, eval evaluator.EvaluationResponse) {
	m.autoRemediated = true
}

func (m *mockHandler) Escalate(alert ingest.Alert, eval evaluator.EvaluationResponse) {
	m.escalated = true
}

func TestRouterLogic(t *testing.T) {
	tests := []struct {
		name           string
		domain         evaluator.Choice
		severity       evaluator.Score
		recoverable    evaluator.Noul
		wantRemediate  bool
		wantEscalate   bool
	}{
		{
			name: "Condition A: Auto-Remediate (Happy Path)",
			domain: evaluator.Choice{Choice: "database", Confidence: 0.96},
			severity: evaluator.Score{Score: 6.8},
			recoverable: evaluator.Noul{Probability: 0.93},
			wantRemediate: true,
			wantEscalate: false,
		},
		{
			name: "Condition B: Escalate due to high severity",
			domain: evaluator.Choice{Choice: "database", Confidence: 0.96},
			severity: evaluator.Score{Score: 8.5},
			recoverable: evaluator.Noul{Probability: 0.93},
			wantRemediate: false,
			wantEscalate: true,
		},
		{
			name: "Condition B: Escalate due to low confidence",
			domain: evaluator.Choice{Choice: "database", Confidence: 0.90},
			severity: evaluator.Score{Score: 6.8},
			recoverable: evaluator.Noul{Probability: 0.93},
			wantRemediate: false,
			wantEscalate: true,
		},
		{
			name: "Fallback: Escalate due to low recoverability",
			domain: evaluator.Choice{Choice: "database", Confidence: 0.96},
			severity: evaluator.Score{Score: 6.8},
			recoverable: evaluator.Noul{Probability: 0.85},
			wantRemediate: false,
			wantEscalate: true,
		},
		{
			name: "Fallback: Escalate due to unknown domain",
			domain: evaluator.Choice{Choice: "", Confidence: 0.98},
			severity: evaluator.Score{Score: 5.0},
			recoverable: evaluator.Noul{Probability: 0.99},
			wantRemediate: false,
			wantEscalate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &mockHandler{}
			config := Config{
				MinConfidence:  0.95,
				MaxSeverity:    8.0,
				MinRecoverable: 0.90,
			}
			r := &Router{Handler: handler, config: config} // Events channel not needed for direct testing of route()

			alert := ingest.Alert{ID: "test-alert"}
			eval := evaluator.EvaluationResponse{
				Answers: evaluator.Answers{
					Domain:          tt.domain,
					Severity:        tt.severity,
					AutoRecoverable: tt.recoverable,
				},
			}

			r.route(alert, eval)

			if handler.autoRemediated != tt.wantRemediate {
				t.Errorf("got AutoRemediate = %v, want %v", handler.autoRemediated, tt.wantRemediate)
			}
			if handler.escalated != tt.wantEscalate {
				t.Errorf("got Escalate = %v, want %v", handler.escalated, tt.wantEscalate)
			}
		})
	}
}

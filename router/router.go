package router

import (
	"jev-proxy/ingest"
	"jev-proxy/evaluator"
	"jev-proxy/runbooks"
	"sync"
)

// Event represents an alert that has been evaluated by evaluator.
type Event struct {
	Alert ingest.Alert
	Eval  evaluator.EvaluationResponse
}

// Config defines the dynamic thresholds for routing decisions.
type Config struct {
	MinConfidence  float64
	MaxSeverity    float64
	MinRecoverable float64
}

// Router listens for evaluated events and routes them to the appropriate handler.
type Router struct {
	Handler runbooks.Handler
	Events  <-chan Event
	wg      *sync.WaitGroup
	config  Config
}

// NewRouter creates a new Router pipeline.
func NewRouter(events <-chan Event, handler runbooks.Handler, wg *sync.WaitGroup, config Config) *Router {
	return &Router{
		Events:  events,
		Handler: handler,
		wg:      wg,
		config:  config,
	}
}

// Start begins processing events concurrently.
func (r *Router) Start() {
	for event := range r.Events {
		r.route(event.Alert, event.Eval)
		r.wg.Done()
	}
}

// route evaluates the Jev response against confidence-gated thresholds.
func (r *Router) route(alert ingest.Alert, eval evaluator.EvaluationResponse) {
	domain := eval.Answers.Domain
	severity := eval.Answers.Severity
	recoverable := eval.Answers.AutoRecoverable

	// Condition B: Human Escalation
	// If confidence < MinConfidence or severity >= MaxSeverity, route to escalation payload
	if domain.Confidence < r.config.MinConfidence || severity.Score >= r.config.MaxSeverity {
		r.Handler.Escalate(alert, eval)
		return
	}

	// Condition A: Auto-Remediate
	// If domain is known, severity < MaxSeverity, auto_recoverable is true (prob > MinRecoverable), and overall confidence >= MinConfidence
	if domain.Choice != "" && severity.Score < r.config.MaxSeverity && recoverable.Probability > r.config.MinRecoverable && domain.Confidence >= r.config.MinConfidence {
		r.Handler.AutoRemediate(alert, eval)
		return
	}

	// Default fallback to escalation if thresholds don't align cleanly
	r.Handler.Escalate(alert, eval)
}

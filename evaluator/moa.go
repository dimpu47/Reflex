package evaluator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"jev-proxy/metrics"
	"jev-proxy/memory"
)

// MoAClient implements the Mixture-of-Agents consensus routing.
type MoAClient struct {
	baseClient  Client
	memoryStore *memory.Store
}

func NewMoAClient(base Client, memStore *memory.Store) *MoAClient {
	return &MoAClient{baseClient: base, memoryStore: memStore}
}

func (m *MoAClient) Evaluate(ctx context.Context, req EvaluationRequest) (*EvaluationResponse, error) {
	start := time.Now()
	defer func() {
		metrics.MoALatency.Observe(time.Since(start).Seconds())
	}()

	// ==========================================
	// TIER 0: ZERO-BURDEN RAG (Memory Retrieval)
	// ==========================================
	if m.memoryStore != nil {
		fmt.Println("🧠 [RAG] Fetching embeddings for alert state...")
		// Use Ollama to get embedding for the raw state
		emb, err := memory.GetEmbedding(ctx, string(req.State))
		if err == nil {
			similar := m.memoryStore.SearchSimilar(emb, 3)
			if len(similar) > 0 {
				fmt.Printf("🔍 [RAG] Found %d historically similar incidents. Injecting into context.\n", len(similar))
				// Inject into the State payload
				var stateMap map[string]interface{}
				if err := json.Unmarshal(req.State, &stateMap); err == nil {
					stateMap["similar_past_incidents"] = similar
					if enriched, err := json.Marshal(stateMap); err == nil {
						req.State = enriched
					}
				}
			}
		} else {
			fmt.Printf("⚠️  [RAG] Failed to get embeddings: %v\n", err)
		}
	}

	// ==========================================
	// TIER 1: FAST PATH (Single cheap LLM call)
	// ==========================================
	fastResp, err := m.baseClient.Evaluate(ctx, req)
	if err != nil {
		fmt.Printf("⚠️ [FAST-PATH] Evaluation failed: %v. Aborting MoA.\n", err)
		return nil, err
	}
	if fastResp.Answers.Domain.Confidence >= 0.95 {
		fmt.Printf("⚡ [FAST-PATH] High confidence (%.2f) on domain %s. Skipping expensive MoA consensus.\n", 
			fastResp.Answers.Domain.Confidence, fastResp.Answers.Domain.Choice)
		return fastResp, nil
	}

	fmt.Printf("🤔 [SLOW-PATH] Confidence is borderline (%.2f). Invoking Mixture-of-Agents consensus...\n", fastResp.Answers.Domain.Confidence)

	// ==========================================
	// TIER 2: SLOW PATH (Multi-Agent Consensus)
	// ==========================================
	personas := []string{"Pessimist", "Optimist", "Judge"}
	
	type result struct {
		resp *EvaluationResponse
		err  error
	}
	
	results := make(chan result, len(personas))
	var wg sync.WaitGroup
	
	for _, p := range personas {
		wg.Add(1)
		go func(persona string) {
			defer wg.Done()
			
			// In production, we would inject the persona via system prompts.
			// Here we simulate by calling the base client (which mocks responses).
			resp, err := m.baseClient.Evaluate(ctx, req)
			results <- result{resp, err}
		}(p)
	}
	
	wg.Wait()
	close(results)
	
	var validResponses []*EvaluationResponse
	for res := range results {
		if res.err == nil {
			validResponses = append(validResponses, res.resp)
		}
	}
	
	if len(validResponses) == 0 {
		return nil, fmt.Errorf("all MoA personas failed to evaluate")
	}
	
	var totalSeverity float64
	var totalRecoverable float64
	domainVotes := make(map[string]int)
	
	for _, resp := range validResponses {
		totalSeverity += resp.Answers.Severity.Score
		totalRecoverable += resp.Answers.AutoRecoverable.Probability
		domainVotes[resp.Answers.Domain.Choice]++
	}
	
	avgSeverity := totalSeverity / float64(len(validResponses))
	avgRecoverable := totalRecoverable / float64(len(validResponses))
	
	// Calculate variance in severity
	var variance float64
	for _, resp := range validResponses {
		diff := resp.Answers.Severity.Score - avgSeverity
		variance += diff * diff
	}
	variance = variance / float64(len(validResponses))
	
	// Determine consensus domain
	var consensusDomain string
	var maxVotes int
	for domain, votes := range domainVotes {
		if votes > maxVotes {
			maxVotes = votes
			consensusDomain = domain
		}
	}
	
	// If variance is high or domain is contested, set confidence to 0
	confidence := 0.95
	if variance > 2.0 || maxVotes < len(validResponses) {
		confidence = 0.0 // Force human escalation
		fmt.Printf("⚠️  MoA Consensus Failed! Variance: %.2f, Votes for %s: %d/%d\n", variance, consensusDomain, maxVotes, len(validResponses))
	} else {
		fmt.Printf("🤝 MoA Consensus Reached! Variance: %.2f\n", variance)
	}
	
	finalResp := &EvaluationResponse{
		Answers: Answers{
			Domain: Choice{
				Choice:     consensusDomain,
				Confidence: confidence,
			},
			Severity: Score{
				Score: avgSeverity,
			},
			AutoRecoverable: Noul{
				Probability: avgRecoverable,
			},
		},
		Usage: Usage{
			InputTokens:  validResponses[0].Usage.InputTokens * len(validResponses),
			OutputTokens: validResponses[0].Usage.OutputTokens * len(validResponses),
		},
	}
	
	return finalResp, nil
}

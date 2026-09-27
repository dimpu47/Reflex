package evaluator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// RealClient implements Client using the actual AI Provider API or a compatible endpoint (like Ollama/vLLM).
type RealClient struct {
	APIKey     string
	Endpoint   string
	HTTPClient *http.Client
}

// NewRealClient creates a new RealClient.
func NewRealClient(apiKey string, customEndpoint string) *RealClient {
	ollamaHost := os.Getenv("OLLAMA_HOST")
	if ollamaHost == "" {
		ollamaHost = "http://localhost:11434"
	}
	endpoint := ollamaHost + "/api/generate" // Default Ollama local endpoint
	if customEndpoint != "" {
		endpoint = customEndpoint
	}

	return &RealClient{
		APIKey:     apiKey,
		Endpoint:   endpoint,
		HTTPClient: &http.Client{},
	}
}

// Evaluate sends a real HTTP request to the AI API (e.g. Ollama, OpenAI).
func (c *RealClient) Evaluate(ctx context.Context, req EvaluationRequest) (*EvaluationResponse, error) {
	prompt := fmt.Sprintf("You are a production SRE triage AI. Analyze this alert and respond ONLY with valid JSON.\n\nAlert: %s\n\nRules:\n- domain.choice MUST be one of: database, application, network, infrastructure. NEVER return unknown.\n- Redis/PostgreSQL/MySQL down -> domain: database\n- Pod/Deployment crash -> domain: application\n- severity.score: float 1.0-10.0 (RedisDown=7.0, PodCrash=6.0)\n- auto_recoverable.probability: float 0.0-1.0 (restart can fix=0.85, data loss=0.1)\n- domain.confidence: your classification confidence 0.0-1.0\n\nRespond with ONLY this JSON structure, no other text:\n{\"answers\": {\"domain\": {\"choice\": \"database\", \"confidence\": 0.95}, \"severity\": {\"score\": 7.0}, \"auto_recoverable\": {\"probability\": 0.85}}, \"usage\": {\"input_tokens\": 0, \"output_tokens\": 0}}", string(req.State))
	
	ollamaReq := map[string]interface{}{
		"model":  "gemma4:latest",
		"prompt": prompt,
		"stream": false,
		"format": "json",
	}

	payload, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var ollamaResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	respStr, ok := ollamaResp["response"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid response format from Ollama")
	}

	var evalResp EvaluationResponse
	if err := json.Unmarshal([]byte(respStr), &evalResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON from LLM: %w", err)
	}

	return &evalResp, nil
}

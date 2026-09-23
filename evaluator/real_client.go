package evaluator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// RealClient implements Client using the actual Jev API or a compatible endpoint (like Laya).
type RealClient struct {
	APIKey     string
	Endpoint   string
	HTTPClient *http.Client
}

// NewRealClient creates a new RealClient.
func NewRealClient(apiKey string, customEndpoint string) *RealClient {
	endpoint := "https://api.typesafe.ai/v1/evaluate" // Default Jev Evaluation Endpoint
	if customEndpoint != "" {
		endpoint = customEndpoint
	}

	return &RealClient{
		APIKey:     apiKey,
		Endpoint:   endpoint,
		HTTPClient: &http.Client{},
	}
}

// Evaluate sends a real HTTP request to the Jev API (or compatible local Laya proxy).
func (c *RealClient) Evaluate(ctx context.Context, req EvaluationRequest) (*EvaluationResponse, error) {
	payload, err := json.Marshal(req)
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

	var evalResp EvaluationResponse
	if err := json.NewDecoder(resp.Body).Decode(&evalResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &evalResp, nil
}

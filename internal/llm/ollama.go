package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ollama/ollama/api"
)

type OllamaClient struct {
	client *api.Client
	model  string
}

func NewOllamaClient(host string, model string) (*OllamaClient, error) {
	if host == "" {
		host = "http://localhost:11434"
	}
	if model == "" {
		model = "deepseek-v2:latest"
	}

	u, err := url.Parse(host)
	if err != nil {
		return nil, err
	}

	client := api.NewClient(u, http.DefaultClient)
	return &OllamaClient{
		client: client,
		model:  model,
	}, nil
}

func (c *OllamaClient) Generate(prompt string) (string, error) {
	ctx := context.Background()
	req := &api.GenerateRequest{
		Model:  c.model,
		Prompt: prompt,
		Stream: new(bool), // false
	}
	*req.Stream = false

	var responseContent string
	fn := func(resp api.GenerateResponse) error {
		responseContent = resp.Response
		return nil
	}

	if err := c.client.Generate(ctx, req, fn); err != nil {
		return "", err
	}

	return responseContent, nil
}

func (c *OllamaClient) Validate(taskContent string, proposal string) (bool, error) {
	ctx := context.Background()
	prompt := fmt.Sprintf(`Task: %s
Proposal: %s

Does the proposal accurately and correctly answer the task?
Respond with JSON object: {"valid": boolean, "reason": string}.
`, taskContent, proposal)

	// Format expects json.RawMessage in this version
	format := json.RawMessage(`"json"`)

	req := &api.GenerateRequest{
		Model:  c.model,
		Prompt: prompt,
		Format: format,
		Stream: new(bool),
	}
	*req.Stream = false

	var responseContent string
	fn := func(resp api.GenerateResponse) error {
		responseContent = resp.Response
		return nil
	}

	if err := c.client.Generate(ctx, req, fn); err != nil {
		return false, err
	}

	var validation validationResponse
	if err := json.Unmarshal([]byte(responseContent), &validation); err != nil {
		return false, fmt.Errorf("failed to parse validation response: %w", err)
	}

	return validation.Valid, nil
}

func (c *OllamaClient) Aggregate(taskContent string, answers []string) (string, error) {
	prompt := fmt.Sprintf("Task: %s\n\nAnswers:\n", taskContent)
	for i, ans := range answers {
		prompt += fmt.Sprintf("Answer %d: %s\n", i+1, ans)
	}
	prompt += "\nAggregate these answers into a single, high-quality response."
	return c.Generate(prompt)
}

func (c *OllamaClient) HealthCheck() error {
	ctx := context.Background()
	// Simply list models
	_, err := c.client.List(ctx)
	return err
}

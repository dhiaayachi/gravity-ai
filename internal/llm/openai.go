package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	openai "github.com/sashabaranov/go-openai"
)

type OpenAIClient struct {
	client *openai.Client
	model  string
}

func NewOpenAIClient(apiKey string, model string) *OpenAIClient {
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if model == "" {
		model = openai.GPT3Dot5Turbo
	}
	return &OpenAIClient{
		client: openai.NewClient(apiKey),
		model:  model,
	}
}

func (c *OpenAIClient) Generate(prompt string) (string, error) {
	resp, err := c.client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model: c.model,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleUser,
					Content: prompt,
				},
			},
		},
	)

	if err != nil {
		return "", err
	}

	return resp.Choices[0].Message.Content, nil
}

type validationResponse struct {
	Valid  bool   `json:"valid"`
	Reason string `json:"reason"`
}

func (c *OpenAIClient) Validate(taskContent string, proposal string) (bool, error) {
	prompt := fmt.Sprintf(`Task: %s
Proposal: %s

Does the proposal accurately and correctly answer the task?
Respond with JSON object: {"valid": boolean, "reason": string}.
`, taskContent, proposal)

	// Use JSON mode for reliability
	resp, err := c.client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model: c.model,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleSystem,
					Content: "You are a validator. Output only JSON.",
				},
				{
					Role:    openai.ChatMessageRoleUser,
					Content: prompt,
				},
			},
			ResponseFormat: &openai.ChatCompletionResponseFormat{
				Type: openai.ChatCompletionResponseFormatTypeJSONObject,
			},
		},
	)

	if err != nil {
		return false, err
	}

	var validation validationResponse
	content := resp.Choices[0].Message.Content

	// Retry parsing if needed (though JSON mode usually guarantees valid JSON)
	if err := json.Unmarshal([]byte(content), &validation); err != nil {
		// Simple retry logic could go here, but with JSON mode it's rarely needed for syntax
		// If fails, defaulting to false is safe
		return false, fmt.Errorf("failed to parse validation response: %w", err)
	}

	return validation.Valid, nil
}

func (c *OpenAIClient) Aggregate(taskContent string, answers []string) (string, error) {
	prompt := fmt.Sprintf("Task: %s\n\nAnswers:\n", taskContent)
	for i, ans := range answers {
		prompt += fmt.Sprintf("Answer %d: %s\n", i+1, ans)
	}
	prompt += "\nAggregate these answers into a single, high-quality response."
	return c.Generate(prompt)
}

func (c *OpenAIClient) HealthCheck() error {
	// Lightweight check: List models
	_, err := c.client.ListModels(context.Background())
	return err
}

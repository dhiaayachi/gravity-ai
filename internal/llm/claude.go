package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/liushuangls/go-anthropic/v2"
)

type ClaudeClient struct {
	client *anthropic.Client
	model  string
}

func NewClaudeClient(apiKey string, model string) *ClaudeClient {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if model == "" {
		model = "claude-3-opus-20240229"
	}
	return &ClaudeClient{
		client: anthropic.NewClient(apiKey),
		model:  model,
	}
}

func (c *ClaudeClient) Generate(prompt string) (string, error) {
	resp, err := c.client.CreateMessages(
		context.Background(),
		anthropic.MessagesRequest{
			Model: anthropic.Model(c.model),
			Messages: []anthropic.Message{
				{
					Role: anthropic.RoleUser,
					Content: []anthropic.MessageContent{
						anthropic.NewTextMessageContent(prompt),
					},
				},
			},
			MaxTokens: 1024,
		},
	)
	if err != nil {
		return "", err
	}

	if len(resp.Content) == 0 {
		return "", fmt.Errorf("empty response from claude")
	}

	if resp.Content[0].Text == nil {
		return "", fmt.Errorf("response content text is nil")
	}
	return *resp.Content[0].Text, nil
}

func (c *ClaudeClient) Validate(taskContent string, proposal string) (bool, error) {
	prompt := fmt.Sprintf(`Task: %s
Proposal: %s

Does the proposal accurately and correctly answer the task?
Respond with JSON object: {"valid": boolean, "reason": string}.
Output ONLY valid JSON.
`, taskContent, proposal)

	resp, err := c.client.CreateMessages(
		context.Background(),
		anthropic.MessagesRequest{
			Model: anthropic.Model(c.model),
			Messages: []anthropic.Message{
				{
					Role: anthropic.RoleUser,
					Content: []anthropic.MessageContent{
						anthropic.NewTextMessageContent(prompt),
					},
				},
			},
			MaxTokens: 1024,
		},
	)
	if err != nil {
		return false, err
	}

	if len(resp.Content) == 0 {
		return false, fmt.Errorf("empty response")
	}

	if resp.Content[0].Text == nil {
		return false, fmt.Errorf("response content text is nil")
	}

	content := *resp.Content[0].Text
	content = cleanJSONMarkdown(content)

	var validation validationResponse
	if err := json.Unmarshal([]byte(content), &validation); err != nil {
		return false, fmt.Errorf("failed to parse validation response: %w", err)
	}

	return validation.Valid, nil
}

func (c *ClaudeClient) Aggregate(taskContent string, answers []string) (string, error) {
	prompt := fmt.Sprintf("Task: %s\n\nAnswers:\n", taskContent)
	for i, ans := range answers {
		prompt += fmt.Sprintf("Answer %d: %s\n", i+1, ans)
	}
	prompt += "\nAggregate these answers into a single, high-quality response."
	return c.Generate(prompt)
}

func (c *ClaudeClient) HealthCheck() error {
	_, err := c.client.CreateMessages(
		context.Background(),
		anthropic.MessagesRequest{
			Model: anthropic.Model(c.model),
			Messages: []anthropic.Message{
				{
					Role: anthropic.RoleUser,
					Content: []anthropic.MessageContent{
						anthropic.NewTextMessageContent("Hello"), // Minimal prompt
					},
				},
			},
			MaxTokens: 1, // Minimize cost
		},
	)
	return err
}

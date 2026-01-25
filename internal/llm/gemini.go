package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

type GeminiClient struct {
	client *genai.Client
	model  string
}

func NewGeminiClient(apiKey string, model string) (*GeminiClient, error) {
	ctx := context.Background()
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if model == "" {
		model = "gemini-flash-latest"
	}

	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, err
	}

	return &GeminiClient{
		client: client,
		model:  model,
	}, nil
}

func (c *GeminiClient) Generate(prompt string) (string, error) {
	ctx := context.Background()
	model := c.client.GenerativeModel(c.model)

	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return "", err
	}

	return parseGeminiResponse(resp)
}

func (c *GeminiClient) Validate(taskContent string, proposal string) (bool, error) {
	ctx := context.Background()
	model := c.client.GenerativeModel(c.model)

	// Gemini doesn't strictly enforce JSON object mode like OpenAI yet in all versions,
	// but prompt engineering usually works.
	prompt := fmt.Sprintf(`Task: %s
Proposal: %s

Does the proposal accurately and correctly answer the task?
Respond with JSON object: {"valid": boolean, "reason": string}.
Output ONLY valid JSON.
`, taskContent, proposal)

	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return false, err
	}

	content, err := parseGeminiResponse(resp)
	if err != nil {
		return false, err
	}

	// Clean up potential Markdown code blocks
	content = cleanJSONMarkdown(content)

	var validation validationResponse
	if err := json.Unmarshal([]byte(content), &validation); err != nil {
		return false, fmt.Errorf("failed to parse validation response: %w, content: %s", err, content)
	}

	return validation.Valid, nil
}

func (c *GeminiClient) Aggregate(taskContent string, answers []string) (string, error) {
	prompt := fmt.Sprintf("Task: %s\n\nAnswers:\n", taskContent)
	for i, ans := range answers {
		prompt += fmt.Sprintf("Answer %d: %s\n", i+1, ans)
	}
	prompt += "\nAggregate these answers into a single, high-quality response."
	return c.Generate(prompt)
}

func (c *GeminiClient) HealthCheck() error {
	// Lightweight check: List models
	// genai client doesn't expose ListModels directly on the main client struct easily in all versions,
	// checking if we can create a model reference is a basic check, but ListModels is better if available.
	// For this pkg, we'll try a very basic generation with 1 token if ListModels isn't obvious,
	// but ListModels IS available via client.ListModels iterator.

	iter := c.client.ListModels(context.Background())
	_, err := iter.Next()
	if err != nil {
		return err
	}
	return nil
}

func parseGeminiResponse(resp *genai.GenerateContentResponse) (string, error) {
	if len(resp.Candidates) == 0 {
		return "", fmt.Errorf("no candidates returned")
	}
	if len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no content parts")
	}

	// Assuming text part
	if txt, ok := resp.Candidates[0].Content.Parts[0].(genai.Text); ok {
		return string(txt), nil
	}
	return "", fmt.Errorf("response content was not text")
}

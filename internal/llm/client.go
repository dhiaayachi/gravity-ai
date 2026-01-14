package llm

import "strings"

// Client is the interface for interacting with the backend LLM
type Client interface {
	Generate(prompt string) (string, error)
	Validate(taskContent string, proposal string) (bool, error)
	HealthCheck() error
}

// cleanJSONMarkdown removes markdown code block formatting for JSON
func cleanJSONMarkdown(content string) string {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
}

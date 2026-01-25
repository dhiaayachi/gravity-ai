package llm

import "strings"

// Client is the interface for interacting with the backend LLM
type Client interface {
	Generate(prompt string) (string, error)
	Validate(taskContent string, proposal string) (bool, error)
	Aggregate(taskContent string, answers []string) (string, error)
	HealthCheck() error
}

// cleanJSONMarkdown removes Markdown code block formatting for JSON
func cleanJSONMarkdown(content string) string {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
}

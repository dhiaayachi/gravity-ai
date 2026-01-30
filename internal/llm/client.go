package llm

import "strings"

// Client is the interface for interacting with the backend LLM
type Client interface {
	Generate(prompt string) (string, error)
	Validate(taskContent string, proposal string) (valid bool, reasoning string, err error)
	Aggregate(taskContent string, answers []string) (string, error)
	Revise(taskContent string, proposal string, feedback []string) (string, error)
}

// cleanJSONMarkdown removes Markdown code block formatting for JSON
func cleanJSONMarkdown(content string) string {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
}

const aggregatePrompt = "\nAggregate these answers into a single, high-quality response. Try to reconcile all the answers into a single answer, if answers are contradictory return an error"
const validatePrompt = `Task: %s
Proposal: %s

History

Does the proposal accurately and correctly answer the task? If you find any flaw in the answer it's not valid
Respond with JSON object: {"valid": boolean, "reason": string}.
`

const revisePrompt = `
You are an expert reviser.
Original Task: 
%s
Original Proposals: 
%s
Feedback from peers:
%s

Please revise the proposal to address the feedback. Keep it concise.
`

package llm

import (
	"fmt"
	"sort"
	"strings"
)

// ValidationContext contains historical data for informed validation
type ValidationContext struct {
	// Proposals by round number
	Proposals map[int]string
	// Feedback by round number (list of reasoning from rejecting votes)
	Feedback map[int][]string
	// Current round being validated
	CurrentRound int
}

// FormatHistory returns a formatted string of the proposal history and feedback
func (vc *ValidationContext) FormatHistory() string {
	if vc == nil || len(vc.Proposals) <= 1 {
		return "" // No history if only current round exists
	}

	var sb strings.Builder
	sb.WriteString("\n--- Revision History ---\n")

	// Sort rounds for consistent output
	var rounds []int
	for r := range vc.Proposals {
		if r < vc.CurrentRound { // Only include past rounds
			rounds = append(rounds, r)
		}
	}
	sort.Ints(rounds)

	for _, round := range rounds {
		sb.WriteString(fmt.Sprintf("\n[Round %d Proposal]:\n%s\n", round, vc.Proposals[round]))
		if feedback, ok := vc.Feedback[round]; ok && len(feedback) > 0 {
			sb.WriteString(fmt.Sprintf("[Round %d Feedback]:\n", round))
			for i, fb := range feedback {
				sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, fb))
			}
		}
	}
	sb.WriteString("--- End History ---\n")
	return sb.String()
}

// Client is the interface for interacting with the backend LLM
type Client interface {
	Generate(prompt string) (string, error)
	Validate(taskContent string, proposal string, ctx *ValidationContext) (valid bool, reasoning string, err error)
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

Current Proposal (Round %d): %s
%s
Does the proposal accurately and correctly answer the task? Consider the revision history if provided.
If you find any flaw in the answer it's not valid.
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

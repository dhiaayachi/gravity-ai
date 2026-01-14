package llm

import "testing"

func TestCleanJSONMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no markdown",
			input:    `{"valid": true}`,
			expected: `{"valid": true}`,
		},
		{
			name:     "markdown json",
			input:    "```json\n{\"valid\": true}\n```",
			expected: `{"valid": true}`,
		},
		{
			name:     "markdown no language",
			input:    "```\n{\"valid\": true}\n```",
			expected: `{"valid": true}`,
		},
		{
			name:     "whitespace handling",
			input:    "  ```json\n{\"valid\": true}\n```  ",
			expected: `{"valid": true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanJSONMarkdown(tt.input)
			if got != tt.expected {
				t.Errorf("cleanJSONMarkdown() = %v, want %v", got, tt.expected)
			}
		})
	}
}

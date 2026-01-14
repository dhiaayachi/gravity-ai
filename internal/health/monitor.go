package health

import "github.com/dhiaayachi/gravity-ai/internal/llm"

// Monitor checks the health of the node components
type Monitor struct {
	llmClient llm.Client
}

// NewMonitor creates a new health monitor
func NewMonitor(client llm.Client) *Monitor {
	return &Monitor{
		llmClient: client,
	}
}

// IsHealthy returns true if all components are healthy
func (m *Monitor) IsHealthy() bool {
	// Check LLM backend
	if err := m.llmClient.HealthCheck(); err != nil {
		return false
	}
	return true
}

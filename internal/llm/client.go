package llm

import "errors"

// Client is the interface for interacting with the backend LLM
type Client interface {
	Generate(prompt string) (string, error)
	HealthCheck() error
}

// MockClient is a mock implementation
type MockClient struct {
	Healthy bool
}

func (m *MockClient) Generate(prompt string) (string, error) {
	if !m.Healthy {
		return "", errors.New("llm unhealthy")
	}
	return "Mock Answer for: " + prompt, nil
}

func (m *MockClient) HealthCheck() error {
	if !m.Healthy {
		return errors.New("connection failed")
	}
	return nil
}

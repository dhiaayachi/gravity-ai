package llm

import "errors"

// Client is the interface for interacting with the backend LLM
type Client interface {
	Generate(prompt string) (string, error)
	Validate(taskContent string, proposal string) (bool, error)
	HealthCheck() error
}

// MockClient is a mock implementation
type MockClient struct {
	Healthy         bool
	ValidationLogic func(taskContent, proposal string) bool
}

func (m *MockClient) Generate(prompt string) (string, error) {
	if !m.Healthy {
		return "", errors.New("llm unhealthy")
	}
	return "Mock Answer for: " + prompt, nil
}

func (m *MockClient) Validate(taskContent string, proposal string) (bool, error) {
	if !m.Healthy {
		return false, errors.New("llm unhealthy")
	}
	if m.ValidationLogic != nil {
		return m.ValidationLogic(taskContent, proposal), nil
	}
	return true, nil // Default to accept
}

func (m *MockClient) HealthCheck() error {
	if !m.Healthy {
		return errors.New("connection failed")
	}
	return nil
}

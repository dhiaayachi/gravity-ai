package mocks

import (
	"errors"

	"github.com/dhiaayachi/gravity-ai/internal/llm"
)

// MockLLM is a configurable mock
type MockLLM struct {
	Healthy         bool
	ValidationLogic func(taskContent, proposal string) bool
}

func (m *MockLLM) Generate(prompt string) (string, error) {
	if !m.Healthy {
		return "", errors.New("llm unhealthy")
	}
	return "Mock Answer for: " + prompt, nil
}

func (m *MockLLM) Validate(taskContent string, proposal string) (bool, string, error) {
	if !m.Healthy {
		return false, "", errors.New("llm unhealthy")
	}
	if m.ValidationLogic != nil {
		return m.ValidationLogic(taskContent, proposal), "mock reasoning", nil
	}
	return true, "mock approved", nil
}

func (m *MockLLM) Aggregate(_ string, _ []string) (string, error) {
	if !m.Healthy {
		return "", errors.New("llm unhealthy")
	}
	return "Aggregated Answer", nil
}

// YesMock always approves
type YesMock struct {
	MockLLM
}

func NewYesMock() llm.Client {
	return &YesMock{
		MockLLM: MockLLM{
			Healthy:         true,
			ValidationLogic: func(t, p string) bool { return true },
		},
	}
}

// NoMock always rejects
type NoMock struct {
	MockLLM
}

func NewNoMock() llm.Client {
	return &NoMock{
		MockLLM: MockLLM{
			Healthy:         true,
			ValidationLogic: func(t, p string) bool { return false },
		},
	}
}

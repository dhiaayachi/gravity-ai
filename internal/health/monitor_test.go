package health

import (
	"errors"
	"testing"

	"github.com/dhiaayachi/gravity-ai/internal/llm"
)

type mockClient struct {
	err error
}

func (m *mockClient) Generate(prompt string) (string, error)         { return "", nil }
func (m *mockClient) Validate(t, p string) (bool, error)             { return true, nil }
func (m *mockClient) Aggregate(t string, a []string) (string, error) { return "agg", nil }
func (m *mockClient) HealthCheck() error                             { return m.err }

var _ llm.Client = &mockClient{}

func TestMonitor_IsHealthy(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		healthy bool
	}{
		{"healthy", nil, true},
		{"unhealthy", errors.New("timeout"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMonitor(&mockClient{err: tt.err})
			if got := m.IsHealthy(); got != tt.healthy {
				t.Errorf("IsHealthy() = %v, want %v", got, tt.healthy)
			}
		})
	}
}

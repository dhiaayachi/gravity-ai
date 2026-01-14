package llm

// Client is the interface for interacting with the backend LLM
type Client interface {
	Generate(prompt string) (string, error)
	Validate(taskContent string, proposal string) (bool, error)
	HealthCheck() error
}

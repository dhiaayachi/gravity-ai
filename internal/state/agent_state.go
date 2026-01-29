package state

// AgentState represents the exposed state of the agent
type AgentState struct {
	ID          string `json:"id"`
	RaftState   string `json:"raft_state"`
	Reputation  int    `json:"reputation"`
	LLMProvider string `json:"llm_provider"`
	LLMModel    string `json:"llm_model"`
}

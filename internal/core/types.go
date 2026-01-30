package core

import "time"

// TaskStatus represents the state of a task in the system
type TaskStatus string

const (
	TaskStatusAdmitted   TaskStatus = "admitted"
	TaskStatusBrainstorm TaskStatus = "brainstorm"
	TaskStatusProposal   TaskStatus = "proposal"
	TaskStatusVote       TaskStatus = "vote"
	TaskStatusDone       TaskStatus = "done"
	TaskStatusFailed     TaskStatus = "failed"
)

// Task represents a unit of work to be processed by the agents
type Task struct {
	ID              string         `json:"id"`
	Content         string         `json:"content"`
	Status          TaskStatus     `json:"status"`
	Requester       string         `json:"requester"` // Could be user or another system
	CreatedAt       time.Time      `json:"created_at"`
	Result          string         `json:"result,omitempty"`
	ConfidenceScore float64        `json:"confidence_score,omitempty"`
	Round           int            `json:"round"`
	Proposals       map[int]string `json:"proposals,omitempty"` // History of proposals by round
}

// Answer represents a response from a single agent during Brainstorm phase
type Answer struct {
	TaskID  string `json:"task_id"`
	AgentID string `json:"agent_id"`
	Content string `json:"content"`
}

// Vote represents a vote from an agent during the Vote phase
type Vote struct {
	TaskID    string `json:"task_id"`
	AgentID   string `json:"agent_id"`
	Accepted  bool   `json:"accepted"`
	Reasoning string `json:"reasoning,omitempty"` // Why the agent voted this way
	Rebuttal  string `json:"rebuttal,omitempty"`  // Counter-proposal (if rejected)
	Round     int    `json:"round"`
}

// AgentConfig represents the persistent state of an agent
type AgentConfig struct {
	ID         string `json:"id"`
	Reputation int    `json:"reputation"`
	Address    string `json:"address"` // Network address for Raft/RPC
}

type AgentMetadata struct {
	ID          string `json:"id"`
	LLMProvider string `json:"llm_provider"`
	LLMModel    string `json:"llm_model"`
}

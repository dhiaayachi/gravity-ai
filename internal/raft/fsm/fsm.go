package fsm

import (
	"encoding/json"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	"github.com/hashicorp/raft"
)

type FSM interface {
	EventsConsumer() chan Event
	GetTask(id string) (*core.Task, error)
	GetTaskAnswers(id string) ([]core.Answer, error)
	GetTaskVotes(id string) (map[string]core.Vote, error)
	GetReputation(id string) int
	GetAllReputations() map[string]int
	GetMetadata(id string) *core.AgentMetadata
}

// Helper types for Logs and Snapshots

type CommandType string

const (
	CommandTypeUpdateReputation CommandType = "update_reputation"
	CommandTypeAdmitTask        CommandType = "admit_task"
	CommandTypeSubmitAnswer     CommandType = "submit_answer"
	CommandTypeUpdateTask       CommandType = "update_task"
	CommandTypeSubmitVote       CommandType = "submit_vote"
	CommandTypeUpdateMetadata   CommandType = "update_metadata"
)

type LogCommand struct {
	Type    CommandType     `json:"type"`
	AgentID string          `json:"agent_id,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
}

type Snapshot struct {
	Reputations map[string]int
	Metadata    map[string]core.AgentMetadata
	Tasks       map[string]*core.Task
}

func (s *Snapshot) Persist(sink raft.SnapshotSink) error {
	err := func() error {
		b, err := json.Marshal(s) // Use the struct itself
		if err != nil {
			return err
		}
		if _, err := sink.Write(b); err != nil {
			return err
		}
		return nil
	}()
	if err != nil {
		_ = sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *Snapshot) Release() {}

type SnapshotData struct {
	Reputations map[string]int                `json:"reputations"`
	Metadata    map[string]core.AgentMetadata `json:"metadata"`
	Tasks       map[string]*core.Task         `json:"tasks"`
}

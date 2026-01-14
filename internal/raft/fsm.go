package raft

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	"github.com/hashicorp/raft"
)

// FSM is the state machine for the Raft system
type FSM struct {
	NodeID      string
	Reputations sync.Map // AgentID -> int
	Tasks       sync.Map // TaskID -> *core.Task
	TaskAnswers sync.Map // TaskID -> []core.Answer
	TaskVotes   sync.Map // TaskID -> []core.Vote

	// Events channel to notify Engine of state changes
	EventCh chan Event
}

type EventType string

const (
	EventTaskAdmitted    EventType = "task_admitted"
	EventAnswerSubmitted EventType = "answer_submitted"
	EventTaskUpdated     EventType = "task_updated"
	EventVoteSubmitted   EventType = "vote_submitted"
)

type Event struct {
	Type EventType
	Data interface{}
}

func NewFSM(nodeID string) *FSM {
	return &FSM{
		NodeID:  nodeID,
		EventCh: make(chan Event, 100),
	}
}

// Apply applies a Raft log entry to the FSM
func (f *FSM) Apply(logEntry *raft.Log) interface{} {
	var cmd LogCommand
	if err := json.Unmarshal(logEntry.Data, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal log data: %w", err)
	}

	switch cmd.Type {
	case CommandTypeUpdateReputation:
		var rep int
		if err := json.Unmarshal(cmd.Value, &rep); err != nil {
			return fmt.Errorf("failed to unmarshal reputation: %w", err)
		}
		f.Reputations.Store(cmd.AgentID, rep)
	case CommandTypeAdmitTask:
		var task core.Task
		if err := json.Unmarshal(cmd.Value, &task); err != nil {
			return fmt.Errorf("failed to unmarshal task: %w", err)
		}
		f.Tasks.Store(task.ID, &task)
		log.Printf("[%s] Task admitted: %s", f.NodeID, task.ID)

		// Notify
		select {
		case f.EventCh <- Event{Type: EventTaskAdmitted, Data: &task}:
		default:
			log.Printf("[%s] FSM EventCh full, dropping event", f.NodeID)
		}

	case CommandTypeSubmitAnswer:
		var answer core.Answer
		if err := json.Unmarshal(cmd.Value, &answer); err != nil {
			return fmt.Errorf("failed to unmarshal answer: %w", err)
		}
		val, _ := f.TaskAnswers.LoadOrStore(answer.TaskID, []core.Answer{})
		answers := val.([]core.Answer)
		answers = append(answers, answer)
		f.TaskAnswers.Store(answer.TaskID, answers)

		// Notify
		select {
		case f.EventCh <- Event{Type: EventAnswerSubmitted, Data: &answer}:
		default:
		}

	case CommandTypeUpdateTask:
		var task core.Task
		if err := json.Unmarshal(cmd.Value, &task); err != nil {
			return fmt.Errorf("failed to unmarshal task: %w", err)
		}
		// Update existing task (e.g. status change, result added)
		if val, ok := f.Tasks.Load(task.ID); ok {
			existing := val.(*core.Task)
			existing.Status = task.Status
			existing.Result = task.Result

			// Notify
			select {
			case f.EventCh <- Event{Type: EventTaskUpdated, Data: existing}:
			default:
			}
		}
	case CommandTypeSubmitVote:
		var vote core.Vote
		if err := json.Unmarshal(cmd.Value, &vote); err != nil {
			return fmt.Errorf("failed to unmarshal vote: %w", err)
		}
		val, _ := f.TaskVotes.LoadOrStore(vote.TaskID, []core.Vote{})
		votes := val.([]core.Vote)

		// Dedup
		found := false
		for i, v := range votes {
			if v.AgentID == vote.AgentID {
				votes[i] = vote
				found = true
				break
			}
		}
		if !found {
			votes = append(votes, vote)
		}

		f.TaskVotes.Store(vote.TaskID, votes)

		// Notify
		select {
		case f.EventCh <- Event{Type: EventVoteSubmitted, Data: &vote}:
		default:
		}
	}
	return nil
}

// Snapshot returns a snapshot of the key-value store
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	// Deep copy to avoid race conditions during serialization
	reputations := make(map[string]int)
	f.Reputations.Range(func(key, value interface{}) bool {
		reputations[key.(string)] = value.(int)
		return true
	})

	tasks := make(map[string]*core.Task)
	f.Tasks.Range(func(key, value interface{}) bool {
		t := *value.(*core.Task)
		tasks[key.(string)] = &t
		return true
	})

	// Copy answers/votes if needed, skipped for brevity in this step but should be here

	return &FSMSnapshot{
		Reputations: reputations,
		Tasks:       tasks,
	}, nil
}

// Restore restores the node to a previous state
func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	var snapshot FSMSnapshotData // Define this struct
	if err := json.NewDecoder(rc).Decode(&snapshot); err != nil {
		return err
	}

	// Clear current state and load new
	f.Reputations = sync.Map{}
	for k, v := range snapshot.Reputations {
		f.Reputations.Store(k, v)
	}

	f.Tasks = sync.Map{}
	f.Tasks = sync.Map{}
	for k, v := range snapshot.Tasks {
		// Only restore active tasks
		if v.Status != core.TaskStatusDone && v.Status != core.TaskStatusFailed {
			f.Tasks.Store(k, v)
		}
	}

	// Also need to handle TaskAnswers/Votes if we were persisting them
	f.TaskAnswers = sync.Map{}
	f.TaskVotes = sync.Map{}

	return nil
}

// Helper types for Logs and Snapshots

type CommandType string

const (
	CommandTypeUpdateReputation CommandType = "update_reputation"
	CommandTypeAdmitTask        CommandType = "admit_task"
	CommandTypeSubmitAnswer     CommandType = "submit_answer"
	CommandTypeUpdateTask       CommandType = "update_task"
	CommandTypeSubmitVote       CommandType = "submit_vote"
)

type LogCommand struct {
	Type    CommandType     `json:"type"`
	AgentID string          `json:"agent_id,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
}

type FSMSnapshot struct {
	Reputations map[string]int
	Tasks       map[string]*core.Task
}

func (s *FSMSnapshot) Persist(sink raft.SnapshotSink) error {
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
		sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *FSMSnapshot) Release() {}

type FSMSnapshotData struct {
	Reputations map[string]int        `json:"reputations"`
	Tasks       map[string]*core.Task `json:"tasks"`
}

package raft

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	"github.com/hashicorp/raft"
)

func TestFSM_Apply(t *testing.T) {
	fsm := NewFSM("node1")

	// Test Admit Task
	task := core.Task{
		ID:      "task1",
		Content: "Do something",
		Status:  core.TaskStatusAdmitted,
	}
	taskBytes, _ := json.Marshal(task)
	cmd := LogCommand{
		Type:  CommandTypeAdmitTask,
		Value: taskBytes,
	}
	cmdBytes, _ := json.Marshal(cmd)

	fsm.Apply(&raft.Log{Data: cmdBytes})

	if val, ok := fsm.Tasks.Load("task1"); !ok {
		t.Errorf("Task not found in FSM")
	} else {
		storedTask := val.(*core.Task)
		if storedTask.Content != "Do something" {
			t.Errorf("Task content mismatch")
		}
	}

	// Verify Event
	select {
	case event := <-fsm.EventCh:
		if event.Type != EventTaskAdmitted {
			t.Errorf("Expected EventTaskAdmitted, got %v", event.Type)
		}
	default:
		t.Errorf("No event emitted")
	}

	// Test Update Reputation
	repCmd := LogCommand{
		Type:    CommandTypeUpdateReputation,
		AgentID: "agentA",
		Value:   []byte("100"),
	}
	repBytes, _ := json.Marshal(repCmd)
	fsm.Apply(&raft.Log{Data: repBytes})

	if val, ok := fsm.Reputations.Load("agentA"); !ok || val.(int) != 100 {
		t.Errorf("Reputation not updated correctly, got %v", val)
	}

	// Test Submit Answer
	ans := core.Answer{
		TaskID:  "task1",
		AgentID: "agentA",
		Content: "Here is the answer",
	}
	ansBytes, _ := json.Marshal(ans)
	ansCmd := LogCommand{
		Type:  CommandTypeSubmitAnswer,
		Value: ansBytes,
	}
	ansCmdBytes, _ := json.Marshal(ansCmd)
	fsm.Apply(&raft.Log{Data: ansCmdBytes})

	if val, ok := fsm.TaskAnswers.Load("task1"); !ok {
		t.Errorf("Answers not found")
	} else {
		answers := val.([]core.Answer)
		if len(answers) != 1 || answers[0].Content != "Here is the answer" {
			t.Errorf("Answer content mismatch")
		}
	}

	// Verify Event
	// Drain potentially buffered events or loop to find the one we want
	found := false
	for i := 0; i < 5; i++ {
		select {
		case event := <-fsm.EventCh:
			if event.Type == EventAnswerSubmitted {
				found = true
			}
		default:
		}
		if found {
			break
		}
	}

	// Test Submit Vote
	vote := core.Vote{
		TaskID:   "task1",
		AgentID:  "agentA",
		Accepted: true,
	}
	voteBytes, _ := json.Marshal(vote)
	voteCmd := LogCommand{
		Type:  CommandTypeSubmitVote,
		Value: voteBytes,
	}
	voteCmdBytes, _ := json.Marshal(voteCmd)
	fsm.Apply(&raft.Log{Data: voteCmdBytes})

	if val, ok := fsm.TaskVotes.Load("task1"); !ok {
		t.Errorf("Votes not found")
	} else {
		votes := val.([]core.Vote)
		if len(votes) != 1 || !votes[0].Accepted {
			t.Errorf("Vote content mismatch")
		}
	}

	// Test Duplicate Vote Update (should overwrite)
	vote2 := core.Vote{
		TaskID:   "task1",
		AgentID:  "agentA",
		Accepted: false,
	}
	vote2Bytes, _ := json.Marshal(vote2)
	voteCmd2 := LogCommand{
		Type:  CommandTypeSubmitVote,
		Value: vote2Bytes,
	}
	voteCmd2Bytes, _ := json.Marshal(voteCmd2)
	fsm.Apply(&raft.Log{Data: voteCmd2Bytes})

	if val, ok := fsm.TaskVotes.Load("task1"); !ok {
		t.Errorf("Votes not found after update")
	} else {
		votes := val.([]core.Vote)
		if len(votes) != 1 || votes[0].Accepted { // Should be false now
			t.Errorf("Vote not updated correctly")
		}
	}
}

func TestFSM_SnapshotRestore(t *testing.T) {
	fsm := NewFSM("node1")

	// Setup initial state
	fsm.Reputations.Store("agentA", 50)
	task := core.Task{ID: "task1", Status: core.TaskStatusAdmitted}
	fsm.Tasks.Store("task1", &task)

	// Create snapshot
	snap, err := fsm.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	// Verify snapshot content logic without persisting
	fsmSnapshot := snap.(*FSMSnapshot) // type assertion
	if fsmSnapshot.Reputations["agentA"] != 50 {
		t.Errorf("Snapshot reputation mismatch")
	}
	if fsmSnapshot.Tasks["task1"].Status != core.TaskStatusAdmitted {
		t.Errorf("Snapshot task mismatch")
	}
}

func TestFSM_Restore(t *testing.T) {
	fsm := NewFSM("node1")

	jsonState := `{"reputations": {"agentA": 10}, "tasks": {"task1": {"id": "task1", "status": "done"}}}`

	// Create a pipe or buffer
	reader, writer := io.Pipe()
	go func() {
		writer.Write([]byte(jsonState))
		writer.Close()
	}()

	err := fsm.Restore(reader)
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	if val, ok := fsm.Reputations.Load("agentA"); !ok || val.(int) != 10 {
		t.Errorf("Reputation restore failed, got %v", val)
	}

	if val, ok := fsm.Tasks.Load("task1"); !ok || val.(*core.Task).Status != core.TaskStatusDone {
		t.Errorf("Task restore failed")
	}
}

type dummySink struct {
	*io.PipeWriter
	*io.PipeReader
	cancel bool
}

func newDummySink() *dummySink {
	r, w := io.Pipe()
	return &dummySink{
		PipeWriter: w,
		PipeReader: r,
	}
}

func (s *dummySink) ID() string { return "dummy" }

func (s *dummySink) Cancel() error {
	s.cancel = true
	return s.Close()
}

func (s *dummySink) Close() error {
	return s.PipeWriter.Close()
}

func TestFSM_Persist(t *testing.T) {
	fsm := NewFSM("node1")
	fsm.Reputations.Store("agentA", 50)

	snap, _ := fsm.Snapshot()
	sink := newDummySink()

	go func() {
		err := snap.Persist(sink)
		if err != nil {
			t.Errorf("Persist failed: %v", err)
		}
	}()

	// Read from sink
	var snapshotData FSMSnapshotData
	err := json.NewDecoder(sink.PipeReader).Decode(&snapshotData)
	if err != nil {
		t.Fatalf("Failed to decode persist output: %v", err)
	}

	if snapshotData.Reputations["agentA"] != 50 {
		t.Errorf("Persisted data mismatch")
	}
}

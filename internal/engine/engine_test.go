package engine

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	raftInternal "github.com/dhiaayachi/gravity-ai/internal/raft"
	"github.com/hashicorp/raft"
)

// Mocks

type mockCommandSender struct {
	cmds [][]byte
	err  error
	mu   sync.Mutex
}

func (m *mockCommandSender) Apply(cmd []byte, timeout time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cmds = append(m.cmds, cmd)
	return m.err
}

type mockClusterState struct {
	serverCount int
	isLeader    bool
	id          string
}

func (m *mockClusterState) IsLeader() bool {
	return m.isLeader
}

func (m *mockClusterState) GetFormattedID() string {
	return m.id
}

func (m *mockClusterState) GetServerCount() (int, error) {
	return m.serverCount, nil
}

type mockLLM struct {
	genResp   string
	genErr    error
	validResp bool
	validErr  error
}

func (m *mockLLM) Generate(prompt string) (string, error) {
	return m.genResp, m.genErr
}

func (m *mockLLM) Validate(taskContent string, proposal string) (bool, error) {
	return m.validResp, m.validErr
}

func (m *mockLLM) HealthCheck() error {
	return nil
}

// Helper to define test harness
type testHarness struct {
	engine     *Engine
	fsm        *raftInternal.FSM
	cmdSender  *mockCommandSender
	cluster    *mockClusterState
	llm        *mockLLM
	nodeConfig *raftInternal.Config
}

func newTestHarness() *testHarness {
	fsm := raftInternal.NewFSM("node1")
	cmdSender := &mockCommandSender{}
	cluster := &mockClusterState{isLeader: true, serverCount: 3, id: "node1"}
	mockL := &mockLLM{genResp: "Answer", validResp: true}
	nodeConfig := &raftInternal.Config{ID: "node1"}

	// Use bare struct initialization as NewEngine requires *AgentNode which is hard to mock entirely
	// We only populate what's needed for logic if we inject dependencies
	eng := &Engine{}
	eng.SetCommandSender(cmdSender)
	eng.SetClusterState(cluster)

	// Inject decoupled fields manually to avoid creating AgentNode
	// We need to use reflection or just make fields exported? No, "Engine" is in same package "engine" so we can access private fields IF we are in "package engine"
	// But test is typically "package engine" or "package engine_test".
	// If "package engine", we can set private fields.

	eng.fsm = fsm
	eng.llm = mockL
	eng.nodeConfig = nodeConfig
	// Initialize listeners map
	// sync.Map zero value is valid

	return &testHarness{
		engine:     eng,
		fsm:        fsm,
		cmdSender:  cmdSender,
		cluster:    cluster,
		llm:        mockL,
		nodeConfig: nodeConfig,
	}
}

func TestSubmitTask(t *testing.T) {
	h := newTestHarness()

	// 1. Success case
	future, err := h.engine.SubmitTask("Task content", "user1")
	if err != nil {
		t.Fatalf("SubmitTask failed: %v", err)
	}
	if future == nil {
		t.Fatal("Future is nil")
	}

	h.cmdSender.mu.Lock()
	if len(h.cmdSender.cmds) != 1 {
		t.Errorf("Expected 1 command, got %d", len(h.cmdSender.cmds))
	}
	h.cmdSender.mu.Unlock()

	// 2. Not leader
	h.cluster.isLeader = false
	_, err = h.engine.SubmitTask("Task content", "user1")
	if err == nil {
		t.Error("Expected error when not leader")
	}
}

func TestFlow_TaskAdmitted_Brainstorm(t *testing.T) {
	h := newTestHarness()
	h.cluster.isLeader = false // Follower also participates

	task := &core.Task{ID: "t1", Content: "c1", Status: core.TaskStatusAdmitted}
	h.fsm.Tasks.Store("t1", task)

	// Inject event
	h.engine.handleTaskAdmitted(task)

	// Should have submitted Answer
	h.cmdSender.mu.Lock()
	if len(h.cmdSender.cmds) != 1 {
		t.Fatalf("Expected Answer command, got %d", len(h.cmdSender.cmds))
	}

	// Verify command type and content
	var cmd raftInternal.LogCommand
	json.Unmarshal(h.cmdSender.cmds[0], &cmd)
	if cmd.Type != raftInternal.CommandTypeSubmitAnswer {
		t.Errorf("Expected CommandTypeSubmitAnswer, got %v", cmd.Type)
	}
	h.cmdSender.mu.Unlock()
}

func TestFlow_Leader_Proposal(t *testing.T) {
	h := newTestHarness()
	task := &core.Task{ID: "t1", Content: "c1", Status: core.TaskStatusAdmitted}
	h.fsm.Tasks.Store("t1", task)

	// Add answer to FSM
	h.fsm.TaskAnswers.Store("t1", []core.Answer{{TaskID: "t1", Content: "ans"}})

	// Trigger event (simulation of Engine.Start loop logic)
	// We call the handler method directly if we extract it, or simulate logic.
	// Engine.Start loop calls `e.runProposalPhase(task)` if `EventAnswerSubmitted` and Leader.

	h.engine.runProposalPhase(task)

	h.cmdSender.mu.Lock()
	if len(h.cmdSender.cmds) != 1 {
		t.Fatalf("Expected UpdateTask command, got %d", len(h.cmdSender.cmds))
	}
	var cmd raftInternal.LogCommand
	json.Unmarshal(h.cmdSender.cmds[0], &cmd)
	if cmd.Type != raftInternal.CommandTypeUpdateTask {
		t.Errorf("Expected CommandTypeUpdateTask, got %v", cmd.Type)
	}
	h.cmdSender.mu.Unlock()

	// Verify task object in command
	var taskUpd core.Task
	json.Unmarshal(cmd.Value, &taskUpd)
	if taskUpd.Status != core.TaskStatusProposal {
		t.Errorf("Expected Proposal status, got %v", taskUpd.Status)
	}
	if taskUpd.Result != "ans" {
		t.Errorf("Expected result 'ans', got %v", taskUpd.Result)
	}
}

func TestFlow_Vote(t *testing.T) {
	h := newTestHarness()
	task := &core.Task{ID: "t1", Result: "ans", Status: core.TaskStatusProposal}

	h.engine.runVotePhase(task)

	h.cmdSender.mu.Lock()
	if len(h.cmdSender.cmds) != 1 {
		t.Fatalf("Expected Vote command, got %d", len(h.cmdSender.cmds))
	}
	var cmd raftInternal.LogCommand
	json.Unmarshal(h.cmdSender.cmds[0], &cmd)
	if cmd.Type != raftInternal.CommandTypeSubmitVote {
		t.Errorf("Expected CommandTypeSubmitVote, got %v", cmd.Type)
	}
	h.cmdSender.mu.Unlock()
}

func TestFlow_Leader_Finalize_Consensus(t *testing.T) {
	h := newTestHarness()
	task := &core.Task{ID: "t1", Status: core.TaskStatusProposal}
	h.fsm.Tasks.Store("t1", task) // Must match in FSM

	// Stores votes: 2/3 accepted
	votes := []core.Vote{
		{AgentID: "1", Accepted: true},
		{AgentID: "2", Accepted: true},
		{AgentID: "3", Accepted: false},
	}
	h.fsm.TaskVotes.Store("t1", votes)

	h.engine.finalizeTask(task)

	h.cmdSender.mu.Lock()
	if len(h.cmdSender.cmds) != 1 {
		t.Fatalf("Expected UpdateTask (Done) command, got %d", len(h.cmdSender.cmds))
	}
	var cmd raftInternal.LogCommand
	json.Unmarshal(h.cmdSender.cmds[0], &cmd)
	var taskUpd core.Task
	json.Unmarshal(cmd.Value, &taskUpd)
	if taskUpd.Status != core.TaskStatusDone {
		t.Errorf("Expected Status Done, got %v", taskUpd.Status)
	}
	h.cmdSender.mu.Unlock()
}

func TestFlow_Leader_Finalize_Rejected(t *testing.T) {
	h := newTestHarness()
	task := &core.Task{ID: "t1", Status: core.TaskStatusProposal}
	h.fsm.Tasks.Store("t1", task)

	votes := []core.Vote{
		{AgentID: "1", Accepted: false},
		{AgentID: "2", Accepted: false},
		{AgentID: "3", Accepted: true},
	}
	h.fsm.TaskVotes.Store("t1", votes)

	h.engine.finalizeTask(task)

	h.cmdSender.mu.Lock()
	if len(h.cmdSender.cmds) != 1 {
		t.Fatalf("Expected UpdateTask (Failed) command")
	}
	var cmd raftInternal.LogCommand
	json.Unmarshal(h.cmdSender.cmds[0], &cmd)
	var taskUpd core.Task
	json.Unmarshal(cmd.Value, &taskUpd)
	if taskUpd.Status != core.TaskStatusFailed {
		t.Errorf("Expected Status Failed, got %v", taskUpd.Status)
	}
	h.cmdSender.mu.Unlock()
}

func TestFlow_Leader_Finalize_NoConsensus(t *testing.T) {
	h := newTestHarness()
	task := &core.Task{ID: "t1", Status: core.TaskStatusProposal}
	h.fsm.Tasks.Store("t1", task)

	// 1/3 voted so far
	votes := []core.Vote{
		{AgentID: "1", Accepted: true},
	}
	h.fsm.TaskVotes.Store("t1", votes)

	h.engine.finalizeTask(task)

	h.cmdSender.mu.Lock()
	if len(h.cmdSender.cmds) != 0 {
		t.Fatalf("Expected NO command, got %d", len(h.cmdSender.cmds))
	}
	h.cmdSender.mu.Unlock()
}

func TestNewEngine(t *testing.T) {
	// Need a dummy node
	fsm := raftInternal.NewFSM("n1")
	cfg := &raftInternal.Config{ID: "n1"}
	node := &raftInternal.AgentNode{
		FSM:    fsm,
		Config: cfg,
		Raft:   &raft.Raft{}, // nil might panic if NewEngine accesses it?
	}
	// NewEngine accesses node.Raft in defaultCommandSender

	eng := NewEngine(node, nil, nil)
	if eng == nil {
		t.Fatal("NewEngine returned nil")
	}
	if eng.fsm != fsm {
		t.Error("FSM not set correctly")
	}
}

func TestEngine_Start(t *testing.T) {
	h := newTestHarness()
	h.engine.Start() // Starts goroutine

	// 1. TaskAdmitted -> Answer
	task := &core.Task{ID: "t1", Content: "c1", Status: core.TaskStatusAdmitted}
	h.fsm.Tasks.Store("t1", task)
	h.fsm.EventCh <- raftInternal.Event{Type: raftInternal.EventTaskAdmitted, Data: task}

	// Wait for command
	time.Sleep(50 * time.Millisecond)
	h.cmdSender.mu.Lock()
	if len(h.cmdSender.cmds) < 1 {
		t.Errorf("Expected Answer command")
	}
	h.cmdSender.mu.Unlock()

	// 2. AnswerSubmitted -> Proposal (as Leader)
	ans := &core.Answer{TaskID: "t1", Content: "ans", AgentID: "node1"}
	h.fsm.TaskAnswers.Store("t1", []core.Answer{*ans})
	h.fsm.EventCh <- raftInternal.Event{Type: raftInternal.EventAnswerSubmitted, Data: ans}

	time.Sleep(50 * time.Millisecond)
	h.cmdSender.mu.Lock()
	if len(h.cmdSender.cmds) < 2 {
		t.Errorf("Expected UpdateTask command (Proposal)")
	}
	h.cmdSender.mu.Unlock()

	// 3. TaskUpdated (Proposal) -> Vote
	taskProp := &core.Task{ID: "t1", Status: core.TaskStatusProposal, Result: "ans"}
	h.fsm.EventCh <- raftInternal.Event{Type: raftInternal.EventTaskUpdated, Data: taskProp}

	time.Sleep(50 * time.Millisecond)
	h.cmdSender.mu.Lock()
	if len(h.cmdSender.cmds) < 3 {
		t.Errorf("Expected Vote command")
	}
	h.cmdSender.mu.Unlock()

	// 4. VoteSubmitted -> Finalize
	vote := &core.Vote{TaskID: "t1", AgentID: "node2", Accepted: true}
	// Setup consensus
	h.fsm.TaskVotes.Store("t1", []core.Vote{
		{AgentID: "node1", Accepted: true},
		{AgentID: "node2", Accepted: true},
		{AgentID: "node3", Accepted: false},
	})
	// Need to ensure task is in FSM as Proposal
	h.fsm.Tasks.Store("t1", taskProp)

	h.fsm.EventCh <- raftInternal.Event{Type: raftInternal.EventVoteSubmitted, Data: vote}

	time.Sleep(50 * time.Millisecond)
	h.cmdSender.mu.Lock()
	if len(h.cmdSender.cmds) < 4 {
		t.Errorf("Expected UpdateTask command (Finalize)")
	}
	h.cmdSender.mu.Unlock()
}

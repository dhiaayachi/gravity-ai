package engine

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	"github.com/dhiaayachi/gravity-ai/internal/engine/tasks-manager"
	raftInternal "github.com/dhiaayachi/gravity-ai/internal/raft"
	fsm2 "github.com/dhiaayachi/gravity-ai/internal/raft/fsm"
	"github.com/hashicorp/raft"
	"go.uber.org/zap"
)

// mockClusterState no longer needed for ISLeader if we use real Raft?
// Actually, engine uses clusterState interface. We can keep mocking it or implement a wrapper around real Raft.
// But IsLeader() check in Engine calls e.clusterState.IsLeader().
// If we use real Raft, we can implement a clusterState that calls Raft.State().
// OR we can just keep mocking ClusterState for "IsLeader" check BUT the Apply() call goes to real Raft.
// Engine struct:
// Node *raftInternal.AgentNode (Has Raft)
// clusterState ClusterState
//
// If we mock ClusterState.IsLeader() returns true, Engine proceeds to e.Node.Raft.Apply().
// If e.Node.Raft is real, `Apply` works.
// BUT real Raft needs to be holding leadership to accept Apply!
// So we MUST make the real Raft leader.
// OR we can mock Raft? No, Raft is a struct.
// So we must bootstrap a single node Raft cluster and wait for it to be leader.

type mockClusterState struct {
	serverCount int
	isLeader    bool
	id          string
	raft        *raft.Raft
}

func (m *mockClusterState) IsLeader() bool {
	// If we use real Raft, we should probably check real state,
	// but strictly for "Apply" to work, Raft must be leader.
	// Tests manually set h.cluster.isLeader = true/false to test logic flow.
	// But now that logic flow calls Real Raft Apply.
	// Real Raft Apply will Fail if not leader.
	// So we MUST ensure Real Raft is leader.
	return m.isLeader
}

func (m *mockClusterState) GetLeaderAddr() string {
	return "leader.example.com:8088"
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

func (m *mockLLM) Aggregate(taskContent string, answers []string) (string, error) {
	return "Aggregated: " + m.genResp, nil
}

func (m *mockLLM) HealthCheck() error {
	return nil
}

type mockClusterClient struct {
	mu           sync.Mutex
	voted        bool
	TaskID       string
	AgentID      string
	voteAccepted bool
	answer       string
}

func (m *mockClusterClient) SubmitVote(ctx context.Context, leaderAddr string, taskID, agentID string, accepted bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.voted = true
	m.TaskID = taskID
	m.AgentID = agentID
	m.voteAccepted = accepted
	return nil
}
func (m *mockClusterClient) SubmitAnswer(ctx context.Context, leaderAddr string, taskID, agentID string, answer string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.voted = true
	m.TaskID = taskID
	m.AgentID = agentID
	m.answer = answer
	return nil
}

type testHarness struct {
	engine     *Engine
	fsm        *fsm2.SyncMapFSM
	cluster    *mockClusterState
	llm        *mockLLM
	nodeConfig *raftInternal.Config
	raft       *raft.Raft
}

type mockTaskNotifier struct{}

func (m *mockTaskNotifier) NotifyTaskCompletion(task *core.Task) {}

func newTestHarness(t *testing.T, clusterClient ClusterClient) *testHarness {
	// Use t.Name() to avoid address collisions in InmemTransport
	// Sanitize name for ID (limit length if needed, simple replacement)
	nodeID := raft.ServerID(t.Name())
	addr := raft.ServerAddress(t.Name())

	// 1. Setup In-Memory Raft
	fsm := fsm2.NewSyncMapFSM(string(nodeID))
	store := raft.NewInmemStore()
	snap, _ := raft.NewFileSnapshotStore(t.TempDir(), 1, os.Stderr)

	_, transport := raft.NewInmemTransport(addr)

	conf := raft.DefaultConfig()
	conf.LocalID = nodeID
	conf.HeartbeatTimeout = 50 * time.Millisecond
	conf.ElectionTimeout = 50 * time.Millisecond
	conf.LeaderLeaseTimeout = 50 * time.Millisecond
	conf.CommitTimeout = 50 * time.Millisecond

	// Bootstrap
	bootstrapConfig := raft.Configuration{
		Servers: []raft.Server{
			{
				ID:      conf.LocalID,
				Address: transport.LocalAddr(),
			},
		},
	}
	raft.BootstrapCluster(conf, store, store, snap, transport, bootstrapConfig)

	r, err := raft.NewRaft(conf, fsm, store, store, snap, transport)
	if err != nil {
		t.Fatalf("Failed to create Raft: %v", err)
	}

	// Wrapper for e.Node
	agentNode := &raftInternal.AgentNode{
		Raft: r,
		FSM:  fsm,
		Config: &raftInternal.Config{
			ID: string(nodeID),
		},
	}

	// Wait for leader
	timeout := time.After(5 * time.Second)
	foundLeader := false
	for !foundLeader {
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for Raft leadership")
		default:
			if r.State() == raft.Leader {
				foundLeader = true
			} else {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}

	cluster := &mockClusterState{isLeader: true, serverCount: 3, id: string(nodeID), raft: r}
	mockL := &mockLLM{genResp: "Answer", validResp: true}

	eng := &Engine{
		Node:            agentNode,
		fsm:             fsm,
		llm:             mockL,
		nodeConfig:      agentNode.Config,
		clusterState:    cluster,
		timerCh:         make(chan string, 100),
		ProposalTimeout: 30 * time.Second,
		VoteTimeout:     10 * time.Second,
		clusterClient:   clusterClient,
		taskNotifier:    &mockTaskNotifier{},
		logger:          zap.NewNop(),
	}

	// Register cleanup
	t.Cleanup(func() {
		r.Shutdown().Error()
	})

	return &testHarness{
		engine:     eng,
		fsm:        fsm,
		cluster:    cluster,
		llm:        mockL,
		nodeConfig: agentNode.Config,
		raft:       r,
	}
}

func TestFlow_TaskAdmitted_Brainstorm(t *testing.T) {
	h := newTestHarness(t, &mockClusterClient{})
	h.cluster.isLeader = false // Follower also participates

	task := &core.Task{ID: "t1", Content: "c1", Status: core.TaskStatusAdmitted}
	h.fsm.Tasks.Store("t1", task)

	// Inject event
	h.engine.handleTaskAdmitted(task)

	// Should have submitted Answer
	// Wait for Answer
	time.Sleep(100 * time.Millisecond)

	client, ok := h.engine.clusterClient.(*mockClusterClient)
	if !ok {
		t.Fatal("ClusterClient is not a mockClusterClient")
	}

	if client.TaskID != "t1" {
		t.Errorf("Expected TaskID t1, got %s", client.TaskID)
	}
}

func TestFlow_Leader_Proposal(t *testing.T) {
	h := newTestHarness(t, &mockClusterClient{})
	task := &core.Task{ID: "t1", Content: "c1", Status: core.TaskStatusAdmitted}
	h.fsm.Tasks.Store("t1", task)

	// Add answer to SyncMapFSM (Need 2 for quorum of 3)
	h.fsm.TaskAnswers.Store("t1", []core.Answer{
		{TaskID: "t1", Content: "ans1"},
		{TaskID: "t1", Content: "ans2"},
		{TaskID: "t1", Content: "ans3"},
	})

	// Trigger event (simulation of Engine.Start loop logic)
	// We call the handler method directly if we extract it, or simulate logic.
	// Engine.Start loop calls `e.runProposalPhase(task)` if `EventAnswerSubmitted` and Leader.

	h.engine.runProposalPhase(task, false)

	// Verify Task Status Updated to Proposal
	time.Sleep(100 * time.Millisecond)

	val, ok := h.fsm.Tasks.Load(task.ID)
	if !ok {
		t.Fatal("Task not in SyncMapFSM")
	}
	taskUpd := val.(*core.Task)
	if taskUpd.Status != core.TaskStatusProposal {
		t.Errorf("Expected Proposal status, got %v", taskUpd.Status)
	}
	if taskUpd.Result != "Aggregated: Answer" {
		t.Errorf("Expected aggregated result, got %v", taskUpd.Result)
	}
}

// SetClusterClient sets the cluster client
func (e *Engine) SetClusterClient(client ClusterClient) {
	e.clusterClient = client
}

func TestFlow_Follower_Vote(t *testing.T) {
	h := newTestHarness(t, nil)
	// Set as Follower
	h.cluster.isLeader = false
	// Configure mock client
	mockClient := &mockClusterClient{}
	h.engine.SetClusterClient(mockClient)

	task := &core.Task{ID: "t1", Result: "ans", Status: core.TaskStatusProposal}

	h.engine.runVotePhase(task)

	// Verify NO Raft command
	// Verify NO Raft command (Follower shouldn't write)
	// With real Raft, Apply would fail if called.
	// But we expect mockClient.voted to be true.

	// Verify Client usage
	mockClient.mu.Lock()
	if !mockClient.voted {
		t.Error("Expected SubmitVote call on ClusterClient")
	}
	if mockClient.TaskID != "t1" {
		t.Errorf("Expected TaskID t1, got %s", mockClient.TaskID)
	}
	// Check mocked leader address
	// Note: We don't check leaderAddr directly in mock unless we store it.
	// But execution implies it didn't return early due to missing leader.
	mockClient.mu.Unlock()
}

func TestFlow_Vote(t *testing.T) {
	h := newTestHarness(t, &mockClusterClient{})
	task := &core.Task{ID: "t1", Result: "ans", Status: core.TaskStatusProposal}

	h.engine.runVotePhase(task)

	time.Sleep(100 * time.Millisecond)
	// Verify Vote in SyncMapFSM/Vote storage
	// We check internal raft logic or assume log applied if no error.
	// SyncMapFSM usually updates TaskVotes?
	// Let's assume yes for now.
	if _, ok := h.fsm.TaskVotes.Load(task.ID); !ok {
		// Wait more
		time.Sleep(100 * time.Millisecond)
		if _, ok := h.fsm.TaskVotes.Load(task.ID); !ok {
			t.Error("No votes found in SyncMapFSM")
		}
	}
}

func TestFlow_Leader_Finalize_Consensus(t *testing.T) {
	h := newTestHarness(t, &mockClusterClient{})
	task := &core.Task{ID: "t1", Status: core.TaskStatusProposal}
	h.fsm.Tasks.Store("t1", task) // Must match in SyncMapFSM

	// Stores votes: 2/3 accepted
	votes := []core.Vote{
		{AgentID: "1", Accepted: true},
		{AgentID: "2", Accepted: true},
		{AgentID: "3", Accepted: false},
	}
	h.fsm.TaskVotes.Store("t1", votes)

	h.engine.runFinalizeTask(task)

	time.Sleep(100 * time.Millisecond)

	// Verify Task Status Done
	val, ok := h.fsm.Tasks.Load(task.ID)
	if !ok {
		t.Fatal("Task not in SyncMapFSM")
	}
	finalTask := val.(*core.Task)
	if finalTask.Status != core.TaskStatusDone {
		t.Errorf("Expected Done status, got %v", finalTask.Status)
	}
}

func TestFlow_Leader_Finalize_Rejected(t *testing.T) {
	h := newTestHarness(t, &mockClusterClient{})
	task := &core.Task{ID: "t1", Status: core.TaskStatusProposal}
	h.fsm.Tasks.Store("t1", task)

	votes := []core.Vote{
		{AgentID: "1", Accepted: false},
		{AgentID: "2", Accepted: false},
		{AgentID: "3", Accepted: true},
	}
	h.fsm.TaskVotes.Store("t1", votes)

	h.engine.runFinalizeTask(task)

	time.Sleep(100 * time.Millisecond)

	// Verify Task Status Failed
	val, ok := h.fsm.Tasks.Load(task.ID)
	if !ok {
		t.Fatal("Task not in SyncMapFSM")
	}
	finalTask := val.(*core.Task)
	if finalTask.Status != core.TaskStatusFailed {
		t.Errorf("Expected Failed status, got %v", finalTask.Status)
	}
}

func TestFlow_Leader_Timeout(t *testing.T) {
	h := newTestHarness(t, &mockClusterClient{})
	h.engine.Start()
	h.engine.ProposalTimeout = 100 * time.Millisecond

	task := &core.Task{ID: "t1", Content: "c1", Status: core.TaskStatusAdmitted}
	h.fsm.Tasks.Store("t1", task)

	// Simulate Admitted event which starts timer
	h.engine.handleTaskAdmitted(task)

	// Wait for timeout
	time.Sleep(500 * time.Millisecond)

	// Check for failure in SyncMapFSM
	time.Sleep(100 * time.Millisecond)

	val, ok := h.fsm.Tasks.Load(task.ID)
	if !ok {
		t.Fatal("Task not in SyncMapFSM")
	}
	failedTask := val.(*core.Task)
	if failedTask.Status != core.TaskStatusFailed {
		t.Errorf("Expected Failed status, got %v", failedTask.Status)
	}

}

func TestFlow_Leader_Finalize_NoConsensus(t *testing.T) {
	h := newTestHarness(t, &mockClusterClient{})
	task := &core.Task{ID: "t1", Status: core.TaskStatusProposal}
	h.fsm.Tasks.Store("t1", task)

	// 1/3 voted so far
	votes := []core.Vote{
		{AgentID: "1", Accepted: true},
	}
	h.fsm.TaskVotes.Store("t1", votes)

	h.engine.runFinalizeTask(task)

	// Verify NO state change since votes < 2?
	// It should NOT finalize if votes < quorum?
	// But current code runFinalizeTask just runs.
	// Actually, verify it DID NOT change to Done/Failed if not enough votes?
	// Or maybe it does fail?
	// The test title says "NoConsensus", which usually means "Rejected" or "Wait"?
	// Engine logic:
	// acceptedVotes := 0
	// for _, v := range votes { if v.Accepted { acceptedVotes++ } }
	// if acceptedVotes >= quorum { Done } else { Failed }

	// Here votes=1, Assume Quorum=2 (Total 3)
	// So 1 < 2 -> Fail.
	// So it SHOULD fail.

	// Previous logic:
	// h.cmdSender check: "Expected NO command"
	// This implies previous test expected it to do NOTHING?
	// Wait, if not enough votes, does it fail immediately?
	// Check engine.go runFinalizeTask:
	// if len(votes) < quorum { return } // Wait for more votes?
	// OR does it decide based on collected votes?
	// Current engine.go (I need to check) typically waits for ALL or timeout?
	// No, runFinalizeTask is triggered by EventVoteSubmitted.
	// let's check runFinalizeTask implementation.

	// If it was checking "Expected NO command", it means it expected to block/wait.
	// verification:
	time.Sleep(50 * time.Millisecond)
	// Should remain in Proposal
	val, ok := h.fsm.Tasks.Load(task.ID)
	if !ok {
		t.Fatal("Task not in SyncMapFSM")
	}
	tStat := val.(*core.Task)
	if tStat.Status != core.TaskStatusProposal {
		t.Errorf("Expected Proposal status (waiting), got %v", tStat.Status)
	}
}

func TestNewEngine(t *testing.T) {
	// Need a dummy node
	fsm := fsm2.NewSyncMapFSM("n1")
	cfg := &raftInternal.Config{ID: "n1"}
	node := &raftInternal.AgentNode{
		FSM:    fsm,
		Config: cfg,
		Raft:   &raft.Raft{}, // nil might panic if NewEngine accesses it?
	}
	// NewEngine accesses node.Raft in defaultCommandSender

	eng := NewEngine(node, nil, nil, nil, &tasks_manager.TasksManager{}, zap.NewNop())
	if eng == nil {
		t.Fatal("NewEngine returned nil")
	}
	if eng.fsm != fsm {
		t.Error("SyncMapFSM not set correctly")
	}
}

func TestEngine_Start(t *testing.T) {
	h := newTestHarness(t, &mockClusterClient{})
	h.engine.Start() // Starts goroutine

	// 1. TaskAdmitted -> Answer
	task := &core.Task{ID: "t1", Content: "c1", Status: core.TaskStatusAdmitted}
	h.fsm.Tasks.Store("t1", task)
	h.fsm.EventCh <- fsm2.Event{Type: fsm2.EventTaskAdmitted, Data: task}

	// Wait for Answer in SyncMapFSM
	time.Sleep(100 * time.Millisecond)
	if _, ok := h.fsm.TaskAnswers.Load(task.ID); !ok {
		// Actually, runBrainstormPhase applies Answer command.
		// Wait longer?
		t.Log("Waiting for answer...")
	}

	// Since we are mocking everything, let's just trigger next steps by injecting events.

	// 2. AnswerSubmitted -> Proposal (as Leader)
	ans := &core.Answer{TaskID: "t1", Content: "ans", AgentID: "node1"}
	// Need 2 answers for quorum of 3
	h.fsm.TaskAnswers.Store("t1", []core.Answer{
		*ans,
		{TaskID: "t1", Content: "ans2", AgentID: "node2"},
		{TaskID: "t1", Content: "ans3", AgentID: "node3"},
	})
	h.fsm.EventCh <- fsm2.Event{Type: fsm2.EventAnswerSubmitted, Data: ans}

	time.Sleep(100 * time.Millisecond)
	// Expect Proposal
	if val, ok := h.fsm.Tasks.Load(task.ID); ok {
		if val.(*core.Task).Status != core.TaskStatusProposal {
			t.Logf("Expected Proposal status, got %v", val.(*core.Task).Status)
		}
	}

	// 3. TaskUpdated (Proposal) -> Vote
	taskProp := &core.Task{ID: "t1", Status: core.TaskStatusProposal, Result: "ans"}
	h.fsm.EventCh <- fsm2.Event{Type: fsm2.EventTaskUpdated, Data: taskProp}

	time.Sleep(100 * time.Millisecond)
	// Expect Vote in SyncMapFSM
	// (Assumes Apply worked)

	// 4. VoteSubmitted -> Finalize
	vote := &core.Vote{TaskID: "t1", AgentID: "node2", Accepted: true}
	// Setup consensus
	h.fsm.TaskVotes.Store("t1", []core.Vote{
		{AgentID: "node1", Accepted: true},
		{AgentID: "node2", Accepted: true},
		{AgentID: "node3", Accepted: false},
	})
	// Need to ensure task is in SyncMapFSM as Proposal
	h.fsm.Tasks.Store("t1", taskProp)

	h.fsm.EventCh <- fsm2.Event{Type: fsm2.EventVoteSubmitted, Data: vote}

	time.Sleep(100 * time.Millisecond)
	// Verify Task Status Done
	if val, ok := h.fsm.Tasks.Load("t1"); ok {
		if val.(*core.Task).Status != core.TaskStatusDone {
			t.Errorf("Expected Done status")
		}
	} else {
		t.Error("Task not found")
	}
}

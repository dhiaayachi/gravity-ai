package engine

import (
	"testing"
	"time"

	"github.com/dhiaayachi/gravity-ai/internal/core"
)

func TestReputationUpdates_And_LeadershipTransfer(t *testing.T) {
	h := newTestHarness(t, &mockClusterClient{})

	// 1. Setup Initial State
	// Current Node (Leader) = "TestReputationUpdates_And_LeadershipTransfer" (sanitized by Raft ID usually, but here h.nodeConfig.ID)
	leaderID := h.nodeConfig.ID
	worker1 := "worker1"
	worker2 := "worker2"

	// Set initial reputations
	// Leader: 100
	// Worker1: 100
	// Worker2: 200 (Higher than leader, should trigger transfer?)
	// Wait, if Worker2 is 200, it should trigger transfer immediately?
	// The check runs in runFinalizeTask.
	// Let's set Worker2 to 90 initially, so no transfer yet.
	h.fsm.Reputations.Store(leaderID, 100)
	h.fsm.Reputations.Store(worker1, 100)
	h.fsm.Reputations.Store(worker2, 90)

	task := &core.Task{ID: "t1", Status: core.TaskStatusProposal, Result: "res"}
	h.fsm.Tasks["t1"] = task

	// Votes:
	// Leader (Self): Accepted
	// Worker1: Accepted
	// Worker2: Rejected

	votes := map[string]core.Vote{
		leaderID: {AgentID: leaderID, Accepted: true},
		worker1:  {AgentID: worker1, Accepted: true},
		worker2:  {AgentID: worker2, Accepted: false},
	}
	h.fsm.TaskVotes["t1"] = votes

	// 2. Run Finalize
	// Quorum = 3/2 + 1 = 2.
	// Accepted = 2 (Leader + Worker1). Consensus = Done.
	h.engine.runFinalizeTask(task)

	// Allow time for Apply
	time.Sleep(200 * time.Millisecond)

	// 3. Verify Reputations

	// Verify Task Confidence Score
	// Leader (100) + Worker1 (100) Agree. Worker2 (90) Disagree.
	// Total = 290. Agree = 200. Score = 200/290 = 0.6896...
	finalTask, _ := h.fsm.GetTask(task.ID)
	expectedScore := 200.0 / 290.0
	if finalTask.ConfidenceScore < expectedScore-0.001 || finalTask.ConfidenceScore > expectedScore+0.001 {
		t.Errorf("Expected ConfidenceScore ~%f, got %f", expectedScore, finalTask.ConfidenceScore)
	}

	// Leader: 100 + 10 = 110 -> Clamped to 100
	lRep := h.fsm.GetReputation(leaderID)
	if lRep != 100 {
		t.Errorf("Expected Leader Rep 100 (clamped), got %d", lRep)
	}

	// Worker1: Agreed (Accepted=True, Result=Done). 100 + 1 = 101 -> Clamped to 100
	w1Rep := h.fsm.GetReputation(worker1)
	if w1Rep != 100 {
		t.Errorf("Expected Worker1 Rep 100 (clamped), got %d", w1Rep)
	}

	// Worker2: Disagreed (Accepted=False, Result=Done). 90 - 1 = 89
	w2Rep := h.fsm.GetReputation(worker2)
	if w2Rep != 89 {
		t.Errorf("Expected Worker2 Rep 89, got %d", w2Rep)
	}

	// 4. Verify Leadership Transfer (Should NOT happen, Leader 110 is max)
	if h.cluster.transferTarget != "" {
		t.Errorf("Expected no transfer, got target %s", h.cluster.transferTarget)
	}

	// 5. Trigger Transfer Scenario
	// To trigger transfer, we need someone > Leader.
	// Leader is at 100 (max).
	// We must lower Leader reputation to allow transfer, OR effectively we can't transfer if everyone is max.
	// Let's degrade Leader first with a Failed task.

	taskFail := &core.Task{ID: "tFail", Status: core.TaskStatusProposal, Result: "fail"}
	h.fsm.Tasks["tFail"] = taskFail
	// Leader "fails" -> -10 -> 90.
	// Worker2 (at 89 from before) Votes Rejected (Correct for Failed) -> +1 -> 90.
	// Worker1 (at 100) Votes Accepted (Wrong) -> -1 -> 99.

	// Wait, we want Worker2 to be > Leader.
	// If Leader -> 90.
	// Worker2 manually set to 95?

	// Let's manually degrade Leader to 90.
	h.fsm.Reputations.Store(leaderID, 90)

	// Set Worker2 to 100 (Max)
	h.fsm.Reputations.Store(worker2, 100)

	// Run a task that keeps status quo or just use empty check?
	// The check runs in runFinalizeTask.
	// Let's run a neutral task where Leader gains +10 back? No then Leader is 100.
	// We want Leader to stay < Worker2.
	// If we run a successful task: Leader +10 -> 100. Worker2 (Agrees) +1 -> 100 (capped).
	// Then Leader == Worker2. No transfer.

	// If we run a FAILED task (Consensus Rejected):
	// Leader -10 -> 80.
	// Worker2 (Agrees / Rejects properly) -> +1 -> 100 (capped).
	// Then Worker2 (100) > Leader (80). Transfer!

	task2 := &core.Task{ID: "t2", Status: core.TaskStatusProposal, Result: "res2"}
	h.fsm.Tasks["t2"] = task2

	// Votes:
	// Leader & Worker2 Reject -> Consensus Rejected (Majority).
	// Worker1 Accepts (Incorrect).
	votesRefused := map[string]core.Vote{
		leaderID: {AgentID: leaderID, Accepted: false},
		worker1:  {AgentID: worker1, Accepted: true},
		worker2:  {AgentID: worker2, Accepted: false},
	}
	h.fsm.TaskVotes["t2"] = votesRefused

	h.engine.runFinalizeTask(task2)
	time.Sleep(200 * time.Millisecond)

	// Leader: 90 - 10 = 80.
	// Worker2: 100 + 1 = 101 -> 100.
	// Transfer should happen.

	h.cluster.mu.Lock()
	target := h.cluster.transferTarget
	h.cluster.mu.Unlock()

	if target != worker2 {
		t.Errorf("Expected transfer to %s, got %s", worker2, target)
	}
}

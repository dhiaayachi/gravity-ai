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
	// Leader: 100 + 10 = 110
	lRep := h.fsm.GetReputation(leaderID)
	if lRep != 110 {
		t.Errorf("Expected Leader Rep 110, got %d", lRep)
	}

	// Worker1: Agreed (Accepted=True, Result=Done). 100 + 1 = 101
	w1Rep := h.fsm.GetReputation(worker1)
	if w1Rep != 101 {
		t.Errorf("Expected Worker1 Rep 101, got %d", w1Rep)
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
	// Manually set Worker2 to 200 (Higher than Leader 110)
	h.fsm.Reputations.Store(worker2, 200)

	// Run check manually or via another task?
	// The check happens in runFinalizeTask. Let's run another task.
	task2 := &core.Task{ID: "t2", Status: core.TaskStatusProposal, Result: "res2"}
	h.fsm.Tasks["t2"] = task2
	h.fsm.TaskVotes["t2"] = votes // Same votes

	// 6. Leadership Transfer (Using MockClusterState)
	// AddVoter not needed because we use GetPeerAddress via ClusterState mock

	h.engine.runFinalizeTask(task2)
	time.Sleep(200 * time.Millisecond)

	// Now Leader should see Worker2 (200) > Self.
	// Should transfer to Worker2.

	// Note: runFinalizeTask might have updated Reputations again.
	// Leader: 110 + 10 = 120.
	// Worker2: 200 - 1 = 199.
	// 199 > 120. Transfer YES.

	h.cluster.mu.Lock()
	target := h.cluster.transferTarget
	h.cluster.mu.Unlock()

	if target != worker2 {
		t.Errorf("Expected transfer to %s, got %s", worker2, target)
	}
}

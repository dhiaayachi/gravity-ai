package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	"github.com/dhiaayachi/gravity-ai/test/cluster"
)

// TestClusterScenarios runs E2E tests for 3 scenarios using the cluster package
func TestClusterScenarios(t *testing.T) {
	// Scenario 1: 3 nodes, All Agree
	t.Run("3 Nodes - All Agree", func(t *testing.T) {
		runScenario(t, 3, 20000, "task-agree", func(nodeID string, task *core.Task) bool {
			return true // All agree
		}, core.TaskStatusDone)
	})

	// Scenario 2: 3 nodes, 2 Agree, 1 Disagree -> Accepted (Majority)
	t.Run("3 Nodes - 2 Agree 1 Disagree", func(t *testing.T) {
		runScenario(t, 3, 20010, "task-majority", func(nodeID string, task *core.Task) bool {
			// e.g. Node 2 disagree
			if nodeID == "node-2" {
				return false
			}
			return true
		}, core.TaskStatusDone)
	})

	// Scenario 3: 3 nodes, 1 Agree (Leader), 2 Disagree -> Rejected
	t.Run("3 Nodes - 1 Agree 2 Disagree", func(t *testing.T) {
		runScenario(t, 3, 20020, "task-rejected", func(nodeID string, task *core.Task) bool {
			// Leader is usually node-0 in bootstrap if we start it first and bootstrap
			if nodeID == "node-0" {
				return true
			}
			return false
		}, core.TaskStatusFailed)
	})
}

func runScenario(t *testing.T, count int, basePort int, taskContent string, voteLogic func(id string, t *core.Task) bool, expectedStatus core.TaskStatus) {
	// 1. Setup Cluster
	c := cluster.Setup(t, count, basePort, voteLogic)
	defer c.Close()

	// 2. Submit Task to Leader
	future, err := c.SubmitTask(taskContent)
	if err != nil {
		t.Fatalf("Failed to submit task: %v", err)
	}

	// 3. Wait for Final Status via Future
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	finalTask, err := future.Await(ctx)
	if err != nil {
		t.Fatalf("Failed waiting for task: %v", err)
	}

	if finalTask.Status != expectedStatus {
		t.Fatalf("Expected status %v, got %v", expectedStatus, finalTask.Status)
	}

	fmt.Printf("Scenario Passed: Task %s final status %v\n", future.TaskID, finalTask.Status)
}

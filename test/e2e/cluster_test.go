package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	"github.com/dhiaayachi/gravity-ai/internal/llm"
	"github.com/dhiaayachi/gravity-ai/test/cluster"
	"github.com/dhiaayachi/gravity-ai/test/mocks"
)

// TestClusterScenarios runs E2E tests for 3 scenarios using the cluster package
func TestClusterScenarios(t *testing.T) {
	// Scenario 1: 3 nodes, All Agree
	t.Run("3 Nodes - All Agree", func(t *testing.T) {
		runScenario(t, 3, 20000, "task-agree", func(i int) llm.Client {
			return mocks.NewYesMock()
		}, core.TaskStatusDone)
	})

	// Scenario 2: 3 nodes, 2 Agree, 1 Disagree -> Accepted (Majority)
	t.Run("3 Nodes - 2 Agree 1 Disagree", func(t *testing.T) {
		runScenario(t, 3, 20010, "task-majority", func(i int) llm.Client {
			// e.g. Node 2 disagree
			if i == 2 {
				return mocks.NewNoMock()
			}
			return mocks.NewYesMock()
		}, core.TaskStatusDone)
	})

	// Scenario 3: 3 nodes, 1 Agree (Leader), 2 Disagree -> Rejected
	t.Run("3 Nodes - 1 Agree 2 Disagree", func(t *testing.T) {
		runScenario(t, 3, 20020, "task-rejected", func(i int) llm.Client {
			// Leader is usually node-0 in bootstrap
			if i == 0 {
				return mocks.NewYesMock()
			}
			return mocks.NewNoMock()
		}, core.TaskStatusFailed)
	})
}

func runScenario(t *testing.T, count int, basePort int, taskContent string, mockFactory func(int) llm.Client, expectedStatus core.TaskStatus) {
	// 1. Setup Cluster
	c := cluster.Setup(t, count, basePort, mockFactory)
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

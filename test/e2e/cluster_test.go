package e2e

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	"github.com/dhiaayachi/gravity-ai/internal/engine"
	"github.com/dhiaayachi/gravity-ai/internal/health"
	"github.com/dhiaayachi/gravity-ai/internal/llm"
	raftInternal "github.com/dhiaayachi/gravity-ai/internal/raft"
	"github.com/hashicorp/raft"
)

// TestClusterScenarios runs E2E tests for 3 scenarios
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

// TestCommandSender implements engine.CommandSender by forwarding to the leader
type TestCommandSender struct {
	Engines []*engine.Engine
}

func (s *TestCommandSender) Apply(cmd []byte, timeout time.Duration) error {
	for _, e := range s.Engines {
		if e.Node.Raft.State() == raft.Leader {
			return e.Node.Raft.Apply(cmd, timeout).Error()
		}
	}
	return fmt.Errorf("no leader found")
}

func runScenario(t *testing.T, count int, basePort int, taskContent string, voteLogic func(id string, t *core.Task) bool, expectedStatus core.TaskStatus) {
	// 1. Setup Cluster
	nodes, engines, dirs := setupCluster(t, count, basePort, voteLogic)
	defer func() {
		for _, n := range nodes {
			n.Close()
		}
		for _, d := range dirs {
			os.RemoveAll(d)
		}
	}()

	// 2. Submit Task to Leader
	leaderEngine := engines[0] // Default to node-0 engine

	// Ensure node-0 is leader or find the actual leader's engine
	time.Sleep(5 * time.Second)
	if nodes[0].Raft.State() != raft.Leader {
		// Find leader
		found := false
		for i, n := range nodes {
			if n.Raft.State() == raft.Leader {
				leaderEngine = engines[i]
				found = true
				break
			}
		}
		if !found {
			t.Fatal("No leader found")
		}
	}

	fmt.Printf("Submitting task to leader: %s\n", leaderEngine.Node.Config.ID)
	task, err := leaderEngine.SubmitTask(taskContent, "tester")
	if err != nil {
		t.Fatalf("Failed to submit task: %v", err)
	}

	// 3. Wait for Final Status
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			t.Fatalf("Timeout waiting for task %s to finalize", task.ID)
		case <-ticker.C:
			// check status in FSM
			if val, ok := leaderEngine.Node.FSM.Tasks.Load(task.ID); ok {
				currentTask := val.(*core.Task)
				if currentTask.Status == core.TaskStatusDone || currentTask.Status == core.TaskStatusFailed {
					if currentTask.Status != expectedStatus {
						t.Fatalf("Expected status %v, got %v", expectedStatus, currentTask.Status)
					}
					return // Success
				}
			}
		}
	}
}

// Helper to create and join nodes
func setupCluster(t *testing.T, count int, basePort int, voteLogic func(id string, t *core.Task) bool) ([]*raftInternal.AgentNode, []*engine.Engine, []string) {
	var nodes []*raftInternal.AgentNode
	var dirs []string
	var engines []*engine.Engine

	// Create nodes and engines
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("node-%d", i)
		port := basePort + i
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		dir, _ := ioutil.TempDir("", id)
		dirs = append(dirs, dir)

		cfg := &raftInternal.Config{
			ID:        id,
			DataDir:   dir,
			BindAddr:  addr,
			Bootstrap: i == 0,
		}

		node, err := raftInternal.NewAgentNode(cfg)
		if err != nil {
			t.Fatalf("Failed to create node %s: %v", id, err)
		}

		mockLLM := &llm.MockClient{Healthy: true}
		eng := engine.NewEngine(node, health.NewMonitor(mockLLM), mockLLM)

		// Set Policy
		nodeID := id // Capture loop variable
		eng.Policy.VoteLogic = func(task *core.Task) bool {
			return voteLogic(nodeID, task)
		}

		nodes = append(nodes, node)
		engines = append(engines, eng)
	}

	// Inject Proxy CommandSender
	proxySender := &TestCommandSender{Engines: engines}
	for _, eng := range engines {
		eng.CommandSender = proxySender
		eng.Start() // Start event loop now
	}

	// Wait for leader
	time.Sleep(3 * time.Second)

	// Join others to 0
	for i := 1; i < count; i++ {
		err := nodes[0].Raft.AddVoter(raft.ServerID(nodes[i].Config.ID), raft.ServerAddress(nodes[i].Config.BindAddr), 0, 0).Error()
		if err != nil {
			t.Fatalf("Failed to join node %d: %v", i, err)
		}
	}

	// Wait for cluster to stabilize
	time.Sleep(5 * time.Second)

	return nodes, engines, dirs
}

package cluster

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

// Cluster represents a managed set of Raft nodes and Engines for testing
type Cluster struct {
	Nodes   []*raftInternal.AgentNode
	Engines []*engine.Engine
	Dirs    []string
	Sender  *TestCommandSender
	T       *testing.T
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

// Setup creates a new cluster with the specified node count, base port, and voting policy.
// It initializes nodes, engines, handles leader join, and returns the Cluster struct.
func Setup(t *testing.T, count int, basePort int, voteLogic func(id string, t *core.Task) bool) *Cluster {
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
		eng.SetVoteLogic(func(task *core.Task) bool {
			return voteLogic(nodeID, task)
		})

		nodes = append(nodes, node)
		engines = append(engines, eng)
	}

	// Inject Proxy CommandSender
	proxySender := &TestCommandSender{Engines: engines}
	for _, eng := range engines {
		eng.SetCommandSender(proxySender)
		eng.Start() // Start event loop now
	}

	// Wait for leader (Bootstrap node 0)
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

	return &Cluster{
		Nodes:   nodes,
		Engines: engines,
		Dirs:    dirs,
		Sender:  proxySender,
		T:       t,
	}
}

// Close cleans up all resources (nodes and directories)
func (c *Cluster) Close() {
	for _, n := range c.Nodes {
		n.Close()
	}
	for _, d := range c.Dirs {
		os.RemoveAll(d)
	}
}

// GetLeader returns the Engine of the current Raft leader
func (c *Cluster) GetLeader() (*engine.Engine, error) {
	for _, n := range c.Nodes {
		if n.Raft.State() == raft.Leader {
			// Find corresponding engine
			for _, e := range c.Engines {
				if e.Node.Config.ID == n.Config.ID {
					// Wait for Leader to see all nodes (Full Quorum Visibility)
					// This ensures Quorum calculation is correct inside Engine
					timeoutCfg := time.After(10 * time.Second)
					for {
						cfg := e.Node.Raft.GetConfiguration().Configuration()
						if len(cfg.Servers) == len(c.Nodes) {
							fmt.Printf("GetLeader: Leader sees %d nodes. Proceeding.\n", len(c.Nodes))
							return e, nil
						}
						select {
						case <-timeoutCfg:
							return nil, fmt.Errorf("timeout waiting for leader to see all %d nodes, saw %d", len(c.Nodes), len(cfg.Servers))
						default:
							time.Sleep(100 * time.Millisecond)
						}
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("no leader found")
}

// SubmitTask submits a task to the cluster leader logic
func (c *Cluster) SubmitTask(content string) (*engine.TaskFuture, error) {
	leader, err := c.GetLeader()
	if err != nil {
		return nil, err
	}

	fmt.Printf("Submitting task to leader: %s\n", leader.Node.Config.ID)
	return leader.SubmitTask(content, "tester")
}

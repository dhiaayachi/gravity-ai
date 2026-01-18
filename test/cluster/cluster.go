package cluster

import (
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/dhiaayachi/gravity-ai/internal/engine"
	agentGrpc "github.com/dhiaayachi/gravity-ai/internal/grpc"
	"github.com/dhiaayachi/gravity-ai/internal/health"
	"github.com/dhiaayachi/gravity-ai/internal/llm"
	raftInternal "github.com/dhiaayachi/gravity-ai/internal/raft"
	tasks_manager "github.com/dhiaayachi/gravity-ai/internal/tasks-manager"
	"github.com/hashicorp/raft"
	"github.com/soheilhy/cmux"
)

// Cluster represents a managed set of Raft nodes and Engines for testing
type Cluster struct {
	Nodes     []*raftInternal.AgentNode
	Engines   []*engine.Engine
	Services  []*agentGrpc.AgentService
	Dirs      []string
	Listeners []net.Listener
	Sender    *TestCommandSender
	T         *testing.T
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
func Setup(t *testing.T, count int, basePort int, mockFactory func(nodeIndex int) llm.Client) *Cluster {
	var nodes []*raftInternal.AgentNode
	var dirs []string
	var engines []*engine.Engine
	var services []*agentGrpc.AgentService
	var listeners []net.Listener

	// Pre-calculate peers
	peers := make(map[string]string)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("node-%d", i)
		port := basePort + i
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		peers[id] = addr
	}

	// Create nodes and engines
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("node-%d", i)
		port := basePort + i
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		dir, _ := os.MkdirTemp("", id)
		dirs = append(dirs, dir)

		// Create peers map for this node
		nodePeers := make(map[string]string)
		for pid, paddr := range peers {
			if pid != id {
				nodePeers[pid] = paddr
			}
		}

		cfg := &raftInternal.Config{
			ID:        id,
			DataDir:   dir,
			BindAddr:  addr,
			Bootstrap: i == 0, // Only 0 bootstraps
			Peers:     nodePeers,
		}

		// Create Listener
		lis, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("Failed to listen on %s: %v", addr, err)
		}
		listeners = append(listeners, lis)

		// Create cmux
		m := cmux.New(lis)
		grpcL := m.Match(cmux.HTTP2())
		raftL := m.Match(cmux.Any())

		node, err := raftInternal.NewAgentNode(cfg, raftL)
		if err != nil {
			t.Fatalf("Failed to create node %s: %v", id, err)
		}

		// Mock LLM setup via factory
		mockLLM := mockFactory(i)

		mgr := tasks_manager.TasksManager{}
		// Client no longer needs port
		eng := engine.NewEngine(node, health.NewMonitor(mockLLM), mockLLM, agentGrpc.NewClient(0), &mgr)

		nodes = append(nodes, node)
		engines = append(engines, eng)

		svc := agentGrpc.NewAgentService(node.Raft, &mgr)

		services = append(services, svc)

		// Start gRPC server
		grpcServer := agentGrpc.NewServer(svc, node, port)
		if err := grpcServer.Start(grpcL); err != nil {
			t.Fatalf("Failed to start gRPC server for %s: %v", id, err)
		}

		// Start cmux
		go func() {
			if err := m.Serve(); err != nil {
				// Log error but check if closed
				// fmt.Printf("cmux error: %v\n", err)
			}
		}()
	}

	// Inject Proxy CommandSender
	proxySender := &TestCommandSender{Engines: engines}
	for _, eng := range engines {
		eng.Start() // Start event loop now
	}

	// Wait for leader (Bootstrap node 0)
	time.Sleep(3 * time.Second)

	// Wait for cluster to stabilize
	time.Sleep(5 * time.Second)

	return &Cluster{
		Nodes:     nodes,
		Engines:   engines,
		Services:  services,
		Dirs:      dirs,
		Listeners: listeners,
		Sender:    proxySender,
		T:         t,
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
	for _, l := range c.Listeners {
		l.Close()
	}
}

// GetLeader returns the Engine of the current Raft leader
func (c *Cluster) GetLeader() (*agentGrpc.AgentService, error) {
	for _, n := range c.Nodes {
		if n.Raft.State() == raft.Leader {
			// Find corresponding engine
			for i, e := range c.Engines {
				if e.Node.Config.ID == n.Config.ID {
					// Wait for Leader to see all nodes (Full Quorum Visibility)
					// This ensures Quorum calculation is correct inside Engine
					timeoutCfg := time.After(10 * time.Second)
					for {
						cfg := e.Node.Raft.GetConfiguration().Configuration()
						if len(cfg.Servers) == len(c.Nodes) {
							fmt.Printf("GetLeader: Leader sees %d nodes. Proceeding.\n", len(c.Nodes))
							return c.Services[i], nil
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
func (c *Cluster) SubmitTask(content string) (*agentGrpc.TaskFuture, error) {
	leader, err := c.GetLeader()
	if err != nil {
		return nil, err
	}

	fmt.Printf("Submitting task to leader")
	return leader.SubmitTask(content, "tester")
}

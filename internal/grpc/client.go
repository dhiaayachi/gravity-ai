package grpc

import (
	"context"
	"fmt"
	"net"

	"github.com/dhiaayachi/gravity-ai/internal/engine"
	pb "github.com/dhiaayachi/gravity-ai/proto/gravity/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Client struct {
	peerPort int
}

func NewClient(peerPort int) *Client {
	return &Client{
		peerPort: peerPort,
	}
}

// Ensure Client implements engine.ClusterClient
var _ engine.ClusterClient = (*Client)(nil)

func (c *Client) SubmitVote(ctx context.Context, leaderAddr string, taskID, agentID string, accepted bool) error {
	// leaderAddr needs to be converted to gRPC port if needed?
	// The leaderAddr from Raft is usually the raft port.
	// We need to know the gRPC port.
	// Current architecture assumes gRPC port is separate.
	// This is a known issue: how do followers know the leader's gRPC port?
	//
	// Option A: Assume fixed offset or same port (if not using separate raft/grpc ports).
	// Option B: Gossip the gRPC address.
	// Option C: Configure all nodes with a map of ID -> gRPC Address.
	//
	// Given "Static Bootstrapping" with -peers flag, we might have raft addresses.
	// If we added -grpc-port, we might assume a convention.
	//
	// But wait! The `leaderAddr` coming from `e.Node.Raft.Leader()` is the Raft transport address (e.g. `node1:8088`).
	// If our gRPC convention is "Raft Port + 1" or explicit config?
	// Currently `internal/grpc/server.go` calculates target using `s.port`.
	// But the *client* doesn't know the remote server's port unless it's standard or discovered.
	//
	// For this 3-node example:
	// node1: raft 8088, grpc 9090
	// node2: raft 8089, grpc 9091
	// node3: raft 8090, grpc 9092
	//
	// There is no simplistic mapping unless we assume one.
	//
	// HOWEVER, the user didn't ask me to solve discovery, just "leverage the grpc method".
	//
	// Workaround for now: Explicit mapping or parsing.
	// Since I don't have a registry, I might need to Parse the host and map it to a port based on known config?
	// Or, easier: Just send to the hostname with a *known* port if running in docker where hostnames are distinct but ports might be standard?
	// In the docker-compose, ports are mapped. Internally spread?
	// Let's check docker-compose.yml.
	//
	// If I can't resolve it perfecty, I might fail.
	// Let's check `transport.go` or `server.go`.
	//
	// `server.go` `getLeaderConn` logic:
	// host, _, err := net.SplitHostPort(string(leaderAddr))
	// target := fmt.Sprintf("%s:%d", host, s.port)
	//
	// Wait, `server.go` logic assumes the leader has the SAME gRPC port as `s.port` (the local node).
	// This implies all nodes use the same gRPC port internally (e.g. 50051), and Docker maps them differently externally?
	// Let's check `docker-compose.yml`.

	// If they have the same internal port, `s.port` is fine.

	// I need `port` in `Client`.
	// So `NewClient` should take the local configured gRPC port?
	// Or `SubmitVote` should take the port? `SubmitVote` interface signature doesn't have it.
	//
	// I will add `grpcPort` to `Client` struct, injected at creation.
	// This assumes homogeneous deployment (all nodes use same internal port).

	// Let's implement that assumption.

	// BUT! In `docker-compose.yml`, usually:
	// node1: GRPC_PORT=50051
	// node2: GRPC_PORT=50051
	// ...
	// So they share the *internal* port.
	// When node2 talks to node1 container, it uses `node1:50051`.
	// So if I know `node1` is the host (from Raft addr `node1:xxxx`), and I know the standard port is 50051.
	//
	// So `Client` needs to know the "Cluster Wide gRPC Port".
	// I will add `defaultPort` to `Client`.

	target := leaderAddr // Placeholder

	// Address Resolution Logic (Duplicate from server.go for now)
	host, _, err := net.SplitHostPort(leaderAddr)
	if err != nil {
		// Try using leaderAddr as host if no port
		host = leaderAddr
	}

	// Assuming the port is the same as we are configured with?
	// I'll make Client hold the `port` to use for peers.

	// Note: This relies on the assumption that peers listen on the `c.peerPort`.
	target = fmt.Sprintf("%s:%d", host, c.peerPort)

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("failed to dial leader: %w", err)
	}
	defer conn.Close()

	client := pb.NewGravityServiceClient(conn)
	req := &pb.SubmitVoteRequest{
		TaskId:   taskID,
		AgentId:  agentID,
		Accepted: accepted,
	}

	resp, err := client.SubmitVote(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("leader rejected vote: %s", resp.Message)
	}
	return nil
}

// We need net package

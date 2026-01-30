package clusterstate

import (
	"fmt"

	raftInternal "github.com/dhiaayachi/gravity-ai/internal/raft"
	"github.com/dhiaayachi/gravity-ai/internal/raft/fsm"
	"github.com/dhiaayachi/gravity-ai/internal/state"
	"github.com/hashicorp/raft"
)

// ClusterState defines the interface for querying cluster status
type ClusterState interface {
	IsLeader() bool
	GetLeaderAddr() string
	GetFormattedID() string // For logging
	GetServerCount() (int, error)
	DropLeader() error
	TransferLeadership(id string) error
	GetClusterAgentsState() []state.AgentState
}

type DefaultClusterState struct {
	Node *raftInternal.AgentNode
	fsm  fsm.FSM
}

func NewDefaultClusterState(node *raftInternal.AgentNode, fsm fsm.FSM) *DefaultClusterState {
	return &DefaultClusterState{Node: node, fsm: fsm}
}

func (d *DefaultClusterState) IsLeader() bool {
	return d.Node.Raft.State() == raft.Leader
}

func (d *DefaultClusterState) DropLeader() error {
	f := d.Node.Raft.LeadershipTransfer()
	return f.Error()
}

func (d *DefaultClusterState) TransferLeadership(id string) error {
	address, err := d.getPeerAddress(id)
	if err != nil {
		return err
	}
	f := d.Node.Raft.LeadershipTransferToServer(raft.ServerID(id), raft.ServerAddress(address))
	return f.Error()
}

func (d *DefaultClusterState) getPeerAddress(id string) (string, error) {
	cfg := d.Node.Raft.GetConfiguration()
	if err := cfg.Error(); err != nil {
		return "", err
	}
	for _, srv := range cfg.Configuration().Servers {
		if string(srv.ID) == id {
			return string(srv.Address), nil
		}
	}
	return "", fmt.Errorf("peer not found: %s", id)
}

func (d *DefaultClusterState) GetLeaderAddr() string {
	addr, _ := d.Node.Raft.LeaderWithID()
	return string(addr)
}

func (d *DefaultClusterState) GetFormattedID() string {
	return d.Node.Config.ID
}

func (d *DefaultClusterState) GetServerCount() (int, error) {
	cfg := d.Node.Raft.GetConfiguration()
	if err := cfg.Error(); err != nil {
		return 0, err
	}
	return len(cfg.Configuration().Servers), nil
}

func (d *DefaultClusterState) GetClusterAgentsState() []state.AgentState {
	cfg := d.Node.Raft.GetConfiguration()
	if err := cfg.Error(); err != nil {
		return nil
	}

	var states []state.AgentState

	for _, srv := range cfg.Configuration().Servers {
		id := string(srv.ID)

		// Raft State (Only accurate for self really, but we can infer Role if we track it?
		// Actually RaftState from others is distinct. We only know if *we* are leader.
		// For others, we only know they are in the config.
		// Let's just put "Unknown" or exclude RaftState for others if not leader?
		// Or maybe we can just say "Voter"?

		raftState := "Voter"
		if id == d.Node.Config.ID {
			raftState = d.Node.Raft.State().String()
		} else {
			// If we are leader, we might look at raft stats to see if they are followers?
			// Simplify for now.
		}

		rep := d.fsm.GetReputation(id)
		meta := d.fsm.GetMetadata(id)

		agentState := state.AgentState{
			ID:         id,
			RaftState:  raftState,
			Reputation: rep,
		}

		if meta != nil {
			agentState.LLMProvider = meta.LLMProvider
			agentState.LLMModel = meta.LLMModel
		}

		states = append(states, agentState)
	}
	return states
}

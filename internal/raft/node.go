package raft

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/hashicorp/raft"
	raftwal "github.com/hashicorp/raft-wal"
)

type AgentNode struct {
	Raft      *raft.Raft
	FSM       *FSM
	Transport *ReputationTransport
	Config    *Config
	// Keep stores to close them
	logStore    *raftwal.WAL
	stableStore *raftwal.WAL
}

type Config struct {
	ID        string
	DataDir   string
	BindAddr  string
	Bootstrap bool
	Peers     map[string]string // ID -> Address
}

func NewAgentNode(cfg *Config, listener net.Listener) (*AgentNode, error) {
	// Setup FSM
	fsm := NewFSM(cfg.ID)

	// Setup Config
	raftConfig := raft.DefaultConfig()
	raftConfig.LocalID = raft.ServerID(cfg.ID)

	// Setup Storage
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data dir: %w", err)
	}

	logStore, err := raftwal.Open(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create log store: %w", err)
	}

	snapStore, err := raft.NewFileSnapshotStore(cfg.DataDir, 2, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot store: %w", err)
	}

	// Setup Transport using StreamLayer and existing listener
	streamLayer := NewStreamLayer(listener)

	// We use NewNetworkTransport with the stream layer
	// MaxPool: 3, Timeout: 10s, LogOutput: os.Stderr
	realTransport := raft.NewNetworkTransport(streamLayer, 3, 10*time.Second, os.Stderr)

	transport := NewReputationTransport(realTransport, fsm, cfg.ID)

	// Create Raft
	r, err := raft.NewRaft(raftConfig, fsm, logStore, logStore, snapStore, transport)
	if err != nil {
		return nil, fmt.Errorf("failed to create raft: %w", err)
	}

	// Bootstrap if requested
	if cfg.Bootstrap {
		servers := []raft.Server{
			{
				ID:      raftConfig.LocalID,
				Address: realTransport.LocalAddr(),
			},
		}

		for peerID, peerAddr := range cfg.Peers {
			servers = append(servers, raft.Server{
				ID:      raft.ServerID(peerID),
				Address: raft.ServerAddress(peerAddr),
			})
		}

		configuration := raft.Configuration{
			Servers: servers,
		}
		r.BootstrapCluster(configuration)
	}

	return &AgentNode{
		Raft:        r,
		FSM:         fsm,
		Transport:   transport,
		Config:      cfg,
		logStore:    logStore,
		stableStore: logStore,
	}, nil
}

func (a *AgentNode) Close() error {
	f := a.Raft.Shutdown()
	if err := f.Error(); err != nil {
		return err
	}

	if err := a.logStore.Close(); err != nil {
		return err
	}
	if err := a.stableStore.Close(); err != nil {
		return err
	}
	if err := a.Transport.Close(); err != nil {
		return err
	}
	if err := a.logStore.Close(); err != nil {
		return err
	}
	return nil
}

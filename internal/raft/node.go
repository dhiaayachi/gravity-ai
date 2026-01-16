package raft

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb"
)

type AgentNode struct {
	Raft      *raft.Raft
	FSM       *FSM
	Transport *ReputationTransport
	Config    *Config
	// Keep stores to close them
	logStore    *raftboltdb.BoltStore
	stableStore *raftboltdb.BoltStore
}

type Config struct {
	ID        string
	DataDir   string
	BindAddr  string
	Bootstrap bool
	Peers     map[string]Peer // ID -> Address
}

type Peer struct {
	RaftAddr string
	GrpcAddr string
}

func NewAgentNode(cfg *Config) (*AgentNode, error) {
	// Setup FSM
	fsm := NewFSM(cfg.ID)

	// Setup Config
	raftConfig := raft.DefaultConfig()
	raftConfig.LocalID = raft.ServerID(cfg.ID)

	// Setup Storage
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data dir: %w", err)
	}

	logStore, err := raftboltdb.NewBoltStore(filepath.Join(cfg.DataDir, "raft-log.bolt"))
	if err != nil {
		return nil, fmt.Errorf("failed to create log store: %w", err)
	}

	stableStore, err := raftboltdb.NewBoltStore(filepath.Join(cfg.DataDir, "raft-stable.bolt"))
	if err != nil {
		return nil, fmt.Errorf("failed to create stable store: %w", err)
	}

	snapStore, err := raft.NewFileSnapshotStore(cfg.DataDir, 2, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot store: %w", err)
	}

	// Setup Transport
	addr, err := net.ResolveTCPAddr("tcp", cfg.BindAddr)
	if err != nil {
		return nil, err
	}
	realTransport, err := raft.NewTCPTransport(cfg.BindAddr, addr, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to create transport: %w", err)
	}

	transport := NewReputationTransport(realTransport, fsm, cfg.ID)

	// Create Raft
	r, err := raft.NewRaft(raftConfig, fsm, logStore, stableStore, snapStore, transport)
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
				Address: raft.ServerAddress(peerAddr.RaftAddr),
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
		stableStore: stableStore,
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
	return nil
}

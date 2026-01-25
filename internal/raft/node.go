package raft

import (
	"fmt"
	"os"

	"github.com/dhiaayachi/gravity-ai/internal/raft/fsm"
	"github.com/hashicorp/raft"
	raftwal "github.com/hashicorp/raft-wal"
	"go.uber.org/zap"
)

type AgentNode struct {
	Raft      *raft.Raft
	FSM       *fsm.SyncMapFSM
	Transport *ReputationTransport
	Config    *Config
	// Keep stores to close them
	logStore    *raftwal.WAL
	stableStore *raftwal.WAL
	logger      *zap.Logger
}

type Config struct {
	ID        string
	DataDir   string
	BindAddr  string
	Bootstrap bool
	Peers     map[string]string // ID -> Address
}

func NewAgentNode(cfg *Config, transport raft.Transport, logger *zap.Logger) (*AgentNode, error) {
	nodeLogger := logger.With(zap.String("component", "raft"), zap.String("agent_id", cfg.ID))

	// Setup SyncMapFSM
	mFsm := fsm.NewSyncMapFSM(cfg.ID)

	// Setup Config
	raftConfig := raft.DefaultConfig()
	raftConfig.LocalID = raft.ServerID(cfg.ID)
	// Hashicorp Raft uses its own logger (hclog). We can bridge it if needed, but for now strict to AgentNode logs.

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

	// Wrap transport with ReputationTransport
	repTransport := NewReputationTransport(transport, mFsm, cfg.ID)

	// Create Raft
	r, err := raft.NewRaft(raftConfig, mFsm, logStore, logStore, snapStore, repTransport)
	if err != nil {
		return nil, fmt.Errorf("failed to create raft: %w", err)
	}

	// Bootstrap if requested
	if cfg.Bootstrap {
		nodeLogger.Info("Bootstrapping cluster")
		servers := []raft.Server{
			{
				ID:      raftConfig.LocalID,
				Address: transport.LocalAddr(),
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
		FSM:         mFsm,
		Transport:   repTransport,
		Config:      cfg,
		logStore:    logStore,
		stableStore: logStore,
		logger:      nodeLogger,
	}, nil
}

func (a *AgentNode) Close() error {
	a.logger.Info("Stopping Raft node")
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

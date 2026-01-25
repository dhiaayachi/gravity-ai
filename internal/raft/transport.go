package raft

import (
	"log"

	fsm2 "github.com/dhiaayachi/gravity-ai/internal/raft/fsm"
	"github.com/hashicorp/raft"
)

// ReputationTransport wraps a raft.Transport to enforce reputation-based voting
type ReputationTransport struct {
	raft.Transport
	fsm     *fsm2.SyncMapFSM
	localID string
	ch      chan raft.RPC
}

// NewReputationTransport creates a new ReputationTransport
func NewReputationTransport(base raft.Transport, fsm *fsm2.SyncMapFSM, localID string) *ReputationTransport {
	t := &ReputationTransport{
		Transport: base,
		fsm:       fsm,
		localID:   localID,
		ch:        make(chan raft.RPC),
	}
	go t.run()
	return t
}

func (t *ReputationTransport) Consumer() <-chan raft.RPC {
	return t.ch
}

func (t *ReputationTransport) run() {
	// Consume from base transport
	baseCh := t.Transport.Consumer()
	for rpc := range baseCh {
		switch req := rpc.Command.(type) {
		case *raft.RequestVoteRequest:
			// Check reputation
			var candidateRep, localRep int

			if val, ok := t.fsm.Reputations.Load(string(req.RPCHeader.Addr)); ok {
				candidateRep = val.(int)
			}
			if val, ok := t.fsm.Reputations.Load(t.localID); ok {
				localRep = val.(int)
			}

			if localRep > candidateRep {
				// Reject vote
				log.Printf("[%s] Rejecting vote from %s (Rep: %d) because local rep (%d) is higher", t.localID, req.RPCHeader.Addr, candidateRep, localRep)
				rpc.Respond(&raft.RequestVoteResponse{
					Term:    req.Term,
					Granted: false,
				}, nil)
				continue
			}
		}
		// Forward message
		t.ch <- rpc
	}
}

// Close closes the transport
func (t *ReputationTransport) Close() error {
	if closer, ok := t.Transport.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

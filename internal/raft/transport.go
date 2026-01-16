package raft

import (
	"log"

	"net"
	"time"

	"github.com/hashicorp/raft"
)

// StreamLayer implements raft.StreamLayer interface for cmux
type StreamLayer struct {
	net.Listener
}

func NewStreamLayer(l net.Listener) *StreamLayer {
	return &StreamLayer{Listener: l}
}

// Dial implements the StreamLayer interface.
func (t *StreamLayer) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", string(address), timeout)
}

// Addr implements the net.Listener interface
func (t *StreamLayer) Addr() net.Addr {
	return t.Listener.Addr()
}

// ReputationTransport wraps a raft.Transport to enforce reputation-based voting
type ReputationTransport struct {
	raft.Transport
	fsm     *FSM
	localID string
	ch      chan raft.RPC
}

// NewReputationTransport creates a new ReputationTransport
func NewReputationTransport(base raft.Transport, fsm *FSM, localID string) *ReputationTransport {
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
			// Check reputation
			var candidateRep, localRep int

			if val, ok := t.fsm.Reputations.Load(string(req.Candidate)); ok {
				candidateRep = val.(int)
			}
			if val, ok := t.fsm.Reputations.Load(t.localID); ok {
				localRep = val.(int)
			}

			if localRep > candidateRep {
				// Reject vote
				log.Printf("[%s] Rejecting vote from %s (Rep: %d) because local rep (%d) is higher", t.localID, req.Candidate, candidateRep, localRep)
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

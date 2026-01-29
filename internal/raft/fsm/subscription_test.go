package fsm

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/assert"
)

func TestSyncMapFSM_Subscribe_Reputation(t *testing.T) {
	fsm := NewSyncMapFSM("node1")

	// 1. Subscribe to reputation updates for agent-1 starting from version 0
	ch := fsm.Subscribe(KeyPrefixReputation, "agent-1", 0)

	// Verify subscriber is in map
	fsm.mutex.RLock()
	subs := fsm.Subscribers[KeyPrefixReputation+":agent-1"]
	assert.Len(t, subs, 1)
	fsm.mutex.RUnlock()

	// 2. Apply an update
	rep := 50
	repBytes, _ := json.Marshal(rep)
	cmd := LogCommand{
		Type:    CommandTypeUpdateReputation,
		AgentID: "agent-1",
		Value:   repBytes,
	}
	cmdBytes, _ := json.Marshal(cmd)
	logEntry := &raft.Log{Data: cmdBytes}

	fsm.Apply(logEntry)

	// 3. Verify notification
	select {
	case entry, ok := <-ch:
		if !ok {
			t.Fatal("Channel closed unexpectedly")
		}
		assert.Equal(t, KeyPrefixReputation+":agent-1", entry.Key)
		assert.Equal(t, uint64(1), entry.Version)
		assert.Equal(t, 50, entry.Value.(int))

		// Verify channel is closed
		select {
		case _, ok := <-ch:
			assert.False(t, ok, "Channel should be closed after notification")
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Channel should be closed")
		}

	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for subscription update")
	}

	// Verify subscriber is removed from map
	fsm.mutex.RLock()
	subsAfter := fsm.Subscribers[KeyPrefixReputation+":agent-1"]
	assert.Len(t, subsAfter, 0, "Subscriber should be removed from map")
	fsm.mutex.RUnlock()
}

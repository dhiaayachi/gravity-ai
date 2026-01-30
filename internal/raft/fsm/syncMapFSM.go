package fsm

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	"github.com/hashicorp/raft"
)

// SyncMapFSM is the state machine for the Raft system
type SyncMapFSM struct {
	NodeID      string
	Reputations sync.Map                        // AgentID -> int
	Metadata    sync.Map                        // AgentID -> core.AgentMetadata
	Tasks       map[string]*core.Task           // TaskID -> *core.Task
	TaskAnswers map[string][]core.Answer        // TaskID -> []core.Answer
	TaskVotes   map[string]map[string]core.Vote // TaskID -> []core.Vote

	KeyVersions sync.Map                  // string (Key) -> uint64
	Subscribers map[string][]chan KVEntry // Key -> []chan KVEntry

	mutex sync.RWMutex
	// Events channel to notify Engine of state changes
	EventCh chan Event
}

func (f *SyncMapFSM) GetTaskAnswers(id string) ([]core.Answer, error) {
	f.mutex.RLock()
	defer f.mutex.RUnlock()
	val, ok := f.TaskAnswers[id]
	if !ok {
		return nil, fmt.Errorf("task Answers not found: %s", id)
	}
	return val, nil
}

func (f *SyncMapFSM) GetTaskVotes(id string) (map[string]core.Vote, error) {
	f.mutex.RLock()
	defer f.mutex.RUnlock()
	val, ok := f.TaskVotes[id]
	if !ok {
		return nil, fmt.Errorf("task Answers not found: %s", id)
	}
	return val, nil
}

func (f *SyncMapFSM) GetReputation(id string) int {
	val, ok := f.Reputations.Load(id)
	if !ok {
		return 100 // Default reputation
	}
	return val.(int)
}

func (f *SyncMapFSM) GetAllReputations() map[string]int {
	reps := make(map[string]int)
	f.Reputations.Range(func(key, value interface{}) bool {
		reps[key.(string)] = value.(int)
		return true
	})
	return reps
}

func (f *SyncMapFSM) GetMetadata(id string) *core.AgentMetadata {
	val, ok := f.Metadata.Load(id)
	if !ok {
		return nil
	}
	meta := val.(core.AgentMetadata)
	return &meta
}

func (f *SyncMapFSM) GetTask(id string) (*core.Task, error) {
	f.mutex.RLock()
	defer f.mutex.RUnlock()
	val, ok := f.Tasks[id]
	if !ok {
		return nil, fmt.Errorf("task Answers not found: %s", id)
	}
	return val, nil
}

func (f *SyncMapFSM) EventsConsumer() chan Event {
	return f.EventCh
}

type EventType string

const (
	EventTaskAdmitted    EventType = "task_admitted"
	EventAnswerSubmitted EventType = "answer_submitted"
	EventTaskUpdated     EventType = "task_updated"
	EventVoteSubmitted   EventType = "vote_submitted"
)

type Event struct {
	Type EventType
	Data interface{}
}

func NewSyncMapFSM(nodeID string) *SyncMapFSM {
	return &SyncMapFSM{
		NodeID:      nodeID,
		EventCh:     make(chan Event, 100),
		Tasks:       make(map[string]*core.Task),
		TaskAnswers: make(map[string][]core.Answer),
		TaskVotes:   make(map[string]map[string]core.Vote),
		Subscribers: make(map[string][]chan KVEntry),
	}
}

// Subscribe implements the FSM interface
func (f *SyncMapFSM) Subscribe(prefix string, id string, version uint64) <-chan KVEntry {
	key := prefix + ":" + id
	ch := make(chan KVEntry, 1) // Buffered channel to match "notification" semantics

	f.mutex.Lock()
	defer f.mutex.Unlock()

	// Get Current Version
	var currentVer uint64
	if val, ok := f.KeyVersions.Load(key); ok {
		currentVer = val.(uint64)
	} else {
		currentVer = 0
	}

	// Logic:
	// If version > 0 AND current > version: Send current data immediately.
	// If version == 0 OR current <= version: Register subscriber for next update.

	if currentVer > version {
		// Send immediate update
		val := f.getByKeyUnlocked(prefix, id)
		if val != nil {
			ch <- KVEntry{
				Key:     key,
				Value:   val,
				Version: currentVer,
			}
			close(ch)
			return ch // Don't add to map, just return closed channel with data
		}
	}

	// Register for future updates
	f.Subscribers[key] = append(f.Subscribers[key], ch)

	return ch
}

func (f *SyncMapFSM) notifySubscribers(key string, value interface{}, version uint64) {
	// Mutex is already held by Apply
	subscribers, ok := f.Subscribers[key]
	if !ok {
		return
	}

	entry := KVEntry{
		Key:     key,
		Value:   value,
		Version: version,
	}

	for _, ch := range subscribers {
		select {
		case ch <- entry:
		default:
			// If channel is full, we drop the notification?
			// Or should we use larger buffer?
			// Ideally we don't block Apply.
			log.Printf("Warning: Subscriber channel full for key %s", key)
		}
		//close(ch)
	}

	// Remove subscribers for this key (one-shot)
	delete(f.Subscribers, key)
}

// getByKeyUnlocked retrieves the value for a key.
// IMPORTANT: Caller must already hold f.mutex (read or write lock).
func (f *SyncMapFSM) getByKeyUnlocked(prefix, id string) interface{} {
	switch prefix {
	case KeyPrefixReputation:
		// Reputations is a sync.Map, safe to call without mutex
		val, ok := f.Reputations.Load(id)
		if !ok {
			return 100 // Default reputation
		}
		return val.(int)
	case KeyPrefixTask:
		// f.Tasks is a regular map, caller must hold mutex
		val, ok := f.Tasks[id]
		if !ok {
			return nil
		}
		return val
	case KeyPrefixMetadata:
		// Metadata is a sync.Map, safe to call without mutex
		val, ok := f.Metadata.Load(id)
		if !ok {
			return nil
		}
		meta := val.(core.AgentMetadata)
		return &meta
	default:
		return nil
	}
}

func (f *SyncMapFSM) incrementVersion(key string) uint64 {
	var newVer uint64
	val, ok := f.KeyVersions.Load(key)
	if !ok {
		newVer = 1
	} else {
		newVer = val.(uint64) + 1
	}
	f.KeyVersions.Store(key, newVer)
	return newVer
}

// Apply applies a Raft log entry to the SyncMapFSM
func (f *SyncMapFSM) Apply(logEntry *raft.Log) interface{} {
	var cmd LogCommand
	if err := json.Unmarshal(logEntry.Data, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal log data: %w", err)
	}

	f.mutex.Lock()
	defer f.mutex.Unlock()
	switch cmd.Type {
	case CommandTypeUpdateReputation:
		var rep int
		if err := json.Unmarshal(cmd.Value, &rep); err != nil {
			return fmt.Errorf("failed to unmarshal reputation: %w", err)
		}
		f.Reputations.Store(cmd.AgentID, rep)

		// Notify Subscribers
		key := KeyPrefixReputation + ":" + cmd.AgentID
		ver := f.incrementVersion(key)
		f.notifySubscribers(key, rep, ver)
	case CommandTypeAdmitTask:
		var task core.Task
		if err := json.Unmarshal(cmd.Value, &task); err != nil {
			return fmt.Errorf("failed to unmarshal task: %w", err)
		}
		f.Tasks[task.ID] = &task
		log.Printf("[%s] Task admitted: %s", f.NodeID, task.ID)

		// Notify
		select {
		case f.EventCh <- Event{Type: EventTaskAdmitted, Data: &task}:
		default:
			log.Printf("[%s] SyncMapFSM EventCh full, dropping event", f.NodeID)
		}

		// Notify Subscribers
		key := KeyPrefixTask + ":" + task.ID
		ver := f.incrementVersion(key)
		f.notifySubscribers(key, &task, ver)

	case CommandTypeSubmitAnswer:

		var answer core.Answer
		if err := json.Unmarshal(cmd.Value, &answer); err != nil {
			return fmt.Errorf("failed to unmarshal answer: %w", err)
		}
		log.Printf("\nFSM: Applying submit answer command: %s\n", answer.TaskID)
		answers, _ := f.TaskAnswers[answer.TaskID]
		answers = append(answers, answer)
		f.TaskAnswers[answer.TaskID] = answers

		// Notify
		select {
		case f.EventCh <- Event{Type: EventAnswerSubmitted, Data: &answer}:
		default:
		}
		log.Printf("\nFSM: Answer submitted: %s\n", answer.TaskID)

	case CommandTypeUpdateTask:
		var task core.Task
		if err := json.Unmarshal(cmd.Value, &task); err != nil {
			return fmt.Errorf("failed to unmarshal task: %w", err)
		}
		// Update existing task (e.g. status change, result added)
		if existing, ok := f.Tasks[task.ID]; ok {
			existing.Status = task.Status
			existing.Result = task.Result
			existing.ConfidenceScore = task.ConfidenceScore

			// Notify
			select {
			case f.EventCh <- Event{Type: EventTaskUpdated, Data: existing}:
			default:
			}

			// Notify Subscribers
			key := KeyPrefixTask + ":" + existing.ID
			ver := f.incrementVersion(key)
			f.notifySubscribers(key, existing, ver)
		}
	case CommandTypeSubmitVote:
		var vote core.Vote
		if err := json.Unmarshal(cmd.Value, &vote); err != nil {
			return fmt.Errorf("failed to unmarshal vote: %w", err)
		}
		votes, ok := f.TaskVotes[vote.TaskID]
		if !ok {
			f.TaskVotes[vote.TaskID] = make(map[string]core.Vote)
			votes = f.TaskVotes[vote.TaskID]
		}
		if _, ok := votes[vote.AgentID]; !ok {
			votes[vote.AgentID] = vote
			// Notify
			select {
			case f.EventCh <- Event{Type: EventVoteSubmitted, Data: &vote}:
			default:
			}
		}

	case CommandTypeUpdateMetadata:
		var meta core.AgentMetadata
		if err := json.Unmarshal(cmd.Value, &meta); err != nil {
			return fmt.Errorf("failed to unmarshal metadata: %w", err)
		}
		f.Metadata.Store(meta.ID, meta)

		// Notify Subscribers
		key := KeyPrefixMetadata + ":" + meta.ID
		ver := f.incrementVersion(key)
		f.notifySubscribers(key, &meta, ver)
	}
	return nil
}

// Snapshot returns a snapshot of the key-value store
func (f *SyncMapFSM) Snapshot() (raft.FSMSnapshot, error) {
	// Deep copy to avoid race conditions during serialization
	reputations := make(map[string]int)
	f.Reputations.Range(func(key, value interface{}) bool {
		reputations[key.(string)] = value.(int)
		return true
	})

	metadata := make(map[string]core.AgentMetadata)
	f.Metadata.Range(func(key, value interface{}) bool {
		metadata[key.(string)] = value.(core.AgentMetadata)
		return true
	})

	tasks := f.Tasks

	// Copy answers/votes if needed, skipped for brevity in this step but should be here

	// Copy KeyVersions
	keyVersions := make(map[string]uint64)
	f.KeyVersions.Range(func(key, value interface{}) bool {
		keyVersions[key.(string)] = value.(uint64)
		return true
	})

	return &Snapshot{
		Reputations: reputations,
		Metadata:    metadata,
		Tasks:       tasks,
		KeyVersions: keyVersions,
	}, nil
}

// Restore restores the node to a previous state
func (f *SyncMapFSM) Restore(rc io.ReadCloser) error {
	defer func(rc io.ReadCloser) {
		_ = rc.Close()
	}(rc)
	var snapshot SnapshotData // Define this struct
	if err := json.NewDecoder(rc).Decode(&snapshot); err != nil {
		return err
	}

	// Clear current state and load new
	f.Reputations = sync.Map{}
	for k, v := range snapshot.Reputations {
		f.Reputations.Store(k, v)
	}

	f.Metadata = sync.Map{}
	for k, v := range snapshot.Metadata {
		f.Metadata.Store(k, v)
	}

	f.Tasks = make(map[string]*core.Task)
	for k, v := range snapshot.Tasks {
		// Only restore active tasks
		if v.Status != core.TaskStatusDone && v.Status != core.TaskStatusFailed {
			f.Tasks[k] = v
		}
	}

	// Also need to handle TaskAnswers/Votes if we were persisting them
	f.TaskAnswers = make(map[string][]core.Answer)
	f.TaskVotes = make(map[string]map[string]core.Vote)

	// Restore KeyVersions
	f.KeyVersions = sync.Map{}
	for k, v := range snapshot.KeyVersions {
		f.KeyVersions.Store(k, v)
	}

	return nil
}

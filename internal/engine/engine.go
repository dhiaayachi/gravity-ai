package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	"github.com/dhiaayachi/gravity-ai/internal/health"
	"github.com/dhiaayachi/gravity-ai/internal/llm"
	raftInternal "github.com/dhiaayachi/gravity-ai/internal/raft"
	"github.com/google/uuid"
	"github.com/hashicorp/raft"
)

type Engine struct {
	Node *raftInternal.AgentNode // Public for E2E tests

	// Decoupled dependencies
	fsm        *raftInternal.FSM
	health     *health.Monitor
	llm        llm.Client
	nodeConfig *raftInternal.Config // For ID

	// Interfaces for mocking
	commandSender CommandSender
	clusterState  ClusterState

	// listeners for task completion (TaskID -> chan *core.Task)
	listeners sync.Map

	// timers for leader phases (TaskID -> *time.Timer)
	timers sync.Map

	ProposalTimeout time.Duration
	VoteTimeout     time.Duration
}

// CommandSender defines the interface for submitting Raft commands
type CommandSender interface {
	Apply(cmd []byte, timeout time.Duration) error
}

// ClusterState defines the interface for querying cluster status
type ClusterState interface {
	IsLeader() bool
	GetFormattedID() string // For logging
	GetServerCount() (int, error)
}

type defaultCommandSender struct {
	Raft *raft.Raft
}

func (d *defaultCommandSender) Apply(cmd []byte, timeout time.Duration) error {
	f := d.Raft.Apply(cmd, timeout)
	return f.Error()
}

type defaultClusterState struct {
	Node *raftInternal.AgentNode
}

func (d *defaultClusterState) IsLeader() bool {
	return d.Node.Raft.State() == raft.Leader
}

func (d *defaultClusterState) GetFormattedID() string {
	return d.Node.Config.ID
}

func (d *defaultClusterState) GetServerCount() (int, error) {
	cfg := d.Node.Raft.GetConfiguration()
	if err := cfg.Error(); err != nil {
		return 0, err
	}
	return len(cfg.Configuration().Servers), nil
}

// TaskFuture allows waiting for a task to be finalized
type TaskFuture struct {
	TaskID   string
	resultCh <-chan *core.Task
}

// Await blocks until the task is finalized or context is cancelled
func (f *TaskFuture) Await(ctx context.Context) (*core.Task, error) {
	select {
	case task := <-f.resultCh:
		return task, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func NewEngine(node *raftInternal.AgentNode, health *health.Monitor, llm llm.Client) *Engine {
	return &Engine{
		Node:            node,
		fsm:             node.FSM,
		health:          health,
		llm:             llm,
		nodeConfig:      node.Config,
		commandSender:   &defaultCommandSender{Raft: node.Raft},
		clusterState:    &defaultClusterState{Node: node},
		ProposalTimeout: 30 * time.Second,
		VoteTimeout:     10 * time.Second,
	}
}

// SetCommandSender sets the command sender (for testing/mocking)
func (e *Engine) SetCommandSender(sender CommandSender) {
	e.commandSender = sender
}

// SetClusterState sets the cluster state (for testing/mocking)
func (e *Engine) SetClusterState(state ClusterState) {
	e.clusterState = state
}

// SubmitTask handles a new task submission and returns a future, called from the leader
func (e *Engine) SubmitTask(content string, requester string) (*TaskFuture, error) {
	if !e.clusterState.IsLeader() {
		return nil, fmt.Errorf("not leader")
	}

	taskID := uuid.New().String()
	task := &core.Task{
		ID:        taskID,
		Content:   content,
		Status:    core.TaskStatusAdmitted,
		Requester: requester,
		CreatedAt: time.Now(),
	}

	// Register listener
	ch := make(chan *core.Task, 1)
	e.listeners.Store(taskID, ch)

	taskBytes, err := json.Marshal(task)
	if err != nil {
		e.listeners.Delete(taskID)
		return nil, err
	}

	cmd := raftInternal.LogCommand{
		Type:  raftInternal.CommandTypeAdmitTask,
		Value: taskBytes,
	}

	b, err := json.Marshal(cmd)
	if err != nil {
		e.listeners.Delete(taskID)
		return nil, err
	}

	if err := e.commandSender.Apply(b, 5*time.Second); err != nil {
		e.listeners.Delete(taskID)
		return nil, err
	}

	return &TaskFuture{
		TaskID:   taskID,
		resultCh: ch,
	}, nil
}

// SubmitAnswer handles answer submission
func (e *Engine) SubmitAnswer(taskID, agentID, content string) error {
	if !e.clusterState.IsLeader() {
		return fmt.Errorf("not leader")
	}

	answer := &core.Answer{
		TaskID:  taskID,
		AgentID: agentID,
		Content: content,
	}

	answerBytes, err := json.Marshal(answer)
	if err != nil {
		return err
	}

	cmd := raftInternal.LogCommand{
		Type:  raftInternal.CommandTypeSubmitAnswer,
		Value: answerBytes,
	}

	b, err := json.Marshal(cmd)
	if err != nil {
		return err
	}

	return e.commandSender.Apply(b, 5*time.Second)
}

// SubmitVote handles vote submission
func (e *Engine) SubmitVote(taskID, agentID string, accepted bool) error {
	if !e.clusterState.IsLeader() {
		return fmt.Errorf("not leader")
	}

	vote := &core.Vote{
		TaskID:   taskID,
		AgentID:  agentID,
		Accepted: accepted,
	}

	voteBytes, err := json.Marshal(vote)
	if err != nil {
		return err
	}

	cmd := raftInternal.LogCommand{
		Type:  raftInternal.CommandTypeSubmitVote,
		Value: voteBytes,
	}

	b, err := json.Marshal(cmd)
	if err != nil {
		return err
	}

	return e.commandSender.Apply(b, 5*time.Second)
}

func (e *Engine) Start() {
	go func() {
		for event := range e.fsm.EventCh {
			switch event.Type {
			case raftInternal.EventTaskAdmitted:
				task := event.Data.(*core.Task)
				e.handleTaskAdmitted(task)
			case raftInternal.EventAnswerSubmitted:
				// If leader, check if we can move to Proposal
				if e.clusterState.IsLeader() {
					ans := event.Data.(*core.Answer)
					// Get task for validation
					if val, ok := e.fsm.Tasks.Load(ans.TaskID); ok {
						task := val.(*core.Task)
						e.runProposalPhase(task)
					}
				}
			case raftInternal.EventTaskUpdated:
				task := event.Data.(*core.Task)
				switch task.Status {
				case core.TaskStatusDone, core.TaskStatusFailed:
					e.notifyTaskCompletion(task)
				case core.TaskStatusProposal:
					// All nodes vote
					e.runVotePhase(task)
				}
			case raftInternal.EventVoteSubmitted:
				if e.clusterState.IsLeader() {
					vote := event.Data.(*core.Vote)
					if val, ok := e.fsm.Tasks.Load(vote.TaskID); ok {
						task := val.(*core.Task)
						e.finalizeTask(task)
					}
				}
			}
		}
	}()
}

func (e *Engine) notifyTaskCompletion(task *core.Task) {
	e.stopTimer(task.ID) // Cleanup timer if any
	if val, ok := e.listeners.Load(task.ID); ok {
		ch := val.(chan *core.Task)
		ch <- task
		close(ch)
		e.listeners.Delete(task.ID)
	}
}

func (e *Engine) handleTaskAdmitted(task *core.Task) {
	log.Printf("[%s] Received task admission: %s. Starting local brainstorm.", e.nodeConfig.ID, task.ID)

	// Start Proposal Timer (waiting for answers)
	if e.clusterState.IsLeader() {
		e.startTimer(task.ID, e.ProposalTimeout, "Brainstorming/Proposal phase exceeded")
	}

	// Every node participates in brainstorm
	e.runBrainstormPhase(task)
}

func (e *Engine) runBrainstormPhase(task *core.Task) {
	log.Printf("[%s] Starting Brainstorm phase for task %s", e.nodeConfig.ID, task.ID)

	// Simulate "Broadcasting" logic by just getting a local answer for now
	ansContent, err := e.llm.Generate(task.Content)
	if err != nil {
		log.Printf("[%s] LLM generation failed: %v", e.nodeConfig.ID, err)
		return
	}

	answer := core.Answer{
		TaskID:  task.ID,
		AgentID: string(e.nodeConfig.ID),
		Content: ansContent,
	}

	ansBytes, _ := json.Marshal(answer)
	cmd := raftInternal.LogCommand{
		Type:  raftInternal.CommandTypeSubmitAnswer,
		Value: ansBytes,
	}
	b, _ := json.Marshal(cmd)

	if err := e.commandSender.Apply(b, 5*time.Second); err != nil {
		log.Printf("[%s] Failed to apply answer: %v", e.nodeConfig.ID, err)
		return
	}
}

func (e *Engine) runProposalPhase(task *core.Task) {
	log.Printf("[%s] Starting Proposal phase for task %s", e.nodeConfig.ID, task.ID)

	// Retrieve answers (Reading from FSM state locally since we are leader)
	var answers []core.Answer
	if val, ok := e.fsm.TaskAnswers.Load(task.ID); ok {
		answers = val.([]core.Answer)
	}

	// 1. Quorum Check
	serverCount, err := e.clusterState.GetServerCount()
	if err != nil {
		log.Printf("[%s] Failed to get server count: %v", e.nodeConfig.ID, err)
		return
	}
	if serverCount == 0 {
		return
	}
	quorum := serverCount/2 + 1

	if len(answers) < quorum {
		log.Printf("[%s] Not enough answers for task %s (Has: %d, Need: %d)", e.nodeConfig.ID, task.ID, len(answers), quorum)
		return
	}

	// 2. Aggregate Answers using LLM
	log.Printf("[%s] Aggregating %d answers for task %s", e.nodeConfig.ID, len(answers), task.ID)

	// Stop Proposal Timer as we are moving to next phase
	e.stopTimer(task.ID)
	// Start Vote Timer
	e.startTimer(task.ID, e.VoteTimeout, "Voting phase exceeded")

	var answerContents []string
	for _, ans := range answers {
		answerContents = append(answerContents, ans.Content)
	}

	proposalContent, err := e.llm.Aggregate(task.Content, answerContents)
	if err != nil {
		log.Printf("[%s] LLM aggregation failed: %v", e.nodeConfig.ID, err)
		return
	}

	// Update Task to Proposal status
	task.Status = core.TaskStatusProposal
	task.Result = proposalContent

	taskBytes, _ := json.Marshal(task)
	cmd := raftInternal.LogCommand{
		Type:  raftInternal.CommandTypeUpdateTask,
		Value: taskBytes,
	}
	b, _ := json.Marshal(cmd)

	if err := e.commandSender.Apply(b, 5*time.Second); err != nil {
		log.Printf("[%s] Failed to update task to proposal: %v", e.nodeConfig.ID, err)
		return
	}
}

func (e *Engine) runVotePhase(task *core.Task) {
	log.Printf("[%s] Starting Vote phase for task %s", e.nodeConfig.ID, task.ID)

	// Simulate "Broadcasting" vote request
	// In reality each node receives the proposal, validates it, and votes.

	// Use LLM to validate the proposal
	isValid, err := e.llm.Validate(task.Content, task.Result)
	if err != nil {
		log.Printf("[%s] LLM validation failed: %v", e.nodeConfig.ID, err)
		// Decide default behavior on error. For now, assume reject if we can't validate.
		isValid = false
	}

	vote := core.Vote{
		TaskID:   task.ID,
		AgentID:  string(e.nodeConfig.ID),
		Accepted: isValid,
	}

	voteBytes, _ := json.Marshal(vote)
	cmd := raftInternal.LogCommand{
		Type:  raftInternal.CommandTypeSubmitVote,
		Value: voteBytes,
	}
	b, _ := json.Marshal(cmd)

	if err := e.commandSender.Apply(b, 5*time.Second); err != nil {
		log.Printf("[%s] Failed to submit vote: %v", e.nodeConfig.ID, err)
		return
	}
}

func (e *Engine) finalizeTask(task *core.Task) {
	// Guard: Check if task is already final (Done or Failed)
	// We re-check FSM status because task variable might be old
	if val, ok := e.fsm.Tasks.Load(task.ID); ok {
		currentTask := val.(*core.Task)
		if currentTask.Status == core.TaskStatusDone || currentTask.Status == core.TaskStatusFailed {
			return
		}
	}

	var votes []core.Vote
	if val, ok := e.fsm.TaskVotes.Load(task.ID); ok {
		votes = val.([]core.Vote)
	}

	// Calculate Quorum
	serverCount, err := e.clusterState.GetServerCount()
	if err != nil {
		log.Printf("[%s] Failed to get raft configuration: %v", e.nodeConfig.ID, err)
		return
	}
	if serverCount == 0 {
		return
	}

	// Quorum is majority
	quorum := serverCount/2 + 1

	accepted := 0
	rejected := 0
	for _, v := range votes {
		if v.Accepted {
			accepted++
		} else {
			rejected++
		}
	}

	var finalStatus core.TaskStatus

	if accepted >= quorum {
		log.Printf("[%s] Consensus reached for task %s (Votes: %d/%d). Accepted.", e.nodeConfig.ID, task.ID, accepted, serverCount)
		finalStatus = core.TaskStatusDone
		// TODO: Increment leader reputation
	} else if rejected >= quorum {
		log.Printf("[%s] Consensus reached for task %s (Votes: %d/%d). REJECTED.", e.nodeConfig.ID, task.ID, rejected, serverCount)
		finalStatus = core.TaskStatusFailed
		// TODO: Decrement leader reputation & trigger election
	} else {
		// No consensus yet
		return
	}

	// Create a copy to update
	finalTask := *task
	finalTask.Status = finalStatus

	taskBytes, _ := json.Marshal(finalTask)
	cmd := raftInternal.LogCommand{
		Type:  raftInternal.CommandTypeUpdateTask,
		Value: taskBytes,
	}
	b, _ := json.Marshal(cmd)
	e.commandSender.Apply(b, 5*time.Second)

	// Notify listeners is done via EventTaskUpdated in the event loop
}

func (e *Engine) startTimer(taskID string, duration time.Duration, failMsg string) {
	e.stopTimer(taskID) // Ensure no existing timer

	timer := time.AfterFunc(duration, func() {
		// Log timeout
		log.Printf("[Timeout] Task %s reached timeout: %s", taskID, failMsg)

		// Create failed task update
		failedTask := &core.Task{
			ID:     taskID,
			Status: core.TaskStatusFailed,
			Result: fmt.Sprintf("Timeout: %s", failMsg),
		}

		propBytes, _ := json.Marshal(failedTask)
		cmd := raftInternal.LogCommand{
			Type:  raftInternal.CommandTypeUpdateTask,
			Value: propBytes,
		}
		b, _ := json.Marshal(cmd)

		// Best effort application
		if err := e.commandSender.Apply(b, 5*time.Second); err != nil {
			log.Printf("Failed to apply timeout failure for task %s: %v", taskID, err)
		}

		e.timers.Delete(taskID)
	})

	e.timers.Store(taskID, timer)
}

func (e *Engine) stopTimer(taskID string) {
	if val, ok := e.timers.Load(taskID); ok {
		timer := val.(*time.Timer)
		timer.Stop()
		e.timers.Delete(taskID)
	}
}

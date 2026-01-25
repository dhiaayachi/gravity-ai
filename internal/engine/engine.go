package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	"github.com/dhiaayachi/gravity-ai/internal/llm"
	raftInternal "github.com/dhiaayachi/gravity-ai/internal/raft"
	"github.com/dhiaayachi/gravity-ai/internal/raft/fsm"
	"github.com/hashicorp/raft"
	"go.uber.org/zap"
)

type TaskNotifier interface {
	NotifyTaskCompletion(task *core.Task)
}

type Engine struct {
	Node *raftInternal.AgentNode // Public for E2E tests

	// Decoupled dependencies
	fsm        fsm.FSM
	llm        llm.Client
	nodeConfig *raftInternal.Config // For ID

	clusterClient ClusterClient
	clusterState  ClusterState

	// listeners for task completion (TaskID -> chan *core.Task)
	taskNotifier TaskNotifier

	// timers for leader phases (TaskID -> *time.Timer)
	timers sync.Map

	// timerCh receives taskIDs for processing timeouts
	timerCh chan string

	ProposalTimeout time.Duration
	VoteTimeout     time.Duration

	logger *zap.Logger
}

// ClusterClient defines the interface for communicating with other agents
type ClusterClient interface {
	SubmitVote(ctx context.Context, leaderAddr string, taskID, agentID string, accepted bool) error
	SubmitAnswer(ctx context.Context, leaderAddr string, taskID, agentID string, content string) error
}

// ClusterState defines the interface for querying cluster status
type ClusterState interface {
	IsLeader() bool
	GetLeaderAddr() string
	GetFormattedID() string // For logging
	GetServerCount() (int, error)
}

type defaultClusterState struct {
	Node *raftInternal.AgentNode
}

func (d *defaultClusterState) IsLeader() bool {
	return d.Node.Raft.State() == raft.Leader
}

func (d *defaultClusterState) GetLeaderAddr() string {
	addr, _ := d.Node.Raft.LeaderWithID()
	return string(addr)
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

func NewEngine(node *raftInternal.AgentNode, llm llm.Client, clusterClient ClusterClient, notifier TaskNotifier, logger *zap.Logger) *Engine {
	return &Engine{
		Node:            node,
		fsm:             node.FSM,
		llm:             llm,
		nodeConfig:      node.Config,
		clusterState:    &defaultClusterState{Node: node},
		timerCh:         make(chan string, 100),
		ProposalTimeout: 60 * time.Second,
		VoteTimeout:     60 * time.Second,
		clusterClient:   clusterClient,
		taskNotifier:    notifier,
		logger:          logger.With(zap.String("component", "engine")),
	}
}

func (e *Engine) Start() {
	e.logger.Info("Starting Engine")
	go func() {
		for {
			select {
			case event := <-e.fsm.EventsConsumer():
				e.handleRaftEvent(event)
			case taskID := <-e.timerCh:
				e.handleTaskTimeout(taskID)
			}
		}
	}()
}

func (e *Engine) handleRaftEvent(event fsm.Event) {
	e.logger.Debug("Handling Raft Event", zap.String("type", string(event.Type)))
	switch event.Type {
	case fsm.EventTaskAdmitted:
		task := event.Data.(*core.Task)
		e.handleTaskAdmitted(task)
	case fsm.EventAnswerSubmitted:
		// If leader, check if we can move to Proposal
		if e.clusterState.IsLeader() {
			ans := event.Data.(*core.Answer)
			// Get task for validation
			task, err := e.fsm.GetTask(ans.TaskID)
			if err != nil {
				break
			}
			e.runProposalPhase(task, false)
		}
	case fsm.EventTaskUpdated:
		task := event.Data.(*core.Task)
		switch task.Status {
		case core.TaskStatusDone, core.TaskStatusFailed:
			e.notifyTaskCompletion(task)
		case core.TaskStatusProposal:
			// All nodes vote
			e.runVotePhase(task)
		}
	case fsm.EventVoteSubmitted:
		if e.clusterState.IsLeader() {
			vote := event.Data.(*core.Vote)
			task, err := e.fsm.GetTask(vote.TaskID)
			if err != nil {
				break
			}
			e.runFinalizeTask(task)
		}
	}
}

func (e *Engine) handleTaskAdmitted(task *core.Task) {
	e.logger.Info("Received task admission. Starting local brainstorm.", zap.String("task_id", task.ID))

	// Start Proposal Timer (waiting for answers)
	if e.clusterState.IsLeader() {
		e.startTimer(task.ID, e.ProposalTimeout)
	}

	// Every node participates in brainstorm as a worker agent
	e.runBrainstormPhase(task)
}

func (e *Engine) handleTaskTimeout(taskID string) {
	e.logger.Warn("Timeout triggered", zap.String("task_id", taskID))
	e.timers.Delete(taskID)

	// Check task status
	task, err := e.fsm.GetTask(taskID)
	if err != nil {
		// Task not found
		e.logger.Warn("Failed to get task", zap.String("task_id", taskID), zap.Error(err))
		return
	}

	if task.Status == core.TaskStatusAdmitted {
		// Proposal Timeout
		if e.clusterState.IsLeader() {

			// Check if we have enough answers to force proposal
			answers, err := e.fsm.GetTaskAnswers(task.ID)
			if err != nil {
				e.logger.Warn("Failed to get task answers", zap.String("task_id", taskID), zap.Error(err))
			}

			serverCount, err := e.clusterState.GetServerCount()
			if err != nil {
				e.logger.Error("Failed to get server count", zap.Error(err), zap.String("task_id", task.ID))
				return
			}
			quorum := serverCount/2 + 1

			if len(answers) >= quorum {
				e.runProposalPhase(task, true)
				return // Successfully triggered or tried
			}

			// If not enough answers, fall through to failure logic
		}
	}

	// Fallthrough for failure (Voting timeout or Brainstorming timeout with < Quorum)
	// Default failure behavior for other states (e.g. Voting)
	failMsg := "Phase timeout exceeded"
	e.logger.Error("Task reached timeout, failing", zap.String("task_id", taskID), zap.String("reason", failMsg))

	// Create failed task update
	failedTask := &core.Task{
		ID:     taskID,
		Status: core.TaskStatusFailed,
		Result: fmt.Sprintf("Timeout: %s", failMsg),
	}

	propBytes, _ := json.Marshal(failedTask)
	cmd := fsm.LogCommand{
		Type:  fsm.CommandTypeUpdateTask,
		Value: propBytes,
	}
	b, _ := json.Marshal(cmd)

	if f := e.Node.Raft.Apply(b, 5*time.Second); f.Error() != nil {
		e.logger.Error("Failed to apply timeout failure", zap.Error(f.Error()), zap.String("task_id", taskID))
	}
}

func (e *Engine) notifyTaskCompletion(task *core.Task) {
	e.stopTimer(task.ID) // Cleanup timer if any
	e.taskNotifier.NotifyTaskCompletion(task)
}

func (e *Engine) runBrainstormPhase(task *core.Task) {
	e.logger.Debug("Contributing to Brainstorm", zap.String("task_id", task.ID))

	// Simulate "Broadcasting" logic by just getting a local answer for now
	ansContent, err := e.llm.Generate(task.Content)
	if err != nil {
		e.logger.Error("LLM generation failed", zap.Error(err), zap.String("task_id", task.ID))
		return
	}

	// Get leader address
	leaderAddr := e.clusterState.GetLeaderAddr()
	if leaderAddr == "" {
		e.logger.Warn("Cannot submit answer: No leader known")
		return
	}

	e.logger.Info("Submitting answer to leader", zap.String("task_id", task.ID), zap.String("leader_addr", leaderAddr), zap.String("answer", ansContent))
	// Submit answer to leader
	err = e.clusterClient.SubmitAnswer(context.Background(), leaderAddr, task.ID, e.nodeConfig.ID, ansContent)
	if err != nil {
		e.logger.Error("Failed to apply answer", zap.Error(err), zap.String("task_id", task.ID))
		return
	}
}

func (e *Engine) runProposalPhase(task *core.Task, force bool) {
	// Guard: Only run proposal phase if task is in Admitted state.
	// This prevents redundant aggregation when multiple answers arrive after quorum is met.
	if task.Status != core.TaskStatusAdmitted {
		return
	}

	// Retrieve answers (Reading from SyncMapFSM state locally since we are leader)
	answers, err := e.fsm.GetTaskAnswers(task.ID)
	if err != nil {
		// Task not found
		e.logger.Error("Failed to get answers", zap.Error(err), zap.String("task_id", task.ID))
	}

	// 1. Check Participation
	serverCount, err := e.clusterState.GetServerCount()
	if err != nil {
		e.logger.Error("Failed to get server count", zap.Error(err), zap.String("task_id", task.ID))
		return
	}
	if serverCount == 0 {
		return
	}
	quorum := serverCount/2 + 1

	// Logic: Proceed if All Answered OR (Quorum Answered AND Force/Timeout)
	if len(answers) == serverCount {
		// Proceed immediately
	} else if len(answers) >= quorum && force {
		// Proceed on timeout with partial results
	} else {
		// Wait
		return
	}

	e.logger.Info("Starting Proposal phase", zap.String("task_id", task.ID))

	// 2. Aggregate Answers using LLM
	e.logger.Debug("Aggregating answers", zap.Int("count", len(answers)), zap.String("task_id", task.ID))

	// Stop Proposal Timer as we are moving to next phase
	e.stopTimer(task.ID)
	// Start Vote Timer
	e.startTimer(task.ID, e.VoteTimeout)

	var answerContents []string
	for _, ans := range answers {
		answerContents = append(answerContents, ans.Content)
	}

	proposalContent, err := e.llm.Aggregate(task.Content, answerContents)
	if err != nil {
		e.logger.Error("LLM aggregation failed", zap.Error(err), zap.String("task_id", task.ID))
		return
	}

	// Update Task to Proposal status
	task.Status = core.TaskStatusProposal
	task.Result = proposalContent

	taskBytes, _ := json.Marshal(task)
	cmd := fsm.LogCommand{
		Type:  fsm.CommandTypeUpdateTask,
		Value: taskBytes,
	}
	b, _ := json.Marshal(cmd)

	if f := e.Node.Raft.Apply(b, 5*time.Second); f.Error() != nil {
		e.logger.Error("Failed to update task to proposal", zap.Error(f.Error()), zap.String("task_id", task.ID))
		return
	}
}

func (e *Engine) runVotePhase(task *core.Task) {

	// Use LLM to validate the proposal
	isValid, err := e.llm.Validate(task.Content, task.Result)
	log.Printf("[%s] Validating and casting vote for task %s voted: %v", e.nodeConfig.ID, task.ID, isValid)
	if err != nil {
		log.Printf("[%s] LLM validation failed: %v", e.nodeConfig.ID, err)
		// Decide default behavior on error. For now, assume reject if we can't validate.
		isValid = false
	}

	// 1. If Leader, apply directly
	if e.clusterState.IsLeader() {
		vote := core.Vote{
			TaskID:   task.ID,
			AgentID:  e.nodeConfig.ID,
			Accepted: isValid,
		}

		voteBytes, _ := json.Marshal(vote)
		cmd := fsm.LogCommand{
			Type:  fsm.CommandTypeSubmitVote,
			Value: voteBytes,
		}
		b, _ := json.Marshal(cmd)

		if f := e.Node.Raft.Apply(b, 5*time.Second); f.Error() != nil {
			log.Printf("[%s] Failed to submit vote (leader): %v", e.nodeConfig.ID, f.Error())
		}
		return
	}

	// 2. If Follower, submit to leader via gRPC
	// We need the leader address.
	// We can't easily get the leader TCP address from Raft object directly if it's not exposed in ClusterState?
	// ClusterState interface needs to expose Leader Address?
	// defaultClusterState uses e.Node.Raft.Leader().
	// But ClusterState only has IsLeader().
	// We can add GetLeaderAddr to ClusterState. Or cast internal node.

	// Let's rely on e.Node.Raft for now via a cast or update ClusterState interface.
	// Updating ClusterState interface is cleaner.

	// BUT, wait. e.Node is public in Engine struct. We can access it directly?
	// Yes, `e.Node.Raft.Leader()`.

	leaderAddr := e.clusterState.GetLeaderAddr()
	if leaderAddr == "" {
		log.Printf("[%s] Cannot vote: No leader known", e.nodeConfig.ID)
		return
	}

	if e.clusterClient == nil {
		log.Printf("[%s] Cannot vote: ClusterClient not initialized", e.nodeConfig.ID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := e.clusterClient.SubmitVote(ctx, leaderAddr, task.ID, e.nodeConfig.ID, isValid); err != nil {
		log.Printf("[%s] Failed to submit vote to leader: %v", e.nodeConfig.ID, err)
	}
}

func (e *Engine) runFinalizeTask(task *core.Task) {
	// Guard: Check if task is already final (Done or Failed)
	// We re-check SyncMapFSM status because task variable might be old
	currentTask, err := e.fsm.GetTask(task.ID)
	if err != nil {
		e.logger.Error("Failed to get task", zap.Error(err), zap.String("task_id", task.ID))
		return
	}
	if currentTask.Status == core.TaskStatusDone || currentTask.Status == core.TaskStatusFailed {
		return
	}

	votes, err := e.fsm.GetTaskVotes(task.ID)
	if err != nil {
		e.logger.Error("Failed to get task votes", zap.Error(err), zap.String("task_id", task.ID))
		return
	}

	// Calculate Quorum
	serverCount, err := e.clusterState.GetServerCount()
	if err != nil {
		e.logger.Error("Failed to get raft configuration", zap.Error(err))
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
		e.logger.Info("Consensus reached: Accepted", zap.String("task_id", task.ID), zap.String("result", task.Result))
		finalStatus = core.TaskStatusDone
		// TODO: Increment leader reputation
	} else if rejected >= quorum {
		e.logger.Info("Consensus reached: REJECTED", zap.String("task_id", task.ID), zap.String("result", task.Result))
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
	cmd := fsm.LogCommand{
		Type:  fsm.CommandTypeUpdateTask,
		Value: taskBytes,
	}
	b, _ := json.Marshal(cmd)
	if f := e.Node.Raft.Apply(b, 5*time.Second); f.Error() != nil {
		// Just log error
		e.logger.Error("Failed to apply update task", zap.Error(f.Error()))
	}

	// Notify listeners is done via EventTaskUpdated in the event loop
}

func (e *Engine) startTimer(taskID string, duration time.Duration) {
	e.stopTimer(taskID) // Ensure no existing timer

	timer := time.AfterFunc(duration, func() {
		e.timers.Delete(taskID)
		e.timerCh <- taskID
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

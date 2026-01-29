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
	DropLeader() error
	TransferLeadership(id string) error
}

type defaultClusterState struct {
	Node *raftInternal.AgentNode
}

func (d *defaultClusterState) IsLeader() bool {
	return d.Node.Raft.State() == raft.Leader
}

func (d *defaultClusterState) DropLeader() error {
	f := d.Node.Raft.LeadershipTransfer()
	return f.Error()
}

func (d *defaultClusterState) TransferLeadership(id string) error {
	address, err := d.getPeerAddress(id)
	if err != nil {
		return err
	}
	f := d.Node.Raft.LeadershipTransferToServer(raft.ServerID(id), raft.ServerAddress(address))
	return f.Error()
}

func (d *defaultClusterState) getPeerAddress(id string) (string, error) {
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
	e.logger.Info("Handling Raft Event", zap.String("type", string(event.Type)))
	var err error
	defer func() {
		if err != nil {
			e.logger.Error("Error handling Raft Event", zap.Error(err))
			if e.clusterState.IsLeader() {
				err = e.clusterState.DropLeader()
				if err != nil {
					e.logger.Error("Error dropping leadership", zap.Error(err))
				}
			}
		}
	}()
	switch event.Type {
	case fsm.EventTaskAdmitted:
		task := event.Data.(*core.Task)
		e.handleTaskAdmitted(task)
	case fsm.EventAnswerSubmitted:
		// If leader, check if we can move to Proposal
		if e.clusterState.IsLeader() {
			ans := event.Data.(*core.Answer)
			// Get task for validation
			var task *core.Task
			task, err = e.fsm.GetTask(ans.TaskID)
			if err != nil {
				e.logger.Warn("Failed to get task", zap.String("taskID", ans.TaskID), zap.Error(err))
				break
			}
			err = e.runProposalPhase(task, false)
		}
	case fsm.EventTaskUpdated:
		task := event.Data.(*core.Task)
		switch task.Status {
		case core.TaskStatusDone, core.TaskStatusFailed:
			e.notifyTaskCompletion(task)
		case core.TaskStatusProposal:
			// All nodes vote
			err = e.runVotePhase(task)
		}
	case fsm.EventVoteSubmitted:
		if e.clusterState.IsLeader() {
			vote := event.Data.(*core.Vote)
			task, err := e.fsm.GetTask(vote.TaskID)
			if err != nil {
				e.logger.Warn("Failed to get vote", zap.String("task", vote.TaskID), zap.Error(err))
				break
			}
			err = e.runFinalizeTask(task)
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
				err := e.runProposalPhase(task, true)
				if err != nil {
					return
				}
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

func (e *Engine) runBrainstormPhase(task *core.Task) error {
	e.logger.Debug("Contributing to Brainstorm", zap.String("task_id", task.ID))

	// Simulate "Broadcasting" logic by just getting a local answer for now
	ansContent, err := e.llm.Generate(task.Content)
	if err != nil {
		e.logger.Error("LLM generation failed", zap.Error(err), zap.String("task_id", task.ID))
		return fmt.Errorf("LLM generation failed: %w", err)
	}

	// Get leader address
	leaderAddr := e.clusterState.GetLeaderAddr()
	if leaderAddr == "" {
		e.logger.Warn("Cannot submit answer: No leader known")
		return fmt.Errorf("Cannot submit answer: No leader known")
	}

	e.logger.Info("Submitting answer to leader", zap.String("task_id", task.ID), zap.String("leader_addr", leaderAddr), zap.String("answer", ansContent))
	// Submit answer to leader
	err = e.clusterClient.SubmitAnswer(context.Background(), leaderAddr, task.ID, e.nodeConfig.ID, ansContent)
	if err != nil {
		e.logger.Error("Failed to apply answer", zap.Error(err), zap.String("task_id", task.ID))
		return fmt.Errorf("Failed to apply answer: %w", err)
	}
	return nil
}

func (e *Engine) runProposalPhase(task *core.Task, force bool) error {
	// Guard: Only run proposal phase if task is in Admitted state.
	// This prevents redundant aggregation when multiple answers arrive after quorum is met.
	if task.Status != core.TaskStatusAdmitted {
		return fmt.Errorf("task status is %s", task.Status)
	}

	// Retrieve answers (Reading from SyncMapFSM state locally since we are leader)
	answers, err := e.fsm.GetTaskAnswers(task.ID)
	if err != nil {
		// Task not found
		e.logger.Error("Failed to get answers", zap.Error(err), zap.String("task_id", task.ID))
		return fmt.Errorf("failed to get answers: %w", err)
	}

	// 1. Check Participation
	serverCount, err := e.clusterState.GetServerCount()
	if err != nil {
		e.logger.Error("Failed to get server count", zap.Error(err), zap.String("task_id", task.ID))
		return fmt.Errorf("failed to get server count: %w", err)
	}
	if serverCount == 0 {
		return fmt.Errorf("no server found for task %s", task.ID)
	}
	quorum := serverCount/2 + 1

	// Logic: Proceed if All Answered OR (Quorum Answered AND Force/Timeout)
	if len(answers) == serverCount {
		// Proceed immediately
	} else if len(answers) >= quorum && force {
		// Proceed on timeout with partial results
	} else {
		// Wait
		return nil
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
		return fmt.Errorf("LLM aggregation failed: %w", err)
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
		return fmt.Errorf("failed to update task to proposal: %w", f.Error())
	}
	return nil
}

func (e *Engine) runVotePhase(task *core.Task) error {

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
		return nil
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
		return fmt.Errorf("cannot vote: No leader known")
	}

	if e.clusterClient == nil {
		log.Printf("[%s] Cannot vote: ClusterClient not initialized", e.nodeConfig.ID)
		return fmt.Errorf("cannot vote: ClusterClient not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := e.clusterClient.SubmitVote(ctx, leaderAddr, task.ID, e.nodeConfig.ID, isValid); err != nil {
		log.Printf("[%s] Failed to submit vote to leader: %v", e.nodeConfig.ID, err)
	}
	return nil
}

func (e *Engine) runFinalizeTask(task *core.Task) error {
	// Guard: Check if task is already final (Done or Failed)
	// We re-check SyncMapFSM status because task variable might be old
	currentTask, err := e.fsm.GetTask(task.ID)
	if err != nil {
		e.logger.Error("Failed to get task", zap.Error(err), zap.String("task_id", task.ID))
		return fmt.Errorf("failed to get task: %w", err)
	}
	if currentTask.Status == core.TaskStatusDone || currentTask.Status == core.TaskStatusFailed {
		e.logger.Info("Task already finished", zap.String("task_id", task.ID))
		return fmt.Errorf("task already finished")
	}

	votes, err := e.fsm.GetTaskVotes(task.ID)
	if err != nil {
		e.logger.Error("Failed to get task votes", zap.Error(err), zap.String("task_id", task.ID))
		return fmt.Errorf("failed to get task votes: %w", err)
	}

	// Calculate Quorum
	serverCount, err := e.clusterState.GetServerCount()
	if err != nil {
		e.logger.Error("Failed to get raft configuration", zap.Error(err))
		return fmt.Errorf("failed to get raft configuration: %w", err)
	}
	if serverCount == 0 {
		e.logger.Info("No raft configuration found", zap.String("task_id", task.ID))
		return fmt.Errorf("no raft configuration found")
	}

	// Quorum is majority
	quorum := serverCount/2 + 1

	accepted := 0
	rejected := 0

	// Track who voted what for reputation calculation
	voters := make(map[string]bool)

	for _, v := range votes {
		voters[v.AgentID] = v.Accepted
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
	} else if rejected >= quorum {
		e.logger.Info("Consensus reached: REJECTED", zap.String("task_id", task.ID), zap.String("result", task.Result))
		finalStatus = core.TaskStatusFailed
	} else {
		e.logger.Info("No consensus reached, yet", zap.String("task_id", task.ID), zap.String("result", task.Result), zap.Int("quorum", quorum), zap.Int("rejected", rejected), zap.Int("accepted", accepted))
		// No consensus yet
		return nil
	}

	// 1. Calculate Confidence Score
	// Confidence = Sum(Reputations of Agreeing Agents) / Sum(Reputations of All Voting Agents)
	var totalRep float64
	var agreeingRep float64

	for voterID, votedAccepted := range voters {
		rep := float64(e.fsm.GetReputation(voterID))
		totalRep += rep

		agreed := (finalStatus == core.TaskStatusDone && votedAccepted) || (finalStatus == core.TaskStatusFailed && !votedAccepted)
		if agreed {
			agreeingRep += rep
		}
	}

	// Add Leader's reputation to correct bucket (Leader implicitly agrees with outcome logic mostly, but let's stick to voters map if leader is in it?)
	// Wait, leader usually votes too? Yes, leader votes in runVotePhase "1. If Leader, apply directly".
	// The `voters` map comes from `votes` which includes leader's vote.
	// So `voters` loop covers everyone who voted.

	var confidenceScore float64
	if totalRep > 0 {
		confidenceScore = agreeingRep / totalRep
	}

	// 2. Update Task
	// Create a copy to update
	finalTask := *task
	finalTask.Status = finalStatus
	finalTask.ConfidenceScore = confidenceScore

	e.logger.Info("Calculated Confidence Score",
		zap.String("task_id", task.ID),
		zap.Float64("score", confidenceScore),
		zap.Float64("agreeing_rep", agreeingRep),
		zap.Float64("total_rep", totalRep))

	taskBytes, _ := json.Marshal(finalTask)
	cmd := fsm.LogCommand{
		Type:  fsm.CommandTypeUpdateTask,
		Value: taskBytes,
	}
	b, _ := json.Marshal(cmd)
	if f := e.Node.Raft.Apply(b, 5*time.Second); f.Error() != nil {
		e.logger.Error("Failed to apply update task", zap.Error(f.Error()))
		return fmt.Errorf("failed to apply update task: %w", f.Error()) // Stop if task update fails
	}

	// 2. Update Reputations
	// Leader Reward/Penalty
	myID := e.nodeConfig.ID
	currentMyRep := e.fsm.GetReputation(myID)
	newMyRep := currentMyRep
	if finalStatus == core.TaskStatusDone {
		newMyRep += 10
	} else {
		newMyRep -= 10
	}
	e.applyReputationUpdate(myID, newMyRep)

	// Voters Reward/Penalty
	for voterID, votedAccepted := range voters {
		if voterID == myID {
			continue // Already handled leader if they voted (which they should have)
		}

		rep := e.fsm.GetReputation(voterID)
		// Reward if agreed with outcome
		// Consenus Accepted (Done) AND Voted Accepted -> Agree
		// Consensus Rejected (Failed) AND Voted Rejected -> Agree
		agreed := (finalStatus == core.TaskStatusDone && votedAccepted) || (finalStatus == core.TaskStatusFailed && !votedAccepted)

		if agreed {
			rep += 1
		} else {
			rep -= 1
		}
		e.applyReputationUpdate(voterID, rep)
	}

	// 3. Check for Leadership Transfer
	e.checkForLeadershipTransfer()

	return nil
	// Notify listeners is done via EventTaskUpdated in the event loop
}

func (e *Engine) applyReputationUpdate(agentID string, newRep int) {
	newRep = max(newRep, 0)
	newRep = min(newRep, 100)
	repBytes, _ := json.Marshal(newRep)
	cmd := fsm.LogCommand{
		Type:    fsm.CommandTypeUpdateReputation,
		AgentID: agentID,
		Value:   repBytes,
	}
	b, _ := json.Marshal(cmd)
	// Fire and forget for individual updates to avoid stalling?
	// Or blocking? Better blocking to ensure consistency before transfer check.
	if f := e.Node.Raft.Apply(b, 2*time.Second); f.Error() != nil {
		e.logger.Error("Failed to apply reputation update", zap.String("agent_id", agentID), zap.Error(f.Error()))
	}
}

func (e *Engine) checkForLeadershipTransfer() {
	reps := e.fsm.GetAllReputations()
	myID := e.nodeConfig.ID
	myRep := e.fsm.GetReputation(myID)

	var bestNode string = myID
	maxRep := myRep // Start with self

	for id, rep := range reps {
		if rep > maxRep {
			maxRep = rep
			bestNode = id
		}
	}

	if bestNode != myID {
		e.logger.Info("Found node with higher reputation. Attempting leadership transfer.",
			zap.String("current_leader", myID),
			zap.Int("current_rep", myRep),
			zap.String("target_node", bestNode),
			zap.Int("target_rep", maxRep))
		if err := e.clusterState.TransferLeadership(bestNode); err != nil {
			e.logger.Error("Failed to transfer leadership", zap.Error(err))
		} else {
			e.logger.Info("Leadership transfer initiated", zap.String("target", bestNode))
		}
	}
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

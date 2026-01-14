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
	Node   *raftInternal.AgentNode
	health *health.Monitor
	llm    llm.Client

	// commandSender handles submitting logical commands (Vote/Answer) to Raft
	commandSender CommandSender

	// listeners for task completion (TaskID -> chan *core.Task)
	listeners sync.Map
}

// CommandSender defines the interface for submitting Raft commands
type CommandSender interface {
	Apply(cmd []byte, timeout time.Duration) error
}

type defaultCommandSender struct {
	Raft *raft.Raft
}

func (d *defaultCommandSender) Apply(cmd []byte, timeout time.Duration) error {
	log.Printf("[%s] Leader applying command", d.Raft.String())
	return d.Raft.Apply(cmd, timeout).Error()
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
		Node:          node,
		health:        health,
		llm:           llm,
		commandSender: &defaultCommandSender{Raft: node.Raft},
	}
}

// SetCommandSender sets the command sender (for testing/mocking)
func (e *Engine) SetCommandSender(sender CommandSender) {
	e.commandSender = sender
}

// SubmitTask handles a new task submission and returns a future, called from the leader
func (e *Engine) SubmitTask(content string, requester string) (*TaskFuture, error) {
	if e.Node.Raft.State() != raft.Leader {
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

func (e *Engine) Start() {
	go func() {
		for event := range e.Node.FSM.EventCh {
			switch event.Type {
			case raftInternal.EventTaskAdmitted:
				task := event.Data.(*core.Task)
				e.handleTaskAdmitted(task)
			case raftInternal.EventAnswerSubmitted:
				// If leader, check if we can move to Proposal
				if e.Node.Raft.State() == raft.Leader {
					ans := event.Data.(*core.Answer)
					// Get task for validation
					if val, ok := e.Node.FSM.Tasks.Load(ans.TaskID); ok {
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
				if e.Node.Raft.State() == raft.Leader {
					vote := event.Data.(*core.Vote)
					if val, ok := e.Node.FSM.Tasks.Load(vote.TaskID); ok {
						task := val.(*core.Task)
						e.finalizeTask(task)
					}
				}
			}
		}
	}()
}

func (e *Engine) notifyTaskCompletion(task *core.Task) {
	if val, ok := e.listeners.Load(task.ID); ok {
		ch := val.(chan *core.Task)
		ch <- task
		close(ch)
		e.listeners.Delete(task.ID)
	}
}

func (e *Engine) handleTaskAdmitted(task *core.Task) {
	log.Printf("[%s] Received task admission: %s. Starting local brainstorm.", e.Node.Config.ID, task.ID)
	// Every node participates in brainstorm
	e.runBrainstormPhase(task)
}

func (e *Engine) runBrainstormPhase(task *core.Task) {
	log.Printf("[%s] Starting Brainstorm phase for task %s", e.Node.Config.ID, task.ID)

	// Simulate "Broadcasting" logic by just getting a local answer for now
	ansContent, err := e.llm.Generate(task.Content)
	if err != nil {
		log.Printf("[%s] LLM generation failed: %v", e.Node.Config.ID, err)
		return
	}

	answer := core.Answer{
		TaskID:  task.ID,
		AgentID: string(e.Node.Config.ID),
		Content: ansContent,
	}

	ansBytes, _ := json.Marshal(answer)
	cmd := raftInternal.LogCommand{
		Type:  raftInternal.CommandTypeSubmitAnswer,
		Value: ansBytes,
	}
	b, _ := json.Marshal(cmd)

	if err := e.commandSender.Apply(b, 5*time.Second); err != nil {
		log.Printf("[%s] Failed to apply answer: %v", e.Node.Config.ID, err)
		return
	}
}

func (e *Engine) runProposalPhase(task *core.Task) {
	log.Printf("[%s] Starting Proposal phase for task %s", e.Node.Config.ID, task.ID)

	// Retrieve answers (Reading from FSM state locally since we are leader)
	var answers []core.Answer
	if val, ok := e.Node.FSM.TaskAnswers.Load(task.ID); ok {
		answers = val.([]core.Answer)
	}

	if len(answers) == 0 {
		log.Printf("[%s] No answers received for task %s", e.Node.Config.ID, task.ID)
		return
	}

	// "Aggregate" answers - for now just pick the first one
	proposalContent := answers[0].Content

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
		log.Printf("[%s] Failed to update task to proposal: %v", e.Node.Config.ID, err)
		return
	}
}

func (e *Engine) runVotePhase(task *core.Task) {
	log.Printf("[%s] Starting Vote phase for task %s", e.Node.Config.ID, task.ID)

	// Simulate "Broadcasting" vote request
	// In reality each node receives the proposal, validates it, and votes.

	// Use LLM to validate the proposal
	isValid, err := e.llm.Validate(task.Content, task.Result)
	if err != nil {
		log.Printf("[%s] LLM validation failed: %v", e.Node.Config.ID, err)
		// Decide default behavior on error. For now, assume reject if we can't validate.
		isValid = false
	}

	vote := core.Vote{
		TaskID:   task.ID,
		AgentID:  string(e.Node.Config.ID),
		Accepted: isValid,
	}

	voteBytes, _ := json.Marshal(vote)
	cmd := raftInternal.LogCommand{
		Type:  raftInternal.CommandTypeSubmitVote,
		Value: voteBytes,
	}
	b, _ := json.Marshal(cmd)

	if err := e.commandSender.Apply(b, 5*time.Second); err != nil {
		log.Printf("[%s] Failed to submit vote: %v", e.Node.Config.ID, err)
		return
	}
}

func (e *Engine) finalizeTask(task *core.Task) {
	// Guard: Check if task is already final (Done or Failed)
	// We re-check FSM status because task variable might be old
	if val, ok := e.Node.FSM.Tasks.Load(task.ID); ok {
		currentTask := val.(*core.Task)
		if currentTask.Status == core.TaskStatusDone || currentTask.Status == core.TaskStatusFailed {
			return
		}
	}

	var votes []core.Vote
	if val, ok := e.Node.FSM.TaskVotes.Load(task.ID); ok {
		votes = val.([]core.Vote)
	}

	// Calculate Quorum
	cfg := e.Node.Raft.GetConfiguration()
	if err := cfg.Error(); err != nil {
		log.Printf("[%s] Failed to get raft configuration: %v", e.Node.Config.ID, err)
		return
	}
	serverCount := len(cfg.Configuration().Servers)
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
		log.Printf("[%s] Consensus reached for task %s (Votes: %d/%d). Accepted.", e.Node.Config.ID, task.ID, accepted, serverCount)
		finalStatus = core.TaskStatusDone
		// TODO: Increment leader reputation
	} else if rejected >= quorum {
		log.Printf("[%s] Consensus reached for task %s (Votes: %d/%d). REJECTED.", e.Node.Config.ID, task.ID, rejected, serverCount)
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

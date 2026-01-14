package engine

import (
	"encoding/json"
	"fmt"
	"log"
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
	// Policy governs the agent's behavior
	Policy Policy

	// CommandSender handles submitting logical commands (Vote/Answer) to Raft
	CommandSender CommandSender
}

type CommandSender interface {
	Apply(cmd []byte, timeout time.Duration) error
}

type DefaultCommandSender struct {
	Raft *raft.Raft
}

func (d *DefaultCommandSender) Apply(cmd []byte, timeout time.Duration) error {
	return d.Raft.Apply(cmd, timeout).Error()
}

type Policy struct {
	VoteLogic func(task *core.Task) bool
}

func NewEngine(node *raftInternal.AgentNode, health *health.Monitor, llm llm.Client) *Engine {
	return &Engine{
		Node:   node,
		health: health,
		llm:    llm,
		Policy: Policy{
			VoteLogic: func(task *core.Task) bool { return true }, // Default auto-accept
		},
		CommandSender: &DefaultCommandSender{Raft: node.Raft},
	}
}

// SubmitTask handles a new task submission
func (e *Engine) SubmitTask(content string, requester string) (*core.Task, error) {
	if e.Node.Raft.State() != raft.Leader {
		return nil, fmt.Errorf("not leader")
	}

	task := &core.Task{
		ID:        uuid.New().String(),
		Content:   content,
		Status:    core.TaskStatusAdmitted,
		Requester: requester,
		CreatedAt: time.Now(),
	}

	taskBytes, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}

	cmd := raftInternal.LogCommand{
		Type:  raftInternal.CommandTypeAdmitTask,
		Value: taskBytes,
	}

	b, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}

	if err := e.CommandSender.Apply(b, 5*time.Second); err != nil {
		return nil, err
	}

	// Trigger Brainstorm phase asynchronously
	// go e.runBrainstormPhase(task) -- Handled via events now

	return task, nil
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
				if task.Status == core.TaskStatusProposal {
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

func (e *Engine) handleTaskAdmitted(task *core.Task) {
	log.Printf("Received task admission: %s. Starting local brainstorm.", task.ID)
	// Every node participates in brainstorm
	e.runBrainstormPhase(task)
}

func (e *Engine) runBrainstormPhase(task *core.Task) {
	log.Printf("Starting Brainstorm phase for task %s", task.ID)

	// Simulate "Broadcasting" logic by just getting a local answer for now
	ansContent, err := e.llm.Generate(task.Content)
	if err != nil {
		log.Printf("LLM generation failed: %v", err)
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

	if err := e.CommandSender.Apply(b, 5*time.Second); err != nil {
		log.Printf("Failed to apply answer: %v", err)
		return
	}

	// Transition to Proposal Phase
	// e.runProposalPhase(task) -- Handled via events now
}

func (e *Engine) runProposalPhase(task *core.Task) {
	log.Printf("Starting Proposal phase for task %s", task.ID)

	// Retrieve answers (Reading from FSM state locally since we are leader)
	var answers []core.Answer
	if val, ok := e.Node.FSM.TaskAnswers.Load(task.ID); ok {
		answers = val.([]core.Answer)
	}

	if len(answers) == 0 {
		log.Printf("No answers received for task %s", task.ID)
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

	if err := e.CommandSender.Apply(b, 5*time.Second); err != nil {
		log.Printf("Failed to update task to proposal: %v", err)
		return
	}

	// Transition to Vote Phase
	// go e.runVotePhase(task) -- Handled via events now
}

func (e *Engine) runVotePhase(task *core.Task) {
	log.Printf("Starting Vote phase for task %s", task.ID)

	// Simulate "Broadcasting" vote request
	// In reality each node receives the proposal, validates it, and votes.

	shouldAccept := e.Policy.VoteLogic(task)

	vote := core.Vote{
		TaskID:   task.ID,
		AgentID:  string(e.Node.Config.ID),
		Accepted: shouldAccept,
	}

	voteBytes, _ := json.Marshal(vote)
	cmd := raftInternal.LogCommand{
		Type:  raftInternal.CommandTypeSubmitVote,
		Value: voteBytes,
	}
	b, _ := json.Marshal(cmd)

	if err := e.CommandSender.Apply(b, 5*time.Second); err != nil {
		log.Printf("Failed to submit vote: %v", err)
		return
	}

	// Check for consensus
	// In a real loop we'd wait for enough votes. Here we just check immediately.
	// e.finalizeTask(task) -- Handled via events now
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
		log.Printf("Failed to get raft configuration: %v", err)
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
		log.Printf("Consensus reached for task %s (Votes: %d/%d). Accepted.", task.ID, accepted, serverCount)
		finalStatus = core.TaskStatusDone
		// TODO: Increment leader reputation
	} else if rejected >= quorum {
		log.Printf("Consensus reached for task %s (Votes: %d/%d). REJECTED.", task.ID, rejected, serverCount)
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
	e.CommandSender.Apply(b, 5*time.Second)
}

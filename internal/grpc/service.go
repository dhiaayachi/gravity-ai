package grpc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	"github.com/dhiaayachi/gravity-ai/internal/engine"
	raftInternal "github.com/dhiaayachi/gravity-ai/internal/raft"
	"github.com/google/uuid"
	"github.com/hashicorp/raft"
)

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

func NewAgentService(raft *raft.Raft, e *engine.Engine) *AgentService {
	return &AgentService{raft: raft, engine: e}
}

type AgentService struct {
	engine *engine.Engine
	raft   *raft.Raft
}

// SubmitTask handles a new task submission and returns a future, called from the leader
func (a *AgentService) SubmitTask(content string, requester string) (*TaskFuture, error) {

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
	a.engine.AddTask(task.ID, ch)

	taskBytes, err := json.Marshal(task)
	if err != nil {
		a.engine.DeleteTask(task)
		return nil, err
	}

	cmd := raftInternal.LogCommand{
		Type:  raftInternal.CommandTypeAdmitTask,
		Value: taskBytes,
	}

	b, err := json.Marshal(cmd)
	if err != nil {
		a.engine.DeleteTask(task)
		return nil, err
	}

	if f := a.raft.Apply(b, 5*time.Second); f.Error() != nil {
		a.engine.DeleteTask(task)
		return nil, f.Error()
	}

	return &TaskFuture{
		TaskID:   taskID,
		resultCh: ch,
	}, nil
}

// SubmitAnswer handles answer submission
func (a *AgentService) SubmitAnswer(taskID, agentID, content string) error {

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

	f := a.raft.Apply(b, 5*time.Second)
	return f.Error()
}

// SubmitVote handles vote submission
func (a *AgentService) SubmitVote(taskID, agentID string, accepted bool) error {

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

	f := a.raft.Apply(b, 5*time.Second)
	return f.Error()
}

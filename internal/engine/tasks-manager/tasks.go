package tasks_manager

import (
	"sync"

	"github.com/dhiaayachi/gravity-ai/internal/core"
)

type TasksManager struct {
	listeners sync.Map
}

func (n *TasksManager) NotifyTaskCompletion(task *core.Task) {
	if val, ok := n.listeners.Load(task.ID); ok {
		ch := val.(chan *core.Task)
		ch <- task
		close(ch)
		n.listeners.Delete(task.ID)
	}
}

func (n *TasksManager) RegisterTask(task *core.Task, ch chan *core.Task) {
	n.listeners.Store(task.ID, ch)
}

func (n *TasksManager) DeregisterTask(task *core.Task) {
	n.listeners.Store(task.ID, task)
}

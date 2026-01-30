package http

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	"github.com/dhiaayachi/gravity-ai/internal/raft/fsm"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	Version = "0.0.1"
)

// OllamaGenerateRequest Ollama API Types
type OllamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"` // Ignored for now, always false
}

type OllamaGenerateResponse struct {
	Model           string  `json:"model"`
	CreatedAt       string  `json:"created_at"`
	Response        string  `json:"response"`
	Done            bool    `json:"done"`
	ConfidenceScore float64 `json:"confidence_score,omitempty"`
}

type OllamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []OllamaMessage `json:"messages"`
	Stream   bool            `json:"stream"` // Ignored for now
}

type OllamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OllamaChatResponse struct {
	Model           string        `json:"model"`
	CreatedAt       string        `json:"created_at"`
	Message         OllamaMessage `json:"message"`
	Done            bool          `json:"done"`
	ConfidenceScore float64       `json:"confidence_score,omitempty"`
}

type OllamaTagsResponse struct {
	Models []OllamaModel `json:"models"`
}

type OllamaModel struct {
	Name       string `json:"name"`
	ModifiedAt string `json:"modified_at"`
	Size       int64  `json:"size"`
	Digest     string `json:"digest"`
}

func (s *Server) handleGenerate(c *gin.Context) {
	var req OllamaGenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Submit task to Gravity Engine
	task := s.submitTask(c, req.Prompt)

	resp := OllamaGenerateResponse{
		Model:           req.Model, // Echo back requested model or actual?
		CreatedAt:       time.Now().Format(time.RFC3339),
		Response:        task.Result, // Use task result as the response
		Done:            true,
		ConfidenceScore: task.ConfidenceScore,
	}

	c.JSON(http.StatusOK, resp)
}

func (s *Server) submitTask(c *gin.Context, prompt string) *core.Task {
	taskID, err := s.agentService.SubmitTask(c.Request.Context(), "http-api", prompt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to submit task: %v", err)})
		return nil
	}

	err, task := s.waitForTask(c.Request.Context(), taskID)
	if err != nil {
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "task timed out or cancelled"})
		return nil
	}

	return task
}

func (s *Server) waitForTask(ctx context.Context, id string) (error, *core.Task) {
	var version uint64 = 0
	for {
		s.logger.Debug("waiting for task", zap.String("id", id), zap.Uint64("last_version", version))
		var err error
		var ev interface{}
		err, ev, version = s.state.WaitForEvent(ctx, fsm.KeyPrefixTask, id, version)
		if err != nil {
			return err, nil
		}
		switch ev.(type) {
		case *core.Task:
			task := ev.(*core.Task)
			if task.Status == core.TaskStatusDone {
				return nil, task
			}
		}
	}
}

func (s *Server) handleChat(c *gin.Context) {
	var req OllamaChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Convert messages to a simple prompt for now
	// Ideally, the internal engine should handle structured messages.
	// We'll just concatenate them.
	prompt := ""
	for _, msg := range req.Messages {
		prompt += fmt.Sprintf("%s: %s\n", msg.Role, msg.Content)
	}

	task := s.submitTask(c, prompt)

	resp := OllamaChatResponse{
		Model:     req.Model,
		CreatedAt: time.Now().Format(time.RFC3339),
		Message: OllamaMessage{
			Role:    "assistant",
			Content: task.Result,
		},
		Done:            true,
		ConfidenceScore: task.ConfidenceScore,
	}

	c.JSON(http.StatusOK, resp)
}

func (s *Server) handleTags(c *gin.Context) {
	// Mock models available
	resp := OllamaTagsResponse{
		Models: []OllamaModel{
			{
				Name:       "gravity:latest",
				ModifiedAt: time.Now().Format(time.RFC3339),
				Size:       0,
				Digest:     "sha256:gravity",
			},
		},
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) handleVersion(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"version": Version})
}

func (s *Server) handleAgentState(c *gin.Context) {
	states := s.state.GetClusterAgentsState()
	c.JSON(http.StatusOK, states)
}

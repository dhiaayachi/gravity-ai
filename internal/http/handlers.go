package http

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
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
	future, err := s.agentService.SubmitTask(req.Prompt, "http-api")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to submit task: %v", err)})
		return
	}

	// Wait for result
	// TODO: Implement long-polling or configurable timeout depending on needs.
	// For now, blocking wait (might timeout client if task takes long)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	task, err := future.Await(ctx)
	if err != nil {
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "task timed out or cancelled"})
		return
	}

	resp := OllamaGenerateResponse{
		Model:           req.Model, // Echo back requested model or actual?
		CreatedAt:       time.Now().Format(time.RFC3339),
		Response:        task.Result, // Use task result as the response
		Done:            true,
		ConfidenceScore: task.ConfidenceScore,
	}

	c.JSON(http.StatusOK, resp)
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

	future, err := s.agentService.SubmitTask(prompt, "http-api")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to submit task: %v", err)})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	task, err := future.Await(ctx)
	if err != nil {
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "task timed out or cancelled"})
		return
	}

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
	states := s.engine.GetClusterAgentsState()
	c.JSON(http.StatusOK, states)
}

package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/dhiaayachi/gravity-ai/internal/grpc"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type Server struct {
	router       *gin.Engine
	agentService *grpc.AgentService
	httpServer   *http.Server
	addr         string
	logger       *zap.Logger
}

func NewServer(addr string, agentService *grpc.AgentService, logger *zap.Logger) *Server {
	// standard gin with logger and recovery
	router := gin.Default()

	s := &Server{
		router:       router,
		agentService: agentService,
		addr:         addr,
		logger:       logger.With(zap.String("component", "http_server")),
	}

	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	api := s.router.Group("/api")
	{
		api.POST("/generate", s.handleGenerate)
		api.POST("/chat", s.handleChat)
		api.GET("/tags", s.handleTags)
		api.GET("/version", s.handleVersion)
	}
}

func (s *Server) Run() error {
	s.httpServer = &http.Server{
		Addr:    s.addr,
		Handler: s.router,
	}

	s.logger.Info("Starting HTTP API (Ollama Compatible)", zap.String("bind-addr", s.addr))
	if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("failed to start http server: %w", err)
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

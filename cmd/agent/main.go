package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"

	"github.com/dhiaayachi/gravity-ai/config"
	"github.com/dhiaayachi/gravity-ai/internal/engine"
	tasks_manager "github.com/dhiaayachi/gravity-ai/internal/engine/tasks-manager"
	agentGrpc "github.com/dhiaayachi/gravity-ai/internal/grpc"
	gravityHttp "github.com/dhiaayachi/gravity-ai/internal/http"
	"github.com/dhiaayachi/gravity-ai/internal/llm"
	"github.com/dhiaayachi/gravity-ai/internal/logger"
	"github.com/dhiaayachi/gravity-ai/internal/raft"
	"github.com/dhiaayachi/gravity-ai/test/mocks"
	raftgrpc "github.com/dhiaayachi/raft-grpc-transport"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

func main() {
	// Define Flags
	// We use pflag for better compatibility with Viper (and standard flags)
	// These flags will override config file and env vars
	pflag.String("agent-id", "agent-1", "Agent ID")
	pflag.String("bind-addr", "127.0.0.1:8000", "Bind address")
	pflag.String("http-addr", ":8080", "HTTP Service address")
	pflag.String("data-dir", "./data", "Data directory")
	pflag.String("peers", "", "Comma-separated list of peer ID=Address pairs (e.g. node2=127.0.0.1:8001)")
	pflag.Bool("bootstrap", false, "Bootstrap the cluster")
	pflag.String("log-level", "info", "Log level (debug, info, warn, error)")

	// LLM Flags
	pflag.String("llm-provider", "mock", "LLM Provider (mock, openai, gemini, claude, ollama)")
	pflag.String("api-key", "", "API Key for llm providers")
	pflag.String("model", "", "Model name (optional)")
	pflag.String("ollama-url", "http://localhost:11434", "Ollama URL")

	// Config File Flag (not bound to config struct, just for loading)
	cfgFile := pflag.String("config", "", "config file (default is ./gravity.yaml)")

	pflag.Parse()

	// Load Configuration
	cfg, err := config.LoadConfig(*cfgFile, pflag.CommandLine)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize Logger
	// We use a hardcoded level from flag/config manually for now or add it to Config struct
	logLevel, _ := pflag.CommandLine.GetString("log_level")
	appLogger, err := logger.New(logLevel)
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer func(appLogger *zap.Logger) {
		err := appLogger.Sync()
		if err != nil {
			log.Fatalf("Failed to sync logger: %v", err)
		}
	}(appLogger)

	appLogger.Info("Starting Agent", zap.String("id", cfg.ID), zap.String("bind-addr", cfg.BindAddr))

	if cfg.Bootstrap {
		appLogger.Info("Bootstrapping cluster", zap.Int("peer_count", len(cfg.Peers)))
	} else {
		appLogger.Info("Starting node (no bootstrap)")
	}

	// Create the main listener
	lis, err := net.Listen("tcp", cfg.BindAddr)
	if err != nil {
		appLogger.Fatal("Failed to listen", zap.String("bind-addr", cfg.BindAddr), zap.Error(err))
	}

	// Create gRPC Server
	// We use insecure here, but in production we should use TLS
	gs := grpc.NewServer()

	// Setup Raft Transport (gRPC)
	tm, err := raftgrpc.NewGrpcTransport(cfg.BindAddr, gs)
	if err != nil {
		appLogger.Fatal("Failed to create raft transport", zap.Error(err))
	}

	peers := make(map[string]string)
	p := strings.Split(cfg.Peers, ",")
	for _, v := range p {
		s := strings.Split(v, "=")
		if len(s) != 2 {
			appLogger.Fatal("Invalid peer address", zap.String("peer", v))
		}
		peers[s[0]] = s[1]
	}
	// Setup Raft Node
	raftConfig := &raft.Config{
		ID:        cfg.ID,
		DataDir:   cfg.DataDir,
		BindAddr:  cfg.BindAddr,
		Bootstrap: cfg.Bootstrap,
		Peers:     peers,
	}

	node, err := raft.NewAgentNode(raftConfig, tm, appLogger)
	if err != nil {
		appLogger.Fatal("Failed to create raft node", zap.Error(err))
	}

	// Setup Dependencies
	var llmClient llm.Client

	switch cfg.LLMProvider {
	case "openai":
		llmClient = llm.NewOpenAIClient(cfg.APIKey, cfg.Model)
	case "gemini":
		client, err := llm.NewGeminiClient(cfg.APIKey, cfg.Model)
		if err != nil {
			appLogger.Fatal("Failed to initialize Gemini client", zap.Error(err))
		}
		llmClient = client
	case "claude":
		llmClient = llm.NewClaudeClient(cfg.APIKey, cfg.Model)
	case "ollama":
		client, err := llm.NewOllamaClient(cfg.OllamaURL, cfg.Model)
		if err != nil {
			appLogger.Fatal("Failed to initialize Ollama client", zap.Error(err))
		}
		llmClient = client
	case "mock":
		llmClient = &mocks.MockLLM{Healthy: true}
	default:
		appLogger.Fatal("Unknown LLM provider", zap.String("provider", cfg.LLMProvider))
	}

	// We pass 0 as port or remove port dependency in client next
	clusterClient := agentGrpc.NewClient()

	tasksMgr := tasks_manager.TasksManager{}
	eng := engine.NewEngine(node, llmClient, clusterClient, &tasksMgr, appLogger, cfg.LLMProvider, cfg.Model)

	eng.Start()

	svc := agentGrpc.NewAgentService(node.Raft, &tasksMgr)
	// Start gRPC Server
	// We ignore grpcPort flag and use the muxed listener
	grpcServer := agentGrpc.NewServer(svc, node, 0, gs, appLogger) // Port is irrelevant for muxed listener but might be used for logging

	if err := grpcServer.Start(lis); err != nil {
		appLogger.Fatal("Failed to start gRPC server", zap.Error(err))
	}

	// Start HTTP Server for API/Admin
	httpServer := gravityHttp.NewServer(cfg.HTTPAddr, svc, eng, appLogger)
	go func() {
		if err := httpServer.Run(); err != nil {
			appLogger.Error("HTTP Start failed", zap.Error(err))
		}
	}()

	// Wait for leader logic (Bootstrap only)
	if cfg.Bootstrap {
		appLogger.Info("Node is bootstrapping...")
	}

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	<-sigCh

	appLogger.Info("Shutting down...")
	grpcServer.Stop()
	if err := httpServer.Shutdown(context.Background()); err != nil {
		appLogger.Error("HTTP server shutdown error", zap.Error(err))
	}
	if err := node.Close(); err != nil {
		appLogger.Error("Error shutting down", zap.Error(err))
	}
}

package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"

	"github.com/dhiaayachi/gravity-ai/config"
	"github.com/dhiaayachi/gravity-ai/internal/engine"
	agentGrpc "github.com/dhiaayachi/gravity-ai/internal/grpc"
	"github.com/dhiaayachi/gravity-ai/internal/health"
	gravityHttp "github.com/dhiaayachi/gravity-ai/internal/http"
	"github.com/dhiaayachi/gravity-ai/internal/llm"
	"github.com/dhiaayachi/gravity-ai/internal/logger"
	"github.com/dhiaayachi/gravity-ai/internal/raft"
	tasks_manager "github.com/dhiaayachi/gravity-ai/internal/tasks-manager"
	"github.com/dhiaayachi/gravity-ai/test/mocks"
	"github.com/soheilhy/cmux"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
)

func main() {
	// Define Flags
	// We use pflag for better compatibility with Viper (and standard flags)
	// These flags will override config file and env vars
	pflag.String("id", "agent-1", "Agent ID")
	pflag.String("addr", "127.0.0.1:8000", "Bind address")
	pflag.String("http_addr", ":8080", "HTTP Service address")
	pflag.String("data_dir", "./data", "Data directory")
	pflag.StringToString("peers", nil, "Comma-separated list of peer ID=Address pairs (e.g. node2=127.0.0.1:8001)")
	pflag.Bool("bootstrap", false, "Bootstrap the cluster")
	pflag.String("log_level", "info", "Log level (debug, info, warn, error)")

	// LLM Flags
	pflag.String("llm_provider", "mock", "LLM Provider (mock, openai, gemini, claude, ollama)")
	pflag.String("api_key", "", "API Key for cloud providers")
	pflag.String("model", "", "Model name (optional)")
	pflag.String("ollama_url", "http://localhost:11434", "Ollama URL")

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
	defer appLogger.Sync()

	appLogger.Info("Starting Agent", zap.String("id", cfg.ID), zap.String("bind_addr", cfg.BindAddr))

	if cfg.Bootstrap {
		appLogger.Info("Bootstrapping cluster", zap.Int("peer_count", len(cfg.Peers)))
	} else {
		appLogger.Info("Starting node (no bootstrap)")
	}

	// Create the main listener
	lis, err := net.Listen("tcp", cfg.BindAddr)
	if err != nil {
		appLogger.Fatal("Failed to listen", zap.String("addr", cfg.BindAddr), zap.Error(err))
	}

	// Create cmux
	m := cmux.New(lis)

	// Match connections
	// gRPC (look for HTTP2 with gRPC content type)
	grpcL := m.Match(cmux.HTTP2())
	// Raft (Any other traffic - Fallback)
	raftL := m.Match(cmux.Any())

	// Setup Raft Node
	raftConfig := &raft.Config{
		ID:        cfg.ID,
		DataDir:   cfg.DataDir,
		BindAddr:  cfg.BindAddr,
		Bootstrap: cfg.Bootstrap,
		Peers:     cfg.Peers,
	}

	node, err := raft.NewAgentNode(raftConfig, raftL, appLogger)
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

	healthMonitor := health.NewMonitor(llmClient)
	// We pass 0 as port or remove port dependency in client next
	clusterClient := agentGrpc.NewClient(0)

	tasksMgr := tasks_manager.TasksManager{}
	eng := engine.NewEngine(node, healthMonitor, llmClient, clusterClient, &tasksMgr, appLogger)

	eng.Start()

	// Start cmux serving
	go func() {
		if err := m.Serve(); err != nil {
			appLogger.Fatal("cmux failed", zap.Error(err))
		}
	}()

	svc := agentGrpc.NewAgentService(node.Raft, &tasksMgr)
	// Start gRPC Server
	// We ignore grpcPort flag and use the muxed listener
	grpcServer := agentGrpc.NewServer(svc, node, 0, appLogger) // Port is irrelevant for muxed listener but might be used for logging
	if err := grpcServer.Start(grpcL); err != nil {
		appLogger.Fatal("Failed to start gRPC server", zap.Error(err))
	}

	// Start HTTP Server for API/Admin
	httpServer := gravityHttp.NewServer(cfg.HTTPAddr, svc, appLogger)
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

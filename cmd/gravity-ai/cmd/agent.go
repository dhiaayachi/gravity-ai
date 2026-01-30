package cmd

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"

	"github.com/dhiaayachi/gravity-ai/config"
	clusterstate "github.com/dhiaayachi/gravity-ai/internal/cluster-state"
	"github.com/dhiaayachi/gravity-ai/internal/engine"
	tasks_manager "github.com/dhiaayachi/gravity-ai/internal/engine/tasks-manager"
	agentGrpc "github.com/dhiaayachi/gravity-ai/internal/grpc"
	gravityHttp "github.com/dhiaayachi/gravity-ai/internal/http"
	"github.com/dhiaayachi/gravity-ai/internal/llm"
	"github.com/dhiaayachi/gravity-ai/internal/logger"
	"github.com/dhiaayachi/gravity-ai/internal/raft"
	"github.com/dhiaayachi/gravity-ai/test/mocks"
	raftgrpc "github.com/dhiaayachi/raft-grpc-transport"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// agentCmd represents the agent command
var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Runs the Gravity AI Agent server",
	Long:  `Starts the Gravity AI Agent, including the Raft consensus engine, gRPC server, and HTTP API.`,
	Run: func(cmd *cobra.Command, args []string) {
		runAgent(cmd)
	},
}

func init() {
	rootCmd.AddCommand(agentCmd)

	// Local Flags for Agent
	agentCmd.Flags().String("agent-id", "agent-1", "Agent ID")
	agentCmd.Flags().String("bind-addr", "127.0.0.1:8000", "Bind address")
	agentCmd.Flags().String("http-addr", ":8080", "HTTP Service address")
	agentCmd.Flags().String("data-dir", "./data", "Data directory")
	agentCmd.Flags().String("peers", "", "Comma-separated list of peer ID=Address pairs (e.g. node2=127.0.0.1:8001)")
	agentCmd.Flags().Bool("bootstrap", false, "Bootstrap the cluster")

	// LLM Flags
	agentCmd.Flags().String("llm-provider", "mock", "LLM Provider (mock, openai, gemini, claude, ollama)")
	agentCmd.Flags().String("api-key", "", "API Key for llm providers")
	agentCmd.Flags().String("llm-model", "", "Model name (optional)")
	agentCmd.Flags().String("ollama-url", "http://localhost:11434", "Ollama URL")
}

func runAgent(cmd *cobra.Command) {
	// Load Configuration
	// We pass the command flags to LoadConfig so it can bind them
	cfg, err := config.LoadConfig(cfgFile, cmd.Flags())
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	SetConfig(cfg)

	// Initialize Logger
	logLevel := cfg.LogLevel // Precedence: Flag > Config > Default
	// But wait, config.LoadConfig already handles precedence if we use it correctly.
	// Let's ensure global log-level flag overrides config file.
	// config.LoadConfig binds flags, so cfg.LogLevel should be correct.

	appLogger, err := logger.New(logLevel)
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	SetLogger(appLogger) // Set global logger

	defer func(appLogger *zap.Logger) {
		err := appLogger.Sync()
		if err != nil {
			// ignore sync error on stdout
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
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		s := strings.Split(v, "=")
		if len(s) != 2 {
			appLogger.Fatal("Invalid peer address", zap.String("peer", v))
		}
		peers[strings.TrimSpace(s[0])] = strings.TrimSpace(s[1])
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
	taskManager := tasks_manager.TasksManager{}

	eng := engine.NewEngine(node, llmClient, clusterClient, &taskManager, appLogger, cfg.LLMProvider, cfg.Model)

	eng.Start()

	svc := agentGrpc.NewAgentService(node.Raft, &taskManager)
	// Start gRPC Server
	// We ignore grpcPort flag and use the muxed listener
	grpcServer := agentGrpc.NewServer(svc, node, 0, gs, appLogger) // Port is irrelevant for muxed listener but might be used for logging

	if err := grpcServer.Start(lis); err != nil {
		appLogger.Fatal("Failed to start gRPC server", zap.Error(err))
	}

	// Start HTTP Server for API/Admin
	httpServer := gravityHttp.NewServer(cfg.HTTPAddr, svc, clusterstate.NewDefaultClusterState(node, node.FSM), appLogger)
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

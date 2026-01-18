package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"

	"github.com/dhiaayachi/gravity-ai/config"
	"github.com/dhiaayachi/gravity-ai/internal/engine"
	agentGrpc "github.com/dhiaayachi/gravity-ai/internal/grpc"
	"github.com/dhiaayachi/gravity-ai/internal/health"
	"github.com/dhiaayachi/gravity-ai/internal/llm"
	"github.com/dhiaayachi/gravity-ai/internal/raft"
	tasks_manager "github.com/dhiaayachi/gravity-ai/internal/tasks-manager"
	"github.com/dhiaayachi/gravity-ai/test/mocks"
	"github.com/soheilhy/cmux"
	"github.com/spf13/pflag"
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

	log.Printf("Starting Agent %s on %s...", cfg.ID, cfg.BindAddr)

	if cfg.Bootstrap {
		log.Printf("Bootstrapping cluster with %d peers", len(cfg.Peers))
	} else {
		log.Printf("Starting node (no bootstrap)")
	}

	// Create the main listener
	lis, err := net.Listen("tcp", cfg.BindAddr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", cfg.BindAddr, err)
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

	node, err := raft.NewAgentNode(raftConfig, raftL)
	if err != nil {
		log.Fatalf("Failed to create raft node: %v", err)
	}

	// Setup Dependencies
	var llmClient llm.Client

	switch cfg.LLMProvider {
	case "openai":
		llmClient = llm.NewOpenAIClient(cfg.APIKey, cfg.Model)
	case "gemini":
		client, err := llm.NewGeminiClient(cfg.APIKey, cfg.Model)
		if err != nil {
			log.Fatalf("Failed to initialize Gemini client: %v", err)
		}
		llmClient = client
	case "claude":
		llmClient = llm.NewClaudeClient(cfg.APIKey, cfg.Model)
	case "ollama":
		client, err := llm.NewOllamaClient(cfg.OllamaURL, cfg.Model)
		if err != nil {
			log.Fatalf("Failed to initialize Ollama client: %v", err)
		}
		llmClient = client
	case "mock":
		llmClient = &mocks.MockLLM{Healthy: true}
	default:
		log.Fatalf("Unknown LLM provider: %s", cfg.LLMProvider)
	}

	healthMonitor := health.NewMonitor(llmClient)
	// We pass 0 as port or remove port dependency in client next
	clusterClient := agentGrpc.NewClient(0)

	tasksMgr := tasks_manager.TasksManager{}
	eng := engine.NewEngine(node, healthMonitor, llmClient, clusterClient, &tasksMgr)

	eng.Start()

	// Start cmux serving
	go func() {
		if err := m.Serve(); err != nil {
			log.Fatalf("cmux failed: %v", err)
		}
	}()

	svc := agentGrpc.NewAgentService(node.Raft, &tasksMgr)
	// Start gRPC Server
	// We ignore grpcPort flag and use the muxed listener
	grpcServer := agentGrpc.NewServer(svc, node, 0) // Port is irrelevant for muxed listener but might be used for logging
	if err := grpcServer.Start(grpcL); err != nil {
		log.Fatalf("Failed to start gRPC server: %v", err)
	}

	// Start HTTP Server for API/Admin
	go func() {
		http.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
			taskContent := r.URL.Query().Get("content")
			if taskContent == "" {
				http.Error(w, "missing content", http.StatusBadRequest)
				return
			}
			f, err := svc.SubmitTask(taskContent, "api-user")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Write([]byte(f.TaskID))
		})

		log.Printf("Starting HTTP API on %s", cfg.HTTPAddr)
		if err := http.ListenAndServe(cfg.HTTPAddr, nil); err != nil {
			// Don't fatal, just log error, as this is secondary
			log.Printf("HTTP Start failed: %v", err)
		}
	}()

	// Wait for leader logic (Bootstrap only)
	if cfg.Bootstrap {
		log.Println("Node is bootstrapping...")
	}

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	<-sigCh

	log.Println("Shutting down...")
	grpcServer.Stop()
	if err := node.Close(); err != nil {
		log.Printf("Error shutting down: %v", err)
	}
}

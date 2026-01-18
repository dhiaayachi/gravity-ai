package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"net"

	"github.com/dhiaayachi/gravity-ai/internal/engine"
	agentGrpc "github.com/dhiaayachi/gravity-ai/internal/grpc"
	"github.com/dhiaayachi/gravity-ai/internal/health"
	"github.com/dhiaayachi/gravity-ai/internal/llm"
	"github.com/dhiaayachi/gravity-ai/internal/raft"
	tasks_manager "github.com/dhiaayachi/gravity-ai/internal/tasks-manager"
	"github.com/dhiaayachi/gravity-ai/test/mocks"
	"github.com/soheilhy/cmux"
)

func main() {
	id := flag.String("id", "agent-1", "Agent ID")
	addr := flag.String("addr", "127.0.0.1:8000", "Bind address")
	dataDir := flag.String("data-dir", "./data", "Data directory")
	peersFlag := flag.String("peers", "", "Comma-separated list of peer ID=Address pairs (e.g. node2=127.0.0.1:8001,node3=127.0.0.1:8002)")
	bootstrap := flag.Bool("bootstrap", false, "Bootstrap the cluster")
	httpAddr := flag.String("http", ":8080", "HTTP Service address")

	// LLM Flags
	provider := flag.String("llm-provider", "mock", "LLM Provider (mock, openai, gemini, claude, ollama)")
	apiKey := flag.String("api-key", "", "API Key for cloud providers")
	model := flag.String("model", "", "Model name (optional)")
	ollamaHost := flag.String("ollama-url", "http://localhost:11434", "Ollama URL")

	flag.Parse()

	log.Printf("Starting Agent %s on %s...", *id, *addr)

	// Parse Peers
	peers := make(map[string]string)
	if *peersFlag != "" {
		importStrings := strings.Split(*peersFlag, ",")
		for _, s := range importStrings {
			parts := strings.Split(s, "=")
			if len(parts) == 2 {
				peers[parts[0]] = parts[1]
			}
		}
	}

	if *bootstrap {
		log.Printf("Bootstrapping cluster with %d peers", len(peers))
	} else {
		log.Printf("Starting node (no bootstrap)")
	}

	// Create the main listener
	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", *addr, err)
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
		ID:        *id,
		DataDir:   *dataDir,
		BindAddr:  *addr,
		Bootstrap: *bootstrap,
		Peers:     peers,
	}

	node, err := raft.NewAgentNode(raftConfig, raftL)
	if err != nil {
		log.Fatalf("Failed to create raft node: %v", err)
	}

	// Setup Dependencies
	var llmClient llm.Client

	switch *provider {
	case "openai":
		llmClient = llm.NewOpenAIClient(*apiKey, *model)
	case "gemini":
		client, err := llm.NewGeminiClient(*apiKey, *model)
		if err != nil {
			log.Fatalf("Failed to initialize Gemini client: %v", err)
		}
		llmClient = client
	case "claude":
		llmClient = llm.NewClaudeClient(*apiKey, *model)
	case "ollama":
		client, err := llm.NewOllamaClient(*ollamaHost, *model)
		if err != nil {
			log.Fatalf("Failed to initialize Ollama client: %v", err)
		}
		llmClient = client
	case "mock":
		llmClient = &mocks.MockLLM{Healthy: true}
	default:
		log.Fatalf("Unknown LLM provider: %s", *provider)
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

		log.Printf("Starting HTTP API on %s", *httpAddr)
		if err := http.ListenAndServe(*httpAddr, nil); err != nil {
			log.Fatalf("HTTP Start failed: %v", err)
		}
	}()

	// Wait for leader logic (Bootstrap only)
	if *bootstrap {
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

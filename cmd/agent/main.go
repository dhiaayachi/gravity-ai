package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/dhiaayachi/gravity-ai/internal/engine"
	"github.com/dhiaayachi/gravity-ai/internal/health"
	"github.com/dhiaayachi/gravity-ai/internal/llm"
	"github.com/dhiaayachi/gravity-ai/internal/raft"
)

func main() {
	id := flag.String("id", "agent-1", "Agent ID")
	addr := flag.String("addr", "127.0.0.1:8000", "Bind address")
	dataDir := flag.String("data-dir", "./data", "Data directory")
	bootstrap := flag.Bool("bootstrap", false, "Bootstrap the cluster")
	flag.Parse()

	log.Printf("Starting Agent %s on %s...", *id, *addr)

	// Setup Raft Node
	raftConfig := &raft.Config{
		ID:        *id,
		DataDir:   *dataDir,
		BindAddr:  *addr,
		Bootstrap: *bootstrap,
	}

	node, err := raft.NewAgentNode(raftConfig)
	if err != nil {
		log.Fatalf("Failed to create raft node: %v", err)
	}

	// Setup Dependencies
	llmClient := &llm.MockClient{Healthy: true}
	healthMonitor := health.NewMonitor(llmClient)
	eng := engine.NewEngine(node, healthMonitor, llmClient)
	eng.Start()

	// Wait for leader
	if *bootstrap {
		log.Println("Waiting for leader election...")
		time.Sleep(3 * time.Second) // Give some time for election

		// Simulate Task Submission if we are leader
		go func() {
			time.Sleep(5 * time.Second)
			log.Println("Submitting a test task...")
			task, err := eng.SubmitTask("What is the meaning of life?", "user-1")
			if err != nil {
				log.Printf("Failed to submit task: %v", err)
			} else {
				log.Printf("Task submitted: %s", task.ID)
			}
		}()
	}

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	<-sigCh

	log.Println("Shutting down...")
	if err := node.Close(); err != nil {
		log.Printf("Error shutting down: %v", err)
	}
}

package metadata

import (
	"context"
	"encoding/json"
	"time"

	"github.com/dhiaayachi/gravity-ai/internal/core"
	"github.com/dhiaayachi/gravity-ai/internal/engine"
	"github.com/dhiaayachi/gravity-ai/internal/raft"
	"github.com/dhiaayachi/gravity-ai/internal/raft/fsm"
	"go.uber.org/zap"
)

type Provider struct {
	node          *raft.AgentNode
	clusterClient engine.ClusterClient
	logger        *zap.SugaredLogger
	llmProvider   string
	llmModel      string
}

func NewProvider(node *raft.AgentNode, client engine.ClusterClient, llmProvider, llmModel string) *Provider {
	return &Provider{
		node:          node,
		clusterClient: client,
		llmProvider:   llmProvider,
		llmModel:      llmModel,
	}

}

func (p *Provider) RegisterMetadata() {
	//Wait for raft to elect a leader
	for retry := 0; retry < 20; retry++ {
		// Wait a bit for Raft to be ready?
		_, id := p.node.Raft.LeaderWithID()
		if id != "" {
			break
		}
		time.Sleep(100 * time.Second)
	}

	meta := core.AgentMetadata{
		ID:          p.node.Config.ID,
		LLMProvider: p.llmProvider,
		LLMModel:    p.llmModel,
	}

	// Retry loop
	for i := 0; i < 5; i++ {
		var err error
		//check leader
		_, id := p.node.Raft.LeaderWithID()
		if string(id) == p.node.Config.ID {
			// Local Apply
			metaBytes, _ := json.Marshal(meta)
			cmd := fsm.LogCommand{
				Type:  fsm.CommandTypeUpdateMetadata,
				Value: metaBytes,
			}
			b, _ := json.Marshal(cmd)
			if f := p.node.Raft.Apply(b, 5*time.Second); f.Error() != nil {
				err = f.Error()
			}
		} else {
			// Forward to Leader
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			err = p.clusterClient.UpdateMetadata(ctx, meta.ID, meta.LLMProvider, meta.LLMModel)

		}

		if err == nil {
			p.logger.Info("Published Metadata", zap.String("id", meta.ID))
			break
		}
		p.logger.Warn("Failed to publish metadata, retrying...", zap.Error(err))
		time.Sleep(2 * time.Second)
	}
}

package grpc

import (
	"context"
	"fmt"

	"github.com/dhiaayachi/gravity-ai/internal/engine"
	pb "github.com/dhiaayachi/gravity-ai/proto/gravity/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Client struct {
	localAddr string
}

func NewClient(localAddr string) *Client {
	return &Client{
		localAddr: localAddr,
	}
}

// Ensure Client implements engine.ClusterClient
var _ engine.ClusterClient = (*Client)(nil)

func (c *Client) SubmitVote(ctx context.Context, taskID, agentID string, accepted bool) error {
	target := c.localAddr

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("failed to dial leader: %w", err)
	}
	defer func(conn *grpc.ClientConn) {
		_ = conn.Close()
	}(conn)

	client := pb.NewGravityServiceClient(conn)
	req := &pb.SubmitVoteRequest{
		TaskId:   taskID,
		AgentId:  agentID,
		Accepted: accepted,
	}

	resp, err := client.SubmitVote(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("leader rejected vote: %s", resp.Message)
	}
	return nil
}

func (c *Client) SubmitAnswer(ctx context.Context, taskID, agentID string, content string) error {
	target := c.localAddr

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("failed to dial leader: %w", err)
	}
	defer func(conn *grpc.ClientConn) {
		_ = conn.Close()
	}(conn)

	client := pb.NewGravityServiceClient(conn)
	req := &pb.SubmitAnswerRequest{
		TaskId:  taskID,
		AgentId: agentID,
		Content: content,
	}

	resp, err := client.SubmitAnswer(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("leader rejected answer: %s", resp.Message)
	}
	return nil
}

func (c *Client) UpdateMetadata(ctx context.Context, agentID, provider, model string) error {
	target := c.localAddr

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("failed to dial leader: %w", err)
	}
	defer func(conn *grpc.ClientConn) {
		_ = conn.Close()
	}(conn)

	client := pb.NewGravityServiceClient(conn)
	req := &pb.UpdateMetadataRequest{
		AgentId:     agentID,
		LlmProvider: provider,
		LlmModel:    model,
	}

	resp, err := client.UpdateMetadata(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("leader rejected metadata update: %s", resp.Message)
	}
	return nil
}

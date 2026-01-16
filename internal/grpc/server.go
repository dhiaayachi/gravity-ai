package grpc

import (
	"context"
	"fmt"
	"log"
	"net"

	"github.com/dhiaayachi/gravity-ai/internal/engine"
	"github.com/dhiaayachi/gravity-ai/internal/raft"
	pb "github.com/dhiaayachi/gravity-ai/proto/gravity/v1"
	hashicorpRaft "github.com/hashicorp/raft"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Server struct {
	pb.UnimplementedGravityServiceServer
	engine *engine.Engine
	node   *raft.AgentNode
	port   int
	server *grpc.Server
}

func NewServer(eng *engine.Engine, node *raft.AgentNode, port int) *Server {
	return &Server{
		engine: eng,
		node:   node,
		port:   port,
	}
}

func (s *Server) Start(l net.Listener) error {
	opts := []grpc.ServerOption{}
	s.server = grpc.NewServer(opts...)
	pb.RegisterGravityServiceServer(s.server, s)

	log.Printf("Starting gRPC server on port %d...", s.port)
	go func() {
		if err := s.server.Serve(l); err != nil {
			log.Printf("gRPC server stopped: %v", err)
		}
	}()
	return nil
}

func (s *Server) Stop() {
	if s.server != nil {
		s.server.GracefulStop()
	}
}

func (s *Server) getLeaderConn(ctx context.Context) (*grpc.ClientConn, error) {
	leaderAddr := s.node.Raft.Leader()
	if leaderAddr == "" {
		return nil, fmt.Errorf("no leader")
	}

	// Assuming Raft address is Host:RaftPort
	// We want Host:GRPCPort
	host, _, err := net.SplitHostPort(string(leaderAddr))
	if err != nil {
		// Fallback if no port in address (e.g. just hostname?)
		// But Raft addresses almost always have ports.
		return nil, fmt.Errorf("invalid leader address: %v", err)
	}

	target := fmt.Sprintf("%s:%d", host, s.port)
	return grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

func (s *Server) SubmitTask(ctx context.Context, req *pb.SubmitTaskRequest) (*pb.SubmitTaskResponse, error) {
	if response, err, forward := forwardToLeader(ctx, s, req,
		func(client pb.GravityServiceClient, ctx context.Context, req *pb.SubmitTaskRequest) (*pb.SubmitTaskResponse, error) {
			return client.SubmitTask(ctx, req)
		}); forward {
		return response, err
	}

	future, err := s.engine.SubmitTask(req.Content, req.Requester)
	if err != nil {
		return nil, err
	}

	return &pb.SubmitTaskResponse{
		TaskId: future.TaskID,
	}, nil
}

func (s *Server) SubmitAnswer(ctx context.Context, req *pb.SubmitAnswerRequest) (*pb.SubmitAnswerResponse, error) {
	if response, err, forward := forwardToLeader(ctx, s, req,
		func(client pb.GravityServiceClient, ctx context.Context, req *pb.SubmitAnswerRequest) (*pb.SubmitAnswerResponse, error) {
			return client.SubmitAnswer(ctx, req)
		}); forward {
		return response, err
	}

	// Using Engine.SubmitAnswer as proxy for Proposal in this architecture
	err := s.engine.SubmitAnswer(req.TaskId, req.AgentId, req.Content)
	if err != nil {
		return &pb.SubmitAnswerResponse{Success: false, Message: err.Error()}, nil
	}

	return &pb.SubmitAnswerResponse{Success: true}, nil
}

func (s *Server) SubmitVote(ctx context.Context, req *pb.SubmitVoteRequest) (*pb.SubmitVoteResponse, error) {
	if response, err, forward := forwardToLeader(ctx, s, req,
		func(client pb.GravityServiceClient, ctx context.Context, req *pb.SubmitVoteRequest) (*pb.SubmitVoteResponse, error) {
			return client.SubmitVote(ctx, req)
		}); forward {
		return response, err
	}

	err := s.engine.SubmitVote(req.TaskId, req.AgentId, req.Accepted)
	if err != nil {
		return &pb.SubmitVoteResponse{Success: false, Message: err.Error()}, nil
	}

	return &pb.SubmitVoteResponse{Success: true}, nil
}

func forwardToLeader[Req any, Res any](ctx context.Context, s *Server, req Req, method func(pb.GravityServiceClient, context.Context, Req) (*Res, error)) (*Res, error, bool) {
	if s.node.Raft.State() != hashicorpRaft.Leader {
		conn, err := s.getLeaderConn(ctx)
		if err != nil {
			return nil, err, false
		}
		defer conn.Close()
		client := pb.NewGravityServiceClient(conn)
		res, err := method(client, ctx, req)
		return res, err, true
	} else {
		return nil, nil, false
	}
}

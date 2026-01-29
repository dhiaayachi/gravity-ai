package grpc

import (
	"context"
	"fmt"
	"net"

	"github.com/dhiaayachi/gravity-ai/internal/raft"
	pb "github.com/dhiaayachi/gravity-ai/proto/gravity/v1"
	hashicorpRaft "github.com/hashicorp/raft"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Server struct {
	pb.UnimplementedGravityServiceServer
	service *AgentService
	node    *raft.AgentNode
	port    int
	server  *grpc.Server
	logger  *zap.Logger
}

func NewServer(svc *AgentService, node *raft.AgentNode, port int, server *grpc.Server, logger *zap.Logger) *Server {
	s := &Server{
		service: svc,
		node:    node,
		port:    port,
		server:  server,
		logger:  logger.With(zap.String("component", "grpc_server"), zap.Int("port", port)),
	}
	pb.RegisterGravityServiceServer(server, s)
	return s
}

func (s *Server) Start(l net.Listener) error {
	s.logger.Info("Starting gRPC server")
	go func() {
		if err := s.server.Serve(l); err != nil {
			s.logger.Error("gRPC server stopped", zap.Error(err))
		}
	}()
	return nil
}

func (s *Server) Stop() {
	if s.server != nil {
		s.server.GracefulStop()
	}
}

func (s *Server) GetGrpcServer() *grpc.Server {
	return s.server
}

func (s *Server) getLeaderConn() (*grpc.ClientConn, error) {
	leaderAddr, _ := s.node.Raft.LeaderWithID()
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

	future, err := s.service.SubmitTask(req.Content, req.Requester)
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
	err := s.service.SubmitAnswer(req.TaskId, req.AgentId, req.Content)
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

	err := s.service.SubmitVote(req.TaskId, req.AgentId, req.Accepted)
	if err != nil {
		return &pb.SubmitVoteResponse{Success: false, Message: err.Error()}, nil
	}

	return &pb.SubmitVoteResponse{Success: true}, nil
}

func (s *Server) UpdateMetadata(ctx context.Context, req *pb.UpdateMetadataRequest) (*pb.UpdateMetadataResponse, error) {
	if response, err, forward := forwardToLeader(ctx, s, req,
		func(client pb.GravityServiceClient, ctx context.Context, req *pb.UpdateMetadataRequest) (*pb.UpdateMetadataResponse, error) {
			return client.UpdateMetadata(ctx, req)
		}); forward {
		return response, err
	}

	err := s.service.UpdateMetadata(ctx, req.AgentId, req.LlmProvider, req.LlmModel)
	if err != nil {
		return &pb.UpdateMetadataResponse{Success: false, Message: err.Error()}, nil
	}

	return &pb.UpdateMetadataResponse{Success: true}, nil
}

func forwardToLeader[Req any, Res any](ctx context.Context, s *Server, req Req, method func(pb.GravityServiceClient, context.Context, Req) (*Res, error)) (*Res, error, bool) {
	if s.node.Raft.State() != hashicorpRaft.Leader {
		conn, err := s.getLeaderConn()
		if err != nil {
			return nil, err, false
		}
		defer func(conn *grpc.ClientConn) {
			err := conn.Close()
			if err != nil {
				s.logger.Warn("Failed to close leader connection", zap.Error(err))
			}
		}(conn)
		client := pb.NewGravityServiceClient(conn)
		res, err := method(client, ctx, req)
		return res, err, true
	}

	return nil, nil, false
}

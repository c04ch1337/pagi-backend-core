package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"time"

	pb "backend-go-model-gateway/proto" // Reference generated code package

	"google.golang.org/grpc"
)

//go:generate protoc --go_out=./proto --go_opt=paths=source_relative --go-grpc_out=./proto --go-grpc_opt=paths=source_relative proto/model.proto

// --- Configuration ---
const DEFAULT_GRPC_PORT = 50051
const SERVICE_NAME = "backend-go-model-gateway"
const VERSION = "1.0.0"

// --- gRPC Server Implementation ---
type server struct {
	pb.UnimplementedModelGatewayServer
}

// GetPlan implements modelgateway.ModelGatewayServer.
func (s *server) GetPlan(ctx context.Context, in *pb.PlanRequest) (*pb.PlanResponse, error) {
	// Structured JSON logging
	log.Printf(
		`{"timestamp": "%s", "level": "info", "service": "%s", "method": "GetPlan", "prompt": %q, "message": "Simulating LLM request and plan generation."}`,
		time.Now().Format(time.RFC3339Nano), SERVICE_NAME, in.GetPrompt(),
	)

	// Simulate latency
	time.Sleep(150 * time.Millisecond)

	return &pb.PlanResponse{
		Plan:      fmt.Sprintf("Plan generated for prompt: %s", in.GetPrompt()),
		ModelName: "pagi-mock-llm-1",
		LatencyMs: 150,
	}, nil
}

func main() {
	// Parse port from environment or flag
	grpcPortEnv := os.Getenv("MODEL_GATEWAY_GRPC_PORT")
	port, err := strconv.Atoi(grpcPortEnv)
	if err != nil || port == 0 {
		port = DEFAULT_GRPC_PORT
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatalf(
			`{"timestamp": "%s", "level": "fatal", "service": "%s", "error": "failed to listen: %v"}`,
			time.Now().Format(time.RFC3339Nano), SERVICE_NAME, err,
		)
	}

	s := grpc.NewServer()
	pb.RegisterModelGatewayServer(s, &server{})

	log.Printf(
		`{"timestamp": "%s", "level": "info", "service": "%s", "version": "%s", "port": %d, "message": "gRPC server listening."}`,
		time.Now().Format(time.RFC3339Nano), SERVICE_NAME, VERSION, port,
	)

	if err := s.Serve(lis); err != nil {
		log.Fatalf(
			`{"timestamp": "%s", "level": "fatal", "service": "%s", "error": "failed to serve: %v"}`,
			time.Now().Format(time.RFC3339Nano), SERVICE_NAME, err,
		)
	}
}

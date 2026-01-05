package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	pb "backend-go-model-gateway/proto" // Reference generated code package

	openai "github.com/sashabaranov/go-openai"
	"google.golang.org/grpc"
)

//go:generate protoc --go_out=./proto --go_opt=paths=source_relative --go-grpc_out=./proto --go-grpc_opt=paths=source_relative proto/model.proto

// --- Configuration ---
const DEFAULT_GRPC_PORT = 50051
const SERVICE_NAME = "backend-go-model-gateway"
const VERSION = "1.0.0"

const (
	defaultProvider          = "openrouter"
	defaultOllamaBaseURL     = "http://localhost:11434"
	defaultRequestTimeoutSec = 5
)

type llmProvider string

const (
	providerOpenRouter llmProvider = "openrouter"
	providerOllama     llmProvider = "ollama"
)

type llmRuntime struct {
	Provider llmProvider
	Model    string
	Client   *openai.Client
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	i, err := strconv.Atoi(v)
	if err != nil || i <= 0 {
		return fallback
	}
	return i
}

func normalizeOllamaBaseURL(base string) string {
	// Ollama's OpenAI-compatible endpoint is typically at /v1
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1") {
		return base
	}
	return base + "/v1"
}

func initializeLLMClient() (*llmRuntime, error) {
	provider := llmProvider(strings.ToLower(getEnv("LLM_PROVIDER", defaultProvider)))

	// Shared OpenAI-compatible client setup (go-openai)
	switch provider {
	case providerOllama:
		ollamaBase := normalizeOllamaBaseURL(getEnv("OLLAMA_BASE_URL", defaultOllamaBaseURL))
		model := getEnv("OLLAMA_MODEL_NAME", "llama3")
		cfg := openai.DefaultConfig("")
		cfg.BaseURL = ollamaBase
		client := openai.NewClientWithConfig(cfg)
		return &llmRuntime{Provider: providerOllama, Model: model, Client: client}, nil

	case providerOpenRouter, "":
		apiKey := os.Getenv("OPENROUTER_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("OPENROUTER_API_KEY is required when LLM_PROVIDER=openrouter")
		}
		model := getEnv("OPENROUTER_MODEL_NAME", "mistralai/mistral-7b-instruct:free")
		cfg := openai.DefaultConfig(apiKey)
		cfg.BaseURL = "https://openrouter.ai/api/v1"
		client := openai.NewClientWithConfig(cfg)
		return &llmRuntime{Provider: providerOpenRouter, Model: model, Client: client}, nil

	default:
		return nil, fmt.Errorf("unsupported LLM_PROVIDER=%q (supported: openrouter, ollama)", provider)
	}
}

// --- gRPC Server Implementation ---
type server struct {
	pb.UnimplementedModelGatewayServer
	llm *llmRuntime
	// Per-request timeout for the LLM call.
	requestTimeout time.Duration
}

// GetPlan implements modelgateway.ModelGatewayServer.
func (s *server) GetPlan(ctx context.Context, in *pb.PlanRequest) (*pb.PlanResponse, error) {
	requestStart := time.Now()

	// Bound the LLM call.
	callCtx, cancel := context.WithTimeout(ctx, s.requestTimeout)
	defer cancel()

	provider := "uninitialized"
	model := "uninitialized"
	if s.llm != nil {
		provider = string(s.llm.Provider)
		model = s.llm.Model
	}

	// Structured JSON logging
	log.Printf(
		`{"timestamp": "%s", "level": "info", "service": "%s", "method": "GetPlan", "provider": %q, "model": %q, "prompt": %q}`,
		time.Now().Format(time.RFC3339Nano), SERVICE_NAME, provider, model, in.GetPrompt(),
	)

	if s.llm == nil || s.llm.Client == nil {
		return nil, fmt.Errorf("LLM client not initialized")
	}

	// Prompt the model to return strict JSON so downstream can parse `model_type` + `steps`.
	system := "You are a planning assistant. Return STRICT JSON only."
	user := fmt.Sprintf(
		"Given this prompt, return a JSON object with keys: model_type (string), steps (array of strings), prompt (string).\nPrompt: %s",
		in.GetPrompt(),
	)

	resp, err := s.llm.Client.CreateChatCompletion(
		callCtx,
		openai.ChatCompletionRequest{
			Model: s.llm.Model,
			Messages: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleSystem, Content: system},
				{Role: openai.ChatMessageRoleUser, Content: user},
			},
			Temperature: 0.2,
		},
	)
	if err != nil {
		return nil, err
	}

	content := ""
	if len(resp.Choices) > 0 {
		content = resp.Choices[0].Message.Content
	}

	// If the provider returns non-JSON, wrap it as JSON for consistency.
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "{") {
		fallback := map[string]any{
			"model_type": provider,
			"steps":      []string{trimmed},
			"prompt":     in.GetPrompt(),
		}
		b, _ := json.Marshal(fallback)
		trimmed = string(b)
	}

	latencyMs := time.Since(requestStart).Milliseconds()
	return &pb.PlanResponse{
		Plan:      trimmed,
		ModelName: s.llm.Model,
		LatencyMs: latencyMs,
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

	llm, err := initializeLLMClient()
	if err != nil {
		log.Fatalf(
			`{"timestamp": "%s", "level": "fatal", "service": "%s", "error": %q}`,
			time.Now().Format(time.RFC3339Nano), SERVICE_NAME, err.Error(),
		)
	}

	timeoutSec := getEnvInt("REQUEST_TIMEOUT_SECONDS", defaultRequestTimeoutSec)

	s := grpc.NewServer()
	pb.RegisterModelGatewayServer(s, &server{llm: llm, requestTimeout: time.Duration(timeoutSec) * time.Second})

	log.Printf(
		`{"timestamp": "%s", "level": "info", "service": "%s", "version": "%s", "port": %d, "provider": %q, "model": %q, "message": "gRPC server listening."}`,
		time.Now().Format(time.RFC3339Nano), SERVICE_NAME, VERSION, port, llm.Provider, llm.Model,
	)

	if err := s.Serve(lis); err != nil {
		log.Fatalf(
			`{"timestamp": "%s", "level": "fatal", "service": "%s", "error": "failed to serve: %v"}`,
			time.Now().Format(time.RFC3339Nano), SERVICE_NAME, err,
		)
	}
}

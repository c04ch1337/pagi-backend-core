package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"backend-go-agent-planner/audit"
	"backend-go-agent-planner/internal/logger"
	pb "backend-go-model-gateway/proto/proto"

	"github.com/go-redis/redis/v8"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type Config struct {
	ModelGatewayAddr    string
	MemoryServiceAddr   string
	MemoryServiceHTTP   string
	RustSandboxGRPCAddr string
	RustSandboxHTTPURL  string
	AuditDBPath         string
	RedisAddr           string

	MaxTurns int
	TopK     int
	KBs      []string
}

func ConfigFromEnv() Config {
	maxTurns := 3
	if v := os.Getenv("AGENT_MAX_TURNS"); v != "" {
		fmt.Sscanf(v, "%d", &maxTurns)
	}

	topK := 3
	if v := os.Getenv("AGENT_RAG_TOP_K"); v != "" {
		fmt.Sscanf(v, "%d", &topK)
	}

	return Config{
		ModelGatewayAddr:    getenv("MODEL_GATEWAY_ADDR", "localhost:50051"),
		MemoryServiceAddr:   getenv("MEMORY_GRPC_ADDR", "localhost:50052"),
		MemoryServiceHTTP:   getenv("MEMORY_URL", "http://localhost:8003"),
		RustSandboxGRPCAddr: getenv("RUST_SANDBOX_GRPC_ADDR", "localhost:50053"),
		RustSandboxHTTPURL:  getenv("RUST_SANDBOX_URL", "http://localhost:8001"),
		AuditDBPath:         getenv("PAGI_AUDIT_DB_PATH", "./pagi_audit.db"),
		RedisAddr:           getenv("REDIS_ADDR", "localhost:6379"),
		MaxTurns:            maxTurns,
		TopK:                topK,
		// Include Mind-KB so the planner can retrieve evolving playbooks via the existing RAG call.
		KBs: []string{"Mind-KB", "Domain-KB", "Body-KB", "Soul-KB"},
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type Planner struct {
	cfg Config

	modelConn  *grpc.ClientConn
	memoryConn *grpc.ClientConn
	rustConn   *grpc.ClientConn

	modelClient  pb.ModelGatewayClient
	memoryClient pb.ModelGatewayClient
	toolClient   pb.ToolServiceClient

	httpClient *http.Client
	auditDB    *audit.AuditDB
	redis      *redis.Client
}

const notificationsChannel = "pagi_notifications"

func NewPlanner(ctx context.Context, cfg Config) (*Planner, error) {
	lg := logger.NewContextLogger(ctx)

	dial := func(ctx context.Context, addr string) (*grpc.ClientConn, error) {
		return grpc.DialContext(
			ctx,
			addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		)
	}

	modelConn, err := dial(ctx, cfg.ModelGatewayAddr)
	if err != nil {
		return nil, fmt.Errorf("dial model gateway: %w", err)
	}

	memoryConn, err := dial(ctx, cfg.MemoryServiceAddr)
	if err != nil {
		_ = modelConn.Close()
		return nil, fmt.Errorf("dial memory service: %w", err)
	}

	rustConn, err := dial(ctx, cfg.RustSandboxGRPCAddr)
	if err != nil {
		_ = memoryConn.Close()
		_ = modelConn.Close()
		return nil, fmt.Errorf("dial rust sandbox: %w", err)
	}

	auditDB, err := audit.NewAuditDB(cfg.AuditDBPath)
	if err != nil {
		_ = rustConn.Close()
		_ = memoryConn.Close()
		_ = modelConn.Close()
		return nil, fmt.Errorf("init audit db: %w", err)
	}

	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		lg.Warn("redis_unavailable", "addr", cfg.RedisAddr, "error", err)
		_ = redisClient.Close()
		redisClient = nil
	}

	return &Planner{
		cfg:          cfg,
		modelConn:    modelConn,
		memoryConn:   memoryConn,
		rustConn:     rustConn,
		modelClient:  pb.NewModelGatewayClient(modelConn),
		memoryClient: pb.NewModelGatewayClient(memoryConn),
		toolClient:   pb.NewToolServiceClient(rustConn),
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		auditDB:      auditDB,
		redis:        redisClient,
	}, nil
}

func (p *Planner) Close() {
	if p == nil {
		return
	}
	if p.modelConn != nil {
		_ = p.modelConn.Close()
	}
	if p.memoryConn != nil {
		_ = p.memoryConn.Close()
	}
	if p.rustConn != nil {
		_ = p.rustConn.Close()
	}
	if p.auditDB != nil {
		_ = p.auditDB.Close()
	}
	if p.redis != nil {
		_ = p.redis.Close()
	}
}

type ToolCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
	Raw  map[string]any `json:"-"`
}

func injectTraceIDToOutgoingGRPC(ctx context.Context) context.Context {
	traceID, _ := ctx.Value(logger.TraceIDKey).(string)
	if strings.TrimSpace(traceID) == "" {
		return ctx
	}
	// gRPC metadata keys must be lowercase.
	key := strings.ToLower(string(logger.TraceIDKey))
	return metadata.AppendToOutgoingContext(ctx, key, traceID)
}

func (p *Planner) RecordStep(ctx context.Context, sessionID, eventType string, data any) error {
	if p == nil || p.auditDB == nil {
		return nil
	}
	traceID, _ := ctx.Value(logger.TraceIDKey).(string)
	return p.auditDB.RecordStep(ctx, traceID, sessionID, eventType, data)
}

func (p *Planner) PublishStatus(ctx context.Context, sessionID string, status string) error {
	if p == nil || p.redis == nil {
		return nil
	}
	traceID, _ := ctx.Value(logger.TraceIDKey).(string)
	payload := map[string]any{
		"trace_id":   traceID,
		"session_id": sessionID,
		"status":     status,
		"timestamp":  time.Now().UTC().Format(time.RFC3339Nano),
	}
	b, _ := json.Marshal(payload)
	return p.redis.Publish(ctx, notificationsChannel, string(b)).Err()
}

func (p *Planner) PublishNotification(ctx context.Context, sessionID string, result string) error {
	if p == nil || p.redis == nil {
		return nil
	}
	traceID, _ := ctx.Value(logger.TraceIDKey).(string)
	payload := map[string]any{
		"trace_id":   traceID,
		"session_id": sessionID,
		"result":     result,
		"timestamp":  time.Now().UTC().Format(time.RFC3339Nano),
	}
	b, _ := json.Marshal(payload)
	return p.redis.Publish(ctx, notificationsChannel, string(b)).Err()
}

// AgentLoop orchestrates Memory -> Plan -> (Tool?) -> Persist, repeating up to MaxTurns.
func (p *Planner) AgentLoop(ctx context.Context, prompt string, sessionID string) (string, error) {
	ctx = injectTraceIDToOutgoingGRPC(ctx)

	basePrompt := prompt
	_ = p.RecordStep(ctx, sessionID, "PLAN_START", map[string]any{"prompt": basePrompt, "max_turns": p.cfg.MaxTurns, "top_k": p.cfg.TopK, "kbs": p.cfg.KBs})
	_ = p.PublishStatus(ctx, sessionID, "STARTED")
	// Collect a per-run playbook sequence (user prompt + tool-plan/tool-result pairs + final answer).
	// This is persisted to Mind-KB only on successful completion.
	playbookSeq := []map[string]string{{"role": "user", "content": basePrompt}}
	hadToolStep := false

	maxTurns := p.cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 3
	}

	for turn := 1; turn <= maxTurns; turn++ {
		// 1) Session history (Episodic/Heart) via Memory HTTP API.
		history, _ := p.fetchSessionHistory(ctx, sessionID)

		// 2) RAG context (Domain/Body/Soul) via Memory gRPC.
		rag, _ := p.memoryClient.GetRAGContext(ctx, &pb.RAGContextRequest{
			Query:          prompt,
			TopK:           int32(p.cfg.TopK),
			KnowledgeBases: p.cfg.KBs,
		})

		plannerInput := buildPlannerPrompt(prompt, history, rag)

		// 3) Planning via Model Gateway.
		planResp, err := p.modelClient.GetPlan(ctx, &pb.PlanRequest{Prompt: plannerInput})
		if err != nil {
			_ = p.RecordStep(ctx, sessionID, "PLAN_ERROR", map[string]any{"error": err.Error()})
			return "", fmt.Errorf("GetPlan: %w", err)
		}
		_ = p.RecordStep(ctx, sessionID, "PLAN_MODEL_RESPONSE", map[string]any{"plan": planResp.GetPlan()})

		toolCall := tryParseToolCall(planResp.GetPlan())
		if toolCall == nil {
			// Successful completion path (non-tool-call final answer).
			playbookSeq = append(playbookSeq, map[string]string{"role": "assistant", "content": planResp.GetPlan()})
			_ = p.RecordStep(ctx, sessionID, "PLAN_END", map[string]any{"result": planResp.GetPlan()})
			if hadToolStep {
				_ = p.storePlaybook(ctx, sessionID, basePrompt, playbookSeq)
			}
			_ = p.storeSessionDelta(ctx, sessionID, prompt, planResp.GetPlan())
			_ = p.PublishNotification(ctx, sessionID, planResp.GetPlan())
			_ = p.PublishStatus(ctx, sessionID, "COMPLETED")
			return planResp.GetPlan(), nil
		}

		_ = p.RecordStep(ctx, sessionID, "TOOL_CALL", map[string]any{"tool": toolCall.Name, "args": toolCall.Args})

		// 4) Tool execution via Rust sandbox ToolService over gRPC.
		toolOut, err := p.executeTool(ctx, toolCall.Name, toolCall.Args)
		if err != nil {
			_ = p.RecordStep(ctx, sessionID, "TOOL_ERROR", map[string]any{"tool": toolCall.Name, "error": err.Error()})
			// Feed tool error back into the loop.
			prompt = prompt + "\n\nTool error: " + err.Error()
			continue
		}
		_ = p.RecordStep(ctx, sessionID, "TOOL_RESULT", map[string]any{"tool": toolCall.Name, "output": toolOut})

		hadToolStep = true
		playbookSeq = append(playbookSeq, map[string]string{"role": "assistant", "content": planResp.GetPlan()})
		playbookSeq = append(playbookSeq, map[string]string{"role": "tool_result", "content": toolOut})

		// 5) Loop/feedback.
		prompt = buildFollowupPrompt(prompt, planResp.GetPlan(), toolOut)
		_ = p.storeSessionDelta(ctx, sessionID, "[tool-plan]", planResp.GetPlan())
		_ = p.storeSessionDelta(ctx, sessionID, "[tool-output]", toolOut)
	}

	return "Max turns reached; unable to complete request.", nil
}

func buildPlannerPrompt(userPrompt string, history []map[string]any, rag *pb.RAGContextResponse) string {
	var b strings.Builder
	b.WriteString("<session_history>\n")
	for _, m := range history {
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		if role != "" || content != "" {
			b.WriteString(role + ": " + content + "\n")
		}
	}
	b.WriteString("</session_history>\n\n")

	b.WriteString("<rag_context>\n")
	if rag != nil {
		for _, m := range rag.GetMatches() {
			b.WriteString("**" + m.GetKnowledgeBase() + "**\n")
			b.WriteString("ID: " + m.GetId() + "\n")
			b.WriteString("Text: " + m.GetText() + "\n---\n")
		}
	}
	b.WriteString("</rag_context>\n\n")

	b.WriteString("<user_prompt>\n")
	b.WriteString(userPrompt)
	b.WriteString("\n</user_prompt>\n")
	return b.String()
}

func buildFollowupPrompt(originalPrompt, plan, toolResult string) string {
	return originalPrompt + "\n\n<plan>\n" + plan + "\n</plan>\n\n<tool_result>\n" + toolResult + "\n</tool_result>\n"
}

func tryParseToolCall(planJSON string) *ToolCall {
	// Minimal parsing strategy:
	// - if JSON contains {"tool": {"name": ..., "args": {...}}} treat it as tool call.
	var raw map[string]any
	if err := json.Unmarshal([]byte(planJSON), &raw); err != nil {
		return nil
	}
	toolObj, ok := raw["tool"].(map[string]any)
	if !ok {
		return nil
	}
	name, _ := toolObj["name"].(string)
	args, _ := toolObj["args"].(map[string]any)
	if strings.TrimSpace(name) == "" {
		return nil
	}
	return &ToolCall{Name: name, Args: args, Raw: raw}
}

func (p *Planner) fetchSessionHistory(ctx context.Context, sessionID string) ([]map[string]any, error) {
	url := strings.TrimRight(p.cfg.MemoryServiceHTTP, "/") + "/memory/latest?session_id=" + sessionID
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("memory/latest: %s", string(b))
	}
	var payload struct {
		Messages []map[string]any `json:"messages"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	return payload.Messages, nil
}

func (p *Planner) storeSessionDelta(ctx context.Context, sessionID, userPrompt, assistantText string) error {
	url := strings.TrimRight(p.cfg.MemoryServiceHTTP, "/") + "/memory/store"
	body := map[string]any{
		"session_id": sessionID,
		"history": []map[string]any{
			{"role": "user", "content": userPrompt},
			{"role": "assistant", "content": assistantText},
		},
		"prompt":       userPrompt,
		"llm_response": map[string]any{"text": assistantText},
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (p *Planner) storePlaybook(
	ctx context.Context,
	sessionID string,
	prompt string,
	historySequence []map[string]string,
) error {
	// POST to the Memory Service HTTP API to persist the playbook into Mind-KB.
	// The Memory Service is responsible for converting this into a Chroma document.
	url := strings.TrimRight(p.cfg.MemoryServiceHTTP, "/") + "/memory/playbook"

	// Skip storing trivial 1-step sessions (no tool use), but keep the call-site simple.
	if len(historySequence) < 3 {
		return nil
	}

	payload := map[string]any{
		"session_id":       sessionID,
		"prompt":           prompt,
		"history_sequence": historySequence,
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("memory/playbook: %s", string(out))
	}
	return nil
}

func (p *Planner) executeTool(ctx context.Context, toolName string, args map[string]any) (string, error) {
	return p.executeToolGRPC(ctx, toolName, args)
}

func (p *Planner) executeToolGRPC(ctx context.Context, toolName string, args map[string]any) (string, error) {
	if p.toolClient == nil {
		return "", fmt.Errorf("rust sandbox tool client is nil")
	}

	if args == nil {
		args = map[string]any{}
	}

	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("marshal tool args: %w", err)
	}

	resp, err := p.toolClient.ExecuteTool(ctx, &pb.ToolRequest{
		ToolName: toolName,
		ArgsJson: string(argsJSON),
	})
	if err != nil {
		return "", fmt.Errorf("ExecuteTool(%q): %w", toolName, err)
	}

	// Keep the tool output structured (LLM-friendly) and consistent across tools.
	out := map[string]any{
		"status": resp.GetStatus(),
		"stdout": resp.GetStdout(),
		"stderr": resp.GetStderr(),
	}
	encoded, _ := json.Marshal(out)
	return string(encoded), nil
}

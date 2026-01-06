package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"backend-go-agent-planner/agent"
	"backend-go-agent-planner/internal/logger"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// traceIDMiddleware generates or extracts a trace ID from the request header
// and adds it to the request context.
func traceIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get(string(logger.TraceIDKey))
		if traceID == "" {
			traceID = uuid.New().String()
		}

		// Propagate ID in response header for client visibility.
		w.Header().Set(string(logger.TraceIDKey), traceID)

		// Inject ID into context.
		ctx := context.WithValue(r.Context(), logger.TraceIDKey, traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requestLogMiddleware logs one line per request, always including trace_id when present.
func requestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		logger.NewContextLogger(r.Context()).Info(
			"http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
		)
	})
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := logger.NewContextLogger(ctx)

	// 1) Initialize Configuration and Planner
	cfg := agent.ConfigFromEnv()
	planner, err := agent.NewPlanner(ctx, cfg)
	if err != nil {
		log.Error("planner_init_failed", "error", err)
		os.Exit(1)
	}
	defer planner.Close()

	// 2) Setup Router
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(traceIDMiddleware)
	r.Use(requestLogMiddleware)

	port := os.Getenv("AGENT_PLANNER_PORT")
	if port == "" {
		port = "8080" // Default port, overridden to 8585 by docker-compose
	}

	// Health Check Endpoint
	r.Get("/health", func(w http.ResponseWriter, _r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Main Planning/Execution Endpoint
	r.Post("/plan", handlePlan(planner))
	// Backwards/alternate naming: allow either endpoint.
	r.Post("/run", handlePlan(planner))

	// 3) Start Server
	server := &http.Server{
		Addr:    fmt.Sprintf(":%s", port),
		Handler: r,
	}

	go func() {
		log.Info("agent_planner_listening", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http_server_failed", "port", port, "error", err)
			os.Exit(1)
		}
	}()

	// 4) Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Info("server_shutdown_start")
	ctxTimeout, cancelTimeout := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelTimeout()

	if err := server.Shutdown(ctxTimeout); err != nil {
		log.Error("server_shutdown_forced", "error", err)
		os.Exit(1)
	}
	log.Info("server_shutdown_complete")
}

type PlanRequest struct {
	Prompt    string `json:"prompt"`
	SessionID string `json:"session_id"`
}

type PlanResponse struct {
	Result string `json:"result"`
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func handlePlan(p *agent.Planner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		log := logger.NewContextLogger(r.Context())

		var req PlanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		if req.Prompt == "" || req.SessionID == "" {
			writeJSONError(w, http.StatusBadRequest, "Prompt and session_id are required")
			return
		}

		log.Info("agent_loop_start", "session_id", req.SessionID)
		result, err := p.AgentLoop(r.Context(), req.Prompt, req.SessionID)
		if err != nil {
			log.Error("agent_loop_failed", "session_id", req.SessionID, "error", err)
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Agent execution failed: %s", err.Error()))
			return
		}
		log.Info("agent_loop_complete", "session_id", req.SessionID)

		resp := PlanResponse{Result: result}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Error("encode_response_failed", "error", err)
		}
	}
}

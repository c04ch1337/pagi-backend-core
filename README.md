# Polygon AGI Backend (Polyglot Microservices)

This repo is a runnable, polyglot microservice scaffold designed for fast iteration on an "agent + tools + gateway + BFF" backend.

## Services & Ports (defaults)

| Service | Language | Purpose | Default Port |
|---|---|---|---|
| backend-python-agent | Python (FastAPI) | Agent planning endpoint + wiring echo call | 8000 |
| backend-rust-sandbox | Rust (Axum/Tokio) | Tool execution sandbox (placeholder) | 8001 |
| backend-go-bff | Go (Gin) | BFF + dashboard aggregator (concurrent fan-out) | 8002 |
| mock_memory_service | Python (FastAPI) | Mock memory endpoint `/memory/latest` | 8003 |
| backend-go-model-gateway | Go (gRPC) | Model gateway placeholder (gRPC) | 50051 |

## Call Flow

Frontend -> **Go BFF** `/api/v1/agi/dashboard-data`

Go BFF concurrently calls:
- Python Agent: `http://.../api/v1/plan`
- Rust Sandbox: `http://.../api/v1/execute_tool`
- Memory Service: `http://.../memory/latest`

Python Agent calls Go BFF `/api/v1/echo` (safe wiring confirmation; no recursion).

## Run (Bare Metal)

1. Copy env:
   - `cp .env.example .env` (optional; defaults exist)

2. Install deps:
   - **Python:** `python -m venv .venv` and activate it.
   - `pip install -r backend-python-agent/requirements.txt`
   - **Go:** `go version` should be installed
   - **Rust:** `cargo --version` should be installed
   - **gRPC Stubs:** Run `make docker-generate` before `make run-dev` to generate Go gRPC stubs.

3. **Run:**
   - `make run-dev`

## Run (Docker Compose)

- `docker compose up --build`

To stop:
- `docker compose down`

## Key Endpoints

### backend-go-bff (8002)
- `GET /health`
- `GET /api/v1/agi/dashboard-data`
- `POST /api/v1/echo`

### backend-python-agent (8000)
- `GET /health`
- `POST /api/v1/plan`

### backend-rust-sandbox (8001)
- `GET /health`
- `POST /api/v1/execute_tool`

### mock_memory_service (8003)
- `GET /health`
- `GET /memory/latest`

## Troubleshooting

- **Port already in use**: edit `.env` (or your environment) and rerun.
- **Service not ready**: check `/health` endpoints; `make run-dev` prints service logs.
- **Go Model Gateway Compile Error (Bare Metal)**: If `go run .` fails due to missing gRPC stubs, run `make docker-generate` and try `make run-dev` again.
- **Docker networking**: in compose, services talk via service names (e.g., `http://pagi-python-agent:8000`).


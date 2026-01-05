from fastapi import FastAPI, Request
import uvicorn
import os
import time
import json
import httpx
from datetime import datetime
from pydantic import BaseModel

app = FastAPI(title="Python Agent Orchestrator")
SERVICE_NAME = "backend-python-agent"
VERSION = "1.0.0"

# Environment variables
GO_BFF_URL = os.environ.get("GO_BFF_URL", "http://localhost:8002")
REQUEST_TIMEOUT = float(os.environ.get("REQUEST_TIMEOUT_SECONDS", 2))
PORT = int(os.environ.get("PY_AGENT_PORT", 8000))


class PlanRequest(BaseModel):
    prompt: str = "Generate a 3-step plan to solve X."


# Middleware for structured JSON logging
@app.middleware("http")
async def log_requests(request: Request, call_next):
    start_time = time.time()
    response = await call_next(request)
    process_time = time.time() - start_time

    log_entry = {
        "timestamp": datetime.now().isoformat(),
        "level": "info",
        "service": SERVICE_NAME,
        "method": request.method,
        "path": request.url.path,
        "status": response.status_code,
        "latency_ms": round(process_time * 1000, 2),
        "request_id": request.headers.get("X-Request-Id", "none"),
    }
    print(json.dumps(log_entry))
    return response


@app.get("/health")
def health_check():
    return {"service": SERVICE_NAME, "status": "ok", "version": VERSION}


@app.post("/api/v1/plan")
async def create_agent_plan(request: Request, plan_request: PlanRequest):
    """Simulates agent planning. Calls Go BFF /echo to confirm wiring."""

    bff_echo_data = {}
    request_id = request.headers.get(
        "X-Request-Id", "generated-python-" + str(int(time.time()))
    )

    # 1. Call Go BFF /echo to confirm reverse wiring (non-recursive check)
    try:
        async with httpx.AsyncClient(timeout=REQUEST_TIMEOUT) as client:
            headers = {"X-Request-Id": request_id}
            response = await client.post(
                f"{GO_BFF_URL}/api/v1/echo",
                json={"ping": SERVICE_NAME, "request_id": request_id},
                headers=headers,
            )
            response.raise_for_status()
            bff_echo_data = response.json()
    except httpx.RequestError as e:
        bff_echo_data = {
            "error": f"BFF connection error: {e.__class__.__name__}",
            "url": f"{GO_BFF_URL}/api/v1/echo",
        }
    except httpx.HTTPStatusError as e:
        bff_echo_data = {
            "error": f"BFF HTTP error: {e.response.status_code}",
            "url": f"{GO_BFF_URL}/api/v1/echo",
        }

    # 2. Return plan payload
    return {
        "service": SERVICE_NAME,
        "status": "ok",
        "plan": {
            "steps": ["think", "call-tools", "summarize"],
            "prompt": plan_request.prompt,
        },
        "bff_echo": bff_echo_data,
        "request_id": request_id,
    }


if __name__ == "__main__":
    uvicorn.run("main:app", host="0.0.0.0", port=PORT, reload=False)


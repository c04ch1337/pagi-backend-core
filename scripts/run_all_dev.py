import subprocess
import os
import signal
import sys
import time
import json
import socket

# --- Configuration ---
PID_FILE = ".pids.json"
APP_VERSION = "0.0.1"


# Minimal .env parser
def load_env():
    env = {}
    try:
        with open(".env") as f:
            for line in f:
                if line.strip() and not line.startswith("#"):
                    key, value = line.strip().split("=", 1)
                    env[key] = value
    except FileNotFoundError:
        pass

    # Apply defaults if not set in .env
    env.setdefault("PY_AGENT_PORT", "8000")
    env.setdefault("RUST_SANDBOX_PORT", "8001")
    env.setdefault("GO_BFF_PORT", "8002")
    env.setdefault("MEMORY_MOCK_PORT", "8003")
    env.setdefault("MODEL_GATEWAY_GRPC_PORT", "50051")
    env.setdefault("PY_AGENT_URL", f"http://localhost:{env['PY_AGENT_PORT']}")
    env.setdefault("RUST_SANDBOX_URL", f"http://localhost:{env['RUST_SANDBOX_PORT']}")
    env.setdefault("GO_BFF_URL", f"http://localhost:{env['GO_BFF_PORT']}")
    env.setdefault("MEMORY_URL", f"http://localhost:{env['MEMORY_MOCK_PORT']}")
    return env


ENV = load_env()

SERVICES = [
    {
        "name": "Python Agent",
        "dir": "backend-python-agent",
        "cmd": [
            "uvicorn",
            "main:app",
            "--host",
            "127.0.0.1",
            "--port",
            ENV["PY_AGENT_PORT"],
        ],
        "port": ENV["PY_AGENT_PORT"],
        "health_url": f"http://localhost:{ENV['PY_AGENT_PORT']}/health",
    },
    {
        "name": "Rust Sandbox",
        "dir": "backend-rust-sandbox",
        "cmd": ["cargo", "run", "--", f"127.0.0.1:{ENV['RUST_SANDBOX_PORT']}",],
        "port": ENV["RUST_SANDBOX_PORT"],
        "health_url": f"http://localhost:{ENV['RUST_SANDBOX_PORT']}/health",
    },
    {
        "name": "Go BFF",
        "dir": "backend-go-bff",
        "cmd": ["go", "run", "."],
        "port": ENV["GO_BFF_PORT"],
        "health_url": f"http://localhost:{ENV['GO_BFF_PORT']}/health",
    },
    {
        "name": "Mock Memory",
        "dir": "scripts",
        "cmd": [
            "uvicorn",
            "mock_memory_service:app",
            "--host",
            "127.0.0.1",
            "--port",
            ENV["MEMORY_MOCK_PORT"],
        ],
        "port": ENV["MEMORY_MOCK_PORT"],
        "health_url": f"http://localhost:{ENV['MEMORY_MOCK_PORT']}/health",
    },
    {
        "name": "Go Model Gateway",
        "dir": "backend-go-model-gateway",
        "cmd": ["go", "run", "."],
        "port": ENV["MODEL_GATEWAY_GRPC_PORT"],
        "health_url": None,  # gRPC has no HTTP health
    },
]


def write_pids(pids):
    with open(PID_FILE, "w") as f:
        json.dump(pids, f)


def read_pids():
    try:
        with open(PID_FILE, "r") as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return []


def cleanup(signum=None, frame=None):
    """Gracefully terminates all running services via PID file."""

    pids = read_pids()
    if not pids:
        print("No running services found to stop.")
        return

    print("\n--- Initiating Cleanup (Stopping Services) ---")
    for name, pid in reversed(pids):
        try:
            os.kill(pid, signal.SIGTERM)
            print(f"[{name}] Sent SIGTERM to PID: {pid}")
        except ProcessLookupError:
            print(f"[{name}] PID {pid} not found (already terminated).")
        except Exception as e:
            print(f"[{name}] Error terminating PID {pid}: {e}")

    # Wait a moment for processes to stop
    time.sleep(2)

    # Attempt to kill any stragglers
    for name, pid in pids:
        try:
            os.kill(pid, signal.SIGKILL)
        except Exception:
            pass  # Ignore if already gone

    if os.path.exists(PID_FILE):
        os.remove(PID_FILE)
    print("Cleanup complete. All services stopped.")


def wait_for_health_check(url, timeout=30, interval=1):
    """Waits for a service's HTTP health check to pass."""

    start_time = time.time()
    print(f"  Waiting for health check at {url}...")
    while time.time() - start_time < timeout:
        try:
            with socket.create_connection(
                ("localhost", int(url.split(":")[-1].split("/")[0])), timeout=1
            ) as sock:
                sock.sendall(b"GET /health HTTP/1.0\r\nHost: localhost\r\n\r\n")
                response = sock.recv(1024).decode()
                if "200 OK" in response and '"status":"ok"' in response.lower():
                    return True
        except Exception:
            pass
        time.sleep(interval)
    return False


def start_services():
    running_pids = []

    # Pre-check for PID file existence
    if os.path.exists(PID_FILE):
        print(f"ERROR: Found existing {PID_FILE}. Run 'make stop-dev' first.")
        sys.exit(1)

    print("\n--- Starting Polygon AGI Backend Services (Bare Metal) ---")

    for service in SERVICES:
        print(f"[{service['name']}] Starting on port {service['port']}...")

        # Merge environment variables for subprocess
        service_env = os.environ.copy()
        for key, value in ENV.items():
            service_env[key] = value

        try:
            process = subprocess.Popen(
                service["cmd"],
                cwd=service["dir"],
                env=service_env,
                start_new_session=True,  # Important for cleanup
            )
            running_pids.append((service["name"], process.pid))
            print(f"[{service['name']}] PID: {process.pid}")
        except FileNotFoundError:
            print(
                f"[{service['name']} ERROR] Command not found (e.g., 'cargo', 'go', 'uvicorn'). Check installation."
            )
        except Exception as e:
            print(f"[{service['name']} ERROR] Failed to start: {e}")

    write_pids(running_pids)
    time.sleep(3)  # Give services a moment to bind

    # Health check loop
    print("\n--- Waiting for Services to become Healthy ---")
    healthy_count = 0
    all_healthy = True

    for name, pid in running_pids:
        service = next(s for s in SERVICES if s["name"] == name)
        if service["health_url"]:
            if wait_for_health_check(service["health_url"]):
                print(f"[{name}] Healthy.")
                healthy_count += 1
            else:
                print(f"[{name}] ERROR: Failed health check after 30s.")
                all_healthy = False
        else:
            print(f"[{name}] No HTTP health check (gRPC). Assuming started.")
            healthy_count += 1

    if all_healthy:
        print("\n--- All HTTP Services are Running! ---")
        print(
            f"Check Go BFF: http://localhost:{ENV['GO_BFF_PORT']}/api/v1/agi/dashboard-data"
        )
    else:
        print("\n--- WARNING: Some services failed to start. ---")

    # Keep script alive until cleanup is called
    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    signal.signal(signal.SIGINT, cleanup)
    signal.signal(signal.SIGTERM, cleanup)

    if len(sys.argv) > 1 and sys.argv[1] == "--stop":
        cleanup()
    else:
        start_services()


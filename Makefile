.PHONY: run-dev stop-dev test docker-up docker-down docker-generate

run-dev:
	python scripts/run_all_dev.py

stop-dev:
	python scripts/run_all_dev.py --stop

test:
	@echo "No tests yet. Add per-service tests as you implement logic."
	@echo "Suggested: Python pytest, Go test ./..., Rust cargo test."

docker-up:
	docker compose up --build

docker-down:
	docker compose down

docker-generate:
	# Utility target to generate gRPC code for bare metal testing
	go generate ./backend-go-model-gateway/...


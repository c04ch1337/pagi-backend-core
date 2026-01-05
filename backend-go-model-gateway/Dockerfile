# --- STAGE 1: BUILD ---
FROM golang:1.22 AS builder
WORKDIR /app

# Install protobuf compiler
RUN apt-get update && apt-get install -y protobuf-compiler

# Install Go tools for protobuf generation
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@latest \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Copy go mod and source
COPY go.mod ./go.mod
RUN go mod download

COPY main.go ./main.go
COPY proto ./proto

# Run code generation
RUN protoc --go_out=./proto --go_opt=paths=source_relative \
           --go-grpc_out=./proto --go-grpc_opt=paths=source_relative \
           proto/model.proto

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o /pagi-go-model-gateway

# --- STAGE 2: RUNTIME ---
FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=builder /pagi-go-model-gateway ./pagi-go-model-gateway
EXPOSE 50051
CMD ["/app/pagi-go-model-gateway"]


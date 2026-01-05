# --- STAGE 1: BUILD ---
FROM rust:1.78 AS builder
WORKDIR /app

# Copy dependency files
COPY Cargo.toml .
COPY src ./src

# Build the application
RUN cargo build --release

# --- STAGE 2: RUNTIME ---
FROM debian:bookworm-slim
WORKDIR /app

# Copy the built binary from the builder stage
COPY --from=builder /app/target/release/backend-rust-sandbox ./backend-rust-sandbox

# Expose the default port
EXPOSE 8001

# Run the final binary
CMD ["./backend-rust-sandbox"]


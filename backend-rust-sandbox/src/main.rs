use axum::{
    extract::Json,
    http::{HeaderMap, StatusCode},
    routing::{get, post},
    Router,
};
use serde::{Deserialize, Serialize};
use std::{env, net::SocketAddr};
use tracing::{info, Level};
use tracing_subscriber::{prelude::*, Registry};

const DEFAULT_PORT: u16 = 8001;
const SERVICE_NAME: &str = "backend-rust-sandbox";
const VERSION: &str = "1.0.0";

#[derive(Serialize)]
struct HealthResponse {
    service: &'static str,
    status: &'static str,
    version: &'static str,
}

#[derive(Serialize, Deserialize)]
struct ToolExecutionRequest {
    tool_name: String,
    code: Option<String>,
}

#[derive(Serialize)]
struct ToolExecutionResponse {
    tool_status: &'static str,
    result: i32,
}

async fn health_check() -> (StatusCode, Json<HealthResponse>) {
    (
        StatusCode::OK,
        Json(HealthResponse {
            service: SERVICE_NAME,
            status: "ok",
            version: VERSION,
        }),
    )
}

async fn execute_tool(
    headers: HeaderMap,
    Json(payload): Json<ToolExecutionRequest>,
) -> (StatusCode, Json<ToolExecutionResponse>) {
    let request_id = headers
        .get("x-request-id")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("none");

    // Log structured JSON request details
    info!(
        request_id = request_id,
        method = "POST",
        path = "/api/v1/execute_tool",
        tool_name = payload.tool_name,
        message = "Simulating secure tool execution."
    );

    // Placeholder: Simulate tool execution success
    (
        StatusCode::OK,
        Json(ToolExecutionResponse {
            tool_status: "executed",
            result: 42,
        }),
    )
}

fn init_logging(log_level: &str) {
    let level = log_level.parse::<Level>().unwrap_or(Level::INFO);

    let subscriber = Registry::default().with(
        tracing_subscriber::fmt::layer()
            .json()
            .with_current_span(false)
            .with_target(true)
            .with_level(true)
            .with_filter(
                tracing_subscriber::EnvFilter::from_default_env().add_directive(level.into()),
            ),
    );
    tracing::subscriber::set_global_default(subscriber)
        .expect("Unable to set global tracing subscriber");
}

#[tokio::main]
async fn main() {
    // Load .env for bare metal if needed
    dotenvy::dotenv().ok();

    let port_str = env::var("RUST_SANDBOX_PORT").unwrap_or_else(|_| DEFAULT_PORT.to_string());
    let log_level = env::var("LOG_LEVEL").unwrap_or_else(|_| "info".to_string());
    let port = port_str.parse::<u16>().unwrap_or(DEFAULT_PORT);

    init_logging(&log_level);

    // Bind to all interfaces so it works in Docker and bare metal.
    let addr = SocketAddr::from(([0, 0, 0, 0], port));
    info!(service = SERVICE_NAME, version = VERSION, port = port, message = "Starting server...");

    let app = Router::new()
        .route("/health", get(health_check))
        .route("/api/v1/execute_tool", post(execute_tool));

    let listener = tokio::net::TcpListener::bind(&addr).await.unwrap();
    axum::serve(listener, app).await.unwrap();
}


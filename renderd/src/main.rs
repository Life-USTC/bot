//! renderd: sandboxed HTTP rendering sidecar backed by typst.
//!
//! POST /render with a JSON `RenderEnvelope` (`{kind, scale?, payload}`)
//! returns PNG bytes plus `X-Image-Width` / `X-Image-Height` headers.
//! GET /healthz is a liveness probe that also reports whether fonts loaded
//! successfully.

mod cards;
mod escape;
mod request;
mod world;

use std::sync::OnceLock;
use std::time::Instant;

use axum::http::{header, HeaderMap, StatusCode};
use axum::response::IntoResponse;
use axum::routing::{get, post};
use axum::{Json, Router};

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "renderd=info".into()),
        )
        .init();

    // Warm the font cache at startup so the first request does not pay for
    // font discovery/parsing, and fail fast if no CJK font is available.
    if let Err(err) = world::fonts() {
        tracing::error!("font initialization failed: {err}");
        std::process::exit(1);
    }
    if let Err(err) = world::warm_assets() {
        tracing::error!("asset initialization failed: {err}");
        std::process::exit(1);
    }

    let app = Router::new()
        .route("/render", post(render_handler))
        .route("/healthz", get(healthz_handler));

    let addr = std::env::var("RENDERD_ADDR").unwrap_or_else(|_| "127.0.0.1:9123".into());
    let listener = tokio::net::TcpListener::bind(&addr)
        .await
        .expect("bind renderd address");
    tracing::info!(%addr, "renderd listening");
    axum::serve(listener, app).await.expect("serve renderd");
}

async fn healthz_handler() -> &'static str {
    "ok"
}

static RENDER_SEM: OnceLock<std::sync::Arc<tokio::sync::Semaphore>> = OnceLock::new();

async fn render_handler(
    Json(env): Json<request::RenderEnvelope>,
) -> Result<impl IntoResponse, (StatusCode, String)> {
    // Bound concurrent renders to the CPU core count; excess requests fail
    // fast with 429 instead of queueing unboundedly on the blocking pool.
    let sem = RENDER_SEM.get_or_init(|| {
        std::sync::Arc::new(tokio::sync::Semaphore::new(
            std::thread::available_parallelism().map(|n| n.get()).unwrap_or(4),
        ))
    });
    let permit = sem.clone().try_acquire_owned().map_err(|_| {
        (StatusCode::TOO_MANY_REQUESTS, "render queue full".to_string())
    })?;
    let started = Instant::now();
    // typst compilation + rasterization is CPU-bound and the `World` is
    // per-request, so run it on the blocking pool to keep the async runtime
    // responsive.
    let result = tokio::task::spawn_blocking(move || {
        let _permit = permit;
        cards::render_png(&env)
    })
    .await;

    let (png, width, height) = match result {
        Ok(Ok(out)) => out,
        Ok(Err(err)) => {
            tracing::warn!("render failed: {err:#}");
            return Err((StatusCode::UNPROCESSABLE_ENTITY, format!("{err:#}")));
        }
        Err(join_err) => {
            tracing::error!("render task panicked: {join_err}");
            return Err((StatusCode::INTERNAL_SERVER_ERROR, "render task failed".into()));
        }
    };

    let elapsed_ms = started.elapsed().as_secs_f64() * 1000.0;
    tracing::info!(elapsed_ms, width, height, "rendered");

    let mut headers = HeaderMap::new();
    headers.insert(header::CONTENT_TYPE, "image/png".parse().unwrap());
    headers.insert("x-image-width", width.to_string().parse().unwrap());
    headers.insert("x-image-height", height.to_string().parse().unwrap());
    headers.insert("x-render-ms", format!("{elapsed_ms:.2}").parse().unwrap());
    Ok((headers, png))
}

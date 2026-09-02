//! Render request envelope: every render call carries a `kind` discriminator
//! plus a kind-specific `payload`.

use serde::Deserialize;

#[derive(Debug, Deserialize)]
pub struct RenderEnvelope {
    pub kind: String,
    #[serde(default)]
    pub scale: Option<f32>,
    #[serde(default)]
    pub payload: serde_json::Value,
}

/// Resolve the rasterization scale: request override wins, then
/// RENDERD_SCALE, then 3.0. Clamped to [1.0, 4.0].
pub fn resolve_scale(req: Option<f32>) -> f32 {
    let from_env = || {
        std::env::var("RENDERD_SCALE")
            .ok()
            .and_then(|v| v.parse::<f32>().ok())
    };
    req.or_else(from_env).unwrap_or(3.0).clamp(1.0, 4.0)
}

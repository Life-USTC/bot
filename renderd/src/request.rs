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
            .filter(|v| v.is_finite())
    };
    req.filter(|v| v.is_finite())
        .or_else(from_env)
        .unwrap_or(3.0)
        .clamp(1.0, 4.0)
}

#[cfg(test)]
mod tests {
    use super::resolve_scale;

    #[test]
    fn request_scale_wins_and_is_clamped() {
        assert_eq!(resolve_scale(Some(2.5)), 2.5);
        assert_eq!(resolve_scale(Some(0.1)), 1.0);
        assert_eq!(resolve_scale(Some(9.0)), 4.0);
    }

    #[test]
    fn non_finite_request_uses_default() {
        assert_eq!(resolve_scale(Some(f32::NAN)), 3.0);
        assert_eq!(resolve_scale(Some(f32::INFINITY)), 3.0);
    }
}

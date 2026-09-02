//! Card dispatch: route a render envelope to the matching card renderer.

pub mod bus;

use crate::request::RenderEnvelope;
use anyhow::anyhow;

pub fn render_png(env: &RenderEnvelope) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let scale = crate::request::resolve_scale(env.scale);
    match env.kind.as_str() {
        "bus" => bus::render(&env.payload, scale),
        other => Err(anyhow!("unsupported kind: {other}")),
    }
}

//! Card dispatch: route a render envelope to the matching card renderer.

pub mod bus;
pub mod richtext;
pub mod weather;

use crate::request::RenderEnvelope;
use crate::world::SandboxWorld;
use anyhow::{anyhow, Context};

pub fn render_png(env: &RenderEnvelope) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let scale = crate::request::resolve_scale(env.scale);
    match env.kind.as_str() {
        "bus" => bus::render(&env.payload, scale),
        "rich" => richtext::render(&env.payload, scale),
        "weather" => weather::render(&env.payload, scale),
        other => Err(anyhow!("unsupported kind: {other}")),
    }
}

/// Compile a generated typst source and rasterize the first page to PNG.
/// Shared by every card renderer; the templates differ, the pipeline does
/// not.
pub(super) fn compile_png(source: String, scale: f32) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    crate::world::fonts().map_err(|e| anyhow!(e))?;

    let world = SandboxWorld::new(source.clone());
    let warned = typst::compile::<typst::layout::PagedDocument>(&world);
    let doc = warned.output.map_err(|errors| {
        use typst::World as _;
        let main = world.source(world.main()).ok();
        let detail = errors
            .iter()
            .map(|e| {
                let mut msg = e.message.to_string();
                if let Some(line) = main
                    .as_ref()
                    .and_then(|s| s.range(e.span))
                    .and_then(|range| main.as_ref().and_then(|s| s.byte_to_line(range.start)))
                {
                    msg.push_str(&format!(" (line {})", line + 1));
                }
                msg
            })
            .collect::<Vec<_>>()
            .join("; ");
        if std::env::var_os("RENDERD_DEBUG_SOURCE").is_some() {
            tracing::error!(source = %source, "typst compile failed");
        }
        anyhow!("typst compile failed: {detail}")
    })?;
    let page = doc.pages.first().context("compiled document has no pages")?;

    let pixmap = typst_render::render(page, scale);
    let png = pixmap.encode_png().context("png encode failed")?;
    Ok((png, pixmap.width(), pixmap.height()))
}

/// Format a JSON number as a typst numeric literal without a trailing `.0`.
pub(super) fn fmt_num(v: f64) -> String {
    if v.fract() == 0.0 {
        format!("{}", v as i64)
    } else {
        format!("{v}")
    }
}

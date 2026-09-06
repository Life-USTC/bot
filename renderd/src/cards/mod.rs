//! Card dispatch: route a render envelope to the matching card renderer.

pub mod bus;
pub mod grid;
pub mod richtext;
pub mod weather;

use crate::request::RenderEnvelope;
use crate::world::SandboxWorld;
use anyhow::{anyhow, Context};

pub(crate) const MAX_SOURCE_BYTES: usize = 8 * 1024 * 1024;
// Keep enough headroom for the largest normal scale-4 schedule while leaving
// a bounded memory envelope for the Typst compiler and pixmap allocation.
pub(crate) const MAX_RENDER_PIXELS: u64 = 24 * 1024 * 1024;
pub(crate) const MAX_RENDER_DIMENSION: u64 = 16_384;
pub(crate) const MAX_PNG_BYTES: usize = 32 * 1024 * 1024;
pub(crate) const MAX_TEXT_BYTES: usize = 4096;
pub(crate) const MAX_LABEL_BYTES: usize = 256;
pub(crate) const MAX_PAYLOAD_TEXT_BYTES: usize = 512 * 1024;

pub(crate) fn check_text(field: &str, value: &str, max_bytes: usize) -> anyhow::Result<()> {
    if value.len() > max_bytes {
        return Err(anyhow!(
            "{field} is too long ({} bytes > {max_bytes})",
            value.len()
        ));
    }
    Ok(())
}

/// Bound the total amount of user text handed to Typst. Per-field limits are
/// useful for diagnostics, but a request containing hundreds of individually
/// valid fields can still make layout disproportionately expensive.
pub(crate) fn check_text_budget(total: &mut usize, field: &str, value: &str) -> anyhow::Result<()> {
    *total = total
        .checked_add(value.len())
        .ok_or_else(|| anyhow!("{field} text budget overflowed"))?;
    if *total > MAX_PAYLOAD_TEXT_BYTES {
        return Err(anyhow!(
            "{field} exceeds the total text budget ({} bytes > {MAX_PAYLOAD_TEXT_BYTES})",
            *total
        ));
    }
    Ok(())
}

pub fn render_png(env: &RenderEnvelope) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let scale = crate::request::resolve_scale(env.scale);
    match env.kind.as_str() {
        "bus" => bus::render(&env.payload, scale),
        "grid" => grid::render(&env.payload, scale),
        "rich" => richtext::render(&env.payload, scale),
        "weather" => weather::render(&env.payload, scale),
        other => Err(anyhow!("unsupported kind: {other}")),
    }
}

/// Compile a generated Typst source and rasterize its single card to PNG.
/// Shared by every card renderer; the templates differ, the pipeline does
/// not.
pub(super) fn compile_png(source: String, scale: f32) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    if !scale.is_finite() || !(1.0..=4.0).contains(&scale) {
        return Err(anyhow!("render scale must be finite and between 1 and 4"));
    }
    if source.len() > MAX_SOURCE_BYTES {
        return Err(anyhow!(
            "generated typst source is too large ({} bytes > {MAX_SOURCE_BYTES})",
            source.len()
        ));
    }
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
    if doc.pages.len() != 1 {
        return Err(anyhow!(
            "card must contain exactly one page, got {}",
            doc.pages.len()
        ));
    }
    let page = doc
        .pages
        .first()
        .context("compiled document has no pages")?;

    let (expected_width, expected_height, _) = checked_pixel_dimensions(
        page.frame.size().x.to_pt(),
        page.frame.size().y.to_pt(),
        scale,
    )?;
    let pixmap = typst_render::render(page, scale);
    debug_assert_eq!(pixmap.width(), expected_width);
    debug_assert_eq!(pixmap.height(), expected_height);
    let png = pixmap.encode_png().context("png encode failed")?;
    if png.len() > MAX_PNG_BYTES {
        return Err(anyhow!(
            "encoded PNG is too large ({} bytes > {MAX_PNG_BYTES})",
            png.len()
        ));
    }
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

/// Check the dimensions used by `typst_render::render` before it allocates a
/// pixmap. The renderer itself unwraps that allocation, so this guard must run
/// on the logical page size rather than after rasterization.
fn checked_pixel_dimensions(
    logical_width: f64,
    logical_height: f64,
    scale: f32,
) -> anyhow::Result<(u32, u32, u64)> {
    if !logical_width.is_finite()
        || !logical_height.is_finite()
        || logical_width <= 0.0
        || logical_height <= 0.0
    {
        return Err(anyhow!(
            "rendered page dimensions must be finite and positive"
        ));
    }
    if !scale.is_finite() || scale <= 0.0 {
        return Err(anyhow!("render scale must be finite and positive"));
    }

    let width = (logical_width * f64::from(scale)).round().max(1.0);
    let height = (logical_height * f64::from(scale)).round().max(1.0);
    if !width.is_finite()
        || !height.is_finite()
        || width > u32::MAX as f64
        || height > u32::MAX as f64
    {
        return Err(anyhow!("rendered image dimensions are out of range"));
    }
    let width = width as u64;
    let height = height as u64;
    if width > MAX_RENDER_DIMENSION || height > MAX_RENDER_DIMENSION {
        return Err(anyhow!(
            "rendered image dimensions are too large: {width}x{height} (maximum {MAX_RENDER_DIMENSION})"
        ));
    }
    let pixels = width
        .checked_mul(height)
        .ok_or_else(|| anyhow!("rendered image pixel count overflowed"))?;
    if pixels > MAX_RENDER_PIXELS {
        return Err(anyhow!(
            "rendered image is too large ({pixels} pixels > {MAX_RENDER_PIXELS})"
        ));
    }
    Ok((width as u32, height as u32, pixels))
}

#[cfg(test)]
mod tests {
    use super::checked_pixel_dimensions;

    #[test]
    fn checks_normal_page_dimensions() {
        assert_eq!(
            checked_pixel_dimensions(390.0, 844.0, 3.0).unwrap(),
            (1170, 2532, 2_962_440)
        );
    }

    #[test]
    fn rejects_non_finite_and_oversized_pages() {
        assert!(checked_pixel_dimensions(f64::NAN, 10.0, 3.0).is_err());
        assert!(checked_pixel_dimensions(20_000.0, 1.0, 1.0).is_err());
        assert!(checked_pixel_dimensions(10_000.0, 10_000.0, 4.0).is_err());
    }

    #[test]
    fn rejects_multiple_pages_instead_of_losing_content() {
        let source = "#set page(width: 390pt, height: 200pt)\nFirst\n#pagebreak()\nSecond";
        let error = super::compile_png(source.into(), 1.0)
            .unwrap_err()
            .to_string();
        assert!(error.contains("exactly one page, got 2"), "{error}");
    }

    #[test]
    fn phone_screen_has_minimum_height_and_grows_without_pagination() {
        let source = |rows: usize| {
            format!(
                "#import \"common.typ\": *\n#show: card-page\n\
                 #card-header(\"课程安排\")\n\
                 #for _ in range({rows}) {{ block[课程名称与地点]; v(12pt) }}\n\
                 #card-footer((\"15:04 · 工作日\", \"Life @ USTC\"))"
            )
        };
        for scale in [1.0, 3.0] {
            let (_, width, height) = super::compile_png(source(2), scale).unwrap();
            assert_eq!((width, height), (390 * scale as u32, 844 * scale as u32));
        }
        let (_, width, height) = super::compile_png(source(48), 3.0).unwrap();
        assert_eq!(width, 1170);
        assert!(height > 2532, "long content must extend beyond one screen");
    }
}

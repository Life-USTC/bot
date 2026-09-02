//! Bus timetable card rendering: JSON payload -> typst source -> PNG.
//!
//! The payload mirrors the legacy Go `renderRichPNG` bus branch: the Go side
//! sends final geometry (content width, per-table column widths, table row
//! pairing) and this side only draws it.

use anyhow::{anyhow, Context};
use serde::Deserialize;

use crate::escape::typst_str;
use crate::world::SandboxWorld;

#[derive(Debug, Deserialize)]
pub struct BusPayload {
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub content_width: f64,
    #[serde(default)]
    pub next_time: Option<String>,
    #[serde(default)]
    pub next_wait: Option<String>,
    #[serde(default)]
    pub footer: Vec<String>,
    #[serde(default)]
    pub rows_of_tables: Vec<Vec<usize>>,
    #[serde(default)]
    pub tables: Vec<BusTable>,
}

#[derive(Debug, Deserialize)]
pub struct BusTable {
    #[serde(default)]
    pub label: String,
    #[serde(default)]
    pub header: Vec<String>,
    #[serde(default)]
    pub header_emphasis: Vec<bool>,
    #[serde(default)]
    pub column_widths: Vec<f64>,
    #[serde(default)]
    pub rows: Vec<BusRow>,
}

#[derive(Debug, Deserialize)]
pub struct BusRow {
    pub cells: Vec<String>,
    #[serde(default)]
    pub highlight: bool,
    #[serde(default)]
    pub departed: bool,
}

/// Render a payload to PNG. Returns `(png_bytes, width_px, height_px)`.
pub fn render(payload: &serde_json::Value, scale: f32) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let req: BusPayload = serde_json::from_value(payload.clone())
        .context("invalid bus payload")?;
    if req.tables.is_empty() {
        return Err(anyhow!("bus render requires at least one table"));
    }
    crate::world::fonts().map_err(|e| anyhow!(e))?;

    let source = build_source(&req);
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

/// Serialize the request into a typst dictionary literal.
fn data_literal(req: &BusPayload) -> String {
    let mut out = String::new();
    out.push('(');
    out.push_str(&format!("title: {}, ", typst_str(&req.title)));
    out.push_str(&format!("content_width: {}, ", fmt_num(req.content_width)));
    match (&req.next_time, &req.next_wait) {
        (Some(time), Some(wait)) => out.push_str(&format!(
            "next_time: {}, next_wait: {}, ",
            typst_str(time),
            typst_str(wait)
        )),
        _ => out.push_str("next_time: none, next_wait: none, "),
    }
    out.push_str("footer: (");
    for line in &req.footer {
        out.push_str(&typst_str(line));
        out.push_str(", ");
    }
    out.push_str("), rows_of_tables: (");
    for row in &req.rows_of_tables {
        out.push('(');
        for idx in row {
            out.push_str(&idx.to_string());
            out.push_str(", ");
        }
        out.push_str("), ");
    }
    out.push_str("), tables: (");
    for table in &req.tables {
        let ncols = table.header.len().max(1);
        out.push('(');
        out.push_str(&format!("label: {}, ", typst_str(&table.label)));
        out.push_str("header: (");
        for h in &table.header {
            out.push_str(&typst_str(h));
            out.push_str(", ");
        }
        out.push_str("), header_emphasis: (");
        for i in 0..ncols {
            let em = table.header_emphasis.get(i).copied().unwrap_or(false);
            out.push_str(if em { "true, " } else { "false, " });
        }
        out.push_str("), column_widths: (");
        for i in 0..ncols {
            let w = table.column_widths.get(i).copied().unwrap_or(0.0);
            out.push_str(&fmt_num(w));
            out.push_str(", ");
        }
        out.push_str("), rows: (");
        for row in &table.rows {
            out.push_str("(cells: (");
            for i in 0..ncols {
                // Pad missing cells so the typst table never wraps mid-row.
                let cell = row.cells.get(i).map(String::as_str).unwrap_or("");
                out.push_str(&typst_str(cell));
                out.push_str(", ");
            }
            out.push_str(&format!(
                "), highlight: {}, departed: {}), ",
                row.highlight, row.departed
            ));
        }
        out.push_str(")), ");
    }
    out.push_str("))");
    out
}

/// Format a JSON number as a typst numeric literal without a trailing `.0`.
fn fmt_num(v: f64) -> String {
    if v.fract() == 0.0 {
        format!("{}", v as i64)
    } else {
        format!("{v}")
    }
}

fn build_source(req: &BusPayload) -> String {
    TEMPLATE.replace("__DATA__", &data_literal(req))
}

const TEMPLATE: &str = include_str!("../templates/bus.typ");

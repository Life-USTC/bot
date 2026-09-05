//! Generic rich text card rendering: JSON payload -> typst source -> PNG.
//!
//! The payload mirrors the legacy Go `renderRichPNG` non-bus branch: the Go
//! side runs `layoutRichText` and sends final geometry (content width,
//! pre-wrapped text lines, per-table column widths); this side only draws.

use anyhow::{anyhow, Context};
use serde::Deserialize;

use crate::escape::typst_str;

#[derive(Debug, Deserialize)]
pub struct RichPayload {
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub content_width: f64,
    #[serde(default)]
    pub compact_first_block: bool,
    #[serde(default)]
    pub footer: Vec<String>,
    #[serde(default)]
    pub blocks: Vec<RichBlock>,
}

/// One layout node: either a text block (`lines`, already wrapped by the Go
/// side) or a table block.
#[derive(Debug, Deserialize)]
pub struct RichBlock {
    #[serde(default)]
    pub heading: String,
    #[serde(default)]
    pub lines: Option<Vec<String>>,
    #[serde(default)]
    pub wrapped: bool,
    #[serde(default)]
    pub table: Option<RichTable>,
}

#[derive(Debug, Deserialize)]
pub struct RichTable {
    #[serde(default)]
    pub header: Vec<String>,
    #[serde(default)]
    pub header_emphasis: Vec<bool>,
    #[serde(default)]
    pub column_widths: Vec<f64>,
    #[serde(default)]
    pub rows: Vec<RichRow>,
}

#[derive(Debug, Deserialize)]
pub struct RichRow {
    pub cells: Vec<String>,
    #[serde(default)]
    pub highlight: bool,
}

/// Render a payload to PNG. Returns `(png_bytes, width_px, height_px)`.
pub fn render(payload: &serde_json::Value, scale: f32) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let req: RichPayload =
        serde_json::from_value(payload.clone()).context("invalid rich payload")?;
    if req.blocks.is_empty() {
        return Err(anyhow!("rich render requires at least one block"));
    }
    let source = build_source(&req);
    super::compile_png(source, scale)
}

/// Serialize the request into a typst dictionary literal.
fn data_literal(req: &RichPayload) -> String {
    let mut out = String::new();
    out.push('(');
    out.push_str(&format!("title: {}, ", typst_str(&req.title)));
    out.push_str(&format!("content_width: {}, ", super::fmt_num(req.content_width)));
    out.push_str(&format!("compact_first_block: {}, ", req.compact_first_block));
    out.push_str("footer: (");
    for line in &req.footer {
        out.push_str(&typst_str(line));
        out.push_str(", ");
    }
    out.push_str("), blocks: (");
    for (i, block) in req.blocks.iter().enumerate() {
        let compact = req.compact_first_block && i == 0;
        out.push('(');
        out.push_str(&format!("heading: {}, ", typst_str(&block.heading)));
        out.push_str(&format!("compact: {compact}, "));
        match &block.table {
            Some(table) => {
                out.push_str("lines: (), wrapped: false, table: ");
                out.push_str(&table_literal(table));
            }
            None => {
                out.push_str("lines: (");
                for line in block.lines.iter().flatten() {
                    out.push_str(&typst_str(line));
                    out.push_str(", ");
                }
                out.push_str(&format!("), wrapped: {}, table: none, ", block.wrapped));
            }
        }
        out.push_str("), ");
    }
    out.push_str("))");
    out
}

fn table_literal(table: &RichTable) -> String {
    let ncols = table.header.len().max(1);
    let mut out = String::new();
    out.push('(');
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
        out.push_str(&super::fmt_num(w));
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
        out.push_str(&format!("), highlight: {}), ", row.highlight));
    }
    out.push_str("))");
    out
}

fn build_source(req: &RichPayload) -> String {
    TEMPLATE.replace("__DATA__", &data_literal(req))
}

const TEMPLATE: &str = include_str!("../templates/richtext.typ");

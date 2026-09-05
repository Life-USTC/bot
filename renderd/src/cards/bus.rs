//! Bus timetable card rendering: JSON payload -> typst source -> PNG.
//!
//! The payload mirrors the legacy Go `renderRichPNG` bus branch: the Go side
//! sends final geometry (content width, per-table column widths, table row
//! pairing) and this side only draws it.

use anyhow::{anyhow, Context};
use serde::Deserialize;

use crate::escape::typst_str;

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
    let source = build_source(&req);
    super::compile_png(source, scale)
}

/// Serialize the request into a typst dictionary literal.
fn data_literal(req: &BusPayload) -> String {
    let mut out = String::new();
    out.push('(');
    out.push_str(&format!("title: {}, ", typst_str(&req.title)));
    out.push_str(&format!("content_width: {}, ", super::fmt_num(req.content_width)));
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


fn build_source(req: &BusPayload) -> String {
    TEMPLATE.replace("__DATA__", &data_literal(req))
}

const TEMPLATE: &str = include_str!("../templates/bus.typ");

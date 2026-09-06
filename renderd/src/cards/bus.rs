//! Bus timetable card rendering: semantic JSON payload -> Typst -> PNG.
//!
//! The Go side supplies the route and timing semantics. Typst owns the
//! shared 390pt phone canvas, wrapping, and auto-height layout.

use anyhow::{anyhow, Context};
use serde::Deserialize;

use crate::escape::typst_str;

#[derive(Debug, Deserialize)]
pub struct BusPayload {
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub next_time: Option<String>,
    #[serde(default)]
    pub next_wait: Option<String>,
    #[serde(default)]
    pub footer: Vec<String>,
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
    pub rows: Vec<BusRow>,
}

#[derive(Debug, Deserialize)]
pub struct BusRow {
    #[serde(default)]
    pub cells: Vec<String>,
    #[serde(default)]
    pub highlight: bool,
    #[serde(default)]
    pub departed: bool,
}

const CARD_WIDTH: f64 = 390.0;
const CONTENT_WIDTH: f64 = 350.0;
const MAX_TABLES: usize = 32;
const MAX_COLUMNS: usize = 32;
const MAX_ROWS: usize = 512;
const MAX_RENDERED_ROWS: usize = 2048;
const MAX_TABLE_COLUMNS: usize = 4;
const CELL_PADDING: f64 = 10.0;
const BODY_SIZE: f64 = 17.0;
const BODY_LINE_HEIGHT: f64 = 28.0;
const TITLE_LINE_HEIGHT: f64 = 32.0;
const SECTION_LINE_HEIGHT: f64 = 26.0;
const SECTION_GAP: f64 = 24.0;
const HEADER_GAP: f64 = 8.0;
const FOOTER_HEIGHT: f64 = 80.0;

fn validate(req: &BusPayload) -> anyhow::Result<()> {
    let mut text_budget = 0usize;
    if req.title.trim().is_empty() {
        return Err(anyhow!("bus payload title is empty"));
    }
    super::check_text("title", &req.title, super::MAX_TEXT_BYTES)?;
    super::check_text_budget(&mut text_budget, "title", &req.title)?;
    if req.footer.len() > 2 {
        return Err(anyhow!("bus payload supports at most 2 footer lines"));
    }
    for (index, line) in req.footer.iter().enumerate() {
        super::check_text(&format!("footer[{index}]"), line, super::MAX_TEXT_BYTES)?;
        super::check_text_budget(&mut text_budget, &format!("footer[{index}]"), line)?;
    }
    if let Some(next_time) = &req.next_time {
        super::check_text("next_time", next_time, super::MAX_LABEL_BYTES)?;
        super::check_text_budget(&mut text_budget, "next_time", next_time)?;
    }
    if let Some(next_wait) = &req.next_wait {
        super::check_text("next_wait", next_wait, super::MAX_LABEL_BYTES)?;
        super::check_text_budget(&mut text_budget, "next_wait", next_wait)?;
    }
    if req.next_time.is_some() != req.next_wait.is_some() {
        return Err(anyhow!(
            "bus next_time and next_wait must be provided together"
        ));
    }
    if req.tables.is_empty() {
        return Err(anyhow!("bus render requires at least one table"));
    }
    if req.tables.len() > MAX_TABLES {
        return Err(anyhow!("bus payload has too many tables"));
    }

    let mut row_count = 0usize;
    for (table_index, table) in req.tables.iter().enumerate() {
        if table.header.is_empty() || table.header.len() > MAX_COLUMNS {
            return Err(anyhow!(
                "bus table {table_index} has an invalid column count"
            ));
        }
        if table.header_emphasis.len() > table.header.len() {
            return Err(anyhow!(
                "bus table {table_index} header emphasis has too many entries"
            ));
        }
        super::check_text(
            &format!("tables[{table_index}].label"),
            &table.label,
            super::MAX_TEXT_BYTES,
        )?;
        super::check_text_budget(
            &mut text_budget,
            &format!("tables[{table_index}].label"),
            &table.label,
        )?;
        for (column, header) in table.header.iter().enumerate() {
            super::check_text(
                &format!("tables[{table_index}].header[{column}]"),
                header,
                super::MAX_LABEL_BYTES,
            )?;
            super::check_text_budget(
                &mut text_budget,
                &format!("tables[{table_index}].header[{column}]"),
                header,
            )?;
        }
        if table.rows.len() > MAX_ROWS - row_count {
            return Err(anyhow!("bus payload has too many rows"));
        }
        row_count += table.rows.len();
        for (row_index, row) in table.rows.iter().enumerate() {
            if row.cells.len() > MAX_COLUMNS {
                return Err(anyhow!(
                    "bus table {table_index} row {row_index} has too many cells"
                ));
            }
            if row.cells.len() > table.header.len() {
                return Err(anyhow!(
                    "bus table {table_index} row {row_index} has more cells than its header"
                ));
            }
            for (column, cell) in row.cells.iter().enumerate() {
                super::check_text(
                    &format!("tables[{table_index}].rows[{row_index}].cells[{column}]"),
                    cell,
                    super::MAX_TEXT_BYTES,
                )?;
                super::check_text_budget(
                    &mut text_budget,
                    &format!("tables[{table_index}].rows[{row_index}].cells[{column}]"),
                    cell,
                )?;
            }
        }
    }
    if row_count > MAX_RENDERED_ROWS {
        return Err(anyhow!("bus payload expands to too many rendered rows"));
    }
    Ok(())
}

/// Render a payload to PNG. Returns `(png_bytes, width_px, height_px)`.
pub fn render(payload: &serde_json::Value, scale: f32) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let req: BusPayload = serde_json::from_value(payload.clone()).context("invalid bus payload")?;
    validate(&req)?;
    // This is a conservative bound used before compilation. The compiler and
    // rasterizer still validate the actual auto-height frame in compile_png.
    super::check_render_dimensions(CARD_WIDTH, logical_height(&req), scale)?;
    let source = build_source(&req);
    super::compile_png(source, scale)
}

fn estimated_lines(value: &str, width: f64) -> usize {
    let width = width.max(1.0);
    let mut lines = 1usize;
    let mut used = 0.0;
    for ch in value.chars() {
        let glyph = if ch.is_ascii() {
            BODY_SIZE * 0.62
        } else {
            BODY_SIZE
        };
        if used > 0.0 && used + glyph > width {
            lines += 1;
            used = glyph;
        } else {
            used += glyph;
        }
    }
    lines
}

fn standard_row_height(table: &BusTable, row: Option<&BusRow>) -> f64 {
    let columns = table.header.len().max(1) as f64;
    let cell_width = (CONTENT_WIDTH / columns - CELL_PADDING * 2.0).max(1.0);
    let line_count = row.map_or(1, |row| {
        (0..table.header.len())
            .map(|index| {
                row.cells
                    .get(index)
                    .map_or(1, |cell| estimated_lines(cell, cell_width))
            })
            .max()
            .unwrap_or(1)
    });
    line_count as f64 * BODY_LINE_HEIGHT + CELL_PADDING * 2.0
}

fn logical_height(req: &BusPayload) -> f64 {
    let title_lines = estimated_lines(&req.title, CONTENT_WIDTH);
    let mut height = title_lines as f64 * TITLE_LINE_HEIGHT + SECTION_GAP;
    if req.next_time.is_some() {
        let wait_width = CONTENT_WIDTH - CELL_PADDING * 2.0;
        let next_lines = estimated_lines(req.next_time.as_deref().unwrap_or(""), wait_width)
            + estimated_lines(req.next_wait.as_deref().unwrap_or(""), wait_width);
        height += next_lines as f64 * BODY_LINE_HEIGHT + CELL_PADDING * 2.0 + SECTION_GAP;
    }
    for (index, table) in req.tables.iter().enumerate() {
        if index > 0 {
            height += SECTION_GAP;
        }
        if !table.label.trim().is_empty() {
            height += SECTION_LINE_HEIGHT + HEADER_GAP;
        }
        height += standard_row_height(table, None);
        if table.header.len() > MAX_TABLE_COLUMNS {
            // The template reflows wide tables as two-column records. A
            // record has one row per field and uses a wider value column.
            let value_width = CONTENT_WIDTH * 2.0 / 3.0 - CELL_PADDING * 2.0;
            for row in &table.rows {
                for index in 0..table.header.len() {
                    let lines = row
                        .cells
                        .get(index)
                        .map_or(1, |cell| estimated_lines(cell, value_width));
                    height += lines as f64 * BODY_LINE_HEIGHT + CELL_PADDING * 2.0;
                }
            }
        } else {
            for row in &table.rows {
                height += standard_row_height(table, Some(row));
            }
        }
    }
    height + FOOTER_HEIGHT
}

/// Serialize the request into a Typst dictionary literal.
fn data_literal(req: &BusPayload) -> String {
    let mut out = String::new();
    out.push('(');
    out.push_str(&format!("title: {}, ", typst_str(&req.title)));
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
    out.push_str("), tables: (");
    for table in &req.tables {
        out.push('(');
        out.push_str(&format!(
            "label: {}, header: (",
            typst_str(table.label.trim())
        ));
        for header in &table.header {
            out.push_str(&typst_str(header));
            out.push_str(", ");
        }
        out.push_str("), header_emphasis: (");
        for index in 0..table.header.len() {
            out.push_str(
                if table.header_emphasis.get(index).copied().unwrap_or(false) {
                    "true, "
                } else {
                    "false, "
                },
            );
        }
        out.push_str("), rows: (");
        for row in &table.rows {
            out.push_str("(cells: (");
            for cell in &row.cells {
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

#[cfg(test)]
mod tests {
    use super::*;

    fn valid_payload() -> BusPayload {
        BusPayload {
            title: "校车 · 东区 → 西区".into(),
            next_time: Some("14:30".into()),
            next_wait: Some("还有 5 分钟".into()),
            footer: vec!["更新时间".into(), "Life @ USTC".into()],
            tables: vec![BusTable {
                label: "东区 → 西区".into(),
                header: vec!["东区".into(), "西区".into()],
                header_emphasis: vec![true, true],
                rows: vec![BusRow {
                    cells: vec!["14:30".into(), "14:40".into()],
                    highlight: true,
                    departed: false,
                }],
            }],
        }
    }

    #[test]
    fn accepts_valid_semantic_payload() {
        validate(&valid_payload()).unwrap();
    }

    #[test]
    fn rejects_inconsistent_semantic_limits() {
        let mut payload = valid_payload();
        payload.tables[0].rows[0].cells[0] = "x".repeat(super::super::MAX_TEXT_BYTES + 1);
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("too long"));

        let mut payload = valid_payload();
        payload.tables[0].rows[0].cells.push("extra".into());
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("more cells"));
    }

    #[test]
    fn rejects_unpaired_next_bus_fields() {
        let mut payload = valid_payload();
        payload.next_wait = None;
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("provided together"));
    }

    #[test]
    fn preserves_full_cells_and_departure_flags_in_source() {
        let mut payload = valid_payload();
        payload.tables[0].rows[0].cells[0] =
            "14:30 · 长标题不会被省略，也不会被替换成省略号".into();
        payload.tables[0].rows[0].departed = true;
        let source = build_source(&payload);
        assert!(source.contains("长标题不会被省略，也不会被替换成省略号"));
        assert!(source.contains("highlight: true, departed: true"));
        assert!(!source.contains("content_width:"));
        assert!(!source.contains("column_widths:"));
    }

    #[test]
    fn estimates_wrapping_height_for_long_cells() {
        let mut short = valid_payload();
        let short_height = logical_height(&short);
        short.tables[0].rows[0].cells[1] = "西区".repeat(40);
        assert!(logical_height(&short) > short_height);
    }

    #[test]
    fn renders_phone_width_and_wraps_long_content() {
        let payload = serde_json::json!({
            "title": "校车 东区 → 西区",
            "next_time": "14:30",
            "next_wait": "还有 5 分钟",
            "footer": ["13:00 · 工作日", "Life @ USTC"],
            "tables": [{
                "label": "东区 → 西区",
                "header": ["东区", "西区"],
                "header_emphasis": [true, true],
                "rows": [{
                    "cells": ["14:30", "14:40 · 这是一个需要在手机宽度上自然换行的完整到站说明文字 https://example.com/route/very-long-unbroken-id-20260906 ABC1234567890ABC1234567890ABC1234567890ABC1234567890"],
                    "highlight": true,
                    "departed": false
                }]
            }]
        });
        let (png, width, height) = render(&payload, 3.0).expect("bus template should compile");
        assert!(!png.is_empty());
        assert_eq!(width, 1170);
        assert!(height > 0);
    }
}

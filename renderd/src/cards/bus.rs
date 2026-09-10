//! Bus timetable card rendering: semantic JSON payload -> Typst -> PNG.
//!
//! The Go side supplies the route and timing semantics. Typst owns the
//! fixed-width paper canvas, wrapping, and auto-height layout.

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

const MAX_TABLES: usize = 32;
const MAX_COLUMNS: usize = 32;
const MAX_ROWS: usize = 512;
const MAX_RENDERED_ROWS: usize = 2048;
const MAX_TABLE_COLUMNS: usize = 4;

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
    let mut rendered_row_count = 0usize;
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
        let expanded_rows = table
            .rows
            .len()
            .checked_mul(if table.header.len() > MAX_TABLE_COLUMNS {
                table.header.len()
            } else {
                1
            })
            .ok_or_else(|| anyhow!("bus rendered row count overflowed"))?;
        if expanded_rows > MAX_RENDERED_ROWS.saturating_sub(rendered_row_count) {
            return Err(anyhow!("bus payload expands to too many rendered rows"));
        }
        rendered_row_count += expanded_rows;
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
    Ok(())
}

/// Render a payload to PNG. Returns `(png_bytes, width_px, height_px)`.
pub fn render(payload: &serde_json::Value, scale: f32) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let req: BusPayload = serde_json::from_value(payload.clone()).context("invalid bus payload")?;
    validate(&req)?;
    let source = build_source(&req);
    super::compile_png(source, scale)
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
    fn renders_content_width_and_wraps_long_content() {
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
        assert!(
            (super::super::SHEET_MIN_WIDTH_PT * 3..=super::super::SHEET_MAX_WIDTH_PT * 3)
                .contains(&width),
            "width {width} outside the sheet's range"
        );
        assert!(height > 0);
    }
    #[test]
    fn explicit_headings_and_long_text_take_layout_space() {
        let mut payload = valid_payload();
        payload.tables[0].label.clear();
        let (_, _, plain) = super::super::compile_png(build_source(&payload), 1.0).unwrap();
        payload.tables[0].label = "晚间加班车".into();
        let (_, _, headed) = super::super::compile_png(build_source(&payload), 1.0).unwrap();
        assert!(headed > plain, "explicit route headings must be drawn");
        payload.tables[0].rows[0].cells[0] = "高新校区到站说明".repeat(30);
        let (_, _, tall) = super::super::compile_png(build_source(&payload), 1.0).unwrap();
        assert!(tall > headed + 30, "long cell text must wrap naturally");
    }

    #[test]
    fn uneven_timetables_pack_without_reserving_empty_grid_rows() {
        let table = |rows: usize| {
            serde_json::json!({
                "header": ["东区", "西区", "先研院", "高新区"],
                "rows": (0..rows).map(|_| serde_json::json!({
                    "cells": ["08:00", "08:10", "", "09:00"]
                })).collect::<Vec<_>>()
            })
        };
        let payload = |tables| serde_json::json!({"title": "校车", "tables": tables});
        let (_, pair_width, pair_height) =
            render(&payload(vec![table(16), table(16)]), 1.0).unwrap();
        let (_, mixed_width, mixed_height) = render(
            &payload(vec![table(16), table(2), table(2), table(16)]),
            1.0,
        )
        .unwrap();
        assert_eq!(mixed_width, pair_width);
        // Four extra trips and two headers should add only their own space,
        // not another complete 16-trip row of the taller neighbouring table.
        assert!(
            mixed_height * 10 < pair_height * 16,
            "uneven tables reserve too much whitespace: pair={pair_height}, mixed={mixed_height}"
        );
    }
}

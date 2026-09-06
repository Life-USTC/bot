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

const MAX_TABLES: usize = 32;
const MAX_COLUMNS: usize = 32;
const MAX_ROWS: usize = 512;
const MAX_RENDERED_ROWS: usize = 2048;
const MAX_DIMENSION: f64 = 4096.0;
const MIN_CONTENT_WIDTH: f64 = 64.0;
const MIN_COLUMN_WIDTH: f64 = 16.0;
const TABLE_COLUMN_GAP: f64 = 20.0;
const CONTENT_TOP: f64 = 82.0;
const HEADER_HEIGHT: f64 = 28.0;
const ROW_HEIGHT: f64 = 32.0;
const ROW_GAP: f64 = 30.0;
const FOOTER_ZONE: f64 = 70.0;

fn validate(req: &BusPayload) -> anyhow::Result<()> {
    let mut text_budget = 0usize;
    if req.title.trim().is_empty() {
        return Err(anyhow!("bus payload title is empty"));
    }
    super::check_text("title", &req.title, super::MAX_TEXT_BYTES)?;
    super::check_text_budget(&mut text_budget, "title", &req.title)?;
    super::check_finite("content_width", req.content_width)?;
    if req.content_width < MIN_CONTENT_WIDTH || req.content_width > MAX_DIMENSION {
        return Err(anyhow!("bus content_width is out of range"));
    }
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
        if table.column_widths.len() != table.header.len() {
            return Err(anyhow!(
                "bus table {table_index} column widths do not match header"
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
            super::MAX_LABEL_BYTES,
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
        let mut width_sum = 0.0;
        for (column, width) in table.column_widths.iter().enumerate() {
            super::check_positive_dimension(
                &format!("tables[{table_index}].column_widths[{column}]"),
                *width,
                MAX_DIMENSION,
            )?;
            if *width < MIN_COLUMN_WIDTH {
                return Err(anyhow!(
                    "bus table {table_index} column {column} is narrower than {MIN_COLUMN_WIDTH}"
                ));
            }
            width_sum += *width;
        }
        if !width_sum.is_finite() || width_sum > req.content_width {
            return Err(anyhow!("bus table {table_index} width is too large"));
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
            for (column, cell) in row.cells.iter().enumerate() {
                if column >= table.header.len() {
                    return Err(anyhow!(
                        "bus table {table_index} row {row_index} has more cells than its header"
                    ));
                }
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
    if req.rows_of_tables.len() > MAX_TABLES {
        return Err(anyhow!("bus payload has too many table rows"));
    }
    if req.rows_of_tables.is_empty() {
        return Err(anyhow!("bus render requires at least one visual row"));
    }
    let mut referenced = vec![false; req.tables.len()];
    let mut rendered_rows = 0usize;
    for (row_index, row) in req.rows_of_tables.iter().enumerate() {
        if row.is_empty() {
            return Err(anyhow!("bus visual row {row_index} is empty"));
        }
        if row.len() > MAX_TABLES {
            return Err(anyhow!("bus table row {row_index} has too many tables"));
        }
        let mut row_width = 0.0;
        for table_index in row {
            if *table_index >= req.tables.len() {
                return Err(anyhow!("bus table index {table_index} is out of range"));
            }
            referenced[*table_index] = true;
            row_width += req.tables[*table_index].column_widths.iter().sum::<f64>();
            rendered_rows = rendered_rows
                .checked_add(req.tables[*table_index].rows.len())
                .ok_or_else(|| anyhow!("bus rendered row count overflowed"))?;
        }
        row_width += TABLE_COLUMN_GAP * (row.len().saturating_sub(1) as f64);
        if row_width > req.content_width {
            return Err(anyhow!(
                "bus visual row {row_index} is wider than content_width"
            ));
        }
    }
    if rendered_rows > MAX_RENDERED_ROWS {
        return Err(anyhow!("bus payload expands to too many rendered rows"));
    }
    if referenced.iter().any(|used| !used) {
        return Err(anyhow!("bus payload contains an unreferenced table"));
    }
    Ok(())
}

/// Render a payload to PNG. Returns `(png_bytes, width_px, height_px)`.
pub fn render(payload: &serde_json::Value, scale: f32) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let req: BusPayload = serde_json::from_value(payload.clone()).context("invalid bus payload")?;
    validate(&req)?;
    super::check_render_dimensions(req.content_width + 64.0, logical_height(&req), scale)?;
    let source = build_source(&req);
    super::compile_png(source, scale)
}

/// Compute the fixed-height portion of the bus template before compilation.
/// Rows may reference the same table more than once, so the calculation walks
/// the visual row grouping rather than only counting serialized tables.
fn logical_height(req: &BusPayload) -> f64 {
    let mut height = CONTENT_TOP;
    for (row_index, row) in req.rows_of_tables.iter().enumerate() {
        if row_index > 0 {
            height += ROW_GAP;
        }
        let row_height = row
            .iter()
            .map(|table_index| {
                let table = &req.tables[*table_index];
                let label_height = if table.label.trim().is_empty() {
                    0.0
                } else {
                    HEADER_HEIGHT
                };
                label_height + HEADER_HEIGHT + ROW_HEIGHT * table.rows.len() as f64
            })
            .fold(0.0, f64::max);
        height += row_height;
    }
    height + FOOTER_ZONE
}

/// Serialize the request into a typst dictionary literal.
fn data_literal(req: &BusPayload) -> String {
    let mut out = String::new();
    out.push('(');
    out.push_str(&format!("title: {}, ", typst_str(&req.title)));
    out.push_str(&format!(
        "content_width: {}, ",
        super::fmt_num(req.content_width)
    ));
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
        // Keep the label semantics in sync with logical_height and the
        // template: outer whitespace is layout-only and is not a label.
        out.push_str(&format!("label: {}, ", typst_str(table.label.trim())));
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

#[cfg(test)]
mod tests {
    use super::*;

    fn valid_payload() -> BusPayload {
        BusPayload {
            title: "校车 · 东区".into(),
            content_width: 640.0,
            next_time: Some("14:30".into()),
            next_wait: Some("还有 5 分钟".into()),
            footer: vec!["更新时间".into(), "Life @ USTC".into()],
            rows_of_tables: vec![vec![0]],
            tables: vec![BusTable {
                label: "东区 → 西区".into(),
                header: vec!["东区".into(), "西区".into()],
                header_emphasis: vec![false, false],
                column_widths: vec![120.0, 120.0],
                rows: vec![BusRow {
                    cells: vec!["14:30".into(), "14:40".into()],
                    highlight: false,
                    departed: false,
                }],
            }],
        }
    }

    #[test]
    fn accepts_valid_payload() {
        validate(&valid_payload()).unwrap();
    }

    #[test]
    fn rejects_inconsistent_table_geometry_and_indices() {
        let mut payload = valid_payload();
        payload.tables[0].column_widths.pop();
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("column widths"));

        let mut payload = valid_payload();
        payload.tables[0].column_widths = vec![f64::NAN, 120.0];
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("finite"));

        let mut payload = valid_payload();
        payload.rows_of_tables[0][0] = 9;
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("out of range"));
    }

    #[test]
    fn rejects_unpaired_next_bus_fields() {
        let mut payload = valid_payload();
        payload.next_wait = None;
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("provided together"));

        let mut payload = valid_payload();
        payload.next_time = None;
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("provided together"));
    }

    #[test]
    fn rejects_oversized_text() {
        let mut payload = valid_payload();
        payload.tables[0].rows[0].cells[0] = "x".repeat(super::super::MAX_TEXT_BYTES + 1);
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("too long"));
    }

    #[test]
    fn keeps_whitespace_only_labels_out_of_height() {
        let mut payload = valid_payload();
        payload.tables[0].label = "   ".into();
        let whitespace_height = logical_height(&payload);
        payload.tables[0].label.clear();
        assert_eq!(whitespace_height, logical_height(&payload));
        payload.tables[0].label = "   ".into();
        assert!(build_source(&payload).contains("label: \"\","));
    }

    #[test]
    fn rejects_empty_visual_rows_and_overlapping_geometry() {
        let mut payload = valid_payload();
        payload.rows_of_tables.clear();
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("visual row"));

        let mut payload = valid_payload();
        payload.content_width = 200.0;
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("width"));
    }

    #[test]
    fn bounds_expanded_duplicate_table_work() {
        let mut payload = valid_payload();
        payload.rows_of_tables = vec![vec![0, 0]];
        validate(&payload).unwrap();
    }
}

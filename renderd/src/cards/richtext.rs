//! Generic rich text card rendering: semantic JSON payload -> Typst -> PNG.
//!
//! The Go side preserves the parsed document. Typst owns the content-sized
//! paper canvas, natural paragraph wrapping, table sizing, and auto-height.

use anyhow::{anyhow, Context};
use serde::Deserialize;

use crate::escape::typst_str;

#[derive(Debug, Deserialize)]
pub struct RichPayload {
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub footer: Vec<String>,
    #[serde(default)]
    pub blocks: Vec<RichBlock>,
}

/// One document block: either text lines or a table. Text is deliberately
/// kept whole so Typst can wrap it at the content width.
#[derive(Debug, Deserialize)]
pub struct RichBlock {
    #[serde(default)]
    pub heading: String,
    #[serde(default)]
    pub lines: Vec<String>,
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
    pub rows: Vec<RichRow>,
}

#[derive(Debug, Deserialize)]
pub struct RichRow {
    #[serde(default)]
    pub cells: Vec<String>,
    #[serde(default)]
    pub highlight: bool,
}

const MAX_BLOCKS: usize = 64;
const MAX_COLUMNS: usize = 32;
const MAX_ROWS: usize = 512;
const MAX_LINES: usize = 512;
const MAX_RENDERED_ROWS: usize = 2048;
const MAX_TABLE_COLUMNS: usize = 4;

fn validate(req: &RichPayload) -> anyhow::Result<()> {
    let mut text_budget = 0usize;
    if req.title.trim().is_empty() {
        return Err(anyhow!("rich payload title is empty"));
    }
    super::check_text("title", &req.title, super::MAX_TEXT_BYTES)?;
    super::check_text_budget(&mut text_budget, "title", &req.title)?;
    if req.footer.len() > 2 {
        return Err(anyhow!("rich payload supports at most 2 footer lines"));
    }
    for (index, line) in req.footer.iter().enumerate() {
        super::check_text(&format!("footer[{index}]"), line, super::MAX_TEXT_BYTES)?;
        super::check_text_budget(&mut text_budget, &format!("footer[{index}]"), line)?;
    }
    if req.blocks.len() > MAX_BLOCKS {
        return Err(anyhow!("rich payload has too many blocks"));
    }

    let mut row_count = 0usize;
    let mut rendered_row_count = 0usize;
    for (block_index, block) in req.blocks.iter().enumerate() {
        super::check_text(
            &format!("blocks[{block_index}].heading"),
            &block.heading,
            super::MAX_TEXT_BYTES,
        )?;
        super::check_text_budget(
            &mut text_budget,
            &format!("blocks[{block_index}].heading"),
            &block.heading,
        )?;
        if block.table.is_some() && !block.lines.is_empty() {
            return Err(anyhow!(
                "rich block {block_index} cannot contain both table and lines"
            ));
        }
        if let Some(table) = &block.table {
            validate_table(
                table,
                block_index,
                &mut text_budget,
                &mut row_count,
                &mut rendered_row_count,
            )?;
        } else {
            if block.lines.len() > MAX_LINES {
                return Err(anyhow!("rich block {block_index} has too many lines"));
            }
            let has_line = block.lines.iter().any(|line| !line.trim().is_empty());
            if block.heading.trim().is_empty() && !has_line {
                return Err(anyhow!(
                    "rich block {block_index} has neither a heading nor text lines"
                ));
            }
            for (line_index, line) in block.lines.iter().enumerate() {
                super::check_text(
                    &format!("blocks[{block_index}].lines[{line_index}]"),
                    line,
                    super::MAX_TEXT_BYTES,
                )?;
                super::check_text_budget(
                    &mut text_budget,
                    &format!("blocks[{block_index}].lines[{line_index}]"),
                    line,
                )?;
            }
        }
    }
    if rendered_row_count > MAX_RENDERED_ROWS {
        return Err(anyhow!("rich payload expands to too many rendered rows"));
    }
    Ok(())
}

fn validate_table(
    table: &RichTable,
    block_index: usize,
    text_budget: &mut usize,
    row_count: &mut usize,
    rendered_row_count: &mut usize,
) -> anyhow::Result<()> {
    if table.header.is_empty() || table.header.len() > MAX_COLUMNS {
        return Err(anyhow!(
            "rich table {block_index} has an invalid column count"
        ));
    }
    if table.header_emphasis.len() > table.header.len() {
        return Err(anyhow!(
            "rich table {block_index} header emphasis has too many entries"
        ));
    }
    for (column, header) in table.header.iter().enumerate() {
        super::check_text(
            &format!("blocks[{block_index}].table.header[{column}]"),
            header,
            super::MAX_LABEL_BYTES,
        )?;
        super::check_text_budget(
            text_budget,
            &format!("blocks[{block_index}].table.header[{column}]"),
            header,
        )?;
    }
    if table.rows.len() > MAX_ROWS.saturating_sub(*row_count) {
        return Err(anyhow!("rich payload has too many rows"));
    }
    *row_count += table.rows.len();
    let expanded_rows = table
        .rows
        .len()
        .checked_mul(if table.header.len() > MAX_TABLE_COLUMNS {
            table.header.len()
        } else {
            1
        })
        .ok_or_else(|| anyhow!("rich rendered row count overflowed"))?;
    if expanded_rows > MAX_RENDERED_ROWS.saturating_sub(*rendered_row_count) {
        return Err(anyhow!("rich payload expands to too many rendered rows"));
    }
    *rendered_row_count += expanded_rows;
    for (row_index, row) in table.rows.iter().enumerate() {
        if row.cells.len() > MAX_COLUMNS {
            return Err(anyhow!(
                "rich table {block_index} row {row_index} has too many cells"
            ));
        }
        if row.cells.len() > table.header.len() {
            return Err(anyhow!(
                "rich table {block_index} row {row_index} has more cells than its header"
            ));
        }
        for (column, cell) in row.cells.iter().enumerate() {
            super::check_text(
                &format!("blocks[{block_index}].table.rows[{row_index}].cells[{column}]"),
                cell,
                super::MAX_TEXT_BYTES,
            )?;
            super::check_text_budget(
                text_budget,
                &format!("blocks[{block_index}].table.rows[{row_index}].cells[{column}]"),
                cell,
            )?;
        }
    }
    Ok(())
}

/// Render a payload to PNG. Returns `(png_bytes, width_px, height_px)`.
pub fn render(payload: &serde_json::Value, scale: f32) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let req: RichPayload =
        serde_json::from_value(payload.clone()).context("invalid rich payload")?;
    validate(&req)?;
    let source = build_source(&req);
    super::compile_png(source, scale)
}

/// Serialize the request into a Typst dictionary literal.
fn data_literal(req: &RichPayload) -> String {
    let mut out = String::new();
    out.push('(');
    out.push_str(&format!("title: {}, ", typst_str(&req.title)));
    out.push_str("footer: (");
    for line in &req.footer {
        out.push_str(&typst_str(line));
        out.push_str(", ");
    }
    out.push_str("), blocks: (");
    for block in &req.blocks {
        out.push_str(&format!(
            "(heading: {}, lines: (",
            typst_str(block.heading.trim())
        ));
        for line in &block.lines {
            out.push_str(&typst_str(line));
            out.push_str(", ");
        }
        out.push_str("), table: ");
        match &block.table {
            Some(table) => out.push_str(&table_literal(table)),
            None => out.push_str("none"),
        }
        out.push_str("), ");
    }
    out.push_str("))");
    out
}

fn table_literal(table: &RichTable) -> String {
    let mut out = String::new();
    out.push('(');
    out.push_str("header: (");
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
        out.push_str(&format!("), highlight: {}), ", row.highlight));
    }
    out.push_str("))");
    out
}

fn build_source(req: &RichPayload) -> String {
    TEMPLATE.replace("__DATA__", &data_literal(req))
}

const TEMPLATE: &str = include_str!("../templates/richtext.typ");

#[cfg(test)]
mod tests {
    use super::*;

    fn valid_payload() -> RichPayload {
        RichPayload {
            title: "帮助".into(),
            footer: vec!["更新时间".into(), "Life @ USTC".into()],
            blocks: vec![RichBlock {
                heading: "命令".into(),
                lines: vec![],
                table: Some(RichTable {
                    header: vec!["命令".into(), "说明".into()],
                    header_emphasis: vec![true, false],
                    rows: vec![RichRow {
                        cells: vec!["/help".into(), "查看帮助".into()],
                        highlight: false,
                    }],
                }),
            }],
        }
    }

    #[test]
    fn accepts_valid_semantic_table_payload() {
        validate(&valid_payload()).unwrap();
    }

    #[test]
    fn rejects_inconsistent_table_and_text_limits() {
        let mut payload = valid_payload();
        payload.blocks[0].table.as_mut().unwrap().rows[0].cells[0] =
            "x".repeat(super::super::MAX_TEXT_BYTES + 1);
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("too long"));

        let mut payload = valid_payload();
        payload.blocks[0].lines = vec!["text".into()];
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("both table and lines"));
    }

    #[test]
    fn preserves_full_text_and_removes_geometry_from_source() {
        let mut payload = valid_payload();
        payload.blocks[0].table = None;
        payload.blocks[0].lines = vec![
            "这是一个完整的长段落，不应该由 Go 预先拆分或省略。".into(),
            "https://example.com/a/very-long-unbroken-id-20260906-with-all-content".into(),
        ];
        let source = build_source(&payload);
        assert!(source.contains("完整的长段落，不应该由 Go 预先拆分或省略"));
        assert!(source.contains("very-long-unbroken-id-20260906-with-all-content"));
        assert!(!source.contains("content_width:"));
        assert!(!source.contains("column_widths:"));
        assert!(!source.contains("wrapped:"));
    }

    #[test]
    fn rejects_page_that_exceeds_actual_output_bound() {
        let payload = serde_json::json!({
            "title": "stress",
            "blocks": [{"lines": vec!["line"; 512]}]
        });
        let err = render(&payload, 3.0).unwrap_err().to_string();
        assert!(
            err.contains("rendered image dimensions are too large")
                || err.contains("card must contain exactly one page"),
            "{err}"
        );
    }

    #[test]
    fn reflows_wide_tables_without_dropping_values() {
        let mut payload = valid_payload();
        payload.blocks[0].table = Some(RichTable {
            header: vec![
                "一".into(),
                "二".into(),
                "三".into(),
                "四".into(),
                "五".into(),
            ],
            header_emphasis: vec![false; 5],
            rows: vec![RichRow {
                cells: vec![
                    "one".into(),
                    "two".into(),
                    "three".into(),
                    "four".into(),
                    "a-complete-long-value-that-must-stay-readable".into(),
                ],
                highlight: true,
            }],
        });
        let source = build_source(&payload);
        for value in [
            "one",
            "two",
            "three",
            "four",
            "a-complete-long-value-that-must-stay-readable",
        ] {
            assert!(source.contains(value), "missing {value}");
        }
    }

    #[test]
    fn renders_content_width_and_wraps_long_content() {
        let payload = serde_json::json!({
            "title": "一个很长的手机卡片标题，也应该完整换行显示",
            "footer": ["13:00 · 工作日", "Life @ USTC"],
            "blocks": [{
                "heading": "说明",
                "lines": ["这是一段足够长的正文，用来验证 Typst 会在 350pt 内容宽度内自动换行，并保留完整内容。 https://example.com/a/very-long-unbroken-id-20260906-with-all-content ABC1234567890ABC1234567890ABC1234567890ABC1234567890"],
                "table": null
            }]
        });
        let (png, width, height) = render(&payload, 3.0).expect("rich template should compile");
        assert!(!png.is_empty());
        assert!(
            (super::super::SHEET_MIN_WIDTH_PT * 3..=super::super::SHEET_MAX_WIDTH_PT * 3)
                .contains(&width),
            "width {width} outside the sheet's range"
        );
        assert!(height > 0);
    }
}

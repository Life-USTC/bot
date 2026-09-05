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

const MAX_BLOCKS: usize = 64;
const MAX_COLUMNS: usize = 32;
const MAX_ROWS: usize = 512;
const MAX_LINES: usize = 512;
const MAX_DIMENSION: f64 = 4096.0;
const MIN_CONTENT_WIDTH: f64 = 64.0;
const MIN_COLUMN_WIDTH: f64 = 16.0;
const FOOTER_ZONE: f64 = 70.0;
const CONTENT_TOP: f64 = 82.0;
const COMPACT_CONTENT_TOP: f64 = 58.0;
const BLOCK_GAP: f64 = 30.0;
const HEADER_HEIGHT: f64 = 28.0;
const TEXT_ROW_HEIGHT: f64 = 32.0;
const COMPACT_TEXT_ROW_HEIGHT: f64 = 24.0;

fn validate(req: &RichPayload) -> anyhow::Result<()> {
    let mut text_budget = 0usize;
    if req.title.trim().is_empty() {
        return Err(anyhow!("rich payload title is empty"));
    }
    super::check_text("title", &req.title, super::MAX_TEXT_BYTES)?;
    super::check_text_budget(&mut text_budget, "title", &req.title)?;
    super::check_finite("content_width", req.content_width)?;
    if req.content_width < MIN_CONTENT_WIDTH || req.content_width > MAX_DIMENSION {
        return Err(anyhow!("rich content_width is out of range"));
    }
    if req.footer.len() > 2 {
        return Err(anyhow!("rich payload supports at most 2 footer lines"));
    }
    for (index, line) in req.footer.iter().enumerate() {
        super::check_text(&format!("footer[{index}]"), line, super::MAX_TEXT_BYTES)?;
        super::check_text_budget(&mut text_budget, &format!("footer[{index}]"), line)?;
    }
    // A title-only document is still a valid legacy card: it renders the title
    // and footer even when there are no content blocks.
    if req.blocks.len() > MAX_BLOCKS {
        return Err(anyhow!("rich payload has too many blocks"));
    }
    let mut row_count = 0usize;
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
        if block.table.is_some() && block.lines.is_some() {
            return Err(anyhow!(
                "rich block {block_index} cannot contain both table and lines"
            ));
        }
        if let Some(table) = &block.table {
            if table.header.is_empty() || table.header.len() > MAX_COLUMNS {
                return Err(anyhow!(
                    "rich table {block_index} has an invalid column count"
                ));
            }
            if table.column_widths.len() != table.header.len() {
                return Err(anyhow!(
                    "rich table {block_index} column widths do not match header"
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
                    &mut text_budget,
                    &format!("blocks[{block_index}].table.header[{column}]"),
                    header,
                )?;
            }
            let mut width_sum = 0.0;
            for (column, width) in table.column_widths.iter().enumerate() {
                super::check_positive_dimension(
                    &format!("blocks[{block_index}].table.column_widths[{column}]"),
                    *width,
                    MAX_DIMENSION,
                )?;
                if *width < MIN_COLUMN_WIDTH {
                    return Err(anyhow!(
                        "rich table {block_index} column {column} is narrower than {MIN_COLUMN_WIDTH}"
                    ));
                }
                width_sum += *width;
            }
            if !width_sum.is_finite() || width_sum > req.content_width {
                return Err(anyhow!("rich table {block_index} width is too large"));
            }
            if table.rows.len() > MAX_ROWS - row_count {
                return Err(anyhow!("rich payload has too many rows"));
            }
            row_count += table.rows.len();
            for (row_index, row) in table.rows.iter().enumerate() {
                if row.cells.len() > MAX_COLUMNS {
                    return Err(anyhow!(
                        "rich table {block_index} row {row_index} has too many cells"
                    ));
                }
                for (column, cell) in row.cells.iter().enumerate() {
                    if column >= table.header.len() {
                        return Err(anyhow!(
                            "rich table {block_index} row {row_index} has more cells than its header"
                        ));
                    }
                    super::check_text(
                        &format!("blocks[{block_index}].table.rows[{row_index}].cells[{column}]"),
                        cell,
                        super::MAX_TEXT_BYTES,
                    )?;
                    super::check_text_budget(
                        &mut text_budget,
                        &format!("blocks[{block_index}].table.rows[{row_index}].cells[{column}]"),
                        cell,
                    )?;
                }
            }
        } else {
            let lines = block.lines.as_deref().unwrap_or(&[]);
            if lines.len() > MAX_LINES {
                return Err(anyhow!("rich block {block_index} has too many lines"));
            }
            let has_line = lines.iter().any(|line| !line.trim().is_empty());
            if block.heading.trim().is_empty() && !has_line {
                return Err(anyhow!(
                    "rich block {block_index} has neither a heading nor text lines"
                ));
            }
            for (line_index, line) in lines.iter().enumerate() {
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
    Ok(())
}

/// Render a payload to PNG. Returns `(png_bytes, width_px, height_px)`.
pub fn render(payload: &serde_json::Value, scale: f32) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let req: RichPayload =
        serde_json::from_value(payload.clone()).context("invalid rich payload")?;
    validate(&req)?;
    super::check_render_dimensions(req.content_width + 64.0, logical_height(&req), scale)?;
    let source = build_source(&req);
    super::compile_png(source, scale)
}

/// Compute the auto-height used by the rich template from its fixed rows. The
/// Go side sends already-wrapped lines, so this is an upper bound on the page
/// height and can be checked before invoking Typst.
fn logical_height(req: &RichPayload) -> f64 {
    let mut height = if req.compact_first_block {
        COMPACT_CONTENT_TOP
    } else {
        CONTENT_TOP
    };
    for (index, block) in req.blocks.iter().enumerate() {
        if index > 0 {
            height += BLOCK_GAP;
        }
        let block_height = if let Some(table) = &block.table {
            let heading = if block.heading.trim().is_empty() {
                0.0
            } else {
                HEADER_HEIGHT
            };
            heading + HEADER_HEIGHT + TEXT_ROW_HEIGHT * table.rows.len() as f64
        } else {
            let heading = if block.heading.trim().is_empty() {
                0.0
            } else {
                HEADER_HEIGHT
            };
            let row_height = if req.compact_first_block && index == 0 {
                COMPACT_TEXT_ROW_HEIGHT
            } else {
                TEXT_ROW_HEIGHT
            };
            heading + row_height * block.lines.as_ref().map_or(0, Vec::len) as f64
        };
        height += block_height;
    }
    height + FOOTER_ZONE
}

/// Serialize the request into a typst dictionary literal.
fn data_literal(req: &RichPayload) -> String {
    let mut out = String::new();
    out.push('(');
    out.push_str(&format!("title: {}, ", typst_str(&req.title)));
    out.push_str(&format!(
        "content_width: {}, ",
        super::fmt_num(req.content_width)
    ));
    out.push_str(&format!(
        "compact_first_block: {}, ",
        req.compact_first_block
    ));
    out.push_str("footer: (");
    for line in &req.footer {
        out.push_str(&typst_str(line));
        out.push_str(", ");
    }
    out.push_str("), blocks: (");
    for (i, block) in req.blocks.iter().enumerate() {
        let compact = req.compact_first_block && i == 0;
        out.push('(');
        // Normalize outer whitespace so the heading presence check and its
        // fixed-height contribution match the Typst template exactly.
        out.push_str(&format!("heading: {}, ", typst_str(block.heading.trim())));
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

#[cfg(test)]
mod tests {
    use super::*;

    fn valid_payload() -> RichPayload {
        RichPayload {
            title: "帮助".into(),
            content_width: 640.0,
            compact_first_block: false,
            footer: vec!["更新时间".into(), "Life @ USTC".into()],
            blocks: vec![RichBlock {
                heading: "命令".into(),
                lines: None,
                wrapped: false,
                table: Some(RichTable {
                    header: vec!["命令".into(), "说明".into()],
                    header_emphasis: vec![true, false],
                    column_widths: vec![160.0, 320.0],
                    rows: vec![RichRow {
                        cells: vec!["/help".into(), "查看帮助".into()],
                        highlight: false,
                    }],
                }),
            }],
        }
    }

    #[test]
    fn accepts_valid_table_payload() {
        validate(&valid_payload()).unwrap();
    }

    #[test]
    fn rejects_inconsistent_table_and_text_limits() {
        let mut payload = valid_payload();
        payload.blocks[0]
            .table
            .as_mut()
            .unwrap()
            .column_widths
            .pop();
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("column widths"));

        let mut payload = valid_payload();
        payload.blocks[0].table = None;
        payload.blocks[0].lines = Some(vec!["x".repeat(super::super::MAX_TEXT_BYTES + 1)]);
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("too long"));

        let mut payload = valid_payload();
        payload.content_width = f64::INFINITY;
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("finite"));
    }

    #[test]
    fn rejects_tall_page_before_typst_compilation() {
        let lines = vec!["line".to_string(); 200];
        let payload = serde_json::json!({
            "title": "stress",
            "content_width": 640,
            "blocks": [{"lines": lines}],
        });
        let err = render(&payload, 3.0).unwrap_err().to_string();
        assert!(
            err.contains("rendered image dimensions are too large"),
            "{err}"
        );
    }

    #[test]
    fn rejects_ambiguous_or_empty_blocks() {
        let mut payload = valid_payload();
        payload.blocks[0].lines = Some(vec![]);
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("both table and lines"));

        let empty = RichPayload {
            title: "帮助".into(),
            content_width: 640.0,
            compact_first_block: false,
            footer: vec![],
            blocks: vec![RichBlock {
                heading: String::new(),
                lines: Some(vec!["   ".into()]),
                wrapped: true,
                table: None,
            }],
        };
        assert!(validate(&empty)
            .unwrap_err()
            .to_string()
            .contains("neither a heading"));
    }

    #[test]
    fn accepts_heading_only_block_and_rejects_overwide_table() {
        let heading_only = RichPayload {
            title: "帮助".into(),
            content_width: 640.0,
            compact_first_block: false,
            footer: vec![],
            blocks: vec![RichBlock {
                heading: "说明".into(),
                lines: None,
                wrapped: false,
                table: None,
            }],
        };
        validate(&heading_only).unwrap();

        let mut overwide = valid_payload();
        overwide.content_width = 100.0;
        assert!(validate(&overwide)
            .unwrap_err()
            .to_string()
            .contains("width is too large"));
    }

    #[test]
    fn accepts_title_only_payload() {
        let mut payload = valid_payload();
        payload.blocks.clear();
        validate(&payload).unwrap();
        assert!(build_source(&payload).contains("blocks: ())"));
        assert_eq!(logical_height(&payload), CONTENT_TOP + FOOTER_ZONE);
    }

    #[test]
    fn compiles_title_only_payload() {
        let payload = serde_json::json!({
            "title": "标题",
            "content_width": 480,
            "footer": ["13:00 · 工作日", "Life @ USTC"],
            "blocks": []
        });
        let (png, width, height) = render(&payload, 1.0).unwrap();
        assert!(!png.is_empty());
        assert_eq!((width, height), (544, 152));
    }
}

//! Schedule-grid rendering: JSON payload -> fixed-position Typst source -> PNG.
//!
//! All schedule semantics (today selection, course colors, bounds, text
//! fitting, and font-size choices) are resolved by the Go caller.  This
//! renderer only paints the rectangles and strings it receives.

use anyhow::{anyhow, Context};
use serde::Deserialize;

use crate::escape::typst_str;

#[derive(Debug, Deserialize)]
pub struct GridPayload {
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub days: Vec<GridDay>,
    #[serde(default)]
    pub periods: Vec<GridPeriod>,
    #[serde(default)]
    pub day_width: f64,
    #[serde(default)]
    pub label_width: f64,
    #[serde(default)]
    pub row_height: f64,
    #[serde(default)]
    pub header_height: f64,
    #[serde(default)]
    pub dividers: Vec<i64>,
    #[serde(default)]
    pub items: Vec<GridItem>,
    #[serde(default)]
    pub footer: Vec<String>,
}

#[derive(Debug, Deserialize)]
pub struct GridDay {
    #[serde(default)]
    pub label: String,
    #[serde(default)]
    pub date: String,
    #[serde(default)]
    pub today: bool,
}

#[derive(Debug, Deserialize)]
pub struct GridPeriod {
    #[serde(default)]
    pub label: String,
    #[serde(default)]
    pub time: String,
}

#[derive(Debug, Deserialize)]
pub struct GridItem {
    pub day: i64,
    pub start: i64,
    pub end: i64,
    #[serde(default)]
    pub course: String,
    #[serde(default)]
    pub location: String,
    #[serde(default)]
    pub weeks: String,
    #[serde(default)]
    pub color: String,
    #[serde(default)]
    pub large: bool,
    /// Explicit sizes preserve the legacy renderer's mixed case where a
    /// merged block can use a compact course face but a large metadata face.
    #[serde(default)]
    pub course_size: f64,
    #[serde(default)]
    pub meta_size: f64,
}

const MAX_DAYS: usize = 14;
const MAX_PERIODS: usize = 64;
const MAX_ITEMS: usize = 512;
const MAX_DIVIDERS: usize = 64;
const MAX_DIMENSION: f64 = 4096.0;
const MAX_FONT_SIZE: f64 = 72.0;
const MARGIN: f64 = 36.0;
const GRID_TOP: f64 = 82.0;
const FOOTER_GAP: f64 = 28.0;
const BOTTOM_MARGIN: f64 = 28.0;

/// Render a grid payload to PNG. Returns `(png_bytes, width_px, height_px)`.
pub fn render(payload: &serde_json::Value, scale: f32) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let req: GridPayload =
        serde_json::from_value(payload.clone()).context("invalid grid payload")?;
    validate(&req)?;
    let logical_width = 2.0 * MARGIN + req.label_width + req.days.len() as f64 * req.day_width;
    let logical_height = GRID_TOP
        + req.header_height
        + req.periods.len() as f64 * req.row_height
        + FOOTER_GAP
        + BOTTOM_MARGIN;
    super::check_render_dimensions(logical_width, logical_height, scale)?;
    let source = build_source(&req);
    super::compile_png(source, scale)
}

fn validate(req: &GridPayload) -> anyhow::Result<()> {
    let mut text_budget = 0usize;
    if req.days.is_empty() || req.periods.is_empty() {
        return Err(anyhow!("grid render requires at least one day and period"));
    }
    if req.days.len() > MAX_DAYS {
        return Err(anyhow!("grid payload has too many days"));
    }
    if req.periods.len() > MAX_PERIODS {
        return Err(anyhow!("grid payload has too many periods"));
    }
    if req.items.len() > MAX_ITEMS {
        return Err(anyhow!("grid payload has too many items"));
    }
    if req.dividers.len() > MAX_DIVIDERS {
        return Err(anyhow!("grid payload has too many dividers"));
    }
    super::check_text("title", &req.title, super::MAX_TEXT_BYTES)?;
    super::check_text_budget(&mut text_budget, "title", &req.title)?;
    super::check_text("summary", &req.summary, super::MAX_TEXT_BYTES)?;
    super::check_text_budget(&mut text_budget, "summary", &req.summary)?;
    if req.footer.len() > 2 {
        return Err(anyhow!("grid payload supports at most 2 footer lines"));
    }
    for (index, line) in req.footer.iter().enumerate() {
        super::check_text(&format!("footer[{index}]"), line, super::MAX_TEXT_BYTES)?;
        super::check_text_budget(&mut text_budget, &format!("footer[{index}]"), line)?;
    }
    for (name, value) in [
        ("day_width", req.day_width),
        ("label_width", req.label_width),
        ("row_height", req.row_height),
        ("header_height", req.header_height),
    ] {
        super::check_positive_dimension(name, value, MAX_DIMENSION)?;
    }
    for (index, day) in req.days.iter().enumerate() {
        super::check_text(
            &format!("days[{index}].label"),
            &day.label,
            super::MAX_LABEL_BYTES,
        )?;
        super::check_text_budget(
            &mut text_budget,
            &format!("days[{index}].label"),
            &day.label,
        )?;
        super::check_text(
            &format!("days[{index}].date"),
            &day.date,
            super::MAX_LABEL_BYTES,
        )?;
        super::check_text_budget(&mut text_budget, &format!("days[{index}].date"), &day.date)?;
    }
    for (index, period) in req.periods.iter().enumerate() {
        super::check_text(
            &format!("periods[{index}].label"),
            &period.label,
            super::MAX_LABEL_BYTES,
        )?;
        super::check_text_budget(
            &mut text_budget,
            &format!("periods[{index}].label"),
            &period.label,
        )?;
        super::check_text(
            &format!("periods[{index}].time"),
            &period.time,
            super::MAX_LABEL_BYTES,
        )?;
        super::check_text_budget(
            &mut text_budget,
            &format!("periods[{index}].time"),
            &period.time,
        )?;
    }
    let day_count = req.days.len() as i64;
    let period_count = req.periods.len() as i64;
    for (index, item) in req.items.iter().enumerate() {
        if item.day < 0
            || item.day >= day_count
            || item.start < 1
            || item.end < item.start
            || item.end > period_count
        {
            return Err(anyhow!(
                "grid item bounds out of range: day={} start={} end={}",
                item.day,
                item.start,
                item.end
            ));
        }
        super::check_text(
            &format!("items[{index}].course"),
            &item.course,
            super::MAX_TEXT_BYTES,
        )?;
        super::check_text_budget(
            &mut text_budget,
            &format!("items[{index}].course"),
            &item.course,
        )?;
        super::check_text(
            &format!("items[{index}].location"),
            &item.location,
            super::MAX_LABEL_BYTES,
        )?;
        super::check_text_budget(
            &mut text_budget,
            &format!("items[{index}].location"),
            &item.location,
        )?;
        super::check_text(
            &format!("items[{index}].weeks"),
            &item.weeks,
            super::MAX_LABEL_BYTES,
        )?;
        super::check_text_budget(
            &mut text_budget,
            &format!("items[{index}].weeks"),
            &item.weeks,
        )?;
        if !item.color.is_empty()
            && (item.color.len() != 7
                || !item.color.starts_with('#')
                || !item.color[1..].bytes().all(|byte| byte.is_ascii_hexdigit()))
        {
            return Err(anyhow!("grid item color must be a #rrggbb value"));
        }
        for (name, value) in [
            ("course_size", item.course_size),
            ("meta_size", item.meta_size),
        ] {
            if !value.is_finite() || value < 0.0 || value > MAX_FONT_SIZE {
                return Err(anyhow!(
                    "grid item {name} must be finite and in the range [0, {MAX_FONT_SIZE}]"
                ));
            }
        }
    }
    for divider in &req.dividers {
        if *divider <= 0 || *divider >= period_count {
            return Err(anyhow!("grid divider out of range: {divider}"));
        }
    }
    Ok(())
}

/// Serialize the request into a Typst dictionary literal. Every user-facing
/// string is escaped before being embedded in generated source.
fn data_literal(req: &GridPayload) -> String {
    let mut out = String::new();
    out.push('(');
    out.push_str(&format!("title: {}, ", typst_str(&req.title)));
    out.push_str(&format!("summary: {}, ", typst_str(&req.summary)));
    out.push_str(&format!("day_width: {}, ", super::fmt_num(req.day_width)));
    out.push_str(&format!(
        "label_width: {}, ",
        super::fmt_num(req.label_width)
    ));
    out.push_str(&format!("row_height: {}, ", super::fmt_num(req.row_height)));
    out.push_str(&format!(
        "header_height: {}, ",
        super::fmt_num(req.header_height)
    ));

    out.push_str("days: (");
    for day in &req.days {
        out.push_str(&format!(
            "(label: {}, date: {}, today: {}), ",
            typst_str(&day.label),
            // The legacy renderer treats whitespace-only dates as absent.
            typst_str(day.date.trim()),
            day.today
        ));
    }
    out.push_str("), periods: (");
    for period in &req.periods {
        out.push_str(&format!(
            "(label: {}, time: {}), ",
            typst_str(&period.label),
            typst_str(&period.time)
        ));
    }
    out.push_str("), dividers: (");
    for divider in &req.dividers {
        out.push_str(&format!("{divider}, "));
    }
    out.push_str("), items: (");
    for item in &req.items {
        out.push_str(&format!(
            "(day: {}, start: {}, end: {}, course: {}, location: {}, weeks: {}, color: {}, large: {}, course_size: {}, meta_size: {}), ",
            item.day,
            item.start,
            item.end,
            typst_str(&item.course),
            typst_str(&item.location),
            typst_str(&item.weeks),
            typst_str(&item.color),
            item.large,
            super::fmt_num(item.course_size),
            super::fmt_num(item.meta_size),
        ));
    }
    out.push_str("), footer: (");
    for line in &req.footer {
        out.push_str(&typst_str(line));
        out.push_str(", ");
    }
    out.push_str("))");
    out
}

fn build_source(req: &GridPayload) -> String {
    TEMPLATE.replace("__DATA__", &data_literal(req))
}

const TEMPLATE: &str = include_str!("../templates/grid.typ");

#[cfg(test)]
mod tests {
    use super::*;

    fn valid_payload() -> GridPayload {
        GridPayload {
            title: "课表".into(),
            summary: "周日–周六 · 第 1–12 节".into(),
            days: vec![GridDay {
                label: "周一".into(),
                date: "09-07".into(),
                today: false,
            }],
            periods: vec![GridPeriod {
                label: "1".into(),
                time: "08:00".into(),
            }],
            day_width: 360.0,
            label_width: 120.0,
            row_height: 56.0,
            header_height: 54.0,
            dividers: vec![],
            items: vec![GridItem {
                day: 0,
                start: 1,
                end: 1,
                course: "课程".into(),
                location: "教室".into(),
                weeks: "1-16 周".into(),
                color: "#e2e8f0".into(),
                large: false,
                course_size: 14.0,
                meta_size: 10.0,
            }],
            footer: vec!["更新时间".into(), "Life @ USTC".into()],
        }
    }

    #[test]
    fn accepts_valid_payload() {
        validate(&valid_payload()).unwrap();
    }

    #[test]
    fn rejects_extreme_dimensions_and_bounds() {
        let mut payload = valid_payload();
        payload.day_width = f64::NAN;
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("finite"));

        let mut payload = valid_payload();
        payload.items[0].day = 1;
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("bounds"));

        let mut payload = valid_payload();
        payload.items[0].color = "#xyzxyz".into();
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("#rrggbb"));
    }

    #[test]
    fn rejects_oversized_text() {
        let mut payload = valid_payload();
        payload.title = "x".repeat(super::super::MAX_TEXT_BYTES + 1);
        assert!(validate(&payload)
            .unwrap_err()
            .to_string()
            .contains("too long"));
    }

    #[test]
    fn normalizes_whitespace_only_dates_in_source() {
        let mut payload = valid_payload();
        payload.days[0].date = "   ".into();
        assert!(build_source(&payload).contains("date: \"\""));
    }
}

//! Schedule rendering: semantic JSON payload -> phone-sized Typst card -> PNG.
//!
//! Go owns schedule semantics such as today selection, course colors, and
//! item validity. Typst owns the phone card's flow layout so long text wraps
//! naturally and each card can grow with its content.

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
    pub period: String,
    pub time: String,
    #[serde(default)]
    pub course: String,
    #[serde(default)]
    pub location: String,
    #[serde(default)]
    pub weeks: String,
    #[serde(default)]
    pub color: String,
}

const MAX_DAYS: usize = 14;
const MAX_PERIODS: usize = 64;
const MAX_ITEMS: usize = 512;

/// Render a schedule payload to PNG. The shared card template fixes the
/// logical width at 390pt; compile_png checks the compiled page dimensions
/// before allocating a raster pixmap.
pub fn render(payload: &serde_json::Value, scale: f32) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let req: GridPayload =
        serde_json::from_value(payload.clone()).context("invalid grid payload")?;
    validate(&req)?;
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
        for (name, value, max_bytes) in [
            ("period", &item.period, super::MAX_TEXT_BYTES),
            ("time", &item.time, super::MAX_TEXT_BYTES),
            ("location", &item.location, super::MAX_LABEL_BYTES),
            ("weeks", &item.weeks, super::MAX_LABEL_BYTES),
        ] {
            super::check_text(&format!("items[{index}].{name}"), value, max_bytes)?;
            super::check_text_budget(&mut text_budget, &format!("items[{index}].{name}"), value)?;
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
        if !item.color.is_empty()
            && (item.color.len() != 7
                || !item.color.starts_with('#')
                || !item.color[1..].bytes().all(|byte| byte.is_ascii_hexdigit()))
        {
            return Err(anyhow!("grid item color must be a #rrggbb value"));
        }
        super::check_text(
            &format!("items[{index}].color"),
            &item.color,
            super::MAX_LABEL_BYTES,
        )?;
        super::check_text_budget(
            &mut text_budget,
            &format!("items[{index}].color"),
            &item.color,
        )?;
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

    out.push_str("days: (");
    for day in &req.days {
        out.push_str(&format!(
            "(label: {}, date: {}, today: {}), ",
            typst_str(&day.label),
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
    out.push_str("), items: (");
    for item in &req.items {
        out.push_str(&format!(
            "(day: {}, start: {}, end: {}, period: {}, time: {}, course: {}, location: {}, weeks: {}, color: {}), ",
            item.day,
            item.start,
            item.end,
            typst_str(&item.period),
            typst_str(&item.time),
            typst_str(&item.course),
            typst_str(&item.location),
            typst_str(&item.weeks),
            typst_str(&item.color),
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
    use serde_json::json;

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
                label: "第 1 节".into(),
                time: "08:00–08:45".into(),
            }],
            items: vec![GridItem {
                day: 0,
                start: 1,
                end: 1,
                period: "第 1 节".into(),
                time: "08:00–08:45".into(),
                course: "课程".into(),
                location: "教室".into(),
                weeks: "1-16 周".into(),
                color: "#e2e8f0".into(),
            }],
            footer: vec!["更新时间".into(), "Life @ USTC".into()],
        }
    }

    #[test]
    fn accepts_valid_payload() {
        validate(&valid_payload()).unwrap();
    }

    #[test]
    fn rejects_invalid_bounds_and_color() {
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
    fn source_keeps_full_text_and_has_no_geometry_fields() {
        let mut payload = valid_payload();
        payload.items[0].course =
            "Introduction to Computational Thinking and Programming Methodology 数据库系统".into();
        payload.items[0].location = "东区 · 高新区 GT-B112".into();
        payload.items[0].weeks = "第 1–16 周（单周）".into();
        let source = build_source(&payload);
        assert!(source.contains(&payload.items[0].course));
        assert!(source.contains(&payload.items[0].location));
        assert!(source.contains(&payload.items[0].weeks));
        assert!(!source.contains("day_width"));
        assert!(!source.contains("course_size"));
        assert!(!source.contains("dividers"));
    }

    #[test]
    fn normalizes_whitespace_only_dates_in_source() {
        let mut payload = valid_payload();
        payload.days[0].date = "   ".into();
        assert!(build_source(&payload).contains("date: \"\""));
    }

    #[test]
    fn renders_phone_width_and_grows_for_wrapped_content() {
        let short = json!({
            "title": "今天课表",
            "summary": "周一 · 第 1–2 节",
            "days": [{"label": "周一", "date": "09-07", "today": true}],
            "periods": [
                {"label": "第 1 节", "time": "08:00–08:45"},
                {"label": "第 2 节", "time": "08:50–09:35"}
            ],
            "items": [{
                "day": 0, "start": 1, "end": 1,
                "period": "第 1 节", "time": "08:00–08:45",
                "course": "数据库系统", "location": "高新区 · GT-B112",
                "weeks": "第 1–16 周", "color": "#dbeafe"
            }],
            "footer": ["更新时间", "Life @ USTC"]
        });
        let (_, short_width, short_height) = render(&short, 3.0).unwrap();
        assert_eq!(short_width, 1170);
        assert!(short_height > 0);

        let long = json!({
            "title": "本周课表",
            "summary": "周日–周六 · 第 1–2 节",
            "days": [{"label": "周一", "date": "09-07", "today": false}],
            "periods": [
                {"label": "第 1 节", "time": "08:00–08:45"},
                {"label": "第 2 节", "time": "08:50–09:35"}
            ],
            "items": [{
                "day": 0, "start": 1, "end": 2,
                "period": "第 1 节–第 2 节", "time": "08:00–09:35",
                "course": "Introduction to Computational Thinking and Programming Methodology 数据库系统",
                "location": "东区教学楼与高新区 GT-B112 之间的综合教学地点",
                "weeks": "第 1–16 周（单周与双周均有安排）", "color": "#dbeafe"
            }],
            "footer": ["更新时间", "Life @ USTC"]
        });
        let (_, long_width, long_height) = render(&long, 3.0).unwrap();
        assert_eq!(long_width, 1170);
        assert!(long_height > short_height);
    }
}

//! Weather card rendering: validated JSON payload -> Typst source -> PNG.
//!
//! Weather semantics stay in the Go response layer. This module validates the
//! structured values, computes the chart coordinates for the shared phone
//! canvas, and hands the remaining layout to the flow-based Typst template.
//! Strings are escaped before they become source code; the sidecar never gives
//! Typst filesystem or network access.

use anyhow::{bail, Context};
use serde::Deserialize;

use crate::escape::typst_str;

// Keep these values in step with the shared common.typ card style. They are
// renderer constants for chart coordinates, not payload geometry.
const CONTENT_WIDTH: f64 = 350.0;
const CHART_PLOT_LEFT: f64 = 22.0;
const CHART_PLOT_RIGHT: f64 = CONTENT_WIDTH - CHART_PLOT_LEFT;
const CHART_PLOT_BOTTOM: f64 = 120.0;
const CHART_TEMP_RANGE: f64 = 88.0;
const CHART_MAX_BAR_HEIGHT: f64 = 34.0;
const DAILY_TRACK_WIDTH: f64 = 1.0;

const MAX_LOCATIONS: usize = 16;
const MAX_HOURLY_POINTS: usize = 168;
const MAX_DAILY_POINTS: usize = 31;
const MAX_ALERTS: usize = 32;
const MAX_TEXT_BYTES: usize = 4096;
const MAX_LABEL_BYTES: usize = 256;
const MAX_NUMERIC_VALUE: f64 = 1_000_000.0;

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WeatherPayload {
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub meta: String,
    #[serde(default)]
    pub footer: Vec<String>,
    #[serde(default)]
    pub locations: Vec<WeatherLocation>,
}

#[derive(Debug, Deserialize)]
pub struct WeatherLocation {
    pub name: String,
    #[serde(default)]
    pub current: WeatherCurrent,
    #[serde(default)]
    pub hourly: Vec<WeatherHour>,
    #[serde(default)]
    pub daily: Vec<WeatherDay>,
    #[serde(default)]
    pub alerts: Vec<String>,
}

#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WeatherCurrent {
    #[serde(default)]
    pub temperature: f64,
    #[serde(default)]
    pub condition_text: String,
    #[serde(default)]
    pub icon: String,
    #[serde(default)]
    pub high: f64,
    #[serde(default)]
    pub low: f64,
    #[serde(default)]
    pub has_range: bool,
    #[serde(default)]
    pub humidity_text: String,
    #[serde(default)]
    pub wind_text: String,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WeatherHour {
    #[serde(default)]
    pub label: String,
    #[serde(default)]
    pub temperature: f64,
    #[serde(default)]
    pub precipitation_probability: f64,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WeatherDay {
    #[serde(default)]
    pub label: String,
    #[serde(default)]
    pub low: f64,
    #[serde(default)]
    pub high: f64,
    #[serde(default)]
    pub condition_text: String,
}

impl WeatherPayload {
    fn validate(&self) -> anyhow::Result<()> {
        if self.title.trim().is_empty() {
            bail!("weather payload title is empty");
        }
        check_text("title", &self.title, MAX_TEXT_BYTES)?;
        check_text("meta", &self.meta, MAX_TEXT_BYTES)?;
        if self.footer.len() > 2 {
            bail!(
                "weather payload footer has {} lines; at most 2 are supported",
                self.footer.len()
            );
        }
        for (index, line) in self.footer.iter().enumerate() {
            check_text(&format!("footer[{index}]"), line, MAX_TEXT_BYTES)?;
        }
        if self.locations.is_empty() {
            bail!("weather render requires at least one location");
        }
        if self.locations.len() > MAX_LOCATIONS {
            bail!(
                "weather payload has too many locations ({} > {MAX_LOCATIONS})",
                self.locations.len()
            );
        }
        for (index, location) in self.locations.iter().enumerate() {
            location.validate(index)?;
        }
        self.validate_text_budget()?;
        Ok(())
    }

    fn validate_text_budget(&self) -> anyhow::Result<()> {
        let mut total = 0usize;
        super::check_text_budget(&mut total, "title", &self.title)?;
        super::check_text_budget(&mut total, "meta", &self.meta)?;
        for (index, line) in self.footer.iter().enumerate() {
            super::check_text_budget(&mut total, &format!("footer[{index}]"), line)?;
        }
        for (index, location) in self.locations.iter().enumerate() {
            super::check_text_budget(
                &mut total,
                &format!("locations[{index}].name"),
                &location.name,
            )?;
            for (field, value) in [
                ("condition_text", &location.current.condition_text),
                ("icon", &location.current.icon),
                ("humidity_text", &location.current.humidity_text),
                ("wind_text", &location.current.wind_text),
            ] {
                super::check_text_budget(
                    &mut total,
                    &format!("locations[{index}].current.{field}"),
                    value,
                )?;
            }
            for (point, hour) in location.hourly.iter().enumerate() {
                super::check_text_budget(
                    &mut total,
                    &format!("locations[{index}].hourly[{point}].label"),
                    &hour.label,
                )?;
            }
            for (point, day) in location.daily.iter().enumerate() {
                super::check_text_budget(
                    &mut total,
                    &format!("locations[{index}].daily[{point}].label"),
                    &day.label,
                )?;
                super::check_text_budget(
                    &mut total,
                    &format!("locations[{index}].daily[{point}].condition_text"),
                    &day.condition_text,
                )?;
            }
            for (alert, text) in location.alerts.iter().enumerate() {
                super::check_text_budget(
                    &mut total,
                    &format!("locations[{index}].alerts[{alert}]"),
                    text,
                )?;
            }
        }
        Ok(())
    }
}

impl WeatherLocation {
    fn validate(&self, index: usize) -> anyhow::Result<()> {
        check_text(
            &format!("locations[{index}].name"),
            &self.name,
            MAX_LABEL_BYTES,
        )?;
        if self.name.trim().is_empty() {
            bail!("locations[{index}].name is empty");
        }
        self.current.validate(index)?;
        if self.hourly.len() > MAX_HOURLY_POINTS {
            bail!(
                "locations[{index}].hourly has too many points ({} > {MAX_HOURLY_POINTS})",
                self.hourly.len()
            );
        }
        for (point, hour) in self.hourly.iter().enumerate() {
            check_text(
                &format!("locations[{index}].hourly[{point}].label"),
                &hour.label,
                MAX_LABEL_BYTES,
            )?;
            finite(
                &format!("locations[{index}].hourly[{point}].temperature"),
                hour.temperature,
            )?;
            finite(
                &format!("locations[{index}].hourly[{point}].precipitation_probability"),
                hour.precipitation_probability,
            )?;
            if !(0.0..=100.0).contains(&hour.precipitation_probability) {
                bail!(
                    "locations[{index}].hourly[{point}].precipitation_probability must be 0..=100"
                );
            }
        }
        if self.daily.len() > MAX_DAILY_POINTS {
            bail!(
                "locations[{index}].daily has too many points ({} > {MAX_DAILY_POINTS})",
                self.daily.len()
            );
        }
        for (point, day) in self.daily.iter().enumerate() {
            check_text(
                &format!("locations[{index}].daily[{point}].label"),
                &day.label,
                MAX_LABEL_BYTES,
            )?;
            check_text(
                &format!("locations[{index}].daily[{point}].condition_text"),
                &day.condition_text,
                MAX_LABEL_BYTES,
            )?;
            finite(&format!("locations[{index}].daily[{point}].low"), day.low)?;
            finite(&format!("locations[{index}].daily[{point}].high"), day.high)?;
            if day.high < day.low {
                bail!("locations[{index}].daily[{point}].high must be >= low");
            }
        }
        if self.alerts.len() > MAX_ALERTS {
            bail!(
                "locations[{index}].alerts has too many entries ({} > {MAX_ALERTS})",
                self.alerts.len()
            );
        }
        for (alert, text) in self.alerts.iter().enumerate() {
            check_text(
                &format!("locations[{index}].alerts[{alert}]"),
                text,
                MAX_TEXT_BYTES,
            )?;
        }
        Ok(())
    }
}

impl WeatherCurrent {
    fn validate(&self, index: usize) -> anyhow::Result<()> {
        finite(
            &format!("locations[{index}].current.temperature"),
            self.temperature,
        )?;
        finite(&format!("locations[{index}].current.high"), self.high)?;
        finite(&format!("locations[{index}].current.low"), self.low)?;
        check_text(
            &format!("locations[{index}].current.condition_text"),
            &self.condition_text,
            MAX_LABEL_BYTES,
        )?;
        check_text(
            &format!("locations[{index}].current.icon"),
            &self.icon,
            MAX_LABEL_BYTES,
        )?;
        check_text(
            &format!("locations[{index}].current.humidity_text"),
            &self.humidity_text,
            MAX_LABEL_BYTES,
        )?;
        check_text(
            &format!("locations[{index}].current.wind_text"),
            &self.wind_text,
            MAX_LABEL_BYTES,
        )?;
        if self.has_range && self.high < self.low {
            bail!("locations[{index}].current.high must be >= low");
        }
        Ok(())
    }
}

fn check_text(field: &str, value: &str, max_bytes: usize) -> anyhow::Result<()> {
    if value.len() > max_bytes {
        bail!("{field} is too long ({} bytes > {max_bytes})", value.len());
    }
    Ok(())
}

fn finite(field: &str, value: f64) -> anyhow::Result<()> {
    if !value.is_finite() || value.abs() > MAX_NUMERIC_VALUE {
        bail!("{field} must be finite and within +/-{MAX_NUMERIC_VALUE}");
    }
    Ok(())
}

/// Render a validated weather payload. Returns `(png_bytes, width_px,
/// height_px)` just like the other card renderers. The Typst page owns its
/// height, so there is no caller-supplied geometry to trust or preserve.
pub fn render(payload: &serde_json::Value, scale: f32) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let req: WeatherPayload =
        serde_json::from_value(payload.clone()).context("invalid weather payload")?;
    req.validate()?;
    // Guard the fixed shared width and requested raster scale before Typst
    // compilation. The page height is intentionally discovered from flow.
    let source = build_source(&req);
    super::compile_png(source, scale)
}

fn build_source(req: &WeatherPayload) -> String {
    TEMPLATE.replace("__DATA__", &data_literal(req))
}

fn data_literal(req: &WeatherPayload) -> String {
    let mut out = String::new();
    out.push('(');
    out.push_str(&format!("title: {}, ", typst_str(&req.title)));
    out.push_str(&format!("meta: {}, ", typst_str(&req.meta)));
    out.push_str("footer: (");
    for line in &req.footer {
        out.push_str(&typst_str(line));
        out.push_str(", ");
    }
    out.push_str("), locations: (");
    for location in &req.locations {
        location_literal(&mut out, location);
        out.push_str(", ");
    }
    out.push_str("))");
    out
}

fn location_literal(out: &mut String, location: &WeatherLocation) {
    let current = &location.current;
    out.push_str("(name: ");
    out.push_str(&typst_str(&location.name));
    out.push_str(", current: (");
    out.push_str(&format!(
        "temperature: {}, temperature_text: {}, condition_text: {}, icon: {}, \
         high_text: {}, low_text: {}, has_range: {}, humidity_text: {}, wind_text: {}), ",
        super::fmt_num(current.temperature),
        typst_str(&format_temp(current.temperature)),
        typst_str(&current.condition_text),
        typst_str(&current.icon),
        typst_str(&format_temp(current.high)),
        typst_str(&format_temp(current.low)),
        current.has_range,
        typst_str(&current.humidity_text),
        typst_str(&current.wind_text),
    ));

    let plot = hourly_plot(&location.hourly);
    out.push_str("hourly: (");
    for (index, hour) in location.hourly.iter().enumerate() {
        let point = &plot.points[index];
        out.push_str(&format!(
            "(label: {}, temperature: {}, temperature_text: {}, \
             precipitation_probability: {}, bar_height: {}, bar_width: {}, \
             x: {}, y: {}, show_temperature: {}, show_axis_label: {}), ",
            typst_str(&hour.label),
            super::fmt_num(hour.temperature),
            typst_str(&format_temp(hour.temperature)),
            super::fmt_num(hour.precipitation_probability),
            super::fmt_num(point.bar_height),
            super::fmt_num(point.bar_width),
            super::fmt_num(point.x),
            super::fmt_num(point.y),
            point.show_temperature,
            point.show_axis_label,
        ));
    }
    out.push_str("), precipitation_summary: ");
    out.push_str(&typst_str(&precipitation_summary(&location.hourly)));
    out.push_str(", plot: (");
    out.push_str("segments: (");
    for segment in &plot.segments {
        out.push_str(&format!(
            "(x0: {}, y0: {}, x1: {}, y1: {}), ",
            super::fmt_num(segment.x0),
            super::fmt_num(segment.y0),
            super::fmt_num(segment.x1),
            super::fmt_num(segment.y1),
        ));
    }
    out.push_str("), area: (");
    for (x, y) in &plot.area {
        out.push_str(&format!(
            "({}, {}), ",
            super::fmt_num(*x),
            super::fmt_num(*y)
        ));
    }
    out.push_str(")), daily: (");
    let daily_bars = daily_bar_geometry(&location.daily);
    for (index, day) in location.daily.iter().enumerate() {
        let (fill_left, fill_width) = daily_bars[index];
        out.push_str(&format!(
            "(label: {}, low: {}, high: {}, low_text: {}, high_text: {}, \
             condition_text: {}, fill_left: {}, fill_width: {}), ",
            typst_str(&day.label),
            super::fmt_num(day.low),
            super::fmt_num(day.high),
            typst_str(&format_temp(day.low)),
            typst_str(&format_temp(day.high)),
            typst_str(&day.condition_text),
            super::fmt_num(fill_left),
            super::fmt_num(fill_width),
        ));
    }
    out.push_str("), alerts: (");
    for alert in &location.alerts {
        out.push_str(&typst_str(alert));
        out.push_str(", ");
    }
    out.push_str("))");
}

fn format_temp(value: f64) -> String {
    format!("{}°", value.round() as i64)
}

fn format_probability(value: f64) -> String {
    let rounded = value.round() as i64;
    if rounded == 0 && value > 0.0 {
        "<1%".to_string()
    } else {
        format!("{rounded}%")
    }
}

#[derive(Debug)]
struct PlotPoint {
    x: f64,
    y: f64,
    bar_height: f64,
    bar_width: f64,
    show_temperature: bool,
    show_axis_label: bool,
}

#[derive(Debug)]
struct PlotSegment {
    x0: f64,
    y0: f64,
    x1: f64,
    y1: f64,
}

#[derive(Debug)]
struct HourlyPlot {
    points: Vec<PlotPoint>,
    segments: Vec<PlotSegment>,
    area: Vec<(f64, f64)>,
}

/// Keep labels at least three points apart for normal 24-point forecasts and
/// increase the step for unusually dense input. The curve and all bars remain
/// based on every point regardless of this presentation choice.
fn axis_label_step(point_count: usize, slot_width: f64) -> usize {
    if point_count <= 6 {
        return 1;
    }
    let min_spacing = 38.0;
    let width_step = (min_spacing / slot_width).ceil() as usize;
    width_step.max(3)
}

/// Match the legacy Catmull-Rom chart's temperature scaling while giving the
/// phone chart side insets for readable first and last labels.
fn hourly_plot(hours: &[WeatherHour]) -> HourlyPlot {
    if hours.is_empty() {
        return HourlyPlot {
            points: Vec::new(),
            segments: Vec::new(),
            area: Vec::new(),
        };
    }
    let plot_width = CHART_PLOT_RIGHT - CHART_PLOT_LEFT;
    let slot_width = plot_width / hours.len() as f64;
    let mut min_temp = hours[0].temperature;
    let mut max_temp = hours[0].temperature;
    for hour in hours {
        min_temp = min_temp.min(hour.temperature);
        max_temp = max_temp.max(hour.temperature);
    }
    if max_temp - min_temp < 2.0 {
        max_temp = min_temp + 2.0;
    }
    min_temp -= 1.0;
    max_temp += 1.0;
    let temp_y = |temperature: f64| {
        let ratio = (temperature - min_temp) / (max_temp - min_temp);
        CHART_PLOT_BOTTOM - ratio * CHART_TEMP_RANGE
    };
    let label_step = axis_label_step(hours.len(), slot_width);

    let mut points = Vec::with_capacity(hours.len());
    for (index, hour) in hours.iter().enumerate() {
        let x = CHART_PLOT_LEFT + slot_width * (index as f64 + 0.5);
        let bar_height = if hour.precipitation_probability > 0.0 {
            (hour.precipitation_probability / 100.0 * CHART_MAX_BAR_HEIGHT).max(2.0)
        } else {
            0.0
        };
        let show_label = index % label_step == 0;
        points.push(PlotPoint {
            x,
            y: temp_y(hour.temperature),
            bar_height,
            bar_width: (slot_width * 0.5).clamp(2.0, 10.0),
            show_temperature: show_label,
            show_axis_label: show_label,
        });
    }

    let temperatures = hours
        .iter()
        .map(|hour| hour.temperature)
        .collect::<Vec<_>>();
    let sampled = catmull_rom(&temperatures, 12);
    let step_x = slot_width / 12.0;
    let mut segments = Vec::with_capacity(sampled.len().saturating_sub(1));
    let mut area = Vec::with_capacity(sampled.len() + 2);
    for (index, temperature) in sampled.iter().enumerate() {
        let x = CHART_PLOT_LEFT + slot_width * 0.5 + index as f64 * step_x;
        area.push((x, temp_y(*temperature)));
        if index > 0 {
            let previous = sampled[index - 1];
            let x0 = x - step_x;
            segments.push(PlotSegment {
                x0,
                y0: temp_y(previous),
                x1: x,
                y1: temp_y(*temperature),
            });
        }
    }
    if let Some((first_x, _)) = area.first().copied() {
        if let Some((last_x, _)) = area.last().copied() {
            area.push((last_x, CHART_PLOT_BOTTOM));
            area.push((first_x, CHART_PLOT_BOTTOM));
        }
    }
    HourlyPlot {
        points,
        segments,
        area,
    }
}

/// Format non-zero precipitation bars as a readable text summary. Adjacent
/// hours with the same probability are grouped, while the chart still keeps
/// every original bar and probability value.
fn precipitation_summary(hours: &[WeatherHour]) -> String {
    #[derive(Debug)]
    struct Group {
        first_index: usize,
        last_index: usize,
        probability: String,
    }

    let mut groups: Vec<Group> = Vec::new();
    for (index, hour) in hours.iter().enumerate() {
        if hour.precipitation_probability <= 0.0 {
            continue;
        }
        let probability = format_probability(hour.precipitation_probability);
        if let Some(previous) = groups.last_mut() {
            if previous.last_index + 1 == index && previous.probability == probability {
                previous.last_index = index;
                continue;
            }
        }
        groups.push(Group {
            first_index: index,
            last_index: index,
            probability,
        });
    }
    if groups.is_empty() {
        return "无降水".to_string();
    }
    groups
        .into_iter()
        .map(|group| {
            if group.first_index == group.last_index {
                format!("{} {}", hours[group.first_index].label, group.probability)
            } else {
                format!(
                    "{}–{} {}",
                    hours[group.first_index].label,
                    hours[group.last_index].label,
                    group.probability
                )
            }
        })
        .collect::<Vec<_>>()
        .join(" · ")
}

/// Compute normalized daily bar positions. A constant range gets a centered
/// marker instead of a zero-width bar, and all values remain in the track.
fn daily_bar_geometry(days: &[WeatherDay]) -> Vec<(f64, f64)> {
    if days.is_empty() {
        return Vec::new();
    }
    let mut week_low = days[0].low;
    let mut week_high = days[0].high;
    for day in days {
        week_low = week_low.min(day.low);
        week_high = week_high.max(day.high);
    }
    let mut span = week_high - week_low;
    if span < 1.0 {
        let padding = (1.0 - span) / 2.0;
        week_low -= padding;
        week_high += padding;
        span = week_high - week_low;
    }
    days.iter()
        .map(|day| {
            let left =
                ((day.low - week_low) / span * DAILY_TRACK_WIDTH).clamp(0.0, DAILY_TRACK_WIDTH);
            let right =
                ((day.high - week_low) / span * DAILY_TRACK_WIDTH).clamp(0.0, DAILY_TRACK_WIDTH);
            (left, (right - left).max(0.0))
        })
        .collect()
}

fn catmull_rom(values: &[f64], samples: usize) -> Vec<f64> {
    if values.len() < 2 || samples == 0 {
        return values.to_vec();
    }
    let at = |index: isize| -> f64 {
        if index < 0 {
            values[0]
        } else if index as usize >= values.len() {
            values[values.len() - 1]
        } else {
            values[index as usize]
        }
    };
    let mut output = Vec::with_capacity((values.len() - 1) * samples + 1);
    for segment in 0..values.len() - 1 {
        let p0 = at(segment as isize - 1);
        let p1 = at(segment as isize);
        let p2 = at(segment as isize + 1);
        let p3 = at(segment as isize + 2);
        for step in 0..samples {
            let t = step as f64 / samples as f64;
            let t2 = t * t;
            let t3 = t2 * t;
            output.push(
                0.5 * ((2.0 * p1)
                    + (-p0 + p2) * t
                    + (2.0 * p0 - 5.0 * p1 + 4.0 * p2 - p3) * t2
                    + (-p0 + 3.0 * p1 - 3.0 * p2 + p3) * t3),
            );
        }
    }
    output.push(*values.last().unwrap());
    output
}

const TEMPLATE: &str = include_str!("../templates/weather.typ");

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn valid_payload() -> serde_json::Value {
        json!({
            "title": "天气",
            "meta": "更新于 15:04 · 数据来源：和风天气",
            "footer": ["15:04 · 工作日", "Life @ USTC"],
            "locations": [{
                "name": "本部",
                "current": {
                    "temperature": 24.4,
                    "conditionText": "阴",
                    "icon": "cloudy",
                    "high": 27,
                    "low": 20,
                    "hasRange": true,
                    "humidityText": "78%",
                    "windText": "东北风 3 级"
                },
                "hourly": [
                    {"label": "16:00", "temperature": 25, "precipitationProbability": 60},
                    {"label": "17:00", "temperature": 25.5, "precipitationProbability": 60},
                    {"label": "18:00", "temperature": 24, "precipitationProbability": 30},
                    {"label": "19:00", "temperature": 23, "precipitationProbability": 0}
                ],
                "daily": [{"label": "今天", "low": 20, "high": 27, "conditionText": "阴"}],
                "alerts": ["高温黄色预警"]
            }]
        })
    }

    #[test]
    fn validates_phone_payload_without_legacy_geometry() {
        let req: WeatherPayload = serde_json::from_value(valid_payload()).unwrap();
        req.validate().unwrap();
        let source = build_source(&req);
        assert!(source.contains("card-page"));
        assert!(!source.contains("canvas_width:"));
    }

    #[test]
    fn rejects_legacy_geometry_fields() {
        let mut value = valid_payload();
        value["canvas_width"] = json!(920);
        let error = serde_json::from_value::<WeatherPayload>(value)
            .unwrap_err()
            .to_string();
        assert!(error.contains("unknown field"), "unexpected error: {error}");
    }

    #[test]
    fn rejects_bad_probability_and_empty_locations() {
        let mut value = valid_payload();
        value["locations"][0]["hourly"][0]["precipitationProbability"] = json!(101);
        let req: WeatherPayload = serde_json::from_value(value).unwrap();
        assert!(req.validate().unwrap_err().to_string().contains("0..=100"));

        let mut value = valid_payload();
        value["locations"] = json!([]);
        let req: WeatherPayload = serde_json::from_value(value).unwrap();
        assert!(req
            .validate()
            .unwrap_err()
            .to_string()
            .contains("at least one location"));

        let mut value = valid_payload();
        value["locations"][0]["daily"][0]["high"] = json!(19);
        let req: WeatherPayload = serde_json::from_value(value).unwrap();
        assert!(req
            .validate()
            .unwrap_err()
            .to_string()
            .contains("daily[0].high must be >= low"));
    }

    #[test]
    fn plot_keeps_full_data_with_sparse_phone_labels() {
        let hours = (0..24)
            .map(|index| WeatherHour {
                label: format!("{index:02}:00"),
                temperature: 20.0 + (index % 5) as f64,
                precipitation_probability: if index % 4 == 0 { 50.0 } else { 0.0 },
            })
            .collect::<Vec<_>>();
        let plot = hourly_plot(&hours);
        assert_eq!(plot.points.len(), 24);
        assert_eq!(
            plot.points
                .iter()
                .filter(|point| point.show_axis_label)
                .count(),
            8
        );
        assert!(plot
            .points
            .iter()
            .all(|point| point.x >= CHART_PLOT_LEFT && point.x <= CHART_PLOT_RIGHT));
        assert_eq!(plot.segments.len(), (hours.len() - 1) * 12);
        assert!(plot
            .points
            .iter()
            .all(|point| point.bar_height.is_finite() && point.bar_width.is_finite()));
    }

    #[test]
    fn plot_is_finite_for_empty_flat_and_single_point_series() {
        assert!(hourly_plot(&[]).points.is_empty());
        let one = vec![WeatherHour {
            label: "now".into(),
            temperature: 20.0,
            precipitation_probability: 0.0,
        }];
        let flat = vec![
            WeatherHour {
                label: "a".into(),
                temperature: 20.0,
                precipitation_probability: 0.0,
            },
            WeatherHour {
                label: "b".into(),
                temperature: 20.0,
                precipitation_probability: 50.0,
            },
        ];
        for plot in [hourly_plot(&one), hourly_plot(&flat)] {
            assert!(!plot.points.is_empty());
            for point in plot.points {
                assert!(point.x.is_finite() && point.y.is_finite());
            }
            for segment in plot.segments {
                assert!(segment.x0.is_finite() && segment.y0.is_finite());
                assert!(segment.x1.is_finite() && segment.y1.is_finite());
            }
        }
        let constant_days = vec![
            WeatherDay {
                label: "今天".into(),
                low: 20.0,
                high: 20.0,
                condition_text: String::new(),
            },
            WeatherDay {
                label: "明天".into(),
                low: 20.0,
                high: 20.0,
                condition_text: String::new(),
            },
        ];
        let bars = daily_bar_geometry(&constant_days);
        assert_eq!(bars.len(), 2);
        assert!(bars.iter().all(|(left, width)| {
            left.is_finite() && width.is_finite() && *left >= 0.0 && *left <= DAILY_TRACK_WIDTH
        }));
    }

    #[test]
    fn renders_empty_sparse_and_constant_chart_cases() {
        let mut empty = valid_payload();
        empty["locations"][0]["hourly"] = json!([]);
        empty["locations"][0]["daily"] = json!([]);
        empty["locations"][0]["alerts"] = json!([]);
        let (_, empty_width, empty_height) = render(&empty, 1.0).unwrap();
        assert_eq!(empty_width, 390);
        assert!(empty_height > 0);

        let mut constant = valid_payload();
        constant["locations"][0]["hourly"] = json!([
            {"label": "现在", "temperature": 20, "precipitationProbability": 0},
            {"label": "一小时后", "temperature": 20, "precipitationProbability": 100}
        ]);
        constant["locations"][0]["daily"] = json!([
            {"label": "今天", "low": 20, "high": 20, "conditionText": "晴"},
            {"label": "明天", "low": 20, "high": 20, "conditionText": "晴"}
        ]);
        let (_, constant_width, constant_height) = render(&constant, 1.0).unwrap();
        assert_eq!(constant_width, 390);
        assert!(constant_height > empty_height);
    }

    #[test]
    fn rejects_non_finite_and_out_of_bound_values() {
        let mut req: WeatherPayload = serde_json::from_value(valid_payload()).unwrap();
        req.locations[0].current.temperature = f64::INFINITY;
        assert!(req.validate().unwrap_err().to_string().contains("finite"));

        let mut req: WeatherPayload = serde_json::from_value(valid_payload()).unwrap();
        req.locations[0].current.temperature = MAX_NUMERIC_VALUE + 1.0;
        assert!(req.validate().unwrap_err().to_string().contains("within"));
    }

    #[test]
    fn renders_phone_width_and_expands_for_long_content() {
        let payload = valid_payload();
        let (png, width, height) = render(&payload, 1.0).expect("weather template should compile");
        assert!(!png.is_empty());
        assert_eq!(width, 390);

        let mut long = valid_payload();
        long["locations"][0]["name"] = json!("中国科学技术大学高新校区气象观测点");
        long["locations"][0]["current"]["windText"] =
            json!("东南偏东风 3 级，阵风 5 级，体感舒适但请注意道路湿滑");
        long["locations"][0]["alerts"] = json!([
            "高温黄色预警：未来六小时最高气温将超过三十五摄氏度，请减少户外活动并及时补水。"
        ]);
        let (_, long_width, long_height) =
            render(&long, 1.0).expect("long weather template should compile");
        assert_eq!(long_width, 390);
        assert!(
            long_height > height,
            "long content did not increase page height"
        );
    }

    #[test]
    fn separates_multiple_locations_with_one_flow_divider() {
        let mut payload = valid_payload();
        let location = payload["locations"][0].clone();
        payload["locations"] = json!([location.clone(), location]);
        let (_, width, height) = render(&payload, 1.0).expect("weather template should compile");
        let single_height = render(&valid_payload(), 1.0).unwrap().2;
        assert_eq!(width, 390);
        assert!(height > single_height);
        let req: WeatherPayload = serde_json::from_value(payload).unwrap();
        let source = build_source(&req);
        assert_eq!(source.matches("line(length: 100%").count(), 1);
    }
}

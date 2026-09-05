//! Weather card rendering: validated JSON payload -> Typst source -> PNG.
//!
//! The Go renderer owns the weather semantics (location selection, labels,
//! provider text, and the structured `WeatherCard`).  This module keeps the
//! same logical canvas and row metrics as `weather_render.go`, then delegates
//! the drawing to the embedded Typst template.  All strings are escaped before
//! they become source code; the sidecar never gives Typst filesystem or network
//! access.

use anyhow::{bail, Context};
use serde::Deserialize;

use crate::escape::typst_str;

const CANVAS_WIDTH: f64 = 920.0;
const MARGIN_X: f64 = 52.0;
const TITLE_BASELINE: f64 = 52.0;
const TITLE_GAP: f64 = 12.0;
const NAME_ROW: f64 = 34.0;
const HERO_ROW: f64 = 110.0;
const TILE_ROW: f64 = 68.0;
const HEADING_ROW: f64 = 32.0;
const CHART_ROW: f64 = 158.0;
const CHART_LABELS: f64 = 20.0;
const DAY_ROW: f64 = 30.0;
const ALERT_ROW: f64 = 24.0;
const BLOCK_GAP: f64 = 30.0;
const FOOTER_ROW: f64 = 48.0;
const CONTENT_WIDTH: f64 = CANVAS_WIDTH - 2.0 * MARGIN_X;
const CHART_PLOT_BOTTOM: f64 = CHART_ROW - 34.0;
const CHART_MAX_BAR_HEIGHT: f64 = 34.0;

const MAX_LOCATIONS: usize = 16;
const MAX_HOURLY_POINTS: usize = 168;
const MAX_DAILY_POINTS: usize = 31;
const MAX_ALERTS: usize = 32;
const MAX_TEXT_BYTES: usize = 4096;
const MAX_LABEL_BYTES: usize = 256;
const MAX_NUMERIC_VALUE: f64 = 1_000_000.0;

#[derive(Debug, Deserialize)]
pub struct WeatherPayload {
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub meta: String,
    #[serde(default)]
    pub footer: Vec<String>,
    #[serde(default = "default_canvas_width")]
    pub canvas_width: f64,
    #[serde(default)]
    pub height: f64,
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

fn default_canvas_width() -> f64 {
    CANVAS_WIDTH
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
        if !self.canvas_width.is_finite() || self.canvas_width <= 0.0 {
            bail!("weather payload canvas_width must be finite and positive");
        }
        if (self.canvas_width - CANVAS_WIDTH).abs() > f64::EPSILON {
            bail!(
                "weather payload canvas_width must be {CANVAS_WIDTH}, got {}",
                self.canvas_width
            );
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
        if self.height != 0.0 && (!self.height.is_finite() || self.height <= 0.0) {
            bail!("weather payload height must be zero or a finite positive number");
        }
        let expected = logical_height(self);
        if self.height != 0.0 && (self.height - expected).abs() > f64::EPSILON {
            bail!(
                "weather payload height does not match location rows (expected {expected}, got {})",
                self.height
            );
        }
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
/// height_px)` just like the other card renderers.
pub fn render(payload: &serde_json::Value, scale: f32) -> anyhow::Result<(Vec<u8>, u32, u32)> {
    let req: WeatherPayload =
        serde_json::from_value(payload.clone()).context("invalid weather payload")?;
    req.validate()?;
    super::check_render_dimensions(CANVAS_WIDTH, logical_height(&req), scale)?;
    let source = build_source(&req);
    super::compile_png(source, scale)
}

fn logical_height(req: &WeatherPayload) -> f64 {
    TITLE_BASELINE
        + TITLE_GAP
        + req
            .locations
            .iter()
            .map(location_height_with_gap)
            .sum::<f64>()
        + FOOTER_ROW
}

fn location_height_with_gap(location: &WeatherLocation) -> f64 {
    location_height(location) + BLOCK_GAP
}

fn location_height(location: &WeatherLocation) -> f64 {
    let mut height = NAME_ROW + HERO_ROW;
    if !location.current.humidity_text.is_empty() || !location.current.wind_text.is_empty() {
        height += TILE_ROW;
    }
    if !location.hourly.is_empty() {
        height += HEADING_ROW + CHART_ROW + CHART_LABELS;
    }
    if !location.daily.is_empty() {
        height += HEADING_ROW + DAY_ROW * location.daily.len() as f64;
    }
    if !location.alerts.is_empty() {
        height += HEADING_ROW + ALERT_ROW * location.alerts.len() as f64;
    }
    height
}

fn build_source(req: &WeatherPayload) -> String {
    TEMPLATE.replace("__DATA__", &data_literal(req))
}

fn data_literal(req: &WeatherPayload) -> String {
    let height = if req.height == 0.0 {
        logical_height(req)
    } else {
        req.height
    };
    let mut out = String::new();
    out.push('(');
    out.push_str(&format!("title: {}, ", typst_str(&req.title)));
    out.push_str(&format!("meta: {}, ", typst_str(&req.meta)));
    out.push_str(&format!(
        "canvas_width: {}, ",
        super::fmt_num(req.canvas_width)
    ));
    out.push_str(&format!("height: {}, ", super::fmt_num(height)));
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

    out.push_str("hourly: (");
    let plot = hourly_plot(&location.hourly);
    for (index, hour) in location.hourly.iter().enumerate() {
        let point = &plot.points[index];
        out.push_str(&format!(
            "(label: {}, temperature_text: {}, precipitation: {}, bar_height: {}, \
             bar_width: {}, x: {}, y: {}, precipitation_label: {}, precipitation_label_y: {}, \
             precipitation_inside: {}), ",
            typst_str(&hour.label),
            typst_str(&format_temp(hour.temperature)),
            super::fmt_num(hour.precipitation_probability),
            super::fmt_num(point.bar_height),
            super::fmt_num(point.bar_width),
            super::fmt_num(point.x),
            super::fmt_num(point.y),
            typst_str(point.precipitation_label.as_deref().unwrap_or("")),
            super::fmt_num(point.precipitation_label_y),
            point.precipitation_inside,
        ));
    }
    out.push_str("), plot: (");
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

#[derive(Debug)]
struct PlotPoint {
    x: f64,
    y: f64,
    bar_height: f64,
    bar_width: f64,
    precipitation_label: Option<String>,
    precipitation_label_y: f64,
    precipitation_inside: bool,
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

/// Match the Go Catmull-Rom chart geometry.  The template receives line
/// segments and polygon vertices, so it does not need floating-point helpers
/// or any data-dependent layout decisions.
fn hourly_plot(hours: &[WeatherHour]) -> HourlyPlot {
    if hours.is_empty() {
        return HourlyPlot {
            points: Vec::new(),
            segments: Vec::new(),
            area: Vec::new(),
        };
    }
    let slot_width = CONTENT_WIDTH / hours.len() as f64;
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
        // Keep the ten-point label clearance introduced by the legacy
        // renderer's current chart implementation.
        CHART_PLOT_BOTTOM - ratio * (CHART_ROW - 58.0)
    };

    let mut points = Vec::with_capacity(hours.len());
    for (index, hour) in hours.iter().enumerate() {
        let x = slot_width * (index as f64 + 0.5);
        let bar_height = if hour.precipitation_probability > 0.0 {
            (hour.precipitation_probability / 100.0 * CHART_MAX_BAR_HEIGHT).max(2.0)
        } else {
            0.0
        };
        let precipitation_label = if hour.precipitation_probability >= 30.0 {
            Some(format!(
                "{}%",
                hour.precipitation_probability.round() as i64
            ))
        } else {
            None
        };
        let precipitation_inside = bar_height >= 14.0;
        let mut precipitation_label_y = CHART_PLOT_BOTTOM - bar_height / 2.0 + 3.0;
        if !precipitation_inside {
            precipitation_label_y = CHART_PLOT_BOTTOM - bar_height - 4.0;
            // The Go renderer skips a short-bar label when it would collide
            // with the temperature label.  The exact collision check needs
            // font metrics, so use the conservative plot-safe position here.
            if precipitation_label_y - 10.0 < temp_y(hour.temperature) - 6.0 {
                precipitation_label_y = -100.0;
            }
        }
        points.push(PlotPoint {
            x,
            y: temp_y(hour.temperature),
            bar_height,
            bar_width: slot_width * 0.44,
            precipitation_label,
            precipitation_label_y,
            precipitation_inside,
        });
    }

    let sampled = catmull_rom(
        &hours
            .iter()
            .map(|hour| hour.temperature)
            .collect::<Vec<_>>(),
        12,
    );
    let step_x = slot_width / 12.0;
    let mut segments = Vec::with_capacity(sampled.len().saturating_sub(1));
    let mut area = Vec::with_capacity(sampled.len() + 2);
    for (index, temperature) in sampled.iter().enumerate() {
        let x = slot_width * 0.5 + index as f64 * step_x;
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
    let span = (week_high - week_low).max(1.0);
    let bar_left = 56.0 + 44.0 + 16.0;
    let bar_right = CONTENT_WIDTH - 44.0 - 8.0;
    let bar_width = bar_right - bar_left;
    days.iter()
        .map(|day| {
            let left = bar_left + (day.low - week_low) / span * bar_width;
            let right = bar_left + (day.high - week_low) / span * bar_width;
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
            "meta": "更新于 15:04",
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
                "hourly": [{"label": "16:00", "temperature": 25, "precipitationProbability": 60}],
                "daily": [{"label": "今天", "low": 20, "high": 27, "conditionText": "阴"}],
                "alerts": ["高温黄色预警"]
            }]
        })
    }

    #[test]
    fn validates_and_computes_legacy_height() {
        let req: WeatherPayload = serde_json::from_value(valid_payload()).unwrap();
        req.validate().unwrap();
        assert_eq!(
            logical_height(&req),
            64.0 + (34.0 + 110.0 + 68.0 + 32.0 + 158.0 + 20.0 + 32.0 + 30.0 + 32.0 + 24.0 + 30.0)
                + 48.0
        );
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
    fn plot_is_finite_for_flat_and_single_point_series() {
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
    }

    #[test]
    fn renders_typst_fixture_at_requested_scale() {
        let payload = valid_payload();
        let (png, width, height) = render(&payload, 1.0).expect("weather template should compile");
        assert!(!png.is_empty());
        assert_eq!(width, CANVAS_WIDTH as u32);
        assert_eq!(
            height,
            logical_height(&serde_json::from_value(payload).unwrap()) as u32
        );
    }
}

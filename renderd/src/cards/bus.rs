//! Bus timetable card rendering: JSON payload -> typst source -> PNG.

use anyhow::{anyhow, Context};
use serde::Deserialize;

use crate::escape::typst_str;
use crate::world::SandboxWorld;

#[derive(Debug, Deserialize)]
pub struct BusPayload {
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub date_line: String,
    #[serde(default)]
    pub semester_line: String,
    #[serde(default)]
    pub next: Option<NextBus>,
    #[serde(default)]
    pub tables: Vec<BusTable>,
}

#[derive(Debug, Deserialize)]
pub struct NextBus {
    pub time: String,
    pub wait: String,
}

#[derive(Debug, Deserialize)]
pub struct BusTable {
    #[serde(default)]
    pub label: String,
    pub header: Vec<String>,
    #[serde(default)]
    pub header_emphasis: Vec<bool>,
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
    crate::world::fonts().map_err(|e| anyhow!(e))?;

    let source = build_source(&req);
    let world = SandboxWorld::new(source);
    let warned = typst::compile::<typst::layout::PagedDocument>(&world);
    let doc = warned.output.map_err(|errors| {
        anyhow!(
            "typst compile failed: {}",
            errors
                .iter()
                .map(|e| e.message.to_string())
                .collect::<Vec<_>>()
                .join("; ")
        )
    })?;
    let page = doc.pages.first().context("compiled document has no pages")?;

    let pixmap = typst_render::render(page, scale);
    let png = pixmap.encode_png().context("png encode failed")?;
    Ok((png, pixmap.width(), pixmap.height()))
}

/// Serialize the request into a typst dictionary literal.
fn data_literal(req: &BusPayload) -> String {
    let mut out = String::new();
    out.push('(');
    out.push_str(&format!("title: {}, ", typst_str(&req.title)));
    out.push_str(&format!("date_line: {}, ", typst_str(&req.date_line)));
    out.push_str(&format!("semester_line: {}, ", typst_str(&req.semester_line)));
    match &req.next {
        Some(next) => out.push_str(&format!(
            "next: (time: {}, wait: {}), ",
            typst_str(&next.time),
            typst_str(&next.wait)
        )),
        None => out.push_str("next: none, "),
    }
    out.push_str("tables: (");
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

const TEMPLATE: &str = r##"
#set page(
  width: 460pt,
  height: auto,
  margin: (left: 26pt, right: 26pt, top: 16pt, bottom: 12pt),
  fill: rgb("#f0fdf4"),
)
#set text(font: ("Noto Sans CJK SC", "Source Han Sans CN", "Source Han Sans SC"), size: 9pt, fill: rgb("#1e293b"))

#let accent = rgb("#16a34a")
#let header-bg = rgb("#dcfce7")
#let highlight-bg = rgb("#bbf7d0")
#let border = rgb("#cbd5e1")
#let faint = rgb("#64748b")
#let departed-ink = rgb("#94a3b8")
#let title-ink = rgb("#0f172a")

#let data = __DATA__

#let cell-body(r, c) = {
  if r.highlight { text(weight: "bold", fill: accent)[#c] }
  else if r.departed { text(fill: departed-ink)[#c] }
  else { [#c] }
}

#let bus-table(t) = {
  let ncols = calc.max(t.header.len(), 1)
  block(stroke: 0.8pt + border, radius: 6pt, clip: true, width: 100%)[
    #table(
      columns: ncols * (1fr,),
      stroke: none,
      inset: (x: 4pt, y: 5pt),
      align: center,
      fill: (x, y) => if y == 0 {
        header-bg
      } else if t.rows.at(calc.min(y - 1, t.rows.len() - 1)).highlight {
        highlight-bg
      },
      table.hline(y: 1, stroke: 0.6pt + border),
      ..t.header.enumerate().map(((i, h)) => {
        if t.header_emphasis.at(i) {
          [#text(weight: "bold", size: 8pt, fill: accent)[#h]]
        } else {
          [#text(weight: "bold", size: 8pt)[#h]]
        }
      }),
      ..t.rows.map(r => r.cells.map(c => [#cell-body(r, c)])).flatten(),
    )
  ]
}

#grid(
  columns: (1fr, auto),
  text(size: 8pt, weight: "bold", fill: accent)[LIFE \@ USTC],
  text(size: 8pt, fill: faint)[#data.date_line],
)
#v(4pt)
#grid(
  columns: (1fr, auto),
  align: bottom,
  text(size: 15pt, weight: "bold", fill: title-ink)[#data.title],
  if data.next != none {
    box(fill: accent, radius: 4pt, inset: (x: 8pt, y: 4pt))[
      #text(fill: white, size: 9pt, weight: "bold")[下一班 #data.next.time（#data.next.wait）]
    ]
  },
)
#v(8pt)

#for t in data.tables {
  if t.label != "" {
    v(2pt)
    text(size: 8pt, fill: faint, weight: "bold")[#t.label]
    v(3pt)
  }
  bus-table(t)
  v(10pt)
}

#line(length: 100%, stroke: 0.5pt + border)
#v(2pt)
#align(center)[#text(size: 7.5pt, fill: faint)[#data.semester_line]]
"##;

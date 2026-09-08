#import "styling.typ": *

// Measure the actual fonts; short columns retain their intrinsic widths.
#let column-widths(rows, count) = range(count).map(index => calc.max(
  0pt, ..rows.map(cells => measure(flow-text(font: text-fonts, size: 14pt, cells.at(index, default: ""))).width)) + 2 * pad)
#let table-natural-width(rows, count) = column-widths(rows, count).sum()
#let text-natural-width(body, style: "body") = measure(styled(style, body)).width
#let column-tracks(rows, count, width) = {
  let natural = column-widths(rows, count)
  if natural.sum() <= width { return natural }
  let narrow = natural.map(w => w <= 192pt)
  let fixed = natural.enumerate().fold(0pt, (sum, (i, w)) => sum + if narrow.at(i) { w } else { 0pt })
  if narrow.any(v => not v) and width - fixed >= 48pt * narrow.filter(v => not v).len() {
    natural.enumerate().map(((i, w)) => if narrow.at(i) { w } else { (w / 1pt) * 1fr })
  } else {
    natural.map(w => (w / 1pt) * 1fr)
  }
}

// Content determines the height. A wide timetable is allowed its own width.
#let card-sheet(natural, body, min-width: min-content-width, max-width: max-content-width, margin: page-margin) = context {
  let width = calc.max(min-width, calc.min(natural, max-width))
  set text(font: text-fonts, size: 13pt, fill: ink, lang: "zh")
  set par(spacing: 8pt)
  set block(spacing: 0pt)
  set page(width: width + 2 * margin, height: auto, margin: margin, fill: ground)
  body
}

#let card-header(title, subtitle: none) = grid(
  columns: (1fr, auto), column-gutter: 24pt, align: (left + bottom, right + bottom),
  title-text(title), if subtitle == none { [] } else { caption-text(subtitle) })

#let section-label(title) = {
  block(inset: (x: pad, y: 7pt), body-text(title, weight: "bold"))
}

// Headers and rows sit directly on the paper, with horizontal rules only.
#let plain-table(t, tracks, bus: false) = {
  let cells = ()
  for (i, label) in t.header.enumerate() {
    cells.push(table.cell(align: center + horizon,
      body-text(label, weight: if t.header_emphasis.at(i, default: false) { "bold" } else { "regular" })))
  }
  for row in t.rows {
    for index in range(t.header.len()) {
      cells.push(table.cell(fill: if row.highlight { accent-soft } else { none },
        flow-text(size: if bus { 14pt } else { 13pt },
          fill: if bus and row.departed and not row.highlight { muted } else { ink },
          row.cells.at(index, default: ""))))
    }
  }
  table(columns: tracks, rows: (28pt,) + (auto,) * t.rows.len(),
    inset: (x: pad, y: 10pt), align: left + horizon,
    stroke: (x, y) => (top: if y > 0 { hairline + border } else { none }),
    ..cells)
}

#let card-footer(lines) = {
  if lines.len() > 0 {
    v(24pt)
    align(right, {
      for (index, value) in lines.enumerate() {
        if index > 0 { v(7pt) }
        block(caption-text(value))
      }
    })
  }
}

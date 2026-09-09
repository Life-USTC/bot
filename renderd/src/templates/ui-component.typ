#import "styling.typ": *

// A fixed paper width constrains native layout; content determines the height.
#let card-sheet(body, width: content-width, margin: page-margin) = {
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

// Keep true tabular content aligned with the timetable's full-cell grid.
#let plain-table(t, bus: false) = {
  let cells = ()
  for (i, label) in t.header.enumerate() {
    cells.push(table.cell(
      fill: table-header,
      align: if bus { center + horizon } else { left + horizon },
      body-text(label, weight: "bold",
        fill: if t.header_emphasis.at(i, default: false) { accent } else { ink })))
  }
  for (row-index, row) in t.rows.enumerate() {
    for index in range(t.header.len()) {
      cells.push(table.cell(fill: if row.highlight {
          table-highlight
        } else if calc.even(row-index) {
          table-stripe-a
        } else {
          table-stripe-b
        },
        text(size: if bus { 14pt } else { 13pt },
          fill: if bus and row.departed and not row.highlight { muted } else { ink },
          row.cells.at(index, default: ""))))
    }
  }
  table(columns: if bus { (1fr,) * t.header.len() } else { (2fr,) + (1fr,) * (t.header.len() - 1) },
    inset: (x: pad, y: 10pt),
    align: if bus { center + horizon } else { left + horizon },
    stroke: table-stroke,
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

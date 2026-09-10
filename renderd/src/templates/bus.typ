#import "styling.typ": *
#import "ui-component.typ": *
#let data = __DATA__

#let route-columns = calc.min(2, data.tables.len())
#let column-width = 300pt
#let gap = 18pt
#let route-table(t) = block(width: column-width, {
  if t.label != "" { section-label(t.label) }
  plain-table(t, bus: true)
})

#card-sheet(width: column-width * route-columns + gap * (route-columns - 1), margin: 24pt, {
  card-header(data.title, subtitle: if data.next_time == none { none } else {
    stack(spacing: 7pt,
      text(size: 11pt, fill: muted, "下一班 " + data.next_time),
      text(size: 11pt, fill: accent, data.next_wait))
  })
  v(18pt)
  // Each column flows independently. Measure actual wrapped content instead
  // of aligning every short table to the bottom of its taller neighbour.
  context {
    let columns = ((),) * route-columns
    let heights = (0pt,) * route-columns
    for t in data.tables {
      let column = if route-columns == 1 or heights.at(0) <= heights.at(1) { 0 } else { 1 }
      let body = route-table(t)
      columns.at(column).push(body)
      heights.at(column) += measure(body, width: column-width).height + gap
    }
    grid(columns: (column-width,) * route-columns, column-gutter: gap, align: top,
      ..columns.map(items => stack(dir: ttb, spacing: gap, ..items)))
  }
  if data.tables.any(t => t.rows.any(row => row.cells.any(cell => cell == ""))) {
    v(12pt)
    caption-text("— 即停")
  }
  card-footer(data.footer)
})

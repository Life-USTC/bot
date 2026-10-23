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
  // Preserve reading order and start each pair of route tables together.
  // Separate tables keep their own trips; rows do not imply matched journeys.
  grid(columns: (column-width,) * route-columns, gutter: gap, align: top,
    ..data.tables.map(route-table))
  if data.tables.any(t => t.rows.any(row => row.cells.any(cell => cell == ""))) {
    v(12pt)
    caption-text("— 即停")
  }
  card-footer(data.footer)
})

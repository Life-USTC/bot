#import "styling.typ": *
#import "ui-component.typ": *
#let data = __DATA__

#let route-columns = if data.tables.len() > 2 { 2 } else { 1 }
#card-sheet(width: 360pt * route-columns + 30pt * (route-columns - 1), {
  card-header(data.title, subtitle: if data.next_time == none { none } else {
    stack(spacing: 7pt,
      text(size: 11pt, fill: muted, "下一班 " + data.next_time),
      text(size: 11pt, fill: accent, data.next_wait))
  })
  v(30pt)
  let tables = (data.tables.filter(t => "高新区" in t.header) +
    data.tables.filter(t => "高新区" not in t.header))
  grid(columns: (1fr,) * route-columns, gutter: 30pt, align: top,
    ..tables.map(t => {
      if t.label != "" { section-label(t.label) }
      plain-table(t, bus: true)
    }))
  card-footer(data.footer)
})

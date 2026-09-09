#import "styling.typ": *
#import "ui-component.typ": *
#let data = __DATA__

#card-sheet(width: 360pt, {
  card-header(data.title, subtitle: if data.next_time == none { none } else {
    stack(spacing: 7pt,
      text(size: 11pt, fill: muted, "下一班 " + data.next_time),
      text(size: 11pt, fill: accent, data.next_wait))
  })
  v(30pt)
  let tables = (data.tables.filter(t => "高新区" in t.header) +
    data.tables.filter(t => "高新区" not in t.header))
  for (index, t) in tables.enumerate() {
    if index > 0 { v(30pt) }
    if t.label != "" { section-label(t.label) }
    plain-table(t, bus: true)
  }
  card-footer(data.footer)
})

#import "styling.typ": *
#import "ui-component.typ": *
#let data = __DATA__

// Campus grouping and reverse-route pairing are domain decisions.
#let paired(tables) = {
  let pairs = ()
  let used = ()
  for (i, t) in tables.enumerate() {
    if i in used { continue }
    let pair = (t,)
    used.push(i)
    for (j, other) in tables.enumerate() {
      if j not in used and t.header == other.header.rev() {
        pair.push(other)
        used.push(j)
        break
      }
    }
    pairs.push(pair)
  }
  pairs
}

#card-sheet(width: 380pt, {
  card-header(data.title, subtitle: if data.next_time == none { none } else {
    stack(spacing: 7pt,
      text(size: 11pt, fill: muted, "下一班 " + data.next_time),
      text(size: 11pt, fill: accent, data.next_wait))
  })
  v(30pt)
  let pairs = (paired(data.tables.filter(t => "高新区" in t.header)) +
    paired(data.tables.filter(t => "高新区" not in t.header)))
  for (index, pair) in pairs.enumerate() {
    if index > 0 { v(30pt) }
    grid(columns: (1fr,) * pair.len(), column-gutter: 20pt,
      ..pair.map(t => {
        if t.label != "" { section-label(t.label) }
        plain-table(t, bus: true)
      }))
  }
  card-footer(data.footer)
})

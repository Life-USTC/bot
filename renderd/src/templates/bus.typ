#import "styling.typ": *
#import "ui-component.typ": *
#let data = __DATA__

#let natural(t) = table-natural-width((t.header,) + t.rows.map(r => r.cells), t.header.len())

// Keep the former campus grouping and put reverse routes next to each other.
#let paired(tables) = {
  let result = ()
  let used = ()
  for (i, t) in tables.enumerate() {
    if i in used { continue }
    result.push(t)
    used.push(i)
    for (j, other) in tables.enumerate() {
      if j not in used and t.header == other.header.rev() {
        result.push(other)
        used.push(j)
        break
      }
    }
  }
  result
}
#context {
  let groups = (
    paired(data.tables.filter(t => "高新区" in t.header)),
    paired(data.tables.filter(t => "高新区" not in t.header)))
  let rows = ()
  for group in groups {
    let row = ()
    let used = 0pt
    for t in group {
      let w = calc.min(natural(t), max-content-width)
      if row.len() > 0 and used + 20pt + w > max-content-width {
        rows.push(row)
        row = ()
        used = 0pt
      }
      used += w + if row.len() > 0 { 20pt } else { 0pt }
      row.push(t)
    }
    if row.len() > 0 { rows.push(row) }
  }
  let title-width = text-natural-width(data.title, style: "title")
  let next-width = if data.next_time == none { 0pt } else { 40pt + measure(text(size: 11pt, "下一班 " + data.next_time + " " + data.next_wait)).width }
  let width = calc.max(title-width + next-width, ..rows.map(row => row.map(natural).sum() + (row.len() - 1) * 20pt))
  card-sheet(width, min-width: 240pt, {
    if data.next_time == none {
      card-header(data.title)
      v(30pt)
    } else {
      grid(columns: (1fr, calc.min(next-width - 40pt, calc.min(width, max-content-width) / 2)),
        column-gutter: 16pt, align: left + top,
        title-text(data.title),
        move(dy: -10pt, align(right, {
          text(size: 11pt, fill: muted, "下一班 " + data.next_time)
          v(7pt)
          text(size: 11pt, fill: accent, data.next_wait)
        })))
      v(18pt)
    }
    for (i, row) in rows.enumerate() {
      if i > 0 { v(30pt) }
      grid(columns: row.map(t => calc.min(natural(t), max-content-width)), column-gutter: 20pt,
        ..row.map(t => plain-table(t,
          column-tracks((t.header,) + t.rows.map(r => r.cells), t.header.len(), calc.min(natural(t), max-content-width)), bus: true)))
    }
    card-footer(data.footer)
  })
}

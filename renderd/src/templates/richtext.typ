#import "styling.typ": *
#import "ui-component.typ": *
#let data = __DATA__

// Native line breaking with the original 32pt ruled text rhythm.
#let prose(value, width, first: false) = context {
  let single = measure(body-text("国Ag")).height
  let body = {
    set par(leading: 32pt - single)
    body-text(value)
  }
  let height = measure(body, width: width - 2 * pad).height
  let count = calc.max(1, int(calc.ceil(height / 32pt)))
  block(width: width, height: count * 32pt, {
    for index in range(count) {
      if index > 0 or not first {
        place(top + left, dy: index * 32pt, line(length: width, stroke: hairline + border))
      }
    }
    block(inset: (x: 8pt, top: 8pt), body)
  })
}

#context {
  let tables = data.blocks.filter(b => b.table != none and b.table.header.len() > 0)
  let natural = calc.max(
    text-natural-width(data.title, style: "title"),
    ..tables.map(b => table-natural-width((b.table.header,) + b.table.rows.map(r => r.cells), b.table.header.len())),
    ..data.blocks.filter(b => b.table == none).map(b => calc.max(0pt, ..b.lines.map(line => text-natural-width(line) + 2 * pad))))
  let width = calc.max(min-content-width, calc.min(natural, max-content-width))
  card-sheet(natural, {
    card-header(data.title)
    v(30pt)
    for (index, item) in data.blocks.enumerate() {
      if index > 0 { v(30pt) }
      if item.heading != "" { section-label(item.heading) }
      if item.table != none and item.table.header.len() > 0 {
        let rows = (item.table.header,) + item.table.rows.map(r => r.cells)
        let tracks = column-tracks(rows, item.table.header.len(), width)
        if table-natural-width(rows, item.table.header.len()) < width { tracks.last() = 1fr }
        plain-table(item.table, tracks)
      } else {
        for (i, value) in item.lines.enumerate() {
          prose(value, width, first: i == 0)
        }
      }
    }
    card-footer(data.footer)
  })
}

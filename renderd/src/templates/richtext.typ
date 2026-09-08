#import "styling.typ": *
#import "ui-component.typ": *
#let data = __DATA__

#card-sheet({
  set par(spacing: 16pt)
  card-header(data.title)
  v(30pt)
  for (index, item) in data.blocks.enumerate() {
    if index > 0 { v(30pt) }
    if item.heading != "" { section-label(item.heading) }
    if item.table != none and item.table.header.len() > 0 {
      plain-table(item.table)
    } else {
      for value in item.lines {
        par(body-text(value))
      }
    }
  }
  card-footer(data.footer)
})

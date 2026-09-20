#import "styling.typ": *
#import "ui-component.typ": *
#let data = __DATA__

// A table card is as wide as its content needs, between the narrow paper used
// for prose and a bound that keeps a long title from stretching the sheet.
#let table-sheet-min = 280pt
#let table-sheet-max = 460pt

#context {
  let tables = data.blocks
    .map(item => item.table)
    .filter(item => item != none and item.header.len() > 0)
  let groups = grouped-column-widths(tables)
  let sheet-width = if tables.len() == 0 {
    content-width
  } else {
    calc.max(table-sheet-min, calc.min(table-sheet-max, grouped-natural-width(groups)))
  }

  card-sheet(width: sheet-width, {
    set par(spacing: 16pt)
    card-header(data.title)
    v(30pt)
    for (index, item) in data.blocks.enumerate() {
      if index > 0 { v(30pt) }
      if item.heading != "" { section-label(item.heading) }
      if item.table != none and item.table.header.len() > 0 {
        let natural = groups.at(table-group-key(item.table))
        plain-table(item.table, columns: fit-column-widths(natural,
          sheet-width - table-frame-width(natural.len())))
      } else {
        for value in item.lines {
          par(body-text(value))
        }
      }
    }
    card-footer(data.footer)
  })
}

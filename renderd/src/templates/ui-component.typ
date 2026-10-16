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

// Horizontal inset a cell spends on padding, independent of its text.
#let cell-pad = 2 * pad
// A column narrower than this wraps into a column of single glyphs, which is
// worse than letting a neighbour wrap one line earlier.
#let min-column-width = 52pt

#let cell-text-size(bus) = if bus { 14pt } else { 13pt }

// Cell strokes sit outside the column widths, so a table that fills its sheet
// needs the vertical rules counted too.
#let table-frame-width(count) = (count + 1) * 1pt

// The width each column would need if nothing wrapped. Must run in a context.
// Every measured run names its font: the sheet width is decided before
// `card-sheet` installs the card's text style, and measuring CJK against
// Typst's default family would size the columns for the wrong glyphs.
#let natural-column-widths(t, bus: false) = {
  let size = cell-text-size(bus)
  t.header.enumerate().map(((index, label)) => {
    let widest = measure(body-text(label, weight: "bold")).width
    for row in t.rows {
      let cell = row.cells.at(index, default: "")
      widest = calc.max(widest, measure(text(font: text-fonts, size: size, cell)).width)
    }
    widest + cell-pad
  })
}

// Tables repeating a header are one list broken into sections, so their
// columns are measured together and their rules line up down the card.
#let table-group-key(t) = t.header.join("\u{0}")

#let grouped-column-widths(tables, bus: false) = {
  let groups = (:)
  for t in tables {
    let key = table-group-key(t)
    let widths = natural-column-widths(t, bus: bus)
    groups.insert(key, if key in groups {
      groups.at(key).zip(widths).map(pair => calc.max(pair.at(0), pair.at(1)))
    } else {
      widths
    })
  }
  groups
}

// The width the widest group wants when nothing wraps.
#let grouped-natural-width(groups) = {
  let widest = 0pt
  for (_, widths) in groups {
    widest = calc.max(widest, widths.sum(default: 0pt) + table-frame-width(widths.len()))
  }
  widest
}

// Share `total` between columns in proportion to what each one actually
// holds. The previous fixed (2fr, 1fr, ...) split assumed the first column
// carried the content, which squeezed reminder cards whose first column is a
// timestamp and whose last column is a course or assignment title.
#let fit-column-widths(natural, total) = {
  let count = natural.len()
  let sum = natural.sum(default: 0pt)
  if count == 0 {
    ()
  } else if sum <= total {
    // Hand the surplus to the column most likely to wrap, so the table fills
    // the sheet instead of leaving a ragged edge beside the footer.
    let widest = 0
    for (index, width) in natural.enumerate() {
      if width > natural.at(widest) { widest = index }
    }
    natural.enumerate().map(((index, width)) =>
      if index == widest { width + (total - sum) } else { width })
  } else {
    // Wider than the sheet: every column wraps, so give each a floor and
    // split the rest by how much text it has to fit.
    let floor = calc.min(min-column-width, total / count)
    let slack = total - floor * count
    natural.map(width => floor + slack * (width / sum))
  }
}

// Keep true tabular content aligned with the timetable's full-cell grid.
#let plain-table(t, bus: false, columns: none) = {
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
        text(size: cell-text-size(bus),
          fill: if bus and row.departed and not row.highlight { muted } else { ink },
          if bus and row.cells.at(index, default: "") == "" { "—" } else {
            row.cells.at(index, default: "")
          })))
    }
  }
  // Bus timetables are columns of same-shaped times, so they ask for no
  // widths and get an even split that reads as a grid. A caller with prose of
  // uneven length measures its own columns and passes them in.
  table(columns: if columns == none { (1fr,) * t.header.len() } else { columns },
    inset: (x: pad, y: if bus { 8pt } else { 10pt }),
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

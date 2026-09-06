// Phone-readable rich text card. Text and table values stay semantic in the
// payload; Typst performs natural wrapping and auto-height layout.
#import "common.typ": *

#let data = __DATA__

#show: card-page

#let table-columns(count) = if count == 2 {
  (2fr, 1fr)
} else if count == 3 {
  (1.5fr, 1fr, 1fr)
} else if count == 4 {
  (1.5fr, 1fr, 1fr, 1fr)
} else {
  range(0, count).map(_ => 1fr)
}

#let rich-header-cell(value, emphasized) = {
  let content = break-long-tokens(value)
  if emphasized {
    text(size: subhead-size, weight: "semibold", fill: ink)[#content]
  } else {
    text(size: subhead-size, fill: muted)[#content]
  }
}

#let rich-body-cell(value, highlighted: false) = {
  let content = break-long-tokens(value)
  if highlighted {
    text(weight: "semibold")[#content]
  } else {
    content
  }
}

#let rich-standard-table(t) = {
  let count = t.header.len()
  let cells = ()
  for (index, header) in t.header.enumerate() {
    cells.push(rich-header-cell(
      header,
      t.header_emphasis.at(index, default: false),
    ))
  }
  for row in t.rows {
    for index in range(0, count) {
      cells.push(rich-body-cell(
        row.cells.at(index, default: ""),
        highlighted: row.highlight,
      ))
    }
  }
  table(
    columns: table-columns(count),
    stroke: none,
    align: left + horizon,
    fill: (_, y) => {
      if y > 0 and y - 1 < t.rows.len() and t.rows.at(y - 1).highlight {
        highlight-bg
      } else {
        none
      }
    },
    ..(range(1, t.rows.len() + 1).map(k =>
      table.hline(y: k, stroke: 0.7pt + line-c)
    )),
    ..cells,
  )
}

// More than four columns turn into repeated label/value records. This gives
// long fields a readable value column while retaining every original label,
// value, and row highlight.
#let rich-record-table(t) = {
  if t.rows.len() == 0 {
    table(
      columns: (1fr, 2fr),
      stroke: none,
      align: (left + horizon, left + horizon),
      ..(range(0, t.header.len()).map(index => (
        rich-header-cell(
          t.header.at(index),
          t.header_emphasis.at(index, default: false),
        ),
        rich-body-cell(""),
      )).flatten()),
    )
  } else {
    for row in t.rows {
      block(
        width: 100%,
        fill: if row.highlight { highlight-bg } else { none },
      )[
        #table(
          columns: (1fr, 2fr),
          stroke: none,
          align: (left + horizon, left + horizon),
          ..(range(1, t.header.len()).map(k =>
            table.hline(y: k, stroke: 0.7pt + line-c)
          )),
          ..(range(0, t.header.len()).map(index => (
            rich-header-cell(
              t.header.at(index),
              t.header_emphasis.at(index, default: false),
            ),
            rich-body-cell(
              row.cells.at(index, default: ""),
              highlighted: row.highlight,
            ),
          )).flatten()),
        )
      ]
    }
  }
}

#let rich-table(t) = {
  if t.header.len() <= 4 {
    rich-standard-table(t)
  } else {
    rich-record-table(t)
  }
}

#let rich-text-block(lines) = {
  block(width: 100%)[
    #for (index, line) in lines.enumerate() [
      #if index > 0 {
        parbreak()
      }
      #break-long-tokens(line)
    ]
  ]
}

#let rich-block-content(block) = {
  if block.table != none {
    rich-table(block.table)
  } else {
    rich-text-block(block.lines)
  }
}

#card-header(break-long-tokens(data.title))

#for block in data.blocks [
  #if block.heading != "" {
    card-section(break-long-tokens(block.heading))
  }
  #card-surface(rich-block-content(block))
]

#card-footer(data.footer.map(line => break-long-tokens(line)))

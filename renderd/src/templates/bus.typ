// Phone-readable bus timetable card. The payload is semantic; all wrapping
// and row sizing happens in Typst at the shared 390pt page width.
#import "common.typ": *

#let data = __DATA__

#show: card-page

// Keep ordinary words and headings untouched. Only unusually long ASCII runs
// get invisible break opportunities, which lets URLs and identifiers wrap
// without changing the characters sent in the semantic payload.
#let break-long-tokens(value) = {
  show regex("[A-Za-z0-9]{24,}"): it => [#it.text.split("").join("\u{200b}")]
  [#value]
}

#let table-columns(count) = range(0, count).map(_ => 1fr)

#let bus-header-cell(value, emphasized) = {
  let content = break-long-tokens(value)
  if emphasized {
    text(weight: "bold")[#content]
  } else {
    content
  }
}

#let bus-body-cell(value, departed: false) = {
  let content = break-long-tokens(value)
  if departed {
    text(fill: muted)[#content]
  } else {
    content
  }
}

#let bus-standard-table(t) = {
  let count = t.header.len()
  let cells = ()
  for (index, header) in t.header.enumerate() {
    cells.push(bus-header-cell(
      header,
      t.header_emphasis.at(index, default: false),
    ))
  }
  for row in t.rows {
    for index in range(0, count) {
      cells.push(bus-body-cell(
        row.cells.at(index, default: ""),
        departed: row.departed,
      ))
    }
  }
  table(
    columns: table-columns(count),
    stroke: none,
    inset: (x: cell-pad-x, y: cell-pad-y),
    align: center + horizon,
    fill: (_, y) => {
      if y > 0 and y - 1 < t.rows.len() and t.rows.at(y - 1).highlight {
        highlight-bg
      } else {
        none
      }
    },
    ..(range(1, t.rows.len() + 1).map(k =>
      table.hline(y: k, stroke: 0.6pt + line-c)
    )),
    ..cells,
  )
}

// Tables wider than four columns become repeated label/value records. This
// keeps every stop/time pair legible while preserving the original headers,
// values, and highlight state.
#let bus-record-table(t) = {
  if t.rows.len() == 0 {
    block(width: 100%)[
      #table(
        columns: (1fr, 2fr),
        stroke: none,
        inset: (x: cell-pad-x, y: cell-pad-y),
        align: (left + horizon, center + horizon),
        ..(range(0, t.header.len()).map(index => (
          text(weight: "bold")[#break-long-tokens(t.header.at(index))],
          bus-body-cell(""),
        )).flatten()),
      )
    ]
  } else {
    for (row-index, row) in t.rows.enumerate() {
      if row-index > 0 {
        v(8pt)
      }
      block(
        width: 100%,
        fill: if row.highlight { highlight-bg } else { none },
        inset: 0pt,
      )[
        #table(
          columns: (1fr, 2fr),
          stroke: none,
          inset: (x: cell-pad-x, y: cell-pad-y),
          align: (left + horizon, center + horizon),
          ..(range(0, t.header.len()).map(index => (
            text(weight: "bold")[#break-long-tokens(t.header.at(index))],
            bus-body-cell(row.cells.at(index, default: ""), departed: row.departed),
          )).flatten()),
        )
      ]
    }
  }
}

#let bus-table(t) = {
  if t.label != "" {
    card-section(break-long-tokens(t.label))
  }
  if t.header.len() <= 4 {
    bus-standard-table(t)
  } else {
    bus-record-table(t)
  }
}

#card-header(break-long-tokens(data.title))

#if data.next_time != none {
  block(
    width: 100%,
    fill: highlight-bg,
    inset: (x: cell-pad-x, y: cell-pad-y),
    radius: 6pt,
  )[
    #grid(
      columns: (1fr, auto),
      column-gutter: 8pt,
      align: horizon,
      [下一班 #break-long-tokens(data.next_time)],
      align(right)[#text(fill: accent)[#break-long-tokens(data.next_wait)]],
    )
  ]
  v(section-gap)
}

#for (index, table) in data.tables.enumerate() [
  #if index > 0 {
    v(section-gap)
  }
  #bus-table(table)
]

#card-footer(data.footer.map(line => break-long-tokens(line)))

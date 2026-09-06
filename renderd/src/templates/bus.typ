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
    text(size: subhead-size, weight: "semibold", fill: ink)[#content]
  } else {
    text(size: subhead-size, fill: muted)[#content]
  }
}

#let bus-body-cell(value, departed: false, highlighted: false) = {
  let content = break-long-tokens(value)
  if departed {
    text(fill: muted)[#content]
  } else if highlighted {
    text(weight: "semibold", fill: ink)[#content]
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
        highlighted: row.highlight,
      ))
    }
  }
  table(
    columns: table-columns(count),
    stroke: none,
    inset: (x: 8pt, y: 10pt),
    align: center + horizon,
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

// Tables wider than four columns become repeated label/value records. This
// keeps every stop/time pair legible while preserving the original headers,
// values, and highlight state.
#let bus-record-table(t) = {
  if t.rows.len() == 0 {
    table(
      columns: (1fr, 2fr),
      stroke: none,
      inset: (x: 8pt, y: 9pt),
      align: (left + horizon, left + horizon),
      ..(range(0, t.header.len()).map(index => (
        bus-header-cell(
          t.header.at(index),
          t.header_emphasis.at(index, default: false),
        ),
        bus-body-cell(""),
      )).flatten()),
    )
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
          inset: (x: 8pt, y: 8pt),
          align: (left + horizon, left + horizon),
          ..(range(1, t.header.len()).map(k =>
            table.hline(y: k, stroke: 0.7pt + line-c)
          )),
          ..(range(0, t.header.len()).map(index => (
            bus-header-cell(
              t.header.at(index),
              t.header_emphasis.at(index, default: false),
            ),
            bus-body-cell(
              row.cells.at(index, default: ""),
              departed: row.departed,
              highlighted: row.highlight,
            ),
          )).flatten()),
        )
      ]
    }
  }
}

#let bus-route-panel(t) = card-surface([
  #if t.label != "" {
    text(size: subhead-size, weight: "semibold", fill: ink)[
      #break-long-tokens(t.label)
    ]
    v(10pt)
  }
  #if t.header.len() <= 4 {
    bus-standard-table(t)
  } else {
    bus-record-table(t)
  }
], inset: (x: 16pt, y: 14pt))

#let bus-header() = {
  if data.title.starts-with("校车 ") {
    let route = data.title.split(" ").slice(1).join(" ")
    card-header("校车", subtitle: break-long-tokens(route))
  } else {
    card-header(break-long-tokens(data.title))
  }
}

#let bus-summary() = card-surface([
  #grid(
    columns: (1fr, auto),
    column-gutter: 12pt,
    align: left + top,
    [
      #text(size: caption-size, weight: "semibold", fill: muted)[下一班]
      #v(3pt)
      #text(size: title-size, weight: "bold", fill: accent)[
        #break-long-tokens(data.next_time)
      ]
    ],
    align(right + top)[
      #text(size: caption-size, fill: muted)[等待时间]
      #v(3pt)
      #text(size: body-size, weight: "semibold", fill: accent)[
        #break-long-tokens(data.next_wait)
      ]
    ],
  )
], inset: (x: 16pt, y: 14pt), fill: highlight-bg)

#bus-header()

#if data.next_time != none {
  bus-summary()
  v(16pt)
}

#for (index, route) in data.tables.enumerate() [
  #if index > 0 {
    v(16pt)
  }
  #bus-route-panel(route)
]

#card-footer(data.footer.map(line => break-long-tokens(line)))

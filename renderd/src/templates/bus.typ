// Bus timetable card, visually replicating the legacy Go renderRichPNG bus
// branch (rich_render.go). All geometry is decided by the Go side; this
// template only draws. Units are logical pt == legacy logical px.
#let data = __DATA__

#let bg = rgb("#fafafa")
#let ink = rgb("#27272a")
#let muted = rgb("#71717a")
#let line-c = rgb("#d4d4d8")
#let highlight-bg = rgb("#f4f4f5")
#let accent = rgb("#0f766e")

// richRenderMetrics (logical px):
#let margin-x = 32pt
#let content-top = 82pt
#let header-h = 28pt
#let row-h = 32pt
#let cell-pad-x = 8pt
#let col-gap = 20pt
#let row-gap = 30pt
// footer occupies FooterGap(24) + FooterLineGap(14) + BottomMargin(32) = 70pt
#let footer-zone = 70pt

#set page(
  width: (data.content_width + 64) * 1pt,
  height: auto,
  margin: 0pt,
  fill: bg,
)
#set text(
  font: ("Fira Code", "Noto Sans CJK SC", "Source Han Sans CN"),
  size: 13pt,
  fill: ink,
)
// All geometry is explicit (fixed heights, explicit v() gaps); suppress the
// default inter-block spacing (1.2em) so legacy metrics are exact.
#set block(spacing: 0pt)

// Watermark: legacy drawBusLogoWatermark puts the 120pt logo's center 30pt
// (size/4) beyond the bottom-right corner, rotated -30deg, at 15% opacity
// (pre-baked into assets/logo-15.png by renderd). Placed first so all text
// draws over it.
#place(
  bottom + right,
  dx: 90pt,
  dy: 90pt,
  rotate(-30deg, image("assets/logo-15.png", width: 120pt)),
)

// Body cells: legacy draws ASCII runs with the 14pt mono face and other runs
// with the 13pt sans face (drawMixedText). The font list already picks Fira
// Code for ASCII; this only bumps ASCII runs to 14pt.
#let body-cell(r, c) = {
  let color = if r.highlight { ink } else if r.departed { muted } else { ink }
  set text(fill: color)
  show regex("[ -~]+"): it => text(size: 14pt)[#it]
  [#c]
}

#let header-cell(h, emphasized) = {
  if emphasized { text(weight: "bold")[#h] } else { [#h] }
}

#let bus-table(t) = {
  let nrows = t.rows.len()
  block(width: t.column_widths.sum() * 1pt)[
    #if t.label != "" {
      box(
        height: header-h,
        inset: (left: cell-pad-x),
        align(left + horizon, text(weight: "bold")[#t.label]),
      )
      line(length: 100%, stroke: 1pt + line-c)
    }
    #table(
      columns: t.column_widths.map(w => w * 1pt),
      rows: (header-h, ..(row-h,) * nrows),
      stroke: none,
      inset: (x: cell-pad-x, y: 0pt),
      align: (x, y) => if y == 0 { center + horizon } else { left + horizon },
      fill: (x, y) => if y > 0 and t.rows.at(y - 1).highlight { highlight-bg },
      ..(range(1, nrows + 1).map(k => table.hline(y: k, stroke: 1pt + line-c))),
      ..(t.header.enumerate().map(((i, h)) => header-cell(h, t.header_emphasis.at(i, default: false)))),
      ..(t.rows.map(r => r.cells.map(c => body-cell(r, c))).flatten()),
    )
  ]
}

// Footer: two right-aligned 9pt muted lines, anchored so the second line's
// baseline sits 32pt above the page bottom (legacy footerY + 14 + 32).
#if data.footer.len() > 0 {
  place(
    bottom + right,
    dx: -margin-x,
    dy: -29pt,
    align(right)[
      #text(size: 9pt, fill: muted)[#data.footer.at(0)]\
      #text(size: 9pt, fill: muted)[#data.footer.at(1)]
    ],
  )
}

#block(height: content-top, inset: (left: margin-x, right: margin-x))[
  #v(35pt)
  #grid(
    columns: (1fr, auto),
    align: bottom,
    text(size: 18pt, weight: "bold")[#data.title],
    if data.next_time != none [
      #align(right)[
        #text(size: 11pt, fill: muted)[下一班 #data.next_time]\
        #text(size: 11pt, fill: accent)[#data.next_wait]
      ]
    ],
  )
]

#block(inset: (left: margin-x, right: margin-x))[
  #for (ri, row) in data.rows_of_tables.enumerate() [
    #if ri > 0 { v(row-gap) }
    #grid(
      columns: row.map(i => data.tables.at(i).column_widths.sum() * 1pt),
      column-gutter: col-gap,
      align: top,
      ..row.map(i => bus-table(data.tables.at(i))),
    )
  ]
  #v(footer-zone)
]

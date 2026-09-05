// Generic rich text card, visually replicating the legacy Go renderRichPNG
// non-bus branch (rich_render.go). All geometry is decided by the Go side:
// text lines arrive pre-wrapped (wrapRichText) and table cells pre-fitted
// (fitRichTextToWidth); this template only draws. Units are logical pt ==
// legacy logical px.
#import "common.typ": *

#let data = __DATA__

// Compact help intro (richDocumentHasCompactHelpIntro): content starts at
// 58pt instead of 82pt, the title shifts right by one text padding, and the
// first text block uses tight 24pt rows without separators.
#let compact = data.compact_first_block
#let top = if compact { 58pt } else { content-top }
#let title-pad = if compact { text-pad-x } else { 0pt }

#set page(
  width: (data.content_width + 64) * 1pt,
  height: auto,
  margin: 0pt,
  fill: bg,
)
#set text(
  font: card-fonts,
  size: 13pt,
  fill: ink,
)
// All geometry is explicit (fixed heights, explicit v() gaps); suppress the
// default inter-block spacing (1.2em) so legacy metrics are exact.
#set block(spacing: 0pt)

// Placed first so all text draws over it.
#card-watermark()

// Header cell: plain 13pt, bold when the markdown cell was **emphasized**.
#let header-cell(h, emphasized) = {
  if emphasized { text(weight: "bold")[#h] } else { [#h] }
}

// Body cell: legacy drawMixedText draws ASCII runs with the 14pt mono face
// and other runs with the 13pt sans face. The font list already picks Fira
// Code for ASCII; this only bumps ASCII runs to 14pt.
#let body-cell(c) = {
  show regex("[ -~]+"): it => text(size: 14pt)[#it]
  [#c]
}

// Text block: optional bold heading row (28pt) plus one row per pre-wrapped
// line (32pt, or 24pt for the compact help intro). 1pt separators sit on
// every internal row boundary except after the last line; compact blocks
// have none. The block spans the full content width (legacy spans only the
// measured block width, but its row background equals the page background,
// so the difference is limited to separator length).
#let text-block(b) = {
  let nlines = b.lines.len()
  let rows = if b.heading != "" { (header-h,) } else { () }
  rows += ((if b.compact { 24pt } else { row-h },)) * nlines
  if rows.len() > 0 {
    let cells = ()
    if b.heading != "" { cells.push(text(weight: "bold")[#b.heading]) }
    for l in b.lines { cells.push([#l]) }
    let hlines = if b.compact {
      ()
    } else {
      range(1, rows.len()).map(k => table.hline(y: k, stroke: 1pt + line-c))
    }
    table(
      columns: (data.content_width * 1pt,),
      rows: rows,
      stroke: none,
      inset: (x: text-pad-x, y: 0pt),
      align: left + horizon,
      ..hlines,
      ..cells,
    )
  }
}

// Table block: optional bold heading row (28pt), then the header row (28pt,
// centered) and body rows (32pt, left). Highlighted (✨) rows get the
// highlight background. Legacy draws a separator under the heading but the
// table's header background immediately paints over it, so none is drawn.
#let table-block(b) = {
  let t = b.table
  let nrows = t.rows.len()
  block(width: t.column_widths.sum() * 1pt)[
    #if b.heading != "" {
      box(
        height: header-h,
        inset: (left: text-pad-x),
        align(left + horizon, text(weight: "bold")[#b.heading]),
      )
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
      ..(t.rows.map(r => r.cells.map(c => body-cell(c))).flatten()),
    )
  ]
}

#card-footer(data.footer)

#block(height: top, inset: (left: margin-x + title-pad, right: margin-x))[
  #v(35pt)
  #text(size: 18pt, weight: "bold")[#data.title]
]

#block(inset: (left: margin-x, right: margin-x))[
  #for (bi, b) in data.blocks.enumerate() [
    #if bi > 0 { v(row-gap) }
    #if b.table != none { table-block(b) } else { text-block(b) }
  ]
  #v(footer-zone)
]

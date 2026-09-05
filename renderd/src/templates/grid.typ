// Fixed-size schedule grid. Go supplies all user text and semantic decisions;
// this template only paints at the logical coordinates in the payload.
#import "common.typ": *

#let data = __DATA__
#let margin = 36pt
#let grid-top = 82pt
#let footer-gap = 28pt
#let bottom-margin = 28pt
#let divider-height = 4pt
#let grid-line = rgb("#cbd5e1")
#let divider-color = rgb("#64748b")
#let alternate-bg = rgb("#f8fafc")
#let today-header-bg = rgb("#ccfbf1")
#let today-bg = rgb("#f0fdfa")
#let neutral-course-bg = rgb("#e2e8f0")

#let page-width = (2 * margin + data.label_width * 1pt + data.days.len() * data.day_width * 1pt)
#let grid-width = (data.label_width + data.days.len() * data.day_width) * 1pt
#let grid-height = data.header_height * 1pt + data.periods.len() * data.row_height * 1pt
#let page-height = grid-top + grid-height + footer-gap + bottom-margin

#set page(
  width: page-width,
  height: page-height,
  margin: 0pt,
  fill: bg,
)
#set text(
  font: card-fonts,
  size: 13pt,
  fill: ink,
)
#set block(spacing: 0pt)

// The grid watermark is pre-faded by renderd because Typst images do not have
// a portable opacity parameter.
#place(
  bottom + right,
  dx: 90pt,
  dy: 90pt,
  rotate(-30deg, image("assets/logo-10.png", width: 120pt)),
)

#let grid-footer(lines) = {
  if lines.len() > 1 {
    place(
      bottom + right,
      dx: -margin,
      dy: -30pt - 14pt,
      text(size: 9pt, fill: muted)[#lines.at(0)],
    )
    place(
      bottom + right,
      dx: -margin,
      dy: -30pt,
      text(size: 9pt, fill: muted)[#lines.at(1)],
    )
  }
}

#let put-cell(x, y, width, height, fill) = place(
  top + left,
  dx: x,
  dy: y,
  box(
    width: width * 1pt,
    height: height * 1pt,
    fill: fill,
    stroke: 1pt + grid-line,
    inset: 0pt,
  ),
)

#let day-header(d) = {
  let header-color = if d.today { accent } else { ink }
  let date-color = if d.today { accent } else { muted }
  if d.date == "" {
    box(
      width: 100%,
      height: 54pt,
      align(center + horizon, text(weight: "bold", fill: header-color)[#d.label]),
    )
  } else {
    stack(
      dir: ttb,
      spacing: 0pt,
      box(
        width: 100%,
        height: 28pt,
        align(center + horizon, text(weight: "bold", fill: header-color)[#d.label]),
      ),
      box(
        width: 100%,
        height: 26pt,
        align(center + horizon, text(size: 9pt, fill: date-color)[#d.date]),
      ),
    )
  }
}

#let period-label(p) = stack(
  dir: ttb,
  spacing: 0pt,
  box(
    width: 100%,
    height: 28pt,
    align(center + horizon, text(weight: "bold")[#p.label]),
  ),
  box(
    width: 100%,
    height: 28pt,
    align(center + horizon, text(size: 9pt, fill: muted)[#p.time]),
  ),
)

#let item-course-size(i) = if i.course_size > 0 {
  i.course_size * 1pt
} else if i.large {
  18pt
} else {
  14pt
}

#let item-meta-size(i) = if i.meta_size > 0 {
  i.meta_size * 1pt
} else if i.large {
  13pt
} else {
  10pt
}

#let item-content(i) = {
  let course-size = item-course-size(i)
  let meta-size = item-meta-size(i)
  if i.location == "" and i.weeks == "" {
    align(center + horizon, text(size: course-size, weight: "bold")[#i.course])
  } else if i.weeks == "" {
    align(center + horizon, stack(
      dir: ttb,
      spacing: 0pt,
      box(
        width: 100%,
        height: 22pt,
        align(center + horizon, text(size: course-size, weight: "bold")[#i.course]),
      ),
      box(
        width: 100%,
        height: 22pt,
        align(center + horizon, text(size: meta-size, fill: muted)[#i.location]),
      ),
    ))
  } else if i.location == "" {
    align(center + horizon, stack(
      dir: ttb,
      spacing: 0pt,
      box(
        width: 100%,
        height: 22pt,
        align(center + horizon, text(size: course-size, weight: "bold")[#i.course]),
      ),
      box(
        width: 100%,
        height: 22pt,
        align(center + horizon, text(size: meta-size, fill: accent)[#i.weeks]),
      ),
    ))
  } else {
    align(center + horizon, stack(
      dir: ttb,
      spacing: 0pt,
      box(
        width: 100%,
        height: 18pt,
        align(center + horizon, text(size: course-size, weight: "bold")[#i.course]),
      ),
      box(
        width: 100%,
        height: 18pt,
        align(center + horizon, text(size: meta-size, fill: muted)[#i.location]),
      ),
      box(
        width: 100%,
        height: 18pt,
        align(center + horizon, text(size: meta-size, fill: accent)[#i.weeks]),
      ),
    ))
  }
}

#let item-box(i) = {
  let x = margin + data.label_width * 1pt + i.day * data.day_width * 1pt
  let y = grid-top + data.header_height * 1pt + (i.start - 1) * data.row_height * 1pt
  let height = (i.end - i.start + 1) * data.row_height * 1pt
  let course-fill = if i.color == "" { neutral-course-bg } else { rgb(i.color) }
  place(
    top + left,
    dx: x,
    dy: y,
    box(
      width: data.day_width * 1pt,
      height: height,
      fill: course-fill,
      stroke: 1pt + accent,
      inset: 1pt,
      item-content(i),
    ),
  )
}

// Header and body cells are painted first, then course blocks, then the
// today/divider overlays to match the legacy draw order.
#block(width: page-width, height: page-height)[
  #place(top + left, dx: margin, dy: 35pt, text(size: 18pt, weight: "bold")[#data.title])
  #place(top + right, dx: -margin, dy: 35pt, text(size: 9pt, fill: muted)[#data.summary])

  #put-cell(margin, grid-top, data.label_width, data.header_height, rgb("#f4f4f5"))
  #place(
    top + left,
    dx: margin + data.label_width / 2 * 1pt,
    dy: grid-top + data.header_height / 2 * 1pt,
    align(center + horizon, text(weight: "bold")[节次]),
  )

  #for (day-index, day) in data.days.enumerate() [
    #let x = margin + data.label_width * 1pt + day-index * data.day_width * 1pt
    #let fill = if day.today { today-header-bg } else { rgb("#f4f4f5") }
    #put-cell(x, grid-top, data.day_width, data.header_height, fill)
  #place(
    top + left,
    dx: x,
    dy: grid-top,
    box(
      width: data.day_width * 1pt,
      height: data.header_height * 1pt,
      align(center + horizon, day-header(day)),
    ),
  )
  ]

  #for (period-index, period) in data.periods.enumerate() [
    #let y = grid-top + data.header_height * 1pt + period-index * data.row_height * 1pt
    #let row-fill = if calc.rem(period-index, 2) == 1 { alternate-bg } else { bg }
    #put-cell(margin, y, data.label_width, data.row_height, row-fill)
  #place(
    top + left,
    dx: margin,
    dy: y,
    box(
      width: data.label_width * 1pt,
      height: data.row_height * 1pt,
      align(center + horizon, period-label(period)),
    ),
  )
    #for (day-index, day) in data.days.enumerate() [
      #let x = margin + data.label_width * 1pt + day-index * data.day_width * 1pt
      #let fill = if day.today { today-bg } else { row-fill }
      #put-cell(x, y, data.day_width, data.row_height, fill)
    ]
  ]

  #for item in data.items [#item-box(item)]

  #for (day-index, day) in data.days.enumerate() [
    #if day.today [
      #let x = margin + data.label_width * 1pt + day-index * data.day_width * 1pt
      #place(top + left, dx: x, dy: grid-top, box(width: 2pt, height: grid-height, fill: accent))
      #place(top + left, dx: x + (data.day_width - 2) * 1pt, dy: grid-top, box(width: 2pt, height: grid-height, fill: accent))
    ]
  ]

  #for boundary in data.dividers [
    #let y = grid-top + data.header_height * 1pt + boundary * data.row_height * 1pt - divider-height / 2
    #place(top + left, dx: margin, dy: y, box(width: grid-width, height: divider-height, fill: divider-color))
  ]

  #grid-footer(data.footer)
]

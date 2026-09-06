// Static iPhone information screens. The page is at least one 390 × 844pt
// viewport tall and extends to keep all content visible. Default output is 3×.
#let card-width = 390pt
#let screen-height = 844pt
#let margin-x = 20pt
#let margin-top = 32pt
#let margin-bottom = 24pt
#let content-width = card-width - 2 * margin-x
#let body-size = 17pt
#let caption-size = 13pt
#let subhead-size = 15pt
#let title-size = 34pt
#let section-size = 22pt
#let section-gap = 28pt
#let surface-pad = 16pt
#let surface-radius = 18pt

#let bg = rgb("#f2f2f7")
#let surface = rgb("#ffffff")
#let ink = rgb("#1c1c1e")
#let muted = rgb("#636366")
#let line-c = rgb("#e5e5ea")
#let accent = rgb("#0066cc")
#let highlight-bg = rgb("#edf5ff")
#let card-fonts = ("Noto Sans CJK SC", "Source Han Sans CN", "Fira Code")

#let card-page(body) = {
  set text(font: card-fonts, size: body-size, fill: ink, lang: "zh", hyphenate: false,
    top-edge: "bounds", bottom-edge: "bounds")
  set par(leading: 5pt, spacing: 0pt)
  set block(spacing: 0pt)
  context {
    let natural-height = measure(body, width: content-width).height
    set page(width: card-width,
      height: calc.max(screen-height, natural-height + margin-top + margin-bottom),
      margin: (x: margin-x, top: margin-top, bottom: margin-bottom), fill: bg)
    body
  }
}

#let card-header(title, subtitle: none) = {
  text(size: title-size, weight: "bold")[#title]
  if subtitle != none and subtitle != "" {
    v(10pt)
    text(size: subhead-size, fill: muted)[#subtitle]
  }
  v(section-gap)
}

#let card-section(title) = {
  text(size: section-size, weight: "semibold")[#title]
  v(12pt)
}

#let card-surface(body, inset: surface-pad, fill: surface) = block(
  width: 100%, fill: fill, radius: surface-radius, inset: inset,
  breakable: false,
)[#body]

// Flexible space anchors the footer to the screen bottom on short cards.
// Its measured natural height is zero, so long cards retain normal flow.
#let card-footer(lines, meta: none) = {
  v(1fr)
  v(28pt)
  set text(size: caption-size, fill: muted)
  if meta != none and meta != "" {
    [#meta]
    v(10pt)
  }
  if lines.len() == 1 {
    [#lines.first()]
  } else if lines.len() > 1 {
    grid(
      columns: (1fr, auto), column-gutter: 12pt,
      [#lines.last()], align(right)[#lines.at(lines.len() - 2)],
    )
  }
}

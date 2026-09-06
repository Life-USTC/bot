// A phone-sized canvas and type scale for every card. All lengths are
// logical points; the default 3x rasterization produces a 1170px-wide PNG.
#let card-width = 390pt
#let margin-x = 20pt
#let content-width = card-width - 2 * margin-x
#let body-size = 17pt
#let caption-size = 13pt
#let title-size = 24pt
#let section-size = 19pt
#let section-gap = 24pt
#let cell-pad-x = 10pt
#let cell-pad-y = 10pt

#let bg = rgb("#fafafa")
#let ink = rgb("#27272a")
#let muted = rgb("#52525b")
#let line-c = rgb("#d4d4d8")
#let highlight-bg = rgb("#f0fdfa")
#let accent = rgb("#0f766e")
#let card-fonts = ("Noto Sans CJK SC", "Source Han Sans CN", "Fira Code")
#let mono-fonts = ("Fira Code", "Noto Sans CJK SC", "Source Han Sans CN")

#let card-page(body) = {
  set page(width: card-width, height: auto, margin: margin-x, fill: bg)
  set text(font: card-fonts, size: body-size, fill: ink, lang: "zh", hyphenate: false)
  set par(leading: 0.5em, spacing: 0pt)
  set block(spacing: 0pt)
  body
}

#let card-header(title, subtitle: none) = {
  text(size: title-size, weight: "bold")[#title]
  if subtitle != none and subtitle != "" {
    v(8pt)
    text(size: caption-size, fill: muted)[#subtitle]
  }
  v(section-gap)
}

#let card-section(title) = {
  text(size: section-size, weight: "bold")[#title]
  v(12pt)
}

// Keep the footer in normal flow so wrapped body content can never cover it.
#let card-footer(lines, meta: none) = {
  v(section-gap)
  line(length: 100%, stroke: 0.6pt + line-c)
  v(10pt)
  set text(size: caption-size, fill: muted)
  if meta != none and meta != "" {
    [#meta]
    v(8pt)
  }
  if lines.len() == 1 {
    [#lines.first()]
  } else if lines.len() > 1 {
    grid(
      columns: (1fr, auto),
      column-gutter: 12pt,
      [#lines.last()],
      align(right)[#lines.at(lines.len() - 2)],
    )
  }
}

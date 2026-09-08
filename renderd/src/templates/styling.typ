// The pre-Typst cards use a flat paper canvas, quiet type and table rules.
#let content-width = 480pt
#let page-margin = 32pt
#let ground = rgb("#fafafa")
#let ink = rgb("#27272a")
#let muted = rgb("#71717a")
#let border = rgb("#d4d4d8")
#let accent = rgb("#0f766e")
#let accent-soft = rgb("#f4f4f5")
#let text-fonts = ("Fira Code", "Source Han Sans CN", "Noto Sans CJK SC")
#let type-scale = (
  caption: (size: 9pt, weight: "regular", fill: muted),
  body: (size: 13pt, weight: "regular", fill: ink),
  title: (size: 18pt, weight: "bold", fill: ink))
#let styled(role, body, weight: auto, fill: auto) = {
  let spec = type-scale.at(role)
  text(font: text-fonts, size: spec.size,
    weight: if weight == auto { spec.weight } else { weight },
    fill: if fill == auto { spec.fill } else { fill }, body)
}
#let caption-text(body, ..args) = styled("caption", body, ..args)
#let body-text(body, ..args) = styled("body", body, ..args)
#let title-text(body, ..args) = styled("title", body, ..args)
#let pad = 8pt
#let hairline = 1pt

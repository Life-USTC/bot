// Shared palette, metrics, watermark and footer for all card templates.
// Values mirror richRenderMetrics / the draw colors in rich_render.go.
// Units are logical pt == legacy logical px.

#let bg = rgb("#fafafa")
#let ink = rgb("#27272a")
#let muted = rgb("#71717a")
#let line-c = rgb("#d4d4d8")
#let highlight-bg = rgb("#f4f4f5")
#let accent = rgb("#0f766e")

#let card-fonts = ("Fira Code", "Noto Sans CJK SC", "Source Han Sans CN")

// richRenderMetrics (logical px):
#let margin-x = 32pt
#let content-top = 82pt
#let header-h = 28pt
#let row-h = 32pt
#let text-pad-x = 8pt
#let cell-pad-x = 8pt
#let col-gap = 20pt
#let row-gap = 30pt
// footer occupies FooterGap(24) + FooterLineGap(14) + BottomMargin(32) = 70pt
#let footer-zone = 70pt

// Watermark: legacy drawBusLogoWatermark puts the 120pt logo's center 30pt
// (size/4) beyond the bottom-right corner, rotated -30deg, at 15% opacity
// (pre-baked into assets/logo-15.png by renderd). Place first in the
// template so all text draws over it.
#let card-watermark() = place(
  bottom + right,
  dx: 90pt,
  dy: 90pt,
  rotate(-30deg, image("assets/logo-15.png", width: 120pt)),
)

// Footer: two right-aligned 9pt muted lines. Legacy anchors text baselines:
// line 2's baseline sits BottomMargin(32) above the page bottom, line 1's
// exactly FooterLineGap(14) above line 2's. Two separate place() calls with
// identical font/size give an exact 14pt baseline gap (same descent lifts
// both baselines off their box bottoms); footer-dy then shifts both boxes so
// line 2's baseline lands at -32pt (descent of the 9pt faces is ~2pt).
#let card-footer(lines) = {
  if lines.len() > 1 {
    place(
      bottom + right,
      dx: -margin-x,
      dy: -30pt - 14pt,
      text(size: 9pt, fill: muted)[#lines.at(0)],
    )
    place(
      bottom + right,
      dx: -margin-x,
      dy: -30pt,
      text(size: 9pt, fill: muted)[#lines.at(1)],
    )
  }
}

// Faithful Typst counterpart of internal/responses/weather_render.go. The
// logical canvas is intentionally wider than the other cards because this is
// the legacy weather card's established 920-point composition.

#import "styling.typ": *

#let data = __DATA__
#let canvas-width = 920pt
#let margin = 52pt
#let content-width = canvas-width - 2 * margin
#let chart-row = 158pt
#let chart-labels = 20pt
#let chart-plot-bottom = 124
#let block-gap = 30pt

#let weather-sun = rgb("#f59e0b")
#let weather-cloud = rgb("#a1a1aa")
#let weather-drop = rgb("#38bdf8")
#let weather-sky = rgb("#7dd3fc")
#let weather-drop-dark = rgb("#0284c7")
#let weather-bolt = rgb("#d97706")
#let weather-hail = rgb("#64748b")
#let weather-line = rgb("#e4e4e7")
#let weather-tile = rgb("#ffffff")
#let weather-chart-fill = weather-sun.transparentize(90%)
#let weather-bar-fill = weather-sky.transparentize(10%)


#set page(
  width: canvas-width,
  height: auto,
  margin: 0pt,
  fill: ground,
)
#set text(font: text-fonts, size: 13pt, fill: ink, lang: "zh")
#set block(spacing: 0pt)

#let placed-text(value, size: 13pt, color: ink, weight: "regular") = {
  text(size: size, fill: color, weight: weight)[#value]
}

#let glyph-dot(x, y, radius, color) = {
  place(top + left, dx: x, dy: y, circle(radius: radius, fill: color, stroke: none))
}

#let glyph-cloud(x: 0pt, y: 0pt, size: 64pt, color: weather-cloud) = {
  // Three overlapping circles plus a low rectangle match weatherDrawCloud.
  place(top + left, dx: x + size * 0.16, dy: y + size * 0.36,
    circle(radius: size * 0.20, fill: color, stroke: none))
  place(top + left, dx: x + size * 0.31, dy: y + size * 0.19,
    circle(radius: size * 0.25, fill: color, stroke: none))
  place(top + left, dx: x + size * 0.56, dy: y + size * 0.42,
    circle(radius: size * 0.16, fill: color, stroke: none))
  place(top + left, dx: x + size * 0.20, dy: y + size * 0.55,
    rect(width: size * 0.62, height: size * 0.19, fill: color, stroke: none))
}

#let glyph-sun(x: 0pt, y: 0pt, size: 64pt) = {
  let cx = x + size / 2
  let cy = y + size / 2
  place(top + left, dx: cx - size * 0.26, dy: cy - size * 0.26,
    circle(radius: size * 0.26, fill: weather-sun, stroke: none))
  // Eight short rays match the legacy glyph's radial line geometry.
  for (dx, dy) in ((0, -0.47), (0.33, -0.33), (0.47, 0), (0.33, 0.33),
                   (0, 0.47), (-0.33, 0.33), (-0.47, 0), (-0.33, -0.33)) {
    let length = calc.sqrt(dx * dx + dy * dy)
    let ux = dx / length
    let uy = dy / length
    place(top + left,
      line(
        start: (cx + ux * size * 0.36, cy + uy * size * 0.36),
        end: (cx + ux * size * 0.48, cy + uy * size * 0.48),
        stroke: size * 0.05 + weather-sun,
      )
    )
  }
}

#let glyph-drop(x, y, color: weather-drop) = {
  place(top + left, dx: x, dy: y,
    rotate(-20deg, rect(width: 3pt, height: 11pt, radius: 1.5pt, fill: color, stroke: none)))
}

#let weather-glyph(icon) = box(width: 64pt, height: 64pt)[
  #if icon == "sun" {
    glyph-sun()
  } else if icon == "cloud-sun" {
    glyph-sun(x: -6pt, y: -5pt, size: 40pt)
    glyph-cloud(x: 9pt, y: 10pt, size: 55pt)
  } else if icon == "cloud-fog" {
    glyph-cloud(y: -5pt, size: 64pt)
    place(top + left, dx: 17pt, dy: 51pt, line(length: 31pt, stroke: 3pt + weather-cloud))
    place(top + left, dx: 22pt, dy: 59pt, line(length: 21pt, stroke: 3pt + weather-cloud))
  } else if icon == "cloud-drizzle" {
    glyph-cloud(y: -5pt, size: 64pt)
    glyph-dot(20pt, 53pt, 2.5pt, weather-drop)
    glyph-dot(32pt, 53pt, 2.5pt, weather-drop)
    glyph-dot(44pt, 53pt, 2.5pt, weather-drop)
  } else if icon == "cloud-rain" {
    glyph-cloud(y: -5pt, size: 64pt)
    glyph-drop(22pt, 48pt)
    glyph-drop(34pt, 48pt)
    glyph-drop(46pt, 48pt)
  } else if icon == "cloud-snow" {
    glyph-cloud(y: -5pt, size: 64pt)
    glyph-dot(19pt, 53pt, 3pt, weather-drop)
    glyph-dot(32pt, 53pt, 3pt, weather-drop)
    glyph-dot(45pt, 53pt, 3pt, weather-drop)
  } else if icon == "cloud-hail" {
    glyph-cloud(y: -5pt, size: 64pt)
    glyph-dot(19pt, 53pt, 3pt, weather-hail)
    glyph-dot(32pt, 53pt, 3pt, weather-hail)
    glyph-dot(45pt, 53pt, 3pt, weather-hail)
  } else if icon == "cloud-lightning" {
    glyph-cloud(y: -6pt, size: 64pt)
    polygon(fill: weather-bolt, stroke: none,
      (35pt, 37pt), (28pt, 51pt), (36pt, 51pt), (29pt, 64pt),
      (43pt, 47pt), (36pt, 47pt))
  } else {
    glyph-cloud(size: 64pt)
  }
]

#let stat-tile(label, value) = block(
  width: 100%, inset: (x: 12pt, top: 12pt, bottom: 15pt),
  fill: weather-tile, stroke: 1pt + weather-line,
  stack(spacing: 12pt,
    placed-text(label, size: 9pt, color: muted),
    placed-text(value, size: 13pt)))

#let section-heading(value) = block(inset: (top: 12pt, bottom: 10pt),
  placed-text(value, weight: "bold"))

#let centered-label(value, x, y, width: 60pt, size: 9pt, color: muted) = {
  place(top + left, dx: x * 1pt - width / 2, dy: y * 1pt,
    box(width: width, align(center + horizon, placed-text(value, size: size, color: color))))
}

#let hourly-chart(points, plot) = {
  let label-step = calc.max(1, int(calc.ceil(points.len() / 24)))
  let axis-step = calc.max(3, int(calc.ceil(points.len() / 8)))
  block(width: content-width, height: chart-row + chart-labels)[
    // The area and curve use the same sampled Catmull-Rom vertices as Go.
    #if plot.area.len() > 2 {
      place(top + left,
        polygon(fill: weather-chart-fill, stroke: none,
          ..plot.area.map(p => (p.at(0) * 1pt, p.at(1) * 1pt))))
    }
    #for segment in plot.segments {
      place(top + left,
        line(start: (segment.x0 * 1pt, segment.y0 * 1pt),
          end: (segment.x1 * 1pt, segment.y1 * 1pt),
          stroke: 2pt + weather-sun))
    }
    // Precipitation bars are drawn after the curve, matching the legacy
    // renderer's paint order.
    #for point in points {
      if point.bar_height > 0 {
        place(top + left,
          dx: (point.x - point.bar_width / 2) * 1pt,
          dy: (chart-plot-bottom - point.bar_height) * 1pt,
          rect(width: point.bar_width * 1pt, height: point.bar_height * 1pt,
            fill: weather-bar-fill, stroke: none))
      }
    }
    #for (index, point) in points.enumerate() {
      if calc.rem(index, label-step) != 0 { continue }
      centered-label(point.temperature_text, point.x, point.y - 16)
      if point.precipitation_label != "" and point.precipitation_label_y > -1 {
        centered-label(point.precipitation_label, point.x, point.precipitation_label_y,
          size: 8pt, color: weather-drop-dark)
      }
    }
    #place(top + left,
        line(start: (0pt, (chart-plot-bottom + 1) * 1pt),
        end: (content-width, (chart-plot-bottom + 1) * 1pt), stroke: 1pt + weather-line))
    #for (index, point) in points.enumerate() {
      if calc.rem(index, axis-step) == 0 {
        centered-label(point.label, point.x, chart-plot-bottom + 16, size: 9pt)
      }
    }
  ]
}

#let daily-bars(days) = {
  let track-left = 116pt
  let track-right = content-width - 52pt
  for day in days {
    grid(columns: (56pt, 44pt, 16pt, 1fr, 52pt), rows: (auto,),
      align: (left + horizon, right + horizon, center + horizon, left + horizon, right + horizon),
      inset: (y: 9pt),
      placed-text(day.label),
      placed-text(day.low_text, color: muted), [],
      box(width: 100%, height: 7pt, {
        rect(width: 100%, height: 7pt, fill: weather-line, stroke: none)
        if day.fill_width > 0 {
          place(top + left, dx: (day.fill_left * 1pt) - track-left,
            rect(width: day.fill_width * 1pt, height: 7pt, radius: 3.5pt,
              fill: gradient.linear(weather-sky, weather-sun, angle: 0deg), stroke: none))
        }
      }),
      placed-text(day.high_text))
  }
}

#let weather-location(location) = block(inset: (left: margin, right: margin))[
  #block(inset: (top: 8pt, bottom: 14pt),
    placed-text(location.name, size: 16pt, weight: "bold"))
  #block(inset: (top: 18pt, bottom: 28pt))[
    #grid(columns: (64pt, auto, 1fr), column-gutter: (24pt, 16pt), align: left + horizon,
      weather-glyph(location.current.icon),
      move(dy: 8pt, placed-text(location.current.temperature_text, size: 56pt)),
      move(dy: 18pt, stack(spacing: 14pt,
        ..(if location.current.condition_text == "" { () } else {
          (placed-text(location.current.condition_text, size: 18pt),)
        }),
        ..(if not location.current.has_range { () } else {
          (placed-text(location.current.high_text + " / " + location.current.low_text,
            color: muted),)
        }))))
  ]
  #if location.current.humidity_text != "" or location.current.wind_text != "" {
    if location.current.humidity_text != "" and location.current.wind_text != "" {
      grid(columns: (1fr, 1fr), column-gutter: 12pt,
        stat-tile("湿度", location.current.humidity_text),
        stat-tile("风", location.current.wind_text))
    } else if location.current.humidity_text != "" {
      stat-tile("湿度", location.current.humidity_text)
    } else {
      stat-tile("风", location.current.wind_text)
    }
    v(12pt)
  }
  #if location.hourly.len() > 0 {
    section-heading("逐小时预报")
    hourly-chart(location.hourly, location.plot)
  }
  #if location.daily.len() > 0 {
    section-heading("每日预报")
    daily-bars(location.daily)
  }
  #if location.alerts.len() > 0 {
    section-heading("天气预警")
    for alert in location.alerts {
      block(inset: (y: 7pt), placed-text(alert, color: weather-bolt))
    }
  }
]

#block(inset: (left: margin, right: margin, top: 35pt, bottom: 15pt),
  placed-text(data.title, size: 18pt, weight: "bold"))
#for (index, location) in data.locations.enumerate() {
  weather-location(location)
  block(height: block-gap, inset: (left: margin, right: margin))[
    #if index < data.locations.len() - 1 {
      place(top + left, dy: block-gap / 2,
        line(length: content-width, stroke: 1pt + weather-line))
    }
  ]
}
#block(inset: (left: margin, right: margin, top: 8pt, bottom: 16pt))[
  #grid(columns: (1fr, auto), column-gutter: 24pt, align: left + bottom,
    placed-text(data.meta, size: 9pt, color: muted),
    align(right, {
      for (i, value) in data.footer.enumerate() {
        if i > 0 { v(7pt) }
        block(placed-text(value, size: 9pt, color: muted))
      }
    }))
]

// Phone-readable weather card. Width, type scale, page flow, and footer
// styling come from the shared common.typ helpers.
#import "common.typ": *

#let data = __DATA__

#let chart-height = 160pt
#let chart-plot-bottom = 120
#let chart-label-y = 137
#let chart-label-width = 46pt
#let chart-content-width = content-width
#let daily-track-width = 100%

#let weather-sun = rgb("#f59e0b")
#let weather-cloud = rgb("#71717a")
#let weather-drop = rgb("#0ea5e9")
#let weather-sky = rgb("#7dd3fc")
#let weather-drop-text = rgb("#075985")
#let weather-bolt = rgb("#92400e")
#let weather-hail = rgb("#475569")
#let weather-line = rgb("#d4d4d8")
#let weather-tile = rgb("#ffffff")
#let weather-chart-fill = weather-sun.transparentize(86%)
#let weather-bar-fill = weather-sky.transparentize(25%)
#let weather-alert-bg = rgb("#fff7ed")
#let weather-alert-line = rgb("#fdba74")

#let placed-text(value, size: body-size, color: ink, weight: "regular") = {
  text(size: size, fill: color, weight: weight)[#value]
}

#let glyph-dot(x, y, radius, color) = {
  place(top + left, dx: x, dy: y,
    circle(radius: radius, fill: color, stroke: none))
}

#let glyph-cloud(x: 0pt, y: 0pt, size: 64pt, color: weather-cloud) = {
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
    rotate(-20deg, rect(width: 3pt, height: 11pt, radius: 1.5pt,
      fill: color, stroke: none)))
}

#let weather-glyph(icon) = box(width: 64pt, height: 64pt)[
  #if icon == "sun" {
    glyph-sun()
  } else if icon == "cloud-sun" {
    glyph-sun(x: -6pt, y: -5pt, size: 40pt)
    glyph-cloud(x: 9pt, y: 10pt, size: 55pt)
  } else if icon == "cloud-fog" {
    glyph-cloud(y: -5pt, size: 64pt)
    place(top + left, dx: 17pt, dy: 51pt,
      line(length: 31pt, stroke: 3pt + weather-cloud))
    place(top + left, dx: 22pt, dy: 59pt,
      line(length: 21pt, stroke: 3pt + weather-cloud))
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
  width: 100%,
  fill: weather-tile,
  stroke: 0.7pt + weather-line,
  inset: (left: cell-pad-x, right: cell-pad-x,
          top: cell-pad-y, bottom: cell-pad-y),
)[
  #text(size: caption-size, fill: muted)[#label]
  #v(4pt)
  #text(size: body-size, fill: ink)[#value]
]

#let hero-details(current) = {
  text(size: 44pt, weight: "bold")[#current.temperature_text]
  if current.condition_text != "" {
    v(5pt)
    text(size: body-size, weight: "bold")[#current.condition_text]
  }
  if current.has_range {
    v(3pt)
    text(size: caption-size, fill: muted)[
      #(current.high_text + " / " + current.low_text)
    ]
  }
}

#let centered-label(value, x, y, color: muted) = {
  place(top + left,
    dx: (x * 1pt) - chart-label-width / 2,
    dy: y * 1pt,
    box(width: chart-label-width,
      align(center + horizon,
        text(size: caption-size, fill: color)[#value])))
}

#let hourly-chart(location) = {
  v(section-gap)
  card-section("逐小时预报")
  block(width: chart-content-width, height: chart-height)[
    #if location.plot.area.len() > 2 {
      place(top + left,
        polygon(fill: weather-chart-fill, stroke: none,
          ..location.plot.area.map(p => (p.at(0) * 1pt, p.at(1) * 1pt))))
    }
    #for segment in location.plot.segments {
      place(top + left,
        line(start: (segment.x0 * 1pt, segment.y0 * 1pt),
          end: (segment.x1 * 1pt, segment.y1 * 1pt),
          stroke: 2pt + weather-sun))
    }
    // Bars retain every precipitation probability; only their text summary
    // is sparse enough to read at phone width.
    #for point in location.hourly {
      if point.bar_height > 0 {
        place(top + left,
          dx: (point.x - point.bar_width / 2) * 1pt,
          dy: (chart-plot-bottom - point.bar_height) * 1pt,
          rect(width: point.bar_width * 1pt,
            height: point.bar_height * 1pt,
            fill: weather-bar-fill, stroke: none))
      }
    }
    #place(top + left,
      line(start: (0pt, (chart-plot-bottom + 1) * 1pt),
        end: (chart-content-width, (chart-plot-bottom + 1) * 1pt),
        stroke: 0.8pt + weather-line))
    #for point in location.hourly {
      if point.show_temperature {
        centered-label(point.temperature_text, point.x, point.y - 15)
      }
      if point.show_axis_label {
        centered-label(point.label, point.x, chart-label-y)
      }
    }
  ]
  grid(
    columns: (10pt, 1fr),
    column-gutter: 8pt,
    align: left + horizon,
    rect(width: 10pt, height: 10pt, fill: weather-bar-fill, stroke: none),
    text(size: caption-size, fill: weather-drop-text)[降水概率（蓝柱）],
  )
  v(5pt)
  text(size: caption-size, fill: muted)[#location.precipitation_summary]
}

#let daily-label(day) = {
  text(size: body-size, weight: "bold")[#day.label]
  if day.condition_text != "" {
    v(2pt)
    text(size: caption-size, fill: muted)[#day.condition_text]
  }
}

#let daily-bar(day) = box(width: 100%, height: 14pt)[
  #place(top + left, dx: 0pt, dy: 4pt,
    rect(width: 100%, height: 6pt, radius: 3pt,
      fill: weather-line, stroke: none))
  #if day.fill_width > 0 {
    place(top + left, dx: day.fill_left * daily-track-width, dy: 4pt,
      rect(width: day.fill_width * daily-track-width, height: 6pt,
        radius: 3pt,
        fill: gradient.linear(weather-sky, weather-sun, angle: 0deg),
        stroke: none))
  } else {
    place(top + left, dx: day.fill_left * daily-track-width - 3pt, dy: 3pt,
      circle(radius: 3pt, fill: weather-sun, stroke: none))
  }
]

#let daily-temperature(value, color: ink) = {
  text(size: body-size, fill: color)[#value]
}

#let daily-bars(days) = {
  for (index, day) in days.enumerate() {
    grid(
      columns: (62pt, 1fr, auto, auto),
      column-gutter: 8pt,
      align: left + horizon,
      daily-label(day),
      daily-bar(day),
      daily-temperature(day.low_text, color: muted),
      daily-temperature(day.high_text, color: ink),
    )
    if index + 1 < days.len() {
      v(8pt)
    }
  }
}

#let weather-alert(alert) = block(
  fill: weather-alert-bg,
  stroke: 0.7pt + weather-alert-line,
  inset: (left: cell-pad-x, right: cell-pad-x,
          top: cell-pad-y, bottom: cell-pad-y),
)[
  #text(size: body-size, fill: weather-bolt)[#alert]
]

#let weather-location(location) = {
  card-section(location.name)
  grid(
    columns: (64pt, 1fr),
    column-gutter: 16pt,
    align: left + top,
    weather-glyph(location.current.icon),
    block(width: 100%)[#hero-details(location.current)],
  )

  if location.current.humidity_text != "" or location.current.wind_text != "" {
    v(16pt)
    if location.current.humidity_text != "" {
      stat-tile("湿度", location.current.humidity_text)
    }
    if location.current.humidity_text != "" and location.current.wind_text != "" {
      v(8pt)
    }
    if location.current.wind_text != "" {
      stat-tile("风", location.current.wind_text)
    }
  }

  if location.hourly.len() > 0 {
    hourly-chart(location)
  }
  if location.daily.len() > 0 {
    v(section-gap)
    card-section("每日预报")
    daily-bars(location.daily)
  }
  if location.alerts.len() > 0 {
    v(section-gap)
    card-section("天气预警")
    for (index, alert) in location.alerts.enumerate() {
      weather-alert(alert)
      if index + 1 < location.alerts.len() {
        v(8pt)
      }
    }
  }
}

#card-page[
  #card-header(data.title)
  #for (index, location) in data.locations.enumerate() {
    if index > 0 {
      v(12pt)
      line(length: 100%, stroke: 0.8pt + weather-line)
      v(12pt)
    }
    weather-location(location)
  }
  #card-footer(data.footer, meta: data.meta)
]

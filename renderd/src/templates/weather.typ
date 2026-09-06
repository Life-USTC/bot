// Static iPhone weather screen. The shared page and type tokens come from
// common.typ; this template only arranges the weather data into grouped
// surfaces that remain readable as their text grows.
#import "common.typ": *

#let data = __DATA__

#let chart-height = 160pt
#let chart-plot-bottom = 120
#let chart-label-y = 137
#let chart-label-width = 42pt
#let chart-content-width = 318pt
#let daily-track-width = 100%

#let weather-sun = rgb("#f59e0b")
#let weather-cloud = rgb("#71717a")
#let weather-drop = rgb("#0ea5e9")
#let weather-sky = rgb("#7dd3fc")
#let weather-drop-text = rgb("#075985")
#let weather-warning = rgb("#c2410c")
#let weather-hail = rgb("#475569")
#let weather-line = line-c
#let weather-chart-fill = weather-sun.transparentize(86%)
#let weather-bar-fill = weather-sky.transparentize(25%)
#let weather-alert-bg = rgb("#fff7ed")
#let weather-alert-line = rgb("#fdba74")

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

#let weather-glyph(icon, size: 64pt) = box(width: size, height: size)[
  #if icon == "sun" {
    glyph-sun(size: size)
  } else if icon == "cloud-sun" {
    glyph-sun(x: -6pt, y: -5pt, size: size * 0.63)
    glyph-cloud(x: size * 0.14, y: size * 0.16, size: size * 0.86)
  } else if icon == "cloud-fog" {
    glyph-cloud(y: -5pt, size: size)
    place(top + left, dx: size * 0.27, dy: size * 0.80,
      line(length: size * 0.48, stroke: 3pt + weather-cloud))
    place(top + left, dx: size * 0.34, dy: size * 0.92,
      line(length: size * 0.33, stroke: 3pt + weather-cloud))
  } else if icon == "cloud-drizzle" {
    glyph-cloud(y: -5pt, size: size)
    glyph-dot(size * 0.31, size * 0.83, 2.5pt, weather-drop)
    glyph-dot(size * 0.50, size * 0.83, 2.5pt, weather-drop)
    glyph-dot(size * 0.69, size * 0.83, 2.5pt, weather-drop)
  } else if icon == "cloud-rain" {
    glyph-cloud(y: -5pt, size: size)
    glyph-drop(size * 0.34, size * 0.75)
    glyph-drop(size * 0.53, size * 0.75)
    glyph-drop(size * 0.72, size * 0.75)
  } else if icon == "cloud-snow" {
    glyph-cloud(y: -5pt, size: size)
    glyph-dot(size * 0.30, size * 0.83, 3pt, weather-drop)
    glyph-dot(size * 0.50, size * 0.83, 3pt, weather-drop)
    glyph-dot(size * 0.70, size * 0.83, 3pt, weather-drop)
  } else if icon == "cloud-hail" {
    glyph-cloud(y: -5pt, size: size)
    glyph-dot(size * 0.30, size * 0.83, 3pt, weather-hail)
    glyph-dot(size * 0.50, size * 0.83, 3pt, weather-hail)
    glyph-dot(size * 0.70, size * 0.83, 3pt, weather-hail)
  } else if icon == "cloud-lightning" {
    glyph-cloud(y: -6pt, size: size)
    polygon(fill: weather-warning, stroke: none,
      (size * 0.55, size * 0.58), (size * 0.44, size * 0.80),
      (size * 0.56, size * 0.80), (size * 0.45, size),
      (size * 0.67, size * 0.73), (size * 0.56, size * 0.73))
  } else {
    glyph-cloud(size: size)
  }
]

#let stat-cell(label, value) = block(width: 100%)[
  #text(size: caption-size, fill: muted)[#label]
  #v(4pt)
  #text(size: body-size, fill: ink)[#value]
]

#let weather-stats(current) = {
  let has-humidity = current.humidity_text != ""
  let has-wind = current.wind_text != ""
  if has-humidity and has-wind {
    card-surface[
      #grid(
        columns: (1fr, 1fr), column-gutter: 12pt,
        stat-cell("湿度", current.humidity_text),
        stat-cell("风", current.wind_text),
      )
    ]
  } else if has-humidity {
    card-surface[stat-cell("湿度", current.humidity_text)]
  } else if has-wind {
    card-surface[stat-cell("风", current.wind_text)]
  }
}

#let weather-hero(current) = card-surface[
  #grid(
    columns: (64pt, 1fr), column-gutter: 16pt, align: left + top,
    weather-glyph(current.icon),
    block(width: 100%)[
      #text(size: 52pt, weight: "bold")[#current.temperature_text]
      #if current.condition_text != "" {
        v(4pt)
        text(size: body-size, weight: "semibold")[#current.condition_text]
      }
      #if current.has_range {
        v(3pt)
        text(size: subhead-size, fill: muted)[
          #("最高 " + current.high_text + " · 最低 " + current.low_text)
        ]
      }
    ],
  )
]

#let centered-label(value, x, y, color: muted) = {
  place(top + left,
    dx: (x * 1pt) - chart-label-width / 2,
    dy: y * 1pt,
    box(width: chart-label-width,
      align(center + horizon,
        text(size: caption-size, fill: color)[#value])))
}

#let hourly-panel(location) = card-surface[
  #box(width: chart-content-width, height: chart-height)[
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
    // Every hourly point remains represented by its bar and curve position.
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
  #grid(
    columns: (10pt, 1fr), column-gutter: 8pt, align: left + horizon,
    rect(width: 10pt, height: 10pt, radius: 2pt,
      fill: weather-bar-fill, stroke: none),
    text(size: caption-size, fill: weather-drop-text)[降水概率（蓝柱）],
  )
  #if location.precipitation_summary != "" {
    v(5pt)
    text(size: caption-size, fill: muted)[#location.precipitation_summary]
  }
]

#let daily-label(day) = block(width: 100%)[
  #text(size: body-size, weight: "semibold")[#day.label]
  #if day.condition_text != "" {
    v(2pt)
    text(size: caption-size, fill: muted)[#day.condition_text]
  }
]

#let daily-temperature(label, value, color) = block(width: 100%)[
  #text(size: caption-size, fill: muted)[#label]
  #v(2pt)
  #text(size: body-size, fill: color, weight: "semibold")[#value]
]

#let daily-bar(day) = box(width: 100%, height: 18pt)[
  #place(top + left, dx: 0pt, dy: 6pt,
    rect(width: 100%, height: 6pt, radius: 3pt,
      fill: weather-line, stroke: none))
  #if day.fill_width > 0 {
    place(top + left, dx: day.fill_left * daily-track-width, dy: 6pt,
      rect(width: day.fill_width * daily-track-width, height: 6pt,
        radius: 3pt,
        fill: gradient.linear(weather-sky, weather-sun, angle: 0deg),
        stroke: none))
  } else {
    place(top + left, dx: day.fill_left * daily-track-width - 3pt, dy: 4pt,
      circle(radius: 3pt, fill: weather-sun, stroke: none))
  }
]

#let daily-forecast(days) = card-surface[
  #for (index, day) in days.enumerate() {
    grid(
      columns: (72pt, 38pt, 1fr, 38pt), column-gutter: 8pt,
      align: left + top,
      daily-label(day),
      daily-temperature("低", day.low_text, muted),
      daily-bar(day),
      daily-temperature("高", day.high_text, ink),
    )
    if index + 1 < days.len() {
      v(10pt)
      line(length: 100%, stroke: 0.6pt + line-c)
      v(10pt)
    }
  }
]

#let alert-row(alert) = {
  grid(
    columns: (8pt, 1fr), column-gutter: 10pt, align: left + top,
    align(center + horizon, circle(radius: 3.5pt, fill: weather-warning,
      stroke: none)),
    text(size: body-size, fill: ink)[#alert],
  )
}

#let alert-panel(alerts) = card-surface(fill: weather-alert-bg)[
  #for (index, alert) in alerts.enumerate() {
    alert-row(alert)
    if index + 1 < alerts.len() {
      v(10pt)
      line(length: 100%, stroke: 0.6pt + weather-alert-line)
      v(10pt)
    }
  }
]

#let weather-location(location) = {
  card-section(location.name)
  weather-hero(location.current)

  if location.current.humidity_text != "" or location.current.wind_text != "" {
    v(12pt)
    weather-stats(location.current)
  }

  if location.hourly.len() > 0 {
    v(section-gap)
    card-section("逐小时预报")
    hourly-panel(location)
  }
  if location.daily.len() > 0 {
    v(section-gap)
    card-section("每日预报")
    daily-forecast(location.daily)
  }
  if location.alerts.len() > 0 {
    v(section-gap)
    card-section("天气预警")
    alert-panel(location.alerts)
  }
}

#card-page[
  #card-header(data.title, subtitle: data.meta)
  #for (index, location) in data.locations.enumerate() {
    if index > 0 {
      v(section-gap)
      line(length: 100%, stroke: 0.8pt + line-c)
      v(section-gap)
    }
    weather-location(location)
  }
  #card-footer(data.footer)
]

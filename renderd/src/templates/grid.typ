// Phone agenda card for daily and weekly schedules. The shared card helpers
// provide the 390pt page, margins, type scale, and footer flow. This template
// uses a grouped-list composition so each day's schedule reads as one unit.
#import "common.typ": *

#let data = __DATA__

#show: card-page

// Preserve the characters in long identifiers while giving Typst sensible
// break opportunities for values that contain no spaces.
#let break-long-tokens(value) = {
  show regex("[A-Za-z0-9]{24,}"): it => [#it.text.split("").join("\u{200b}")]
  [#value]
}

#let entry-count(count) = str(count) + " 项课程安排"

#let weekly-summary(days, count) = {
  let first-date = days.first().date
  let last-date = days.last().date
  if first-date != "" and last-date != "" {
    if first-date == last-date {
      [#break-long-tokens(first-date) · #entry-count(count)]
    } else {
      [#break-long-tokens(first-date) 至 #break-long-tokens(last-date) · #entry-count(count)]
    }
  } else if first-date != "" {
    [#break-long-tokens(first-date) · #entry-count(count)]
  } else if last-date != "" {
    [#break-long-tokens(last-date) · #entry-count(count)]
  } else {
    [#entry-count(count)]
  }
}

#let course-marker(item) = {
  let color = if item.color == "" { accent } else { rgb(item.color) }
  box(width: 12pt, height: 12pt)[
    #align(center + horizon)[
      #circle(radius: 4pt, fill: color, stroke: none)
    ]
  ]
}

#let schedule-meta(item) = {
  if item.period != "" {
    text(size: caption-size, fill: muted)[#break-long-tokens(item.period)]
  }
  if item.time != "" {
    if item.period != "" [#text(size: caption-size, fill: muted)[ · ]]
    text(size: subhead-size, weight: "medium", fill: accent)[
      #break-long-tokens(item.time)
    ]
  }
}

#let detail-line(label, value) = {
  grid(
    columns: (auto, 1fr),
    column-gutter: 6pt,
    align: (left + horizon, left + horizon),
    text(size: caption-size, fill: muted)[#label],
    text(size: caption-size, fill: muted)[#break-long-tokens(value)],
  )
}

#let schedule-item(item) = {
  grid(
    columns: (12pt, 1fr),
    column-gutter: 10pt,
    align: (center + top, left + top),
    course-marker(item),
    block(width: 100%)[
      #text(size: body-size, weight: "semibold")[
        #break-long-tokens(item.course)
      ]
      #if item.period != "" or item.time != "" {
        v(5pt)
        schedule-meta(item)
      }
      #if item.location != "" {
        v(8pt)
        detail-line("地点", item.location)
      }
      #if item.weeks != "" {
        v(3pt)
        detail-line("周次", item.weeks)
      }
    ],
  )
}

#let list-divider() = block(width: 100%, height: 1pt, fill: line-c)

#let day-badge(day) = {
  if day.date == "" {
    none
  } else {
    let badge-fill = if day.today { highlight-bg } else { surface }
    let badge-color = if day.today { accent } else { muted }
    block(
      fill: badge-fill,
      radius: 10pt,
      inset: (x: 9pt, y: 4pt),
    )[
      #text(size: caption-size, weight: if day.today { "semibold" } else { "regular" }, fill: badge-color)[
        #break-long-tokens(day.date)
      ]
    ]
  }
}

#let day-heading(day) = {
  grid(
    columns: (1fr, auto),
    column-gutter: 8pt,
    align: (left + horizon, right + horizon),
    text(size: section-size, weight: "semibold")[#break-long-tokens(day.label)],
    day-badge(day),
  )
}

#let empty-day() = card-surface(
  grid(
    columns: (12pt, 1fr),
    column-gutter: 10pt,
    align: (center + horizon, left + horizon),
    box(width: 12pt, height: 12pt)[
      #align(center + horizon)[
        #circle(radius: 3pt, fill: line-c, stroke: none)
      ]
    ],
    text(size: subhead-size, fill: muted)[暂无课程],
  ),
  inset: (x: surface-pad, y: 11pt),
)

#let schedule-list(entries) = {
  for (item-index, item) in entries.enumerate() {
    if item-index > 0 {
      v(13pt)
      list-divider()
      v(13pt)
    }
    schedule-item(item)
  }
}

#let day-group(day, entries, show-heading: true) = {
  if show-heading {
    day-heading(day)
    v(10pt)
  }
  if entries.len() == 0 {
    empty-day()
  } else {
    card-surface(
      schedule-list(entries),
      inset: (x: surface-pad, y: surface-pad),
    )
  }
}

#let header-summary = if data.days.len() == 1 {
  let day = data.days.first()
  let entries = data.items.filter(item => item.day == 0)
  [#break-long-tokens(day.label) · #entry-count(entries.len())]
} else {
  weekly-summary(data.days, data.items.len())
}

#card-header(break-long-tokens(data.title), subtitle: header-summary)

#for (day-index, day) in data.days.enumerate() [
  #if day-index > 0 { v(22pt) }
  #let entries = data.items.filter(item => item.day == day-index)
  #day-group(day, entries, show-heading: data.days.len() != 1)
]

#card-footer(data.footer.map(line => break-long-tokens(line)))

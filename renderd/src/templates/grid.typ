// Phone agenda card for daily and weekly schedules. The shared card helpers
// provide the 390pt page, margins, type scale, and footer flow. This template
// uses a grouped-list composition so each day's schedule reads as one unit.
#import "common.typ": *

#let data = __DATA__

#show: card-page

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

#let detail-line(label, value) = text(size: caption-size, fill: muted)[
  #label #break-long-tokens(value)
]

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
        linebreak()
        schedule-meta(item)
      }
      #if item.location != "" {
        linebreak()
        detail-line("地点", item.location)
      }
      #if item.weeks != "" {
        linebreak()
        detail-line("周次", item.weeks)
      }
    ],
  )
}

#let day-date(day) = {
  if day.date == "" {
    none
  } else {
    let badge-color = if day.today { accent } else { muted }
    text(size: caption-size, weight: if day.today { "semibold" } else { "regular" }, fill: badge-color)[
      #break-long-tokens(day.date)
    ]
  }
}

#let day-heading(day) = {
  grid(
    columns: (1fr, auto),
    column-gutter: 8pt,
    align: (left + horizon, right + horizon),
    text(size: section-size, weight: "semibold")[#break-long-tokens(day.label)],
    day-date(day),
  )
}

#let empty-day() = card-surface(
  text(size: subhead-size, fill: muted)[暂无课程],
)

#let schedule-list(entries) = {
  for item in entries {
    schedule-item(item)
  }
}

#let day-group(day, entries, show-heading: true) = {
  if show-heading {
    day-heading(day)
  }
  if entries.len() == 0 {
    empty-day()
  } else {
    card-surface(schedule-list(entries))
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
  #let entries = data.items.filter(item => item.day == day-index)
  #day-group(day, entries, show-heading: data.days.len() != 1)
]

#card-footer(data.footer.map(line => break-long-tokens(line)))

// Phone agenda card for daily and weekly schedules. The shared card helpers
// provide the 390pt page, margins, type scale, and footer flow; this template
// only arranges semantic schedule content.
#import "common.typ": *

#let data = __DATA__

#show: card-page

#let day-title(day) = {
  if day.date == "" {
    day.label
  } else {
    day.label + " · " + day.date
  }
}

#let day-heading(day) = {
  let title = day-title(day)
  if day.today {
    block(
      width: 100%,
      fill: highlight-bg,
      stroke: 1pt + accent,
      radius: 6pt,
      inset: (x: 12pt, y: 8pt),
    )[
      #text(size: section-size, weight: "bold", fill: accent)[#title]
    ]
    v(12pt)
  } else {
    card-section(title)
  }
}

#let course-fill(item) = if item.color == "" {
  rgb("#f4f4f5")
} else {
  rgb(item.color)
}

#let schedule-item(item) = block(
  width: 100%,
  fill: course-fill(item),
  stroke: 1pt + line-c,
  radius: 6pt,
  inset: (x: 12pt, y: 10pt),
)[
  #text(size: caption-size, weight: "bold", fill: accent)[#item.period]
  #if item.time != "" [
    #text(size: caption-size, fill: muted)[ · #item.time]
  ]
  #v(6pt)
  #text(size: body-size, weight: "bold")[#item.course]
  #if item.location != "" {
    v(4pt)
    text(size: caption-size, fill: muted)[地点：#item.location]
  }
  #if item.weeks != "" {
    v(3pt)
    text(size: caption-size, fill: accent)[周次：#item.weeks]
  }
]

#let empty-day() = block(
  width: 100%,
  fill: rgb("#f4f4f5"),
  inset: (x: 12pt, y: 8pt),
  radius: 6pt,
)[
  #text(size: caption-size, fill: muted)[暂无课程]
]

#card-header(data.title, subtitle: data.summary)

#for (day-index, day) in data.days.enumerate() [
  #if day-index > 0 { v(section-gap) }
  #day-heading(day)
  #let entries = data.items.filter(item => item.day == day-index)
  #if entries.len() == 0 {
    empty-day()
  } else {
    for (item-index, item) in entries.enumerate() [
      #if item-index > 0 { v(8pt) }
      #schedule-item(item)
    ]
  }
]

#card-footer(data.footer)

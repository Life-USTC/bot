#import "styling.typ": *
#import "ui-component.typ": *
#let data = __DATA__

#let period-width = 112pt
// Typst measures wrapped titles at the actual cell width. Only the title's
// font size changes; normal grid rows and text layout remain content-driven.
#let course-title(value) = layout(bounds => {
  let title(size) = text(font: ("Source Han Sans CN", "Noto Sans CJK SC"),
    size: size, weight: "bold", lang: "en", hyphenate: true, value)
  let selected = title(10pt)
  for size in (14pt, 12pt) {
    let candidate = title(size)
    if measure(candidate, width: bounds.width).height <= 80pt {
      selected = candidate
      break
    }
  }
  selected
})

#let role-label(kind) = if kind == "teaching_assistant" {
  "助教"
} else if kind == "auditor" {
  "旁听"
} else {
  ""
}

#let role-badge(label) = box(
  fill: rgb("#ef4444"),
  radius: 3pt,
  inset: (x: 6pt, y: 2pt),
  text(font: ("Source Han Sans CN", "Noto Sans CJK SC"), size: 10pt,
    weight: "bold", fill: white, label))

#let role-badge-labels(items) = {
  let labels = ()
  for item in items {
    labels = labels + (role-label(item.kind),)
    labels = labels + item.additional_kinds.map(role-label)
  }
  let unique = ()
  for label in labels {
    if label != "" and not unique.contains(label) {
      unique.push(label)
    }
  }
  unique
}

#let role-badges(items) = {
  let labels = role-badge-labels(items)
  if labels.len() == 0 {
    []
  } else {
    grid(columns: (auto,) * labels.len(), column-gutter: 3pt,
      ..labels.map(label => role-badge(label)))
  }
}

#let course-body(item) = {
  set par(leading: 0.55em)
  stack(spacing: 12pt,
    course-title(item.course),
    ..(if item.location == "" { () } else { (text(size: 11pt, fill: muted, item.location),) }),
    ..(if item.weeks == "" { () } else { (text(size: 11pt, fill: accent, item.weeks),) }))
}

#let interval-body(group) = {
  for (index, item) in group.items.enumerate() {
    if index > 0 { v(12pt) }
    if group.items.len() > 1 { caption-text(item.period + " · " + item.time); v(4pt) }
    course-body(item)
  }
}

#{
  let day-width = if data.days.len() == 1 { 260pt } else { 90pt }
  let width = period-width + day-width * data.days.len()
  let cells = (grid.cell(x: 0, y: 0, body-text("节次", weight: "bold")),)
  for (i, day) in data.days.enumerate() {
    cells.push(grid.cell(x: i + 1, y: 0,
      stack(spacing: 12pt,
        body-text(day.label, weight: "bold", fill: if day.today { accent } else { ink }),
        ..(if day.date == "" { () } else { (caption-text(day.date, fill: if day.today { accent } else { muted }),) }))))
  }
  for (i, period) in data.periods.enumerate() {
    cells.push(grid.cell(x: 0, y: i + 1, inset: (x: 8pt, y: 20pt),
      stack(spacing: 12pt,
        body-text(period.label, weight: "bold"),
        caption-text(period.time))))
  }
  // Rowspan retains each class's actual period. Overlaps share their occupied
  // interval instead of being moved to a different time or silently dropped.
  for day in range(data.days.len()) {
    let items = data.items.filter(item => item.day == day).sorted(key: item => item.start)
    let groups = ()
    for item in items {
      if groups.len() > 0 and item.start <= groups.last().end {
        let last = groups.pop()
        groups.push((start: last.start, end: calc.max(last.end, item.end), items: last.items + (item,)))
      } else {
        groups.push((start: item.start, end: item.end, items: (item,)))
      }
    }
    for group in groups {
      let item = group.items.first()
      cells.push(grid.cell(x: day + 1, y: group.start, rowspan: group.end - group.start + 1,
        fill: if item.color == "" { rgb("#e2e8f0") } else { rgb(item.color) },
        stroke: 1pt + accent,
        {
          place(top + right, dx: -4pt, dy: 4pt, role-badges(group.items))
          if group.start == group.end and role-badge-labels(group.items).len() > 0 {
            v(18pt)
          }
          interval-body(group)
        }))
    }
  }
  let summary = if data.days.len() == 1 {
    data.days.first().label + " · 第 1–" + str(data.periods.len()) + " 节"
  } else {
    let lines = (data.semester, data.week, data.date_range).filter(value => value != "")
    if lines.len() == 0 { none } else { stack(spacing: 6pt, ..lines.map(caption-text)) }
  }
  card-sheet(width: width, margin: 36pt, {
    card-header(data.title, subtitle: summary)
    v(28pt)
    grid(columns: (period-width,) + (day-width,) * data.days.len(), rows: auto,
      align: center + horizon, inset: 8pt,
      fill: (x, y) => if y == 0 {
        if x > 0 and data.days.at(x - 1).today { table-highlight-strong } else { table-header }
      } else if x > 0 and data.days.at(x - 1).today { table-highlight }
      else if calc.even(y) { table-stripe-b } else { table-stripe-a },
      stroke: table-stroke, ..cells,
      ..(5, 10).filter(n => n < data.periods.len()).map(n => grid.hline(y: n + 1, stroke: 2pt + rgb("#64748b"))))
    card-footer(data.footer)
  })
}

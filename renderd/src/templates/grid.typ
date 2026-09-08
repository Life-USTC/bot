#import "styling.typ": *
#import "ui-component.typ": *
#let data = __DATA__

#let period-width = 120pt
#let course-body(item) = {
  let large = item.end > item.start
  let size = if large { 18pt } else { 14pt }
  text(size: size, weight: "bold", item.course)
  if item.location != "" { v(8pt); text(size: 13pt, fill: muted, item.location) }
  if item.weeks != "" { v(8pt); text(size: 13pt, fill: accent, item.weeks) }
}

#let interval-body(group) = {
  for (index, item) in group.items.enumerate() {
    if index > 0 { v(12pt) }
    if group.items.len() > 1 { caption-text(item.period + " · " + item.time); v(4pt) }
    course-body(item)
  }
}

#{
  let day-width = if data.days.len() == 1 { 360pt } else { 156pt }
  let width = period-width + day-width * data.days.len()
  let cells = (grid.cell(x: 0, y: 0, body-text("节次", weight: "bold")),)
  for (i, day) in data.days.enumerate() {
    cells.push(grid.cell(x: i + 1, y: 0, {
      body-text(day.label, weight: "bold", fill: if day.today { accent } else { ink })
      if day.date != "" { v(8pt); caption-text(day.date, fill: if day.today { accent } else { muted }) }
    }))
  }
  for (i, period) in data.periods.enumerate() {
    cells.push(grid.cell(x: 0, y: i + 1, {
      body-text(period.label, weight: "bold")
      v(8pt)
      caption-text(period.time)
    }))
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
        interval-body(group)))
    }
  }
  let summary = (if data.days.len() == 1 { data.days.first().label } else if data.days.all(d => d.date == "") { "整学期" } else { "周日–周六" }) + " · 第 1–" + str(data.periods.len()) + " 节"
  card-sheet(width: width, margin: 36pt, {
    card-header(data.title, subtitle: summary)
    v(28pt)
    grid(columns: (period-width,) + (day-width,) * data.days.len(), rows: auto,
      align: center + horizon, inset: 8pt,
      fill: (x, y) => if y == 0 {
        if x > 0 and data.days.at(x - 1).today { rgb("#ccfbf1") } else { rgb("#f4f4f5") }
      } else if x > 0 and data.days.at(x - 1).today { rgb("#f0fdfa") }
      else if calc.even(y) { rgb("#f8fafc") } else { ground },
      stroke: 1pt + rgb("#cbd5e1"), ..cells,
      ..(5, 10).filter(n => n < data.periods.len()).map(n => grid.hline(y: n + 1, stroke: 4pt + rgb("#64748b"))),
      ..data.days.enumerate().filter(((i, day)) => day.today).map(((i, day)) => (
        grid.vline(x: i + 1, stroke: 2pt + accent),
        grid.vline(x: i + 2, stroke: 2pt + accent))).flatten())
    card-footer(data.footer)
  })
}

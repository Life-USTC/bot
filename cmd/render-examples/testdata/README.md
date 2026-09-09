# Public production-data fixtures

Captured on 2026-09-09. Rendering is offline and deterministic; these are snapshots,
not a claim that the example clock is the current date or that future schedules
cannot change. No student account or personal timetable is used.

## Courses

Source: <https://catalog.ustc.edu.cn/api/teach/lesson/list-for-teach/461>.
Semester 461 is `2026年秋季学期`, 2026-08-30 through 2027-01-15, from
<https://catalog.ustc.edu.cn/api/teach/semester/list>.

`courses.json` selects six public sections from that response. Course names,
campuses, rooms, weekdays, period spans, and teaching-week ranges are preserved.
`sourceSchedule` retains the original public schedule string; `id` and `code`
identify the sections. Teacher names and student-count fields are omitted.
The weekday integers use Sunday = 0, matching the Bot grid. A `~` in a week range
becomes `–` for display; no time or room is invented.

| Section | Course | Meetings per week |
| --- | --- | ---: |
| MATH1006.12 | 数学分析(B1) | 3 |
| MATH1009.06 | 线性代数(B1) | 2 |
| CS1003.15 | 计算机程序设计 | 3 |
| MARX1014.16 | 习近平新时代中国特色社会主义思想概论 | 1 |
| PE00001.04 | 基础体育 | 1 |
| FL1013.01 | 大学英语交流 | 1 |

This is an assembled selection of public sections, **not one student's enrollment**.
It has no overlapping periods. The fixed clock is 2026-10-21 15:04 +0800, in week 8
(10/18–10/24), when all eleven selected meetings are active: the programming
lecture is on even weeks and its evening lab begins in week 4. The daily image
filters Wednesday directly from the weekly image so the two views cannot drift.

The thirteen lesson times and academic-week dates match the official calendar:
<https://www.teach.ustc.edu.cn/calendar/20135.html>. Period 1 is 07:50–08:35;
period 13 is 21:10–21:55. Production uses the same times in
`internal/commands/response.go` (`ustcLessonPeriods`).

## Buses

Source: <https://static.life-ustc.tiankaima.dev/bus_data_v3.json>, the public static
feed imported by the production server. Its notice says campus/high-tech trial
service starts 2026-08-30 and Taihu Road service starts 2026-08-27, citing
<https://www.ustc.edu.cn/ggfw/rdlj.htm>.

`bus.json` projects route IDs, campus names, and complete time matrices from
`weekday_routes`, `saturday_routes`, and `sunday_routes`. Coordinates are omitted;
`高新` is normalized to the Bot's `高新区`. Null intermediate times remain null in
the snapshot and blank in the image; they are not estimated.

- `bus-single`: weekday routes 1 and 2, all 28 east/west trips, including unknown
  North Campus times.
- `bus-all`: all 10 weekday routes and 105 trips, including both directions,
  South Campus, four-stop high-tech service, and Taihu Road Campus.
- `bus-weekend`: all 10 Saturday routes and 49 trips; these include different
  route IDs and service counts. Its clock is Saturday 2026-10-24 15:04 +0800.

Each image includes departed trips and marks the earliest remaining departure,
matching the production image response. Snapshot rows pass through the normal
Go rich-table parser and remote renderer, the same entry point used by the bus
command. Markdown cells preserve empty intermediate stops. No rows are removed
to fit a canvas.

Raw response SHA-256 at capture (before projection):

- Courses: `7e011da669686fcc1a30ad0dc07fbcce9afdbe68e970593e36f9214e67746111`
- Bus: `3b29b455a6ac448aa03c15b21487bdf68c5348a943db3c0e773b29f96bad7492`

Refresh deliberately from the source URLs and review the selected section IDs,
teaching-week applicability, row counts, and null times before replacing a
snapshot. Normal example generation and CI must not fetch changing network data.

Weather and todo cards remain explicitly labeled synthetic layout examples.
The archived pre-Typst reference images use the older fixtures pinned at
`967932782ccda31aefe6d8405e860dc5cb2e5426`; they illustrate historical styling and
are not comparisons using the same input data.

# Pre-Typst renderer references

Generated from the seven fixtures in `cmd/render-examples/main.go` using the
in-process `responses.Renderer` at legacy commit
`b62f863fda82f9ddc1002de2ea89555f49eede8a`. The clock is fixed to
`2026-09-02 15:04 +0800`; output scale is 2×.

| Fixture | PNG size | Logical size |
| --- | ---: | ---: |
| `bus-single.png` | 760×732 | 380×366pt |
| `bus-all.png` | 876×732 | 438×366pt |
| `rich-table.png` | 1088×608 | 544×304pt |
| `rich-text.png` | 1408×688 | 704×344pt |
| `grid-week.png` | 2568×1728 | 1284×864pt |
| `grid-day.png` | 1104×1728 | 552×864pt |
| `weather.png` | 1840×2872 | 920×1436pt |

All cards use a flat `#FAFAFA` background, dark ink `#27272A`, muted text
`#71717A`, and a rotated Life @ USTC logo peeking from the lower-right edge.
Legacy rich cards use 18pt bold titles, 13pt body and headings, 14pt table
cells, 11pt next-bus text, and 9pt metadata. The rich frame has 32pt side
margins, content top 82pt, 30pt block/row gaps, 32pt body rows, 28pt table
headers, 24pt footer gap, 14pt footer line gap, and 32pt bottom margin. Plain
prose draws a `#D4D4D8` one-point rule after every wrapped row; table highlights
use `#F4F4F5` and the teal accent is `#0F766E`.

Bus tables measure each column as the widest header at 13pt or body cell at
14pt plus 16pt horizontal padding. The fixture's five-character ASCII times
measure to 43pt, making every bus column 59pt. A three-stop table is 177pt and
the four-stop table is 236pt. `bus-single` is 316pt wide because its title and
next-bus banner measure 153pt + 123pt + 40pt; adding 32pt margins gives 380pt.
`bus-all` is 374pt wide because its reverse three-stop pair is 177pt + 20pt +
177pt; adding margins gives 438pt. The high-tech route is placed above the
other route group; exact reverse routes share a row.

Schedule grids use 36pt margins, 120pt period labels, 156pt day columns (360pt
for one day), 82pt grid top, 54pt header, and 56pt period rows. Course text is
14pt bold with 10pt metadata; items spanning at least two rows may use 18pt
course and 13pt metadata. Alternating rows are `#F8FAFC`, today header/body are
`#CCFBF1`/`#F0FDFA`, grid lines are `#CBD5E1`, section dividers `#64748B`, and
today borders use `#0F766E`. Long course names are ellipsized at the day width.

Weather uses 52pt margins and a 920pt canvas. Each location consumes 34pt name,
110pt hero, 68pt stat tiles, 32pt section heading, 158pt chart, 20pt chart
labels, 30pt per daily row, and 24pt per alert, with a 30pt location gap and
48pt footer row. Title/name/condition/body/heading/meta sizes are 18/16/18/13/
13/9pt; the temperature is 56pt. Hourly data is a Catmull–Rom amber curve
(`#F59E0B`) with 10% amber area fill, 60% precipitation bars in sky blue
(`#7DD3FC`, labels `#0284C7`), and x-axis labels every three hours. Daily rows
use 7pt gray tracks (`#E4E4E7`) and a sky-blue-to-amber gradient. Weather glyphs
use amber sun, gray cloud `#A1A1AA`, sky drops `#38BDF8`, dark amber lightning
`#D97706`, and slate hail `#64748B`.

Source pointers (all at the legacy commit): `internal/responses/rich_render.go`
lines 48–67, 92–116, 222–280, 397–412, 522–538, and 557–705;
`internal/responses/schedule_grid.go` lines 31–74 and 161–360;
`internal/responses/weather_render.go` lines 15–66 and 136–395;
`internal/responses/weather_glyph.go` lines 9–79. Run `./scripts/render-reference.sh` to
recreate the PNGs from the current fixture source by archiving the fixed
legacy commit into a temporary checkout.

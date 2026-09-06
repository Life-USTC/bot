package responses

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
	"time"

	"golang.org/x/image/font"
)

// weatherRenderMetrics mirrors the flat card chrome used by the bus renderer:
// logical units are multiplied by Scale for the final canvas.
type weatherRenderMetrics struct {
	CanvasWidth    int
	Scale          int
	MarginX        int
	TitleBaseline  int
	LocationHeight int
	NameRow        int
	HeroRow        int
	TileRow        int
	TileGap        int
	HeadingRow     int
	ChartRow       int
	ChartLabels    int
	DayRow         int
	AlertRow       int
	BlockGap       int
	FooterRow      int
}

func defaultWeatherRenderMetrics() weatherRenderMetrics {
	return weatherRenderMetrics{
		CanvasWidth:   920,
		Scale:         2,
		MarginX:       52,
		TitleBaseline: 52,
		NameRow:       34,
		HeroRow:       110,
		TileRow:       68,
		TileGap:       12,
		HeadingRow:    32,
		ChartRow:      158,
		ChartLabels:   20,
		DayRow:        30,
		AlertRow:      24,
		BlockGap:      30,
		FooterRow:     48,
	}
}

var (
	weatherInk     = color.RGBA{39, 39, 42, 255}
	weatherMuted   = color.RGBA{113, 113, 122, 255}
	weatherLine    = color.RGBA{228, 228, 231, 255}
	weatherBg      = color.RGBA{250, 250, 250, 255}
	weatherAmber   = color.RGBA{245, 158, 11, 255}
	weatherAmberDk = color.RGBA{217, 119, 6, 255}
	weatherSkyFill = color.RGBA{125, 211, 252, 255}
	weatherSkyDark = color.RGBA{2, 132, 199, 255}
	weatherTileBg  = color.RGBA{255, 255, 255, 255}
)

type weatherFaces struct {
	Title       font.Face
	TitleMono   font.Face
	Name        font.Face
	NameMono    font.Face
	Temp        font.Face
	TempMono    font.Face
	Cond        font.Face
	CondMono    font.Face
	Body        font.Face
	BodyMono    font.Face
	Heading     font.Face
	HeadingMono font.Face
	Meta        font.Face
	MetaMono    font.Face
}

func (r Renderer) weatherFacesFor(scale int) (weatherFaces, error) {
	load := func(fn func(float64) (font.Face, error), size int) (font.Face, error) {
		return fn(float64(size * scale))
	}
	var faces weatherFaces
	var err error
	if faces.Title, err = load(r.sansBoldFontFace, 18); err != nil {
		return faces, err
	}
	if faces.TitleMono, err = load(r.monoBoldFontFace, 18); err != nil {
		return faces, err
	}
	if faces.Name, err = load(r.sansBoldFontFace, 16); err != nil {
		return faces, err
	}
	if faces.NameMono, err = load(r.monoBoldFontFace, 16); err != nil {
		return faces, err
	}
	if faces.Temp, err = load(r.sansFontFace, 56); err != nil {
		return faces, err
	}
	if faces.TempMono, err = load(r.monoFontFace, 56); err != nil {
		return faces, err
	}
	if faces.Cond, err = load(r.sansFontFace, 18); err != nil {
		return faces, err
	}
	if faces.CondMono, err = load(r.monoFontFace, 18); err != nil {
		return faces, err
	}
	if faces.Body, err = load(r.sansFontFace, 13); err != nil {
		return faces, err
	}
	if faces.BodyMono, err = load(r.monoFontFace, 13); err != nil {
		return faces, err
	}
	if faces.Heading, err = load(r.sansBoldFontFace, 13); err != nil {
		return faces, err
	}
	if faces.HeadingMono, err = load(r.monoBoldFontFace, 13); err != nil {
		return faces, err
	}
	if faces.Meta, err = load(r.sansFontFace, 9); err != nil {
		return faces, err
	}
	if faces.MetaMono, err = load(r.monoFontFace, 9); err != nil {
		return faces, err
	}
	return faces, nil
}

func weatherLocationHeight(loc WeatherCardLocation, m weatherRenderMetrics) int {
	height := m.NameRow + m.HeroRow
	if loc.Current.HumidityText != "" || loc.Current.WindText != "" {
		height += m.TileRow
	}
	if len(loc.Hourly) > 0 {
		height += m.HeadingRow + m.ChartRow + m.ChartLabels
	}
	if len(loc.Daily) > 0 {
		height += m.HeadingRow + m.DayRow*len(loc.Daily)
	}
	if len(loc.Alerts) > 0 {
		height += m.HeadingRow + m.AlertRow*len(loc.Alerts)
	}
	return height
}

func (r Renderer) renderWeatherCardPNG(card *WeatherCard) ([]byte, int, int, error) {
	m := defaultWeatherRenderMetrics()
	height := m.TitleBaseline + 12
	for _, loc := range card.Locations {
		height += weatherLocationHeight(loc, m) + m.BlockGap
	}
	height += m.FooterRow
	space := imageRenderSpace{Width: m.CanvasWidth, Height: height, Scale: m.Scale}
	s := space.px

	faces, err := r.weatherFacesFor(m.Scale)
	if err != nil {
		return nil, 0, 0, err
	}

	canvas := image.NewRGBA(space.bounds())
	drawRect(canvas, canvas.Bounds(), weatherBg)
	drawBusLogoWatermark(canvas, canvas.Bounds(), s(120), 0.15)
	drawMixedText(canvas, faces.Title, faces.TitleMono, s(m.MarginX), s(m.TitleBaseline), "天气", weatherInk)

	left := m.MarginX
	right := m.CanvasWidth - m.MarginX
	contentWidth := right - left
	cursor := m.TitleBaseline + 12
	for index, loc := range card.Locations {
		if index > 0 {
			dividerY := s(cursor + m.BlockGap/2)
			drawRect(canvas, image.Rect(s(left), dividerY, s(right), dividerY+s(1)), weatherLine)
			cursor += m.BlockGap
		}
		cursor = r.drawWeatherLocation(canvas, s, faces, m, loc, left, right, contentWidth, cursor)
	}

	now := r.now().In(time.FixedZone("CST", 8*60*60))
	footerY := s(cursor + m.FooterRow - 20)
	if card.Meta != "" {
		drawMixedText(canvas, faces.Meta, faces.MetaMono, s(left), footerY+s(6), card.Meta, weatherMuted)
	}
	footerLines := richFooterLines(now)
	drawRightMixedText(canvas, faces.Meta, faces.MetaMono, s(right), footerY-s(6), footerLines[0], weatherMuted)
	drawRightMixedText(canvas, faces.Meta, faces.MetaMono, s(right), footerY+s(10), footerLines[1], weatherMuted)

	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), canvas.Bounds().Dx(), canvas.Bounds().Dy(), nil
}

func (r Renderer) drawWeatherLocation(canvas *image.RGBA, s func(int) int, faces weatherFaces, m weatherRenderMetrics, loc WeatherCardLocation, left, right, contentWidth, cursor int) int {
	// Location name.
	drawMixedText(canvas, faces.Name, faces.NameMono, s(left), s(cursor+22), loc.Name, weatherInk)
	cursor += m.NameRow

	// Hero row: condition glyph, big temperature, condition text and range.
	heroTop := cursor
	glyphSize := 64
	weatherDrawGlyph(canvas, s, loc.Current.Icon, left, heroTop+18, glyphSize)
	tempText := weatherFormatTemp(loc.Current.Temperature)
	tempX := left + glyphSize + 24
	drawMixedText(canvas, faces.Temp, faces.TempMono, s(tempX), s(heroTop+18+62), tempText, weatherInk)
	tempAdvance := int(mixedTextAdvance(mixedFontRuns(tempText, faces.Temp, faces.TempMono)) >> 6)
	condX := tempX + tempAdvance/m.Scale + 16
	condBaseline := heroTop + 18 + 52
	if loc.Current.ConditionText != "" {
		drawMixedText(canvas, faces.Cond, faces.CondMono, s(condX), s(condBaseline), loc.Current.ConditionText, weatherInk)
	}
	if loc.Current.HasRange {
		rangeText := weatherFormatTemp(loc.Current.High) + " / " + weatherFormatTemp(loc.Current.Low)
		drawMixedText(canvas, faces.Body, faces.BodyMono, s(condX), s(condBaseline+24), rangeText, weatherMuted)
	}
	cursor += m.HeroRow

	// Stat tiles: humidity and wind.
	tiles := []struct{ label, value string }{}
	if loc.Current.HumidityText != "" {
		tiles = append(tiles, struct{ label, value string }{"湿度", loc.Current.HumidityText})
	}
	if loc.Current.WindText != "" {
		tiles = append(tiles, struct{ label, value string }{"风", loc.Current.WindText})
	}
	if len(tiles) > 0 {
		tileWidth := (contentWidth - m.TileGap*(len(tiles)-1)) / len(tiles)
		for i, tile := range tiles {
			x := left + i*(tileWidth+m.TileGap)
			y := cursor
			drawRect(canvas, image.Rect(s(x), s(y), s(x+tileWidth), s(y+m.TileRow-12)), weatherTileBg)
			weatherStrokeRect(canvas, s, x, y, tileWidth, m.TileRow-12, weatherLine)
			drawMixedText(canvas, faces.Meta, faces.MetaMono, s(x+12), s(y+20), tile.label, weatherMuted)
			drawMixedText(canvas, faces.Body, faces.BodyMono, s(x+12), s(y+42), tile.value, weatherInk)
		}
		cursor += m.TileRow
	}

	if len(loc.Hourly) > 0 {
		cursor = drawWeatherHourlyChart(canvas, s, faces, m, loc.Hourly, left, contentWidth, cursor)
	}
	if len(loc.Daily) > 0 {
		cursor = drawWeatherDailyBars(canvas, s, faces, m, loc.Daily, left, contentWidth, cursor)
	}
	if len(loc.Alerts) > 0 {
		drawMixedText(canvas, faces.Heading, faces.HeadingMono, s(left), s(cursor+18), "天气预警", weatherInk)
		cursor += m.HeadingRow
		for _, alert := range loc.Alerts {
			drawMixedText(canvas, faces.Body, faces.BodyMono, s(left), s(cursor+16), alert, weatherAmberDk)
			cursor += m.AlertRow
		}
	}
	return cursor
}

func drawWeatherHourlyChart(canvas *image.RGBA, s func(int) int, faces weatherFaces, m weatherRenderMetrics, hourly []WeatherCardHourPoint, left, contentWidth, cursor int) int {
	drawMixedText(canvas, faces.Heading, faces.HeadingMono, s(left), s(cursor+18), "逐小时预报", weatherInk)
	cursor += m.HeadingRow

	chartTop := cursor
	chartHeight := m.ChartRow
	plotBottom := chartTop + chartHeight - 34
	slotWidth := float64(contentWidth) / float64(len(hourly))

	minTemp, maxTemp := hourly[0].Temperature, hourly[0].Temperature
	for _, point := range hourly {
		minTemp = math.Min(minTemp, point.Temperature)
		maxTemp = math.Max(maxTemp, point.Temperature)
	}
	if maxTemp-minTemp < 2 {
		maxTemp = minTemp + 2
	}
	minTemp -= 1
	maxTemp += 1
	tempY := func(temp float64) float64 {
		ratio := (temp - minTemp) / (maxTemp - minTemp)
		return float64(plotBottom) - ratio*float64(chartHeight-58)
	}
	pointX := func(i int) float64 {
		return float64(left) + slotWidth*(float64(i)+0.5)
	}

	// Area fill under the smoothed curve.
	ys := make([]float64, len(hourly))
	for i, point := range hourly {
		ys[i] = point.Temperature
	}
	sampled := weatherCatmullRom(ys, 12)
	stepX := slotWidth / 12
	for i := 0; i < len(sampled)-1; i++ {
		x0 := float64(left) + slotWidth*0.5 + float64(i)*stepX
		x1 := x0 + stepX
		y0 := tempY(sampled[i])
		y1 := tempY(sampled[i+1])
		weatherFillTrapezoid(canvas, s, x0, x1, y0, y1, float64(plotBottom), weatherAmber, 0.10)
	}
	for i := 0; i < len(sampled)-1; i++ {
		x0 := float64(left) + slotWidth*0.5 + float64(i)*stepX
		x1 := x0 + stepX
		weatherThickLine(canvas, s, x0, tempY(sampled[i]), x1, tempY(sampled[i+1]), 2.2, weatherAmber)
	}

	// Precipitation probability bars sit on the plot baseline.
	maxBarHeight := 34.0
	for i, point := range hourly {
		if point.PrecipitationProbability <= 0 {
			continue
		}
		barHeight := point.PrecipitationProbability / 100 * maxBarHeight
		if barHeight < 2 {
			barHeight = 2
		}
		barWidth := slotWidth * 0.44
		x := pointX(i) - barWidth/2
		weatherBlendRect(canvas, s, x, float64(plotBottom)-barHeight, barWidth, barHeight, weatherSkyFill, 0.9)
	}
	baselineY := s(plotBottom) + 1
	drawRect(canvas, image.Rect(s(left), baselineY, s(left+contentWidth), baselineY+s(1)), weatherLine)

	// Temperature label above each curve point.
	for i, point := range hourly {
		drawCenteredMixedText(canvas, faces.Meta, faces.MetaMono, s(int(pointX(i))), s(int(tempY(point.Temperature))-6), weatherFormatTemp(point.Temperature), weatherMuted)
	}

	// Probability label for meaningful precipitation bars: inside tall bars,
	// above short ones when the curve leaves room, otherwise skipped.
	for i, point := range hourly {
		if point.PrecipitationProbability < 30 {
			continue
		}
		barHeight := point.PrecipitationProbability / 100 * maxBarHeight
		label := strconv.Itoa(int(math.Round(point.PrecipitationProbability))) + "%"
		if barHeight >= 14 {
			drawCenteredMixedText(canvas, faces.Meta, faces.MetaMono, s(int(pointX(i))), s(int(float64(plotBottom)-barHeight/2+3)), label, weatherSkyDark)
			continue
		}
		labelY := float64(plotBottom) - barHeight - 4
		if labelY-10 < tempY(point.Temperature)-6 {
			continue
		}
		drawCenteredMixedText(canvas, faces.Meta, faces.MetaMono, s(int(pointX(i))), s(int(labelY)), label, weatherSkyDark)
	}

	// Sparse x-axis labels every three hours.
	labelY := plotBottom + 16
	for i, point := range hourly {
		if i%3 != 0 {
			continue
		}
		drawCenteredMixedText(canvas, faces.Meta, faces.MetaMono, s(int(pointX(i))), s(labelY), point.Label, weatherMuted)
	}
	return chartTop + chartHeight + m.ChartLabels
}

func drawWeatherDailyBars(canvas *image.RGBA, s func(int) int, faces weatherFaces, m weatherRenderMetrics, daily []WeatherCardDayPoint, left, contentWidth, cursor int) int {
	drawMixedText(canvas, faces.Heading, faces.HeadingMono, s(left), s(cursor+18), "每日预报", weatherInk)
	cursor += m.HeadingRow

	weekLow, weekHigh := daily[0].Low, daily[0].High
	for _, day := range daily {
		weekLow = math.Min(weekLow, day.Low)
		weekHigh = math.Max(weekHigh, day.High)
	}
	span := weekHigh - weekLow
	if span < 1 {
		span = 1
	}

	labelWidth := 56
	tempWidth := 44
	barLeft := left + labelWidth + tempWidth + 16
	barRight := left + contentWidth - tempWidth - 8
	barWidth := float64(barRight - barLeft)

	for _, day := range daily {
		rowY := cursor
		drawMixedText(canvas, faces.Body, faces.BodyMono, s(left), s(rowY+18), day.Label, weatherInk)
		drawRightMixedText(canvas, faces.Body, faces.BodyMono, s(left+labelWidth+tempWidth), s(rowY+18), weatherFormatTemp(day.Low), weatherMuted)
		trackY := rowY + 9
		drawRect(canvas, image.Rect(s(barLeft), s(trackY), s(barRight), s(trackY+7)), weatherLine)
		fillLeft := float64(barLeft) + (day.Low-weekLow)/span*barWidth
		fillRight := float64(barLeft) + (day.High-weekLow)/span*barWidth
		weatherGradientRect(canvas, s, fillLeft, float64(trackY), fillRight, float64(trackY+7), weatherSkyFill, weatherAmber)
		drawRightMixedText(canvas, faces.Body, faces.BodyMono, s(left+contentWidth), s(rowY+18), weatherFormatTemp(day.High), weatherInk)
		cursor += m.DayRow
	}
	return cursor
}

func weatherFormatTemp(value float64) string {
	return strconv.Itoa(int(math.Round(value))) + "°"
}

// weatherCatmullRom samples a Catmull-Rom spline through ys with duplicated
// endpoints, returning len(ys)-1 segments each split into samples steps plus
// the final point.
func weatherCatmullRom(ys []float64, samples int) []float64 {
	if len(ys) < 2 {
		return append([]float64(nil), ys...)
	}
	at := func(i int) float64 {
		if i < 0 {
			return ys[0]
		}
		if i >= len(ys) {
			return ys[len(ys)-1]
		}
		return ys[i]
	}
	out := make([]float64, 0, (len(ys)-1)*samples+1)
	for segment := 0; segment < len(ys)-1; segment++ {
		p0, p1, p2, p3 := at(segment-1), at(segment), at(segment+1), at(segment+2)
		for step := 0; step < samples; step++ {
			t := float64(step) / float64(samples)
			t2 := t * t
			t3 := t2 * t
			value := 0.5 * ((2 * p1) + (-p0+p2)*t + (2*p0-5*p1+4*p2-p3)*t2 + (-p0+3*p1-3*p2+p3)*t3)
			out = append(out, value)
		}
	}
	out = append(out, ys[len(ys)-1])
	return out
}

func weatherBlendPx(dst *image.RGBA, x, y int, c color.RGBA, alpha float64) {
	if !image.Pt(x, y).In(dst.Bounds()) {
		return
	}
	offset := dst.PixOffset(x, y)
	inv := 1 - alpha
	dst.Pix[offset+0] = uint8(float64(c.R)*alpha + float64(dst.Pix[offset+0])*inv)
	dst.Pix[offset+1] = uint8(float64(c.G)*alpha + float64(dst.Pix[offset+1])*inv)
	dst.Pix[offset+2] = uint8(float64(c.B)*alpha + float64(dst.Pix[offset+2])*inv)
	dst.Pix[offset+3] = 255
}

func weatherBlendRect(dst *image.RGBA, s func(int) int, x, y, width, height float64, c color.RGBA, alpha float64) {
	x0 := s(int(math.Round(x)))
	y0 := s(int(math.Round(y)))
	x1 := s(int(math.Round(x + width)))
	y1 := s(int(math.Round(y + height)))
	for py := y0; py < y1; py++ {
		for px := x0; px < x1; px++ {
			weatherBlendPx(dst, px, py, c, alpha)
		}
	}
}

func weatherFillCircle(dst *image.RGBA, s func(int) int, cx, cy, radius float64, c color.RGBA, alpha float64) {
	scx, scy, sr := s(int(math.Round(cx))), s(int(math.Round(cy))), s(int(math.Round(radius)))
	for dy := -sr; dy <= sr; dy++ {
		for dx := -sr; dx <= sr; dx++ {
			if dx*dx+dy*dy <= sr*sr {
				weatherBlendPx(dst, scx+dx, scy+dy, c, alpha)
			}
		}
	}
}

func weatherThickLine(dst *image.RGBA, s func(int) int, x0, y0, x1, y1, width float64, c color.RGBA) {
	length := math.Hypot(x1-x0, y1-y0)
	steps := int(length * 4)
	if steps < 1 {
		steps = 1
	}
	radius := width / 2
	for step := 0; step <= steps; step++ {
		t := float64(step) / float64(steps)
		weatherFillCircle(dst, s, x0+(x1-x0)*t, y0+(y1-y0)*t, radius, c, 1)
	}
}

// weatherFillTrapezoid fills the vertical span between the curve segment and
// the baseline with a soft alpha, column by column.
func weatherFillTrapezoid(dst *image.RGBA, s func(int) int, x0, x1, y0, y1, baseline float64, c color.RGBA, alpha float64) {
	sx0 := s(int(math.Round(x0)))
	sx1 := s(int(math.Round(x1)))
	if sx1 <= sx0 {
		sx1 = sx0 + 1
	}
	for px := sx0; px < sx1; px++ {
		t := float64(px-sx0) / float64(sx1-sx0)
		top := y0 + (y1-y0)*t
		syTop := s(int(math.Round(top)))
		syBottom := s(int(math.Round(baseline)))
		for py := syTop; py < syBottom; py++ {
			weatherBlendPx(dst, px, py, c, alpha)
		}
	}
}

func weatherGradientRect(dst *image.RGBA, s func(int) int, x0, y0, x1, y1 float64, from, to color.RGBA) {
	sx0 := s(int(math.Round(x0)))
	sx1 := s(int(math.Round(x1)))
	sy0 := s(int(math.Round(y0)))
	sy1 := s(int(math.Round(y1)))
	if sx1 <= sx0 {
		return
	}
	for px := sx0; px < sx1; px++ {
		t := float64(px-sx0) / float64(sx1-sx0)
		c := color.RGBA{
			R: uint8(float64(from.R)*(1-t) + float64(to.R)*t),
			G: uint8(float64(from.G)*(1-t) + float64(to.G)*t),
			B: uint8(float64(from.B)*(1-t) + float64(to.B)*t),
			A: 255,
		}
		for py := sy0; py < sy1; py++ {
			weatherBlendPx(dst, px, py, c, 1)
		}
	}
	// Rounded ends.
	radius := (y1 - y0) / 2
	weatherFillCircle(dst, s, x0, (y0+y1)/2, radius, from, 1)
	weatherFillCircle(dst, s, x1, (y0+y1)/2, radius, to, 1)
}

func weatherStrokeRect(dst *image.RGBA, s func(int) int, x, y, width, height int, c color.RGBA) {
	drawRect(dst, image.Rect(s(x), s(y), s(x+width), s(y)+s(1)), c)
	drawRect(dst, image.Rect(s(x), s(y+height)-s(1), s(x+width), s(y+height)), c)
	drawRect(dst, image.Rect(s(x), s(y), s(x)+s(1), s(y+height)), c)
	drawRect(dst, image.Rect(s(x+width)-s(1), s(y), s(x+width), s(y+height)), c)
}

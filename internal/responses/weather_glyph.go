package responses

import (
	"image"
	"image/color"
	"math"
)

var (
	weatherGlyphSun   = color.RGBA{245, 158, 11, 255}
	weatherGlyphCloud = color.RGBA{161, 161, 170, 255}
	weatherGlyphDrop  = color.RGBA{56, 189, 248, 255}
	weatherGlyphBolt  = color.RGBA{217, 119, 6, 255}
	weatherGlyphHail  = color.RGBA{100, 116, 139, 255}
)

// weatherDrawGlyph draws a filled weather glyph for one of the server's
// normalized condition icon names (sun, cloud-sun, cloud, cloudy, cloud-fog,
// cloud-drizzle, cloud-rain, cloud-snow, cloud-hail, cloud-lightning).
// (x, y) is the top-left corner of the square box in logical units.
func weatherDrawGlyph(dst *image.RGBA, s func(int) int, icon string, x, y, size int) {
	switch icon {
	case "sun":
		weatherDrawSun(dst, s, float64(x), float64(y), float64(size), 1)
	case "cloud-sun":
		weatherDrawSun(dst, s, float64(x)-float64(size)*0.10, float64(y)-float64(size)*0.08, float64(size)*0.62, 1)
		weatherDrawCloud(dst, s, float64(x)+float64(size)*0.14, float64(y)+float64(size)*0.16, float64(size)*0.86, weatherGlyphCloud)
	case "cloud-fog":
		weatherDrawCloud(dst, s, float64(x), float64(y)-float64(size)*0.08, float64(size), weatherGlyphCloud)
		weatherThickLine(dst, s, float64(x)+0.26*float64(size), float64(y)+0.80*float64(size), float64(x)+0.74*float64(size), float64(y)+0.80*float64(size), 0.045*float64(size), weatherGlyphCloud)
		weatherThickLine(dst, s, float64(x)+0.34*float64(size), float64(y)+0.92*float64(size), float64(x)+0.66*float64(size), float64(y)+0.92*float64(size), 0.045*float64(size), weatherGlyphCloud)
	case "cloud-drizzle":
		weatherDrawCloud(dst, s, float64(x), float64(y)-float64(size)*0.08, float64(size), weatherGlyphCloud)
		for _, dx := range []float64{0.32, 0.50, 0.68} {
			weatherFillCircle(dst, s, float64(x)+dx*float64(size), float64(y)+0.86*float64(size), 0.038*float64(size), weatherGlyphDrop, 1)
		}
	case "cloud-rain":
		weatherDrawCloud(dst, s, float64(x), float64(y)-float64(size)*0.08, float64(size), weatherGlyphCloud)
		for _, dx := range []float64{0.34, 0.52, 0.70} {
			weatherThickLine(dst, s, float64(x)+dx*float64(size), float64(y)+0.78*float64(size), float64(x)+(dx-0.05)*float64(size), float64(y)+0.94*float64(size), 0.04*float64(size), weatherGlyphDrop)
		}
	case "cloud-snow":
		weatherDrawCloud(dst, s, float64(x), float64(y)-float64(size)*0.08, float64(size), weatherGlyphCloud)
		for _, dx := range []float64{0.30, 0.50, 0.70} {
			weatherFillCircle(dst, s, float64(x)+dx*float64(size), float64(y)+0.86*float64(size), 0.05*float64(size), weatherGlyphDrop, 1)
		}
	case "cloud-hail":
		weatherDrawCloud(dst, s, float64(x), float64(y)-float64(size)*0.08, float64(size), weatherGlyphCloud)
		for _, dx := range []float64{0.32, 0.50, 0.68} {
			weatherFillCircle(dst, s, float64(x)+dx*float64(size), float64(y)+0.86*float64(size), 0.05*float64(size), weatherGlyphHail, 1)
		}
	case "cloud-lightning":
		weatherDrawCloud(dst, s, float64(x), float64(y)-float64(size)*0.10, float64(size), weatherGlyphCloud)
		weatherThickLine(dst, s, float64(x)+0.54*float64(size), float64(y)+0.58*float64(size), float64(x)+0.44*float64(size), float64(y)+0.78*float64(size), 0.05*float64(size), weatherGlyphBolt)
		weatherThickLine(dst, s, float64(x)+0.44*float64(size), float64(y)+0.78*float64(size), float64(x)+0.58*float64(size), float64(y)+0.78*float64(size), 0.05*float64(size), weatherGlyphBolt)
		weatherThickLine(dst, s, float64(x)+0.58*float64(size), float64(y)+0.78*float64(size), float64(x)+0.46*float64(size), float64(y)+0.98*float64(size), 0.05*float64(size), weatherGlyphBolt)
	default: // cloud, cloudy, unknown
		weatherDrawCloud(dst, s, float64(x), float64(y), float64(size), weatherGlyphCloud)
	}
}

func weatherDrawSun(dst *image.RGBA, s func(int) int, x, y, size float64, alpha float64) {
	cx := x + size/2
	cy := y + size/2
	weatherFillCircle(dst, s, cx, cy, size*0.26, weatherGlyphSun, alpha)
	for i := 0; i < 8; i++ {
		angle := float64(i) * math.Pi / 4
		dx := math.Cos(angle)
		dy := math.Sin(angle)
		weatherThickLine(dst, s, cx+dx*size*0.36, cy+dy*size*0.36, cx+dx*size*0.48, cy+dy*size*0.48, size*0.05, weatherGlyphSun)
	}
}

func weatherDrawCloud(dst *image.RGBA, s func(int) int, x, y, size float64, c color.RGBA) {
	weatherFillCircle(dst, s, x+0.36*size, y+0.56*size, 0.20*size, c, 1)
	weatherFillCircle(dst, s, x+0.56*size, y+0.44*size, 0.25*size, c, 1)
	weatherFillCircle(dst, s, x+0.72*size, y+0.58*size, 0.16*size, c, 1)
	weatherBlendRect(dst, s, x+0.20*size, y+0.55*size, 0.62*size, 0.19*size, c, 1)
}

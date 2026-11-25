package main

import (
	"image"
	"image/color"
	"math"
)

// Saved overlays of the map picture, same colours as ui/app.js: thresholds, virtual walls and zones,
// carpets, furniture. px turns robot millimetres into image pixels.
func drawOverlays(img *image.RGBA, view mapView, px func([]int) (int, int)) {
	rgb := func(r, g, b uint8) color.RGBA { return color.RGBA{r, g, b, 255} }
	// Carpets the robot found: the app's texture, a checkerboard of darker half cells.
	for _, c := range view.Carpets {
		poly := [][2]float64{}
		for i := 0; i+1 < len(c.Points); i += 2 {
			x, y := px(c.Points[i : i+2])
			poly = append(poly, [2]float64{float64(x), float64(y)})
		}
		fillWhere(img, poly, func(x, y int) { checker(img, x, y, 38) })
	}
	rects := []struct {
		list         [][]int
		edge         color.RGBA
		alpha        int // fill, percent; 0 for the carpet texture
		dashed, mark bool
	}{
		{view.NoGo, rgb(0xff, 0x4d, 0x4d), 25, false, false},
		{view.NoMop, rgb(0x5a, 0xa9, 0xff), 25, false, false},
		{view.CarpetsAdded, color.RGBA{0, 0, 0, 255}, 0, false, true},
		{view.CarpetsIgnored, rgb(0x8a, 0x8a, 0x8a), 15, true, false},
		{view.Ramps, rgb(0x2f, 0x9e, 0x44), 25, false, false},
	}
	for _, k := range rects {
		for _, r := range k.list {
			if len(r) != 4 {
				continue
			}
			x0, y0 := px(r[:2])
			x1, y1 := px(r[2:4])
			x0, x1, y0, y1 = min(x0, x1), max(x0, x1), min(y0, y1), max(y0, y1)
			for y := y0; y <= y1; y++ {
				for x := x0; x <= x1; x++ {
					if k.mark {
						checker(img, x, y, 76)
					} else {
						blend(img, x, y, k.edge, k.alpha*255/100)
					}
				}
			}
			for _, e := range [][4]int{{x0, y0, x1, y0}, {x1, y0, x1, y1}, {x1, y1, x0, y1}, {x0, y1, x0, y0}} {
				thick(img, e[0], e[1], e[2], e[3], k.edge, k.dashed)
			}
		}
	}
	for _, w := range view.Walls {
		if len(w) == 4 {
			x0, y0 := px(w[:2])
			x1, y1 := px(w[2:4])
			thick(img, x0, y0, x1, y1, rgb(0xff, 0x4d, 0x4d), false)
		}
	}
	for kind, list := range map[string][][]int{"p": view.Passable, "i": view.Impassable} {
		c := rgb(0x55, 0xf2, 0xaa)
		if kind == "i" {
			c = rgb(0xff, 0x6b, 0x80)
		}
		for _, t := range list {
			if len(t) == 4 {
				x0, y0 := px(t[:2])
				x1, y1 := px(t[2:4])
				thick(img, x0, y0, x1, y1, c, false)
			}
		}
	}
	for _, f := range view.Furniture {
		cx, cy := px([]int{f.X, f.Y})
		w, h := float64(f.Width*mapScale)/float64(view.GridSize), float64(f.Height*mapScale)/float64(view.GridSize)
		rot := f.Rotation * math.Pi / 180
		sin, cos := math.Sincos(rot)
		reach := int(math.Hypot(w, h)/2) + 2
		edge := rgb(0xc5, 0x8b, 0x2b)
		for y := cy - reach; y <= cy+reach; y++ {
			for x := cx - reach; x <= cx+reach; x++ {
				dx, dy := float64(x-cx), float64(y-cy)
				lx, ly := dx*cos-dy*sin, dx*sin+dy*cos
				if math.Abs(lx) > w/2 || math.Abs(ly) > h/2 {
					continue
				}
				if math.Abs(lx) > w/2-1.5 || math.Abs(ly) > h/2-1.5 {
					blend(img, x, y, edge, 255)
				} else {
					blend(img, x, y, edge, 64)
				}
			}
		}
	}
}

// blend paints c over the pixel with alpha 0..255; points outside the image are ignored.
func blend(img *image.RGBA, x, y int, c color.RGBA, alpha int) {
	if !(image.Point{x, y}).In(img.Rect) {
		return
	}
	p := img.Pix[img.PixOffset(x, y):]
	for i, v := range []uint8{c.R, c.G, c.B} {
		p[i] = uint8((int(p[i])*(255-alpha) + int(v)*alpha) / 255)
	}
}

// checker darkens half of the 2x2-pixel squares, the app's carpet texture.
func checker(img *image.RGBA, x, y, alpha int) {
	if (x/2+y/2)&1 == 1 {
		blend(img, x, y, color.RGBA{0, 0, 0, 255}, alpha)
	}
}

// thick is a 2-pixel line, optionally dashed (6 on, 4 off).
func thick(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA, dashed bool) {
	n := 0
	stroke(x0, y0, x1, y1, func(x, y int) {
		n++
		if dashed && n%10 >= 6 {
			return
		}
		for _, d := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
			blend(img, x+d[0], y+d[1], c, 255)
		}
	})
}

// fillWhere calls paint for every pixel inside the polygon (even-odd rule).
func fillWhere(img *image.RGBA, poly [][2]float64, paint func(x, y int)) {
	if len(poly) < 3 {
		return
	}
	lo, hi := poly[0][1], poly[0][1]
	for _, p := range poly {
		lo, hi = math.Min(lo, p[1]), math.Max(hi, p[1])
	}
	for y := max(int(lo), 0); y <= min(int(hi), img.Rect.Dy()-1); y++ {
		xs := []float64{}
		for i, a := range poly {
			b := poly[(i+1)%len(poly)]
			if (a[1] <= float64(y)+.5) != (b[1] <= float64(y)+.5) {
				xs = append(xs, a[0]+(float64(y)+.5-a[1])/(b[1]-a[1])*(b[0]-a[0]))
			}
		}
		for i := range xs { // few crossings: insertion sort
			for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
				xs[j], xs[j-1] = xs[j-1], xs[j]
			}
		}
		for i := 0; i+1 < len(xs); i += 2 {
			for x := max(int(math.Round(xs[i])), 0); x < min(int(math.Round(xs[i+1])), img.Rect.Dx()); x++ {
				paint(x, y)
			}
		}
	}
}

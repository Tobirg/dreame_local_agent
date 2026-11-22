package main

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
)

// Same colours as ui/app.js.
var (
	roomPalette = []color.RGBA{{0x7f, 0xd9, 0xc2, 255}, {0xa3, 0xbc, 0xec, 255}, {0xe5, 0xbd, 0x8a, 255}, {0xbc, 0xac, 0xeb, 255},
		{0xa7, 0xd3, 0x9a, 255}, {0xe3, 0xa5, 0xb4, 255}, {0x93, 0xcd, 0xd9, 255}, {0xd4, 0xd0, 0x93, 255}}
	wallColour    = color.RGBA{0xd1, 0xd9, 0xe2, 255}
	pathColour    = color.RGBA{0xff, 0xff, 0xff, 200}
	robotColour   = color.RGBA{0x4d, 0xa3, 0xff, 255}
	chargerColour = color.RGBA{0x55, 0xf2, 0xaa, 255}
)

const mapScale = 3 // output pixels per map cell, as the web UI draws it

// mapPNG renders the saved map with the station and, while cleaning, the robot and its path.
func (a *agent) mapPNG() ([]byte, error) {
	view, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if view.Empty || len(view.Origin) != 2 {
		return nil, errors.New("no saved map")
	}
	img := image.NewRGBA(image.Rect(0, 0, view.Width*mapScale, view.Height*mapScale))
	for i, id := range view.Pixels {
		if id == 0 {
			continue
		}
		c := wallColour
		if id != 255 {
			c = roomPalette[(int(id)-1)%len(roomPalette)]
		}
		x, y := i%view.Width, view.Height-1-i/view.Width // map rows go up, image rows go down
		for dy := 0; dy < mapScale; dy++ {
			for dx := 0; dx < mapScale; dx++ {
				img.SetRGBA(x*mapScale+dx, y*mapScale+dy, c)
			}
		}
	}
	// Robot millimetres to image pixels.
	px := func(p []int) (int, int) {
		return (p[0] - view.Origin[0]) * mapScale / view.GridSize, (view.Height*view.GridSize - (p[1] - view.Origin[1])) * mapScale / view.GridSize
	}
	live := a.livePose()
	if !live.Live || live.MapID != view.MapID {
		live = livePose{}
	}
	for i := 1; i < len(live.Path); i++ {
		x0, y0 := px(live.Path[i-1])
		x1, y1 := px(live.Path[i])
		line(img, x0, y0, x1, y1, pathColour)
	}
	charger := view.Charger
	if live.Charger != nil {
		charger = live.Charger
	}
	if charger != nil {
		x, y := px(charger)
		disc(img, x, y, 5, chargerColour)
	}
	if live.Robot != nil {
		x, y := px(live.Robot)
		disc(img, x, y, 7, robotColour)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func disc(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	for y := -r; y <= r; y++ {
		for x := -r; x <= r; x++ {
			if x*x+y*y <= r*r {
				img.SetRGBA(cx+x, cy+y, c)
			}
		}
	}
}

// line draws with Bresenham's algorithm; SetRGBA ignores points outside the image.
func line(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	for e := dx + dy; ; {
		img.SetRGBA(x0, y0, c)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * e
		if e2 >= dy {
			e += dy
			x0 += sx
		}
		if e2 <= dx {
			e += dx
			y0 += sy
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

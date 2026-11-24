// Copyright 2026 Dreame Local contributors. MIT license.
// Small independent viewer for the saved MIOT map format observed on r2505.
package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type mapRoom struct {
	ID   int     `json:"id"`
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Type int     `json:"type"` // see roomtypes.go
	// Saved seg_inf fields, for room type edits.
	kind, index int
	rawName     string
}
type mapView struct {
	ThresholdsEditable bool                       `json:"thresholds_editable"`
	Passable           [][]int                    `json:"passable_thresholds"`
	Impassable         [][]int                    `json:"impassable_thresholds"`
	VWS                map[string]json.RawMessage `json:"-"`
	// Virtual walls (vw.line), no-go zones (vw.rect) and no-mop zones (vw.mop), robot millimetres.
	RestrictionsEditable bool    `json:"restrictions_editable"`
	Walls                [][]int `json:"walls"`
	NoGo                 [][]int `json:"no_go_zones"`
	NoMop                [][]int `json:"no_mop_zones"`
	// Carpets: found by the robot (carpet_polygon: id -> [x, y, ...] mm), drawn by the user (vw.addcpt)
	// and areas where a found carpet is ignored (vw.nocpt); rectangles [x0, y0, x1, y1]. See carpets.go.
	CarpetsEditable bool          `json:"carpets_editable"`
	Carpets         []carpetShape `json:"carpets"`
	CarpetsAdded    [][]int       `json:"carpets_added"`
	CarpetsIgnored  [][]int       `json:"carpets_ignored"`
	// Per-room cleaning settings by room id, from /data/ri/setting.json2; see roomsettings.go.
	RoomSettings map[string][]int `json:"room_settings"`
	Charger      []int            `json:"charger,omitempty"` // station position, robot millimetres
	Empty        bool             `json:"empty"`
	MapID        int              `json:"map_id"`
	Width        int              `json:"width"`
	Height       int              `json:"height"`
	GridSize     int              `json:"grid_size"`
	Origin       []int            `json:"origin"`
	Pixels       []byte           `json:"pixels"`
	Rooms        []mapRoom        `json:"rooms"`
	Revision     string           `json:"revision"`
	Source       string           `json:"source"`
	Live         bool             `json:"live"`
	// What the AI camera recognised on the last run that reported it: [x, y, type, confidence %]; see obstacles.go.
	Obstacles   [][]int    `json:"obstacles"`
	ObstaclesAt *time.Time `json:"obstacles_at,omitempty"`
}

func decodeSavedFrame(encoded string) ([]byte, error) {
	raw, err := decodeFrame(encoded)
	if err == nil && raw[4] != 'I' {
		return nil, errors.New("saved map is not a full I frame")
	}
	return raw, err
}

// decodeFrame unpacks any map frame: I (full) or P (changes, sent while cleaning).
func decodeFrame(encoded string) ([]byte, error) {
	encoded = strings.NewReplacer("_", "/", "-", "+").Replace(strings.TrimRight(strings.TrimSpace(encoded), "="))
	packed, err := base64.RawStdEncoding.DecodeString(encoded) // padding is optional in the firmware's frames
	if err != nil {
		return nil, errors.New("map frame is not base64")
	}
	r, err := zlib.NewReader(bytes.NewReader(packed))
	if err != nil {
		return nil, errors.New("map frame is not zlib")
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, (32<<20)+1))
	if err != nil || len(raw) > 32<<20 || len(raw) < 27 {
		return nil, errors.New("invalid map frame size")
	}
	return raw, nil
}
func makeMapView(raw []byte, model string) (mapView, error) {
	view := mapView{Source: "local_saved_file", Live: false, Rooms: []mapRoom{}}
	if len(raw) < 27 {
		return view, errors.New("map header is incomplete")
	}
	view.MapID = int(binary.LittleEndian.Uint16(raw[0:2]))
	view.Width = int(binary.LittleEndian.Uint16(raw[19:21]))
	view.Height = int(binary.LittleEndian.Uint16(raw[21:23]))
	view.GridSize = int(binary.LittleEndian.Uint16(raw[17:19]))
	total := view.Width * view.Height
	if view.Width <= 0 || view.Height <= 0 || view.Width > 4096 || view.Height > 4096 || total > 4<<20 || view.GridSize <= 0 || 27+total > len(raw) {
		return view, errors.New("invalid map dimensions")
	}
	var meta struct {
		Origin []int                      `json:"origin"`
		VWS    map[string]json.RawMessage `json:"vws"`
		VW     *struct {
			Line   [][]int `json:"line"`
			Rect   [][]int `json:"rect"`
			Mop    [][]int `json:"mop"`
			Addcpt [][]int `json:"addcpt"`
			Nocpt  [][]int `json:"nocpt"`
		} `json:"vw"`
		Walls struct {
			Version int `json:"version_flag"`
		} `json:"walls_info"`
		CarpetPolygon map[string][]json.RawMessage `json:"carpet_polygon"`
		Charger       []int                        `json:"charger"`
		Segments      map[string]struct {
			Name  string `json:"name"`
			Type  int    `json:"type"`
			Index int    `json:"index"`
		} `json:"seg_inf"`
	}
	if err := json.Unmarshal(raw[27+total:], &meta); err != nil {
		return view, errors.New("invalid map metadata")
	}
	view.VWS = meta.VWS
	view.Walls, view.NoGo, view.NoMop = [][]int{}, [][]int{}, [][]int{}
	if vw := meta.VW; vw != nil {
		view.RestrictionsEditable = true
		for _, list := range []*[][]int{&vw.Line, &vw.Rect, &vw.Mop} {
			for _, row := range *list {
				if len(row) != 4 {
					view.RestrictionsEditable = false // unknown row shape: do not rewrite it
				}
			}
		}
		if vw.Line != nil {
			view.Walls = vw.Line
		}
		if vw.Rect != nil {
			view.NoGo = vw.Rect
		}
		if vw.Mop != nil {
			view.NoMop = vw.Mop
		}
	}
	var added, ignored [][]int
	if meta.VW != nil {
		added, ignored = meta.VW.Addcpt, meta.VW.Nocpt
	}
	view.carpets(meta.VW != nil, meta.CarpetPolygon, added, ignored)
	view.Passable = [][]int{}
	view.Impassable = [][]int{}
	if meta.VWS != nil {
		_, supported := meta.VWS["npthrsd"]
		view.ThresholdsEditable = supported
		if raw, ok := meta.VWS["vwsl"]; ok && string(raw) != "null" {
			if json.Unmarshal(raw, &view.Passable) != nil {
				view.ThresholdsEditable = false
			}
		}
		if raw, ok := meta.VWS["npthrsd"]; ok && string(raw) != "null" {
			if json.Unmarshal(raw, &view.Impassable) != nil {
				view.ThresholdsEditable = false
			}
		}
		for _, lines := range [][][]int{view.Passable, view.Impassable} {
			for _, line := range lines {
				if len(line) != 4 {
					view.ThresholdsEditable = false
				}
			}
		}
	}
	if len(meta.Charger) == 2 {
		view.Charger = meta.Charger
	}
	view.Origin = []int{int(int16(binary.LittleEndian.Uint16(raw[23:25]))), int(int16(binary.LittleEndian.Uint16(raw[25:27])))}
	if len(meta.Origin) == 2 {
		view.Origin = meta.Origin
	}
	v3 := meta.Walls.Version == 3 || model == "dreame.vacuum.r2505"
	view.Pixels = make([]byte, total)
	type centroid struct{ X, Y, N int }
	centers := map[int]centroid{}
	for i, pixel := range raw[27 : 27+total] {
		id := int(pixel & 0x3f)
		wall := pixel&0x80 != 0
		if v3 {
			id = int(pixel & 0x1f)
			wall = (pixel>>5)&3 > 0
			if id == 31 && !wall {
				id = 0
			}
		}
		if wall {
			view.Pixels[i] = 255
			continue
		}
		if id == 0 {
			continue
		}
		view.Pixels[i] = byte(id)
		c := centers[id]
		c.X += i % view.Width
		c.Y += i / view.Width
		c.N++
		centers[id] = c
	}
	for id, c := range centers {
		info := meta.Segments[strconv.Itoa(id)]
		view.Rooms = append(view.Rooms, mapRoom{ID: id, Name: roomName(id, info.Type, info.Index, info.Name), Type: info.Type, kind: info.Type, index: info.Index, rawName: info.Name, X: float64(c.X) / float64(c.N), Y: float64(c.Y) / float64(c.N)})
	}
	sort.Slice(view.Rooms, func(i, j int) bool { return view.Rooms[i].ID < view.Rooms[j].ID })
	sum := sha256.Sum256(raw)
	view.Revision = hex.EncodeToString(sum[:])
	return view, nil
}
func (a *agent) mapView() (mapView, error) {
	empty := mapView{Empty: true, Source: "local_saved_file", Live: false, Rooms: []mapRoom{}}
	b, err := readLimited(a.path("/data/log/map_info.bin"), maxMap)
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return empty, errors.New("cannot read saved map")
	}
	var maps struct {
		Current *int `json:"curr_id"`
		Entries []struct {
			Map string `json:"map"`
		} `json:"mapstr"`
	}
	if json.Unmarshal(b, &maps) != nil {
		return empty, errors.New("invalid saved map list")
	}
	if maps.Current == nil || len(maps.Entries) == 0 {
		return empty, nil
	}
	for _, entry := range maps.Entries {
		raw, err := decodeSavedFrame(entry.Map)
		if err != nil {
			return empty, err
		}
		if int(binary.LittleEndian.Uint16(raw[:2])) == *maps.Current {
			view, err := makeMapView(raw, a.cfg.ExpectedModel)
			if err == nil {
				view.RoomSettings = a.roomSettings(view.MapID)
				o := a.obstacles(view.MapID)
				if view.Obstacles = o.List; !o.At.IsZero() {
					view.ObstaclesAt = &o.At
				}
			}
			return view, err
		}
	}
	return empty, errors.New("selected saved map is missing")
}

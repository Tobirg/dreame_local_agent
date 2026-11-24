package main

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Carpet editing as in the app's CarpetEditPage, checked on r2505 2026-11-24: ActMapAdv (6/2)
// {"cpt": {"addcpt": [...], "nocpt": [...]}} replaces both lists in vw; each item is a rectangle
// [x0, y0, x1, y1] in robot millimetres (the app snaps them to the 50 mm grid). Carpets the robot
// found stay in carpet_polygon; an ignored rectangle over one keeps it out of carpet handling.

type carpetShape struct {
	ID     int   `json:"id"`
	Points []int `json:"points"` // x, y, x, y, ... mm
}

const (
	maxAddedCarpets   = 20 // the app's limit
	maxIgnoredCarpets = 50
)

func (v *mapView) carpets(hasVW bool, polygons map[string][]json.RawMessage, added, ignored [][]int) {
	v.Carpets, v.CarpetsAdded, v.CarpetsIgnored = []carpetShape{}, [][]int{}, [][]int{}
	for k, entry := range polygons {
		id, err := strconv.Atoi(k)
		var flat []float64
		if err != nil || len(entry) == 0 || json.Unmarshal(entry[0], &flat) != nil || len(flat) < 6 || len(flat)%2 != 0 {
			continue
		}
		shape := carpetShape{ID: id, Points: make([]int, len(flat))}
		for i, f := range flat {
			shape.Points[i] = int(math.Round(f))
		}
		v.Carpets = append(v.Carpets, shape)
	}
	sort.Slice(v.Carpets, func(i, j int) bool { return v.Carpets[i].ID < v.Carpets[j].ID })
	v.CarpetsEditable = hasVW
	for _, list := range [][][]int{added, ignored} {
		for _, row := range list {
			if len(row) != 4 {
				v.CarpetsEditable = false // unknown row shape (ellipse, id): do not rewrite it
			}
		}
	}
	if added != nil {
		v.CarpetsAdded = added
	}
	if ignored != nil {
		v.CarpetsIgnored = ignored
	}
}

func (a *agent) carpetsPatch(raw json.RawMessage) (map[string]any, error) {
	var request struct {
		MapID    int     `json:"map_id"`
		Revision string  `json:"revision"`
		Added    [][]int `json:"carpets_added"`
		Ignored  [][]int `json:"carpets_ignored"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil {
		return nil, errors.New("invalid carpets edit")
	}
	current, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if current.Empty || !current.CarpetsEditable || len(current.Origin) != 2 {
		return nil, errors.New("current saved map does not expose editable carpets")
	}
	if request.MapID != current.MapID || request.Revision == "" || request.Revision != current.Revision {
		return nil, errors.New("map changed; refresh before editing carpets")
	}
	if request.Added == nil || request.Ignored == nil || len(request.Added) > maxAddedCarpets || len(request.Ignored) > maxIgnoredCarpets {
		return nil, errors.New("expected carpets_added (at most 20) and carpets_ignored (at most 50) lists")
	}
	maxX, maxY := current.Origin[0]+current.Width*current.GridSize, current.Origin[1]+current.Height*current.GridSize
	snap := func(v, start int) int { // nearest grid line, as the app's adjustValue
		g := current.GridSize
		return start + int(math.Round(float64(v-start)/float64(g)))*g
	}
	out := [2][][]int{{}, {}}
	for i, list := range [][][]int{request.Added, request.Ignored} {
		for _, r := range list {
			if len(r) != 4 {
				return nil, errors.New("a carpet area is [x0, y0, x1, y1]")
			}
			x0, y0 := snap(min(r[0], r[2]), current.Origin[0]), snap(min(r[1], r[3]), current.Origin[1])
			x1, y1 := snap(max(r[0], r[2]), current.Origin[0]), snap(max(r[1], r[3]), current.Origin[1])
			if x0 < current.Origin[0] || y0 < current.Origin[1] || x1 > maxX || y1 > maxY {
				return nil, errors.New("carpet area is outside the map")
			}
			if x1-x0 < 2*current.GridSize || y1-y0 < 2*current.GridSize {
				return nil, errors.New("a carpet area must be at least 10 cm on each side")
			}
			out[i] = append(out[i], []int{x0, y0, x1, y1})
		}
	}
	return map[string]any{"cpt": map[string]any{"addcpt": out[0], "nocpt": out[1]}}, nil
}

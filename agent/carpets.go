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
	Type   int   `json:"type"`   // 5 medium-to-high pile, 6 low pile, 7 other (CarpetEditTypePage)
	// How the robot treats this carpet (setting.json2 carpet_clean_set): -1 the global
	// carpet setting (4/36), 1 avoid, 2 vacuum only, 3 vacuum with the mop taken off,
	// 6 ignore the carpet, 7 cross it. Nil when the robot keeps no row for it.
	Clean *int `json:"clean,omitempty"`
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
		shape := carpetShape{ID: id, Points: make([]int, len(flat)), Type: 7}
		if len(entry) > 1 {
			json.Unmarshal(entry[1], &shape.Type)
		}
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

// carpetTypesPatch sets the type of carpets the robot found, like CarpetEditTypePage:
// 6/2 {"carpetmaterial": {"origincpt": [[id, type]], "addcpt": [], "roomcpt": [[room, material]]}}.
// The app always sends every found carpet and every room, so unchanged ones go back as they are.
// Added rectangles get types only with a firmware id (a fifth field), which the r2505 does not give.
func (a *agent) carpetTypesPatch(raw json.RawMessage) (map[string]any, error) {
	var request struct {
		MapID    int            `json:"map_id"`
		Revision string         `json:"revision"`
		Types    map[string]int `json:"types"` // carpet id -> 5, 6 or 7
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || len(request.Types) == 0 {
		return nil, errors.New("invalid carpet types edit")
	}
	current, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if current.Empty || len(current.Carpets) == 0 {
		return nil, errors.New("current saved map has no carpets found by the robot")
	}
	if request.MapID != current.MapID || request.Revision == "" || request.Revision != current.Revision {
		return nil, errors.New("map changed; refresh before editing carpets")
	}
	known := map[string]bool{}
	origin := make([][]int, 0, len(current.Carpets))
	for _, c := range current.Carpets {
		id := strconv.Itoa(c.ID)
		known[id] = true
		t, ok := request.Types[id]
		if !ok {
			t = c.Type
		}
		origin = append(origin, []int{c.ID, t})
	}
	for id, t := range request.Types {
		if !known[id] || t < 5 || t > 7 {
			return nil, errors.New("carpet " + id + ": unknown carpet or type not 5..7")
		}
	}
	rooms := [][]int{}
	for _, r := range current.Rooms {
		if r.material != nil {
			rooms = append(rooms, []int{r.ID, *r.material})
		}
	}
	return map[string]any{"carpetmaterial": map[string]any{"origincpt": origin, "addcpt": [][]int{}, "roomcpt": rooms}}, nil
}

// carpetCleanSets reads setting.json2 carpet_clean_set[map] = [[0, carpet id, mode], ...]
// (0 = a carpet the robot found). Checked on r2505: the app's SingleCarpetSetPageV3 writes
// 6/2 {"carpetcleanset": [[0, id, mode]]} and the robot updates that row; choosing a carpet
// type also sets the recommended mode (low pile -> 3), and setting the type back does not undo it.
func (a *agent) carpetCleanSets(mapID int) map[int]int {
	b, err := readLimited(a.path(roomSettingsFile), 1<<20)
	if err != nil {
		return nil
	}
	var file struct {
		Sets map[string][][]int `json:"carpet_clean_set"`
	}
	if json.Unmarshal(b, &file) != nil {
		return nil
	}
	out := map[int]int{}
	for _, row := range file.Sets[strconv.Itoa(mapID)] {
		if len(row) >= 3 && row[0] == 0 {
			out[row[1]] = row[2]
		}
	}
	return out
}

func (v *mapView) attachCarpetClean(sets map[int]int) {
	for i := range v.Carpets {
		if mode, ok := sets[v.Carpets[i].ID]; ok {
			v.Carpets[i].Clean = &mode
		}
	}
}

var carpetCleanModes = map[int]bool{-1: true, 1: true, 2: true, 3: true, 6: true, 7: true}

func (a *agent) carpetCleanPatch(raw json.RawMessage) (map[string]any, error) {
	var request struct {
		MapID    int            `json:"map_id"`
		Revision string         `json:"revision"`
		Clean    map[string]int `json:"clean"` // carpet id -> mode
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || len(request.Clean) == 0 {
		return nil, errors.New("invalid carpet clean edit")
	}
	current, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if request.MapID != current.MapID || request.Revision == "" || request.Revision != current.Revision {
		return nil, errors.New("map changed; refresh before editing carpets")
	}
	rows := [][]int{}
	for _, c := range current.Carpets {
		mode, ok := request.Clean[strconv.Itoa(c.ID)]
		if !ok {
			continue
		}
		if c.Clean == nil {
			return nil, errors.New("the robot keeps no clean setting for carpet " + strconv.Itoa(c.ID))
		}
		if !carpetCleanModes[mode] {
			return nil, errors.New("carpet clean mode must be -1, 1, 2, 3, 6 or 7")
		}
		rows = append(rows, []int{0, c.ID, mode})
	}
	if len(rows) != len(request.Clean) {
		return nil, errors.New("unknown carpet id")
	}
	return map[string]any{"carpetcleanset": rows}, nil
}

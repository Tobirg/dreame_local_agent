package main

import (
	"encoding/json"
	"errors"
	"strings"
)

// restrictionsPatch builds the ActMapAdv (6/2) value for virtual walls and zones. Checked on
// r2505 4.3.9_2021: {"mapid","vw":{"line","rect","mop"}} replaces those three lists and keeps
// the other vw lists (cliff, nocpt, addcpt) and the rest of the map unchanged.
func (a *agent) restrictionsPatch(raw json.RawMessage) (map[string]any, error) {
	var request struct {
		MapID    int     `json:"map_id"`
		Revision string  `json:"revision"`
		Walls    [][]int `json:"walls"`
		NoGo     [][]int `json:"no_go_zones"`
		NoMop    [][]int `json:"no_mop_zones"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil {
		return nil, errors.New("invalid restrictions edit")
	}
	current, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if current.Empty || !current.RestrictionsEditable || len(current.Origin) != 2 {
		return nil, errors.New("current saved map does not expose editable walls and zones")
	}
	if request.MapID != current.MapID || request.Revision == "" || request.Revision != current.Revision {
		return nil, errors.New("map changed; refresh before editing walls and zones")
	}
	if request.Walls == nil || request.NoGo == nil || request.NoMop == nil || len(request.Walls)+len(request.NoGo)+len(request.NoMop) > 100 {
		return nil, errors.New("expected walls, no_go_zones and no_mop_zones lists, at most 100 items")
	}
	maxX, maxY := current.Origin[0]+current.Width*current.GridSize, current.Origin[1]+current.Height*current.GridSize
	for i, list := range [][][]int{request.Walls, request.NoGo, request.NoMop} {
		for _, row := range list {
			if len(row) != 4 || (row[0] == row[2] && row[1] == row[3]) {
				return nil, errors.New("each wall or zone needs two distinct points")
			}
			if i > 0 && (row[0] == row[2] || row[1] == row[3]) {
				return nil, errors.New("a zone cannot have zero width or height")
			}
			for j, c := range row {
				lo, hi := current.Origin[j%2], maxX
				if j%2 == 1 {
					hi = maxY
				}
				if c < lo || c > hi {
					return nil, errors.New("wall or zone coordinates are outside the map")
				}
			}
		}
	}
	return map[string]any{"mapid": current.MapID, "vw": map[string]any{"line": request.Walls, "rect": request.NoGo, "mop": request.NoMop}}, nil
}

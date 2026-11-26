package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Floor material, as the r2505 plugin reads seg_inf (MapDataUtil): material 2 tiles, 1 wooden
// planks with direction 0 (along x, shown as 4) or 90 (along y, shown as 3), no direction:
// along the room's longer side (1). Older maps keep 3 and 4 in material itself. The app's floor
// editor sends ActMapAdv (6/2) {"nsm": {room: {material[, direction]}}, "mapid"}.
func floorKind(material, direction *int) int {
	if material == nil || *material < 0 || *material > 4 {
		return 0
	}
	if *material == 1 && direction != nil {
		switch *direction {
		case 0:
			return 4
		case 90:
			return 3
		}
	}
	return *material
}

func (a *agent) roomFloorsPatch(raw json.RawMessage) (map[string]any, error) {
	var request struct {
		MapID    int    `json:"map_id"`
		Revision string `json:"revision"`
		Rooms    []struct {
			ID    int `json:"id"`
			Floor int `json:"floor"`
		} `json:"rooms"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil {
		return nil, errors.New("invalid room floors")
	}
	current, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if current.Empty {
		return nil, errors.New("no saved map")
	}
	if request.MapID != current.MapID || request.Revision == "" || request.Revision != current.Revision {
		return nil, errors.New("map changed; refresh before changing floors")
	}
	if len(request.Rooms) == 0 || len(request.Rooms) > 64 {
		return nil, errors.New("expected 1..64 rooms")
	}
	known := map[int]bool{}
	for _, r := range current.Rooms {
		known[r.ID] = true
	}
	nsm := map[string]any{}
	for _, r := range request.Rooms {
		key := strconv.Itoa(r.ID)
		if !known[r.ID] || nsm[key] != nil {
			return nil, errors.New("room " + key + " is not on the current map or listed twice")
		}
		switch r.Floor {
		case 0, 2:
			nsm[key] = map[string]int{"material": r.Floor}
		case 3:
			nsm[key] = map[string]int{"material": 1, "direction": 90}
		case 4:
			nsm[key] = map[string]int{"material": 1, "direction": 0}
		default:
			return nil, errors.New("floor: 0 other, 2 tiles, 3 planks along y, 4 planks along x")
		}
	}
	return map[string]any{"nsm": nsm, "mapid": current.MapID}, nil
}

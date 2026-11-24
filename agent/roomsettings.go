package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Per-room cleaning settings ("customized cleaning"). Checked on r2505 4.3.9_2021:
// /data/ri/setting.json2 keeps clean_sets[map][room] = [suction, water, repeats, order, mode, extra],
// and ActMapAdv (6/2) {"customeClean": [[room, suction, water, repeats, mode, extra]]} rewrites
// those fields of the listed rooms (order stays; an omitted extra becomes 0, so it is carried over).
const roomSettingsFile = "/data/ri/setting.json2"

// roomSettings returns the settings of the map's rooms, or nil when the robot keeps none.
func (a *agent) roomSettings(mapID int) map[string][]int {
	b, err := readLimited(a.path(roomSettingsFile), 1<<20)
	if err != nil {
		return nil
	}
	var file struct {
		CleanSets map[string]map[string][]int `json:"clean_sets"`
	}
	if json.NewDecoder(strings.NewReader(string(b))).Decode(&file) != nil {
		return nil
	}
	rooms := map[string][]int{}
	for id, row := range file.CleanSets[strconv.Itoa(mapID)] {
		if len(row) >= 6 {
			rooms[id] = row
		}
	}
	if len(rooms) == 0 {
		return nil
	}
	return rooms
}

// roomOrderPatch sets the cleaning order like the app's CustomOrderPage (old protocol, as on r2505):
// {"cleanOrder": [room, ...]} writes 1, 2, ... into the order field of those rooms.
func (a *agent) roomOrderPatch(raw json.RawMessage) (map[string]any, error) {
	var request struct {
		MapID    int    `json:"map_id"`
		Revision string `json:"revision"`
		Rooms    []int  `json:"rooms"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil {
		return nil, errors.New("invalid room order")
	}
	current, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if current.Empty || current.RoomSettings == nil {
		return nil, errors.New("this robot keeps no per-room settings for the current map")
	}
	if request.MapID != current.MapID || request.Revision == "" || request.Revision != current.Revision {
		return nil, errors.New("map changed; refresh before editing the room order")
	}
	seen := map[int]bool{}
	for _, id := range request.Rooms {
		if _, ok := current.RoomSettings[strconv.Itoa(id)]; !ok || seen[id] {
			return nil, errors.New("room " + strconv.Itoa(id) + " is not on the current map or repeats")
		}
		seen[id] = true
	}
	if len(request.Rooms) != len(current.RoomSettings) {
		return nil, errors.New("list every room of the map once")
	}
	return map[string]any{"cleanOrder": request.Rooms}, nil
}

func (a *agent) roomSettingsPatch(raw json.RawMessage) (map[string]any, error) {
	var request struct {
		MapID    int    `json:"map_id"`
		Revision string `json:"revision"`
		Rooms    []struct {
			ID      int `json:"id"`
			Suction int `json:"suction"`
			Water   int `json:"water"`
			Repeats int `json:"repeats"`
			Mode    int `json:"mode"`
		} `json:"rooms"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil {
		return nil, errors.New("invalid room settings")
	}
	current, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if current.Empty || current.RoomSettings == nil {
		return nil, errors.New("this robot keeps no per-room settings for the current map")
	}
	if request.MapID != current.MapID || request.Revision == "" || request.Revision != current.Revision {
		return nil, errors.New("map changed; refresh before editing room settings")
	}
	if len(request.Rooms) == 0 || len(request.Rooms) > 64 {
		return nil, errors.New("expected 1..64 rooms")
	}
	rows := make([][]int, 0, len(request.Rooms))
	for _, r := range request.Rooms {
		old, ok := current.RoomSettings[strconv.Itoa(r.ID)]
		if !ok {
			return nil, errors.New("room " + strconv.Itoa(r.ID) + " has no settings on the current map")
		}
		// Mode: 0 vacuum, 1 mop, 2 vacuum and mop (Tasshack names; 2 observed on r2505).
		if r.Suction < 0 || r.Suction > 3 || r.Water < 1 || r.Water > 3 || r.Repeats < 1 || r.Repeats > 3 || r.Mode < 0 || r.Mode > 2 {
			return nil, errors.New("suction 0..3, water 1..3, repeats 1..3, mode 0..2")
		}
		rows = append(rows, []int{r.ID, r.Suction, r.Water, r.Repeats, r.Mode, old[5]})
	}
	return map[string]any{"customeClean": rows}, nil
}

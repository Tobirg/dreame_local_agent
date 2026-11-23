package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Room types as in the Dreame app (AreaTypeLDS, r2505 plugin). The saved map keeps them in
// seg_inf[room] = {type, index, name}: index numbers rooms of one type from 0, name (base64)
// is used only by type 0. The app's manual rename sends ActMapAdv (6/2) {"nsr": {room:
// {type, index[, name]}}, "mapid"}.
var roomTypeNames = map[int]string{
	1: "Гостиная", 2: "Спальня", 15: "Вторая спальня", 3: "Кабинет", 4: "Кухня", 5: "Столовая",
	6: "Ванная", 7: "Балкон", 8: "Коридор", 9: "Кладовая", 10: "Гардеробная", 11: "Приёмная",
	12: "Офис", 13: "Спортзал", 14: "Зона отдыха",
}

func roomName(id, kind, index int, name string) string {
	if label, ok := roomTypeNames[kind]; ok {
		if index > 0 {
			return fmt.Sprintf("%s %d", label, index+1)
		}
		return label
	}
	if b, err := base64.StdEncoding.DecodeString(name); err == nil && len(b) > 0 && utf8.Valid(b) {
		return string(b)
	}
	if name != "" {
		return name
	}
	return fmt.Sprintf("Комната %d", id)
}

func (a *agent) roomTypesPatch(raw json.RawMessage) (map[string]any, error) {
	var request struct {
		MapID    int    `json:"map_id"`
		Revision string `json:"revision"`
		Rooms    []struct {
			ID   int    `json:"id"`
			Type int    `json:"type"`
			Name string `json:"name"`
		} `json:"rooms"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil {
		return nil, errors.New("invalid room types")
	}
	current, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if current.Empty {
		return nil, errors.New("no saved map")
	}
	if request.MapID != current.MapID || request.Revision == "" || request.Revision != current.Revision {
		return nil, errors.New("map changed; refresh before renaming rooms")
	}
	if len(request.Rooms) == 0 || len(request.Rooms) > 64 {
		return nil, errors.New("expected 1..64 rooms")
	}
	type room struct {
		id, kind, oldKind, index, oldIndex int
		name, oldName                      string
		requested                          bool
	}
	rooms := map[int]*room{}
	for _, r := range current.Rooms {
		rooms[r.ID] = &room{id: r.ID, kind: r.kind, oldKind: r.kind, index: r.index, oldIndex: r.index, name: r.rawName, oldName: r.rawName}
	}
	for _, r := range request.Rooms {
		target := rooms[r.ID]
		if target == nil || target.requested {
			return nil, errors.New("room " + strconv.Itoa(r.ID) + " is not on the current map or listed twice")
		}
		_, known := roomTypeNames[r.Type]
		name := strings.TrimSpace(r.Name)
		if r.Type == 0 && (name == "" || utf8.RuneCountInString(name) > 24) {
			return nil, errors.New("own room name: 1..24 characters")
		}
		if !known && r.Type != 0 {
			return nil, errors.New("unknown room type " + strconv.Itoa(r.Type))
		}
		target.requested, target.kind, target.name = true, r.Type, ""
		if r.Type == 0 {
			target.name = base64.StdEncoding.EncodeToString([]byte(name))
		}
	}
	// Renumber each type like the app: rooms that kept it stay in order, newly set ones go last.
	byKind := map[int][]*room{}
	for _, r := range rooms {
		byKind[r.kind] = append(byKind[r.kind], r)
	}
	nsr := map[string]any{}
	for _, list := range byKind {
		sort.Slice(list, func(i, j int) bool {
			ki, kj := list[i].kind == list[i].oldKind, list[j].kind == list[j].oldKind
			if ki != kj {
				return ki
			}
			if ki && list[i].oldIndex != list[j].oldIndex {
				return list[i].oldIndex < list[j].oldIndex
			}
			return list[i].id < list[j].id
		})
		for i, r := range list {
			r.index = i
			if r.kind == 0 {
				r.index = 0 // own names are not numbered
			}
			if !r.requested && r.index == r.oldIndex {
				continue
			}
			entry := map[string]any{"type": r.kind, "index": r.index}
			if r.kind == 0 {
				entry["name"] = r.name
			}
			nsr[strconv.Itoa(r.id)] = entry
		}
	}
	return map[string]any{"nsr": nsr, "mapid": current.MapID}, nil
}

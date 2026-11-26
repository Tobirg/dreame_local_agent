package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Quick commands of the app (QuickCommandPage, MyCommandPage, VacuumExtend.actionQuickCommand):
// 4/48 lists them as JSON [{"id", "name" (base64), ...}]; action 4/8 with 4/10 =
// {"cmd": "EDIT_COMMAND", "params": {...}} edits them: type 1 adds {id, name, tasks, mapId},
// type 3 renames, type 4 deletes. A task row is [room, suction, water, repeats, mode, extra],
// the same fields the robot keeps per room in clean_sets. New ids start at 32 like the app.
// A schedule runs one when its mop field is id<<8 (ProtocolUtil.encodeTimingMop) and area is 0.

func parseQuickCommands(value any) []map[string]any {
	s, _ := value.(string)
	var rows []map[string]any
	out := []map[string]any{}
	if json.Unmarshal([]byte(s), &rows) != nil {
		return out
	}
	for _, r := range rows {
		id, ok := r["id"].(float64)
		if !ok {
			continue
		}
		item := map[string]any{"id": int(id)}
		if b64, _ := r["name"].(string); b64 != "" {
			if name, err := base64.StdEncoding.DecodeString(b64); err == nil {
				item["name"] = string(name)
			}
		}
		if m, ok := r["mapId"]; ok {
			item["map_id"] = m
		}
		out = append(out, item)
	}
	return out
}

func quickCommandAction(did string, params map[string]any) (string, any, error) {
	value, _ := json.Marshal(map[string]any{"cmd": "EDIT_COMMAND", "params": params})
	return "action", map[string]any{"did": did, "siid": 4, "aiid": 8, "in": []any{map[string]any{"piid": 10, "value": string(value)}}}, nil
}

func (a *agent) quickCommandCommand(p commandParams, did string) (string, any, error) {
	if p.Name == "delete_quick_command" {
		if p.ID == nil || *p.ID < 1 {
			return "", nil, errors.New("delete_quick_command requires id")
		}
		return quickCommandAction(did, map[string]any{"id": *p.ID, "type": 4})
	}
	name := strings.TrimSpace(p.Label)
	if name == "" || len(name) > 60 || len(p.Rooms) == 0 || len(p.Rooms) > 32 {
		return "", nil, errors.New("save_quick_command requires a name (up to 60 bytes) and 1..32 rooms")
	}
	view, err := a.mapView()
	if err != nil {
		return "", nil, err
	}
	if view.Empty || view.RoomSettings == nil {
		return "", nil, errors.New("no saved map with per-room settings")
	}
	rows := make([][]int, 0, len(p.Rooms))
	for _, room := range p.Rooms {
		cs, ok := view.RoomSettings[strconv.Itoa(room)]
		if !ok {
			return "", nil, fmt.Errorf("room %d has no settings on the current map", room)
		}
		rows = append(rows, []int{room, cs[0], cs[1], cs[2], cs[4], cs[5]})
	}
	a.dev.mu.Lock()
	list, _ := a.dev.details["quick_commands"].([]map[string]any)
	a.dev.mu.Unlock()
	if len(list) >= 20 {
		return "", nil, errors.New("at most 20 quick commands")
	}
	used := map[int]bool{}
	for _, c := range list {
		used[c["id"].(int)] = true
	}
	id := 32
	for used[id] {
		id++
	}
	return quickCommandAction(did, map[string]any{
		"id": id, "type": 1, "mapId": view.MapID, "tasks": [][][]int{rows},
		"name": base64.StdEncoding.EncodeToString([]byte(name)),
	})
}

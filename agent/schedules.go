package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Cleaning schedules, property 8/2, format taken from the r2505 app plugin (TimeTaskPage,
// AddTimingTaskNewPage, ProtocolUtil). The robot keeps tasks joined by ';', each
// "id-enable-HH:MM-days-repeat-map-mode-mop-area". Writing one task adds it or replaces the
// task with the same id; action 8/1 with 8/3 = id deletes it. Days are seven 0/1 digits from
// Sunday to Saturday; "0000000" with repeat 0 is a one-time task. Area 0 cleans the whole map
// with the current settings; otherwise it is a comma list of encoded rooms.

var daysRe = regexp.MustCompile(`^[01]{1,7}$`)

func parseSchedules(value any) []map[string]any {
	s, _ := value.(string)
	out := []map[string]any{}
	for _, task := range strings.Split(s, ";") {
		f := strings.Split(task, "-")
		if len(f) != 9 {
			continue
		}
		id, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		days := f[3]
		if daysRe.MatchString(days) {
			days = strings.Repeat("0", 7-len(days)) + days // the app pads on the left
		}
		item := map[string]any{
			"id": id, "enabled": f[1] == "1" || f[1] == "2", "invalid": f[1] == "3", "time": f[2],
			"days": days, "repeat": f[4] != "0", "map_id": f[5], "raw": task,
		}
		if f[8] != "0" {
			rooms := []int{}
			for _, part := range strings.Split(f[8], ",") {
				if v, err := strconv.ParseUint(part, 10, 32); err == nil {
					rooms = append(rooms, int(v&255))
				}
			}
			item["rooms"] = rooms
		}
		out = append(out, item)
	}
	return out
}

// encodeTimingRoom packs a room like ProtocolUtil.encodeTimingCustom:
// mode<<20 | suction<<16 | water<<12 | repeats<<8 | room (mop temperature bit 11 left 0).
func encodeTimingRoom(room, repeats, water, suction, mode int) int {
	return mode<<20 | suction<<16 | water<<12 | repeats<<8 | room
}

func (a *agent) scheduleCommand(p commandParams, did string) (string, any, error) {
	if p.Name == "delete_schedule" {
		if p.ID == nil || *p.ID < 1 {
			return "", nil, errors.New("delete_schedule requires id")
		}
		return "action", map[string]any{"did": did, "siid": 8, "aiid": 1, "in": []any{map[string]any{"piid": 3, "value": strconv.Itoa(*p.ID)}}}, nil // a number is accepted but deletes nothing
	}
	if p.Enabled == nil || !clockRe.MatchString(p.Time) || len(p.Days) != 7 || !daysRe.MatchString(p.Days) {
		return "", nil, errors.New("set_schedule requires enabled, time HH:MM and days as seven 0/1 digits from Sunday")
	}
	a.dev.mu.Lock()
	tasks := parseSchedules(nil)
	if list, ok := a.dev.details["schedules"].([]map[string]any); ok {
		tasks = list
	}
	a.dev.mu.Unlock()
	id := 0
	if p.ID != nil {
		id = *p.ID
		if id < 1 || id > 255 {
			return "", nil, errors.New("schedule id must be 1..255")
		}
	} else {
		for _, t := range tasks {
			id = max(id, t["id"].(int))
		}
		id++
		if len(tasks) >= 20 {
			return "", nil, errors.New("at most 20 schedules")
		}
	}
	view, err := a.mapView()
	if err != nil {
		return "", nil, err
	}
	if view.Empty {
		return "", nil, errors.New("no saved map")
	}
	area, mapID, modeMop := "0", strconv.Itoa(view.MapID), "0-0"
	for _, t := range tasks {
		if p.ID != nil && t["id"] == id && p.Rooms == nil {
			// Re-saving a task without rooms (switching it on or off, a new time) keeps what it cleans.
			f := strings.Split(t["raw"].(string), "-")
			mapID, modeMop, area = f[5], f[6]+"-"+f[7], f[8]
		}
	}
	if len(p.Rooms) > 0 {
		if len(p.Rooms) > 32 || p.Repeats < 1 || p.Repeats > 3 || p.Suction < 0 || p.Suction > 3 || p.Water < 1 || p.Water > 3 {
			return "", nil, errors.New("rooms need repeats 1..3, suction 0..3, water 1..3")
		}
		known := map[int]bool{}
		for _, r := range view.Rooms {
			known[r.ID] = true
		}
		parts := make([]string, 0, len(p.Rooms))
		for _, room := range p.Rooms {
			if !known[room] || room > 255 {
				return "", nil, fmt.Errorf("room %d is not on the current map", room)
			}
			mode := 0
			if row, ok := view.RoomSettings[strconv.Itoa(room)]; ok {
				mode = row[4]
			}
			parts = append(parts, strconv.Itoa(encodeTimingRoom(room, p.Repeats, p.Water, p.Suction, mode)))
		}
		area = strings.Join(parts, ",")
	}
	enable, repeat := 0, 1
	if *p.Enabled {
		enable = 1
	}
	if p.Days == "0000000" {
		repeat = 0
	}
	task := fmt.Sprintf("%d-%d-%s-%s-%d-%s-%s-%s", id, enable, p.Time, p.Days, repeat, mapID, modeMop, area)
	return "set_properties", []any{map[string]any{"did": did, "siid": 8, "piid": 2, "value": task}}, nil
}

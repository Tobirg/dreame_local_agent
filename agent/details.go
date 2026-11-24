// Slow-changing robot details read once a minute together with the state poll:
// consumables, cleaning totals and do-not-disturb. Consumable siid/piid are checked against the
// Dreame app plugins (official); tank_filter, fluffing_roller and roller_mop_filter (17, 32, 33)
// exist only in the MIT Tasshack tables. Names are ours and stable; the robot answers only the
// ones its model has.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"time"
)

const detailsEvery = time.Minute

type consumable struct {
	name                      string
	siid, leftPiid, hoursPiid int // percent left, hours left; reset is action <siid>/1
}

var consumables = []consumable{
	{"main_brush", 9, 2, 1}, {"side_brush", 10, 2, 1}, {"filter", 11, 1, 2}, {"sensor", 16, 1, 2},
	{"tank_filter", 17, 1, 2}, {"mop_pad", 18, 1, 2}, {"silver_ion", 19, 2, 1}, {"detergent", 20, 1, 2},
	{"squeegee", 24, 2, 1}, {"dirty_water_channel", 25, 2, 1}, {"onboard_dirty_water_tank", 26, 2, 1},
	{"deodorizer", 29, 2, 1}, {"wheel", 30, 2, 1}, {"scale_inhibitor", 31, 2, 1}, {"fluffing_roller", 32, 1, 2},
	{"roller_mop_filter", 33, 1, 2}, {"water_outlet_filter", 35, 2, 1}, {"track", 36, 2, 1},
	{"washboard", 37, 2, 1}, {"filter_cleaning", 38, 2, 1}, {"roller_cover", 39, 2, 1}, {"main_brush_cover", 41, 2, 1},
}

// detailProps lists every property the slow read asks for.
func detailProps() []stateProp {
	props := []stateProp{
		{"dnd.enabled", 5, 1}, {"dnd.start", 5, 2}, {"dnd.end", 5, 3}, {"dnd_periods", 5, 4}, {"schedules", 8, 2}, {"quick_commands", 4, 48},
		{"stats.minutes", 12, 2}, {"stats.cleanings", 12, 3}, {"stats.area", 12, 4},
	}
	for _, c := range consumables {
		props = append(props, stateProp{"consumables." + c.name + ".left", c.siid, c.leftPiid}, stateProp{"consumables." + c.name + ".hours", c.siid, c.hoursPiid})
	}
	return props
}

// parseDetails turns get_properties rows into {"dnd": {...}, "stats": {...}, "consumables": {name: {...}}}.
func parseDetails(result any) map[string]any {
	raw, _ := result.(json.RawMessage)
	var rows []struct {
		SIID  int             `json:"siid"`
		PIID  int             `json:"piid"`
		Code  int             `json:"code"`
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		return nil
	}
	got := map[[2]int]any{}
	for _, r := range rows {
		if v, err := decodeParams(r.Value); r.Code == 0 && err == nil && v != nil {
			got[[2]int{r.SIID, r.PIID}] = v
		}
	}
	group := func(fields map[string][2]int) map[string]any {
		out := map[string]any{}
		for k, id := range fields {
			if v, ok := got[id]; ok {
				out[k] = v
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	details := map[string]any{}
	if dnd := group(map[string][2]int{"enabled": {5, 1}, "start": {5, 2}, "end": {5, 3}}); dnd != nil {
		details["dnd"] = dnd
	}
	if v, ok := got[[2]int{5, 4}]; ok {
		details["dnd_periods"] = parseDNDPeriods(v)
	}
	if v, ok := got[[2]int{4, 48}]; ok {
		details["quick_commands"] = parseQuickCommands(v)
	}
	if v, ok := got[[2]int{8, 2}]; ok {
		details["schedules"] = parseSchedules(v)
	}
	if stats := group(map[string][2]int{"minutes": {12, 2}, "cleanings": {12, 3}, "area": {12, 4}}); stats != nil {
		details["stats"] = stats
	}
	parts := map[string]any{}
	for _, c := range consumables {
		if v := group(map[string][2]int{"left": {c.siid, c.leftPiid}, "hours": {c.siid, c.hoursPiid}}); v != nil {
			parts[c.name] = v
		}
	}
	if len(parts) > 0 {
		details["consumables"] = parts
	}
	return details
}

func findConsumable(name string) (consumable, bool) {
	for _, c := range consumables {
		if c.name == name {
			return c, true
		}
	}
	return consumable{}, false
}

var clockRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// detailCommand builds the MIOT request for reset_consumable, set_dnd and clean_zone.
func (a *agent) detailCommand(p commandParams, did string) (string, any, error) {
	switch p.Name {
	case "reset_consumable":
		c, ok := findConsumable(p.Consumable)
		if !ok {
			return "", nil, errors.New("unknown consumable")
		}
		a.dev.mu.Lock()
		parts, _ := a.dev.details["consumables"].(map[string]any)
		_, answered := parts[c.name]
		a.dev.mu.Unlock()
		if !answered {
			return "", nil, errors.New("this robot does not report " + c.name)
		}
		return "action", map[string]any{"did": did, "siid": c.siid, "aiid": 1, "in": []any{}}, nil
	case "remote_move":
		// Manual drive (Tasshack REMOTE_CONTROL, 4/15). Each call moves a short step; the first one
		// takes the robot off the dock, and it leaves the mode by itself ~15 s after the last step.
		if p.Velocity == nil || p.Rotation == nil || *p.Velocity < -600 || *p.Velocity > 600 || *p.Rotation < -360 || *p.Rotation > 360 {
			return "", nil, errors.New("remote_move requires velocity -600..600 and rotation -360..360")
		}
		value := fmt.Sprintf(`{"spdv":%d,"spdw":%d,"audio":"false","random":%d}`, *p.Velocity, *p.Rotation, rand.Intn(65535))
		return "set_properties", []any{map[string]any{"did": did, "siid": 4, "piid": 15, "value": value}}, nil
	case "set_dnd":
		if p.Enabled == nil || !clockRe.MatchString(p.Start) || !clockRe.MatchString(p.End) {
			return "", nil, errors.New("set_dnd requires enabled and start/end as HH:MM")
		}
		return "set_properties", []any{
			map[string]any{"did": did, "siid": 5, "piid": 1, "value": *p.Enabled},
			map[string]any{"did": did, "siid": 5, "piid": 2, "value": p.Start},
			map[string]any{"did": did, "siid": 5, "piid": 3, "value": p.End},
		}, nil
	case "go_to":
		// Drive to a point for the camera, like the app's video MapControlView: 4/1 mode 23
		// (MonitorSpotMode) with {"spoint": [], "tpoint": [[x, y, 0, 0]]}; the robot stops there
		// in status 17. Mode 22 (patrol over saved points) ends at once without the cloud video
		// session, so patrol.go chains go_to instead.
		if p.Value != nil || p.Rooms != nil || p.Zones != nil || len(p.Points) != 1 {
			return "", nil, errors.New("go_to takes one point [x, y]")
		}
		view, err := a.mapView()
		if err != nil {
			return "", nil, err
		}
		if view.Empty || len(view.Origin) != 2 {
			return "", nil, errors.New("no saved map")
		}
		pt := p.Points[0]
		if len(pt) != 2 || pt[0] < view.Origin[0] || pt[1] < view.Origin[1] || pt[0] > view.Origin[0]+view.Width*view.GridSize || pt[1] > view.Origin[1]+view.Height*view.GridSize {
			return "", nil, fmt.Errorf("point %v is not [x, y] on the map", pt)
		}
		value, _ := json.Marshal(map[string]any{"spoint": [][]int{}, "tpoint": [][]int{{pt[0], pt[1], 0, 0}}})
		return "action", map[string]any{"did": did, "siid": 4, "aiid": 1, "in": []any{map[string]any{"piid": 1, "value": 23}, map[string]any{"piid": 10, "value": string(value)}}}, nil
	case "dock_self_repair":
		a.dev.mu.Lock()
		activity, _ := a.dev.snap.State["activity"].(string)
		a.dev.mu.Unlock()
		if activity != "docked" {
			return "", nil, errors.New("the robot must be on the station")
		}
		return "set_properties", []any{map[string]any{"did": did, "siid": 99, "piid": 8, "value": `{"bittest":[17,0]}`}}, nil
	case "set_schedule", "delete_schedule":
		return a.scheduleCommand(p, did)
	case "save_quick_command", "delete_quick_command":
		return a.quickCommandCommand(p, did)
	case "set_dnd_period", "delete_dnd_period":
		return a.dndPeriodCommand(p, did)
	case "clean_spot":
		in, err := a.spotInput(p)
		if err != nil {
			return "", nil, err
		}
		return "action", map[string]any{"did": did, "siid": 4, "aiid": 1, "in": in}, nil
	case "clean_zone":
		in, err := a.zoneInput(p)
		if err != nil {
			return "", nil, err
		}
		return "action", map[string]any{"did": did, "siid": 4, "aiid": 1, "in": in}, nil
	}
	return "", nil, errors.New("unknown command")
}

// zoneInput checks rectangles against the saved map; coordinates are robot millimetres like the map view.
func (a *agent) zoneInput(p commandParams) ([]any, error) {
	if p.Value != nil || p.Rooms != nil || len(p.Zones) == 0 || len(p.Zones) > 5 || p.Repeats < 1 || p.Repeats > 3 ||
		p.Suction < 0 || p.Suction > 3 || p.Water < 1 || p.Water > 3 {
		return nil, errors.New("clean_zone requires 1..5 zones [x0,y0,x1,y1], repeats 1..3, suction 0..3, water 1..3")
	}
	view, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if view.Empty || len(view.Origin) != 2 {
		return nil, errors.New("no saved map")
	}
	minX, minY := view.Origin[0], view.Origin[1]
	maxX, maxY := minX+view.Width*view.GridSize, minY+view.Height*view.GridSize
	areas := make([][]int, 0, len(p.Zones))
	for _, z := range p.Zones {
		if len(z) != 4 {
			return nil, errors.New("a zone is [x0, y0, x1, y1]")
		}
		x0, y0, x1, y1 := min(z[0], z[2]), min(z[1], z[3]), max(z[0], z[2]), max(z[1], z[3])
		if x0 < minX || y0 < minY || x1 > maxX || y1 > maxY {
			return nil, fmt.Errorf("zone %v is outside the map", z)
		}
		if x1-x0 < 300 || y1-y0 < 300 {
			return nil, errors.New("a zone must be at least 30 cm on each side")
		}
		areas = append(areas, []int{x0, y0, x1, y1, p.Repeats, p.Suction, p.Water})
	}
	value, _ := json.Marshal(map[string]any{"areas": areas})
	return []any{map[string]any{"piid": 1, "value": 19}, map[string]any{"piid": 10, "value": string(value)}}, nil
}

// spotInput cleans a 1.5 x 1.5 m zone around each point, cut to the map edge. The X50 takes the
// Tasshack spot command (status 20, {"points": ...}) but cleans the whole room instead.
func (a *agent) spotInput(p commandParams) ([]any, error) {
	if p.Value != nil || p.Rooms != nil || p.Zones != nil || len(p.Points) == 0 || len(p.Points) > 3 {
		return nil, errors.New("clean_spot requires 1..3 points [x, y], repeats 1..3, suction 0..3, water 1..3")
	}
	view, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if view.Empty || len(view.Origin) != 2 {
		return nil, errors.New("no saved map")
	}
	minX, minY := view.Origin[0], view.Origin[1]
	maxX, maxY := minX+view.Width*view.GridSize, minY+view.Height*view.GridSize
	for _, pt := range p.Points {
		if len(pt) != 2 || pt[0] < minX || pt[1] < minY || pt[0] > maxX || pt[1] > maxY {
			return nil, fmt.Errorf("point %v is not [x, y] on the map", pt)
		}
		p.Zones = append(p.Zones, []int{max(pt[0]-spotHalf, minX), max(pt[1]-spotHalf, minY), min(pt[0]+spotHalf, maxX), min(pt[1]+spotHalf, maxY)})
	}
	p.Points = nil
	return a.zoneInput(p)
}

const spotHalf = 750 // half the side of the zone cleaned around a spot point, mm

// errorNames maps 2/2 codes to stable machine names (the MIT Tasshack dreame-vacuum tables; the
// firmware's own enum covers only 0..51 and has typos). Home Assistant automations use them, so
// they stay; the text people read is the app's (officialFaults, error_text).
var errorNames = map[int64]string{
	0: "no_error", 1: "drop", 2: "cliff", 3: "bumper", 4: "gesture", 5: "bumper_repeat", 6: "drop_repeat",
	7: "optical_flow", 8: "box", 9: "tankbox", 10: "waterbox_empty", 11: "box_full", 12: "brush", 13: "side_brush",
	14: "fan", 15: "left_wheel_motor", 16: "right_wheel_motor", 17: "turn_suffocate", 18: "forward_suffocate",
	19: "charger_get", 20: "battery_low", 21: "charge_fault", 22: "battery_percentage", 23: "heart",
	24: "camera_occlusion", 25: "move", 26: "flow_shielding", 27: "infrared_shielding", 28: "charge_no_electric",
	29: "battery_fault", 30: "fan_speed_error", 31: "left_wheel_speed", 32: "right_wheel_speed", 33: "bmi055_acce",
	34: "bmi055_gyro", 35: "xv7001", 36: "left_magnet", 37: "right_magnet", 38: "flow_error", 39: "infrared_fault",
	40: "camera_fault", 41: "strong_magnet", 42: "water_pump", 43: "rtc", 44: "auto_key_trig", 45: "p3v3",
	46: "camera_idle", 47: "blocked", 48: "lds_error", 49: "lds_bumper", 50: "water_pump", 51: "filter_blocked",
	54: "edge", 55: "carpet", 56: "laser", 57: "edge", 58: "ultrasonic", 59: "no_go_zone", 61: "route", 62: "route",
	63: "blocked", 64: "blocked", 65: "restricted", 66: "restricted", 67: "restricted", 68: "remove_mop",
	69: "mop_removed", 70: "mop_removed", 71: "mop_pad_stop_rotate", 72: "mop_pad_stop_rotate", 74: "mop_install_failed",
	75: "low_battery_turn_off", 76: "dirty_tank_not_installed", 78: "robot_in_hidden_room", 79: "lds_failed_to_lift",
	80: "robot_stuck", 81: "robot_stuck_repeat", 82: "slippery_floor", 84: "unknown_error", 85: "check_mop_install",
	86: "dirty_water_tank_full", 88: "retractable_leg_stuck", 89: "internal_error", 90: "robot_stuck",
	91: "robot_stuck_on_tables", 92: "robot_stuck_on_passage", 93: "robot_stuck_on_threshold",
	94: "robot_stuck_on_low_lying_area", 95: "robot_stuck_on_ramp", 96: "robot_stuck_on_obstacle", 97: "robot_stuck_on_pet",
	98: "robot_stuck_on_slippery_surface", 99: "robot_stuck_on_carpet", 101: "bin_full", 102: "bin_open", 103: "bin_open",
	104: "bin_full", 105: "water_tank", 106: "dirty_water_tank", 107: "water_tank_dry", 108: "dirty_water_tank",
	109: "dirty_water_tank_blocked", 110: "dirty_water_tank_pump", 111: "mop_pad", 112: "wet_mop_pad", 114: "clean_mop_pad",
	116: "clean_tank_level", 117: "station_disconnected", 118: "dirty_tank_level", 119: "washboard_level",
	120: "no_mop_in_station", 121: "dust_bag_full", 122: "unknown_warning", 123: "self_test_failed",
	124: "washboard_not_working", 125: "drainage_failed", 126: "mop_not_detected", 127: "mop_holder_error", 128: "dock_error",
	129: "wash_failed", 200: "robot_stuck_on_curtain", 201: "edge_mop_stop_rotate", 202: "edge_mop_detached",
	203: "chassis_lift_malfunction", 207: "internal_error", 209: "mop_cover_error", 210: "roller_mop_error",
	212: "robotic_arm_stopped", 213: "onboard_water_tank_empty", 214: "onboard_dirty_water_tank_full", 215: "mop_not_installed",
	217: "lds_error", 218: "roller_mop_error", 222: "fluffing_roller_error", 223: "mop_cover_error", 224: "mop_cover_error",
	225: "roller_mop_error", 226: "blocked_by_obstacle", 227: "drainage_outlet_filter", 228: "main_wheels_error",
	229: "internal_error", 230: "internal_error", 1000: "return_to_charge_failed",
}

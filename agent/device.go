// Copyright 2026 Dreame Local contributors. MIT license.
// Device model layer: one poller owns periodic robot reads and every client gets
// the same cached, normalized snapshot; capabilities combine the firmware's own
// per-model hardware table, properties the robot actually answers, and a known
// firmware fingerprint; named commands replace raw siid/aiid for adapters.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type stateProp struct {
	name       string
	siid, piid int
}

// IDs as used by ui/app.js. Observed on r2505 4.3.9_2021: charging (3/2) 1 on dock, 2 off dock,
// 5 returning; station (4/25) 0 idle, 1 mop wash/wetting, 2 drying, 5 seen during post-clean wash.
var stateProps = []stateProp{
	{"status", 4, 1}, {"error", 2, 2}, {"battery", 3, 1}, {"charging", 3, 2},
	{"suction", 4, 4}, {"water", 4, 5}, {"station", 4, 25}, {"volume", 7, 1},
	{"region", 4, 19}, {"region_code", 14, 3},
	// Current or last run: minutes and square metres (Tasshack cleaning_time / cleaned_area).
	{"cleaning_time", 4, 2}, {"cleaned_area", 4, 3},
	// Bit mask the robot reports about itself (app plugin SUPPORT_CAPABILITY); r2505 answers 31.
	{"device_capability", 4, 83},
}

// deviceCapabilityBits names the 4/83 bits as the app plugin does.
var deviceCapabilityBits = []string{"video_monitor", "quick_command", "streamer_and_ai", "smart_charge", "record_sound"}

// 4/1 codes, same table as ui/app.js statusNames.
var statusNames = map[int64]string{
	0: "idle", 1: "paused", 2: "cleaning", 3: "returning", 4: "part_cleaning", 5: "edge_cleaning",
	6: "charging", 12: "error", 13: "remote_control", 14: "sleeping", 17: "idle", 18: "room_cleaning", 19: "zone_cleaning",
	20: "spot_cleaning", 21: "mapping", 15: "test_mode", 22: "patrol", 23: "monitor_spot", 1501: "water_self_check",
	// 1510 + n: check n of the app's BIT list is running (BITSingleDeviceCheck2, CHECK_MODEL_VALUE).
	1527: "station_self_repair",
}

// activity collapses firmware status into the small set every smart home understands.
func activity(status int64, charging int64, hasCharging bool) string {
	switch status {
	case 2, 4, 5, 18, 19, 20, 21:
		return "cleaning"
	case 1:
		return "paused"
	case 3:
		return "returning"
	case 6:
		return "docked"
	case 12:
		return "error"
	}
	// Idle, sleep, station work and unknown codes: decide by charging contact (1 charging, 3 complete).
	if hasCharging && (charging == 1 || charging == 3) {
		return "docked"
	}
	return "idle"
}

type snapshot struct {
	Seq       uint64          `json:"seq"`
	UpdatedAt *time.Time      `json:"updated_at"`
	Stale     bool            `json:"stale"`
	Error     string          `json:"error,omitempty"`
	State     map[string]any  `json:"state"`
	Command   *commandOutcome `json:"command,omitempty"` // see outcome.go
}

type device struct {
	mu        sync.Mutex
	snap      snapshot
	polled    bool // at least one successful poll, so missing properties mean "not answered"
	poke      chan struct{}
	hashOnce  sync.Once
	hashes    map[string]string
	hwOnce    sync.Once
	hw        map[string]any // this model's row of the firmware hardware table; nil if unknown
	details   map[string]any // consumables, stats, dnd; see details.go
	detailsAt time.Time
	run       *cleanRun     // cleaning in progress; see history.go
	changed   chan struct{} // closed on the next snapshot change; see watch
	streams   atomic.Int32
}

func int64Of(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil
}

func (a *agent) poll(ctx context.Context) {
	props := stateProps
	a.dev.mu.Lock()
	withDetails := time.Since(a.dev.detailsAt) >= detailsEvery
	a.dev.mu.Unlock()
	if withDetails {
		props = append(append([]stateProp(nil), stateProps...), detailProps()...)
	}
	list := make([]map[string]int, len(props))
	for i, p := range props {
		list[i] = map[string]int{"siid": p.siid, "piid": p.piid}
	}
	params, _ := json.Marshal(list)
	result, err := a.call(ctx, request{Method: "get_properties", Params: params})
	var state map[string]any
	if err == nil {
		state, err = normalize(result)
	}
	a.dev.mu.Lock()
	defer a.dev.mu.Unlock()
	if err == nil && withDetails {
		a.dev.details, a.dev.detailsAt = parseDetails(result), time.Now()
	}
	for k, v := range a.dev.details {
		if state != nil {
			state[k] = v
		}
	}
	if err != nil {
		// Keep the last known state but mark it stale: missing data is not "unchanged".
		if !a.dev.snap.Stale || a.dev.snap.Error != err.Error() {
			a.bumpLocked()
		}
		a.dev.snap.Stale, a.dev.snap.Error = true, err.Error()
		return
	}
	now := time.Now().UTC()
	if a.settleCommandLocked(state, now) || a.dev.snap.Stale || !reflect.DeepEqual(a.dev.snap.State, state) {
		a.bumpLocked()
	}
	prev := a.dev.snap.State
	a.dev.snap = snapshot{Seq: a.dev.snap.Seq, UpdatedAt: &now, State: state, Command: a.dev.snap.Command}
	a.dev.polled = true
	a.trackRun(state, withDetails)
	a.noteMessages(prev, state)
}

func normalize(result any) (map[string]any, error) {
	raw, ok := result.(json.RawMessage)
	var rows []struct {
		SIID  int             `json:"siid"`
		PIID  int             `json:"piid"`
		Code  int             `json:"code"`
		Value json.RawMessage `json:"value"`
	}
	if !ok || json.Unmarshal(raw, &rows) != nil {
		return nil, errors.New("invalid robot property response")
	}
	state := map[string]any{}
	for _, row := range rows {
		if row.Code != 0 || len(row.Value) == 0 {
			continue
		}
		for _, p := range stateProps {
			if p.siid == row.SIID && p.piid == row.PIID {
				if v, err := decodeParams(row.Value); err == nil && v != nil {
					state[p.name] = v
				}
			}
		}
	}
	if status, ok := int64Of(state["status"]); ok {
		charging, hasCharging := int64Of(state["charging"])
		state["activity"] = activity(status, charging, hasCharging)
		if name, ok := statusNames[status]; ok {
			state["status_name"] = name
		} else if status > 1510 && status < 1600 {
			state["status_name"] = "self_check"
		}
	}
	if code, ok := int64Of(state["error"]); ok {
		if name, ok := errorNames[code]; ok {
			state["error_name"] = name
		}
		if text, ok := officialFaults[code]; ok {
			state["error_text"], state["error_text_en"] = text[0], text[1]
		}
	}
	return state, nil
}

func (a *agent) snapshot() snapshot {
	a.dev.mu.Lock()
	defer a.dev.mu.Unlock()
	s := a.dev.snap
	if s.State == nil {
		s.Stale, s.State = true, map[string]any{}
	}
	return s
}

// refreshSoon asks the poller for early reads after a command; never blocks.
// The robot still returns the old value right after a set (measured: new value within 100 ms),
// and status changes such as start/dock take longer, so read twice instead of at once.
func (a *agent) refreshSoon() {
	for _, d := range refreshDelays {
		time.AfterFunc(d, func() {
			select {
			case a.dev.poke <- struct{}{}:
			default:
			}
		})
	}
}

var refreshDelays = []time.Duration{500 * time.Millisecond, 2 * time.Second}

func (a *agent) runPoller(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		task, cancel := context.WithTimeout(ctx, 40*time.Second)
		a.poll(task)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-a.dev.poke:
		}
	}
}

// ---- capabilities ----

// Firmware the commands were checked against. Values from dreame-research/protocol/firmware-profiles.json.
var knownProfiles = []struct {
	id, model, firmware string
	hashes              map[string]string
}{{
	id: "r2505-r2440-4.3.9_2021", model: "dreame.vacuum.r2505", firmware: "4.3.9_2021",
	hashes: map[string]string{
		"/usr/bin/avacmd":          "83fcb84903ee251eae9028d11480708db211691c802cd194c4235daaacfd3556",
		"/ava/lib/node_msg_cvt.so": "1394b26ba6d865994290e071b8414fcc72c3a2bdaa3ae49e120c7873cce8ce3b",
		"/ava/lib/node_iot.so":     "dd5bf5b3da2ab44d14224dbb0aa4137c5ce21ca6987132b00fb553eae94fec82",
		"/usr/bin/dmiot":           "0919b0c79261064c4684aaf6fd9d705aaab5aea3385779ea80efa7ea2fc8886b",
	},
}}

func fileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// firmwareProfile hashes once per process: firmware binaries only change with an OTA reboot.
func (a *agent) firmwareProfile(model, firmware string) map[string]any {
	a.dev.hashOnce.Do(func() {
		a.dev.hashes = map[string]string{}
		for _, p := range knownProfiles {
			for path := range p.hashes {
				if _, done := a.dev.hashes[path]; !done {
					a.dev.hashes[path] = fileSHA256(a.path(path))
				}
			}
		}
	})
	result := map[string]any{"profile": nil, "verified": false}
	for _, p := range knownProfiles {
		if p.model != model {
			continue
		}
		var mismatch []string
		for path, want := range p.hashes {
			if a.dev.hashes[path] != want {
				mismatch = append(mismatch, path)
			}
		}
		if len(mismatch) == 0 && p.firmware == firmware {
			return map[string]any{"profile": p.id, "verified": true}
		}
		result["closest_profile"], result["mismatch"] = p.id, mismatch
	}
	return result
}

// hardware reads the firmware's own per-model table, so new models need no agent update.
func (a *agent) hardware(model string) (map[string]any, error) {
	b, err := readLimited(a.path("/ava/conf/dconf_runtime.json"), 4<<20)
	if err != nil {
		return nil, errors.New("firmware hardware table is unavailable")
	}
	var models map[string]map[string]any
	if json.Unmarshal(b, &models) != nil {
		return nil, errors.New("firmware hardware table is not valid JSON")
	}
	hw, ok := models[model]
	if !ok {
		return nil, errors.New("model is not in the firmware hardware table")
	}
	return hw, nil
}

// deviceCapabilities decodes 4/83 into names; nil when the robot does not report it.
func deviceCapabilities(v any) []string {
	mask, ok := int64Of(v)
	if !ok {
		return nil
	}
	names := []string{}
	for i, name := range deviceCapabilityBits {
		if mask&(1<<i) != 0 {
			names = append(names, name)
		}
	}
	return names
}

// modelHardware is hardware() for this robot, read once: the table only changes with an OTA reboot.
func (a *agent) modelHardware() map[string]any {
	a.dev.hwOnce.Do(func() {
		if info, err := a.identity(); err == nil {
			a.dev.hw, _ = a.hardware(info["model"])
		}
	})
	return a.dev.hw
}

func (a *agent) capabilities() (map[string]any, error) {
	info, err := a.identity()
	if err != nil {
		return nil, err
	}
	hw, hwErr := a.hardware(info["model"])
	a.dev.mu.Lock()
	polled, state := a.dev.polled, a.dev.snap.State
	a.dev.mu.Unlock()
	props := map[string]any{}
	for _, p := range stateProps {
		_, ok := state[p.name]
		props[p.name] = map[string]any{"siid": p.siid, "piid": p.piid, "readable": readable(polled, ok)}
	}
	commands := map[string]any{}
	for name, c := range commandTable {
		available, reason := c.available(hw)
		commands[name] = map[string]any{"available": available, "reason": reason}
	}
	// Hashes of the checked firmware files go into compatibility reports, so new profiles can be added.
	firmware := a.firmwareProfile(info["model"], info["fw_ver"])
	firmware["hashes"] = a.dev.hashes
	caps := map[string]any{
		"agent":             map[string]string{"version": version},
		"identity":          map[string]string{"model": info["model"], "did": info["did"], "fw_ver": info["fw_ver"]},
		"firmware":          firmware,
		"control":           a.robotWriteAllowed(info) == nil,
		"hardware":          hw,
		"properties":        props,
		"commands":          commands,
		"device_capability": deviceCapabilities(state["device_capability"]),
		"modules":           a.listModules(),
	}
	if hwErr != nil {
		caps["hardware_error"] = hwErr.Error()
	}
	return caps, nil
}

// readable is null until the robot has answered at least once.
func readable(polled, ok bool) any {
	if !polled {
		return nil
	}
	return ok
}

// ---- named commands ----

type command struct {
	method     string // "action" or "set_properties"
	siid, id   int    // aiid for actions, piid for properties
	in         []any  // fixed action input
	min, max   int    // value range for set_properties
	hwKey      string // hardware flag that must allow this command
	hwAccepts  func(v any) bool
	clearRooms bool
	detail     bool
}

func (c command) available(hw map[string]any) (any, string) {
	if c.hwKey == "" {
		return true, ""
	}
	if hw == nil {
		return nil, "hardware table unavailable"
	}
	if v, ok := hw["RUNTIME_"+c.hwKey]; ok && !c.hwAccepts(v) {
		return false, fmt.Sprintf("%s=%v", c.hwKey, v)
	}
	return true, ""
}

func is(want string) func(any) bool { return func(v any) bool { return v == want } }
func present(v any) bool            { return v != "N" && v != "False" && v != "none" }

// Action IDs as used by ui/app.js.
var commandTable = map[string]command{
	"start":       {method: "action", siid: 2, id: 1},
	"pause":       {method: "action", siid: 2, id: 2},
	"stop":        {method: "action", siid: 4, id: 2},
	"dock":        {method: "action", siid: 3, id: 1},
	"locate":      {method: "action", siid: 7, id: 1},
	"mapping":     {method: "action", siid: 4, id: 1, in: []any{map[string]any{"piid": 1, "value": 21}}},
	"empty":       {method: "action", siid: 15, id: 1, hwKey: "AUTO_DUST_COLLECTION", hwAccepts: is("True")},
	"wash_mop":    {method: "action", siid: 4, id: 4, in: []any{map[string]any{"piid": 10, "value": "2,1"}}, hwKey: "DOCK_TYPE", hwAccepts: present},
	"dry_start":   {method: "action", siid: 4, id: 4, in: []any{map[string]any{"piid": 10, "value": "3,1"}}, hwKey: "HOT_AIR_DRYING_AC_PTC", hwAccepts: is("True")},
	"dry_stop":    {method: "action", siid: 4, id: 4, in: []any{map[string]any{"piid": 10, "value": "3,0"}}, hwKey: "HOT_AIR_DRYING_AC_PTC", hwAccepts: is("True")},
	"set_suction": {method: "set_properties", siid: 4, id: 4, min: 0, max: 3},
	"set_volume":  {method: "set_properties", siid: 7, id: 1, min: 0, max: 100},
	"clean_rooms": {method: "action", siid: 4, id: 1, clearRooms: true},
	// Built by detailCommand in details.go.
	"clean_zone":       {detail: true},
	"clean_spot":       {detail: true},
	"reset_consumable": {detail: true},
	"set_dnd":          {detail: true},
	"remote_move":      {detail: true},
	"set_schedule":     {detail: true},
	"delete_schedule":  {detail: true},
	// Station self-repair (app DockRepairPage): 99/8 bittest 17, about 5-10 minutes, status 15.
	"dock_self_repair": {detail: true},
	"go_to":            {detail: true},
}

type commandParams struct {
	Name       string  `json:"name"`
	Value      *int    `json:"value"`
	Rooms      []int   `json:"rooms"`
	Repeats    int     `json:"repeats"`
	Suction    int     `json:"suction"`
	Water      int     `json:"water"`
	Zones      [][]int `json:"zones"`
	Points     [][]int `json:"points"`
	Consumable string  `json:"consumable"`
	Enabled    *bool   `json:"enabled"`
	Start      string  `json:"start"`
	End        string  `json:"end"`
	Setting    string  `json:"setting"`  // set_setting: a ui/settings.js id
	To         any     `json:"to"`       // set_setting: the new value
	Velocity   *int    `json:"velocity"` // remote_move: forward speed, negative backwards
	Rotation   *int    `json:"rotation"` // remote_move: turn rate
	ID         *int    `json:"id"`       // set_schedule (omit to add), delete_schedule
	Time       string  `json:"time"`     // set_schedule: HH:MM
	Days       string  `json:"days"`     // set_schedule: seven 0/1 digits from Sunday
}

// runCommand is reached through call(), so it already holds the IPC gate and passed identity checks.
func (a *agent) runCommand(ctx context.Context, raw json.RawMessage, info map[string]string) (any, error) {
	var p commandParams
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil {
		return nil, errors.New("invalid command")
	}
	switch p.Name {
	case "patrol":
		return a.startPatrol(p, info["did"])
	case "locate", "set_volume", "set_suction", "set_setting", "reset_consumable", "set_dnd", "set_schedule", "delete_schedule":
	default:
		stopPatrol() // the owner took over
	}
	if p.Name == "set_setting" {
		result, err := a.setSetting(ctx, p.Setting, p.To, info["did"])
		if err == nil {
			a.refreshSoon()
		}
		return result, err
	}
	c, ok := commandTable[p.Name]
	if !ok {
		return nil, errors.New("unknown command")
	}
	if c.hwKey != "" {
		hw, _ := a.hardware(info["model"])
		// Unknown hardware does not block; only an explicit "not present" does.
		if available, reason := c.available(hw); available == false {
			return nil, errors.New("command not supported by this model: " + reason)
		}
	}
	var params any
	method := c.method
	switch {
	case c.detail:
		var err error
		if method, params, err = a.detailCommand(p, info["did"]); err != nil {
			return nil, err
		}
		a.dev.mu.Lock()
		a.dev.detailsAt = time.Time{} // re-read consumables and dnd on the next poll
		a.dev.mu.Unlock()
	case c.method == "set_properties":
		if p.Value == nil || *p.Value < c.min || *p.Value > c.max {
			return nil, fmt.Errorf("value must be %d..%d", c.min, c.max)
		}
		params = []any{map[string]any{"did": info["did"], "siid": c.siid, "piid": c.id, "value": *p.Value}}
	case c.clearRooms:
		in, err := a.roomInput(p)
		if err != nil {
			return nil, err
		}
		params = map[string]any{"did": info["did"], "siid": c.siid, "aiid": c.id, "in": in}
	default:
		if p.Value != nil || p.Rooms != nil {
			return nil, errors.New("command takes no arguments")
		}
		in := c.in
		if in == nil {
			in = []any{}
		}
		params = map[string]any{"did": info["did"], "siid": c.siid, "aiid": c.id, "in": in}
	}
	result, err := a.miot(ctx, method, params)
	if err != nil {
		return nil, err
	}
	if p.Name == "remote_move" {
		// Sent several times a second while a drive button is held: no extra polls or state events.
		return result, rejected(result)
	}
	a.refreshSoon()
	if err := rejected(result); err != nil {
		return result, err
	}
	a.noteCommand(p)
	return result, nil
}

// rejected reports a non-zero MIOT code; code0 is acceptance only, not physical completion.
func rejected(result any) error {
	raw, _ := result.(json.RawMessage)
	type row struct {
		Code int `json:"code"`
	}
	var one row
	var many []row
	if json.Unmarshal(raw, &many) != nil {
		if json.Unmarshal(raw, &one) != nil {
			return errors.New("robot response is invalid; outcome unknown; not retried")
		}
		many = []row{one}
	}
	for _, r := range many {
		if r.Code != 0 {
			return fmt.Errorf("robot rejected command (code=%d)", r.Code)
		}
	}
	return nil
}

// roomInput mirrors roomCommand in ui/app.js and refuses rooms absent from the saved map.
func (a *agent) roomInput(p commandParams) ([]any, error) {
	if p.Value != nil || len(p.Rooms) == 0 || len(p.Rooms) > 64 || p.Repeats < 1 || p.Repeats > 3 ||
		p.Suction < 0 || p.Suction > 3 || p.Water < 1 || p.Water > 3 {
		return nil, errors.New("clean_rooms requires rooms, repeats 1..3, suction 0..3, water 1..3")
	}
	view, err := a.mapView()
	if err != nil {
		return nil, err
	}
	known := map[int]bool{}
	for _, r := range view.Rooms {
		known[r.ID] = true
	}
	selects := make([][]int, 0, len(p.Rooms))
	for _, id := range p.Rooms {
		if !known[id] {
			return nil, fmt.Errorf("room %d is not on the current map", id)
		}
		selects = append(selects, []int{id, p.Repeats, p.Suction, p.Water, 1})
	}
	value, _ := json.Marshal(map[string]any{"selects": selects})
	return []any{map[string]any{"piid": 1, "value": 18}, map[string]any{"piid": 10, "value": string(value)}}, nil
}

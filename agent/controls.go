package main

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"time"
)

func (a *agent) queueReboot() (any, error) {
	command := a.rebootCommand
	if command == "" {
		var err error
		command, err = exec.LookPath("reboot")
		if err != nil {
			return nil, errors.New("native reboot command is unavailable")
		}
	}
	if !a.rebootQueued.CompareAndSwap(false, true) {
		return nil, errors.New("reboot is already requested")
	}
	go func() {
		time.Sleep(time.Second)
		if exec.Command(command).Run() != nil {
			a.rebootQueued.Store(false)
		}
	}()
	return map[string]any{"accepted": true, "rebooting": true}, nil
}
func (a *agent) thresholdPatch(raw json.RawMessage) (map[string]any, error) {
	var request struct {
		MapID      int     `json:"map_id"`
		Revision   string  `json:"revision"`
		Passable   [][]int `json:"passable"`
		Impassable [][]int `json:"impassable"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil {
		return nil, errors.New("invalid threshold edit")
	}
	current, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if current.Empty || !current.ThresholdsEditable {
		return nil, errors.New("current saved map does not expose editable passable/impassable thresholds")
	}
	if request.MapID != current.MapID || request.Revision == "" || request.Revision != current.Revision {
		return nil, errors.New("map changed; refresh before editing thresholds")
	}
	if request.Passable == nil || request.Impassable == nil || len(request.Passable)+len(request.Impassable) > 100 {
		return nil, errors.New("expected both threshold lists, at most 100 lines")
	}
	for _, lines := range [][][]int{request.Passable, request.Impassable} {
		for _, line := range lines {
			if len(line) != 4 || (line[0] == line[2] && line[1] == line[3]) {
				return nil, errors.New("threshold requires two distinct points")
			}
			for i, coordinate := range line {
				axis := i % 2
				max := current.Width
				if axis == 1 {
					max = current.Height
				}
				if coordinate < current.Origin[axis] || coordinate > current.Origin[axis]+max*current.GridSize {
					return nil, errors.New("threshold coordinates are outside the map")
				}
			}
		}
	}
	// Preserve ramps, cliffs and unknown firmware fields. Only the two line lists change.
	vws := map[string]any{}
	for key, value := range current.VWS {
		vws[key] = value
	}
	vws["vwsl"] = request.Passable
	vws["npthrsd"] = request.Impassable
	return map[string]any{"mapid": current.MapID, "vws": vws}, nil
}

// changeSetting checks the fresh physical value and writes within the same IPC gate.
func (a *agent) changeSetting(ctx context.Context, raw json.RawMessage, did string) (any, error) {
	var p struct {
		SIID     int             `json:"siid"`
		PIID     int             `json:"piid"`
		Expected json.RawMessage `json:"expected"`
		Value    json.RawMessage `json:"value"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil || p.SIID < 1 || p.SIID > 65535 || p.PIID < 1 || p.PIID > 65535 || len(p.Expected) == 0 || len(p.Value) == 0 {
		return nil, errors.New("invalid setting change")
	}
	result, err := a.miot(ctx, "get_properties", []any{map[string]any{"did": did, "siid": p.SIID, "piid": p.PIID}})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		SIID  int             `json:"siid"`
		PIID  int             `json:"piid"`
		Code  int             `json:"code"`
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(result.(json.RawMessage), &rows) != nil || len(rows) != 1 || rows[0].Code != 0 || len(rows[0].Value) == 0 || (rows[0].SIID != 0 && rows[0].SIID != p.SIID) || (rows[0].PIID != 0 && rows[0].PIID != p.PIID) {
		return nil, errors.New("setting is unavailable")
	}
	expected, err := decodeParams(p.Expected)
	if err != nil {
		return nil, err
	}
	current, err := decodeParams(rows[0].Value)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(expected, current) {
		return nil, errors.New("setting changed; refresh its value before applying")
	}
	value, err := decodeParams(p.Value)
	if err != nil {
		return nil, err
	}
	return a.miot(ctx, "set_properties", []any{map[string]any{"did": did, "siid": p.SIID, "piid": p.PIID, "value": value}})
}

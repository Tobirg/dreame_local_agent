package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"time"
)

type mapReplyError struct {
	reason  string
	outcome string
	reply   json.RawMessage
}

func (e *mapReplyError) Error() string { return e.reason }

func rpcErrorResponse(err error) map[string]any {
	body := map[string]any{"error": err.Error()}
	var operation *mapReplyError
	if errors.As(err, &operation) {
		body["result"] = operation.reply
		body["outcome"] = operation.outcome
	}
	return body
}

func isMapEditAction(method string, params any) bool {
	p, ok := params.(map[string]any)
	return method == "action" && ok &&
		(p["siid"] == json.Number("6") || p["siid"] == 6) &&
		(p["aiid"] == json.Number("2") || p["aiid"] == 2)
}

// The map handler has an operation result separate from the dispatch code.
// Even a valid zero is acceptance only, never proof of physical completion.
func validateMapReply(reply json.RawMessage) error {
	fail := func(outcome, reason string) error {
		return &mapReplyError{reason: reason, outcome: outcome, reply: reply}
	}
	var result struct {
		Code *int64          `json:"code"`
		SIID *int64          `json:"siid"`
		AIID *int64          `json:"aiid"`
		Out  json.RawMessage `json:"out"`
	}
	if json.Unmarshal(reply, &result) != nil || result.Code == nil ||
		(result.SIID != nil && *result.SIID != 6) || (result.AIID != nil && *result.AIID != 2) {
		return fail("indeterminate", "Map response is invalid; outcome unknown; not retried")
	}
	if *result.Code != 0 {
		return fail("rejected", fmt.Sprintf("Map command rejected (code=%d)", *result.Code))
	}
	var rows []struct {
		PIID  int64           `json:"piid"`
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(result.Out, &rows) != nil {
		return fail("indeterminate", "Map operation result is invalid; outcome unknown; not retried")
	}
	var opRet *int64
	for _, row := range rows {
		if row.PIID != 6 {
			continue
		}
		var value *int64
		if opRet != nil || json.Unmarshal(row.Value, &value) != nil || value == nil {
			return fail("indeterminate", "Map operation result is ambiguous; outcome unknown; not retried")
		}
		opRet = value
	}
	if opRet == nil {
		return fail("indeterminate", "Map operation result is missing; outcome unknown; not retried")
	}
	if *opRet != 0 {
		return fail("rejected", fmt.Sprintf("Map operation rejected (op_ret=%d)", *opRet))
	}
	return nil
}

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

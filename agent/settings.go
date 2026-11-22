package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// settingDef is one entry of ui/settings.js, the catalog the panel uses; the agent reads the
// same embedded file so modules (Home Assistant) get exactly the panel's settings.
type settingDef struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Kind    string `json:"kind"`
	SIID    int    `json:"siid"`
	PIID    int    `json:"piid"`
	Bit     int64  `json:"bit,omitempty"`
	JSONKey string `json:"json_key,omitempty"`
	Auto    string `json:"auto,omitempty"`
	Packed  string `json:"packed,omitempty"`
	Window  string `json:"window,omitempty"`
	Options []struct {
		Value any    `json:"value"`
		Label string `json:"label"`
	} `json:"options,omitempty"`
	Min  *float64 `json:"min,omitempty"`
	Max  *float64 `json:"max,omitempty"`
	Step *float64 `json:"step,omitempty"`
}

var settingsCatalog = sync.OnceValues(func() ([]settingDef, error) {
	b, err := webAssets.ReadFile("ui/settings.js")
	if err != nil {
		return nil, err
	}
	s := string(b)
	start, end := strings.Index(s, "["), strings.LastIndex(s, "]")
	if start < 0 || end < start {
		return nil, errors.New("settings catalog is malformed")
	}
	var defs []settingDef
	if err := json.Unmarshal([]byte(s[start:end+1]), &defs); err != nil {
		return nil, err
	}
	return defs, nil
})

// Settings Home Assistant can show as a switch, select or number; time windows stay in the panel.
func (d settingDef) simple() bool {
	return d.Kind == "bool" || d.Kind == "select" || d.Kind == "number"
}

// "Off" for these auto settings keeps the last level as a negative number (as the app does).
var autoKeepsLevel = map[string]bool{"MeticulousTwist": true, "MopScalable": true, "SmartAutoMop": true, "SmartAutoWash": true}

// decode mirrors decodeSetting in ui/app.js; ok is false when the robot does not have the setting.
func (d settingDef) decode(raw any) (any, bool) {
	if raw == nil {
		return nil, false
	}
	if d.Auto != "" || d.JSONKey != "" {
		object := raw
		if s, isString := raw.(string); isString && json.Unmarshal([]byte(s), &object) != nil {
			return nil, false
		}
		if d.Auto != "" {
			entries, isList := object.([]any)
			if !isList {
				entries = []any{object}
			}
			for _, e := range entries {
				if m, _ := e.(map[string]any); m != nil && m["k"] == d.Auto {
					v, found := m["v"]
					return v, found && v != nil
				}
			}
			return nil, false
		}
		m, _ := object.(map[string]any)
		v, found := m[d.JSONKey]
		return v, found
	}
	n, isNumber := raw.(float64)
	whole := isNumber && n == float64(int64(n)) && n >= 0 && n <= 0xffffffff
	switch {
	case d.Bit != 0:
		return whole && int64(n)&d.Bit != 0, whole
	case d.Packed != "":
		if !whole {
			return nil, false
		}
		v := int64(n)
		switch d.Packed {
		case "mode":
			return float64(swap02(v & 3)), true
		case "interval":
			return float64(v >> 8 & 255), true
		}
		return float64(v >> 16), true
	case d.Kind == "bool":
		if _, isBool := raw.(bool); !isBool && !whole {
			return nil, false
		}
	}
	return raw, true
}

// swap02: the panel's mode 0 (dry) is code 2 on the robot and the other way round.
func swap02(v int64) int64 {
	switch v {
	case 0:
		return 2
	case 2:
		return 0
	}
	return v
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// encode mirrors settingValue in ui/app.js: the new raw property value for value, given the current raw.
func (d settingDef) encode(value, raw any) (any, error) {
	number := func() (float64, error) {
		switch v := value.(type) {
		case float64:
			return v, nil
		case bool:
			return float64(boolInt(v)), nil
		}
		return 0, fmt.Errorf("%s needs a number", d.ID)
	}
	switch {
	case d.Auto != "":
		n, err := number()
		if err != nil {
			return nil, err
		}
		if autoKeepsLevel[d.Auto] && n == 0 {
			if current, ok := d.decode(raw); ok {
				if c, _ := current.(float64); c > 0 {
					n = -c
				}
			}
		}
		b, _ := json.Marshal(map[string]any{"k": d.Auto, "v": n})
		return string(b), nil
	case d.JSONKey != "":
		s, isString := raw.(string)
		var data map[string]any
		if isString {
			if json.Unmarshal([]byte(s), &data) != nil {
				return nil, errors.New("setting is unavailable")
			}
		} else if m, ok := raw.(map[string]any); ok {
			data = map[string]any{}
			for k, v := range m {
				data[k] = v
			}
		} else {
			return nil, errors.New("setting is unavailable")
		}
		if _, wasBool := data[d.JSONKey].(bool); wasBool {
			on, _ := value.(bool)
			data[d.JSONKey] = on
		} else {
			n, err := number()
			if err != nil {
				return nil, err
			}
			data[d.JSONKey] = n
		}
		if !isString {
			return data, nil
		}
		b, _ := json.Marshal(data)
		return string(b), nil
	}
	current, _ := raw.(float64)
	cur := int64(current)
	switch {
	case d.Bit != 0:
		on, isBool := value.(bool)
		if !isBool {
			return nil, fmt.Errorf("%s needs true or false", d.ID)
		}
		if on {
			return float64(cur | d.Bit), nil
		}
		return float64(cur &^ d.Bit), nil
	case d.Packed != "":
		n, err := number()
		if err != nil {
			return nil, err
		}
		next := int64(n)
		switch d.Packed {
		case "mode":
			return float64(cur&^3 | swap02(next)), nil
		case "interval":
			return float64(cur&^0xff00 | next<<8), nil
		}
		return float64(cur&0xffff | next<<16), nil
	case d.Kind == "bool":
		on, isBool := value.(bool)
		if !isBool {
			return nil, fmt.Errorf("%s needs true or false", d.ID)
		}
		if _, rawBool := raw.(bool); rawBool {
			return on, nil
		}
		if on && current > 0 {
			return raw, nil
		}
		return float64(boolInt(on)), nil
	}
	return value, nil
}

// valid checks a value against the catalog's options or range before anything is written.
func (d settingDef) valid(value any) bool {
	switch d.Kind {
	case "bool":
		_, ok := value.(bool)
		return ok
	case "select":
		for _, o := range d.Options {
			if fmt.Sprint(o.Value) == fmt.Sprint(value) {
				return true
			}
		}
	case "number":
		n, ok := value.(float64)
		return ok && (d.Min == nil || n >= *d.Min) && (d.Max == nil || n <= *d.Max)
	}
	return false
}

type settingState struct {
	settingDef
	Value any `json:"value"`
}

// readSettings returns the simple settings this robot has, with their current values.
func (a *agent) readSettings(ctx context.Context, did string) ([]settingState, error) {
	defs, err := settingsCatalog()
	if err != nil {
		return nil, err
	}
	raw, err := a.settingRaw(ctx, did, defs)
	if err != nil {
		return nil, err
	}
	out := []settingState{}
	seen := map[string]bool{}
	for _, d := range defs {
		r, found := raw[[2]int{d.SIID, d.PIID}]
		if !d.simple() || seen[d.ID] || !found {
			continue
		}
		if v, ok := d.decode(r); ok {
			seen[d.ID] = true
			out = append(out, settingState{d, v})
		}
	}
	return out, nil
}

func (a *agent) settingRaw(ctx context.Context, did string, defs []settingDef) (map[[2]int]any, error) {
	var pairs [][2]int
	known := map[[2]int]bool{}
	for _, d := range defs {
		k := [2]int{d.SIID, d.PIID}
		if !known[k] {
			known[k] = true
			pairs = append(pairs, k)
		}
	}
	out := map[[2]int]any{}
	for i := 0; i < len(pairs); i += 20 {
		var props []any
		for _, k := range pairs[i:min(i+20, len(pairs))] {
			props = append(props, map[string]any{"did": did, "siid": k[0], "piid": k[1]})
		}
		result, err := a.miot(ctx, "get_properties", props)
		if err != nil {
			return nil, err
		}
		var rows []struct {
			SIID, PIID, Code int
			Value            any
		}
		raw, _ := result.(json.RawMessage)
		if json.Unmarshal(raw, &rows) != nil {
			return nil, errors.New("invalid settings response")
		}
		for _, r := range rows {
			if r.Code == 0 && r.Value != nil {
				out[[2]int{r.SIID, r.PIID}] = r.Value
			}
		}
	}
	return out, nil
}

// setSetting changes one catalog setting by id, through changeSetting's compare-and-set.
func (a *agent) setSetting(ctx context.Context, id string, value any, did string) (any, error) {
	defs, err := settingsCatalog()
	if err != nil {
		return nil, err
	}
	for _, d := range defs {
		if d.ID != id || !d.simple() {
			continue
		}
		if !d.valid(value) {
			return nil, fmt.Errorf("invalid value for %s", id)
		}
		values, err := a.settingRaw(ctx, did, []settingDef{d})
		if err != nil {
			return nil, err
		}
		current, found := values[[2]int{d.SIID, d.PIID}]
		if _, ok := d.decode(current); !found || !ok {
			continue // another catalog entry with this id may match the robot's format
		}
		next, err := d.encode(value, current)
		if err != nil {
			return nil, err
		}
		params, _ := json.Marshal(map[string]any{"siid": d.SIID, "piid": d.PIID, "expected": current, "value": next})
		return a.changeSetting(ctx, params, did)
	}
	return nil, fmt.Errorf("setting %s is not available on this robot", id)
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func catalogEntry(t *testing.T, id string, match func(settingDef) bool) settingDef {
	t.Helper()
	defs, err := settingsCatalog()
	if err != nil || len(defs) < 50 {
		t.Fatal("catalog", len(defs), err)
	}
	for _, d := range defs {
		if d.ID == id && (match == nil || match(d)) {
			return d
		}
	}
	t.Fatal("no setting", id)
	return settingDef{}
}

func TestSettingCodec(t *testing.T) {
	mode := catalogEntry(t, "cleaning_mode", nil)
	// 5120 = vacuum and mop (code 0) is the panel's 2; vacuum only (code 2) is the panel's 0.
	if v, ok := mode.decode(float64(5120)); !ok || v != float64(2) {
		t.Fatal(v, ok)
	}
	if v, _ := mode.encode(float64(0), float64(5120)); v != float64(5122) {
		t.Fatal("vacuum only", v)
	}
	humidity := catalogEntry(t, "mop_humidity", nil)
	if v, _ := humidity.decode(float64(5122)); v != float64(0) {
		t.Fatal("humidity is the high half", v)
	}
	bit := catalogEntry(t, "ai_bit_ai_pet_detection", nil)
	if v, _ := bit.encode(true, float64(6)); v != float64(22) {
		t.Fatal(v)
	}
	if v, ok := bit.decode(float64(22)); !ok || v != true {
		t.Fatal(v)
	}
	auto := catalogEntry(t, "mop_pad_swing", nil)
	if v, ok := auto.decode(`[{"k":"MopScalable","v":2},{"k":"x","v":1}]`); !ok || v != float64(2) {
		t.Fatal(v, ok)
	}
	if v, _ := auto.encode(float64(0), `{"k":"MopScalable","v":2}`); v != `{"k":"MopScalable","v":-2}` {
		t.Fatal("off keeps the level", v)
	}
	if _, ok := auto.decode(`{"k":"Other","v":1}`); ok {
		t.Fatal("missing auto key decoded")
	}
	key := catalogEntry(t, "ai_json_ai_pet_detection", nil)
	if v, _ := key.encode(true, `{"whether_have_pet":false,"other":1}`); v != `{"other":1,"whether_have_pet":true}` {
		t.Fatal(v)
	}
	lock := catalogEntry(t, "child_lock", nil)
	if lock.valid(float64(1)) || !lock.valid(true) || !mode.valid(float64(3)) || mode.valid(float64(9)) {
		t.Fatal("validation")
	}
	if v, _ := lock.encode(false, float64(1)); v != float64(0) {
		t.Fatal(v)
	}
}

func TestSettingsOnRobot(t *testing.T) {
	a := testAgent(t)
	log := filepath.Join(a.root, "writes.log")
	script := `#!/usr/bin/env python3
import json,os,sys
r=json.loads(sys.argv[1][len('msg_cvt'):])
values={'4/23':5120,'4/27':0,'4/22':6,'4/50':'[{"k":"MopScalable","v":2}]'}
if r['method']=='get_properties':
    res=[dict(p,code=0,value=values[f"{p['siid']}/{p['piid']}"]) if f"{p['siid']}/{p['piid']}" in values else dict(p,code=-4001) for p in r['params']]
else:
    open(os.path.join(os.path.dirname(os.path.abspath(__file__)),'writes.log'),'a').write(json.dumps(r['params'])+'\n')
    res=[dict(x,code=0) for x in r['params']]
print(json.dumps({'id':r['id'],'result':res}))
`
	if err := os.WriteFile(a.avacmd, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := a.readSettings(context.Background(), "12345678")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{}
	for _, s := range got {
		values[s.ID] = s.Value
	}
	if values["cleaning_mode"] != float64(2) || values["child_lock"] != float64(0) || values["mop_pad_swing"] != float64(2) || values["ai_bit_ai_pet_detection"] != false {
		t.Fatal(values)
	}
	if _, found := values["dnd"]; found {
		t.Fatal("settings the robot lacks must be left out")
	}
	if _, err := a.setSetting(context.Background(), "cleaning_mode", float64(0), "12345678"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), `"piid": 23, "siid": 4, "value": 5122`) {
		t.Fatal(string(b))
	}
	for _, bad := range []struct {
		id    string
		value any
	}{{"cleaning_mode", float64(9)}, {"child_lock", "yes"}, {"dnd", true}, {"nope", true}} {
		if _, err := a.setSetting(context.Background(), bad.id, bad.value, "12345678"); err == nil {
			t.Error("accepted", bad)
		}
	}
}

func TestMapCardCalibration(t *testing.T) {
	a := testAgent(t)
	raw, err := decodeSavedFrame(syntheticFrame(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:36], []byte(`{"origin":[-100,-200]}`)...)
	writeFile(t, a, "/data/log/map_info.bin", `{"curr_id":2,"mapstr":[{"map":"`+encodeTestFrame(t, raw)+`"}]}`)
	card, err := a.mapCard()
	if err != nil {
		t.Fatal(err)
	}
	// 3x3 cells of 50 mm from (-100,-200), 3 pixels a cell, rows going up: robot (0,0) is pixel (6, -3), as mapPNG draws it.
	first := card["calibration_points"].([]map[string]any)[0]
	if !reflect.DeepEqual(first["map"], map[string]float64{"x": 6, "y": -3}) {
		t.Fatal(first)
	}
	// Room 1 fills cells (0,0), (1,0) and (0,1) in the bottom-left corner: centre inside them, in millimetres.
	room, _ := card["rooms"].(map[string]any)["1"].(map[string]any)
	if x, _ := room["x"].(int); room == nil || x < -100 || x > 0 {
		t.Fatal(card["rooms"])
	}
	// The L of three cells: six corners, counter-clockwise from the bottom-left, in millimetres.
	want := [][2]int{{-100, -200}, {0, -200}, {0, -150}, {-50, -150}, {-50, -100}, {-100, -100}}
	if got := room["outline"]; !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

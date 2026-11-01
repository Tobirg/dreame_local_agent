package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func thresholdFixture(t *testing.T, a *agent) mapView {
	t.Helper()
	raw, err := decodeSavedFrame(syntheticFrame(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:36], []byte(`{"origin":[-100,-200],"vws":{"vwsl":[[-90,-190,-60,-150]],"npthrsd":[],"ramp":[[1,2,3,4,7]],"cliff":[[5,6,7,8]],"future_field":{"keep":true}}}`)...)
	view, err := makeMapView(raw, "dreame.vacuum.r2505")
	if err != nil {
		t.Fatal(err)
	}
	// Encode the test map through the same native container shape.
	encoded := encodeTestFrame(t, raw)
	body, _ := json.Marshal(map[string]any{"curr_id": 2, "mapstr": []any{map[string]any{"map": encoded}}})
	if err = os.WriteFile(a.path("/data/log/map_info.bin"), body, 0600); err != nil {
		t.Fatal(err)
	}
	return view
}
func TestThresholdActionPreservesRampsCliffsAndFutureFields(t *testing.T) {
	a := testAgent(t)
	view := thresholdFixture(t, a)
	input, _ := json.Marshal(map[string]any{"map_id": 2, "revision": view.Revision, "passable": [][]int{}, "impassable": [][]int{{-80, -180, -50, -150}}})
	result, err := a.call(context.Background(), request{Method: "local.thresholds", Params: input})
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		DID   string `json:"did"`
		SIID  int    `json:"siid"`
		AIID  int    `json:"aiid"`
		Input []struct {
			PIID  int    `json:"piid"`
			Value string `json:"value"`
		} `json:"in"`
	}
	if json.Unmarshal(result.(json.RawMessage), &action) != nil || action.DID != "12345678" || action.SIID != 6 || action.AIID != 2 || len(action.Input) != 1 || action.Input[0].PIID != 4 {
		t.Fatal(string(result.(json.RawMessage)))
	}
	var patch struct {
		VWS map[string]json.RawMessage `json:"vws"`
	}
	if json.Unmarshal([]byte(action.Input[0].Value), &patch) != nil {
		t.Fatal("bad map patch")
	}
	for _, key := range []string{"ramp", "cliff", "future_field"} {
		if string(patch.VWS[key]) != string(view.VWS[key]) {
			t.Fatalf("lost %s", key)
		}
	}
}
func TestThresholdValidationAndStaleMap(t *testing.T) {
	a := testAgent(t)
	view := thresholdFixture(t, a)
	for _, change := range []map[string]any{
		{"map_id": 3, "revision": view.Revision, "passable": [][]int{}, "impassable": [][]int{}},
		{"map_id": 2, "revision": "stale", "passable": [][]int{}, "impassable": [][]int{}},
		{"map_id": 2, "revision": view.Revision, "passable": [][]int{{1, 1, 1, 1}}, "impassable": [][]int{}},
		{"map_id": 2, "revision": view.Revision, "passable": [][]int{{-500, -180, 0, -100}}, "impassable": [][]int{}},
		{"map_id": 2, "revision": view.Revision, "passable": [][]int{}},
	} {
		body, _ := json.Marshal(change)
		if _, err := a.call(context.Background(), request{Method: "local.thresholds", Params: body}); err == nil {
			t.Fatal(change)
		}
	}
}
func TestSettingOptimisticCheckBeforeWrite(t *testing.T) {
	for _, expected := range []int{1, 3} {
		t.Run(string(rune('0'+expected)), func(t *testing.T) {
			a := testAgent(t)
			marker := a.path("/written.json")
			script := "#!/usr/bin/env python3\nimport sys,json\nr=json.loads(sys.argv[1][7:])\np=r['params'][0]\nif r['method']=='get_properties':\n p.update(code=0,value=3)\nelse:\n open(" + quotePython(marker) + ",'w').write(json.dumps(p))\n p.update(code=0)\nprint(json.dumps({'id':r['id'],'result':[p]}))\n"
			_ = os.WriteFile(a.avacmd, []byte(script), 0700)
			params, _ := json.Marshal(map[string]any{"siid": 4, "piid": 12, "expected": expected, "value": 2})
			_, err := a.call(context.Background(), request{Method: "local.setting", Params: params})
			_, written := os.Stat(marker)
			if expected == 3 {
				if err != nil || written != nil {
					t.Fatal(err, written)
				}
			} else if err == nil || !os.IsNotExist(written) {
				t.Fatal("stale setting was written", err, written)
			}
		})
	}
}
func quotePython(value string) string { b, _ := json.Marshal(value); return string(b) }
func TestRebootUsesNativeCommandOnceAndRequiresControl(t *testing.T) {
	a := testAgent(t)
	marker := filepath.Join(t.TempDir(), "rebooted")
	a.rebootCommand = filepath.Join(t.TempDir(), "reboot")
	_ = os.WriteFile(a.rebootCommand, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700)
	a.cfg.AllowControl = false
	if _, err := a.call(context.Background(), request{Method: "local.reboot"}); err == nil {
		t.Fatal("disabled reboot accepted")
	}
	a.cfg.AllowControl = true
	if _, err := a.call(context.Background(), request{Method: "local.reboot", Params: json.RawMessage(`{"command":"shell"}`)}); err == nil {
		t.Fatal("parameters accepted")
	}
	result, err := a.call(context.Background(), request{Method: "local.reboot"})
	if err != nil || result.(map[string]any)["accepted"] != true {
		t.Fatal(result, err)
	}
	if _, err := a.call(context.Background(), request{Method: "local.reboot"}); err == nil {
		t.Fatal("duplicate reboot accepted")
	}
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native reboot command not invoked")
}
func TestWiFiSignalQualityAndMissingData(t *testing.T) {
	ssid, dbm, quality := wifiSignal("wpa_state=COMPLETED\nssid=Home\n", "RSSI=-65\nLINKSPEED=72\n")
	if ssid != "Home" || dbm == nil || *dbm != -65 || quality == nil || *quality != 70 {
		t.Fatal(ssid, dbm, quality)
	}
	ssid, _, _ = wifiSignal("wpa_state=COMPLETED\nssid=Home\\x20WiFi\n", "RSSI=-60\n")
	if ssid != "Home WiFi" {
		t.Fatal("encoded SSID not decoded", ssid)
	}
	for _, line := range []string{"RSSI=not-a-number", "RSSI=0", ""} {
		_, rssi, pct := wifiSignal("", line)
		if rssi != nil || pct != nil {
			t.Fatal(line)
		}
	}
	for _, tc := range []struct {
		line  string
		value int
	}{{"RSSI=-110", 0}, {"RSSI=-35", 100}} {
		_, _, q := wifiSignal("", tc.line)
		if q == nil || *q != tc.value {
			t.Fatal(tc)
		}
	}
}
func TestThresholdViewExposesSavedGeometry(t *testing.T) {
	a := testAgent(t)
	view := thresholdFixture(t, a)
	if !view.ThresholdsEditable || len(view.Passable) != 1 || len(view.Impassable) != 0 {
		t.Fatal(view)
	}
	b, _ := json.Marshal(view)
	if strings.Contains(string(b), "future_field") {
		t.Fatal("internal map metadata leaked")
	}
}

func encodeTestFrame(t *testing.T, raw []byte) string {
	t.Helper()
	var packed bytes.Buffer
	w := zlib.NewWriter(&packed)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(packed.Bytes())
}

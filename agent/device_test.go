package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mockRobot answers get_properties from a fixed table (5/5 unsupported), echoes writes
// with code 0 (code -1 for value 13) and logs every request.
func mockRobot(t *testing.T, a *agent) string {
	t.Helper()
	log := filepath.Join(a.root, "requests.log")
	script := `#!/usr/bin/env python3
import json,os,sys
root=os.path.dirname(os.path.abspath(__file__))
if os.path.exists(os.path.join(root,'fail')): sys.exit(1)
r=json.loads(sys.argv[1][len('msg_cvt'):])
open(os.path.join(root,'requests.log'),'a').write(json.dumps(r)+'\n')
values={'4/1':0,'3/1':80,'3/2':1,'4/4':2,'2/2':0}
if r['method']=='get_properties':
    res=[dict(p,code=0,value=values[f"{p['siid']}/{p['piid']}"]) if f"{p['siid']}/{p['piid']}" in values else dict(p,code=-4001) for p in r['params']]
else:
    res=r['params'] if isinstance(r['params'],list) else dict(r['params'])
    code=-1 if '"value": 13' in json.dumps(r) else 0
    res=[dict(x,code=code) for x in res] if isinstance(res,list) else dict(res,code=code)
print(json.dumps({'id':r['id'],'result':res}))
`
	mock := filepath.Join(a.root, "avacmd")
	if err := os.WriteFile(mock, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a.avacmd = mock
	return log
}

func writeFile(t *testing.T, a *agent, path, content string) {
	t.Helper()
	full := a.path(path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestActivityMapping(t *testing.T) {
	for _, c := range []struct {
		status, charging int64
		has              bool
		want             string
	}{{18, 0, false, "cleaning"}, {1, 0, false, "paused"}, {3, 0, false, "returning"}, {6, 0, false, "docked"},
		{12, 0, false, "error"}, {0, 3, true, "docked"}, {14, 2, true, "idle"}, {0, 0, false, "idle"}, {99, 1, true, "docked"}} {
		if got := activity(c.status, c.charging, c.has); got != c.want {
			t.Errorf("activity(%d,%d,%v)=%s want %s", c.status, c.charging, c.has, got, c.want)
		}
	}
}

func TestPollerSnapshotAndStale(t *testing.T) {
	a := testAgent(t)
	mockRobot(t, a)
	if s := a.snapshot(); !s.Stale || len(s.State) != 0 {
		t.Fatal("snapshot before first poll must be stale and empty", s)
	}
	a.poll(context.Background())
	s := a.snapshot()
	if s.Stale || s.State["activity"] != "docked" || s.State["status_name"] != "idle" || s.State["battery"] != json.Number("80") {
		t.Fatal(s)
	}
	if _, ok := s.State["station"]; ok {
		t.Fatal("unsupported property must be absent", s.State)
	}
	seq := s.Seq
	a.poll(context.Background())
	if a.snapshot().Seq != seq {
		t.Fatal("unchanged state must keep seq")
	}
	writeFile(t, a, "fail", "")
	a.poll(context.Background())
	s = a.snapshot()
	if !s.Stale || s.Error == "" || s.State["battery"] != json.Number("80") || s.Seq == seq {
		t.Fatal("failed poll must keep last state, mark stale and bump seq", s)
	}
}

func TestCommandSendsMappedActionWithDID(t *testing.T) {
	a := testAgent(t)
	log := mockRobot(t, a)
	if _, err := a.call(context.Background(), request{Method: "local.command", Params: json.RawMessage(`{"name":"dock"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.call(context.Background(), request{Method: "local.command", Params: json.RawMessage(`{"name":"set_suction","value":3}`)}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"method": "action"`) || !strings.Contains(lines[0], `"siid": 3`) || !strings.Contains(lines[0], `"aiid": 1`) ||
		!strings.Contains(lines[0], `"did": "12345678"`) || !strings.Contains(lines[1], `"set_properties"`) || !strings.Contains(lines[1], `"value": 3`) {
		t.Fatal(lines)
	}
}

func TestCommandValidation(t *testing.T) {
	a := testAgent(t)
	log := mockRobot(t, a)
	for _, params := range []string{
		`{"name":"nope"}`, `{"name":"set_suction","value":9}`, `{"name":"set_suction"}`, `{"name":"start","value":1}`,
		`{"name":"start","extra":1}`, `{"name":"clean_rooms","rooms":[1],"repeats":1,"suction":1,"water":2}`,
	} {
		if _, err := a.call(context.Background(), request{Method: "local.command", Params: json.RawMessage(params)}); err == nil {
			t.Error("accepted", params)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("invalid command reached the robot")
	}
	a.cfg.AllowControl = false
	if _, err := a.call(context.Background(), request{Method: "local.command", Params: json.RawMessage(`{"name":"start"}`)}); err == nil {
		t.Fatal("control disabled but command accepted")
	}
}

func TestCommandRejectedByRobot(t *testing.T) {
	a := testAgent(t)
	mockRobot(t, a)
	_, err := a.call(context.Background(), request{Method: "local.command", Params: json.RawMessage(`{"name":"set_volume","value":13}`)})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatal("non-zero robot code must be an error", err)
	}
	if rejected(json.RawMessage(`{"code":-1}`)) == nil || rejected(json.RawMessage(`[{"code":0},{"code":-3}]`)) == nil || rejected(json.RawMessage(`[{"code":0}]`)) != nil {
		t.Fatal("rejected() misreads MIOT codes")
	}
}

func TestHardwareGatesStationCommands(t *testing.T) {
	a := testAgent(t)
	log := mockRobot(t, a)
	empty := request{Method: "local.command", Params: json.RawMessage(`{"name":"empty"}`)}
	// Unknown hardware (no table) does not block.
	if _, err := a.call(context.Background(), empty); err != nil {
		t.Fatal(err)
	}
	writeFile(t, a, "/ava/conf/dconf_runtime.json", `{"dreame.vacuum.r2505":{"RUNTIME_AUTO_DUST_COLLECTION":"N"}}`)
	if _, err := a.call(context.Background(), empty); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatal("explicitly absent hardware must block", err)
	}
	b, _ := os.ReadFile(log)
	if strings.Count(string(b), "\n") != 1 {
		t.Fatal("blocked command reached the robot")
	}
}

func TestCapabilities(t *testing.T) {
	a := testAgent(t)
	mockRobot(t, a)
	writeFile(t, a, "/ava/conf/dconf_runtime.json", `{"dreame.vacuum.r2505":{"RUNTIME_AUTO_DUST_COLLECTION":"True","RUNTIME_HOT_AIR_DRYING_AC_PTC":"N","RUNTIME_BUMPER_H":81.0}}`)
	caps, err := a.capabilities()
	if err != nil {
		t.Fatal(err)
	}
	props := caps["properties"].(map[string]any)
	if props["battery"].(map[string]any)["readable"] != nil {
		t.Fatal("readability is unknown before the first poll")
	}
	a.poll(context.Background())
	caps, _ = a.capabilities()
	props = caps["properties"].(map[string]any)
	if props["battery"].(map[string]any)["readable"] != true || props["station"].(map[string]any)["readable"] != false {
		t.Fatal(props)
	}
	cmds := caps["commands"].(map[string]any)
	if cmds["empty"].(map[string]any)["available"] != true || cmds["dry_start"].(map[string]any)["available"] != false || cmds["start"].(map[string]any)["available"] != true {
		t.Fatal(cmds)
	}
	fw := caps["firmware"].(map[string]any)
	if fw["verified"] != false || fw["closest_profile"] != "r2505-r2440-4.3.9_2021" || len(fw["mismatch"].([]string)) != 4 {
		t.Fatal("mock binaries must not verify", fw)
	}
	if caps["hardware"].(map[string]any)["RUNTIME_BUMPER_H"] != 81.0 {
		t.Fatal(caps["hardware"])
	}
}

func TestHTTPStateAndCommand(t *testing.T) {
	a := testAgent(t)
	mockRobot(t, a)
	a.poll(context.Background())
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	do := func(method, path, body string, auth bool) (int, map[string]any) {
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		return res.StatusCode, v
	}
	if code, _ := do("GET", "/api/v1/state", "", false); code != 401 {
		t.Fatal("state without token", code)
	}
	code, v := do("GET", "/api/v1/state", "", true)
	if code != 200 || v["result"].(map[string]any)["state"].(map[string]any)["activity"] != "docked" {
		t.Fatal(code, v)
	}
	if code, _ := do("POST", "/api/v1/state", "", true); code != 405 {
		t.Fatal("state must be GET only", code)
	}
	if code, v := do("POST", "/api/v1/command", `{"name":"start"}`, true); code != 200 {
		t.Fatal(code, v)
	}
	if code, _ := do("POST", "/api/v1/command", `{"name":`, true); code != 400 {
		t.Fatal("malformed command", code)
	}
	if code, _ := do("GET", "/api/v1/capabilities", "", true); code != 200 {
		t.Fatal("capabilities", code)
	}
}

func TestRefreshSoonWaitsForRobot(t *testing.T) {
	a := &agent{}
	a.dev.poke = make(chan struct{}, 1)
	a.refreshSoon()
	select {
	case <-a.dev.poke:
		t.Fatal("poked at once; the robot still answers the old value then")
	case <-time.After(300 * time.Millisecond):
	}
	for range refreshDelays {
		select {
		case <-a.dev.poke:
		case <-time.After(3 * time.Second):
			t.Fatal("no delayed poke")
		}
	}
}

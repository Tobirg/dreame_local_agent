package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testAgent(t *testing.T) *agent {
	t.Helper()
	root := t.TempDir()
	for p, content := range map[string]string{
		"data/config/dmio/device.conf": "model=dreame.vacuum.r2505\ndid=12345678\nmac=00:11:22:33:44:55\nkey=DO_NOT_RETURN\n",
		"etc/os-release":               "{\"fw_arm_ver\":\"4.3.9_2021\"}",
		"data/log/map_info.bin":        "{\"mapstr\":[],\"curr_id\":null}",
	} {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mock := filepath.Join(root, "avacmd")
	script := "#!/usr/bin/env python3\nimport json,sys\nr=json.loads(sys.argv[1][len('msg_cvt'):])\nprint('firmware log')\nprint(json.dumps({'id':r['id'],'result':r['params']}))\n"
	if err := os.WriteFile(mock, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return &agent{root: root, avacmd: mock, gate: make(chan struct{}, 1), cfg: config{Token: strings.Repeat("a", 40), ExpectedModel: "dreame.vacuum.r2505", ExpectedDID: "12345678", ExpectedMAC: "00:11:22:33:44:55", AllowControl: true}}
}
func TestIdentityFiltersSecrets(t *testing.T) {
	a := testAgent(t)
	v, e := a.identity()
	if e != nil {
		t.Fatal(e)
	}
	if v["key"] != "" || v["fw_ver"] != "4.3.9_2021" {
		t.Fatal(v)
	}
}
func TestWrongRobotRejected(t *testing.T) {
	a := testAgent(t)
	a.cfg.ExpectedDID = "999"
	if _, e := a.identity(); e == nil {
		t.Fatal("identity mismatch accepted")
	}
}
func TestConflictingConfigRejected(t *testing.T) {
	a := testAgent(t)
	p := a.path("/data/config/miio/device.conf")
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte("did=999"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := a.identity(); e == nil {
		t.Fatal("conflicting config accepted")
	}
}
func TestReadMapsUsesFixedFile(t *testing.T) {
	a := testAgent(t)
	v, e := a.call(context.Background(), request{Method: "local.maps"})
	if e != nil {
		t.Fatal(e)
	}
	if !json.Valid(v.(json.RawMessage)) {
		t.Fatal("bad JSON")
	}
	if _, e := a.call(context.Background(), request{Method: "local.maps", Params: json.RawMessage(`{"path":"/etc/private"}`)}); e == nil {
		t.Fatal("arbitrary path accepted")
	}
}
func TestMalformedMapDoesNotReturnStaleSuccess(t *testing.T) {
	a := testAgent(t)
	_ = os.WriteFile(a.path("/data/log/map_info.bin"), []byte("{"), 0600)
	if _, e := a.call(context.Background(), request{Method: "local.maps"}); e == nil {
		t.Fatal("broken JSON accepted")
	}
}
func TestMissingMapsHaveEmptyList(t *testing.T) {
	a := testAgent(t)
	_ = os.Remove(a.path("/data/log/map_info.bin"))
	if _, e := a.call(context.Background(), request{Method: "local.maps"}); e != nil {
		t.Fatal(e)
	}
}
func TestDirectArgvAndPhysicalDID(t *testing.T) {
	a := testAgent(t)
	input := `{"did":"4.1","siid":4,"aiid":1,"in":[{"piid":1,"value":"' $(touch /tmp/dreame-injected) ; ` + "`echo BAD`" + `"}]}`
	v, e := a.call(context.Background(), request{Method: "action", Params: json.RawMessage(input)})
	if e != nil {
		t.Fatal(e)
	}
	var r map[string]any
	if json.Unmarshal(v.(json.RawMessage), &r) != nil || r["did"] != "12345678" {
		t.Fatal(string(v.(json.RawMessage)))
	}
	value := r["in"].([]any)[0].(map[string]any)["value"].(string)
	if !strings.Contains(value, "$(touch") {
		t.Fatal("argument was changed")
	}
}
func TestDisabledControlStillAllowsReads(t *testing.T) {
	a := testAgent(t)
	a.cfg.AllowControl = false
	if _, e := a.call(context.Background(), request{Method: "get_properties", Params: json.RawMessage(`[{"siid":3,"piid":1}]`)}); e != nil {
		t.Fatal(e)
	}
	if _, e := a.call(context.Background(), request{Method: "action", Params: json.RawMessage(`{"siid":6,"aiid":1,"in":[]}`)}); e != nil {
		t.Fatal(e)
	}
	if _, e := a.call(context.Background(), request{Method: "action", Params: json.RawMessage(`{"siid":4,"aiid":1,"in":[]}`)}); e == nil {
		t.Fatal("control enabled")
	}
}
func TestControlRequiresPinning(t *testing.T) {
	a := testAgent(t)
	a.cfg.ExpectedMAC = ""
	if _, e := a.call(context.Background(), request{Method: "set_properties", Params: json.RawMessage(`[{"siid":4,"piid":4,"value":2}]`)}); e == nil {
		t.Fatal("unpinned control")
	}
}
func TestAllowlistAndIDs(t *testing.T) {
	for _, r := range []request{{Method: "shell", Params: json.RawMessage(`{}`)}, {Method: "action", Params: json.RawMessage(`{"siid":true,"aiid":1}`)}, {Method: "set_properties", Params: json.RawMessage(`[{"siid":4,"piid":4}]`)}, {Method: "get_properties", Params: json.RawMessage(`[]`)}} {
		a := testAgent(t)
		if _, e := a.call(context.Background(), r); e == nil {
			t.Fatalf("accepted %s", r.Method)
		}
	}
}
func TestReplyMatching(t *testing.T) {
	v, e := parseReply([]byte(`log {"id":2,"result":false} {"id":3,"result":[1]}`), 3)
	if e != nil || string(v) != "[1]" {
		t.Fatal(string(v), e)
	}
}
func TestReplyMissingAndError(t *testing.T) {
	for _, s := range []string{`{"ret":"ok"}`, `{"id":3,"error":{"code":-1}}`} {
		if _, e := parseReply([]byte(s), 3); e == nil {
			t.Fatal("invalid response accepted")
		}
	}
}
func TestHTTPAuthAndRoundTrip(t *testing.T) {
	a := testAgent(t)
	for _, authorized := range []bool{false, true} {
		r := httptest.NewRequest("POST", "/rpc", strings.NewReader(`{"method":"miIO.info"}`))
		if authorized {
			r.Header.Set("Authorization", "Bearer "+a.cfg.Token)
		}
		w := httptest.NewRecorder()
		a.handler(w, r)
		expected := http.StatusUnauthorized
		if authorized {
			expected = 200
		}
		if w.Code != expected {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestTrailingJSONRejected(t *testing.T) {
	a := testAgent(t)
	r := httptest.NewRequest("POST", "/rpc", strings.NewReader(`{"method":"miIO.info"} {}`))
	r.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	w := httptest.NewRecorder()
	a.handler(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestCancellationDoesNotStartQueuedCommand(t *testing.T) {
	a := testAgent(t)
	a.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := a.call(ctx, request{Method: "miIO.info"}); e == nil {
		t.Fatal("cancelled request ran")
	}
}

func TestOutputBufferIsBoundedWithIOCopy(t *testing.T) {
	var b limitedBuffer
	_, err := io.Copy(&b, strings.NewReader(strings.Repeat("x", 17<<20)))
	if err != nil || !b.overflow || b.buf.Len() != 16<<20 {
		t.Fatal("output buffer limit failed", err, b.buf.Len())
	}
}

func TestRegionMessageUsesExactDirectArguments(t *testing.T) {
	a := testAgent(t)
	script := "#!/usr/bin/env python3\nimport json,sys\nassert sys.argv[1]=='msg_cvt'\nr=json.loads(sys.argv[2])\nassert r=={'type':'msgCvt','cmd':'device_lock','code':200}\nprint('message sent')\n"
	if err := os.WriteFile(a.avacmd, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := a.call(context.Background(), request{Method: "local.region_lock", Params: json.RawMessage(`{"code":200}`)})
	if err != nil {
		t.Fatal(err)
	}
	v := result.(map[string]any)
	if v["dispatched"] != true || v["confirmed"] != false {
		t.Fatal(v)
	}
}
func TestRegionMessageValidationAndControl(t *testing.T) {
	for _, raw := range []string{`null`, `{"code":201}`, `{"code":"200"}`, `{"code":200,"cmd":"shell"}`, `{"code":200.0}`} {
		a := testAgent(t)
		if _, err := a.call(context.Background(), request{Method: "local.region_lock", Params: json.RawMessage(raw)}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, field := range []string{"disabled", "model", "did", "mac", "identity"} {
		a := testAgent(t)
		switch field {
		case "disabled":
			a.cfg.AllowControl = false
		case "model":
			a.cfg.ExpectedModel = ""
		case "did":
			a.cfg.ExpectedDID = ""
		case "mac":
			a.cfg.ExpectedMAC = ""
		case "identity":
			a.cfg.ExpectedDID = "other"
		}
		if _, err := a.call(context.Background(), request{Method: "local.region_lock", Params: json.RawMessage(`{"code":401}`)}); err == nil {
			t.Fatalf("accepted %s", field)
		}
	}
}
func TestRegionFailedDispatchIsNotSuccess(t *testing.T) {
	a := testAgent(t)
	_ = os.WriteFile(a.avacmd, []byte("#!/bin/sh\nexit 1\n"), 0700)
	if _, err := a.call(context.Background(), request{Method: "local.region_lock", Params: json.RawMessage(`{"code":401}`)}); err == nil {
		t.Fatal("failed command accepted")
	}
}

func TestDetachedChildHelper(t *testing.T) {
	if os.Getenv("DREAME_DAEMON_TEST") != "1" {
		return
	}
	sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, 0, 0, 0)
	if errno != 0 {
		os.Exit(2)
	}
	fmt.Printf("pid=%d sid=%d\n", os.Getpid(), sid)
	time.Sleep(time.Minute)
	os.Exit(0)
}
func TestDetachedProcessHasOwnSessionAndLog(t *testing.T) {
	t.Setenv("DREAME_DAEMON_TEST", "1")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	pidPath := filepath.Join(dir, "agent.pid")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := startDetached(executable, []string{"-test.run=^TestDetachedChildHelper$"}, logPath, pidPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, 0, nil)
	}()
	data, err := os.ReadFile(pidPath)
	if err != nil || strings.TrimSpace(string(data)) != strconv.Itoa(pid) {
		t.Fatal("PID file", err, string(data))
	}
	expected := fmt.Sprintf("pid=%d sid=%d", pid, pid)
	for attempt := 0; attempt < 100; attempt++ {
		data, _ = os.ReadFile(logPath)
		if strings.Contains(string(data), expected) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child did not establish a new session", string(data))
}

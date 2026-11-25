package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// monitorAgent answers 10001/4 1102 with verify code `answer` and logs every msg_cvt call.
func monitorAgent(t *testing.T, answer int) (*agent, func() []string) {
	t.Helper()
	a := testAgent(t)
	a.cfg.VideoCodeSHA256 = strings.Repeat("ab", 32)
	logPath := filepath.Join(a.root, "calls.log")
	script := `#!/usr/bin/env python3
import json,sys
r=json.loads(sys.argv[1][len('msg_cvt'):])
p=r['params']
open(` + "'" + logPath + "'" + `,'a').write(json.dumps(p)+'\n')
res=p
if isinstance(p,dict) and p.get('siid')==10001 and p.get('aiid')==4:
    res={'siid':10001,'aiid':4,'code':0,'out':[{'piid':1102,'value':'{"code":` + string(rune('0'+answer)) + `, "fail":0}'}]}
print(json.dumps({'id':r['id'],'result':res}))
`
	if err := os.WriteFile(a.avacmd, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	monitor = monitorState{}
	t.Cleanup(func() {
		monitor.Lock()
		if monitor.stop != nil {
			close(monitor.stop)
		}
		monitor = monitorState{}
	})
	return a, func() []string {
		b, _ := os.ReadFile(logPath)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

func TestRemoteMoveOpensMonitoringFirst(t *testing.T) {
	a, calls := monitorAgent(t, 2)
	for i := 0; i < 2; i++ {
		if _, err := a.call(context.Background(), request{Method: "local.command", Params: json.RawMessage(`{"name":"remote_move","velocity":200,"rotation":0}`)}); err != nil {
			t.Fatal(err)
		}
	}
	got := calls()
	if len(got) != 4 || !strings.Contains(got[0], `\"oldcode\":\"abab`) || !strings.Contains(got[0], `"piid": 1102`) ||
		!strings.Contains(got[1], `\"operType\":\"monitor\"`) || !strings.Contains(got[1], `\"operation\":\"start\"`) ||
		!strings.Contains(got[2], `"piid": 15`) || !strings.Contains(got[3], `"piid": 15`) || !strings.Contains(got[3], `timestamp`) {
		t.Fatal(strings.Join(got, "\n"))
	}
}

func TestRemoteMoveWithoutCodeRefused(t *testing.T) {
	a, _ := monitorAgent(t, 2)
	a.cfg.VideoCodeSHA256 = ""
	if _, err := a.call(context.Background(), request{Method: "local.command", Params: json.RawMessage(`{"name":"remote_move","velocity":200,"rotation":0}`)}); err != errNoVideoCode {
		t.Fatal(err)
	}
}

func TestWrongVideoCodeNotRetried(t *testing.T) {
	a, calls := monitorAgent(t, 1)
	for i := 0; i < 2; i++ {
		if _, err := a.call(context.Background(), request{Method: "local.command", Params: json.RawMessage(`{"name":"remote_move","velocity":0,"rotation":45}`)}); err == nil {
			t.Fatal("wrong code accepted")
		}
	}
	if got := calls(); len(got) != 1 { // the firmware freezes the code after 5 wrong tries
		t.Fatal(got)
	}
}

func TestVerifyCode(t *testing.T) {
	for raw, want := range map[string]int{
		`{"siid":10001,"aiid":4,"out":[{"piid":1102,"value":"{\"code\":2, \"fail\":0}"}],"code":0}`: 2,
		`{"siid":10001,"aiid":4,"out":[{"piid":1102,"value":"{\"code\":3}"}],"code":0}`:             3,
		`{"code":0}`: -1,
	} {
		if got := verifyCode(json.RawMessage(raw)); got != want {
			t.Error(raw, got)
		}
	}
}

func TestVideoCodeRoute(t *testing.T) {
	a := testAgent(t)
	a.configPath = filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(a.configPath, []byte(`{"token":"`+a.cfg.Token+`","custom":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	control, err := a.createToken("ha", "control")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	do := func(token, body string) int {
		method := "GET"
		if body != "" {
			method = "POST"
		}
		req, _ := http.NewRequest(method, server.URL+"/api/v1/system/video_code", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if do(control["token"], `{"code":"1234"}`) != 403 || do(a.cfg.Token, `{"code":"12"}`) != 400 || do(a.cfg.Token, `{"code":"1234"}`) != 200 {
		t.Fatal("route")
	}
	saved, _ := os.ReadFile(a.configPath)
	sum := sha256.Sum256([]byte("1234"))
	if strings.Contains(string(saved), `"1234"`) || !strings.Contains(string(saved), hex.EncodeToString(sum[:])) || !strings.Contains(string(saved), `"custom": 1`) || a.videoCodeHash() != hex.EncodeToString(sum[:]) {
		t.Fatal(string(saved))
	}
	if do(a.cfg.Token, `{"code":""}`) != 200 || a.videoCodeHash() != "" {
		t.Fatal("not cleared")
	}
}

func TestVideoMonitorOpenAndEnd(t *testing.T) {
	a, calls := monitorAgent(t, 2)
	run := func(body string) {
		if _, err := a.call(context.Background(), request{Method: "local.command", Params: json.RawMessage(body)}); err != nil {
			t.Fatal(body, err)
		}
	}
	run(`{"name":"video_monitor","enabled":true}`)
	run(`{"name":"video_monitor","enabled":true}`) // heartbeat: no second start
	run(`{"name":"video_monitor","enabled":false}`)
	got := calls()
	if len(got) != 3 || !strings.Contains(got[1], `\"operation\":\"start\"`) || !strings.Contains(got[2], `\"operation\":\"end\"`) || monitor.open {
		t.Fatal(strings.Join(got, "\n"))
	}
}

func TestFindPetInsideMonitoring(t *testing.T) {
	a, calls := monitorAgent(t, 2)
	if _, err := a.call(context.Background(), request{Method: "local.command", Params: json.RawMessage(`{"name":"find_pet"}`)}); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 3 || !strings.Contains(got[2], `"value": 22`) || !strings.Contains(got[2], `findpet`) {
		t.Fatal(strings.Join(got, "\n"))
	}
	monitor.Lock()
	held := monitor.holds
	monitor.Unlock()
	if held != 1 {
		t.Fatal("pet search does not hold the session", held)
	}
}

func TestCameraStream(t *testing.T) {
	a := testAgent(t)
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	get := func() *http.Response {
		req, _ := http.NewRequest("GET", server.URL+"/api/v1/camera/stream", nil)
		req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := get(); res.StatusCode != 404 {
		t.Fatal("stream without the camera module", res.StatusCode)
	}
	mod := a.path("/data/dreame-local/modules/camera")
	if err := os.MkdirAll(mod, 0700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(mod, "manifest.json"), []byte(`{"name":"camera","version":"1.6.0","title":"Камера","description":"test","handler":"handler"}`), 0600)
	_ = os.WriteFile(filepath.Join(mod, "handler"), []byte("#!/bin/sh\n"), 0700)
	if _, err := a.installedModule("camera"); err != nil {
		t.Fatal(err)
	}
	dir := a.path(cameraLiveDir)
	_ = os.MkdirAll(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, "live.jpg"), []byte("\xff\xd8frame\xff\xd9"), 0600)
	res := get()
	defer res.Body.Close()
	buf := make([]byte, 200)
	n, _ := io.ReadAtLeast(res.Body, buf, 60)
	if !strings.Contains(string(buf[:n]), "Content-Type: image/jpeg") || !strings.Contains(string(buf[:n]), "frame") {
		t.Fatal(string(buf[:n]))
	}
	if _, err := os.Stat(filepath.Join(dir, "want")); err != nil {
		t.Fatal("stream does not ask the module to capture")
	}
}

func TestVideoCloudBlockDuringSession(t *testing.T) {
	a, calls := monitorAgent(t, 2)
	a.cfg.VideoCloudBlock = true
	rules := filepath.Join(a.root, "iptables.log")
	fake := filepath.Join(a.root, "iptables")
	// -D and -X fail when there is nothing to delete, as the real one does.
	script := "#!/bin/sh\necho \"$*\" >> " + rules + "\ncase \"$*\" in *' -D '*|*' -X '*|*' -F '*) exit 1;; esac\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a.iptables = []string{fake}
	run := func(body string) {
		if _, err := a.call(context.Background(), request{Method: "local.command", Params: json.RawMessage(body)}); err != nil {
			t.Fatal(body, err)
		}
	}
	run(`{"name":"video_monitor","enabled":true}`)
	b, _ := os.ReadFile(rules)
	log := string(b)
	if !strings.Contains(log, "-t nat -A DREAME_VIDEO -d 192.168.0.0/16 -j RETURN") || !strings.Contains(log, "-t nat -A DREAME_VIDEO -p tcp -j DNAT --to-destination 127.0.0.1:9") || !strings.Contains(log, "-t nat -I OUTPUT -p udp -j DREAME_VIDEO") {
		t.Fatal(log)
	}
	if got := calls(); len(got) != 2 { // the block is in place before monitor start
		t.Fatal(got)
	}
	_ = os.Remove(rules)
	run(`{"name":"video_monitor","enabled":false}`)
	b, _ = os.ReadFile(rules)
	if !strings.Contains(string(b), "-t nat -D OUTPUT -p tcp -j DREAME_VIDEO") || strings.Contains(string(b), " -I ") {
		t.Fatal(string(b))
	}
}

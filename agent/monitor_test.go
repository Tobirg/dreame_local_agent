package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

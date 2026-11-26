package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogs(t *testing.T) {
	a := testAgent(t)
	writeFile(t, a, agentLogFile, strings.Repeat("old line\n", 5000)+"last line\n")
	writeFile(t, a, moduleDataBase+"/mqtt/service.log", "connected\n")
	writeFile(t, a, moduleDataBase+"/camera/light", "") // a module without a log is left out
	logs := a.logs()
	if len(logs) != 2 || logs[0].Source != "agent" || logs[1].Source != "mqtt" || logs[1].Text != "connected\n" {
		t.Fatalf("%+v", logs)
	}
	if text := logs[0].Text; len(text) > logTail || !strings.HasPrefix(text, "old line\n") || !strings.HasSuffix(text, "last line\n") || logs[0].Size != 5000*9+10 {
		t.Fatal("the tail starts at a whole line", len(text), text[:20])
	}

	do := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/logs", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		a.handler(w, r)
		return w
	}
	w := do(a.cfg.Token) // other tokens: TestScopedTokens
	var body struct{ Result []logSource }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Result) != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
}

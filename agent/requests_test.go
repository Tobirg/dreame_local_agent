package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestRepeatedHTTPMutationExecutesOnlyOnce(t *testing.T) {
	a := testAgent(t)
	marker := a.path("/executions")
	script := "#!/usr/bin/env python3\nimport json,sys,time\nr=json.loads(sys.argv[1][7:])\nopen(" + quotePython(marker) + ",'a').write('x')\ntime.sleep(.05)\nprint(json.dumps({'id':r['id'],'result':{'code':0}}))\n"
	_ = os.WriteFile(a.avacmd, []byte(script), 0700)
	const body = `{"method":"action","params":{"siid":7,"aiid":1,"in":[]}}`
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/rpc", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
			req.Header.Set("X-Dreame-Request-ID", "0123456789abcdef0123456789abcdef")
			w := httptest.NewRecorder()
			a.handler(w, req)
			results <- w.Code
		}()
	}
	wg.Wait()
	close(results)
	for status := range results {
		if status != 200 {
			t.Fatal(status)
		}
	}
	executions, _ := os.ReadFile(marker)
	if string(executions) != "x" {
		t.Fatal("native command was repeated", string(executions))
	}
}
func TestRequestIDPayloadConflictAndFailedOutcomeAreRetained(t *testing.T) {
	a := testAgent(t)
	_ = os.WriteFile(a.avacmd, []byte("#!/bin/sh\nexit 1\n"), 0700)
	req := request{Method: "action", Params: json.RawMessage(`{"siid":7,"aiid":1}`)}
	status, first := a.memoCall(context.Background(), req, "0123456789abcdef")
	if status != 502 {
		t.Fatal(status)
	}
	// Replacing the executable cannot make replay execute it a second time.
	_ = os.WriteFile(a.avacmd, []byte("#!/bin/sh\nexit 0\n"), 0700)
	status, again := a.memoCall(context.Background(), req, "0123456789abcdef")
	if status != 502 || string(first) != string(again) {
		t.Fatal(status, string(again))
	}
	req.Params = json.RawMessage(`{"siid":2,"aiid":1}`)
	status, _ = a.memoCall(context.Background(), req, "0123456789abcdef")
	if status != 409 {
		t.Fatal("request ID payload changed", status)
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestMapOperationResultValidation(t *testing.T) {
	for _, tc := range []struct{ name, reply, outcome string }{
		{"accepted", `{"siid":6,"aiid":2,"code":0,"out":[{"piid":6,"value":0}]}`, ""},
		{"future_fields", `{"code":0,"future":true,"out":[{"piid":7,"value":{"future":true}},{"piid":6,"value":0}]}`, ""},
		{"dispatch_rejected", `{"code":-1}`, "rejected"},
		{"operation_rejected", `{"code":0,"out":[{"piid":6,"value":-1}]}`, "rejected"},
		{"unknown_operation_code", `{"code":0,"out":[{"piid":6,"value":12345}]}`, "rejected"},
		{"missing_out", `{"code":0}`, "indeterminate"},
		{"null_out", `{"code":0,"out":null}`, "indeterminate"},
		{"empty_out", `{"code":0,"out":[]}`, "indeterminate"},
		{"wrong_piid", `{"code":0,"out":[{"piid":7,"value":0}]}`, "indeterminate"},
		{"missing_value", `{"code":0,"out":[{"piid":6}]}`, "indeterminate"},
		{"string_value", `{"code":0,"out":[{"piid":6,"value":"0"}]}`, "indeterminate"},
		{"float_value", `{"code":0,"out":[{"piid":6,"value":0.5}]}`, "indeterminate"},
		{"duplicate_result", `{"code":0,"out":[{"piid":6,"value":0},{"piid":6,"value":-1}]}`, "indeterminate"},
		{"wrong_action", `{"siid":6,"aiid":1,"code":0,"out":[{"piid":6,"value":0}]}`, "indeterminate"},
		{"missing_code", `{"out":[{"piid":6,"value":0}]}`, "indeterminate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMapReply(json.RawMessage(tc.reply))
			if tc.outcome == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted an unsuccessful or unknown map outcome")
			}
			body := rpcErrorResponse(err)
			if body["outcome"] != tc.outcome || string(body["result"].(json.RawMessage)) != tc.reply {
				t.Fatal("lost outcome or original firmware result", body)
			}
		})
	}
}

func TestMapFailureHTTPPreservesResultAndMemoDoesNotRepeat(t *testing.T) {
	for _, memo := range []bool{false, true} {
		a := testAgent(t)
		marker := a.path("/map-executions")
		script := "#!/usr/bin/env python3\nimport json,sys\nr=json.loads(sys.argv[1][7:])\nopen(" + quotePython(marker) + ",'a').write('x')\nprint(json.dumps({'id':r['id'],'result':{'siid':6,'aiid':2,'code':0,'out':[{'piid':6,'value':-7}]}}))\n"
		if err := os.WriteFile(a.avacmd, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		attempts := 1
		if memo {
			attempts = 2
		}
		for i := 0; i < attempts; i++ {
			r := httptest.NewRequest("POST", "/rpc", strings.NewReader(`{"method":"action","params":{"siid":6,"aiid":2,"in":[]}}`))
			r.Header.Set("Authorization", "Bearer "+a.cfg.Token)
			if memo {
				r.Header.Set("X-Dreame-Request-ID", "map-operation-0123456789")
			}
			w := httptest.NewRecorder()
			a.handler(w, r)
			var body struct {
				Outcome string          `json:"outcome"`
				Result  json.RawMessage `json:"result"`
			}
			if w.Code != 502 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Outcome != "rejected" || !strings.Contains(string(body.Result), "-7") {
				t.Fatal(w.Code, w.Body.String())
			}
		}
		data, err := os.ReadFile(marker)
		if err != nil || string(data) != "x" {
			t.Fatal("map action repeated", string(data), err)
		}
	}
}

func TestMapRequestUsesActionControlPolicyAndMemo(t *testing.T) {
	params := json.RawMessage(`{"siid":6,"aiid":1,"in":[]}`)
	request := request{Method: "action", Params: params}
	if requestIsRead(request) {
		t.Fatal("map request classified as read")
	}
	for _, pin := range []bool{false, true} {
		a := testAgent(t)
		if pin {
			a.cfg.ExpectedMAC = ""
		} else {
			a.cfg.AllowControl = false
		}
		a.avacmd = a.path("/must-not-be-started")
		_, err := a.call(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "control requires") {
			t.Fatal(err)
		}
	}
	a := testAgent(t)
	marker := a.path("/request-executions")
	script := "#!/usr/bin/env python3\nimport json,sys\nr=json.loads(sys.argv[1][7:])\nopen(" + quotePython(marker) + ",'a').write('x')\nprint(json.dumps({'id':r['id'],'result':{'siid':6,'aiid':1,'code':0,'out':[]}}))\n"
	if err := os.WriteFile(a.avacmd, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		status, _ := a.memoCall(context.Background(), request, "map-request-0123456789")
		if status != 200 {
			t.Fatal(status)
		}
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "x" {
		t.Fatal("map request repeated", string(data), err)
	}
}

func TestThresholdFailureUsesMapOperationResult(t *testing.T) {
	a := testAgent(t)
	view := thresholdFixture(t, a)
	params, _ := json.Marshal(map[string]any{"map_id": view.MapID, "revision": view.Revision, "passable": [][]int{}, "impassable": [][]int{}})
	script := "#!/usr/bin/env python3\nimport json,sys\nr=json.loads(sys.argv[1][7:])\nprint(json.dumps({'id':r['id'],'result':{'siid':6,'aiid':2,'code':0,'out':[{'piid':6,'value':-9}]}}))\n"
	if err := os.WriteFile(a.avacmd, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := a.call(context.Background(), request{Method: "local.thresholds", Params: params})
	if err == nil || rpcErrorResponse(err)["outcome"] != "rejected" {
		t.Fatal("threshold rejection was reported as success", err)
	}
}

package main

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestSettingsCatalogSources(t *testing.T) {
	defs, err := settingsCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defs {
		_, official := officialProps[[2]int{d.SIID, d.PIID}]
		if d.Src != "plugin" && d.Src != "tasshack" || d.Src == "plugin" && !official {
			t.Fatalf("%s: src %q, in app plugin: %v", d.ID, d.Src, official)
		}
	}
}

// The firmware answers 28/3 on r2505 although its hardware table says the LDS does not lift.
func TestHardwareTableHidesSettings(t *testing.T) {
	script := `#!/usr/bin/env python3
import json,sys
r=json.loads(sys.argv[1][len('msg_cvt'):])
values={'28/3':0,'4/27':0}
res=[dict(p,code=0,value=values[f"{p['siid']}/{p['piid']}"]) if f"{p['siid']}/{p['piid']}" in values else dict(p,code=-4001) for p in r['params']]
print(json.dumps({'id':r['id'],'result':res}))
`
	for _, tc := range []struct {
		lift string
		want bool
	}{{"False_High", false}, {"True", true}} {
		a := testAgent(t)
		if err := os.WriteFile(a.avacmd, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, a, "/ava/conf/dconf_runtime.json", `{"dreame.vacuum.r2505":{"RUNTIME_LIFT_LDS":"`+tc.lift+`"}}`)
		got, err := a.readSettings(context.Background(), "12345678")
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, s := range got {
			ids[s.ID] = true
		}
		if ids["auto_lds_coverage"] != tc.want || !ids["child_lock"] {
			t.Fatal(tc.lift, ids)
		}
		_, err = a.setSetting(context.Background(), "auto_lds_coverage", float64(1), "12345678")
		if (err == nil) != tc.want {
			t.Fatal(tc.lift, "set:", err)
		}
	}
}

func TestOfficialErrorTextAndCapability(t *testing.T) {
	state, err := normalize(json.RawMessage(`[{"siid":2,"piid":2,"value":101,"code":0},{"siid":4,"piid":83,"value":19,"code":0}]`))
	if err != nil {
		t.Fatal(err)
	}
	if state["error_name"] != "bin_full" || state["error_text"] == nil || state["error_text_en"] == "" {
		t.Fatal(state)
	}
	if got := deviceCapabilities(state["device_capability"]); !reflect.DeepEqual(got, []string{"video_monitor", "quick_command", "record_sound"}) {
		t.Fatal(got)
	}
	if deviceCapabilities(nil) != nil {
		t.Fatal("unreported mask must stay unknown")
	}
}

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type tarEntry struct {
	name, body string
	kind       byte
}

func voicePack(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: 0644, Size: int64(len(e.body)), Typeflag: kind}
		if kind == tar.TypeSymlink {
			h.Size, h.Linkname = 0, "/etc/passwd"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(e.body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return b.Bytes()
}

// voiceAgent has built-in EN, the ZH voice in use and an avacmd that records its arguments and,
// like the player, writes language_in_use on AVA_MEDIA_DL_FINISHED.
func voiceAgent(t *testing.T) (*agent, string) {
	a := testAgent(t)
	writeFile(t, a, "/audio/EN/45.ogg", "OggS-en45")
	writeFile(t, a, "/audio/ZH/45.ogg", "OggS-zh45")
	writeFile(t, a, "/audio/SYS/1.ogg", "system sound")
	writeFile(t, a, "/audio/EN/tts.json", `{"tts":"builtin"}`)
	writeFile(t, a, voiceInUse, "ZH\x00") // as the firmware writes it
	log := filepath.Join(a.root, "avacmd.log")
	script := "#!/usr/bin/env python3\nimport json,sys\nopen(" + `'` + log + `'` + ",'a').write(' '.join(sys.argv[1:])+'\\n')\n" +
		"if sys.argv[1]=='media':\n m=json.loads(sys.argv[2])\n if m.get('cmd')=='AVA_MEDIA_DL_FINISHED': open(" + `'` + a.path(voiceInUse) + `'` + ",'w').write(m['language_id']+chr(0))\n"
	if err := os.WriteFile(a.avacmd, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return a, log
}

func TestVoiceInstallUseRemove(t *testing.T) {
	a, log := voiceAgent(t)
	v := a.voices()
	if v["current"] != "ZH" || !reflect.DeepEqual(v["voices"], []voice{{ID: "EN", Builtin: true, Files: 1, Bytes: 9, TTS: true}, {ID: "ZH", Builtin: true, Files: 1, Bytes: 9}}) {
		t.Fatalf("%+v", v)
	}
	pack := voicePack(t, tarEntry{name: "./", kind: tar.TypeDir}, tarEntry{name: "./0.ogg", body: "OggS-ru0"}, tarEntry{name: "45.ogg", body: "OggS-ru45"},
		tarEntry{name: "tts.json", body: `{"tts":1}`}, tarEntry{name: "../../etc/7.ogg", body: "x"}, tarEntry{name: "readme.txt", body: "x"},
		tarEntry{name: "link.ogg", kind: tar.TypeSymlink})
	for id, want := range map[string]string{"EN": "built-in", "ru": "capital", "SYS": "built-in"} {
		if _, err := a.installVoice(id, pack); err == nil || (id != "SYS" && !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v", id, err)
		}
	}
	if _, err := a.installVoice("RU", voicePack(t, tarEntry{name: "readme.txt", body: "x"})); err == nil {
		t.Fatal("a pack without phrases must be refused")
	}
	if _, err := a.installVoice("RU", []byte("not gzip")); err == nil {
		t.Fatal("not an archive")
	}
	res, err := a.installVoice("RU", pack)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res["skipped"], []string{"../../etc/7.ogg", "readme.txt", "link.ogg"}) {
		t.Fatal("skipped", res["skipped"])
	}
	if _, err := os.Stat(a.path("/etc/7.ogg")); err == nil {
		t.Fatal("path traversal")
	}
	if numbers, _, tts := phrases(a.path(voiceStore + "/RU")); !reflect.DeepEqual(numbers, []int{0, 45}) || !tts {
		t.Fatal(numbers, tts)
	}

	if _, err := a.useVoice(t.Context(), "RU"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(a.path(voicePlayer + "/RU/45.ogg")); string(b) != "OggS-ru45" {
		t.Fatal("the player folder", string(b))
	}
	if b, _ := os.ReadFile(a.path(voiceTTS)); string(b) != `{"tts":1}` {
		t.Fatal("tts.json", string(b))
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), `clb {"cmd":"set_tts_path","path":"/data/zt_conmon_file/tts.json","type":"system"}`) ||
		!strings.Contains(string(calls), `media {"cmd":"AVA_MEDIA_DL_FINISHED","language_id":"RU","result":0,"type":"media"}`) {
		t.Fatal(string(calls))
	}
	if _, err := a.removeVoice("RU"); err == nil {
		t.Fatal("the voice in use must not be removed")
	}
	// A built-in voice is switched to without copying; the uploaded one can then go.
	if v, err := a.useVoice(t.Context(), "EN"); err != nil || v["current"] != "EN" {
		t.Fatal(v, err)
	}
	if b, _ := os.ReadFile(a.path(voiceTTS)); string(b) != `{"tts":"builtin"}` {
		t.Fatal("the built-in voice's tts.json comes back", string(b))
	}
	if _, err := a.removeVoice("EN"); err == nil {
		t.Fatal("built-in voices stay")
	}
	if _, err := a.removeVoice("RU"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.path(voicePlayer + "/RU")); err == nil {
		t.Fatal("the player's copy must go with the voice")
	}
	if _, err := a.useVoice(t.Context(), "RU"); err == nil {
		t.Fatal("removed voice used")
	}
}

func TestVoiceRoutes(t *testing.T) {
	a, log := voiceAgent(t)
	do := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		a.handler(w, r)
		return w
	}
	if w := do("GET", "/api/v1/voice/phrase?id=EN&n=45", a.cfg.Token, ""); w.Code != 200 || w.Body.String() != "OggS-en45" || w.Header().Get("Content-Type") != "audio/ogg" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := do("GET", "/api/v1/voice/phrases?id=ZH", a.cfg.Token, ""); w.Body.String() != `{"result":[45]}`+"\n" {
		t.Fatal(w.Body.String())
	}
	if w := do("GET", "/api/v1/voice/phrase?id=../etc&n=1", a.cfg.Token, ""); w.Code != 400 {
		t.Fatal("bad voice code", w.Code)
	}
	if w := do("POST", "/api/v1/voice/play", a.cfg.Token, `{"number":45}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if calls, _ := os.ReadFile(log); !strings.Contains(string(calls), `media {"cmd":"play","file_number":45,"type":"media"}`) {
		t.Fatal(string(calls))
	}
	if w := do("POST", "/api/v1/voice/install?id=RU", a.cfg.Token, string(voicePack(t, tarEntry{name: "1.ogg", body: "OggS"}))); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a.cfg.AllowControl = false
	if w := do("POST", "/api/v1/voice/use", a.cfg.Token, `{"id":"RU"}`); w.Code != 403 {
		t.Fatal("writes need allow_control", w.Code)
	}
}

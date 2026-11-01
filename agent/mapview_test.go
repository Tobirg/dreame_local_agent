package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func syntheticFrame(t *testing.T, id uint16) string {
	t.Helper()
	raw := make([]byte, 27)
	binary.LittleEndian.PutUint16(raw, id)
	raw[4] = 'I'
	binary.LittleEndian.PutUint16(raw[17:19], 50)
	binary.LittleEndian.PutUint16(raw[19:21], 3)
	binary.LittleEndian.PutUint16(raw[21:23], 3)
	raw = append(raw, 1, 1, 255, 1, 2, 2, 0, 2, 2)
	raw = append(raw, []byte(`{"origin":[-100,-200],"seg_inf":{"1":{"name":"Test room"},"2":{}}}`)...)
	var packed bytes.Buffer
	w := zlib.NewWriter(&packed)
	_, _ = w.Write(raw)
	_ = w.Close()
	return base64.StdEncoding.EncodeToString(packed.Bytes())
}
func TestIndependentSavedMapDecoder(t *testing.T) {
	raw, e := decodeSavedFrame(syntheticFrame(t, 2))
	if e != nil {
		t.Fatal(e)
	}
	v, e := makeMapView(raw, "dreame.vacuum.r2505")
	if e != nil {
		t.Fatal(e)
	}
	if v.MapID != 2 || v.Width != 3 || len(v.Rooms) != 2 || v.Pixels[2] != 255 || v.Live || v.Origin[0] != -100 {
		t.Fatalf("bad map view: %+v", v)
	}
	if v.Rooms[0].Name != "Test room" || v.Revision == "" {
		t.Fatal(v.Rooms)
	}
}
func TestMapHeaderIDRatherThanSlot(t *testing.T) {
	a := testAgent(t)
	maps := map[string]any{"curr_id": 2, "mapstr": []any{map[string]any{"id": 0, "map": syntheticFrame(t, 2)}}}
	data, _ := json.Marshal(maps)
	_ = os.WriteFile(a.path("/data/log/map_info.bin"), data, 0600)
	v, e := a.call(context.Background(), request{Method: "local.map_view"})
	if e != nil || v.(mapView).MapID != 2 {
		t.Fatal(v, e)
	}
}
func TestEmptyMapView(t *testing.T) {
	a := testAgent(t)
	v, e := a.mapView()
	if e != nil || !v.Empty {
		t.Fatal(v, e)
	}
}
func TestBadMapDimensionsRejected(t *testing.T) {
	raw, e := decodeSavedFrame(syntheticFrame(t, 2))
	if e != nil {
		t.Fatal(e)
	}
	raw[19] = 255
	raw[20] = 255
	if _, e := makeMapView(raw, "dreame.vacuum.r2505"); e == nil {
		t.Fatal("invalid grid accepted")
	}
}
func TestBadMapEncodingRejected(t *testing.T) {
	if _, e := decodeSavedFrame("not a frame"); e == nil {
		t.Fatal("invalid base64 accepted")
	}
}
func TestAssetsHaveNoEmbeddedToken(t *testing.T) {
	a := testAgent(t)
	for _, path := range []string{"/", "/app.js", "/style.css", "/settings.js"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		a.handler(w, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), a.cfg.Token) || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal(path, w.Code)
		}
	}
}
func TestStaticAssetsDoNotExposeRPC(t *testing.T) {
	a := testAgent(t)
	r := httptest.NewRequest("GET", "/rpc", nil)
	w := httptest.NewRecorder()
	a.handler(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
func TestMapViewRequiresAuth(t *testing.T) {
	a := testAgent(t)
	r := httptest.NewRequest("POST", "/rpc", strings.NewReader(`{"method":"local.map_view"}`))
	w := httptest.NewRecorder()
	a.handler(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestRealMapWhenProvided(t *testing.T) {
	path := os.Getenv("DREAME_MAP_TEST_FILE")
	if path == "" {
		t.Skip("no private robot map supplied")
	}
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	a := testAgent(t)
	if e := os.WriteFile(a.path("/data/log/map_info.bin"), data, 0600); e != nil {
		t.Fatal(e)
	}
	view, e := a.mapView()
	if e != nil {
		t.Fatal(e)
	}
	if view.MapID != 2 || view.Width != 130 || view.Height != 209 || len(view.Rooms) != 5 || !view.ThresholdsEditable || len(view.Impassable) != 1 {
		t.Fatal("private robot map decode mismatch")
	}
	for i, room := range view.Rooms {
		if room.ID != i+1 {
			t.Fatal("room IDs mismatch")
		}
	}
}

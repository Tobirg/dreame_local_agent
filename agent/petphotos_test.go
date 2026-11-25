package main

import (
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

func TestPetPhotos(t *testing.T) {
	a := testAgent(t)
	fw := a.path(firmwarePetDir)
	if err := os.MkdirAll(fw, 0700); err != nil {
		t.Fatal(err)
	}
	old, fresh := time.Now().Add(-time.Hour), time.Now()
	write := func(name, body string, at time.Time) {
		p := filepath.Join(fw, name)
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	write("cat 1.gif", "GIF89a-one", old)
	write("2.GIF", "GIF89a-two", old.Add(time.Minute))
	write("writing.gif", "GIF8", fresh) // may still be being written
	write("notes.txt", "x", old)
	write("empty.gif", "", old)
	a.collectPetPhotos(time.Now())

	// A run with a pet among its obstacles; its photo is listed too, the shoe is not.
	start := old.Add(-30 * time.Minute).UTC()
	end := start.Add(10 * time.Minute)
	if err := os.MkdirAll(a.path(runPhotoDir), 0700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"100-0.jpg", "100-1.jpg"} {
		if err := os.WriteFile(filepath.Join(a.path(runPhotoDir), n), []byte("JPEG"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.appendHistory(cleanRun{Start: start, End: &end, Obstacles: [][]int{{1, 2, 133, 90}, {3, 4, petObstacleType, 81}}, Photos: []string{"100-0.jpg", "100-1.jpg"}}); err != nil {
		t.Fatal(err)
	}

	list := a.petPhotos()
	names := []string{}
	for _, p := range list {
		names = append(names, p.Source+":"+p.Name)
	}
	want := []string{
		"gif:pet-" + itoa(int(old.Add(time.Minute).Unix())) + "-2.gif",
		"gif:pet-" + itoa(int(old.Unix())) + "-cat_1.gif",
		"obstacle:100-1.jpg",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") || list[2].Confidence != 81 || list[2].URL != "/api/v1/history/photo?name=100-1.jpg" {
		t.Fatalf("%v %+v", names, list)
	}
	if info, err := os.Stat(filepath.Join(a.path(petPhotoDir), list[0].Name)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("kept copy must be private", err)
	}

	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	do := func(method, path, body string) (int, string, string) {
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Content-Type"), string(b)
	}
	if code, ctype, body := do("GET", list[1].URL, ""); code != 200 || ctype != "image/gif" || body != "GIF89a-one" {
		t.Fatal(code, ctype, body)
	}
	for _, bad := range []string{"../history.json", "pet-1-..%2Fx.gif", "100-1.jpg"} {
		if code, _, _ := do("GET", "/api/v1/pets/photo?name="+bad, ""); code != 400 {
			t.Fatal(bad, code)
		}
	}
	req, _ := http.NewRequest("GET", server.URL+list[0].URL, nil)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 401 {
		t.Fatal("photo served without a token")
	}
	var v struct {
		Result []petPhoto `json:"result"`
	}
	if code, _, body := do("GET", "/api/v1/pets/photos", ""); code != 200 || json.Unmarshal([]byte(body), &v) != nil || len(v.Result) != 3 {
		t.Fatal(code, body)
	}

	// Deleting a GIF keeps it from being copied again while the firmware still has it.
	if code, _, body := do("POST", "/api/v1/pets/delete", `{"name":"`+list[1].Name+`"}`); code != 200 {
		t.Fatal(code, body)
	}
	a.collectPetPhotos(time.Now().Add(time.Minute))
	// Deleting the pet photo of a run keeps the obstacle and drops only the photo.
	if code, _, body := do("POST", "/api/v1/pets/delete", `{"name":"100-1.jpg"}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, _, _ := do("POST", "/api/v1/pets/delete", `{"name":"100-0.jpg"}`); code != 404 {
		t.Fatal("a photo that is not a pet must stay")
	}
	list = a.petPhotos()
	if len(list) != 2 || list[0].Name != "pet-"+itoa(int(fresh.Unix()))+"-writing.gif" || list[1].Name != want[0][4:] {
		t.Fatalf("%+v", list) // the settled file came in; the deleted one did not come back
	}
	runs := a.readHistory()
	if len(runs[0].Obstacles) != 2 || runs[0].Photos[1] != "" || runs[0].Photos[0] != "100-0.jpg" {
		t.Fatalf("%+v", runs[0])
	}
	if _, err := os.Stat(filepath.Join(a.path(runPhotoDir), "100-1.jpg")); !os.IsNotExist(err) {
		t.Fatal("run photo file must be removed")
	}
}

func TestPetPhotosTrim(t *testing.T) {
	a := testAgent(t)
	fw := a.path(firmwarePetDir)
	_ = os.MkdirAll(fw, 0700)
	base := time.Now().Add(-2 * time.Hour)
	for i := 0; i < maxPetPhotos+5; i++ {
		p := filepath.Join(fw, itoa(i)+".gif")
		_ = os.WriteFile(p, []byte("GIF"), 0644)
		at := base.Add(time.Duration(i) * time.Second)
		_ = os.Chtimes(p, at, at)
	}
	a.collectPetPhotos(time.Now())
	a.collectPetPhotos(time.Now()) // trimmed ones are not copied again
	kept := a.keptPetPhotos()
	if len(kept) != maxPetPhotos || kept[len(kept)-1].Name != "pet-"+itoa(int(base.Add(5*time.Second).Unix()))+"-5.gif" {
		t.Fatal(len(kept), kept[len(kept)-1].Name)
	}
}

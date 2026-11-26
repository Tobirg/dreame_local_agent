package main

// Voice packs. Checked on r2505 4.3.9_2021: the firmware's player (node_media) plays phrase N as
// /audio/<language>/N.ogg, and for a language without a built-in folder there (EN, JA, KO, ZH)
// as /data/personalized_voice/<language>/N.ogg. The language in use is set by the message its own
// download script sends when a pack is unpacked (AVA_MEDIA_DL_FINISHED) and kept in
// /data/config/ava/language_in_use. The cloud path (set_voice with a URL) is not used: for a
// language without a built-in folder it unpacks under /data/audio and has nothing to mount onto.
// The agent keeps uploaded packs in its own folder and copies the one in use for the player.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	voiceStore     = "/data/dreame-local/voices"
	voicePlayer    = "/data/personalized_voice"
	voiceBuiltin   = "/audio"
	voiceInUse     = "/data/config/ava/language_in_use"
	voiceTTS       = "/data/zt_conmon_file/tts.json" // where the firmware's download script puts a pack's tts.json
	maxVoicePack   = 64 << 20
	maxVoiceFile   = 8 << 20
	maxVoiceFiles  = 2000
	maxVoiceUnpack = 256 << 20
)

var (
	voiceIDRe   = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,7}$`)
	voiceFileRe = regexp.MustCompile(`^([0-9]{1,4})\.ogg$`)
	// Folders of /audio that hold system sounds, not a voice.
	voiceSystemDirs = map[string]bool{"CAL": true, "SYS": true, "TEST": true}
)

type voice struct {
	ID      string `json:"id"`
	Builtin bool   `json:"builtin"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes,omitempty"`
	TTS     bool   `json:"tts,omitempty"`
}

// phrases lists a voice folder's phrase numbers and whether it has a tts.json.
func phrases(dir string) (numbers []int, size int64, tts bool) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == "tts.json" {
			tts = true
		}
		if m := voiceFileRe.FindStringSubmatch(e.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
			numbers = append(numbers, n)
			if info, err := e.Info(); err == nil {
				size += info.Size()
			}
		}
	}
	sort.Ints(numbers)
	return numbers, size, tts
}

// voiceDir is the folder the player or the agent's store has for a voice.
func (a *agent) voiceDir(id string) (string, bool) {
	if !voiceIDRe.MatchString(id) || voiceSystemDirs[id] {
		return "", false
	}
	if n, _, _ := phrases(a.path(path.Join(voiceBuiltin, id))); len(n) > 0 {
		return a.path(path.Join(voiceBuiltin, id)), true
	}
	return a.path(path.Join(voiceStore, id)), false
}

func (a *agent) voices() map[string]any {
	list := []voice{}
	for _, base := range []string{voiceBuiltin, voiceStore} {
		entries, _ := os.ReadDir(a.path(base))
		for _, e := range entries {
			if !e.IsDir() || !voiceIDRe.MatchString(e.Name()) || (base == voiceBuiltin && voiceSystemDirs[e.Name()]) {
				continue
			}
			n, size, tts := phrases(a.path(path.Join(base, e.Name())))
			if len(n) > 0 {
				list = append(list, voice{ID: e.Name(), Builtin: base == voiceBuiltin, Files: len(n), Bytes: size, TTS: tts})
			}
		}
	}
	current := ""
	if b, err := readLimited(a.path(voiceInUse), 64); err == nil {
		current = strings.Trim(string(b), " \t\r\n\x00") // the firmware writes "ZH\x00"
	}
	return map[string]any{"current": current, "voices": list}
}

// installVoice unpacks a .tar.gz of N.ogg files (and an optional tts.json) into the store.
func (a *agent) installVoice(id string, pack []byte) (map[string]any, error) {
	if !voiceIDRe.MatchString(id) {
		return nil, errors.New("voice code must be 2-8 capital letters or digits, starting with a letter (RU, UK, RU2)")
	}
	if dir, builtin := a.voiceDir(id); builtin || dir == "" { // "" : a folder of system sounds (SYS, CAL, TEST)
		return nil, fmt.Errorf("%s is a built-in voice of the firmware; upload the pack under another code, for example %s2", id, id)
	}
	gz, err := gzip.NewReader(bytes.NewReader(pack))
	if err != nil {
		return nil, errors.New("not a .tar.gz archive")
	}
	if err := os.MkdirAll(a.path(voiceStore), 0700); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(a.path(voiceStore), ".upload-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	tr := tar.NewReader(gz)
	files, total, skipped := 0, int64(0), []string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("damaged archive")
		}
		name := strings.TrimPrefix(path.Clean("/"+h.Name), "/") // packs are flat; "./12.ogg" is fine
		if h.Typeflag == tar.TypeDir || name == "" {
			continue
		}
		if h.Typeflag != tar.TypeReg || strings.Contains(name, "/") || (name != "tts.json" && !voiceFileRe.MatchString(name)) {
			if len(skipped) < 5 {
				skipped = append(skipped, h.Name)
			}
			continue
		}
		if h.Size > maxVoiceFile || files >= maxVoiceFiles || total+h.Size > maxVoiceUnpack {
			return nil, errors.New("pack too large: at most 2000 files of 8 MB each")
		}
		f, err := os.OpenFile(filepath.Join(tmp, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return nil, errors.New("duplicate file " + name)
		}
		n, err := io.Copy(f, io.LimitReader(tr, maxVoiceFile+1))
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil || n > maxVoiceFile {
			return nil, errors.New("cannot unpack " + name)
		}
		files++
		total += n
	}
	if numbers, _, _ := phrases(tmp); len(numbers) == 0 {
		return nil, errors.New("the archive has no N.ogg phrase files")
	}
	final := a.path(path.Join(voiceStore, id))
	old := final + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(final, old); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Rename(old, final)
		return nil, err
	}
	_ = os.RemoveAll(old)
	if err := os.Chmod(final, 0755); err != nil { // the player reads it
		return nil, err
	}
	result := a.voices()
	result["installed"], result["skipped"] = id, skipped
	return result, nil
}

// useVoice makes the player speak with a voice: a built-in one directly, a stored one copied
// to the folder the player reads for languages without a built-in folder.
func (a *agent) useVoice(ctx context.Context, id string) (map[string]any, error) {
	dir, builtin := a.voiceDir(id)
	if dir == "" {
		return nil, errors.New("invalid voice code")
	}
	if n, _, _ := phrases(dir); len(n) == 0 {
		return nil, errors.New("no such voice: " + id)
	}
	if !builtin {
		// The firmware keeps one personal voice at a time (its download script empties the folder).
		target := a.path(voicePlayer)
		tmp := target + ".new"
		_ = os.RemoveAll(tmp)
		if err := copyFlat(dir, filepath.Join(tmp, id)); err != nil {
			_ = os.RemoveAll(tmp)
			return nil, err
		}
		_ = os.RemoveAll(target)
		if err := os.Rename(tmp, target); err != nil {
			return nil, err
		}
	}
	// Voice assistant answers, as the firmware's download script installs them; a built-in voice
	// brings its own back after an uploaded one.
	if b, err := os.ReadFile(filepath.Join(dir, "tts.json")); err == nil {
		if old, _ := os.ReadFile(a.path(voiceTTS)); !bytes.Equal(old, b) {
			if err := copyFile(filepath.Join(dir, "tts.json"), a.path(voiceTTS)); err != nil {
				return nil, err
			}
			msg, _ := json.Marshal(map[string]string{"type": "system", "cmd": "set_tts_path", "path": voiceTTS})
			if _, err := a.runAvacmd(ctx, "clb", string(msg)); err != nil {
				return nil, err
			}
		}
	}
	msg, _ := json.Marshal(map[string]any{"type": "media", "cmd": "AVA_MEDIA_DL_FINISHED", "result": 0, "language_id": id})
	if _, err := a.runAvacmd(ctx, "media", string(msg)); err != nil {
		return nil, err
	}
	// The player writes language_in_use itself; wait briefly so the reply shows the new voice.
	for i := 0; i < 10; i++ {
		if v := a.voices(); v["current"] == id {
			return v, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, errors.New("the robot did not switch to " + id)
}

func (a *agent) removeVoice(id string) (map[string]any, error) {
	dir, builtin := a.voiceDir(id)
	if dir == "" || builtin {
		return nil, errors.New("only uploaded voices can be removed")
	}
	if a.voices()["current"] == id {
		return nil, errors.New("this voice is in use: switch to another one first")
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	_ = os.RemoveAll(a.path(path.Join(voicePlayer, id))) // the player's copy, left from when it was in use
	return a.voices(), nil
}

func copyFlat(from, to string) error {
	if err := os.MkdirAll(to, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			if err := copyFile(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

// voiceRoutes: GET /api/v1/voice, GET /api/v1/voice/phrases?id= (the numbers), GET /api/v1/voice/phrase?id=&n= (the .ogg, for listening in the
// browser), POST /api/v1/voice/install?id= (the .tar.gz as the body), /use, /remove and /play
// (says phrase n on the robot). Only the main token reaches them (limitedRoute).
func (a *agent) voiceRoutes(w http.ResponseWriter, r *http.Request, send func(int, any)) bool {
	p := r.URL.Path
	if p != "/api/v1/voice" && !strings.HasPrefix(p, "/api/v1/voice/") {
		return false
	}
	reply := func(v any, err error) {
		if err != nil {
			send(502, rpcErrorResponse(err))
			return
		}
		send(200, map[string]any{"result": v})
	}
	switch {
	case p == "/api/v1/voice" && r.Method == "GET":
		send(200, map[string]any{"result": a.voices()})
		return true
	case p == "/api/v1/voice/phrases" && r.Method == "GET":
		dir, _ := a.voiceDir(r.URL.Query().Get("id"))
		numbers, _, _ := phrases(dir)
		if dir == "" || numbers == nil {
			numbers = []int{}
		}
		send(200, map[string]any{"result": numbers})
		return true
	case p == "/api/v1/voice/phrase" && r.Method == "GET":
		dir, _ := a.voiceDir(r.URL.Query().Get("id"))
		n, err := strconv.Atoi(r.URL.Query().Get("n"))
		if dir == "" || err != nil || n < 0 || n > 9999 {
			send(400, map[string]string{"error": "id and n required"})
			return true
		}
		b, err := readLimited(filepath.Join(dir, strconv.Itoa(n)+".ogg"), maxVoiceFile)
		if err != nil {
			send(404, map[string]string{"error": "no such phrase"})
			return true
		}
		w.Header().Set("Content-Type", "audio/ogg")
		_, _ = w.Write(b)
		return true
	case r.Method != "POST" || (p != "/api/v1/voice/install" && p != "/api/v1/voice/use" && p != "/api/v1/voice/remove" && p != "/api/v1/voice/play"):
		send(404, map[string]string{"error": "not found"})
		return true
	}
	info, err := a.identity()
	if err == nil {
		err = a.robotWriteAllowed(info)
	}
	if err != nil {
		send(403, map[string]string{"error": err.Error()})
		return true
	}
	if p == "/api/v1/voice/install" {
		extendDeadlines(w, 5*time.Minute)
		pack, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxVoicePack))
		if err != nil {
			send(400, map[string]string{"error": "file too large (64 MB at most) or upload interrupted"})
			return true
		}
		reply(a.installVoice(r.URL.Query().Get("id"), pack))
		return true
	}
	var req struct {
		ID     string `json:"id"`
		Number *int   `json:"number"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		send(400, map[string]string{"error": "invalid request"})
		return true
	}
	switch p {
	case "/api/v1/voice/use":
		reply(a.useVoice(r.Context(), req.ID))
	case "/api/v1/voice/remove":
		reply(a.removeVoice(req.ID))
	default:
		if req.Number == nil || *req.Number < 0 || *req.Number > 9999 {
			send(400, map[string]string{"error": "number 0-9999 required"})
			return true
		}
		msg, _ := json.Marshal(map[string]any{"type": "media", "cmd": "play", "file_number": *req.Number})
		_, err := a.runAvacmd(r.Context(), "media", string(msg))
		reply(map[string]any{"played": *req.Number}, err)
	}
	return true
}

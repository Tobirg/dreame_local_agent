package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ADB on the robot: the firmware runs adbd with key checking (ADB_AUTH_ENABLE=1) and reads the allowed keys from
// /etc/adb_keys. The root boot hook bind-mounts /data/test/adb_keys over it, so that file is the list the owner
// edits. The panel lists the keys, adds and removes them, and stops ADB until the next boot; a reboot always
// starts it again (rc.sysinit), so the robot cannot be locked out.
const (
	adbKeysPath    = "/data/test/adb_keys"
	adbKeysBackup  = "/data/test/adb_keys.before-agent"
	adbActiveKeys  = "/etc/adb_keys"
	adbAuthService = "/usr/bin/adbd_auth_service"
	adbKeyBytes    = 524 // Android RSA public key struct: len, n0inv, n[64], rr[64], exponent
)

var adbCommentRe = regexp.MustCompile(`^[A-Za-z0-9@._ :-]{0,64}$`)

type adbControl struct {
	mu     sync.Mutex
	signal func(pid int, sig syscall.Signal) error // tests record instead
	start  func() error                            // tests record instead
	wait   time.Duration
}

// adbRefusal is a request the agent turns down (400); other errors are failures on the robot (500).
type adbRefusal string

func (e adbRefusal) Error() string { return string(e) }

type adbKey struct {
	ID      string `json:"id"`
	Comment string `json:"comment"`
}

// adbKeyLine checks one "<base64 key> [comment]" line and returns its id (first 16 hex of the key's sha256).
func adbKeyLine(line string) (id, comment string, ok bool) {
	key, comment, _ := strings.Cut(strings.TrimSpace(line), " ")
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != adbKeyBytes || binary.LittleEndian.Uint32(raw) != 64 {
		return "", "", false
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8]), strings.TrimSpace(comment), true
}

func (a *agent) adbKeyFile() (string, bool) {
	if st, err := os.Stat(a.path(adbKeysPath)); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
		return a.path(adbKeysPath), true
	}
	return a.path(adbActiveKeys), false
}

func (a *agent) adbKeys() ([]adbKey, bool, error) {
	path, editable := a.adbKeyFile()
	b, err := readLimited(path, 65536)
	if err != nil {
		return nil, false, errors.New("cannot read the ADB keys")
	}
	keys := []adbKey{}
	for _, line := range strings.Split(string(b), "\n") {
		if id, comment, ok := adbKeyLine(line); ok {
			keys = append(keys, adbKey{ID: id, Comment: comment})
		}
	}
	return keys, editable, nil
}

// writeADBKeys rewrites /data/test/adb_keys in place: /etc/adb_keys is a bind mount of that very file, so a
// rename would leave adbd reading the old one. The first edit keeps a copy of the original next to it.
func (a *agent) writeADBKeys(path string, data []byte) error {
	backup := a.path(adbKeysBackup)
	if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
		old, err := readLimited(path, 65536)
		if err != nil {
			return errors.New("cannot read the ADB keys")
		}
		if err := atomicPrivate(backup, old); err != nil {
			return errors.New("cannot save a copy of the ADB keys")
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return errors.New("cannot write the ADB keys")
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	if cerr := f.Close(); werr != nil || serr != nil || cerr != nil {
		return errors.New("writing the ADB keys failed; the copy is in " + adbKeysBackup)
	}
	return nil
}

// editADBKeys applies change to the lines of the key file; lines it does not understand are kept as they are.
// At least one valid key must stay, or nobody could use ADB until the file is fixed.
func (a *agent) editADBKeys(change func(lines []string) ([]string, error)) error {
	a.adb.mu.Lock()
	defer a.adb.mu.Unlock()
	path, editable := a.adbKeyFile()
	if !editable {
		return adbRefusal("ADB keys can be changed only through " + adbKeysPath + " (set up by the root boot hook)")
	}
	b, err := readLimited(path, 65536)
	if err != nil {
		return errors.New("cannot read the ADB keys")
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if lines, err = change(lines); err != nil {
		return err
	}
	valid := 0
	for _, l := range lines {
		if _, _, ok := adbKeyLine(l); ok {
			valid++
		}
	}
	if valid == 0 {
		return adbRefusal("the last ADB key cannot be removed")
	}
	return a.writeADBKeys(path, []byte(strings.Join(lines, "\n")+"\n"))
}

func (a *agent) addADBKey(line string) error {
	line = strings.TrimSpace(line)
	id, comment, ok := adbKeyLine(line)
	if !ok || strings.ContainsAny(line, "\r\n") {
		return adbRefusal("not an ADB public key: paste one line of adbkey.pub")
	}
	if !adbCommentRe.MatchString(comment) {
		return adbRefusal("key comment: up to 64 Latin letters, digits and @._:- ")
	}
	return a.editADBKeys(func(lines []string) ([]string, error) {
		for _, l := range lines {
			if other, _, ok := adbKeyLine(l); ok && other == id {
				return nil, adbRefusal("this key is already allowed")
			}
		}
		return append(lines, line), nil
	})
}

func (a *agent) removeADBKey(id string) error {
	return a.editADBKeys(func(lines []string) ([]string, error) {
		out, found := []string{}, false
		for _, l := range lines {
			if other, _, ok := adbKeyLine(l); ok && other == id {
				found = true
				continue
			}
			out = append(out, l)
		}
		if !found {
			return nil, adbRefusal("no such ADB key")
		}
		return out, nil
	})
}

// adbPIDs finds the running adbd and its auth service by their executables.
func (a *agent) adbPIDs() (adbd, auth []int) {
	entries, _ := os.ReadDir(a.path("/proc"))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		exe, err := os.Readlink(a.path("/proc/" + e.Name() + "/exe"))
		if err != nil {
			continue
		}
		switch filepath.Base(exe) {
		case "adbd":
			adbd = append(adbd, pid)
		case "adbd_auth_service":
			auth = append(auth, pid)
		}
	}
	return adbd, auth
}

// adbAuthChecked says whether this adbd checks keys; one started without it would let anyone in.
func (a *agent) adbAuthChecked(pid int) bool {
	env, err := readLimited(a.path("/proc/"+strconv.Itoa(pid)+"/environ"), 65536)
	return err == nil && bytes.Contains(append(append([]byte{0}, env...), 0), []byte("\x00ADB_AUTH_ENABLE=1\x00"))
}

func (a *agent) adbSignal(pid int, sig syscall.Signal) error {
	if a.adb.signal != nil {
		return a.adb.signal(pid, sig)
	}
	return syscall.Kill(pid, sig)
}

func (a *agent) stopADB() error {
	adbd, auth := a.adbPIDs()
	for _, pid := range append(auth, adbd...) { // the auth service first, so it does not start adbd again
		_ = a.adbSignal(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(a.adb.waitTime())
	for time.Now().Before(deadline) {
		if d, s := a.adbPIDs(); len(d)+len(s) == 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	d, s := a.adbPIDs()
	for _, pid := range append(s, d...) {
		_ = a.adbSignal(pid, syscall.SIGKILL)
	}
	time.Sleep(200 * time.Millisecond)
	if d, s := a.adbPIDs(); len(d)+len(s) != 0 {
		return errors.New("ADB did not stop")
	}
	return nil
}

func (c *adbControl) waitTime() time.Duration {
	if c.wait > 0 {
		return c.wait
	}
	return 10 * time.Second
}

// startADB starts the firmware's auth service the way the boot hook does; it starts adbd. An adbd that came up
// without key checking is stopped again.
func (a *agent) startADB() error {
	if adbd, _ := a.adbPIDs(); len(adbd) > 0 {
		return nil
	}
	start := a.adb.start
	if start == nil {
		start = func() error {
			_, err := startDetached(adbAuthService, nil, "/tmp/dreame-local-adb.log", "/tmp/dreame-local-adb.pid")
			return err
		}
	}
	if err := start(); err != nil {
		return errors.New("cannot start ADB: " + err.Error())
	}
	deadline := time.Now().Add(a.adb.waitTime())
	for time.Now().Before(deadline) {
		if adbd, _ := a.adbPIDs(); len(adbd) > 0 {
			for _, pid := range adbd {
				if !a.adbAuthChecked(pid) {
					_ = a.stopADB()
					return errors.New("ADB started without key checking and was stopped again; reboot the robot to get ADB back")
				}
			}
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("ADB did not start; reboot the robot to get it back")
}

func (a *agent) adbStatus() map[string]any {
	adbd, _ := a.adbPIDs()
	keys, editable, err := a.adbKeys()
	out := map[string]any{"running": len(adbd) > 0, "keys": keys, "editable": editable}
	if len(adbd) > 0 {
		out["auth"] = a.adbAuthChecked(adbd[0])
	}
	if err != nil {
		out["keys_error"] = err.Error()
	}
	return out
}

func (a *agent) adbRoute(r *http.Request, send func(int, any)) {
	fail := func(err error) {
		code := 500
		var refused adbRefusal
		if errors.As(err, &refused) {
			code = 400
		}
		send(code, map[string]string{"error": err.Error()})
	}
	switch {
	case r.URL.Path == "/api/v1/system/adb" && r.Method == "GET":
		send(200, map[string]any{"result": a.adbStatus()})
	case r.URL.Path == "/api/v1/system/adb" && r.Method == "POST":
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req) != nil || req.Enabled == nil {
			send(400, map[string]string{"error": `send {"enabled": true|false}`})
			return
		}
		a.adb.mu.Lock()
		var err error
		if *req.Enabled {
			err = a.startADB()
		} else {
			err = a.stopADB()
		}
		a.adb.mu.Unlock()
		if err != nil {
			fail(err)
			return
		}
		send(200, map[string]any{"result": a.adbStatus()})
	case r.URL.Path == "/api/v1/system/adb/keys" && r.Method == "POST":
		var req struct {
			Key string `json:"key"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil {
			send(400, map[string]string{"error": "invalid request"})
			return
		}
		if err := a.addADBKey(req.Key); err != nil {
			fail(err)
			return
		}
		send(200, map[string]any{"result": a.adbStatus()})
	case strings.HasPrefix(r.URL.Path, "/api/v1/system/adb/keys/") && r.Method == "DELETE":
		if err := a.removeADBKey(strings.TrimPrefix(r.URL.Path, "/api/v1/system/adb/keys/")); err != nil {
			fail(err)
			return
		}
		send(200, map[string]any{"result": a.adbStatus()})
	default:
		send(405, map[string]string{"error": "method not allowed"})
	}
}

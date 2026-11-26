package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Video monitoring session, as the app's camera page opens it (bundle class Monitor, siid 10001).
// Checked on r2505 2026-11-25: without it the firmware takes remote_move (4/15) as "remote clean" and
// runs the vacuum motor. With it open, video_monitor raises ava's monitoring flag (ava_msg_dev type=19),
// the robot says "camera monitoring", the same 4/15 drives with clean_state 0 and the motor stays off.
// Order: 10001/4 piid 1102 verifyAccessCode {oldcode: sha256(PIN), session} authorises the session
// (else 10001/1 answers -1), 10001/1 piid 1 monitor start, keep_alive (piid 6) at most every 10 s,
// monitor end. The owner's video access code is kept only as its sha256 (config video_code_sha256).

const (
	monitorSession   = "local"
	monitorKeepAlive = 8 * time.Second
	// The robot leaves remote control ~15 s after the last step; the session ends a while after that.
	monitorIdle = 30 * time.Second
	avaLogGlob  = "/tmp/log/log_*"
	cameraHold  = "/tmp/dreame-camera/hold" // the camera module's live view leaves /dev/video0 while fresh
)

// How long the firmware may take to confirm monitoring (ava "type=19, value=1"; 0.5-0.8 s seen).
var monitorConfirm = 4 * time.Second

var errNoVideoCode = errors.New("set the video access code (panel: Camera) first: without the video monitoring session the robot runs the vacuum motor while driving")

type monitorState struct {
	sync.Mutex
	open    bool
	last    time.Time
	holds   int    // running patrols keep the session open
	refused string // a code hash the robot refused: not sent again, so the firmware does not freeze the code
	stop    chan struct{}
}

var monitor monitorState

var videoCodeRe = regexp.MustCompile(`^[0-9]{4,8}$`)

func (a *agent) videoCodeHash() string {
	a.tokensMu.RLock()
	defer a.tokensMu.RUnlock()
	return a.cfg.VideoCodeSHA256
}

func monitorValue(fields map[string]any) string {
	fields["session"] = monitorSession
	b, _ := json.Marshal(fields)
	return string(b)
}

func monitorAction(did string, aiid, piid int, fields map[string]any) map[string]any {
	return map[string]any{"did": did, "siid": 10001, "aiid": aiid, "in": []any{map[string]any{"piid": piid, "value": monitorValue(fields)}}}
}

// ensureMonitor opens the session if needed (caller holds the IPC gate, as runCommand does) and notes use.
func (a *agent) ensureMonitor(ctx context.Context, did string) error {
	hash := a.videoCodeHash()
	if hash == "" {
		return errNoVideoCode
	}
	monitor.Lock()
	defer monitor.Unlock()
	monitor.last = time.Now()
	if monitor.open {
		return nil
	}
	if monitor.refused == hash {
		return errors.New("the robot refused this video access code; enter the code again in the panel")
	}
	result, err := a.miot(ctx, "action", monitorAction(did, 4, 1102, map[string]any{"oldcode": hash}))
	if err != nil {
		return err
	}
	if err := rejected(result); err != nil {
		return errors.New("video access code not checked: " + err.Error())
	}
	switch verifyCode(result) {
	case 2: // right code
	case 0:
		return errors.New("no video access code is set on the robot: set one in the Dreame app's camera page")
	case 1:
		monitor.refused = hash
		return errors.New("the robot refused the video access code; enter the code again in the panel")
	case 3:
		monitor.refused = hash
		return errors.New("the video access code is locked after wrong attempts; reset it in the Dreame app")
	default:
		return errors.New("unexpected answer to the video access code")
	}
	// The firmware encodes the camera and streams it to its cloud while a session is open (RTMP to an
	// address the cloud hands out). With video_cloud_block (panel: protections) new outbound connections
	// are refused while the session lasts; existing ones, such as the cloud MQTT, stay up.
	if a.videoCloudBlocked() {
		if err := a.cloudBlock(true); err != nil {
			return errors.New("cannot keep the robot's video off the cloud: " + err.Error())
		}
	}
	// video_monitor needs /dev/video0, which the camera module's live view may hold: without it the
	// session fails quietly and the next step runs the motor (seen 2026-11-25 on 0.74.0).
	a.holdCamera(true)
	offsets := a.avaLogOffsets()
	result, err = a.miot(ctx, "action", monitorAction(did, 1, 1, map[string]any{"token": "alify", "channelId": monitorSession, "area": "0", "operType": "monitor", "operation": "start"}))
	if err == nil && rejected(result) != nil {
		err = errors.New("the robot did not open video monitoring")
	}
	// Drive only once ava raises its monitoring flag (about 0.5 s after start), never on a hope.
	if err == nil && !a.monitoringConfirmed(ctx, offsets, monitorConfirm) {
		_, _ = a.miot(ctx, "action", monitorAction(did, 1, 1, map[string]any{"operType": "monitor", "operation": "end"}))
		err = errors.New("video monitoring did not start (is the camera busy?); not driving, so the vacuum motor stays off")
	}
	if err != nil {
		a.cloudBlock(false)
		a.holdCamera(false)
		return err
	}
	// The app sends FillinLight "101" (auto) right after start; without it the firmware, after the
	// session, stops lighting the fill light for cleaning in the dark (seen 2026-11-25 from 19:06).
	if _, err := a.miot(ctx, "action", monitorAction(did, 3, 9, map[string]any{"value": "101"})); err != nil {
		log.Printf("video monitoring: fill light auto: %v", err)
	}
	monitor.open = true
	monitor.stop = make(chan struct{})
	go a.keepMonitor(monitor.stop)
	return nil
}

// holdMonitor keeps the session open until the returned release runs (patrol).
func (a *agent) holdMonitor() func() {
	monitor.Lock()
	monitor.holds++
	monitor.Unlock()
	return func() {
		monitor.Lock()
		monitor.holds--
		monitor.last = time.Now()
		monitor.Unlock()
	}
}

func (a *agent) keepMonitor(stop chan struct{}) {
	t := time.NewTicker(monitorKeepAlive)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		if a.endIdleMonitor(stop) {
			return
		}
		fields := map[string]any{"operType": "keep_alive", "videoStatus": "opened"}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		raw, _ := json.Marshal(monitorAction("", 1, 6, fields))
		_, err := a.call(ctx, request{Method: "action", Params: raw})
		cancel()
		if err == nil {
			a.holdCamera(true) // keep the hold fresh
			continue
		}
		log.Printf("video monitoring: %v", err)
		monitor.Lock()
		if monitor.stop == stop {
			monitor.open = false
			monitor.stop = nil
			a.cloudBlock(false)
			a.holdCamera(false)
		}
		monitor.Unlock()
		return
	}
}

// endIdleMonitor ends the session when nobody used it for monitorIdle and reports whether it is over.
// It checks and ends while holding the IPC gate and the monitor lock (the order runCommand takes them):
// a drive step that slipped in between the check and the end would find the session "open", be sent
// after the end and run the vacuum motor. keep_alive goes to piid 6 (checkAlive), start and end to
// piid 1 (PropMonitor).
func (a *agent) endIdleMonitor(stop chan struct{}) bool {
	monitor.Lock()
	idle := monitor.holds == 0 && time.Since(monitor.last) > monitorIdle
	monitor.Unlock()
	if !idle {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	select {
	case a.gate <- struct{}{}:
	case <-ctx.Done():
		return false // the next tick tries again
	}
	defer func() { <-a.gate }()
	monitor.Lock()
	defer monitor.Unlock()
	if monitor.stop != stop {
		return true // ended or replaced meanwhile
	}
	if monitor.holds > 0 || time.Since(monitor.last) <= monitorIdle {
		return false // used again while we waited for the gate
	}
	info, err := a.identity()
	if err == nil {
		_, err = a.miot(ctx, "action", monitorAction(info["did"], 1, 1, map[string]any{"operType": "monitor", "operation": "end"}))
	}
	if err != nil {
		log.Printf("video monitoring: %v", err)
	}
	monitor.open, monitor.stop = false, nil
	a.cloudBlock(false)
	a.holdCamera(false)
	return true
}

// videoCodeRoute: GET says whether a code is set; POST {"code": "1234"} stores its sha256, "" clears it.
// Main token only (limitedRoute lets no scoped token reach /api/v1/system).
func (a *agent) videoCodeRoute(w http.ResponseWriter, r *http.Request, send func(int, any)) {
	switch r.Method {
	case "GET":
		send(200, map[string]any{"result": map[string]any{"configured": a.videoCodeHash() != ""}})
	case "POST":
		var req struct {
			Code *string `json:"code"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req) != nil || req.Code == nil || (*req.Code != "" && !videoCodeRe.MatchString(*req.Code)) {
			send(400, map[string]string{"error": "code: 4-8 digits, or empty to clear"})
			return
		}
		hash := ""
		if *req.Code != "" {
			sum := sha256.Sum256([]byte(*req.Code))
			hash = hex.EncodeToString(sum[:])
		}
		a.tokensMu.Lock()
		err := a.saveConfigField("video_code_sha256", hash)
		if err == nil {
			a.cfg.VideoCodeSHA256 = hash
		}
		a.tokensMu.Unlock()
		if err == nil {
			// Entering the code again (say, after fixing it in the Dreame app) is a new try, even
			// when it is the same code the robot refused before.
			monitor.Lock()
			monitor.refused = ""
			monitor.Unlock()
		}
		if err != nil {
			send(500, map[string]string{"error": err.Error()})
			return
		}
		send(200, map[string]any{"result": map[string]any{"configured": hash != ""}})
	default:
		send(405, map[string]string{"error": "method not allowed"})
	}
}

// verifyCode reads 1102's answer {"code": n, "fail": k}: 0 no code set, 1 wrong, 2 right, 3 locked
// (bundle StartUpPage.startWithCode); -1 when the answer has no such field.
func verifyCode(result any) int {
	raw, _ := result.(json.RawMessage)
	var reply struct {
		Out []struct {
			Value string `json:"value"`
		} `json:"out"`
	}
	if json.Unmarshal(raw, &reply) != nil || len(reply.Out) != 1 {
		return -1
	}
	var v struct {
		Code *int `json:"code"`
	}
	if json.Unmarshal([]byte(reply.Out[0].Value), &v) != nil || v.Code == nil {
		return -1
	}
	return *v.Code
}

// videoMonitor opens (enabled true, also the panel's heartbeat) or ends the monitoring session.
func (a *agent) videoMonitor(ctx context.Context, p commandParams, did string) (any, error) {
	if p.Enabled == nil || p.Value != nil || p.Points != nil || p.Rooms != nil {
		return nil, errors.New("video_monitor takes enabled true or false")
	}
	if *p.Enabled {
		if err := a.ensureMonitor(ctx, did); err != nil {
			return nil, err
		}
		return map[string]any{"monitoring": true}, nil
	}
	return map[string]any{"monitoring": a.endMonitor(ctx, did)}, nil
}

// endMonitor ends the session now unless a patrol or pet search holds it; it reports whether it stays open.
func (a *agent) endMonitor(ctx context.Context, did string) bool {
	monitor.Lock()
	defer monitor.Unlock()
	if !monitor.open {
		return false
	}
	if monitor.holds > 0 {
		return true
	}
	if _, err := a.miot(ctx, "action", monitorAction(did, 1, 1, map[string]any{"operType": "monitor", "operation": "end"})); err != nil {
		log.Printf("video monitoring: %v", err)
	}
	close(monitor.stop)
	monitor.open, monitor.stop = false, nil
	a.cloudBlock(false)
	a.holdCamera(false)
	return false
}

// findPet runs the app's pet search (FindPet page): 4/1 mode 22 MonitorCruiseMode with {"findpet": ""}.
// The robot looks around the home for the pet in status 22; the session stays open while it does.
// Without the session mode 22 ends at once (checked 2026-11-24).
func (a *agent) findPet(ctx context.Context, p commandParams, did string) (any, error) {
	if p.Value != nil || p.Points != nil || p.Rooms != nil || p.Enabled != nil {
		return nil, errors.New("find_pet takes no arguments")
	}
	if err := a.ensureMonitor(ctx, did); err != nil {
		return nil, err
	}
	result, err := a.miot(ctx, "action", map[string]any{"did": did, "siid": 4, "aiid": 1, "in": []any{
		map[string]any{"piid": 1, "value": 22}, map[string]any{"piid": 10, "value": `{"findpet":""}`}}})
	if err != nil {
		return nil, err
	}
	if err := rejected(result); err != nil {
		return result, err
	}
	release := a.holdMonitor()
	go func() {
		defer release()
		start, seen := time.Now(), false
		for time.Since(start) < 30*time.Minute {
			time.Sleep(5 * time.Second)
			switch s := a.robotStatus(); {
			case s == 22:
				seen = true
			case seen || time.Since(start) > 30*time.Second:
				return
			}
		}
	}()
	a.refreshSoon()
	return result, nil
}

// cloudBlock adds (or removes) nat OUTPUT chain DREAME_VIDEO, which sends every new outbound TCP/UDP
// connection to a public address to 127.0.0.1:9 (refused). The r2505 kernel has only the nat table
// (no filter, owner or cgroup match) and every firmware process runs as root, so it cannot single out
// video_monitor; the block lasts only while the session is open. Removing is best effort and idempotent.
func (a *agent) cloudBlock(on bool) error {
	if len(a.iptables) == 0 {
		return nil
	}
	run := func(args ...string) error {
		cmd := exec.Command(a.iptables[0], append(append([]string{}, a.iptables[1:]...), args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("iptables %s: %v %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	const chain = "DREAME_VIDEO"
	for _, p := range []string{"tcp", "udp"} {
		for run("-t", "nat", "-D", "OUTPUT", "-p", p, "-j", chain) == nil {
		}
	}
	_ = run("-t", "nat", "-F", chain)
	_ = run("-t", "nat", "-X", chain)
	if !on {
		return nil
	}
	steps := [][]string{{"-t", "nat", "-N", chain}}
	for _, private := range []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "224.0.0.0/4"} {
		steps = append(steps, []string{"-t", "nat", "-A", chain, "-d", private, "-j", "RETURN"})
	}
	steps = append(steps,
		[]string{"-t", "nat", "-A", chain, "-p", "tcp", "-j", "DNAT", "--to-destination", "127.0.0.1:9"},
		[]string{"-t", "nat", "-A", chain, "-p", "udp", "-j", "DNAT", "--to-destination", "127.0.0.1:9"},
		[]string{"-t", "nat", "-I", "OUTPUT", "-p", "tcp", "-j", chain},
		[]string{"-t", "nat", "-I", "OUTPUT", "-p", "udp", "-j", chain})
	for _, s := range steps {
		if err := run(s...); err != nil {
			a.cloudBlock(false)
			return err
		}
	}
	return nil
}

func (a *agent) videoCloudBlocked() bool {
	a.tokensMu.RLock()
	defer a.tokensMu.RUnlock()
	return a.cfg.VideoCloudBlock
}

// videoCloudRoute: GET/POST {"enabled": bool} for video_cloud_block (main token only).
func (a *agent) videoCloudRoute(w http.ResponseWriter, r *http.Request, send func(int, any)) {
	a.boolRoute(r, send, "video_cloud_block", &a.cfg.VideoCloudBlock)
}

func (a *agent) boolRoute(r *http.Request, send func(int, any), field string, value *bool) {
	switch r.Method {
	case "GET":
		a.tokensMu.Lock()
		v := *value
		a.tokensMu.Unlock()
		send(200, map[string]any{"result": map[string]any{"enabled": v}})
	case "POST":
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req) != nil || req.Enabled == nil {
			send(400, map[string]string{"error": "enabled (true/false) required"})
			return
		}
		a.tokensMu.Lock()
		err := a.saveConfigField(field, *req.Enabled)
		if err == nil {
			*value = *req.Enabled
		}
		a.tokensMu.Unlock()
		if err != nil {
			send(500, map[string]string{"error": err.Error()})
			return
		}
		send(200, map[string]any{"result": map[string]any{"enabled": *req.Enabled}})
	default:
		send(405, map[string]string{"error": "method not allowed"})
	}
}

// avaLogOffsets notes the size of ava's logs, so monitoringConfirmed reads only what comes after.
func (a *agent) avaLogOffsets() map[string]int64 {
	out := map[string]int64{}
	paths, _ := filepath.Glob(a.path(avaLogGlob))
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			out[p] = st.Size()
		}
	}
	return out
}

// monitoringConfirmed waits for ava's "type=19, value=1" (video_monitor raised the monitoring flag)
// in what its logs gained since offsets; a log that shrank was rotated and is read from the start.
func (a *agent) monitoringConfirmed(ctx context.Context, offsets map[string]int64, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for {
		paths, _ := filepath.Glob(a.path(avaLogGlob))
		for _, p := range paths {
			f, err := os.Open(p)
			if err != nil {
				continue
			}
			st, _ := f.Stat()
			from := offsets[p]
			if st == nil || st.Size() < from {
				from = 0
			}
			b, _ := io.ReadAll(io.LimitReader(io.NewSectionReader(f, from, 1<<30), 4<<20))
			f.Close()
			if bytes.Contains(b, []byte("type=19, value=1")) {
				return true
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// holdCamera asks the camera module's live view to leave /dev/video0 to the firmware (hold file,
// fresh for 20 s) and waits up to 2 s until it reports it did; false removes the hold.
func (a *agent) holdCamera(on bool) {
	if _, err := a.installedModule("camera"); err != nil {
		return
	}
	hold := a.path(cameraHold)
	if !on {
		_ = os.Remove(hold)
		return
	}
	_ = os.MkdirAll(filepath.Dir(hold), 0700)
	if os.WriteFile(hold, []byte("agent\n"), 0600) != nil {
		return
	}
	status := filepath.Join(filepath.Dir(hold), "status.json")
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		b, _ := readLimited(status, 4096)
		if !bytes.Contains(b, []byte(`"/dev/video0"`)) {
			return
		}
	}
}

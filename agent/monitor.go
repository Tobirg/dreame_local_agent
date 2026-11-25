package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
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
	monitorIdle = 45 * time.Second
)

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
	result, err = a.miot(ctx, "action", monitorAction(did, 1, 1, map[string]any{"token": "alify", "channelId": monitorSession, "area": "0", "operType": "monitor", "operation": "start"}))
	if err != nil {
		return err
	}
	if rejected(result) != nil {
		monitor.refused = hash
		return errors.New("the robot did not open video monitoring: wrong video access code?")
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
		monitor.Lock()
		idle := monitor.holds == 0 && time.Since(monitor.last) > monitorIdle
		monitor.Unlock()
		fields := map[string]any{"operType": "keep_alive", "videoStatus": "opened"}
		if idle {
			fields = map[string]any{"operType": "monitor", "operation": "end"}
		}
		raw, _ := json.Marshal(monitorAction("", 1, idle2piid(idle), fields))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, err := a.call(ctx, request{Method: "action", Params: raw})
		cancel()
		if err != nil {
			log.Printf("video monitoring: %v", err)
		}
		if idle || err != nil {
			monitor.Lock()
			if monitor.stop == stop {
				monitor.open = false
				monitor.stop = nil
			}
			monitor.Unlock()
			return
		}
	}
}

// keep_alive goes to piid 6 (checkAlive), start and end to piid 1 (PropMonitor).
func idle2piid(end bool) int {
	if end {
		return 1
	}
	return 6
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

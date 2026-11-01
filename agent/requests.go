package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type commandMemo struct {
	digest  [32]byte
	done    chan struct{}
	created time.Time
	status  int
	body    []byte
}

func requestIsRead(req request) bool {
	if req.Method == "get_properties" || req.Method == "miIO.info" || req.Method == "local.maps" || req.Method == "local.map_view" || strings.HasSuffix(req.Method, "_status") {
		return true
	}
	if req.Method == "action" {
		params, err := decodeParams(req.Params)
		if err == nil {
			_, read, err := validate(req.Method, params)
			return err == nil && read
		}
	}
	return false
}
func (a *agent) memoCall(ctx context.Context, req request, id string) (int, []byte) {
	failure := func(code int, message string) (int, []byte) {
		body, _ := json.Marshal(map[string]string{"error": message})
		return code, body
	}
	if len(id) < 16 || len(id) > 80 {
		return failure(http.StatusBadRequest, "invalid command request ID")
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return failure(http.StatusBadRequest, "invalid command request ID")
		}
	}
	canonical, _ := json.Marshal(req)
	digest := sha256.Sum256(canonical)
	a.memoMu.Lock()
	if a.memos == nil {
		a.memos = map[string]*commandMemo{}
	}
	if existing := a.memos[id]; existing != nil {
		a.memoMu.Unlock()
		if existing.digest != digest {
			return failure(http.StatusConflict, "command request ID reused for another payload")
		}
		select {
		case <-existing.done:
			return existing.status, existing.body
		case <-ctx.Done():
			return failure(504, "waiting for original command outcome")
		}
	}
	// Bound retained responses. Pending commands are never evicted.
	if len(a.memos) >= 256 {
		var oldestID string
		var oldest time.Time
		for key, item := range a.memos {
			select {
			case <-item.done:
				if oldestID == "" || item.created.Before(oldest) {
					oldestID = key
					oldest = item.created
				}
			default:
			}
		}
		if oldestID == "" {
			a.memoMu.Unlock()
			return failure(503, "too many pending commands")
		}
		delete(a.memos, oldestID)
	}
	entry := &commandMemo{digest: digest, done: make(chan struct{}), created: time.Now()}
	a.memos[id] = entry
	a.memoMu.Unlock()
	result, err := a.call(ctx, req)
	if err != nil {
		entry.status, entry.body = failure(502, err.Error())
	} else {
		entry.status = 200
		entry.body, _ = json.Marshal(map[string]any{"result": result})
	}
	// Keep large responses from retaining map/image buffers; do not execute again.
	if len(entry.body) > 16<<10 {
		entry.status, entry.body = failure(502, "command completed; response too large to retain; command will not be repeated")
	}
	close(entry.done)
	return entry.status, entry.body
}

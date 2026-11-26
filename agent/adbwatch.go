package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Asking to allow a new ADB device, as Android does. When a device whose key is not allowed connects, its adb
// client sends the public key in the clear (an AUTH message with arg0 3, RSAPUBLICKEY). The agent only listens
// to the robot's ADB port with a packet socket; it never talks to adbd or its auth service, so a fault here
// cannot change who gets in. The panel shows the request; "Allow" adds the key to the allowed list.
const (
	adbPort          = 4567
	adbAuthCommand   = 0x48545541 // "AUTH"
	adbAuthPublicKey = 3
	adbRequestTTL    = 10 * time.Minute
	maxADBRequests   = 8
)

type adbRequest struct {
	ID      string    `json:"id"`
	Comment string    `json:"comment"`
	From    string    `json:"from"`
	At      time.Time `json:"at"`
	line    string
}

type adbWatch struct {
	mu       sync.Mutex
	requests map[string]*adbRequest
	declined map[string]time.Time
	flows    map[string]*adbFlow
	running  bool
	err      string
}

type adbFlow struct {
	buf  []byte
	seen time.Time
}

// adbPublicKey finds a complete AUTH RSAPUBLICKEY message in b and returns its key line.
func adbPublicKey(b []byte) (string, bool) {
	for i := 0; i+24 <= len(b); i++ {
		if binary.LittleEndian.Uint32(b[i:]) != adbAuthCommand || binary.LittleEndian.Uint32(b[i+4:]) != adbAuthPublicKey {
			continue
		}
		size := int(binary.LittleEndian.Uint32(b[i+12:]))
		if binary.LittleEndian.Uint32(b[i+20:]) != adbAuthCommand^0xffffffff || size < 100 || size > 2048 {
			continue
		}
		if i+24+size > len(b) {
			return "", false // the rest is still on its way
		}
		data := bytes.TrimRight(b[i+24:i+24+size], "\x00\n ")
		return string(data), true
	}
	return "", false
}

// cleanADBComment keeps the comment the client sent (user@host) within the characters the key file accepts.
func cleanADBComment(c string) string {
	out := []rune{}
	for _, r := range c {
		if len(out) == 64 {
			break
		}
		if adbCommentRe.MatchString(string(r)) {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	return strings.TrimSpace(string(out))
}

// seeADBSegment takes the TCP payload a client sent to the ADB port.
func (a *agent) seeADBSegment(from string, flow string, payload []byte, now time.Time) {
	w := &a.adbWatch
	w.mu.Lock()
	if w.flows == nil {
		w.flows = map[string]*adbFlow{}
	}
	for k, f := range w.flows {
		if now.Sub(f.seen) > 10*time.Second {
			delete(w.flows, k)
		}
	}
	f := w.flows[flow]
	if f == nil {
		if len(w.flows) >= 64 {
			w.mu.Unlock()
			return
		}
		f = &adbFlow{}
		w.flows[flow] = f
	}
	f.seen = now
	f.buf = append(f.buf, payload...)
	if len(f.buf) > 4096 {
		f.buf = append([]byte(nil), f.buf[len(f.buf)-4096:]...)
	}
	line, ok := adbPublicKey(f.buf)
	if ok {
		delete(w.flows, flow)
	}
	w.mu.Unlock()
	if ok {
		a.noteADBKey(from, line, now)
	}
}

func (a *agent) noteADBKey(from, line string, now time.Time) {
	key, comment, _ := strings.Cut(line, " ")
	comment = cleanADBComment(comment)
	line = strings.TrimSpace(key + " " + comment)
	id, _, ok := adbKeyLine(line)
	if !ok {
		return
	}
	if keys, _, err := a.adbKeys(); err == nil {
		for _, k := range keys {
			if k.ID == id {
				return // already allowed: the client only offered the key before signing
			}
		}
	}
	w := &a.adbWatch
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.requests == nil {
		w.requests, w.declined = map[string]*adbRequest{}, map[string]time.Time{}
	}
	if t, ok := w.declined[id]; ok && now.Sub(t) < adbRequestTTL {
		return
	}
	if r := w.requests[id]; r != nil {
		r.At, r.From = now, from
		return
	}
	if len(w.requests) >= maxADBRequests {
		oldest := ""
		for k, r := range w.requests {
			if oldest == "" || r.At.Before(w.requests[oldest].At) {
				oldest = k
			}
		}
		delete(w.requests, oldest)
	}
	w.requests[id] = &adbRequest{ID: id, Comment: comment, From: from, At: now, line: line}
	log.Printf("ADB: new device %q from %s asks to connect; allow it in the panel (Access → ADB)", comment, from)
}

func (a *agent) adbRequests(now time.Time) []adbRequest {
	w := &a.adbWatch
	w.mu.Lock()
	defer w.mu.Unlock()
	out := []adbRequest{}
	for id, r := range w.requests {
		if now.Sub(r.At) > adbRequestTTL {
			delete(w.requests, id)
			continue
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// answerADBRequest allows (adds the key) or declines a pending request.
func (a *agent) answerADBRequest(id string, allow bool) error {
	w := &a.adbWatch
	w.mu.Lock()
	r := w.requests[id]
	if r == nil {
		w.mu.Unlock()
		return adbRefusal("no such request (it may have expired)")
	}
	line := r.line
	w.mu.Unlock()
	if allow {
		if err := a.addADBKey(line); err != nil && err.Error() != "this key is already allowed" {
			return err
		}
	}
	w.mu.Lock()
	delete(w.requests, id)
	if !allow {
		w.declined[id] = time.Now()
	}
	w.mu.Unlock()
	return nil
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// adbFilter is "tcp dst port 4567" over IPv4 for a packet socket (as tcpdump -dd prints it), so the agent wakes
// only for ADB traffic to the robot.
var adbFilter = []syscall.SockFilter{
	{Code: 0x28, Jt: 0, Jf: 0, K: 12},         // ldh [12] ethertype
	{Code: 0x15, Jt: 0, Jf: 8, K: 0x0800},     // IPv4
	{Code: 0x30, Jt: 0, Jf: 0, K: 23},         // ldb [23] protocol
	{Code: 0x15, Jt: 0, Jf: 6, K: 6},          // TCP
	{Code: 0x28, Jt: 0, Jf: 0, K: 20},         // ldh [20] fragment
	{Code: 0x45, Jt: 4, Jf: 0, K: 0x1fff},     // not a later fragment
	{Code: 0xb1, Jt: 0, Jf: 0, K: 14},         // ldxb 4*([14]&0xf)
	{Code: 0x48, Jt: 0, Jf: 0, K: 16},         // ldh [x+16] destination port
	{Code: 0x15, Jt: 0, Jf: 1, K: adbPort},    // 4567
	{Code: 0x06, Jt: 0, Jf: 0, K: 0x00040000}, // accept
	{Code: 0x06, Jt: 0, Jf: 0, K: 0},          // drop
}

// tcpPayload returns the source address, a flow key and the TCP payload of an Ethernet/IPv4 frame to adbPort.
func tcpPayload(frame []byte) (from, flow string, payload []byte, ok bool) {
	if len(frame) < 14+20 || binary.BigEndian.Uint16(frame[12:]) != 0x0800 {
		return "", "", nil, false
	}
	ip := frame[14:]
	ihl := int(ip[0]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(ip[2:]))
	if ip[0]>>4 != 4 || ihl < 20 || ip[9] != 6 || total > len(ip) || total < ihl+20 {
		return "", "", nil, false
	}
	tcp := ip[ihl:total]
	off := int(tcp[12]>>4) * 4
	if off < 20 || off > len(tcp) || binary.BigEndian.Uint16(tcp[2:]) != adbPort {
		return "", "", nil, false
	}
	src := net.IP(ip[12:16]).String()
	return src, src + ":" + strconv.Itoa(int(binary.BigEndian.Uint16(tcp[0:]))), tcp[off:], true
}

func (a *agent) runADBWatch(ctx context.Context) {
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err == nil {
		prog := syscall.SockFprog{Len: uint16(len(adbFilter)), Filter: &adbFilter[0]}
		if _, _, e := syscall.Syscall6(syscall.SYS_SETSOCKOPT, uintptr(fd), syscall.SOL_SOCKET, syscall.SO_ATTACH_FILTER, uintptr(unsafe.Pointer(&prog)), unsafe.Sizeof(prog), 0); e != 0 {
			err = e
		} else {
			err = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &syscall.Timeval{Sec: 1})
		}
		if err != nil {
			syscall.Close(fd)
		}
	}
	a.adbWatch.mu.Lock()
	a.adbWatch.running, a.adbWatch.err = err == nil, ""
	if err != nil {
		a.adbWatch.err = err.Error()
	}
	a.adbWatch.mu.Unlock()
	if err != nil {
		log.Printf("ADB: cannot watch for new devices: %v", err)
		return
	}
	defer syscall.Close(fd)
	buf := make([]byte, 65536)
	for ctx.Err() == nil {
		n, sa, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
				continue
			}
			log.Printf("ADB: watching for new devices stopped: %v", err)
			return
		}
		if ll, ok := sa.(*syscall.SockaddrLinklayer); ok && ll.Pkttype == syscall.PACKET_OUTGOING {
			continue
		}
		if from, flow, payload, ok := tcpPayload(buf[:n]); ok && len(payload) > 0 {
			a.seeADBSegment(from, flow, payload, time.Now())
		}
	}
}

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// adbAuthMessage is what an adb client sends when the robot does not know its key.
func adbAuthMessage(keyLine string) []byte {
	data := append([]byte(keyLine), 0)
	h := make([]byte, 24)
	binary.LittleEndian.PutUint32(h[0:], adbAuthCommand)
	binary.LittleEndian.PutUint32(h[4:], adbAuthPublicKey)
	binary.LittleEndian.PutUint32(h[12:], uint32(len(data)))
	binary.LittleEndian.PutUint32(h[20:], adbAuthCommand^0xffffffff)
	return append(h, data...)
}

func TestADBConnectRequest(t *testing.T) {
	a := testAgent(t)
	pc := testADBKey(t, "pavel@PashaPC")
	if err := os.MkdirAll(a.path("/data/test"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.path(adbKeysPath), []byte(pc+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// The client's own key, already allowed, raises no request.
	a.seeADBSegment("192.168.2.20", "192.168.2.20:5000", adbAuthMessage(pc), now)
	if len(a.adbRequests(now)) != 0 {
		t.Fatal("request for an allowed key")
	}
	// A new key split over two segments, with a comment the key file would refuse.
	laptop := testADBKey(t, "pavel@laptop;rm -rf")
	msg := adbAuthMessage(laptop)
	a.seeADBSegment("192.168.2.50", "192.168.2.50:6000", msg[:24], now)
	if len(a.adbRequests(now)) != 0 {
		t.Fatal("request from half a message")
	}
	a.seeADBSegment("192.168.2.50", "192.168.2.50:6000", msg[24:], now)
	reqs := a.adbRequests(now)
	if len(reqs) != 1 || reqs[0].Comment != "pavel@laptop_rm -rf" || reqs[0].From != "192.168.2.50" {
		t.Fatalf("%+v", reqs)
	}
	// Junk on the port and a wrong magic are ignored.
	bad := adbAuthMessage(testADBKey(t, "x"))
	bad[20] ^= 1
	a.seeADBSegment("192.168.2.60", "192.168.2.60:1", bad, now)
	a.seeADBSegment("192.168.2.60", "192.168.2.60:2", []byte("CNXN garbage"), now)
	if len(a.adbRequests(now)) != 1 {
		t.Fatal("junk made a request")
	}

	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	answer := func(id string, allow bool) int {
		b, _ := json.Marshal(map[string]bool{"allow": allow})
		req, _ := http.NewRequest("POST", server.URL+"/api/v1/system/adb/requests/"+id, strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if answer(reqs[0].ID, true) != 200 {
		t.Fatal("allow")
	}
	got, _ := os.ReadFile(a.path(adbKeysPath))
	if !strings.HasSuffix(string(got), strings.Fields(laptop)[0]+" pavel@laptop_rm -rf\n") || len(a.adbRequests(now)) != 0 {
		t.Fatalf("key file %q", got)
	}
	if answer(reqs[0].ID, true) != 400 {
		t.Fatal("answered twice")
	}
	// Declined: not asked again for a while.
	tablet := testADBKey(t, "tablet")
	a.seeADBSegment("192.168.2.70", "192.168.2.70:1", adbAuthMessage(tablet), now)
	id := a.adbRequests(now)[0].ID
	if answer(id, false) != 200 {
		t.Fatal("decline")
	}
	a.seeADBSegment("192.168.2.70", "192.168.2.70:2", adbAuthMessage(tablet), now)
	if len(a.adbRequests(now)) != 0 || strings.Contains(string(mustRead(t, a.path(adbKeysPath))), strings.Fields(tablet)[0]) {
		t.Fatal("declined key asked again or added")
	}
	// Requests expire.
	a.seeADBSegment("192.168.2.80", "192.168.2.80:1", adbAuthMessage(testADBKey(t, "old")), now)
	if len(a.adbRequests(now.Add(adbRequestTTL+time.Second))) != 0 {
		t.Fatal("request did not expire")
	}
}

func mustRead(t *testing.T, path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTCPPayload(t *testing.T) {
	frame := make([]byte, 14+20+20+3)
	binary.BigEndian.PutUint16(frame[12:], 0x0800)
	ip := frame[14:]
	ip[0], ip[9] = 0x45, 6
	binary.BigEndian.PutUint16(ip[2:], 20+20+3)
	copy(ip[12:16], []byte{192, 168, 2, 50})
	tcp := ip[20:]
	binary.BigEndian.PutUint16(tcp[0:], 40000)
	binary.BigEndian.PutUint16(tcp[2:], adbPort)
	tcp[12] = 5 << 4
	copy(tcp[20:], "abc")
	from, flow, payload, ok := tcpPayload(frame)
	if !ok || from != "192.168.2.50" || flow != "192.168.2.50:40000" || string(payload) != "abc" {
		t.Fatal(from, flow, string(payload), ok)
	}
	binary.BigEndian.PutUint16(tcp[2:], 80)
	if _, _, _, ok := tcpPayload(frame); ok {
		t.Fatal("other port accepted")
	}
	if _, _, _, ok := tcpPayload(frame[:30]); ok {
		t.Fatal("short frame accepted")
	}
}

// TestADBWatchLive runs the packet socket on loopback when the test may open one (root).
func TestADBWatchLive(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:4567")
	if err != nil {
		t.Skip("port 4567 busy")
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				buf := make([]byte, 4096)
				for {
					if _, err := c.Read(buf); err != nil {
						c.Close()
						return
					}
				}
			}()
		}
	}()
	a := testAgent(t)
	_ = os.MkdirAll(filepath.Dir(a.path(adbKeysPath)), 0700)
	_ = os.WriteFile(a.path(adbKeysPath), []byte(testADBKey(t, "pc")+"\n"), 0644)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { a.runADBWatch(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.adbWatch.mu.Lock()
		running, werr := a.adbWatch.running, a.adbWatch.err
		a.adbWatch.mu.Unlock()
		if werr != "" {
			t.Skip("no packet socket here: " + werr)
		}
		if running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	c, err := net.Dial("tcp", "127.0.0.1:4567")
	if err != nil {
		t.Fatal(err)
	}
	msg := adbAuthMessage(testADBKey(t, "live@test"))
	_, _ = c.Write(msg[:24])
	time.Sleep(20 * time.Millisecond)
	_, _ = c.Write(msg[24:])
	defer c.Close()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r := a.adbRequests(time.Now()); len(r) == 1 && r[0].Comment == "live@test" && r[0].From == "127.0.0.1" {
			cancel()
			<-done
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the live AUTH message was not seen", a.adbRequests(time.Now()))
}

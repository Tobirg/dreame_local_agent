package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"
)

// Camera patrol: the agent drives the robot to each point with go_to (mode 23), waits until it
// stands there (status 17), looks around for patrolDwell, and docks after the last point. Checked
// on r2505 2026-11-24: a go_to in the corridor stopped within 3 cm; the firmware's own patrol
// (mode 22) said "Task ends" after 25 cm, with or without saved points.

const (
	patrolDwell    = 5 * time.Second
	patrolLegLimit = 3 * time.Minute
)

var patrolRun struct {
	sync.Mutex
	cancel context.CancelFunc
}

// stopPatrol ends a running patrol; any command that moves the robot calls it first.
func stopPatrol() {
	patrolRun.Lock()
	defer patrolRun.Unlock()
	if patrolRun.cancel != nil {
		patrolRun.cancel()
		patrolRun.cancel = nil
	}
}

func (a *agent) startPatrol(ctx context.Context, p commandParams, did string) (any, error) {
	if len(p.Points) == 0 || len(p.Points) > 20 {
		return nil, errors.New("patrol takes 1..20 points [x, y]")
	}
	legs := make([]json.RawMessage, 0, len(p.Points))
	for _, pt := range p.Points {
		_, params, err := a.detailCommand(commandParams{Name: "go_to", Points: [][]int{pt}}, did)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(params)
		legs = append(legs, b)
	}
	stopPatrol()
	if a.videoCodeHash() != "" { // look around inside the video monitoring session, as the app does
		if err := a.ensureMonitor(ctx, did); err != nil {
			return nil, err
		}
	}
	release := a.holdMonitor()
	run, cancel := context.WithCancel(context.Background())
	patrolRun.Lock()
	patrolRun.cancel = cancel
	patrolRun.Unlock()
	go func() {
		defer release()
		a.patrol(run, legs)
	}()
	return map[string]any{"patrol": "started", "points": len(legs)}, nil
}

func (a *agent) robotStatus() int64 {
	a.dev.mu.Lock()
	defer a.dev.mu.Unlock()
	s, _ := int64Of(a.dev.snap.State["status"])
	return s
}

func (a *agent) patrol(ctx context.Context, legs []json.RawMessage) {
	for i, leg := range legs {
		if _, err := a.call(ctx, request{Method: "action", Params: leg}); err != nil {
			log.Printf("patrol: point %d not sent: %v", i+1, err)
			return
		}
		if !a.waitArrival(ctx) {
			log.Printf("patrol: stopped at point %d", i+1)
			return // someone else took over or the robot failed: leave it as it is
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(patrolDwell):
		}
	}
	if _, err := a.call(ctx, request{Method: "action", Params: json.RawMessage(`{"siid":3,"aiid":1,"in":[]}`)}); err != nil {
		log.Printf("patrol: dock not sent: %v", err)
	}
	stopPatrol()
}

// waitArrival waits for status 23 (driving) and then 17 (standing at the point).
func (a *agent) waitArrival(ctx context.Context) bool {
	start, moving := time.Now(), false
	for time.Since(start) < patrolLegLimit {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
		select { // a fresh poll instead of waiting for the regular one
		case a.dev.poke <- struct{}{}:
		default:
		}
		switch s := a.robotStatus(); {
		case s == 23:
			moving = true
		case s == 17 && moving:
			return true
		case moving || time.Since(start) > 20*time.Second:
			return false
		}
	}
	return false
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Patrol schedules. The app keeps them in 8/5 (CruiseTaskPage) for the firmware's own patrol,
// but mode 22 ends at once on the r2505 without the cloud video session, so the agent keeps
// its own list and starts its own patrol (patrol.go) at the set time. Times are the robot's
// local clock (/etc/localtime), like its cleaning schedules; days are the seven Sunday-first
// digits of schedules.go. A patrol starts only when the robot sits docked without an error.

const patrolSchedulesFile = "/data/dreame-local/patrol-schedules.json"

type patrolSchedule struct {
	ID      int     `json:"id"`
	Enabled bool    `json:"enabled"`
	Time    string  `json:"time"`
	Days    string  `json:"days"`
	Points  [][]int `json:"points"`
	Last    string  `json:"last,omitempty"` // "2006-01-02 15:04" of the last start, so a minute fires once
}

var patrolSchedulesMu sync.Mutex

func (a *agent) readPatrolSchedules() []patrolSchedule {
	list := []patrolSchedule{}
	if b, err := readLimited(a.path(patrolSchedulesFile), 1<<20); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	return list
}

func (a *agent) writePatrolSchedules(list []patrolSchedule) error {
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path(patrolSchedulesFile)), 0700); err != nil {
		return err
	}
	return atomicPrivate(a.path(patrolSchedulesFile), b)
}

func (a *agent) setPatrolSchedule(raw json.RawMessage, did string) (any, error) {
	var p struct {
		ID      *int    `json:"id"`
		Enabled bool    `json:"enabled"`
		Time    string  `json:"time"`
		Days    string  `json:"days"`
		Points  [][]int `json:"points"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || !clockRe.MatchString(p.Time) || len(p.Days) != 7 || !daysRe.MatchString(p.Days) || p.Days == "0000000" || len(p.Points) == 0 || len(p.Points) > 20 {
		return nil, errors.New("patrol schedule requires time HH:MM, days as seven 0/1 digits from Sunday (at least one 1) and 1..20 points")
	}
	for _, pt := range p.Points { // same checks as a patrol started now
		if _, _, err := a.detailCommand(commandParams{Name: "go_to", Points: [][]int{pt}}, did); err != nil {
			return nil, err
		}
	}
	patrolSchedulesMu.Lock()
	defer patrolSchedulesMu.Unlock()
	list := a.readPatrolSchedules()
	item := patrolSchedule{Enabled: p.Enabled, Time: p.Time, Days: p.Days, Points: p.Points}
	if p.ID != nil {
		for i := range list {
			if list[i].ID == *p.ID {
				item.ID, item.Last = list[i].ID, list[i].Last
				list[i] = item
				return item, a.writePatrolSchedules(list)
			}
		}
		return nil, errors.New("no patrol schedule with this id")
	}
	if len(list) >= 10 {
		return nil, errors.New("at most 10 patrol schedules")
	}
	for _, s := range list {
		item.ID = max(item.ID, s.ID)
	}
	item.ID++
	return item, a.writePatrolSchedules(append(list, item))
}

func (a *agent) deletePatrolSchedule(raw json.RawMessage) (any, error) {
	var p struct {
		ID int `json:"id"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil, errors.New("expected {\"id\": n}")
	}
	patrolSchedulesMu.Lock()
	defer patrolSchedulesMu.Unlock()
	list := a.readPatrolSchedules()
	for i, s := range list {
		if s.ID == p.ID {
			return map[string]any{"deleted": p.ID}, a.writePatrolSchedules(append(list[:i], list[i+1:]...))
		}
	}
	return nil, errors.New("no patrol schedule with this id")
}

// duePatrol returns the schedule to start at now and marks it, or nil.
func duePatrol(list []patrolSchedule, now time.Time) *patrolSchedule {
	stamp := now.Format("2006-01-02 15:04")
	for i := range list {
		s := &list[i]
		if s.Enabled && s.Time == now.Format("15:04") && len(s.Days) == 7 && s.Days[now.Weekday()] == '1' && s.Last != stamp {
			s.Last = stamp
			return s
		}
	}
	return nil
}

func (a *agent) runPatrolSchedules(ctx context.Context) {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		patrolSchedulesMu.Lock()
		list := a.readPatrolSchedules()
		due := duePatrol(list, time.Now())
		if due != nil {
			_ = a.writePatrolSchedules(list)
		}
		patrolSchedulesMu.Unlock()
		if due == nil {
			continue
		}
		a.dev.mu.Lock()
		activity, _ := a.dev.snap.State["activity"].(string)
		errCode, _ := int64Of(a.dev.snap.State["error"])
		a.dev.mu.Unlock()
		if activity != "docked" || errCode != 0 {
			log.Printf("patrol schedule %d skipped: robot %s, error %d", due.ID, activity, errCode)
			continue
		}
		params, _ := json.Marshal(map[string]any{"name": "patrol", "points": due.Points})
		if _, err := a.call(ctx, request{Method: "local.command", Params: params}); err != nil {
			log.Printf("patrol schedule %d not started: %v", due.ID, err)
		}
	}
}

package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDuePatrol(t *testing.T) {
	// 2026-11-24 is a Tuesday: index 2 of the Sunday-first digits.
	now := time.Date(2026, 11, 24, 9, 30, 10, 0, time.Local)
	list := []patrolSchedule{
		{ID: 1, Enabled: true, Time: "09:30", Days: "1000000", Points: [][]int{{0, 0}}},
		{ID: 2, Enabled: false, Time: "09:30", Days: "1111111"},
		{ID: 3, Enabled: true, Time: "09:30", Days: "0010000"},
	}
	due := duePatrol(list, now)
	if due == nil || due.ID != 3 || list[2].Last != "2026-11-24 09:30" {
		t.Fatalf("%+v", due)
	}
	if duePatrol(list, now.Add(15*time.Second)) != nil {
		t.Fatal("fired twice in one minute")
	}

	a := testAgent(t)
	if _, err := a.setPatrolSchedule(json.RawMessage(`{"enabled":true,"time":"25:00","days":"1111111","points":[[0,0]]}`), "1"); err == nil {
		t.Fatal("bad time accepted")
	}
	if err := a.writePatrolSchedules(list); err != nil {
		t.Fatal(err)
	}
	if _, err := a.deletePatrolSchedule(json.RawMessage(`{"id":2}`)); err != nil || len(a.readPatrolSchedules()) != 2 {
		t.Fatal(err, a.readPatrolSchedules())
	}
}

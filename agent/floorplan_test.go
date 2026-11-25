package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPlanWalls(t *testing.T) {
	// Two rooms sharing the wall x=100 (listed in opposite directions); a door on it from y=40 to y=60.
	var p planInfo
	err := json.Unmarshal([]byte(`{"storeys":[{"rooms":[
		{"room_id":2,"walls":[{"beg_pt_x":100,"beg_pt_y":0,"end_pt_x":100,"end_pt_y":100},{"beg_pt_x":100,"beg_pt_y":100,"end_pt_x":0,"end_pt_y":100},{"beg_pt_x":5,"beg_pt_y":5,"end_pt_x":5,"end_pt_y":5}]},
		{"room_id":1,"walls":[{"beg_pt_x":100,"beg_pt_y":100,"end_pt_x":100,"end_pt_y":0},{"beg_pt_x":100,"beg_pt_y":0,"end_pt_x":200,"end_pt_y":0}]}],
	"doors":[{"beg_pt_x":100,"beg_pt_y":40,"end_pt_x":100,"end_pt_y":60}]}]}`), &p)
	if err != nil {
		t.Fatal(err)
	}
	got := planWalls(p)
	want := []planWall{
		{[4]int{100, 0, 100, 40}, []int{1, 2}}, {[4]int{100, 60, 100, 100}, []int{1, 2}},
		{[4]int{0, 100, 100, 100}, []int{2}}, {[4]int{100, 0, 200, 0}, []int{1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if planWalls(planInfo{}) != nil {
		t.Fatal("no storeys must give no walls")
	}
}

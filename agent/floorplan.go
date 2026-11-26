package main

import "sort"

// Floor plan walls from walls_info.storeys[0], as the Dreame app's "room outline": straight
// axis-aligned segments per room, shared walls merged, a gap cut at every door.
// Robot millimetres; the app draws them shifted by half a cell (see planShift).
type planWall struct {
	Seg   [4]int `json:"seg"`   // x0, y0, x1, y1 with x0 <= x1 and y0 <= y1
	Rooms []int  `json:"rooms"` // rooms the wall belongs to, for highlighting the selection
}

type planInfo struct {
	Storeys []struct {
		Rooms []struct {
			ID    int `json:"room_id"`
			Walls []struct {
				X0 int `json:"beg_pt_x"`
				Y0 int `json:"beg_pt_y"`
				X1 int `json:"end_pt_x"`
				Y1 int `json:"end_pt_y"`
			} `json:"walls"`
		} `json:"rooms"`
		Doors []struct {
			X0 int `json:"beg_pt_x"`
			Y0 int `json:"beg_pt_y"`
			X1 int `json:"end_pt_x"`
			Y1 int `json:"end_pt_y"`
		} `json:"doors"`
	} `json:"storeys"`
}

func minMax(a, b int) (int, int) {
	if a > b {
		return b, a
	}
	return a, b
}

// planWalls merges identical walls of neighbouring rooms (either direction) and cuts door gaps.
// shortcut: only the first storey, as the app; walls that are not axis-aligned are left out.
func planWalls(p planInfo) []planWall {
	if len(p.Storeys) == 0 {
		return nil
	}
	st := p.Storeys[0]
	byKey := map[[4]int]*planWall{}
	var order [][4]int
	for _, room := range st.Rooms {
		for _, w := range room.Walls {
			x0, x1 := minMax(w.X0, w.X1)
			y0, y1 := minMax(w.Y0, w.Y1)
			if x0 != x1 && y0 != y1 || x0 == x1 && y0 == y1 {
				continue
			}
			k := [4]int{x0, y0, x1, y1}
			if byKey[k] == nil {
				byKey[k] = &planWall{Seg: k}
				order = append(order, k)
			}
			byKey[k].Rooms = append(byKey[k].Rooms, room.ID)
		}
	}
	out := []planWall{}
	for _, k := range order {
		parts := [][4]int{k}
		for _, d := range st.Doors {
			dx0, dx1 := minMax(d.X0, d.X1)
			dy0, dy1 := minMax(d.Y0, d.Y1)
			var next [][4]int
			for _, s := range parts {
				switch {
				case dx0 == dx1 && s[0] == s[2] && s[0] == dx0 && s[1] <= dy0 && s[3] >= dy1: // vertical door in a vertical wall
					next = append(next, [4]int{s[0], s[1], s[2], dy0}, [4]int{s[0], dy1, s[2], s[3]})
				case dy0 == dy1 && s[1] == s[3] && s[1] == dy0 && s[0] <= dx0 && s[2] >= dx1:
					next = append(next, [4]int{s[0], s[1], dx0, s[3]}, [4]int{dx1, s[1], s[2], s[3]})
				default:
					next = append(next, s)
				}
			}
			parts = next
		}
		rooms := byKey[k].Rooms
		sort.Ints(rooms)
		for _, s := range parts {
			if s[0] != s[2] || s[1] != s[3] { // a door at the very end leaves an empty piece
				out = append(out, planWall{Seg: s, Rooms: rooms})
			}
		}
	}
	return out
}

package main

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
)

// Furniture on the saved map, as the app's FurnitureEditPage (v2) sees it. Checked on r2505
// 2026-11-24: funiture_info rows are [id, type, room, width, height, 0, x, y, 0, rotation, 0, 0,
// scale, editType, ...] in robot millimetres, (x, y) the centre. ActMapAdv (6/2)
// {"mapfurniture_v2": [rows]} REPLACES the whole list (a one-row edit dropped the other three), so
// the agent always sends every row: kept rows as the robot stored them, plus new ones.
// The app names types 1..25; robot-found rows may carry other types and are kept untouched.

type furnitureItem struct {
	Index    int     `json:"index"`
	Type     int     `json:"type"`
	Room     int     `json:"room"`
	X        int     `json:"x"`
	Y        int     `json:"y"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Rotation float64 `json:"rotation"`
}

func (v *mapView) furniture(rows []json.RawMessage) {
	v.Furniture, v.furnitureRows = []furnitureItem{}, rows
	v.FurnitureEditable = rows != nil
	for i, raw := range rows {
		var f []float64
		if json.Unmarshal(raw, &f) != nil || len(f) < 13 {
			v.FurnitureEditable = false // unknown row shape: do not rewrite the list
			continue
		}
		v.Furniture = append(v.Furniture, furnitureItem{i, int(f[1]), int(f[2]), int(math.Round(f[6])), int(math.Round(f[7])), int(math.Abs(f[3])), int(math.Abs(f[4])), f[9]})
	}
}

func (a *agent) furniturePatch(raw json.RawMessage) (map[string]any, error) {
	var request struct {
		MapID    int    `json:"map_id"`
		Revision string `json:"revision"`
		Remove   []int  `json:"remove"`
		Add      []struct {
			Type     int `json:"type"`
			X        int `json:"x"`
			Y        int `json:"y"`
			Width    int `json:"width"`
			Height   int `json:"height"`
			Rotation int `json:"rotation"`
		} `json:"add"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil {
		return nil, errors.New("invalid furniture edit")
	}
	current, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if current.Empty || !current.FurnitureEditable || len(current.Origin) != 2 {
		return nil, errors.New("current saved map does not expose editable furniture")
	}
	if request.MapID != current.MapID || request.Revision == "" || request.Revision != current.Revision {
		return nil, errors.New("map changed; refresh before editing furniture")
	}
	drop := map[int]bool{}
	for _, i := range request.Remove {
		if i < 0 || i >= len(current.furnitureRows) || drop[i] {
			return nil, errors.New("unknown or repeated furniture index")
		}
		drop[i] = true
	}
	rows := []any{}
	for i, r := range current.furnitureRows {
		if !drop[i] {
			rows = append(rows, r)
		}
	}
	for _, f := range request.Add {
		if f.Type < 1 || f.Type > 25 || f.Width < 100 || f.Width > 4000 || f.Height < 100 || f.Height > 4000 || f.Rotation < 0 || f.Rotation >= 360 {
			return nil, errors.New("furniture needs type 1..25, width and height 100..4000 mm, rotation 0..359")
		}
		cx, cy := (f.X-current.Origin[0])/current.GridSize, (f.Y-current.Origin[1])/current.GridSize
		if f.X < current.Origin[0] || f.Y < current.Origin[1] || cx >= current.Width || cy >= current.Height {
			return nil, errors.New("furniture is outside the map")
		}
		room := int(current.Pixels[cy*current.Width+cx])
		if room == 0 || room == 255 {
			return nil, errors.New("put furniture inside a room")
		}
		// As the app's revertFurnitureToCommit; editType 1 = added by the user.
		rows = append(rows, []any{0, f.Type, room, f.Width, f.Height, 0, f.X, f.Y, 0, f.Rotation, 0, 0, 1, 1, 0})
	}
	if len(rows) > 64 {
		return nil, errors.New("at most 64 furniture items")
	}
	return map[string]any{"mapfurniture_v2": rows}, nil
}

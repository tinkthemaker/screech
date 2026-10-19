package core

import (
	"strconv"
)

// WindowPlacement is where the desktop window last sat. Like theme and
// volume it lives in meta: local presentation state, not taste data.
// Coordinates are physical screen pixels as the OS reports them, so they
// may be negative on monitors left of the primary.
type WindowPlacement struct {
	X, Y     int
	Unfolded bool
	Saved    bool // false: never saved; the shell centres the window
}

// WindowPlacement reads the gui_x/gui_y/gui_unfolded meta keys. Saved is
// true only once all three exist, so a partially written placement never
// convinces the shell it knows where to go.
func (c *Core) WindowPlacement() WindowPlacement {
	xStr, errX := c.store.GetMeta("gui_x")
	yStr, errY := c.store.GetMeta("gui_y")
	uStr, errU := c.store.GetMeta("gui_unfolded")
	if errX != nil || errY != nil || errU != nil {
		return WindowPlacement{}
	}
	if xStr == "" || yStr == "" || uStr == "" {
		return WindowPlacement{}
	}
	x, errX := strconv.Atoi(xStr)
	y, errY := strconv.Atoi(yStr)
	if errX != nil || errY != nil {
		return WindowPlacement{}
	}
	return WindowPlacement{X: x, Y: y, Unfolded: uStr == "1", Saved: true}
}

// SetWindowPlacement writes all three keys in one transaction so the
// placement is never half-saved.
func (c *Core) SetWindowPlacement(p WindowPlacement) error {
	tx, err := c.store.Begin()
	if err != nil {
		return err
	}
	s := c.store.WithTx(tx)
	unfolded := "0"
	if p.Unfolded {
		unfolded = "1"
	}
	for _, kv := range [][2]string{
		{"gui_x", strconv.Itoa(p.X)},
		{"gui_y", strconv.Itoa(p.Y)},
		{"gui_unfolded", unfolded},
	} {
		if err := s.SetMeta(kv[0], kv[1]); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

package staking

import (
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
)

func wrapBox(w, h int) layout.Widget {
	return func(_ C) D {
		return D{Size: image.Pt(w, h)}
	}
}

func TestLayoutWrapRowUnconstrainedKeepsNaturalWidth(t *testing.T) {
	ops := new(op.Ops)
	gtx := layout.Context{
		Ops: ops,
		Constraints: layout.Constraints{
			Max: image.Pt(1e6, 1000),
		},
	}
	d := layoutWrapRow(gtx, wrapBox(80, 20), wrapBox(40, 20))
	if d.Size.X != 120 {
		t.Fatalf("unconstrained width = %d, want 120 (natural). Expanding to Max.X hid values off-screen.", d.Size.X)
	}
	if d.Size.Y != 20 {
		t.Fatalf("unconstrained height = %d, want 20", d.Size.Y)
	}
}

func TestLayoutWrapRowFitsUsesSpaceBetween(t *testing.T) {
	ops := new(op.Ops)
	gtx := layout.Context{
		Ops: ops,
		Constraints: layout.Constraints{
			Max: image.Pt(400, 1000),
		},
	}
	d := layoutWrapRow(gtx, wrapBox(80, 20), wrapBox(40, 20))
	if d.Size.X != 400 {
		t.Fatalf("fitted width = %d, want 400 (space-between fills the row)", d.Size.X)
	}
}

func TestLayoutWrapRowTooWideWrapsBelow(t *testing.T) {
	ops := new(op.Ops)
	gtx := layout.Context{
		Ops: ops,
		Constraints: layout.Constraints{
			Max: image.Pt(100, 1000),
		},
	}
	d := layoutWrapRow(gtx, wrapBox(80, 20), wrapBox(80, 20))
	if d.Size.Y < 40 {
		t.Fatalf("wrapped height = %d, want at least 40 (second item below)", d.Size.Y)
	}
	if d.Size.X > 100 {
		t.Fatalf("wrapped width = %d, want <= 100", d.Size.X)
	}
}

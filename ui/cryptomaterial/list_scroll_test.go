package cryptomaterial

import (
	"image"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/monetarium/skarb-wallet/ui/values"
)

func TestScrollbarMovementRequestsFrame(t *testing.T) {
	for _, action := range []string{"track click", "drag"} {
		t.Run(action, func(t *testing.T) {
			th := &Theme{Base: material.NewTheme(), Color: (&values.Color{}).DefaultThemeColors()}
			state := &widget.List{List: layout.List{Axis: layout.Vertical}}
			list := th.List(state)
			router := new(input.Router)
			gtx := layout.Context{Ops: new(op.Ops), Source: router.Source(), Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(300, 200))}
			frame := func() {
				list.Layout(gtx, 20, func(gtx layout.Context, _ int) layout.Dimensions {
					return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 50)}
				})
				router.Frame(gtx.Ops)
				gtx.Ops.Reset()
			}
			frame()
			if action == "track click" {
				router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(295, 150)},
					pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(295, 150)})
			} else {
				router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(295, 15)})
				frame()
				router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(295, 25)})
				frame()
				router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(295, 80)},
					pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(295, 80)})
			}
			frame()
			if state.ScrollDistance() == 0 {
				t.Fatal("scrollbar did not move")
			}
			if _, ok := router.WakeupTime(); !ok {
				t.Fatal("scrollbar changed position without requesting another frame")
			}
			frame()
			if state.Position.First == 0 && state.Position.Offset == 0 {
				t.Fatal("scrollbar did not move the visible list")
			}
		})
	}
}

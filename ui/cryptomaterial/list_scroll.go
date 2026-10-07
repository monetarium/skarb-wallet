package cryptomaterial

import (
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// ScrollbarStyle configures the presentation of a scrollbar.
type ScrollbarStyle struct {
	material.ScrollbarStyle
}

// ListStyle configures the presentation of a layout.List with a scrollbar.
type ListStyle struct {
	material.ListStyle
}

func (t *Theme) Scrollbar(state *widget.Scrollbar) ScrollbarStyle {
	return ScrollbarStyle{material.Scrollbar(t.Base, state)}
}

func (t *Theme) List(state *widget.List) ListStyle {
	list := ListStyle{material.List(t.Base, state)}
	list.Indicator.Color = t.Color.Gray3
	list.Indicator.HoverColor = t.Color.Gray2
	return list
}

// Layout the list and its scrollbar.
func (l ListStyle) Layout(gtx layout.Context, length int, w layout.ListElement) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	dims := l.ListStyle.Layout(gtx, length, w)
	// Material applies scrollbar movement after drawing the list. Request
	// another frame so the last drag or track click is visible immediately.
	if l.Scrollbar.ScrollDistance() != 0 {
		gtx.Execute(op.InvalidateCmd{})
	}
	return dims
}

package ui

import (
	"image"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/ui/assets"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/notification"
)

type editorInputPage struct {
	*app.GenericPageModal
	editor cryptomaterial.Editor
}

func (*editorInputPage) OnNavigatedTo()                  {}
func (*editorInputPage) OnNavigatedFrom()                {}
func (pg *editorInputPage) HandleUserInteractions(gtx C) { pg.editor.Update(gtx) }
func (pg *editorInputPage) Layout(gtx C) D {
	return layout.Inset{Top: 16, Left: 16, Right: 16}.Layout(gtx, pg.editor.Layout)
}

func TestWindowBackdropDoesNotStealEditorFocus(t *testing.T) {
	for _, source := range []pointer.Source{pointer.Mouse, pointer.Touch} {
		t.Run(source.String(), func(t *testing.T) {
			th := cryptomaterial.NewTheme(assets.FontCollection(), assets.DecredIcons, false)
			pg := &editorInputPage{GenericPageModal: app.NewGenericPageModal("input-test"), editor: th.Editor(new(widget.Editor), "")}
			nav := app.NewSimpleWindowNavigator(func() {})
			nav.Display(pg)
			win := &Window{navigator: nav, load: &load.Load{Theme: th, Toast: notification.NewToast(th)}}
			router := new(input.Router)
			gtx := layout.Context{Ops: new(op.Ops), Source: router.Source(), Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(300, 100))}
			frame := func() {
				pg.HandleUserInteractions(gtx)
				win.prepareToDisplayUI(gtx)
				router.Frame(gtx.Ops)
				gtx.Ops.Reset()
			}
			frame()
			router.Queue(pointer.Event{Kind: pointer.Press, Source: source, Buttons: pointer.ButtonPrimary, Position: f32.Pt(100, 40)},
				pointer.Event{Kind: pointer.Release, Source: source, Position: f32.Pt(100, 40)})
			frame()
			frame()
			if !router.Source().Focused(pg.editor.Editor) {
				t.Fatal("window backdrop stole focus from the clicked editor")
			}
			router.Queue(key.EditEvent{Text: "test word"})
			frame()
			if pg.editor.Editor.Text() != "test word" {
				t.Fatal("editor could not receive text after a primary click")
			}
		})
	}
}

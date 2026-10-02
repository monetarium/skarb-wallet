package cryptomaterial

import (
	"image"
	"runtime"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/monetarium/skarb-wallet/ui/values"
)

func TestLatinShortcut(t *testing.T) {
	cases := map[string]string{
		"Ф": "A",
		"С": "C",
		"М": "V",
		"Ч": "X",
		"Я": "Z",
	}
	for got, want := range cases {
		latin, ok := latinShortcut(got)
		if !ok || latin != want {
			t.Fatalf("%s: got %q ok=%v, want %q", got, latin, ok, want)
		}
	}
	if _, ok := latinShortcut("V"); ok {
		t.Fatal("latin V must stay with widget.Editor")
	}
}

func TestEditorContextMenuClick(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "linux"} {
		secondary := pointer.Event{Source: pointer.Mouse, Buttons: pointer.ButtonSecondary}
		if !editorContextMenuClick(secondary, goos) {
			t.Fatalf("%s: right click must open the menu", goos)
		}
		primary := pointer.Event{Source: pointer.Mouse, Buttons: pointer.ButtonPrimary}
		if editorContextMenuClick(primary, goos) {
			t.Fatalf("%s: plain primary click must not open a context menu", goos)
		}
		primary.Modifiers = key.ModCtrl
		if got := editorContextMenuClick(primary, goos); got != (goos == "darwin") {
			t.Fatalf("%s: Control+click context menu = %v", goos, got)
		}
	}
}

func TestControlClickShowsPasteMenu(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Control+click is a macOS context menu gesture")
	}
	th := &Theme{Base: material.NewTheme(), Color: (&values.Color{}).DefaultThemeColors(), Styles: values.DefaultWidgetStyles()}
	editor := th.Editor(new(widget.Editor), "")
	router := new(input.Router)
	gtx := layout.Context{Ops: new(op.Ops), Source: router.Source(), Now: time.Now(), Constraints: layout.Exact(image.Pt(300, 70))}
	editor.Layout(gtx)
	router.Frame(gtx.Ops)
	gtx.Ops.Reset()
	router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Modifiers: key.ModCtrl, Position: f32.Pt(20, 20)})
	editor.Layout(gtx)
	if !editor.isShowMenu {
		t.Fatal("Control+click did not show Paste")
	}
	editor.paste.Click()
	editor.Update(gtx)
	if !router.ClipboardRequested() || editor.isShowMenu {
		t.Fatal("Paste must request clipboard data and close the menu")
	}
	editor.isShowMenu = true
	editor.Layout(gtx.Disabled())
	if editor.isShowMenu {
		t.Fatal("disabled editor left its menu visible behind a modal")
	}
}

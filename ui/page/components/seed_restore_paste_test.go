package components

import (
	"fmt"
	"image"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/clipboard"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/monetarium/skarb-wallet/app"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	libutils "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/assets"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/values"
)

func TestPastedSeedWords(t *testing.T) {
	words := pastedSeedWords("  abandon  ability\nabout  ")
	if len(words) != 3 || words[0] != "abandon" || words[1] != "ability" || words[2] != "about" {
		t.Fatalf("got %#v", words)
	}
	words = pastedSeedWords("1. abandon 2) ability\n3.about")
	if len(words) != 3 || words[0] != "abandon" || words[1] != "ability" || words[2] != "about" {
		t.Fatalf("numbered paste got %#v", words)
	}
	if got := pastedSeedWords("abandon"); len(got) != 1 {
		t.Fatalf("single word must stay one field, got %#v", got)
	}
	if got := pastedSeedWords("   "); len(got) != 0 {
		t.Fatalf("blank paste got %#v", got)
	}
}

func seedRestoreTestPage(seedType sharedW.WordSeedType) (*SeedRestore, C, *input.Router) {
	l := &load.Load{
		AppInfo: &load.AppInfo{},
		Theme:   cryptomaterial.NewTheme(assets.FontCollection(), assets.DecredIcons, false),
	}
	pg := NewSeedRestorePage(l, "test", libutils.DCRWalletAsset, nil, func() sharedW.WordSeedType { return seedType })
	router := new(input.Router)
	gtx := layout.Context{
		Ops: new(op.Ops), Source: router.Source(), Now: time.Now(),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1000, 700)),
	}
	pg.HandleUserInteractions(gtx)
	pg.Layout(gtx)
	router.Frame(gtx.Ops)
	gtx.Ops.Reset()
	return pg, gtx, router
}

func TestClipboardSeedDistributedBeforeLayout(t *testing.T) {
	for _, seedType := range []sharedW.WordSeedType{sharedW.WordSeed12, sharedW.WordSeed24, sharedW.WordSeed33} {
		t.Run(strconv.Itoa(seedType.ToInt()), func(t *testing.T) {
			pg, gtx, router := seedRestoreTestPage(seedType)
			words := seedType.AllWords()[:seedType.ToInt()]
			router.Source().Execute(clipboard.ReadCmd{Tag: pg.seedEditors.editors[0].Edit.Editor})
			router.Queue(transfer.DataEvent{Type: "application/text", Open: func() io.ReadCloser {
				return io.NopCloser(strings.NewReader(strings.Join(words, " ")))
			}})

			// Only deliver the clipboard event: no mouse move or extra frame.
			pg.HandleUserInteractions(gtx)
			for i, word := range words {
				if got := pg.seedEditors.editors[i].Edit.Editor.Text(); got != word {
					t.Fatalf("field %d: got %q, want %q", i+1, got, word)
				}
			}
			if pg.openPopupIndex != -1 {
				t.Fatal("full phrase paste left a suggestion menu open")
			}
			pg.Layout(gtx)
			router.Frame(gtx.Ops)
			gtx.Ops.Reset()
			pg.HandleUserInteractions(gtx)
			if pg.openPopupIndex != -1 {
				t.Fatal("programmatic field changes reopened suggestions")
			}
		})
	}
}

func TestValidateClosesSuggestionsBeforeErrorModal(t *testing.T) {
	pg, gtx, _ := seedRestoreTestPage(sharedW.WordSeed12)
	pg.SetParentNav(app.NewSimpleWindowNavigator(func() {}))
	for _, editor := range pg.seedEditors.editors[:12] {
		editor.Edit.Editor.SetText("abandon") // Valid words, invalid checksum.
	}
	pg.openPopupIndex = 0
	pg.seedEditors.focusIndex = 0
	pg.validateSeed.Click()
	pg.HandleUserInteractions(gtx)
	if pg.window.TopModal() == nil {
		t.Fatal("expected invalid seed modal")
	}
	if pg.openPopupIndex != -1 || pg.seedEditors.focusIndex != -1 || len(pg.suggestions) != 0 {
		t.Fatal("suggestions remained open behind the validation modal")
	}
}

func TestClearAllAfterSeedEntry(t *testing.T) {
	for _, source := range []pointer.Source{pointer.Mouse, pointer.Touch} {
		for _, seedType := range []sharedW.WordSeedType{sharedW.WordSeed12, sharedW.WordSeed24, sharedW.WordSeed33} {
			for _, scenario := range []string{"first", "other", "typed"} {
				t.Run(fmt.Sprintf("%v/%d/%s", source, seedType.ToInt(), scenario), func(t *testing.T) {
					pg, gtx, router := seedRestoreTestPage(seedType)
					frame := func() {
						pg.HandleUserInteractions(gtx)
						pg.Layout(gtx)
						router.Frame(gtx.Ops)
						gtx.Ops.Reset()
					}
					words := seedType.AllWords()[:seedType.ToInt()]
					if scenario == "typed" {
						for i, word := range words {
							router.Source().Execute(key.FocusCmd{Tag: pg.seedEditors.editors[i].Edit.Editor})
							router.Queue(key.EditEvent{Text: word})
							frame()
						}
					} else {
						i := 0
						if scenario == "other" {
							i = 4
						}
						router.Source().Execute(clipboard.ReadCmd{Tag: pg.seedEditors.editors[i].Edit.Editor})
						router.Queue(transfer.DataEvent{Type: "application/text", Open: func() io.ReadCloser {
							return io.NopCloser(strings.NewReader(strings.Join(words, " ")))
						}})
						frame()
					}
					if !pg.resetSeedFields.Enabled() {
						t.Fatal("Clear all is disabled with text in the form")
					}
					// Click the real button through the input router, including a tap
					// whose press and release arrive in the same frame.
					y := float32(15 + ((seedType.ToInt()+4)/5)*45 + 15)
					router.Queue(pointer.Event{Kind: pointer.Press, Source: source, Buttons: pointer.ButtonPrimary, Position: f32.Pt(45, y)},
						pointer.Event{Kind: pointer.Release, Source: source, Position: f32.Pt(45, y)})
					frame()
					frame()
					for i, editor := range pg.seedEditors.editors {
						if editor.Edit.Editor.Text() != "" {
							t.Fatalf("field %d remained after Clear all", i+1)
						}
					}
					frame()
					if pg.resetSeedFields.Enabled() || pg.openPopupIndex != -1 {
						t.Fatal("clear left stale button/menu state")
					}
				})
			}
		}
	}
}

func TestRestoreWordFieldClickAndTyping(t *testing.T) {
	for _, source := range []pointer.Source{pointer.Mouse, pointer.Touch} {
		t.Run(fmt.Sprint(source), func(t *testing.T) {
			l := &load.Load{AppInfo: &load.AppInfo{}, Theme: cryptomaterial.NewTheme(assets.FontCollection(), assets.DecredIcons, false)}
			pg := NewRestorePage(l, "test", libutils.DCRWalletAsset, nil)
			pg.OnAttachedToNavigator(app.NewSimpleWindowNavigator(func() {}))
			pg.OnNavigatedTo()
			pg.seedTypeDropdown.SetSelectedValue(values.String(values.Str12WordSeed))
			pg.seedRestorePage.hideSeedMenus() // Reproduce focus lost after changing seed type.
			router := new(input.Router)
			gtx := layout.Context{Ops: new(op.Ops), Source: router.Source(), Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1200, 832))}
			frame := func() { pg.HandleUserInteractions(gtx); pg.Layout(gtx); router.Frame(gtx.Ops); gtx.Ops.Reset() }
			frame()

			router.Queue(pointer.Event{Kind: pointer.Press, Source: source, Buttons: pointer.ButtonPrimary, Position: f32.Pt(150, 182)},
				pointer.Event{Kind: pointer.Release, Source: source, Position: f32.Pt(150, 182)})
			frame()

			frame()
			router.Queue(key.EditEvent{Text: "abandon"})
			frame()
			if got := pg.seedRestorePage.seedEditors.editors[0].Edit.Editor.Text(); got != "abandon" {
				t.Fatalf("click then type: got %q", got)
			}
		})
	}
}

func TestPasteSeedWordsKeepsBulkEditorFocus(t *testing.T) {
	l := &load.Load{AppInfo: &load.AppInfo{}, Theme: cryptomaterial.NewTheme(assets.FontCollection(), assets.DecredIcons, false)}
	pg := NewRestorePage(l, "test", libutils.DCRWalletAsset, nil)
	pg.OnAttachedToNavigator(app.NewSimpleWindowNavigator(func() {}))
	pg.OnNavigatedTo()
	pg.toggleSeedInput.SetChecked(true)
	router := new(input.Router)
	gtx := layout.Context{Ops: new(op.Ops), Source: router.Source(), Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1200, 832))}
	frame := func() { pg.HandleUserInteractions(gtx); pg.Layout(gtx); router.Frame(gtx.Ops); gtx.Ops.Reset() }
	frame()
	// A pending focus request from the hidden first-word field must not
	// steal focus during the next interaction pass.
	router.Source().Execute(key.FocusCmd{Tag: pg.seedInputEditor.Editor})
	phrase := strings.TrimSpace(strings.Repeat("abandon ", 11) + "about")
	router.Source().Execute(clipboard.ReadCmd{Tag: pg.seedInputEditor.Editor})
	router.Queue(transfer.DataEvent{Type: "application/text", Open: func() io.ReadCloser { return io.NopCloser(strings.NewReader(phrase)) }})
	pg.HandleUserInteractions(gtx)
	if !gtx.Source.Focused(pg.seedInputEditor.Editor) {
		t.Fatal("hidden word editor stole focus")
	}
	if pg.seedInputEditor.Editor.Text() != phrase || !pg.confirmSeedButton.Enabled() {
		t.Fatal("bulk paste was not processed before layout")
	}
	if len(pg.KeysToHandle()) != 0 {
		t.Fatal("hidden word fields still handle bulk editor navigation")
	}
	pg.seedInputEditor.Editor.SetText("")
	pg.HandleUserInteractions(gtx)
	if pg.confirmSeedButton.Enabled() {
		t.Fatal("empty bulk editor left Validate enabled")
	}
}

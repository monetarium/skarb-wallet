package components

import (
	"image"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/io/input"
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

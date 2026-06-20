package components

import (
	"errors"
	"sync/atomic"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/monetarium/skarb-wallet/app"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	libutils "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/utils"
	"github.com/monetarium/skarb-wallet/ui/values"
)

const (
	CreateWalletID    = "create_wallet"
	defaultWalletName = "myWallet"
)

type walletAction struct {
	title     string
	clickable *cryptomaterial.Clickable
	border    cryptomaterial.Border
	width     unit.Dp
}

type CreateWallet struct {
	*load.Load
	// GenericPageModal defines methods such as ID() and OnAttachedToNavigator()
	// that helps this Page satisfy the app.Page interface. It also defines
	// helper methods for accessing the PageNavigator that displayed this page
	// and the root WindowNavigator.
	*app.GenericPageModal

	scrollContainer *widget.List
	list            layout.List

	walletActions []*walletAction

	assetTypeDropdown     *cryptomaterial.DropDown
	assetTypeError        cryptomaterial.Label
	walletName            cryptomaterial.Editor
	watchOnlyWalletHex    cryptomaterial.Editor
	passwordEditor        cryptomaterial.Editor
	confirmPasswordEditor cryptomaterial.Editor
	watchOnlyCheckBox     cryptomaterial.CheckBoxStyle
	materialLoader        material.LoaderStyle
	seedTypeDropdown      *cryptomaterial.DropDown

	continueBtn cryptomaterial.Button
	restoreBtn  cryptomaterial.Button
	importBtn   cryptomaterial.Button
	backButton  cryptomaterial.IconButton

	selectedWalletAction int

	walletCreationSuccessCallback func(newWallet sharedW.Asset)

	// showLoader / isLoading are written by the create/import goroutines and
	// read by Layout every frame — atomic per CLAUDE.md §3. Editor errors
	// produced on those goroutines are staged (stagedNameErr/stagedXpubErr +
	// pendingErrApply) and applied to the editors in HandleUserInteractions on
	// the UI thread, since Editor.SetError mutates state Layout reads.
	showLoader atomic.Bool
	isLoading  atomic.Bool

	stagedNameErr   string
	stagedXpubErr   string
	pendingErrApply atomic.Bool

	// createdWallet + pendingCreateSuccess move the wallet-created success
	// callback OFF the create/import goroutine and onto the UI thread. That
	// callback navigates into the seed-backup → verify-seed flow; doing the
	// navigation from the goroutine (CLAUDE.md §3) races the UI thread and can
	// drop the push to the backup flow, so the verify step never appears and a
	// freshly created wallet looks like it was created without any seed check.
	createdWallet        sharedW.Asset
	pendingCreateSuccess atomic.Bool
}

func NewCreateWallet(l *load.Load, walletCreationSuccessCallback func(newWallet sharedW.Asset), assetType ...libutils.AssetType) *CreateWallet {
	pg := &CreateWallet{
		GenericPageModal: app.NewGenericPageModal(CreateWalletID),
		scrollContainer: &widget.List{
			List: layout.List{
				Axis:      layout.Vertical,
				Alignment: layout.Middle,
			},
		},
		assetTypeDropdown: NewAssetTypeDropDown(l),
		list:              layout.List{Axis: layout.Vertical},

		continueBtn:          l.Theme.Button(values.String(values.StrContinue)),
		restoreBtn:           l.Theme.Button(values.String(values.StrRestore)),
		importBtn:            l.Theme.Button(values.String(values.StrImport)),
		watchOnlyCheckBox:    l.Theme.CheckBox(new(widget.Bool), values.String(values.StrImportWatchingOnlyWallet)),
		selectedWalletAction: -1,
		assetTypeError:       l.Theme.Body1(""),

		Load:                          l,
		walletCreationSuccessCallback: walletCreationSuccessCallback,
	}

	if walletCreationSuccessCallback == nil {
		pg.walletCreationSuccessCallback = func(_ sharedW.Asset) {
			pg.ParentNavigator().CloseCurrentPage()
		}
	}

	if len(assetType) > 0 {
		pg.assetTypeDropdown.SetSelectedValue(assetType[0].String())
	}

	pg.walletName = l.Theme.Editor(new(widget.Editor), values.String(values.StrEnterWalletName))
	pg.walletName.Editor.SingleLine, pg.walletName.Editor.Submit = true, true

	pg.watchOnlyWalletHex = l.Theme.Editor(new(widget.Editor), values.String(values.StrExtendedPubKey))
	pg.watchOnlyWalletHex.Editor.SingleLine, pg.watchOnlyWalletHex.Editor.Submit, pg.watchOnlyWalletHex.IsTitleLabel = false, true, false

	pg.passwordEditor = l.Theme.EditorPassword(new(widget.Editor), values.String(values.StrSpendingPassword))
	pg.passwordEditor.Editor.SingleLine, pg.passwordEditor.Editor.Submit = true, true

	pg.confirmPasswordEditor = l.Theme.EditorPassword(new(widget.Editor), values.String(values.StrConfirmSpendingPassword))
	pg.confirmPasswordEditor.Editor.SingleLine, pg.confirmPasswordEditor.Editor.Submit = true, true

	pg.materialLoader = material.Loader(l.Theme.Base)

	defaultWordSeedType := &cryptomaterial.DropDownItem{
		Text: values.String(values.Str12WordSeed),
	}

	// MarginPadding180 (was 130) so the full Ukrainian seed-type label
	// ("Сід із 33 слів") fits instead of being clipped mid-word.
	pg.seedTypeDropdown = pg.Theme.NewCommonDropDown(GetWordSeedTypeDropdownItems(), defaultWordSeedType, values.MarginPadding180, values.TxDropdownGroup, false)

	pg.backButton = GetBackButton(l)

	return pg
}

// NewAssetTypeDropDown creates a new asset type drop down component.
//
// The displayed Text for each asset is the user-facing label from
// AssetType.DisplayName(), NOT the internal asset-type ID
// (AssetType.String() returns "DCR" — a Decred-fork artefact that
// confuses Monetarium users who see "VAR & SKA" branding everywhere
// else). Critical: any code that maps the dropdown's Selected() value
// back to an AssetType must go through ParseAssetTypeDisplayName, not
// libutils.AssetType(Selected()) — otherwise it'll get the display
// label "VAR & SKA" and silently fail any equality check against
// DCRWalletAsset.
func NewAssetTypeDropDown(l *load.Load) *cryptomaterial.DropDown {
	items := []cryptomaterial.DropDownItem{}

	for _, assType := range l.AssetsManager.AllAssetTypes() {
		item := cryptomaterial.DropDownItem{
			Text: assType.DisplayName(),
			Icon: l.Theme.AssetIcon(assType),
		}
		items = append(items, item)
	}

	assetTypeDropdown := l.Theme.NewCommonDropDown(items, nil, values.MarginPadding340, values.AssetTypeDropdownGroup, false)
	return assetTypeDropdown
}

// OnNavigatedTo is called when the page is about to be displayed and
// may be used to initialize page features that are only relevant when
// the page is displayed.
// Part of the load.Page interface.
func (pg *CreateWallet) OnNavigatedTo() {
	pg.showLoader.Store(false)
	pg.initPageItems()
}

func (pg *CreateWallet) initPageItems() {
	radius := cryptomaterial.CornerRadius{
		TopRight:    8,
		BottomRight: 8,
		TopLeft:     8,
		BottomLeft:  8,
	}

	walletActions := []*walletAction{
		{
			title:     values.String(values.StrNewWallet),
			clickable: pg.Theme.NewClickable(true),
			border: cryptomaterial.Border{
				Radius: radius,
				Color:  pg.Theme.Color.DefaultThemeColors().White,
				Width:  values.MarginPadding2,
			},
			width: values.MarginPadding110,
		},
		{
			title:     values.String(values.StrRestoreExistingWallet),
			clickable: pg.Theme.NewClickable(true),
			border: cryptomaterial.Border{
				Radius: radius,
				Color:  pg.Theme.Color.DefaultThemeColors().White,
				Width:  values.MarginPadding2,
			},
			width: values.MarginPadding195,
		},
	}

	pg.walletActions = walletActions
}

// OnNavigatedFrom is called when the page is about to be removed from
// the displayed window. This method should ideally be used to disable
// features that are irrelevant when the page is NOT displayed.
// NOTE: The page may be re-displayed on the app's window, in which case
// OnNavigatedTo() will be called again. This method should not destroy UI
// components unless they'll be recreated in the OnNavigatedTo() method.
// Part of the load.Page interface.
func (pg *CreateWallet) OnNavigatedFrom() {}

// Layout draws the page UI components into the provided C
// to be eventually drawn on screen.
// Part of the load.Page interface.
func (pg *CreateWallet) Layout(gtx C) D {
	pg.handleEditorEvents(gtx)
	return cryptomaterial.UniformPadding(gtx, func(gtx layout.Context) layout.Dimensions {
		return cryptomaterial.LinearLayout{
			Width:     cryptomaterial.MatchParent,
			Height:    cryptomaterial.MatchParent,
			Direction: layout.Center,
		}.Layout2(gtx, func(gtx C) D {
			width := values.MarginPadding377
			if pg.IsMobileView() {
				width = pg.Load.CurrentAppWidth()
			}
			return cryptomaterial.LinearLayout{
				Width:     gtx.Dp(width),
				Height:    cryptomaterial.MatchParent,
				Alignment: layout.Middle,
			}.Layout2(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Top: values.MarginPadding20}.Layout(gtx, func(gtx C) D {
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(pg.backButton.Layout),
								layout.Rigid(layout.Spacer{Width: values.MarginPadding10}.Layout),
								layout.Rigid(func(gtx C) D {
									lbl := pg.Theme.H6(values.String(values.StrCreateWallet))
									lbl.TextSize = values.TextSizeTransform(pg.IsMobileView(), values.TextSize20)
									return lbl.Layout(gtx)
								}),
							)
						})
					}),
					layout.Rigid(func(gtx C) D {
						return pg.Theme.List(pg.scrollContainer).Layout(gtx, 1, func(gtx C, _ int) D {
							return layout.Inset{
								Top:   values.MarginPadding26,
								Right: values.MarginPadding20,
							}.Layout(gtx, func(gtx C) D {
								return layout.Stack{}.Layout(gtx,
									layout.Expanded(pg.walletOptions),
									layout.Expanded(pg.walletTypeSection),
								)
							})
						})
					}),
				)
			})
		})
	}, pg.IsMobileView())
}

func (pg *CreateWallet) walletTypeSection(gtx C) D {
	return layout.Flex{Axis: layout.Vertical, Spacing: layout.SpaceBetween}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical, Spacing: layout.SpaceBetween}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					titleLabel := pg.Theme.Label(values.TextSize16, values.String(values.StrSelectAssetType))
					titleLabel.Font.Weight = font.Bold
					return titleLabel.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					pg.assetTypeDropdown.Width = values.MarginPaddingTransform(pg.IsMobileView(), values.MarginPadding340)
					return layout.Inset{Top: values.MarginPadding10}.Layout(gtx, func(gtx C) D {
						return pg.assetTypeDropdown.Layout(gtx)
					})
				}),
			)
		}),
	)
}

func (pg *CreateWallet) walletOptions(gtx C) D {
	return layout.Inset{Top: values.MarginPadding90}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				list := layout.List{}
				return list.Layout(gtx, len(pg.walletActions), func(gtx C, i int) D {
					item := pg.walletActions[i]

					// set selected item background color
					col := pg.Theme.Color.Surface
					title := pg.Theme.Label(values.TextSizeTransform(pg.IsMobileView(), values.TextSize16), item.title)
					title.Color = pg.Theme.Color.Gray1

					radius := cryptomaterial.Radius(8)
					borderColor := pg.Theme.Color.LightGray
					item.border = cryptomaterial.Border{
						Radius: radius,
						Color:  borderColor,
						Width:  values.MarginPadding2,
					}

					if pg.selectedWalletAction == i {
						col = pg.Theme.Color.LightGray
						title.Color = pg.Theme.Color.Primary

						item.border.Color = pg.Theme.Color.Primary
					}

					if item.clickable.IsHovered() {
						item.border.Color = pg.Theme.Color.Gray1
						title.Color = pg.Theme.Color.Gray1
					}

					return layout.Inset{
						Right: values.MarginPadding8,
					}.Layout(gtx, func(gtx C) D {
						return cryptomaterial.LinearLayout{
							Width:       gtx.Dp(item.width),
							Height:      cryptomaterial.WrapContent,
							Orientation: layout.Vertical,
							Alignment:   layout.Middle,
							Direction:   layout.Center,
							Background:  col,
							Clickable:   item.clickable,
							Border:      item.border,
							Padding:     layout.UniformInset(values.MarginPadding12),
							Margin:      layout.Inset{Bottom: values.MarginPadding15},
						}.Layout2(gtx, title.Layout)
					})
				})
			}),
			layout.Rigid(func(gtx C) D {
				switch pg.selectedWalletAction {
				case 0:
					return pg.createNewWallet(gtx)
				case 1:
					return pg.restoreWallet(gtx)
				default:
					return D{}
				}
			}),
		)
	})
}

func (pg *CreateWallet) createNewWallet(gtx C) D {
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(layout.Spacer{Height: values.MarginPadding50}.Layout),
				layout.Rigid(layout.Spacer{Height: values.MarginPadding14}.Layout),
				layout.Rigid(pg.walletName.Layout),
				layout.Rigid(layout.Spacer{Height: values.MarginPadding24}.Layout),
				layout.Rigid(pg.passwordEditor.Layout),
				layout.Rigid(layout.Spacer{Height: values.MarginPadding24}.Layout),
				layout.Rigid(pg.confirmPasswordEditor.Layout),
				layout.Rigid(layout.Spacer{Height: values.MarginPadding24}.Layout),
				layout.Rigid(func(gtx C) D {
					return layout.Flex{}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D {
							return layout.E.Layout(gtx, func(gtx C) D {
								if pg.isLoading.Load() {
									gtx.Constraints.Max.X = gtx.Dp(values.MarginPadding20)
									gtx.Constraints.Min.X = gtx.Constraints.Max.X
									return pg.materialLoader.Layout(gtx)
								}
								return pg.continueBtn.Layout(gtx)
							})
						}),
					)
				}),
			)
		}),
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			textSize16 := values.TextSizeTransform(pg.IsMobileView(), values.TextSize16)
			return layout.Flex{Alignment: layout.Middle,
				Spacing: layout.SpaceBetween}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: values.MarginPadding15}.Layout(gtx, pg.Theme.Label(textSize16, values.String(values.StrWordSeedType)).Layout)
				}),
				layout.Rigid(pg.seedTypeDropdown.Layout),
			)
		}),
	)
}

func (pg *CreateWallet) restoreWallet(gtx C) D {
	textSize16 := values.TextSizeTransform(pg.IsMobileView(), values.TextSize16)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(pg.Theme.Label(textSize16, values.String(values.StrExistingWalletName)).Layout),
		layout.Rigid(pg.watchOnlyCheckBox.Layout),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{
				Top:    values.MarginPadding14,
				Bottom: values.MarginPadding20,
			}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(pg.walletName.Layout),
					layout.Rigid(func(gtx C) D {
						if !pg.watchOnlyCheckBox.CheckBox.Value {
							return D{}
						}
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx C) D {
								return layout.Inset{
									Top:    values.MarginPadding10,
									Bottom: values.MarginPadding8,
								}.Layout(gtx, pg.Theme.Label(textSize16, values.String(values.StrExtendedPubKey)).Layout)
							}),
							layout.Rigid(pg.watchOnlyWalletHex.Layout),
						)
					}),
				)
			})
		}),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					return layout.E.Layout(gtx, func(gtx C) D {
						if pg.showLoader.Load() {
							loader := material.Loader(pg.Theme.Base)
							loader.Color = pg.Theme.Color.Gray1
							return loader.Layout(gtx)
						}
						if pg.watchOnlyCheckBox.CheckBox.Value {
							return pg.importBtn.Layout(gtx)
						}
						return pg.restoreBtn.Layout(gtx)
					})
				}),
			)
		}),
	)
}

func (pg *CreateWallet) handleEditorEvents(gtx C) {
	isSubmit, isChanged := cryptomaterial.HandleEditorEvents(gtx, &pg.watchOnlyWalletHex, &pg.walletName, &pg.passwordEditor, &pg.confirmPasswordEditor)
	if isChanged {
		// reset error when any editor is modified
		pg.walletName.SetError("")
		pg.passwordEditor.SetError("")
		pg.confirmPasswordEditor.SetError("")
		pg.watchOnlyWalletHex.SetError("")
	}

	// Enter (an editor Submit event) is routed to the action for the CURRENTLY
	// selected mode only. Previously isSubmit fired BOTH the create branch
	// (which then errored on the empty password fields) AND the watch-only
	// import branch while restoring, so pressing Enter on the restore screen did
	// the wrong thing. Now: create mode → Continue; restore mode → Restore (or
	// Import for a watch-only wallet). restoreBtn is handled in
	// HandleUserInteractions, so translate Enter into a button click it picks up.
	if isSubmit {
		switch pg.selectedWalletAction {
		case 0: // create new wallet
			pg.continueBtn.Click()
		case 1: // restore / import existing wallet
			if pg.watchOnlyCheckBox.CheckBox.Value {
				pg.importBtn.Click()
			} else {
				pg.restoreBtn.Click()
			}
		}
		pg.ParentWindow().Reload()
	}

	// create wallet action
	if pg.continueBtn.Clicked(gtx) {
		valid := pg.validCreateWalletInputs()
		log.Infof("CreateWallet: continue clicked action=%d valid=%v name=%q",
			pg.selectedWalletAction, valid, pg.walletName.Editor.Text())
		if valid {
			if pg.checkWalletNameExists() {
				return
			}
			go pg.createWallet()
		}
	}

	// imported (watch-only) wallet click action control
	if pg.importBtn.Clicked(gtx) && pg.validRestoreWalletInputs() {
		pg.showLoader.Store(true)
		var err error
		var newWallet sharedW.Asset
		go func() {
			// Selected() is the display name ("VAR & SKA"); map it back to the
			// asset ID. The old strings.ToLower(Selected()) compared against
			// "dcr", never matched, and fell through to a nil-wallet success
			// callback (same bug fixed in createWallet).
			switch libutils.ParseAssetTypeDisplayName(pg.assetTypeDropdown.Selected()) {
			case libutils.DCRWalletAsset:
				var walletWithXPub int
				walletWithXPub, err = pg.AssetsManager.DCRWalletWithXPub(pg.watchOnlyWalletHex.Editor.Text())
				if walletWithXPub == -1 {
					newWallet, err = pg.AssetsManager.CreateNewDCRWatchOnlyWallet(pg.walletName.Editor.Text(), pg.watchOnlyWalletHex.Editor.Text())
				} else {
					err = errors.New(values.String(values.StrXpubWalletExist))
				}
			default:
				err = errors.New(values.String(values.StrNotSupported))
			}

			if err != nil {
				// Stage the error for the UI thread — Editor.SetError mutates
				// state Layout reads, so it must not run on this goroutine.
				if err.Error() == libutils.ErrExist {
					pg.stagedXpubErr = values.StringF(values.StrWalletExist, pg.walletName.Editor.Text())
				} else {
					pg.stagedXpubErr = err.Error()
				}
				pg.pendingErrApply.Store(true)
				pg.showLoader.Store(false)
				pg.ParentWindow().Reload()
				return
			}
			// Fire the success callback on the UI thread (see createdWallet).
			pg.createdWallet = newWallet
			pg.pendingCreateSuccess.Store(true)
			pg.showLoader.Store(false)
			pg.ParentWindow().Reload()
		}()
	}
}

func (pg *CreateWallet) createWallet() {
	defer func() {
		pg.isLoading.Store(false)
	}()
	pg.isLoading.Store(true)
	walletName := pg.walletName.Editor.Text()
	pass := pg.passwordEditor.Editor.Text()
	seedType := GetWordSeedType(pg.seedTypeDropdown.Selected())
	var newWallet sharedW.Asset
	var err error
	// assetTypeDropdown.Selected() returns the DISPLAY NAME ("VAR & SKA"), not
	// the asset ID ("DCR"). Map it back via ParseAssetTypeDisplayName — the
	// previous `switch strings.ToLower(Selected())` compared the display name
	// against DCRWalletAsset.ToStringLower() ("dcr"), never matched, fell
	// through with newWallet=nil/err=nil, and then fired the success callback
	// with a nil wallet — which closed the page back to home WITHOUT creating
	// anything and WITHOUT an error. That was the "Continue throws to home" bug.
	switch libutils.ParseAssetTypeDisplayName(pg.assetTypeDropdown.Selected()) {
	case libutils.DCRWalletAsset:
		newWallet, err = pg.AssetsManager.CreateNewDCRWallet(walletName, pass, sharedW.PassphraseTypePass, seedType)
		if err != nil {
			if err.Error() == libutils.ErrExist {
				// Stage for the UI thread (this runs on the create goroutine).
				pg.stagedNameErr = values.StringF(values.StrWalletExist, walletName)
				pg.pendingErrApply.Store(true)
				pg.ParentWindow().Reload()
				return
			}

			errModal := modal.NewErrorModal(pg.Load, err.Error(), modal.DefaultClickFunc())
			pg.ParentWindow().ShowModal(errModal)
			return
		}
	default:
		log.Errorf("CreateWallet: unrecognised asset type %q from dropdown", pg.assetTypeDropdown.Selected())
		errModal := modal.NewErrorModal(pg.Load, values.String(values.StrNotSupported), modal.DefaultClickFunc())
		pg.ParentWindow().ShowModal(errModal)
		return
	}

	log.Infof("CreateWallet: created wallet name=%q id=%d", walletName, newWallet.GetWalletID())
	// Stage the success for the UI thread — the callback navigates into the
	// seed-backup/verify flow and must not run on this goroutine (see
	// createdWallet doc; same reason the staged errors above don't).
	pg.createdWallet = newWallet
	pg.pendingCreateSuccess.Store(true)
	pg.ParentWindow().Reload()
}

// HandleUserInteractions is called just before Layout() to determine
// if any user interaction recently occurred on the page and may be
// used to update the page's UI components shortly before they are
// displayed.
// Part of the load.Page interface.
func (pg *CreateWallet) HandleUserInteractions(gtx C) {
	// Apply errors staged by the create/import goroutines on the UI thread
	// (Editor.SetError mutates state Layout reads — see CLAUDE.md §3).
	if pg.pendingErrApply.CompareAndSwap(true, false) {
		if pg.stagedNameErr != "" {
			pg.walletName.SetError(pg.stagedNameErr)
			pg.stagedNameErr = ""
		}
		if pg.stagedXpubErr != "" {
			pg.watchOnlyWalletHex.SetError(pg.stagedXpubErr)
			pg.stagedXpubErr = ""
		}
	}

	// Fire the wallet-created success callback on the UI thread (staged by the
	// create/import goroutine). It navigates into the seed-backup → verify-seed
	// flow, which must not be driven from the goroutine — see createdWallet.
	if pg.pendingCreateSuccess.CompareAndSwap(true, false) {
		w := pg.createdWallet
		pg.createdWallet = nil
		if pg.walletCreationSuccessCallback != nil {
			pg.walletCreationSuccessCallback(w)
		}
	}

	// back button action
	if pg.backButton.Button.Clicked(gtx) {
		pg.ParentNavigator().CloseCurrentPage()
	}

	if pg.assetTypeDropdown.Changed(gtx) {
		pg.assetTypeError.Text = ""
	}
	if pg.seedTypeDropdown.Changed(gtx) {
		pg.assetTypeError.Text = ""
	}

	// decred wallet type sub action
	for i, item := range pg.walletActions {
		if item.clickable.Clicked(gtx) {
			pg.selectedWalletAction = i
		}
	}

	// restore wallet actions
	if pg.restoreBtn.Clicked(gtx) && pg.validRestoreWalletInputs() {
		if pg.checkWalletNameExists() {
			return
		}

		afterRestore := func(newWallet sharedW.Asset) {
			// todo setup mixer for restored accounts automatically
			pg.walletCreationSuccessCallback(newWallet)
		}
		// Dropdown stores the user-facing label ("VAR & SKA") — must
		// run it through ParseAssetTypeDisplayName to get the internal
		// AssetType ID; raw libutils.AssetType("VAR & SKA") would be a
		// non-matching string and silently break downstream equality
		// against DCRWalletAsset.
		ast := libutils.ParseAssetTypeDisplayName(pg.assetTypeDropdown.Selected())
		pg.ParentWindow().Display(NewRestorePage(pg.Load, pg.walletName.Editor.Text(), ast, afterRestore))
	}
}

func (pg *CreateWallet) checkWalletNameExists() bool {
	walletName := pg.walletName.Editor.Text()
	exists, err := pg.AssetsManager.DoesWalletNameExist(walletName)
	if err != nil {
		pg.walletName.SetError(err.Error())
		return true
	}
	if exists {
		pg.walletName.SetError(values.StringF(values.StrWalletExist, walletName))
		return true
	}
	return false
}

func (pg *CreateWallet) passwordsMatch(editors ...*widget.Editor) bool {
	if len(editors) < 2 {
		return false
	}

	password := editors[0]
	matching := editors[1]

	if password.Text() != matching.Text() {
		pg.confirmPasswordEditor.SetError(values.String(values.StrPasswordNotMatch))
		return false
	}

	pg.confirmPasswordEditor.SetError("")
	return true
}

func (pg *CreateWallet) validCreateWalletInputs() bool {
	pg.walletName.SetError("")
	pg.passwordEditor.SetError("")
	pg.confirmPasswordEditor.SetError("")
	pg.assetTypeError = pg.Theme.Body1("")

	if !utils.StringNotEmpty(pg.walletName.Editor.Text()) {
		pg.walletName.SetError(values.String(values.StrEnterWalletName))
		return false
	}

	if !utils.ValidateLengthName(pg.walletName.Editor.Text()) {
		pg.walletName.SetError(values.String(values.StrWalletNameLengthError))
		return false
	}

	// A spending password is REQUIRED. The old code returned true when the
	// password field was empty (it only checked the match when a password was
	// typed), so "Continue" with empty passwords passed validation and then
	// CreateNewDCRWallet failed on the empty passphrase — a confusing dead end.
	if !utils.EditorsNotEmpty(pg.passwordEditor.Editor) {
		pg.passwordEditor.SetError(values.String(values.StrEnterSpendingPassword))
		return false
	}
	if !utils.EditorsNotEmpty(pg.confirmPasswordEditor.Editor) {
		pg.confirmPasswordEditor.SetError(values.String(values.StrConfirmSpendingPassword))
		return false
	}
	return pg.passwordsMatch(pg.passwordEditor.Editor, pg.confirmPasswordEditor.Editor)
}

// activeTabEditors returns the editors Tab should cycle through for the
// currently-selected wallet action.
func (pg *CreateWallet) activeTabEditors() []*widget.Editor {
	switch pg.selectedWalletAction {
	case 0: // create new wallet
		return []*widget.Editor{pg.walletName.Editor, pg.passwordEditor.Editor, pg.confirmPasswordEditor.Editor}
	case 1: // restore / import
		if pg.watchOnlyCheckBox.CheckBox.Value {
			return []*widget.Editor{pg.walletName.Editor, pg.watchOnlyWalletHex.Editor}
		}
		return []*widget.Editor{pg.walletName.Editor}
	default:
		return nil
	}
}

// KeysToHandle registers Tab per active editor focus tag (see the long note in
// create_password_modal.go): keying on the focused editor — not the page — is
// what makes the filter match and Tab actually move between the fields.
func (pg *CreateWallet) KeysToHandle() []event.Filter {
	eds := pg.activeTabEditors()
	filters := make([]event.Filter, 0, len(eds)+3)
	filters = append(filters, key.FocusFilter{Target: pg})
	for _, ed := range eds {
		filters = append(filters, key.Filter{Focus: ed, Name: key.NameTab, Optional: key.ModShift})
	}
	// Window-level Enter triggers the primary action for the current mode
	// (Continue when creating, Restore/Import when restoring) — so the restore
	// screen, which has a single editor and thus no Tab filters, still reacts to
	// Enter. Focused editors submit via their own Submit event; HandleKeyPress
	// skips this branch for them so Enter never double-fires.
	filters = append(filters,
		key.Filter{Name: key.NameReturn},
		key.Filter{Name: key.NameEnter},
	)
	return filters
}

// HandleKeyPress switches editors on Tab. Press-only: a Tab press fires Press
// AND Release, and SwitchEditors moves focus each call, so acting on both is a
// net no-op.
func (pg *CreateWallet) HandleKeyPress(gtx C, evt *key.Event) {
	if evt.State != key.Press {
		return
	}

	if evt.Name == key.NameReturn || evt.Name == key.NameEnter {
		if pg.isLoading.Load() {
			return
		}
		// Focused editors submit through their own Submit event (isSubmit in
		// handleEditorEvents); only act on window-level Enter so a single Enter
		// never fires the action twice.
		for _, ed := range pg.activeTabEditors() {
			if gtx.Source.Focused(ed) {
				return
			}
		}
		switch pg.selectedWalletAction {
		case 0: // create new wallet
			pg.continueBtn.Click()
		case 1: // restore / import existing wallet
			if pg.watchOnlyCheckBox.CheckBox.Value {
				pg.importBtn.Click()
			} else {
				pg.restoreBtn.Click()
			}
		}
		pg.ParentWindow().Reload()
		return
	}

	cryptomaterial.SwitchEditors(gtx, evt, pg.activeTabEditors()...)
}

func (pg *CreateWallet) validRestoreWalletInputs() bool {
	pg.walletName.SetError("")
	pg.watchOnlyWalletHex.SetError("")
	pg.assetTypeError = pg.Theme.Body1("")

	if !utils.StringNotEmpty(pg.walletName.Editor.Text()) {
		pg.walletName.SetError(values.String(values.StrEnterWalletName))
		return false
	}

	if !utils.ValidateLengthName(pg.walletName.Editor.Text()) {
		pg.walletName.SetError(values.String(values.StrWalletNameLengthError))
		return false
	}

	if pg.watchOnlyCheckBox.CheckBox.Value && !utils.StringNotEmpty(pg.watchOnlyWalletHex.Editor.Text()) {
		pg.watchOnlyWalletHex.SetError(values.String(values.StrEnterExtendedPubKey))
		return false
	}

	return true
}

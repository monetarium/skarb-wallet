package modal

import (
	"strconv"
	"sync"
	"sync/atomic"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/monetarium/skarb-wallet/app"
	libutils "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/utils"
	"github.com/monetarium/skarb-wallet/ui/values"
)

type CreatePasswordModal struct {
	*load.Load
	*cryptomaterial.Modal

	walletName            cryptomaterial.Editor
	passwordEditor        cryptomaterial.Editor
	confirmPasswordEditor cryptomaterial.Editor
	passwordStrength      cryptomaterial.ProgressBarStyle

	// isLoading is written by the positive-button goroutine (setLoading(false)
	// on failure) and read by Handle/Layout on the UI thread every frame —
	// atomic, per CLAUDE.md §3. serverError is likewise written via SetError
	// from positiveButtonClicked callbacks running on that goroutine, so its
	// access is guarded by errMu.
	isLoading              atomic.Bool
	isCancelable           bool
	walletNameEnabled      bool
	showWalletWarnInfo     bool
	confirmPasswordEnabled bool

	dialogTitle string
	errMu       sync.Mutex
	serverError string
	description string

	parent app.Page

	materialLoader material.LoaderStyle

	customWidget layout.Widget

	// positiveButtonText string
	btnPositive cryptomaterial.Button
	// Returns true to dismiss dialog
	positiveButtonClicked func(walletName, password string, m *CreatePasswordModal) bool

	// negativeButtonText    string
	btnNegative           cryptomaterial.Button
	negativeButtonClicked func()
}

func NewCreatePasswordModal(l *load.Load) *CreatePasswordModal {
	cm := &CreatePasswordModal{
		Load:                   l,
		passwordStrength:       l.Theme.ProgressBar(0),
		btnPositive:            l.Theme.Button(values.String(values.StrConfirm)),
		btnNegative:            l.Theme.OutlineButton(values.String(values.StrCancel)),
		isCancelable:           true,
		confirmPasswordEnabled: true,
	}
	cm.Modal = l.Theme.ModalFloatTitle("create_password_modal", l.IsMobileView(), cm.firstLoad)

	cm.btnPositive.Font.Weight = font.Medium

	cm.btnNegative.Font.Weight = font.Medium
	cm.btnNegative.Margin = layout.Inset{Right: values.MarginPadding8}

	cm.walletName = l.Theme.Editor(new(widget.Editor), values.String(values.StrWalletName))
	cm.walletName.Editor.SingleLine, cm.walletName.Editor.Submit = true, true

	cm.passwordEditor = l.Theme.EditorPassword(new(widget.Editor), values.String(values.StrSpendingPassword))
	cm.passwordEditor.Editor.SingleLine, cm.passwordEditor.Editor.Submit = true, true

	cm.confirmPasswordEditor = l.Theme.EditorPassword(new(widget.Editor), values.String(values.StrConfirmSpendingPassword))
	cm.confirmPasswordEditor.Editor.SingleLine, cm.confirmPasswordEditor.Editor.Submit = true, true
	cm.confirmPasswordEditor.AllowSpaceError(true)

	// Set the default click functions
	cm.negativeButtonClicked = func() {}
	cm.positiveButtonClicked = func(_, _ string, _ *CreatePasswordModal) bool { return true }

	cm.materialLoader = material.Loader(l.Theme.Base)

	return cm
}

func (cm *CreatePasswordModal) OnResume() {
	cm.btnPositive.SetEnabled(cm.validToCreate())
}

func (cm *CreatePasswordModal) firstLoad(gtx C) {
	if cm.walletNameEnabled {
		gtx.Execute(key.FocusCmd{Tag: cm.walletName.Editor})
	} else {
		gtx.Execute(key.FocusCmd{Tag: cm.passwordEditor.Editor})
	}
}

func (cm *CreatePasswordModal) OnDismiss() {}

func (cm *CreatePasswordModal) Title(title string) *CreatePasswordModal {
	cm.dialogTitle = title
	return cm
}

func (cm *CreatePasswordModal) EnableName(enable bool) *CreatePasswordModal {
	cm.walletNameEnabled = enable
	return cm
}

func (cm *CreatePasswordModal) EnableConfirmPassword(enable bool) *CreatePasswordModal {
	cm.confirmPasswordEnabled = enable
	return cm
}

func (cm *CreatePasswordModal) NameHint(hint string) *CreatePasswordModal {
	cm.walletName.Hint = hint
	return cm
}

func (cm *CreatePasswordModal) PasswordHint(hint string) *CreatePasswordModal {
	cm.passwordEditor.Hint = hint
	return cm
}

func (cm *CreatePasswordModal) ConfirmPasswordHint(hint string) *CreatePasswordModal {
	cm.confirmPasswordEditor.Hint = hint
	return cm
}

func (cm *CreatePasswordModal) ShowWalletInfoTip(show bool) *CreatePasswordModal {
	cm.showWalletWarnInfo = show
	return cm
}

func (cm *CreatePasswordModal) SetPositiveButtonText(text string) *CreatePasswordModal {
	cm.btnPositive.Text = text
	return cm
}

func (cm *CreatePasswordModal) SetPositiveButtonCallback(callback func(walletName, password string, m *CreatePasswordModal) bool) *CreatePasswordModal {
	cm.positiveButtonClicked = callback
	return cm
}

func (cm *CreatePasswordModal) SetNegativeButtonText(text string) *CreatePasswordModal {
	cm.btnNegative.Text = text
	return cm
}

func (cm *CreatePasswordModal) SetNegativeButtonCallback(callback func()) *CreatePasswordModal {
	cm.negativeButtonClicked = callback
	return cm
}

func (cm *CreatePasswordModal) setLoading(loading bool) {
	cm.isLoading.Store(loading)
}

func (cm *CreatePasswordModal) SetCancelable(min bool) *CreatePasswordModal {
	cm.isCancelable = min
	return cm
}

func (cm *CreatePasswordModal) SetDescription(description string) *CreatePasswordModal {
	cm.description = description
	return cm
}

func (cm *CreatePasswordModal) SetError(err string) {
	cm.errMu.Lock()
	cm.serverError = values.TranslateErr(err)
	cm.errMu.Unlock()
}

func (cm *CreatePasswordModal) SetPasswordTitleVisibility(show bool) {
	cm.passwordEditor.IsTitleLabel = show
}

func (cm *CreatePasswordModal) UseCustomWidget(layout layout.Widget) *CreatePasswordModal {
	cm.customWidget = layout
	return cm
}

func (cm *CreatePasswordModal) validToCreate() bool {
	nameValid := true
	if cm.walletNameEnabled {
		nameValid = utils.EditorsNotEmpty(cm.walletName.Editor)
	}

	validPassword, passwordsMatch := true, true
	if cm.confirmPasswordEnabled {
		validPassword = utils.EditorsNotEmpty(cm.confirmPasswordEditor.Editor)
		if len(cm.confirmPasswordEditor.Editor.Text()) > 0 {
			passwordsMatch = cm.passwordsMatch(cm.passwordEditor.Editor, cm.confirmPasswordEditor.Editor)
		}
	}

	return nameValid && utils.EditorsNotEmpty(cm.passwordEditor.Editor) && validPassword && passwordsMatch
}

// SetParent sets the page that created PasswordModal as it's parent.
func (cm *CreatePasswordModal) SetParent(parent app.Page) *CreatePasswordModal {
	cm.parent = parent
	return cm
}

func (cm *CreatePasswordModal) Handle(gtx C) {
	cm.btnPositive.SetEnabled(cm.validToCreate())

	isSubmit, isChanged := cryptomaterial.HandleEditorEvents(gtx, &cm.passwordEditor, &cm.confirmPasswordEditor, &cm.walletName)
	if isChanged {
		// reset all modal errors when any editor is modified
		cm.errMu.Lock()
		cm.serverError = ""
		cm.errMu.Unlock()
		cm.walletName.SetError("")
		cm.passwordEditor.SetError("")
		cm.confirmPasswordEditor.SetError("")
	}

	if cm.btnPositive.Clicked(gtx) || isSubmit {
		if cm.walletNameEnabled {
			if !utils.EditorsNotEmpty(cm.walletName.Editor) {
				cm.walletName.SetError(values.String(values.StrEnterWalletName))
				return
			}
		}

		if !utils.EditorsNotEmpty(cm.passwordEditor.Editor) {
			cm.passwordEditor.SetError(values.String(values.StrEnterSpendingPassword))
			return
		}

		if cm.confirmPasswordEnabled {
			if !utils.EditorsNotEmpty(cm.confirmPasswordEditor.Editor) {
				cm.confirmPasswordEditor.SetError(values.String(values.StrConfirmSpendingPassword))
				return
			}
			// Refuse mismatched passwords. The Confirm BUTTON is disabled on
			// mismatch (validToCreate), but isSubmit — pressing Enter in an
			// editor — reached this point with only emptiness checks, so the
			// modal dismissed and the wallet got created with the FIRST
			// field's password while the mismatch error was still on screen.
			if !cm.passwordsMatch(cm.passwordEditor.Editor, cm.confirmPasswordEditor.Editor) {
				return
			}
		}
		cm.setLoading(true)
		go func() {
			if cm.positiveButtonClicked(cm.walletName.Editor.Text(), cm.passwordEditor.Editor.Text(), cm) {
				cm.Dismiss()
				return
			}
			cm.setLoading(false)
		}()
	}

	cm.btnNegative.SetEnabled(!cm.isLoading.Load())
	if cm.btnNegative.Clicked(gtx) {
		if !cm.isLoading.Load() {
			if cm.parent != nil {
				cm.parent.OnNavigatedTo()
			}
			cm.negativeButtonClicked()
			cm.Dismiss()
		}
	}

	if cm.Modal.BackdropClicked(gtx, cm.isCancelable) {
		if !cm.isLoading.Load() {
			cm.Dismiss()
		}
	}

	if cm.confirmPasswordEnabled {
		utils.ComputePasswordStrength(&cm.passwordStrength, cm.Theme, cm.passwordEditor.Editor)
	}
}

// KeysToHandle returns a Filter's slice that describes a set of key combinations
// that this modal wishes to capture. The HandleKeyPress() method will only be
// called when any of these key combinations is pressed.
// Satisfies the load.KeyEventHandler interface for receiving key events.
// tabEditors returns the editors that Tab cycles through, in order, for the
// currently-enabled modal fields. Single source of truth shared by
// KeysToHandle (which registers Tab per editor focus tag) and HandleKeyPress.
func (cm *CreatePasswordModal) tabEditors() []*widget.Editor {
	switch {
	case cm.walletNameEnabled && cm.confirmPasswordEnabled:
		return []*widget.Editor{cm.walletName.Editor, cm.passwordEditor.Editor, cm.confirmPasswordEditor.Editor}
	case cm.walletNameEnabled:
		return []*widget.Editor{cm.walletName.Editor, cm.passwordEditor.Editor}
	default:
		return []*widget.Editor{cm.passwordEditor.Editor, cm.confirmPasswordEditor.Editor}
	}
}

func (cm *CreatePasswordModal) KeysToHandle() []event.Filter {
	// Register Tab on EACH active editor's focus tag — not on cm. A
	// key.Filter only matches while its Focus tag is the focused widget, and
	// the focused widget is whichever editor the user is typing in, never the
	// modal cm itself. With the old Focus:cm filter, Tab never reached
	// HandleKeyPress/SwitchEditors, so Gio's DEFAULT focus traversal handled
	// it — and that chain includes each password field's show/hide ("eye")
	// IconButton, so the first Tab landed on the eye icon and a second Tab was
	// needed to reach the next field. Keying the filter to the editors makes
	// SwitchEditors authoritative AND consumes the Tab, suppressing the
	// default traversal through the toggle button.
	filters := []event.Filter{key.FocusFilter{Target: cm}}
	for _, ed := range cm.tabEditors() {
		filters = append(filters, key.Filter{Focus: ed, Name: key.NameTab, Optional: key.ModShift})
	}
	// Window-level Enter = press the Confirm button (when valid). Focus:nil
	// so it matches regardless of which widget is focused — focused editors
	// already submit via their own Submit event, and HandleKeyPress skips
	// this branch for them to avoid double-firing.
	filters = append(filters,
		key.Filter{Name: key.NameReturn},
		key.Filter{Name: key.NameEnter},
	)
	return filters
}

// HandleKeyPress is called when one or more keys are pressed on the current
// window that match any of the key combinations returned by KeysToHandle().
// Satisfies the load.KeyEventHandler interface for receiving key events.
func (cm *CreatePasswordModal) HandleKeyPress(gtx C, evt *key.Event) {
	// A physical Tab press delivers BOTH a key.Press and a key.Release event.
	// SwitchEditors moves focus to the next editor every time it runs without
	// checking evt.State, so firing it on both events advances focus on Press
	// and then immediately moves it back on Release — a net no-op, which is
	// exactly the "Tab does nothing" symptom. Act on Press only. (The upstream
	// pattern got away without this guard because its Focus:cm filter never
	// matched a focused editor, so SwitchEditors never actually ran and Tab
	// fell through to Gio's default traversal — which stepped through the
	// password show/hide toggle, the original two-presses bug.)
	if evt.State != key.Press {
		return
	}

	if evt.Name == key.NameReturn || evt.Name == key.NameEnter {
		if cm.isLoading.Load() {
			return
		}
		// A focused editor already submits via its own Submit event (the
		// isSubmit path in Handle) — only treat this as "press Confirm" when
		// focus is elsewhere, so a single Enter never fires the action twice.
		for _, ed := range cm.tabEditors() {
			if gtx.Source.Focused(ed) {
				return
			}
		}
		// Queue a Confirm click; Handle's existing validation (emptiness +
		// password match) decides whether the modal actually proceeds.
		cm.btnPositive.Click()
		cm.ParentWindow().Reload()
		return
	}

	cryptomaterial.SwitchEditors(gtx, evt, cm.tabEditors()...)
}

func (cm *CreatePasswordModal) passwordsMatch(editors ...*widget.Editor) bool {
	if len(editors) < 2 {
		return false
	}

	password := editors[0]
	matching := editors[1]

	if password.Text() != matching.Text() {
		cm.confirmPasswordEditor.SetError(values.String(values.StrPasswordNotMatch))
		return false
	}

	cm.confirmPasswordEditor.SetError("")
	return true
}

func (cm *CreatePasswordModal) titleLayout() layout.Widget {
	return func(gtx C) D {
		t := cm.Theme.H6(cm.dialogTitle)
		if cm.IsMobileView() {
			t.TextSize = values.TextSize16
		}
		t.Font.Weight = font.SemiBold
		return layout.Inset{Bottom: values.MarginPadding10}.Layout(gtx, t.Layout)
	}
}

func (cm *CreatePasswordModal) descriptionLayout() layout.Widget {
	return func(gtx C) D {
		desc := cm.Theme.Label(values.TextSizeTransform(cm.IsMobileView(), values.TextSize16), cm.description)
		return layout.Inset{Bottom: values.MarginPadding5}.Layout(gtx, desc.Layout)
	}
}

func (cm *CreatePasswordModal) Layout(gtx C) D {
	return cm.Modal.Layout(gtx, cm.LayoutComponents())
}

func (cm *CreatePasswordModal) LayoutComponents() []layout.Widget {
	btnTextSize := values.TextSize16
	if cm.IsMobileView() {
		btnTextSize = values.TextSize14
	}
	cm.btnNegative.TextSize = btnTextSize
	cm.btnPositive.TextSize = btnTextSize

	w := []layout.Widget{}

	if cm.dialogTitle != "" {
		w = append(w, cm.titleLayout())
	}

	if cm.description != "" {
		w = append(w, cm.descriptionLayout())
	}

	if cm.customWidget != nil {
		w = append(w, cm.customWidget)
	}

	cm.errMu.Lock()
	serverError := cm.serverError
	cm.errMu.Unlock()
	if serverError != "" {
		// set wallet name editor error if wallet name already exist
		if serverError == libutils.ErrExist && cm.walletNameEnabled {
			cm.walletName.SetError(values.StringF(values.StrWalletExist, cm.walletName.Editor.Text()))
		} else if !utils.ValidateLengthName(cm.walletName.Editor.Text()) && cm.walletNameEnabled {
			cm.walletName.SetError(values.String(values.StrWalletNameLengthError))
		} else {
			t := cm.Theme.Body2(serverError)
			t.Color = cm.Theme.Color.Danger
			w = append(w, t.Layout)
		}
	}

	if cm.walletNameEnabled {
		w = append(w, cm.walletName.Layout)
	}

	w = append(w, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(cm.passwordEditor.Layout),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Left: values.MarginPadding20, Right: values.MarginPadding20}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Spacing: layout.SpaceBetween}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							if cm.showWalletWarnInfo {
								txt := cm.Theme.Label(values.TextSize12, values.String(values.StrSpendingPasswordInfo2))
								txt.Color = cm.Theme.Color.GrayText1
								return txt.Layout(gtx)
							}
							return D{}
						}),
						layout.Rigid(func(gtx C) D {
							txt := cm.Theme.Label(values.TextSize12, strconv.Itoa(cm.passwordEditor.Editor.Len()))
							txt.Color = cm.Theme.Color.GrayText1

							if txt.Text != "0" {
								return layout.E.Layout(gtx, txt.Layout)
							}
							return D{}
						}),
					)
				})
			}),
		)
	})

	if cm.confirmPasswordEnabled {
		w = append(w, cm.passwordStrength.Layout)
		w = append(w, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(cm.confirmPasswordEditor.Layout),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Right: values.MarginPadding20}.Layout(gtx, func(gtx C) D {
						txt := cm.Theme.Label(values.TextSize12, strconv.Itoa(cm.confirmPasswordEditor.Editor.Len()))
						txt.Color = cm.Theme.Color.GrayText1
						if txt.Text != "0" {
							return layout.E.Layout(gtx, txt.Layout)
						}

						return D{}
					})
				}),
			)
		})
	}

	w = append(w, func(gtx C) D {
		return layout.E.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					if cm.isLoading.Load() {
						return D{}
					}

					return cm.btnNegative.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					if cm.isLoading.Load() {
						return cm.materialLoader.Layout(gtx)
					}

					return cm.btnPositive.Layout(gtx)
				}),
			)
		})
	})

	return w
}

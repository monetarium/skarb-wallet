package send

import (
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"gioui.org/io/key"
	"gioui.org/widget"
	"golang.org/x/exp/shiny/materialdesign/icons"

	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/skarb-wallet/device"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	libUtil "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/values"
)

var tabOptions = []string{
	values.StrAddress,
	values.StrWallets,
}

type destination struct {
	*load.Load

	addressChanged           func()
	destinationAddressEditor cryptomaterial.Editor
	sourceAccount            *sharedW.Account

	walletDropdown  *components.WalletDropdown
	accountDropdown *components.AccountDropdown

	accountSwitch *cryptomaterial.SegmentedControl

	// QR scanning (Android): the poll goroutine never touches UI state —
	// results land in these atomics and handle() applies them on the UI
	// thread (CLAUDE.md §3). reload is the owning window's Reload, wired
	// by newRecipient.
	reload       func()
	qrScanGen    atomic.Int32           // bump to cancel a running poll loop
	qrScanResult atomic.Pointer[string] // decoded text pending UI apply
	qrScanFailed atomic.Bool            // scanner error pending toast
}

func newSendDestination(l *load.Load, assetType libUtil.AssetType) *destination {
	dst := &destination{
		Load:          l,
		accountSwitch: l.Theme.SegmentedControl(tabOptions, cryptomaterial.SegmentTypeGroupMax),
	}

	dst.accountSwitch.SetEnableSwipe(false)
	dst.accountSwitch.DisableUniform(true)

	if l.Device.QRScanSupported() {
		// Trailing camera button opens the in-app QR scanner.
		scanIcon, _ := widget.NewIcon(icons.ImagePhotoCamera)
		dst.destinationAddressEditor = l.Theme.IconEditor(new(widget.Editor),
			values.String(values.StrDestAddr), scanIcon, true)
		dst.destinationAddressEditor.EditorIconButtonEvent = dst.startQRScan
	} else {
		dst.destinationAddressEditor = l.Theme.Editor(new(widget.Editor), values.String(values.StrDestAddr))
	}
	dst.destinationAddressEditor.TextSize = values.TextSizeTransform(l.IsMobileView(), values.TextSize16)
	dst.destinationAddressEditor.Editor.SingleLine = true
	// URI keyboards disable autocorrection — a base58 address must never
	// be "corrected" into dictionary words by a mobile IME.
	dst.destinationAddressEditor.Editor.InputHint = key.HintURL
	dst.destinationAddressEditor.Editor.SetText("")
	dst.destinationAddressEditor.IsTitleLabel = false

	dst.initDestinationWalletSelector(assetType)
	return dst
}

func (dst *destination) initDestinationWalletSelector(assetType libUtil.AssetType) {
	dst.walletDropdown = components.NewWalletDropdown(dst.Load, assetType).
		SetChangedCallback(func(wallet sharedW.Asset) {
			if dst.accountDropdown != nil {
				_ = dst.accountDropdown.Setup(wallet)
			}
		}).
		WalletValidator(func(wallet sharedW.Asset) bool {
			if dst.sourceAccount == nil {
				return true
			}
			if wallet.GetWalletID() == dst.sourceAccount.WalletID {
				account, err := wallet.GetAccountsRaw()
				if err != nil || len(account.Accounts) < 2 {
					return false
				}
			}
			return true
		}).
		EnableWatchOnlyWallets(true).
		Setup()
	dst.accountDropdown = components.NewAccountDropdown(dst.Load).
		SetChangedCallback(func(_ *sharedW.Account) {
			dst.addressChanged()
		}).
		AccountValidator(func(account *sharedW.Account) bool {
			if dst.sourceAccount == nil {
				return false
			}
			accountIsValid := account.Number != load.MaxInt32
			// Filter mixed wallet
			destinationWallet := dst.walletDropdown.SelectedWallet()
			isMixedAccount := load.MixedAccountNumber(destinationWallet) == account.Number

			// Filter the sending account.
			sourceWalletID := dst.sourceAccount.WalletID
			isSameAccount := sourceWalletID == account.WalletID && account.Number == dst.sourceAccount.Number
			if !accountIsValid || isSameAccount || isMixedAccount {
				return false
			}
			return true
		}).
		Setup(dst.walletDropdown.SelectedWallet())
}

// setCoinType propagates the actively-sent coin to the destination selectors
// so the wallet/account rows show the balance of THAT coin — previously they
// always showed VAR even while sending SKA1.
func (dst *destination) setCoinType(ct cointype.CoinType) {
	if dst.walletDropdown != nil {
		dst.walletDropdown.SetCoinType(ct)
	}
	if dst.accountDropdown != nil {
		dst.accountDropdown.SetCoinType(ct)
	}
}

// destinationAddress validates the destination address obtained from the provided
// raw address or the selected account address.
func (dst *destination) destinationAddress() (string, error) {
	if dst.isSendToAddress() {
		return dst.validateDestinationAddress()
	}

	destinationAccount := dst.accountDropdown.SelectedAccount()
	if destinationAccount == nil {
		// errors.New (not fmt.Errorf) because the i18n string may contain
		// a literal `%` that would otherwise be parsed as a format verb.
		return "", errors.New(values.String(values.StrInvalidAddress))
	}

	wal := dst.AssetsManager.WalletWithID(destinationAccount.WalletID)
	return wal.CurrentAddress(destinationAccount.Number)
}

func (dst *destination) destinationAccount() *sharedW.Account {
	if dst.isSendToAddress() {
		return nil
	}

	return dst.accountDropdown.SelectedAccount()
}

// validateDestinationAddress checks if raw address provided as destination is
// valid.
func (dst *destination) validateDestinationAddress() (string, error) {
	address := dst.destinationAddressEditor.Editor.Text()
	address = strings.TrimSpace(address)

	if address == "" {
		return address, errors.New(values.String(values.StrDestinationMissing))
	}

	if dst.walletDropdown != nil && dst.walletDropdown.SelectedWallet() != nil && dst.walletDropdown.SelectedWallet().IsAddressValid(address) {
		dst.destinationAddressEditor.SetError("")
		return address, nil
	}

	return address, errors.New(values.String(values.StrInvalidAddress))
}

func (dst *destination) validate() bool {
	if dst.isSendToAddress() {
		_, err := dst.validateDestinationAddress()
		// if err equals to nil then the address is valid.
		return err == nil
	}

	if dst.destinationAccount() == nil {
		dst.setError(values.String(values.StrNoValidAccountFound))
		return false
	}

	return true
}

func (dst *destination) setError(errMsg string) {
	if dst.isSendToAddress() {
		dst.destinationAddressEditor.SetError(errMsg)
	}
}

func (dst *destination) clearAddressInput() {
	dst.destinationAddressEditor.SetError("")
	dst.destinationAddressEditor.Editor.SetText("")
}

// isSendToAddress returns the current tab selection status without depending
// on a buffered state.
func (dst *destination) isSendToAddress() bool {
	return dst.accountSwitch.SelectedSegment() == values.StrAddress
}

func (dst *destination) HandleDropdownInteraction(gtx C) {
	dst.accountDropdown.Handle(gtx)
	dst.walletDropdown.Handle(gtx)
}

// startQRScan opens the Android camera overlay (EditorIconButtonEvent)
// and polls it off-thread until a QR decodes, the user cancels, the page
// stops the scan, or a 2-minute deadline passes.
func (dst *destination) startQRScan() {
	hint := values.String(values.StrScanQRHint)
	cancelLabel := values.String(values.StrCancel)
	// A previous session's undelivered outcome must not fire now.
	dst.qrScanResult.Store(nil)
	dst.qrScanFailed.Store(false)
	if err := dst.Device.QRScanStart(hint, cancelLabel); err != nil {
		dst.Toast.NotifyError(values.String(values.StrScannerUnavailable))
		return
	}
	gen := dst.qrScanGen.Add(1)
	go func() {
		deadline := time.Now().Add(2 * time.Minute)
		var requestingSince time.Time
		lastSeq := 0
		for time.Now().Before(deadline) {
			time.Sleep(150 * time.Millisecond)
			if dst.qrScanGen.Load() != gen {
				return // cancelled by stopQRScan (page left / new scan)
			}
			state, seq, frame, w, h, err := dst.Device.QRScanTick(hint, cancelLabel, lastSeq)
			switch {
			case err != nil || state == device.QRStateError:
				dst.Device.QRScanStop()
				dst.qrScanFailed.Store(true)
				dst.reloadWindow()
				return
			case state == device.QRStateIdle:
				return // user tapped ✕
			case state == device.QRStateRequesting:
				// GioActivity drops the permission-result callback, so a
				// denial is indistinguishable from an unanswered dialog —
				// give up after 30s instead of the icon looking dead for
				// the full deadline.
				if requestingSince.IsZero() {
					requestingSince = time.Now()
				}
				if time.Since(requestingSince) > 30*time.Second {
					dst.Device.QRScanStop()
					dst.qrScanFailed.Store(true)
					dst.reloadWindow()
					return
				}
				continue
			}
			if frame == nil {
				continue
			}
			lastSeq = seq
			text, err := device.DecodeQRLuma(frame, w, h)
			if err != nil {
				continue // no QR in this frame — keep aiming
			}
			dst.Device.QRScanStop()
			addr := normalizeScannedAddress(text)
			dst.qrScanResult.Store(&addr)
			dst.reloadWindow()
			return
		}
		dst.Device.QRScanStop()
	}()
}

// stopQRScan cancels any in-flight scan; safe on every platform.
func (dst *destination) stopQRScan() {
	dst.qrScanGen.Add(1)
	if dst.Device.QRScanSupported() {
		dst.Device.QRScanStop()
	}
}

func (dst *destination) reloadWindow() {
	if dst.reload != nil {
		dst.reload()
	}
}

// normalizeScannedAddress reduces a scanned payload to a bare address:
// payment URIs ("monetarium:ADDR?amount=…") lose the scheme and query.
func normalizeScannedAddress(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ':'); i >= 0 && strings.EqualFold(s[:i], "monetarium") {
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	return s
}

func (dst *destination) handle(gtx C) {
	// Apply a finished QR scan on the UI thread.
	if p := dst.qrScanResult.Swap(nil); p != nil {
		dst.destinationAddressEditor.Editor.SetText(*p)
		dst.addressChanged()
	}
	if dst.qrScanFailed.CompareAndSwap(true, false) {
		dst.Toast.NotifyError(values.String(values.StrScannerUnavailable))
	}

	if dst.accountSwitch.Changed() {
		dst.addressChanged()
	}

	for {
		event, ok := dst.destinationAddressEditor.Editor.Update(gtx)
		if !ok {
			break
		}

		if gtx.Source.Focused(dst.destinationAddressEditor.Editor) {
			switch event.(type) {
			case widget.ChangeEvent:
				dst.addressChanged()
			}
		}
	}
}

// styleWidgets sets the appropriate colors for the destination widgets.
func (dst *destination) styleWidgets() {
	// dst.accountSwitch.Active, dst.accountSwitch.Inactive = dst.Theme.Color.Surface, color.NRGBA{}
	// dst.accountSwitch.ActiveTextColor, dst.accountSwitch.InactiveTextColor = dst.Theme.Color.GrayText1, dst.Theme.Color.Surface
	dst.destinationAddressEditor.EditorStyle.Color = dst.Theme.Color.Text
}

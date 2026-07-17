package ui

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	giouiApp "gioui.org/app"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/monetarium/skarb-wallet/app"
	libutils "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/assets"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/notification"
	"github.com/monetarium/skarb-wallet/ui/page"
	"github.com/monetarium/skarb-wallet/ui/values"
)

// Window represents the app window (and UI in general). There should only be one.
// Window maintains an internal state of variables to determine what to display at
// any point in time.
type Window struct {
	*giouiApp.Window
	navigator app.WindowNavigator

	ctx       context.Context
	ctxCancel context.CancelFunc

	load *load.Load

	// Quit channel used to trigger background process to begin implementing the
	// shutdown protocol.
	Quit chan struct{}
	// IsShutdown channel is used to report that background processes have
	// completed shutting down, therefore the UI processes can finally stop.
	IsShutdown chan struct{}

	// dragger is used to handle drag gestures.
	drag       gesture.Drag
	isClick    bool
	isDragging bool

	// screenAwake mirrors the mobile keep-screen-on flag: while a wallet
	// is syncing the screen must not sleep — mobile OSes cut background
	// networking, stranding the SPV sync half-finished.
	screenAwake    bool
	lastAwakeCheck time.Time
}

type (
	C = layout.Context
	D = layout.Dimensions
)

type WriteClipboard struct {
	Text string
}

// reloadMinInterval paces app-triggered redraws (navigator.Reload).
// During SPV sync the progress/tx listeners fire Reload for every header
// batch and transaction, redrawing the whole window at up to 60fps for
// minutes — a battery killer on phones. 50ms still allows 20fps of
// notification-driven updates; input-driven redraws are unaffected (gio
// invalidates on its own for those).
const reloadMinInterval = 50 * time.Millisecond

// throttledInvalidate rate-limits calls to invalidate to one per
// reloadMinInterval. A call arriving inside the quiet window schedules a
// single trailing invalidate instead of being dropped, so the final
// state after a burst is always rendered.
func throttledInvalidate(invalidate func()) func() {
	var mu sync.Mutex
	var last time.Time
	var pending bool
	return func() {
		mu.Lock()
		if pending { // a trailing invalidate is already scheduled
			mu.Unlock()
			return
		}
		wait := reloadMinInterval - time.Since(last)
		if wait <= 0 {
			last = time.Now()
			mu.Unlock()
			invalidate()
			return
		}
		pending = true
		mu.Unlock()
		time.AfterFunc(wait, func() {
			mu.Lock()
			last = time.Now()
			pending = false
			mu.Unlock()
			invalidate()
		})
	}
}

// CreateWindow creates and initializes a new window with start
// as the first page displayed.
// Should never be called more than once as it calls
// app.NewWindow() which does not support being called more
// than once.
func CreateWindow(appInfo *load.AppInfo) (*Window, error) {
	// Always use the full "Skarb Wallet" branding in the window title; only
	// non-mainnet builds get a "(testnet)"-style suffix to make the network
	// obvious. Previously mainnet showed just "Skarb" while testnet showed
	// "Skarb Wallet (testnet)" — that asymmetry confused users.
	appTitle := giouiApp.Title(values.String(values.StrAppWallet))
	// appSize overwrites gioui's default app size of 'Size(800, 600)'.
	// We deliberately use DefaultWindowWidth/Height (1200×800), not
	// AppWidth/AppHeight (800×650): the latter is a content-padding
	// threshold, the former is the initial window size. Ukrainian
	// localisation needs more horizontal room — six wallet tabs plus a
	// sidebar didn't fit at 800px wide.
	appSize := giouiApp.Size(values.DefaultWindowWidth, values.DefaultWindowHeight)
	// appMinSize is the minimum size the app.
	appMinSize := giouiApp.MinSize(values.MobileAppWidth, values.MobileAppHeight)
	if net := appInfo.AssetsManager.NetType(); net != libutils.Mainnet {
		appTitle = giouiApp.Title(values.StringF(values.StrAppTitle, net.Display()))
	}

	ctx, cancel := context.WithCancel(context.Background())
	giouiWindow := new(giouiApp.Window)
	giouiWindow.Option(appSize, appMinSize, appTitle)
	win := &Window{
		ctx:        ctx,
		ctxCancel:  cancel,
		Window:     giouiWindow,
		navigator:  app.NewSimpleWindowNavigator(throttledInvalidate(giouiWindow.Invalidate)),
		Quit:       make(chan struct{}, 1),
		IsShutdown: make(chan struct{}, 1),
	}

	l, err := win.NewLoad(appInfo, giouiWindow)
	if err != nil {
		return nil, err
	}

	win.load = l

	startPage := page.NewStartPage(win.ctx, win.load)
	win.load.AppInfo.ReadyForDisplay(win.Window, startPage)

	// Set DEX ctx to enable initializing dex from any page.
	appInfo.AssetsManager.UpdateDEXCtx(win.ctx)

	return win, nil
}

func (win *Window) NewLoad(appInfo *load.AppInfo, window *giouiApp.Window) (*load.Load, error) {
	l := load.NewLoad(appInfo, window)
	th := cryptomaterial.NewTheme(assets.FontCollection(), assets.DecredIcons, false)
	if th == nil {
		return nil, errors.New("unexpected error while loading theme")
	}

	// fetch status of the wallet if its online.
	go libutils.IsOnline()

	// Set the user-configured theme colors on app load.
	var isDarkModeOn bool
	if appInfo.AssetsManager.LoadedWalletsCount() > 0 {
		// A valid DB interface must have been set. Otherwise no valid wallet exists.
		isDarkModeOn = appInfo.AssetsManager.IsDarkModeOn()
	}
	th.SwitchDarkMode(isDarkModeOn, assets.DecredIcons)

	l.Theme = th
	// NB: Toasts implementation is maintained here for the cases where its
	// very essential to have a toast UI component implementation otherwise
	// restraints should be exercised when planning to reuse it else where.
	l.Toast = notification.NewToast(th)
	l.Printer = message.NewPrinter(language.English)

	appInfo.AssetsManager.SetToast(l.Toast)

	// DarkModeSettingChanged checks if any page or any
	// modal implements the AppSettingsChangeHandler
	l.DarkModeSettingChanged = func(isDarkModeOn bool) {
		if page, ok := win.navigator.CurrentPage().(load.AppSettingsChangeHandler); ok {
			page.OnDarkModeChanged(isDarkModeOn)
		}
		if modal := win.navigator.TopModal(); modal != nil {
			if modal, ok := modal.(load.AppSettingsChangeHandler); ok {
				modal.OnDarkModeChanged(isDarkModeOn)
			}
		}
	}

	l.LanguageSettingChanged = func() {
		if page, ok := win.navigator.CurrentPage().(load.AppSettingsChangeHandler); ok {
			page.OnLanguageChanged()
		}
		// Also forward to the top modal, mirroring DarkModeSettingChanged —
		// otherwise a modal that caches values.String in struct fields keeps
		// the previous language after a switch.
		if modal := win.navigator.TopModal(); modal != nil {
			if modal, ok := modal.(load.AppSettingsChangeHandler); ok {
				modal.OnLanguageChanged()
			}
		}
	}

	l.CurrencySettingChanged = func() {
		if page, ok := win.navigator.CurrentPage().(load.AppSettingsChangeHandler); ok {
			page.OnCurrencyChanged()
		}
	}

	return l, nil
}

// HandleEvents runs main event handling and page rendering loop.
func (win *Window) HandleEvents() {
	done := make(chan os.Signal, 1)
	if runtime.GOOS == "windows" {
		// For controlled shutdown to work on windows, the channel has to be
		// listening to all signals.
		// https://github.com/golang/go/commit/8cfa01943a7f43493543efba81996221bb0f27f8
		signal.Notify(done)
	} else {
		// Signals are primarily used on Unix-like systems.
		signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	}

	var isShuttingDown bool

	displayShutdownPage := func(windowAlreadyDestroyed bool) {
		if isShuttingDown {
			return
		}

		doShutdown := func() {
			isShuttingDown = true

			log.Info("...Initiating the app shutdown protocols...")

			// clear all stack and display the shutdown page as backend processes are
			// terminating.
			win.navigator.ClearStackAndDisplay(page.NewStartPage(win.ctx, win.load, true))
			win.ctxCancel()

			// Trigger the backend processes shutdown.
			win.Quit <- struct{}{}
		}

		if !win.load.AssetsManager.DEXCInitialized() || windowAlreadyDestroyed {
			doShutdown()
			return
		}

		if !win.load.AssetsManager.DexClient().Active() {
			doShutdown()
			return
		}

		// User has active orders, show an error modal and only allow shutdown
		// if user allows it.
		m := modal.NewErrorModal(win.load, values.String(values.StrDexError), modal.DefaultClickFunc())
		m.SetPositiveButtonText(values.String(values.StrYes)).SetPositiveButtonCallback(func(_ bool, _ *modal.InfoModal) bool {
			doShutdown()
			return true
		}).
			SetNegativeButtonText(values.String(values.StrNo)).
			SetNegativeButtonCallback(func() {
				win.navigator.Display(win.navigator.CurrentPage())
			}).
			SetCancelable(true).
			Body(values.String(values.StrActiveDexOrderError))

		win.navigator.ShowModal(m)
	}

	// Create window chan event and listen events from window event
	events := make(chan event.Event)
	acks := make(chan struct{}, 2)
	go func() {
		for {
			ev := win.load.Device.ProcessEvent(win.Window)
			events <- ev
			<-acks
			if _, ok := ev.(giouiApp.DestroyEvent); ok {
				return
			}
		}
	}()

	for {
		// Select either the os interrupt or the window event, whichever becomes
		// ready first.
		select {
		case <-done:
			displayShutdownPage(false)
		case <-win.IsShutdown:
			// backend processes shutdown is complete, exit UI process too.
			_ = win.load.Device.SetScreenAwake(false)
			return
		case e := <-events:
			switch evt := e.(type) {
			case giouiApp.DestroyEvent:
				displayShutdownPage(true)
				acks <- struct{}{}
			case giouiApp.FrameEvent:
				ops := win.handleFrameEvent(evt)
				evt.Frame(ops)
			default:
				log.Tracef("Unhandled window event %v\n", e)
			}
			acks <- struct{}{}
		}
	}
}

// handleFrameEvent is called when a FrameEvent is received by the active
// window. It expects a new frame in the form of a list of operations that
// describes what to display and how to handle input. This operations list
// is returned to the caller for displaying on screen.
func (win *Window) handleFrameEvent(evt giouiApp.FrameEvent) *op.Ops {
	// The usable width excludes system decorations (status/navigation
	// bars, notches) — giouiApp.NewContext below clamps the layout to
	// that safe area, so the mobile-view breakpoint must match it.
	usableWidth := evt.Size.X -
		evt.Metric.Dp(evt.Insets.Left) - evt.Metric.Dp(evt.Insets.Right)
	win.load.SetCurrentAppWidth(usableWidth, evt.Metric)
	win.updateScreenAwake()
	ops := &op.Ops{}
	gtx := giouiApp.NewContext(ops, evt)

	switch {
	case win.navigator.CurrentPage() == nil:
		// Prepare to display the StartPage if no page is currently displayed.
		win.navigator.Display(win.load.StartPage())

	default:
		// The app window may have received some user interaction such as key
		// presses, a button click, etc which triggered this FrameEvent. Handle
		// such interactions before re-displaying the UI components. This
		// ensures that the proper interface is displayed to the user based on
		// the action(s) they just performed.
		win.navigator.CurrentPage().HandleUserInteractions(gtx)
		if modal := win.navigator.TopModal(); modal != nil {
			modal.Handle(gtx)
		}
	}

	// Generate an operations list with instructions for drawing the window's UI
	// components onto the screen. Use the generated ops to request key events.
	win.prepareToDisplayUI(gtx)
	win.addListenKeyEvent(gtx)
	return ops
}

// updateScreenAwake keeps the phone screen on while any wallet is
// actively syncing or rescanning: mobile OSes stop networking the moment
// the screen sleeps, stranding the SPV sync half-finished. The sync
// state is read from in-memory flags (no DB) at most once a second; the
// native keep-screen-on call fires only when the state flips, on a
// separate goroutine so the JNI round-trip never blocks a frame.
func (win *Window) updateScreenAwake() {
	if runtime.GOOS != "android" && runtime.GOOS != "ios" {
		return
	}
	if time.Since(win.lastAwakeCheck) < time.Second {
		return
	}
	win.lastAwakeCheck = time.Now()
	syncing := false
	for _, wal := range win.load.AssetsManager.AllWallets() {
		if wal.IsSyncing() || wal.IsRescanning() {
			syncing = true
			break
		}
	}
	if syncing != win.screenAwake {
		win.screenAwake = syncing
		go func() { _ = win.load.Device.SetScreenAwake(syncing) }()
	}
}

// prepareToDisplayUI creates an operation list and writes the layout of all the
// window UI components into it. The created ops is returned and may be used to
// record further operations before finally being rendered on screen via
// system.FrameEvent.Frame(ops).
func (win *Window) prepareToDisplayUI(gtx layout.Context) {
	// Back arrows re-register as hardware-back targets while the pages
	// lay out below; handleEvents at the end of this frame reads them.
	win.load.Theme.ResetBackTargets()
	backgroundWidget := layout.Expanded(func(gtx C) D {
		return win.load.Theme.DropdownBackdrop.Layout(gtx, func(gtx C) D {
			return cryptomaterial.Fill(gtx, win.load.Theme.Color.Gray4)
		})
	})

	currentPageWidget := layout.Stacked(func(gtx C) D {
		if modal := win.navigator.TopModal(); modal != nil {
			gtx = gtx.Disabled()
		}
		if win.navigator.CurrentPage() == nil {
			win.navigator.Display(page.NewStartPage(win.ctx, win.load))
		}
		return win.load.Theme.DropdownBackdrop.Layout(gtx, win.navigator.CurrentPage().Layout)
	})

	topModalLayout := layout.Stacked(func(gtx C) D {
		modal := win.navigator.TopModal()
		if modal == nil {
			return D{}
		}
		return win.load.Theme.DropdownBackdrop.Layout(gtx, modal.Layout)
	})

	win.drag.Add(gtx.Ops)

	// Use a StackLayout to write the above UI components into an operations
	// list via a graphical context that is linked to the ops.
	layout.Stack{Alignment: layout.N}.Layout(
		gtx,
		backgroundWidget,
		currentPageWidget,
		topModalLayout,
		layout.Stacked(win.load.Toast.Layout),
	)
	win.handleEvents(gtx)
}

func (win *Window) addListenKeyEvent(gtx C) {
	// Request key events on the top modal, if necessary.
	// Only request key events on the current page if no modal is displayed.
	if modal := win.navigator.TopModal(); modal != nil {
		if handler, ok := modal.(load.KeyEventHandler); ok {
			if len(handler.KeysToHandle()) == 0 || handler.KeysToHandle() == nil {
				return
			}
			for {
				e, ok := gtx.Event(handler.KeysToHandle()...)
				if !ok {
					break
				}
				switch e := e.(type) {
				case key.Event:
					handler.HandleKeyPress(gtx, &e)
				}
			}
		}
	} else {
		if handler, ok := win.navigator.CurrentPage().(load.KeyEventHandler); ok {
			if len(handler.KeysToHandle()) == 0 || handler.KeysToHandle() == nil {
				return
			}
			for {
				e, ok := gtx.Event(handler.KeysToHandle()...)
				if !ok {
					break
				}
				switch e := e.(type) {
				case key.Event:
					handler.HandleKeyPress(gtx, &e)
				}
			}
		}
	}
}

func (win *Window) handleEvents(gtx C) {
	win.handleUserClick(gtx)
	win.listenSoftKey(gtx)
}

// handleUserClick listen touch action of user for mobile.
func (win *Window) handleUserClick(gtx C) {
	for {
		event, ok := win.drag.Update(gtx.Metric, gtx.Source, gesture.Both)
		if !ok {
			break
		}
		switch event.Kind {
		case pointer.Press:
			win.isClick = true
		case pointer.Drag:
			win.isDragging = true
		case pointer.Release:
			if win.isClick && !win.isDragging {
				gtx.Execute(key.SoftKeyboardCmd{Show: false})
			}
			win.isClick = false
			win.isDragging = false
		}
	}

	win.load.Theme.ShowKeyboardIfEditorFocused(gtx)
}

// handleShortKeys listen keys pressed.
func (win *Window) listenSoftKey(gtx C) {
	// check for presses of the back key.
	if runtime.GOOS == "android" {
		// Claim the back key only while a back target is on screen.
		// Registering the filter marks the key handled at the driver
		// (GioView.onBack returns JNI_TRUE), so an unconditional claim
		// would swallow the press on root pages where there is nothing
		// to go back to — with no claim Android's default applies and
		// the app is backgrounded, as users expect.
		if !win.load.Theme.HasBackTarget() {
			return
		}
		for {
			event, ok := gtx.Event(key.Filter{
				Name: key.NameBack,
			})
			if !ok {
				break
			}

			switch event := event.(type) {
			case key.Event:
				if event.Name == key.NameBack && event.State == key.Press {
					// A visible modal owns the screen: never navigate the
					// page underneath it. Modals expose their own cancel
					// controls, and some (seed backup, password prompts)
					// must not be silently dismissed.
					if win.navigator.TopModal() == nil {
						win.load.Theme.OnTapBack()
						win.navigator.Reload()
					}
				}
			}
		}
	}
}

package components

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/utils"
	"github.com/monetarium/skarb-wallet/ui/values"
)

type VSPSelector struct {
	*load.Load
	dcrWallet *dcr.Asset

	dialogTitle string

	changed      bool
	showVSPModal *cryptomaterial.Clickable
	selectedVSP  *dcr.VSP
	// allowDirectBuy adds the synthetic "Direct buy (solo)" entry to the
	// picker. Opt-in per call site: the manual purchase flow supports a
	// no-VSP purchase, the auto-buyer does not.
	allowDirectBuy bool
}

func NewVSPSelector(l *load.Load, dcrWallet *dcr.Asset) *VSPSelector {
	v := &VSPSelector{
		Load:         l,
		dcrWallet:    dcrWallet,
		showVSPModal: l.Theme.NewClickable(true),
	}
	return v
}

func (v *VSPSelector) Title(title string) *VSPSelector {
	v.dialogTitle = title
	return v
}

// AllowDirectBuy enables the synthetic "Direct buy (solo)" picker entry.
func (v *VSPSelector) AllowDirectBuy() *VSPSelector {
	v.allowDirectBuy = true
	return v
}

func (v *VSPSelector) Changed() bool {
	changed := v.changed
	v.changed = false
	return changed
}

func (v *VSPSelector) SelectVSP(vspHost string) {
	// An empty host is the Direct-buy sentinel — but only when this selector
	// opted into it (the auto-buyer restores its saved config host through
	// here, and an unset config must not preselect Direct buy).
	if vspHost == "" {
		if v.allowDirectBuy {
			v.changed = true
			v.selectedVSP = dcr.DirectBuyVSP
		}
		return
	}
	for _, vsp := range v.dcrWallet.KnownVSPs() {
		if vsp.Host == vspHost {
			v.changed = true
			v.selectedVSP = vsp
			break
		}
	}
}

func (v *VSPSelector) SelectedVSP() *dcr.VSP {
	return v.selectedVSP
}

func (v *VSPSelector) handle(gtx C, window app.WindowNavigator) {
	if v.showVSPModal.Clicked(gtx) {
		modal := newVSPSelectorModal(v.Load, v.dcrWallet).
			title(values.String(values.StrVotingServiceProvider)).
			allowDirectBuy(v.allowDirectBuy).
			selected(v.selectedVSP).
			vspSelected(func(info *dcr.VSP) {
				v.SelectVSP(info.Host)
			})
		window.ShowModal(modal)
	}
}

func (v *VSPSelector) Layout(window app.WindowNavigator, gtx C) D {
	v.handle(gtx, window)

	border := widget.Border{
		Color:        v.Theme.Color.Gray2,
		CornerRadius: values.MarginPadding8,
		Width:        values.MarginPadding2,
	}

	return border.Layout(gtx, func(gtx C) D {
		textSize16 := values.TextSizeTransform(v.IsMobileView(), values.TextSize16)
		return layout.UniformInset(values.MarginPadding12).Layout(gtx, func(gtx C) D {
			return v.showVSPModal.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						if v.selectedVSP == nil {
							txt := v.Theme.Label(textSize16, values.String(values.StrSelectVSP))
							txt.Color = v.Theme.Color.GrayText3
							return txt.Layout(gtx)
						}
						if v.selectedVSP.IsDirectBuy() {
							return v.Theme.Label(textSize16, values.String(values.StrDirectBuy)).Layout(gtx)
						}
						return v.Theme.Label(textSize16, v.selectedVSP.Host).Layout(gtx)
					}),
					layout.Flexed(1, func(gtx C) D {
						return layout.E.Layout(gtx, func(gtx C) D {
							return layout.Flex{}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									// FeePercentage promotes from the embedded
									// *VspInfoResponse, which is nil for the
									// Direct-buy sentinel — no fee to show.
									if v.selectedVSP == nil || v.selectedVSP.VspInfoResponse == nil {
										return D{}
									}
									txt := v.Theme.Label(textSize16, fmt.Sprintf("%v%%", v.selectedVSP.FeePercentage))
									return txt.Layout(gtx)
								}),
								layout.Rigid(func(gtx C) D {
									inset := layout.Inset{
										Left: values.MarginPadding15,
									}
									return inset.Layout(gtx, func(gtx C) D {
										ic := cryptomaterial.NewIcon(v.Theme.Icons.DropDownIcon)
										ic.Color = v.Theme.Color.Gray1
										return ic.Layout(gtx, values.MarginPadding20)
									})
								}),
							)
						})
					}),
				)
			})
		})
	})
}

type vspSelectorModal struct {
	*load.Load
	*cryptomaterial.Modal

	dialogTitle string

	inputVSP cryptomaterial.Editor
	addVSP   cryptomaterial.Button

	selectedVSP *dcr.VSP
	vspList     *cryptomaterial.ClickableList

	vspSelectedCallback func(*dcr.VSP)
	// showDirectBuy prepends the synthetic "Direct buy (solo)" row.
	showDirectBuy bool

	dcrImpl *dcr.Asset

	materialLoader material.LoaderStyle
	// isLoadingVSP is written from the ReloadVSPList goroutine and read by
	// Layout — atomic to avoid the §3 data race.
	isLoadingVSP atomic.Bool
}

func newVSPSelectorModal(l *load.Load, dcrWallet *dcr.Asset) *vspSelectorModal {
	v := &vspSelectorModal{
		Load:  l,
		Modal: l.Theme.ModalFloatTitle("VSPSelectorModal", l.IsMobileView(), nil),

		inputVSP:       l.Theme.Editor(new(widget.Editor), values.String(values.StrAddVSP)),
		addVSP:         l.Theme.Button(values.String(values.StrSave)),
		vspList:        l.Theme.NewClickableList(layout.Vertical),
		dcrImpl:        dcrWallet,
		materialLoader: material.Loader(l.Theme.Base),
	}
	v.inputVSP.Editor.SingleLine = true

	v.addVSP.SetEnabled(false)

	return v
}

func (v *vspSelectorModal) OnResume() {
	if len(v.dcrImpl.KnownVSPs()) == 0 {
		go func() {
			v.isLoadingVSP.Store(true) // This is used to set the UI to loading VSP state.
			v.dcrImpl.ReloadVSPList(context.TODO())
			// set isLoadingVSP to false, this indicates to the UI that we are done
			// loading vsp(s)
			v.isLoadingVSP.Store(false)
			v.ParentWindow().Reload()
		}()
	}
}

func (v *vspSelectorModal) Handle(gtx C) {
	v.addVSP.SetEnabled(v.editorsNotEmpty(v.inputVSP.Editor))
	if v.addVSP.Clicked(gtx) {
		if !utils.ValidateHost(v.inputVSP.Editor.Text()) {
			v.inputVSP.SetError(values.StringF(values.StrValidateHostErr, v.inputVSP.Editor.Text()))
			return
		}
		go func() {
			err := v.dcrImpl.SaveVSP(v.inputVSP.Editor.Text())
			if err != nil {
				errModal := modal.NewErrorModal(v.Load, err.Error(), modal.DefaultClickFunc())
				v.ParentWindow().ShowModal(errModal)
			} else {
				v.inputVSP.Editor.SetText("")
			}
		}()
	}

	if v.Modal.BackdropClicked(gtx, true) {
		v.Dismiss()
	}

	if clicked, selectedItem := v.vspList.ItemClicked(); clicked {
		// Snapshot once and bounds-check: ReloadVSPList runs in a goroutine and
		// can replace the list with a shorter one between the click frame and
		// now, making selectedItem out of range. With Direct buy enabled the
		// synthetic row occupies index 0 and real VSPs shift by one.
		vsps := v.dcrImpl.KnownVSPs()
		if v.showDirectBuy {
			if selectedItem == 0 {
				v.selectedVSP = dcr.DirectBuyVSP
				v.vspSelectedCallback(v.selectedVSP)
				v.Dismiss()
				return
			}
			selectedItem--
		}
		if selectedItem >= 0 && selectedItem < len(vsps) {
			v.selectedVSP = vsps[selectedItem]
			v.vspSelectedCallback(v.selectedVSP)
			v.Dismiss()
		}
	}
}

func (v *vspSelectorModal) title(title string) *vspSelectorModal {
	v.dialogTitle = title
	return v
}

func (v *vspSelectorModal) vspSelected(callback func(*dcr.VSP)) *vspSelectorModal {
	v.vspSelectedCallback = callback
	return v
}

func (v *vspSelectorModal) allowDirectBuy(allow bool) *vspSelectorModal {
	v.showDirectBuy = allow
	return v
}

// selected seeds the modal's current selection from the collapsed control so
// the checkmark marks the active entry. Without this the modal always opened
// with selectedVSP == nil (it is recreated on every open and only assigned on
// the click that immediately dismisses it), so the checkmark never rendered.
func (v *vspSelectorModal) selected(vsp *dcr.VSP) *vspSelectorModal {
	v.selectedVSP = vsp
	return v
}

func (v *vspSelectorModal) Layout(gtx C) D {
	textSize20 := values.TextSizeTransform(v.IsMobileView(), values.TextSize20)
	textSize14 := values.TextSizeTransform(v.IsMobileView(), values.TextSize14)
	textSize16 := values.TextSizeTransform(v.IsMobileView(), values.TextSize16)
	return v.Modal.Layout(gtx, []layout.Widget{
		func(gtx C) D {
			title := v.Theme.Label(textSize20, v.dialogTitle)
			// Override title when VSP is loading.
			if v.isLoadingVSP.Load() {
				title = v.Theme.Label(textSize20, values.String(values.StrLoadingVSP))
			}
			title.Font.Weight = font.SemiBold
			return title.Layout(gtx)
		},
		func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					// Return 0 dimension if VSP is loading.
					if v.isLoadingVSP.Load() {
						return D{}
					}

					txt := v.Theme.Label(textSize14, values.String(values.StrAddress))
					txt.Color = v.Theme.Color.GrayText2
					txtFee := v.Theme.Label(textSize14, values.String(values.StrFee))
					txtFee.Color = v.Theme.Color.GrayText2
					return EndToEndRow(gtx, txt.Layout, txtFee.Layout)
				}),
				layout.Rigid(func(gtx C) D {
					// if VSP(s) are being loaded, show loading UI — unless the
					// Direct-buy row is enabled: it needs no network, so keep
					// the list (with Direct buy clickable) and show a smaller
					// loader below it instead.
					if v.isLoadingVSP.Load() && !v.showDirectBuy {
						return layout.UniformInset(values.MarginPadding140).Layout(gtx, v.materialLoader.Layout)
					}

					// if no vsp loaded, display a no vsp text (the synthetic
					// Direct-buy row still renders when enabled).
					vsps := v.dcrImpl.KnownVSPs()
					if len(vsps) == 0 && !v.showDirectBuy && !v.isLoadingVSP.Load() {
						noVsp := v.Theme.Label(textSize14, values.String(values.StrNoVSPLoaded))
						noVsp.Color = v.Theme.Color.GrayText2
						return layout.Inset{Top: values.MarginPadding5}.Layout(gtx, noVsp.Layout)
					}

					rows := len(vsps)
					if v.showDirectBuy {
						rows++ // synthetic "Direct buy (solo)" row at index 0
					}
					return v.vspList.Layout(gtx, rows, func(gtx C, i int) D {
						// Show scrollbar on VSP selector modal
						v.Modal.ShowScrollbar(true)
						if v.showDirectBuy {
							if i == 0 {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Flexed(0.8, func(gtx C) D {
										return layout.Inset{Top: values.MarginPadding12, Bottom: values.MarginPadding12}.Layout(gtx, func(gtx C) D {
											return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
												layout.Rigid(v.Theme.Label(textSize16, values.String(values.StrDirectBuy)).Layout),
												layout.Rigid(func(gtx C) D {
													warn := v.Theme.Label(values.TextSizeTransform(v.IsMobileView(), values.TextSize12), values.String(values.StrDirectBuyWarning))
													warn.Color = v.Theme.Color.Danger
													return layout.Inset{Top: values.MarginPadding4}.Layout(gtx, warn.Layout)
												}),
											)
										})
									}),
									layout.Rigid(func(gtx C) D {
										if v.selectedVSP == nil || !v.selectedVSP.IsDirectBuy() {
											return D{}
										}
										ic := cryptomaterial.NewIcon(v.Theme.Icons.NavigationCheck)
										return ic.Layout(gtx, values.MarginPadding20)
									}),
								)
							}
							i-- // real VSPs are shifted one down by the synthetic row
						}
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(0.8, func(gtx C) D {
								return layout.Inset{Top: values.MarginPadding12, Bottom: values.MarginPadding12}.Layout(gtx, func(gtx C) D {
									txt := v.Theme.Label(textSize14, fmt.Sprintf("%v%%", vsps[i].FeePercentage))
									txt.Color = v.Theme.Color.GrayText1
									return EndToEndRow(gtx, v.Theme.Label(textSize16, vsps[i].Host).Layout, txt.Layout)
								})
							}),
							layout.Rigid(func(gtx C) D {
								if v.selectedVSP == nil || v.selectedVSP.Host != vsps[i].Host {
									return D{}
								}
								ic := cryptomaterial.NewIcon(v.Theme.Icons.NavigationCheck)
								return ic.Layout(gtx, values.MarginPadding20)
							}),
						)
					})
				}),
				layout.Rigid(func(gtx C) D {
					// Small inline loader below the (Direct-buy-only) list while
					// real VSPs are still being fetched.
					if v.isLoadingVSP.Load() && v.showDirectBuy {
						return layout.UniformInset(values.MarginPadding24).Layout(gtx, v.materialLoader.Layout)
					}
					return D{}
				}),
			)
		},
		func(gtx C) D {
			// Return 0 dimension if VSP is loading.
			if v.isLoadingVSP.Load() {
				return D{}
			}

			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, v.inputVSP.Layout),
				layout.Rigid(v.addVSP.Layout),
			)
		},
	})
}

func (v *vspSelectorModal) editorsNotEmpty(editors ...*widget.Editor) bool {
	for _, e := range editors {
		if strings.TrimSpace(e.Text()) == "" {
			return false
		}
	}

	return true
}

func (v *vspSelectorModal) OnDismiss() {}

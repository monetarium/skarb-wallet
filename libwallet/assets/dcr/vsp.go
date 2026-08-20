package dcr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	vspdClient "github.com/decred/vspd/client/v3"
	vspd "github.com/decred/vspd/types/v2"
	"github.com/monetarium/monetarium-node/dcrutil"
	dcrW "github.com/monetarium/monetarium-wallet/wallet"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
)

// builtinVSPs returns the VSP hosts shipped with the wallet for a network.
//
// Decred discovers VSPs through a directory service (api.decred.org/?c=vsp);
// Monetarium runs no such service, and the Decred one only lists VSPs for
// Decred's own chains, so querying it produced hosts that can never serve a
// Monetarium ticket. The list is compiled in instead. Users can still add any
// other host from the VSP selector — those are stored per-wallet in the
// wallet database (vspDbData.SavedHosts) and are merged with this list.
func builtinVSPs(net utils.NetworkType) []string {
	switch net {
	case utils.Testnet:
		return []string{"https://vsp.testnet.monetarium.online"}
	case utils.Mainnet:
		return []string{"https://vsp.monetarium.online"}
	default:
		return nil
	}
}

// vspServesThisNetwork reports whether a VSP's self-declared network is the
// one this wallet is on.
//
// The two sides name testnet differently: vspd reports the chain parameters'
// own name ("testnet3"), while the wallet's NetworkType is the user-facing
// "testnet". Comparing those directly — as both call sites used to, one with
// equality and one with strings.Contains — rejected every testnet VSP,
// including the built-in one, which simply never appeared in the selector.
// Comparing against the chain parameters removes the guesswork: a matching
// VSP runs on the same params and therefore reports exactly this name.
func (asset *Asset) vspServesThisNetwork(vspNetwork string) bool {
	return vspNetwork == asset.chainParams.Name
}

// VSPClient loads or creates a VSP client instance for the specified host.
func (asset *Asset) VSPClient(account int32, host string, pubKey []byte) (*dcrW.VSPClient, error) {
	if !asset.WalletOpened() {
		return nil, utils.ErrDCRNotInitialized
	}

	// Resolve -1 before the cache key, otherwise VSPTicketInfo(-1) and
	// PurchaseTickets(account) created two clients for the same host/account
	// and fee retries could pay from the ticket-buyer account, not the one
	// that bought the ticket. Manual-only users have no auto-buyer account
	// — fall back to default account 0 instead of failing the whole client.
	if account == -1 {
		if asset.IsTicketBuyerAccountSet() {
			account = asset.AutoTicketsBuyerConfig().PurchaseAccount
		} else {
			account = 0
		}
	}
	host = normalizeVSPHost(host)

	asset.vspMu.Lock()
	defer asset.vspMu.Unlock()
	if asset.vspClients == nil {
		asset.vspClients = make(map[string]*dcrW.VSPClient)
	}
	// Key by host+account: FeeAcct/ChangeAcct are baked into the client.
	cacheKey := fmt.Sprintf("%s#%d", host, account)
	if client, ok := asset.vspClients[cacheKey]; ok {
		client.UpdateMaxFee(asset.vspMaxFee())
		return client, nil
	}

	client, err := asset.createVspClient(account, host, pubKey)
	if err != nil {
		return nil, err
	}
	if life, _ := asset.ShutdownContextWithCancel(); life != nil {
		client.SetLifetime(life)
	}

	asset.vspClients[cacheKey] = client
	return client, nil
}

func (asset *Asset) createVspClient(account int32, host string, pubKey []byte) (*dcrW.VSPClient, error) {
	// The VSP integration moved from the standalone monetarium-wallet/vsp
	// package into the wallet package: config no longer carries the Wallet /
	// Params / Dialer (the wallet is the receiver, the dialer is a NewVSPClient
	// argument), and the policy MaxFee is now a dcrutil.Amount.
	cfg := dcrW.VSPClientConfig{
		URL:    host,
		PubKey: base64.StdEncoding.EncodeToString(pubKey),
	}

	// A caller-supplied account (manual purchase, or the auto-buyer's
	// PurchaseAccount) is honored as-is. Only the -1 sentinel (e.g. from
	// VSPTicketInfo) falls back to the configured ticket-purchase account. The
	// previous `if account != -1` inverted this and clobbered the caller's
	// account with the config account, sending VSP fee/change to the wrong one.
	if account == -1 {
		if asset.IsTicketBuyerAccountSet() {
			account = asset.AutoTicketsBuyerConfig().PurchaseAccount
		} else {
			account = 0
		}
	}

	cfg.Policy = &dcrW.VSPPolicy{
		// Decred's 0.2 coin cap is too low for Monetarium mainnet: VSP
		// fee is 5% of a ~48 VAR ticket ≈ 2.4 VAR, so receiveFeeAddress
		// rejected the quote and no fee tx was created.
		MaxFee:     asset.vspMaxFee(),
		FeeAcct:    uint32(account),
		ChangeAcct: uint32(account),
	}

	return asset.Internal().DCR.NewVSPClient(cfg, log, nil)
}

// vspMaxFee is the highest VSP fee the wallet will pay. Mainnet VSP quotes
// a percentage of ticket price (currently 5%); 20% of the current sdiff with
// a 10 VAR floor covers that and leaves headroom if sdiff rises.
func (asset *Asset) vspMaxFee() dcrutil.Amount {
	const minMaxFee = dcrutil.Amount(10e8) // 10 VAR
	const maxFeePct = 20
	tp, err := asset.TicketPrice()
	if err != nil || tp == nil || tp.TicketPrice <= 0 {
		return minMaxFee
	}
	cap := dcrutil.Amount(tp.TicketPrice) * maxFeePct / 100
	if cap < minMaxFee {
		return minMaxFee
	}
	return cap
}

// KnownVSPs returns a list of known VSPs. This list may be updated by calling
// ReloadVSPList. This method is safe for concurrent access.
func (asset *Asset) KnownVSPs() []*VSP {
	asset.vspMu.RLock()
	defer asset.vspMu.RUnlock()
	return asset.vsps
}

// SaveVSP marks a VSP as known and will be susbequently included as part of
// known VSPs.
func (asset *Asset) SaveVSP(host string) (err error) {
	host = normalizeVSPHost(host)
	if host == "" {
		return fmt.Errorf("empty VSP host")
	}

	// Duplicate against saved hosts AND the in-memory list (builtin VSPs
	// are not in SavedHosts, so adding the shipped host used to succeed
	// and show twice).
	vspDbData := asset.getVSPDBData()
	for _, savedHost := range vspDbData.SavedHosts {
		if normalizeVSPHost(savedHost) == host {
			return fmt.Errorf("duplicate host %s", host)
		}
	}
	for _, known := range asset.KnownVSPs() {
		if known != nil && normalizeVSPHost(known.Host) == host {
			return fmt.Errorf("duplicate host %s", host)
		}
	}

	log.Infof("SaveVSP: fetching vspinfo from %s", host)
	info, err := vspInfo(host)
	if err != nil {
		log.Errorf("SaveVSP: vspinfo %s: %v", host, err)
		return err
	}

	if !asset.vspServesThisNetwork(info.Network) {
		return fmt.Errorf("invalid net %s", info.Network)
	}

	vspDbData.SavedHosts = append(vspDbData.SavedHosts, host)
	vspDbData.RemovedHosts = withoutHost(vspDbData.RemovedHosts, host)
	asset.updateVSPDBData(vspDbData)

	asset.vspMu.Lock()
	asset.vsps = append(asset.vsps, &VSP{Host: host, VspInfoResponse: info})
	asset.vspMu.Unlock()

	log.Infof("SaveVSP: saved %s", host)
	return
}

// DeleteVSP removes host from the known-VSP list. Builtin hosts are not
// deleted from the binary — they are recorded in RemovedHosts so ReloadVSPList
// skips them. Adding the same host again via SaveVSP clears that skip.
func (asset *Asset) DeleteVSP(host string) error {
	host = normalizeVSPHost(host)
	if host == "" {
		return fmt.Errorf("empty VSP host")
	}

	data := asset.getVSPDBData()
	data.SavedHosts = withoutHost(data.SavedHosts, host)
	if !containsHost(data.RemovedHosts, host) {
		data.RemovedHosts = append(data.RemovedHosts, host)
	}
	if normalizeVSPHost(data.LastUsedVSP) == host {
		data.LastUsedVSP = ""
	}
	asset.updateVSPDBData(data)

	asset.vspMu.Lock()
	kept := make([]*VSP, 0, len(asset.vsps))
	for _, v := range asset.vsps {
		if v != nil && normalizeVSPHost(v.Host) != host {
			kept = append(kept, v)
		}
	}
	asset.vsps = kept
	asset.vspMu.Unlock()

	log.Infof("DeleteVSP: removed %s", host)
	return nil
}

func withoutHost(list []string, host string) []string {
	host = normalizeVSPHost(host)
	out := make([]string, 0, len(list))
	for _, h := range list {
		if normalizeVSPHost(h) != host {
			out = append(out, h)
		}
	}
	return out
}

func containsHost(list []string, host string) bool {
	host = normalizeVSPHost(host)
	for _, h := range list {
		if normalizeVSPHost(h) == host {
			return true
		}
	}
	return false
}

// LastUsedVSP returns the host of the last used VSP, as saved by the
// SaveLastUsedVSP() method.
func (asset *Asset) LastUsedVSP() string {
	return asset.getVSPDBData().LastUsedVSP
}

// SaveLastUsedVSP saves the host of the last used VSP.
func (asset *Asset) SaveLastUsedVSP(host string) {
	vspDbData := asset.getVSPDBData()
	vspDbData.LastUsedVSP = host
	asset.updateVSPDBData(vspDbData)
}

type vspDbData struct {
	SavedHosts   []string
	LastUsedVSP  string
	RemovedHosts []string
}

func (asset *Asset) getVSPDBData() *vspDbData {
	vspDbData := new(vspDbData)
	_ = asset.ReadUserConfigValue(sharedW.KnownVSPsConfigKey, vspDbData)
	return vspDbData
}

func (asset *Asset) updateVSPDBData(data *vspDbData) {
	asset.SaveUserConfigValue(sharedW.KnownVSPsConfigKey, data)
}

// ReloadVSPList reloads the list of known VSPs.
// This method makes multiple network calls; should be called in a goroutine
// to prevent blocking the UI thread.
func (asset *Asset) ReloadVSPList(ctx context.Context) {
	log.Debugf("Reloading list of known VSPs")
	defer log.Debugf("Reloaded list of known VSPs")

	vspDbData := asset.getVSPDBData()
	removed := make(map[string]bool, len(vspDbData.RemovedHosts))
	for _, h := range vspDbData.RemovedHosts {
		removed[normalizeVSPHost(h)] = true
	}
	vspList := make(map[string]*vspd.VspInfoResponse)
	for _, host := range vspDbData.SavedHosts {
		if removed[normalizeVSPHost(host)] {
			continue
		}
		vspInfo, err := vspInfo(host)
		if err != nil {
			// User saved this VSP. Log an error message.
			log.Errorf("get vsp info error for %s: %v", host, err)
		} else {
			vspList[host] = vspInfo
		}
		if ctx.Err() != nil {
			return // context canceled, abort
		}
	}

	for _, host := range builtinVSPs(asset.NetType()) {
		if removed[normalizeVSPHost(host)] {
			continue
		}
		if _, wasAdded := vspList[host]; wasAdded {
			continue // the user saved this one too
		}

		vspInfo, err := vspInfo(host)
		if err != nil {
			// A shipped host being unreachable is not the user's problem to
			// see: it is either down or not deployed yet. Leave it out of the
			// list and log for diagnostics.
			log.Debugf("get vsp info error for builtin host %s: %v", host, err)
			continue
		}

		if !asset.vspServesThisNetwork(vspInfo.Network) {
			log.Warnf("builtin vsp %s serves network %q, wallet is on %q; skipping",
				host, vspInfo.Network, asset.chainParams.Name)
			continue
		}

		vspList[host] = vspInfo
		if ctx.Err() != nil {
			return // context canceled, abort
		}
	}

	asset.vspMu.Lock()
	asset.vsps = make([]*VSP, 0, len(vspList))
	for host, info := range vspList {
		asset.vsps = append(asset.vsps, &VSP{Host: host, VspInfoResponse: info})
	}
	asset.vspMu.Unlock()
}

func normalizeVSPHost(host string) string {
	host = strings.TrimSpace(host)
	u, err := url.Parse(host)
	if err != nil || u.Host == "" {
		return strings.TrimRight(strings.ToLower(host), "/")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func vspInfo(vspHost string) (*vspd.VspInfoResponse, error) {
	req := &utils.ReqConfig{
		Method:    http.MethodGet,
		HTTPURL:   strings.TrimRight(vspHost, "/") + "/api/v3/vspinfo",
		IsRetByte: true,
	}

	respBytes := []byte{}
	resp, err := utils.HTTPRequest(req, &respBytes)
	if err != nil {
		return nil, err
	}

	vspInfoResponse := new(vspd.VspInfoResponse)
	if err := json.Unmarshal(respBytes, vspInfoResponse); err != nil {
		return nil, err
	}

	// Validate server response.
	err = vspdClient.ValidateServerSignature(resp, respBytes, vspInfoResponse.PubKey)
	return vspInfoResponse, err
}

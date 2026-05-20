package dcr

import (
	"time"

	"github.com/monetarium/monetarium-wallet/errors"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
)

// slowListenerWatchdog logs a warning when a tx/block notification callback
// takes longer than this. UI-side handlers that hold gioui locks past a few
// hundred ms freeze the wallet visibly; the watchdog turns silent freezes
// into traceable [WARN] lines pointing at the offending listener.
const slowListenerWatchdog = 2 * time.Second

func (asset *Asset) listenForTransactions() {
	go func() {
		n := asset.Internal().DCR.NtfnServer.TransactionNotifications()

		for {
			select {
			case v := <-n.C:
				if v == nil {
					return
				}
				batchStart := time.Now()
				if len(v.UnminedTransactions) > 0 || len(v.AttachedBlocks) > 0 {
					log.Infof("[%d] TX notification batch: unmined=%d attached_blocks=%d",
						asset.ID, len(v.UnminedTransactions), len(v.AttachedBlocks))
				}
				for _, transaction := range v.UnminedTransactions {
					tempTransaction, err := asset.decodeTransactionWithTxSummary(&transaction, nil)
					if err != nil {
						log.Errorf("[%d] Error ntfn parse tx: %v", asset.ID, err)
						return
					}

					overwritten, err := asset.GetWalletDataDb().SaveOrUpdate(&sharedW.Transaction{}, tempTransaction)
					if err != nil {
						log.Errorf("[%d] New Tx save err: %v", asset.ID, err)
						return
					}

					if !overwritten {
						log.Infof("[%d] New Transaction %s (direction=%d amount=%d type=%s)",
							asset.ID, tempTransaction.Hash, tempTransaction.Direction,
							tempTransaction.Amount, tempTransaction.Type)
						asset.mempoolTransactionNotification(tempTransaction)
					}
				}

				for _, block := range v.AttachedBlocks {
					blockHash := block.Header.BlockHash()
					for _, transaction := range block.Transactions {
						tempTransaction, err := asset.decodeTransactionWithTxSummary(&transaction, &blockHash)
						if err != nil {
							log.Errorf("[%d] Error ntfn parse tx: %v", asset.ID, err)
							return
						}

						_, err = asset.GetWalletDataDb().SaveOrUpdate(&sharedW.Transaction{}, tempTransaction)
						if err != nil {
							log.Errorf("[%d] Incoming block replace tx error :%v", asset.ID, err)
							return
						}
						log.Infof("[%d] Tx confirmed %s at height %d",
							asset.ID, transaction.Hash.String(), block.Header.Height)
						asset.publishTransactionConfirmed(transaction.Hash.String(), int32(block.Header.Height))
					}

					log.Debugf("[%d] Block attached: height=%d txs=%d",
						asset.ID, block.Header.Height, len(block.Transactions))
					asset.publishBlockAttached(int32(block.Header.Height))
				}

				if len(v.AttachedBlocks) > 0 {
					asset.checkWalletMixers()
				}

				if elapsed := time.Since(batchStart); elapsed > slowListenerWatchdog {
					log.Warnf("[%d] TX notification batch took %s — UI may have observed a freeze",
						asset.ID, elapsed)
				}

			case <-asset.syncData.syncCanceled:
				n.Done()
			}
		}
	}()
}

// AddTxAndBlockNotificationListener registers a set of functions to be invoked
// when a transaction or block update is processed by the asset. If async is
// true, the provided callback methods will be called from separate goroutines,
// allowing notification senders to continue their operation without waiting
// for the listener to complete processing the notification. This asyncrhonous
// handling is especially important for cases where the wallet process that
// sends the notification temporarily prevents access to other wallet features
// until all notification handlers finish processing the notification. If a
// notification handler were to try to access such features, it would result
// in a deadlock.
func (asset *Asset) AddTxAndBlockNotificationListener(txAndBlockNotificationListener *sharedW.TxAndBlockNotificationListener, uniqueIdentifier string) error {
	asset.notificationListenersMu.Lock()
	defer asset.notificationListenersMu.Unlock()

	_, ok := asset.txAndBlockNotificationListeners[uniqueIdentifier]
	if ok {
		return errors.New(utils.ErrListenerAlreadyExist)
	}

	asset.txAndBlockNotificationListeners[uniqueIdentifier] = txAndBlockNotificationListener
	return nil
}

func (asset *Asset) RemoveTxAndBlockNotificationListener(uniqueIdentifier string) {
	asset.notificationListenersMu.Lock()
	defer asset.notificationListenersMu.Unlock()

	delete(asset.txAndBlockNotificationListeners, uniqueIdentifier)
}

func (asset *Asset) checkWalletMixers() {
	if asset.IsAccountMixerActive() {
		unmixedAccount := asset.ReadInt32ConfigValueForKey(sharedW.AccountMixerUnmixedAccount, -1)
		hasMixableOutput := asset.accountHasMixableOutput(unmixedAccount)
		if !hasMixableOutput {
			log.Infof("[%d] unmixed account does not have a mixable output, stopping account mixer", asset.ID)
			err := asset.StopAccountMixer()
			if err != nil {
				log.Errorf("Error stopping account mixer: %v", err)
			}
		}
	}
}

// runListenerWatched fires listener body in a fresh goroutine and emits a
// [WARN] line if it takes longer than slowListenerWatchdog. listenerID is the
// uniqueIdentifier the caller registered with — surfaces *which* UI page is
// holding things up, not just "some listener was slow".
func (asset *Asset) runListenerWatched(event, listenerID string, body func()) {
	go func() {
		start := time.Now()
		done := make(chan struct{})
		go func() {
			defer close(done)
			body()
		}()
		select {
		case <-done:
			if elapsed := time.Since(start); elapsed > slowListenerWatchdog {
				log.Warnf("[%d] %s listener %q took %s — likely cause of perceived UI freeze",
					asset.ID, event, listenerID, elapsed)
			}
		case <-time.After(slowListenerWatchdog):
			log.Warnf("[%d] %s listener %q running longer than %s — UI freeze in progress",
				asset.ID, event, listenerID, slowListenerWatchdog)
			<-done
			log.Warnf("[%d] %s listener %q completed after %s",
				asset.ID, event, listenerID, time.Since(start))
		}
	}()
}

func (asset *Asset) mempoolTransactionNotification(transaction *sharedW.Transaction) {
	asset.notificationListenersMu.RLock()
	defer asset.notificationListenersMu.RUnlock()

	for id, listener := range asset.txAndBlockNotificationListeners {
		if listener.OnTransaction != nil {
			id, listener := id, listener
			asset.runListenerWatched("OnTransaction", id, func() {
				listener.OnTransaction(asset.ID, transaction)
			})
		}
	}
}

func (asset *Asset) publishTransactionConfirmed(transactionHash string, blockHeight int32) {
	asset.notificationListenersMu.RLock()
	defer asset.notificationListenersMu.RUnlock()

	for id, listener := range asset.txAndBlockNotificationListeners {
		if listener.OnTransactionConfirmed != nil {
			id, listener := id, listener
			asset.runListenerWatched("OnTransactionConfirmed", id, func() {
				listener.OnTransactionConfirmed(asset.ID, transactionHash, blockHeight)
			})
		}
	}
}

func (asset *Asset) publishBlockAttached(blockHeight int32) {
	asset.notificationListenersMu.RLock()
	defer asset.notificationListenersMu.RUnlock()

	for id, listener := range asset.txAndBlockNotificationListeners {
		if listener.OnBlockAttached != nil {
			id, listener := id, listener
			asset.runListenerWatched("OnBlockAttached", id, func() {
				listener.OnBlockAttached(asset.ID, blockHeight)
			})
		}
	}
}

func (asset *Asset) IsNotificationListenerExist(uniqueIdentifier string) bool {
	_, ok := asset.txAndBlockNotificationListeners[uniqueIdentifier]
	return ok
}

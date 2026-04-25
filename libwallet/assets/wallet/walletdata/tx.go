package walletdata

import "go.etcd.io/bbolt"

type transaction struct {
	boltTx *bbolt.Tx
}

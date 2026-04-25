package walletdata

import (
	"go.etcd.io/bbolt"
)

type bucket struct {
	upstream *bbolt.Bucket
}

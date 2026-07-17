package walletdata

import (
	"fmt"
	"reflect"

	"github.com/monetarium/monetarium-wallet/errors"
	"github.com/asdine/storm"
)

const KeyEndBlock = "EndBlock"

// SaveOrUpdate saves a transaction to the database and would overwrite
// if a transaction with same hash exists
func (db *DB) SaveOrUpdate(emptyTxPointer, record interface{}) (overwritten bool, err error) {
	return saveOrUpdate(db.walletDataDB, emptyTxPointer, record)
}

// saveOrUpdate runs the read-compare-delete-save dance on any storm
// node: the live DB (one bbolt write transaction per operation) or an
// open BatchTx (all operations share a single transaction).
func saveOrUpdate(node storm.Node, emptyTxPointer, record interface{}) (overwritten bool, err error) {
	v := reflect.ValueOf(record)
	txHash := reflect.Indirect(v).FieldByName("Hash").String()
	err = node.One("Hash", txHash, emptyTxPointer)
	if err != nil && err != storm.ErrNotFound {
		err = errors.Errorf("error checking if record was already indexed: %s", err.Error())
		return
	}

	v2 := reflect.ValueOf(emptyTxPointer)
	timestamp := reflect.Indirect(v2).FieldByName("Timestamp").Int()
	txlabel := reflect.Indirect(v2).FieldByName("Label").String()

	if timestamp > 0 {
		overwritten = true
		// delete old record before saving new (if it exists)
		_ = node.DeleteStruct(emptyTxPointer)
	}

	if txlabel != "" {
		// Must be a transaction we are dealing with so update the Label field value.
		// Persist the tx labels here since they are not sent via the network.
		// Tx labels are only local to the specific wallet that uses them.
		v.Elem().FieldByName("Label").SetString(txlabel)
	}

	err = node.Save(record)
	return
}

// BatchTx accumulates writes in one storm/bbolt transaction — a single
// fsync per Commit instead of one (or three) per record. The initial
// tx-index pass over a whole history is disk-bound on exactly that,
// which made first sync crawl on slow (phone) storage.
type BatchTx struct {
	node storm.Node
}

// BeginBatch opens a write transaction. bbolt allows one writer at a
// time: other writers block until Commit/Rollback, so keep batches
// short-lived and do any decoding work outside of them.
func (db *DB) BeginBatch() (*BatchTx, error) {
	node, err := db.walletDataDB.Begin(true)
	if err != nil {
		return nil, err
	}
	return &BatchTx{node: node}, nil
}

// SaveOrUpdate mirrors DB.SaveOrUpdate inside the batch transaction.
func (b *BatchTx) SaveOrUpdate(emptyTxPointer, record interface{}) (overwritten bool, err error) {
	return saveOrUpdate(b.node, emptyTxPointer, record)
}

// SaveLastIndexPoint mirrors DB.SaveLastIndexPoint inside the batch, so
// the resume checkpoint commits atomically with the rows it covers.
func (b *BatchTx) SaveLastIndexPoint(endBlockHeight int32) error {
	return saveLastIndexPoint(b.node, endBlockHeight)
}

func (b *BatchTx) Commit() error   { return b.node.Commit() }
func (b *BatchTx) Rollback() error { return b.node.Rollback() }

func (db *DB) SaveOrUpdateVspdRecord(emptyTxPointer, record interface{}) (updated bool, err error) {
	v := reflect.ValueOf(record)
	txHash := reflect.Indirect(v).FieldByName("Hash").String()
	err = db.walletDataDB.One("Hash", txHash, emptyTxPointer)
	if err != nil && err != storm.ErrNotFound {
		err = errors.Errorf("error checking if record was already indexed: %s", err.Error())
		return
	}
	if err == storm.ErrNotFound {
		err = db.walletDataDB.Save(record)
		return
	}

	updated = true
	err = db.walletDataDB.Update(record)
	return
}

func (db *DB) LastIndexPoint() (int32, error) {
	var endBlockHeight int32
	err := db.walletDataDB.Get(TxBucketName, KeyEndBlock, &endBlockHeight)
	if err != nil && err != storm.ErrNotFound {
		return 0, err
	}

	return endBlockHeight, nil
}

func (db *DB) SaveLastIndexPoint(endBlockHeight int32) error {
	return saveLastIndexPoint(db.walletDataDB, endBlockHeight)
}

func saveLastIndexPoint(node storm.Node, endBlockHeight int32) error {
	err := node.Set(TxBucketName, KeyEndBlock, &endBlockHeight)
	if err != nil {
		return fmt.Errorf("error setting block height for last indexed tx: %s", err.Error())
	}
	return nil
}

func (db *DB) ClearSavedTransactions(emptyTxPointer interface{}) error {
	err := db.walletDataDB.Drop(emptyTxPointer)
	if err != nil {
		return err
	}

	return db.SaveLastIndexPoint(0)
}

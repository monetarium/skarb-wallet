// pvertest demonstrates the bug we just found: at pver=11
// (BatchedCFiltersV2Version), an SKA-containing block does NOT round-trip
// through wire.WriteMessage/wire.ReadMessage in a way that preserves its
// committed merkle root, because CoinType (DualCoinVersion=12) and the
// variable-length SKA value (SKABigIntVersion=13) are silently dropped.
package main

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/monetarium/monetarium-node/blockchain/standalone"
	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/monetarium-node/wire"
)

func main() {
	params := chaincfg.TestNet3Params()

	mkSKATx := func() *wire.MsgTx {
		tx := wire.NewMsgTx()
		tx.Version = 1
		in := wire.NewTxIn(&wire.OutPoint{Index: 0, Tree: wire.TxTreeRegular}, 0, nil)
		in.SKAValueIn = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
		in.SignatureScript = []byte{0x01, 0x02}
		tx.AddTxIn(in)
		tx.AddTxOut(wire.NewTxOutSKA(big.NewInt(500_000_000), cointype.CoinType(1),
			[]byte{0x76, 0xa9, 0x14, 0xde, 0xad, 0xbe, 0xef, 0xde, 0xad, 0xbe, 0xef,
				0xde, 0xad, 0xbe, 0xef, 0xde, 0xad, 0xbe, 0xef, 0xde, 0xad, 0xbe, 0xef,
				0x88, 0xac}))
		return tx
	}

	for _, pver := range []uint32{
		wire.BatchedCFiltersV2Version, // 11 -- the buggy value
		wire.DualCoinVersion,          // 12 -- adds CoinType but no SKA bigint
		wire.SKABigIntVersion,         // 13 -- correct one
	} {
		tx := mkSKATx()
		before := tx.TxHashFull()

		var buf bytes.Buffer
		_ = wire.WriteMessage(&buf, tx, pver, params.Net)
		decoded, _, err := wire.ReadMessage(&buf, pver, params.Net)
		if err != nil {
			fmt.Printf("pver=%-2d  WRITE/READ ERR: %v\n", pver, err)
			continue
		}
		rx := decoded.(*wire.MsgTx)
		after := rx.TxHashFull()

		merkleBefore := standalone.CalcTxTreeMerkleRoot([]*wire.MsgTx{tx})
		merkleAfter := standalone.CalcTxTreeMerkleRoot([]*wire.MsgTx{rx})

		match := before == after
		mmatch := merkleBefore == merkleAfter
		fmt.Printf("pver=%-2d  TxHashFull preserved? %v   merkle preserved? %v\n",
			pver, match, mmatch)
		fmt.Printf("           rxTxOut[0]: coinType=%d value=%d skaVal=%v\n",
			rx.TxOut[0].CoinType, rx.TxOut[0].Value, rx.TxOut[0].SKAValue)
		fmt.Printf("           rxTxIn [0]: skaIn=%v sigLen=%d\n",
			rx.TxIn[0].SKAValueIn, len(rx.TxIn[0].SignatureScript))
		fmt.Println()
	}
}

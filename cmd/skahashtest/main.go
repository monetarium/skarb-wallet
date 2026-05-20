// skahashtest reproduces the round-trip path the dcrwallet SPV mode takes
// when it receives a tx: parse from wire bytes, then ask for TxHash() and
// compare to what was on the wire. If the hash changes after the round-trip,
// dcrwallet will reject the tx as "received unrequested tx" because its
// requestedTxs map was keyed by the wire-side hash.
//
// We construct a synthetic SKA1 MsgTx with various SKAValue magnitudes (and
// for symmetry a VAR control), encode it via wire.WriteMessage exactly like a
// peer would push, then read it back via wire.ReadMessage and verify both
// .TxHash() invocations agree.
package main

import (
	"bytes"
	"fmt"
	"math/big"
	"os"

	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/monetarium-node/wire"
)

func main() {
	pkScript := []byte{0x76, 0xa9, 0x14, 0xde, 0xad, 0xbe, 0xef, 0xde, 0xad, 0xbe, 0xef,
		0xde, 0xad, 0xbe, 0xef, 0xde, 0xad, 0xbe, 0xef, 0xde, 0xad, 0xbe, 0xef,
		0x88, 0xac}

	cases := []struct {
		label string
		mkTx  func() *wire.MsgTx
	}{
		{"VAR 1.5 coin", func() *wire.MsgTx {
			tx := wire.NewMsgTx()
			tx.Version = 1
			tx.AddTxIn(synInput())
			tx.AddTxOut(wire.NewTxOutWithCoinType(150_000_000, cointype.CoinTypeVAR, pkScript))
			return tx
		}},
		{"SKA1 1 atom", func() *wire.MsgTx {
			tx := wire.NewMsgTx()
			tx.Version = 1
			tx.AddTxIn(synInput())
			tx.AddTxOut(wire.NewTxOutSKA(big.NewInt(1), cointype.CoinType(1), pkScript))
			return tx
		}},
		{"SKA1 1.5e18 atoms (~typical 1.5 coin)", func() *wire.MsgTx {
			tx := wire.NewMsgTx()
			tx.Version = 1
			tx.AddTxIn(synInput())
			v := new(big.Int).Mul(big.NewInt(15), new(big.Int).Exp(big.NewInt(10), big.NewInt(17), nil))
			tx.AddTxOut(wire.NewTxOutSKA(v, cointype.CoinType(1), pkScript))
			return tx
		}},
		{"SKA1 value with leading-zero byte (0x00FF)", func() *wire.MsgTx {
			// Manually build a TxOut where SKAValue is set from bytes including
			// leading zero. This stress-tests whether SetBytes/Bytes round-trip
			// preserves length (i.e. whether the wire format ever transmits
			// non-minimal SKA encodings).
			tx := wire.NewMsgTx()
			tx.Version = 1
			tx.AddTxIn(synInput())
			ska := new(big.Int).SetBytes([]byte{0x00, 0xFF}) // == 255 numerically
			tx.AddTxOut(wire.NewTxOutSKA(ska, cointype.CoinType(1), pkScript))
			return tx
		}},
	}

	failed := 0
	for _, c := range cases {
		tx := c.mkTx()
		hashBefore := tx.TxHash()

		// Send: wire.WriteMessage uses ProtocolVersion (=13) and TxSerializeFull.
		var buf bytes.Buffer
		if err := wire.WriteMessage(&buf, tx, wire.ProtocolVersion, wire.MainNet); err != nil {
			fmt.Printf("%-40s WRITE ERR: %v\n", c.label, err)
			failed++
			continue
		}
		// Receive: read back via wire.ReadMessage as a peer would.
		decoded, _, err := wire.ReadMessage(&buf, wire.ProtocolVersion, wire.MainNet)
		if err != nil {
			fmt.Printf("%-40s READ ERR: %v\n", c.label, err)
			failed++
			continue
		}
		rxTx, ok := decoded.(*wire.MsgTx)
		if !ok {
			fmt.Printf("%-40s NOT MsgTx: %T\n", c.label, decoded)
			failed++
			continue
		}
		hashAfter := rxTx.TxHash()

		status := "✓"
		if hashBefore != hashAfter {
			status = "✗ HASH MISMATCH"
			failed++
		}
		fmt.Printf("%-40s %s\n  before=%s\n  after =%s\n", c.label, status, hashBefore, hashAfter)
	}

	if failed > 0 {
		fmt.Printf("\n❌ %d case(s) failed round-trip — wallet will reject these as 'received unrequested tx'\n", failed)
		os.Exit(1)
	}
	fmt.Println("\n✅ All cases round-trip cleanly. Wire-level encode/decode is not the bug source.")
}

func synInput() *wire.TxIn {
	op := wire.OutPoint{Hash: chainhash.Hash{1, 2, 3}, Index: 0, Tree: wire.TxTreeRegular}
	in := wire.NewTxIn(&op, 0, nil)
	in.SignatureScript = []byte{0x01, 0x02}
	in.Sequence = 0xffffffff
	return in
}

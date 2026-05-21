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

	synSKAInput := func(ska *big.Int) *wire.TxIn {
		in := synInput()
		in.SKAValueIn = ska
		return in
	}
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
		{"SKA1 1 atom (output only)", func() *wire.MsgTx {
			tx := wire.NewMsgTx()
			tx.Version = 1
			tx.AddTxIn(synInput())
			tx.AddTxOut(wire.NewTxOutSKA(big.NewInt(1), cointype.CoinType(1), pkScript))
			return tx
		}},
		{"SKA1 1.5e18 atoms (output only)", func() *wire.MsgTx {
			tx := wire.NewMsgTx()
			tx.Version = 1
			tx.AddTxIn(synInput())
			v := new(big.Int).Mul(big.NewInt(15), new(big.Int).Exp(big.NewInt(10), big.NewInt(17), nil))
			tx.AddTxOut(wire.NewTxOutSKA(v, cointype.CoinType(1), pkScript))
			return tx
		}},
		{"SKA1 leading-zero bytes (0x00FF) output", func() *wire.MsgTx {
			tx := wire.NewMsgTx()
			tx.Version = 1
			tx.AddTxIn(synInput())
			ska := new(big.Int).SetBytes([]byte{0x00, 0xFF})
			tx.AddTxOut(wire.NewTxOutSKA(ska, cointype.CoinType(1), pkScript))
			return tx
		}},
		// Realistic SKA tx — input HAS SKAValueIn populated (this is what
		// signed SKA spends actually look like on the wire). The block-
		// merkle-tree path uses TxHashFull(), which includes witness, so
		// a round-trip glitch on SKAValueIn is the prime suspect for the
		// DCP0005MerkleRoot validation failure we're seeing.
		{"SKA1 spend with SKAValueIn=1e18 (full witness)", func() *wire.MsgTx {
			tx := wire.NewMsgTx()
			tx.Version = 1
			tx.AddTxIn(synSKAInput(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)))
			tx.AddTxOut(wire.NewTxOutSKA(big.NewInt(500_000_000), cointype.CoinType(1), pkScript))
			return tx
		}},
		{"SKA1 spend with SKAValueIn leading-zero byte", func() *wire.MsgTx {
			tx := wire.NewMsgTx()
			tx.Version = 1
			tx.AddTxIn(synSKAInput(new(big.Int).SetBytes([]byte{0x00, 0xFF})))
			tx.AddTxOut(wire.NewTxOutSKA(big.NewInt(255), cointype.CoinType(1), pkScript))
			return tx
		}},
	}

	failed := 0
	for _, c := range cases {
		tx := c.mkTx()
		hashBefore := tx.TxHash()
		fullHashBefore := tx.TxHashFull()

		// Send: wire.WriteMessage uses ProtocolVersion (=13) and TxSerializeFull.
		var buf bytes.Buffer
		if err := wire.WriteMessage(&buf, tx, wire.ProtocolVersion, wire.MainNet); err != nil {
			fmt.Printf("%-50s WRITE ERR: %v\n", c.label, err)
			failed++
			continue
		}
		// Receive: read back via wire.ReadMessage as a peer would.
		decoded, _, err := wire.ReadMessage(&buf, wire.ProtocolVersion, wire.MainNet)
		if err != nil {
			fmt.Printf("%-50s READ ERR: %v\n", c.label, err)
			failed++
			continue
		}
		rxTx, ok := decoded.(*wire.MsgTx)
		if !ok {
			fmt.Printf("%-50s NOT MsgTx: %T\n", c.label, decoded)
			failed++
			continue
		}
		hashAfter := rxTx.TxHash()
		fullHashAfter := rxTx.TxHashFull()

		prefixOK := hashBefore == hashAfter
		fullOK := fullHashBefore == fullHashAfter
		switch {
		case prefixOK && fullOK:
			fmt.Printf("%-50s ✓ both prefix and full\n", c.label)
		case prefixOK && !fullOK:
			fmt.Printf("%-50s ✗ TxHashFull MISMATCH (witness)\n  full before=%s\n  full after =%s\n",
				c.label, fullHashBefore, fullHashAfter)
			failed++
		case !prefixOK:
			fmt.Printf("%-50s ✗ TxHash MISMATCH (prefix)\n  before=%s\n  after =%s\n",
				c.label, hashBefore, hashAfter)
			failed++
		}
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

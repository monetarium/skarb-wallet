package wallet

import (
	"testing"

	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/txscript"
	"github.com/monetarium/monetarium-wallet/wallet/udb"
)

func testParams() *chaincfg.Params {
	return &chaincfg.Params{
		CoinbaseMaturity:   256,
		SStxChangeMaturity: 1,
	}
}

func TestCreditSpendable(t *testing.T) {
	params := testParams()
	const tip int32 = 1000

	matureHeight := tip - int32(params.CoinbaseMaturity) // 744 → 1000-744+1 = 257 > 256
	immatureHeight := tip - 10                           // 10 confs, still immature

	p2pkh := []byte{txscript.OP_DUP, txscript.OP_HASH160, 0x14}
	sstx := append([]byte{txscript.OP_SSTX}, p2pkh...)
	ssgen := append([]byte{txscript.OP_SSGEN}, p2pkh...)
	sstxChange := append([]byte{txscript.OP_SSTXCHANGE}, p2pkh...)

	tests := []struct {
		name   string
		credit *udb.Credit
		want   bool
	}{
		{
			name: "regular p2pkh",
			credit: &udb.Credit{
				BlockMeta: udb.BlockMeta{Block: udb.Block{Height: tip - 2}},
				PkScript:  p2pkh,
			},
			want: true,
		},
		{
			name: "live ticket never spendable as regular input",
			credit: &udb.Credit{
				BlockMeta: udb.BlockMeta{Block: udb.Block{Height: matureHeight}},
				PkScript:  sstx,
			},
			want: false,
		},
		{
			name: "immature coinbase",
			credit: &udb.Credit{
				BlockMeta:    udb.BlockMeta{Block: udb.Block{Height: immatureHeight}},
				PkScript:     p2pkh,
				FromCoinBase: true,
			},
			want: false,
		},
		{
			name: "mature coinbase",
			credit: &udb.Credit{
				BlockMeta:    udb.BlockMeta{Block: udb.Block{Height: matureHeight}},
				PkScript:     p2pkh,
				FromCoinBase: true,
			},
			want: true,
		},
		{
			name: "immature vote reward (expiry)",
			credit: &udb.Credit{
				BlockMeta: udb.BlockMeta{Block: udb.Block{Height: immatureHeight}},
				PkScript:  p2pkh,
				HasExpiry: true,
			},
			want: false,
		},
		{
			name: "mature vote reward (expiry)",
			credit: &udb.Credit{
				BlockMeta: udb.BlockMeta{Block: udb.Block{Height: matureHeight}},
				PkScript:  p2pkh,
				HasExpiry: true,
			},
			want: true,
		},
		{
			name: "immature ssgen",
			credit: &udb.Credit{
				BlockMeta: udb.BlockMeta{Block: udb.Block{Height: immatureHeight}},
				PkScript:  ssgen,
			},
			want: false,
		},
		{
			name: "mature ssgen",
			credit: &udb.Credit{
				BlockMeta: udb.BlockMeta{Block: udb.Block{Height: matureHeight}},
				PkScript:  ssgen,
			},
			want: true,
		},
		{
			name: "sstxchange at 1 conf is not yet mature (need > 1)",
			credit: &udb.Credit{
				BlockMeta: udb.BlockMeta{Block: udb.Block{Height: tip}},
				PkScript:  sstxChange,
			},
			want: false,
		},
		{
			name: "sstxchange at 2 confs is mature",
			credit: &udb.Credit{
				BlockMeta: udb.BlockMeta{Block: udb.Block{Height: tip - 1}},
				PkScript:  sstxChange,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := creditSpendable(params, tt.credit, tip)
			if got != tt.want {
				t.Fatalf("creditSpendable() = %v, want %v", got, tt.want)
			}
		})
	}
}

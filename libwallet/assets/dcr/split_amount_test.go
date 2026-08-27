package dcr

import (
	"testing"

	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
)

func TestApplySplitAmountsIncludesVSPFee(t *testing.T) {
	splitHash := "splithash"
	ticket := &sharedW.Transaction{
		Type: TxTypeTicketPurchase,
		Inputs: []*sharedW.TxInput{
			{PreviousTransactionHash: splitHash, Amount: 37_0000_0000},
		},
	}
	feeTx := &sharedW.Transaction{
		Hash: "feepayment",
		Type: TxTypeRegular,
		Inputs: []*sharedW.TxInput{
			{PreviousTransactionHash: splitHash, Amount: 27_000_000},
		},
	}
	split := &sharedW.Transaction{
		Hash:      splitHash,
		Type:      TxTypeRegular,
		Direction: TxDirectionTransferred,
		Inputs:    []*sharedW.TxInput{{AccountNumber: 0, Amount: 1}},
		Outputs: []*sharedW.TxOutput{
			{AccountNumber: 0, Amount: 37_0000_0000},
			{AccountNumber: 0, Amount: 27_000_000},
			{AccountNumber: 0, Amount: 100_0000_0000},
		},
		Amount: 1000, // stored fee placeholder
	}

	split.AmountAtoms = "1000"
	ApplySplitAmounts([]*sharedW.Transaction{split, ticket})
	if split.Amount != 37_0000_0000 {
		t.Fatalf("tickets only: got %d, want %d", split.Amount, int64(37_0000_0000))
	}
	if split.AmountAtoms != "" {
		t.Fatalf("tickets only: AmountAtoms still %q, want empty so the UI uses Amount", split.AmountAtoms)
	}

	split.AmountAtoms = "1000"
	ApplySplitAmountsWithFees([]*sharedW.Transaction{split, ticket, feeTx}, func(h string) bool {
		return h == "feepayment"
	})
	want := int64(37_0000_0000 + 27_000_000)
	if split.Amount != want {
		t.Fatalf("tickets+fee: got %d, want %d", split.Amount, want)
	}
	if split.AmountAtoms != "" {
		t.Fatalf("tickets+fee: AmountAtoms still %q, want empty", split.AmountAtoms)
	}
}

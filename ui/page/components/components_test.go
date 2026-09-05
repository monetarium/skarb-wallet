package components

import (
	"testing"

	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/txhelper"
)

func TestUsesTicketLifetimeCountdown(t *testing.T) {
	cases := []struct {
		typ  string
		want bool
	}{
		{txhelper.TxTypeTicketPurchase, true},
		{txhelper.TxTypeVote, false},
		{txhelper.TxTypeRevocation, false},
		{txhelper.TxTypeRegular, false},
		{txhelper.TxTypeCoinBase, false},
	}
	for _, tc := range cases {
		got := usesTicketLifetimeCountdown(&sharedW.Transaction{Type: tc.typ})
		if got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.typ, got, tc.want)
		}
	}
	if usesTicketLifetimeCountdown(nil) {
		t.Fatal("nil tx must not use a lifetime countdown")
	}
}

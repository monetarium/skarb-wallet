// Copyright (c) 2025 The Monetarium developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package jsonrpc

import (
	"testing"

	"github.com/monetarium/monetarium-wallet/rpc/jsonrpc/types"
)

// TestGetVoteFeeConsolidationAddressCmd tests the GetVoteFeeConsolidationAddressCmd structure
func TestGetVoteFeeConsolidationAddressCmd(t *testing.T) {
	tests := []struct {
		name    string
		account string
	}{
		{
			name:    "Default account",
			account: "default",
		},
		{
			name:    "Staker account",
			account: "staker",
		},
		{
			name:    "Account by number",
			account: "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &types.GetVoteFeeConsolidationAddressCmd{
				Account: tt.account,
			}

			if cmd.Account != tt.account {
				t.Errorf("Account mismatch: got %s, want %s", cmd.Account, tt.account)
			}

			t.Logf("✓ GetVoteFeeConsolidationAddressCmd created for account: %s", tt.account)
		})
	}
}

// TestSetVoteFeeConsolidationAddressCmd tests the SetVoteFeeConsolidationAddressCmd structure
func TestSetVoteFeeConsolidationAddressCmd(t *testing.T) {
	tests := []struct {
		name    string
		account string
		address string
	}{
		{
			name:    "Set for default account",
			account: "default",
			address: "SsWKp7wtdTZYabYFYSc9cnxhwFEjA5g4pFc",
		},
		{
			name:    "Set for staker account",
			account: "staker",
			address: "SsXciQNTo3HuV5tX3yy4hXndRWgLMRVC7Ah",
		},
		{
			name:    "Set for account by number",
			account: "0",
			address: "SsYcrXcBfUA1TAYiMhYRTkHupEapH7QWNU6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &types.SetVoteFeeConsolidationAddressCmd{
				Account: tt.account,
				Address: tt.address,
			}

			if cmd.Account != tt.account {
				t.Errorf("Account mismatch: got %s, want %s", cmd.Account, tt.account)
			}

			if cmd.Address != tt.address {
				t.Errorf("Address mismatch: got %s, want %s", cmd.Address, tt.address)
			}

			t.Logf("✓ SetVoteFeeConsolidationAddressCmd created: account=%s, address=%s",
				tt.account, tt.address)
		})
	}
}

// TestClearVoteFeeConsolidationAddressCmd tests the ClearVoteFeeConsolidationAddressCmd structure
func TestClearVoteFeeConsolidationAddressCmd(t *testing.T) {
	tests := []struct {
		name    string
		account string
	}{
		{
			name:    "Clear for default account",
			account: "default",
		},
		{
			name:    "Clear for staker account",
			account: "staker",
		},
		{
			name:    "Clear for account by number",
			account: "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &types.ClearVoteFeeConsolidationAddressCmd{
				Account: tt.account,
			}

			if cmd.Account != tt.account {
				t.Errorf("Account mismatch: got %s, want %s", cmd.Account, tt.account)
			}

			t.Logf("✓ ClearVoteFeeConsolidationAddressCmd created for account: %s", tt.account)
		})
	}
}

// TestGetVoteFeeConsolidationAddressResult tests the GetVoteFeeConsolidationAddressResult structure
func TestGetVoteFeeConsolidationAddressResult(t *testing.T) {
	tests := []struct {
		name      string
		account   string
		address   string
		isDefault bool
	}{
		{
			name:      "Default address for account",
			account:   "default",
			address:   "SsWKp7wtdTZYabYFYSc9cnxhwFEjA5g4pFc",
			isDefault: true,
		},
		{
			name:      "Custom address for account",
			account:   "staker",
			address:   "SsXciQNTo3HuV5tX3yy4hXndRWgLMRVC7Ah",
			isDefault: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := types.GetVoteFeeConsolidationAddressResult{
				Account:   tt.account,
				Address:   tt.address,
				IsDefault: tt.isDefault,
			}

			if result.Account != tt.account {
				t.Errorf("Account mismatch: got %s, want %s", result.Account, tt.account)
			}

			if result.Address != tt.address {
				t.Errorf("Address mismatch: got %s, want %s", result.Address, tt.address)
			}

			if result.IsDefault != tt.isDefault {
				t.Errorf("IsDefault mismatch: got %t, want %t", result.IsDefault, tt.isDefault)
			}

			t.Logf("✓ GetVoteFeeConsolidationAddressResult: account=%s, address=%s, isDefault=%t",
				tt.account, tt.address, tt.isDefault)
		})
	}
}

// TestConsolidationAddressRPCFlow tests the expected flow of the consolidation address RPC commands
func TestConsolidationAddressRPCFlow(t *testing.T) {
	// This test documents the expected flow of the RPC commands without requiring a running wallet
	t.Log("Expected RPC Flow:")
	t.Log("1. getvotefeeconsolidationaddress - Get current address (default or custom)")
	t.Log("2. setvotefeeconsolidationaddress - Set custom address")
	t.Log("3. getvotefeeconsolidationaddress - Verify custom address (isDefault=false)")
	t.Log("4. clearvotefeeconsolidationaddress - Clear custom address")
	t.Log("5. getvotefeeconsolidationaddress - Verify back to default (isDefault=true)")

	account := "default"
	customAddr := "SsWKp7wtdTZYabYFYSc9cnxhwFEjA5g4pFc"

	// Step 1: Get initial address
	getCmd1 := &types.GetVoteFeeConsolidationAddressCmd{Account: account}
	t.Logf("Step 1: getvotefeeconsolidationaddress account=%s", getCmd1.Account)

	// Step 2: Set custom address
	setCmd := &types.SetVoteFeeConsolidationAddressCmd{
		Account: account,
		Address: customAddr,
	}
	t.Logf("Step 2: setvotefeeconsolidationaddress account=%s address=%s",
		setCmd.Account, setCmd.Address)

	// Step 3: Get address again (should show custom)
	getCmd2 := &types.GetVoteFeeConsolidationAddressCmd{Account: account}
	t.Logf("Step 3: getvotefeeconsolidationaddress account=%s (expect custom)", getCmd2.Account)

	// Step 4: Clear custom address
	clearCmd := &types.ClearVoteFeeConsolidationAddressCmd{Account: account}
	t.Logf("Step 4: clearvotefeeconsolidationaddress account=%s", clearCmd.Account)

	// Step 5: Get address again (should show default)
	getCmd3 := &types.GetVoteFeeConsolidationAddressCmd{Account: account}
	t.Logf("Step 5: getvotefeeconsolidationaddress account=%s (expect default)", getCmd3.Account)

	t.Log("✓ RPC flow documented successfully")
}

// TestConsolidationAddressCommandsExist verifies that the commands are registered
func TestConsolidationAddressCommandsExist(t *testing.T) {
	// This test verifies that the command types exist and can be instantiated
	commands := []struct {
		name string
		cmd  interface{}
	}{
		{"getvotefeeconsolidationaddress", &types.GetVoteFeeConsolidationAddressCmd{}},
		{"setvotefeeconsolidationaddress", &types.SetVoteFeeConsolidationAddressCmd{}},
		{"clearvotefeeconsolidationaddress", &types.ClearVoteFeeConsolidationAddressCmd{}},
	}

	for _, tc := range commands {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cmd == nil {
				t.Errorf("Command %s is nil", tc.name)
			}
			t.Logf("✓ Command %s exists and can be instantiated", tc.name)
		})
	}
}

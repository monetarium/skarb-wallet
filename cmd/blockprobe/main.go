// blockprobe fetches a SPECIFIC block from a peer (by hash) and asks the
// hard question: does the block's transactions reproduce the header's
// MerkleRoot when re-hashed locally?
//
// This isolates the DCP0005MerkleRoot consensus failure we see in dcrwallet's
// discovery phase. If the wallet's locally-computed combined merkle root does
// not match the header's committed value, the wallet rejects the entire chain
// at that block. We need to know if:
//
//   (a) The discrepancy is real chain data (peer serves bytes that, when
//       hashed via the current standalone.CalcCombinedTxTreeMerkleRoot, do
//       not match the header), OR
//   (b) Our wallet's wire decoder produces a different in-memory MsgTx than
//       the peer used when the block was sealed.
//
// We print per-tx prefix/full hashes so we can compare them against what the
// node side believes they are (via `dcrctl getblock <hash> 1`).
//
// Usage:
//
//	go run ./cmd/blockprobe -addr 176.9.28.21:19508 -net testnet \
//	    -block ad757b2c9cdc89786e36cec2e3a6b2b467e0dc9e083ed5af4cb897ca7bf7623e
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"encoding/hex"

	"github.com/monetarium/skarb-wallet/libwallet/addresshelper"
	"github.com/monetarium/monetarium-node/blockchain/standalone"
	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
	"github.com/monetarium/monetarium-node/wire"
)

func main() {
	addr := flag.String("addr", "176.9.28.21:19508", "host:port of the peer to fetch from")
	netName := flag.String("net", "testnet", "mainnet | testnet")
	blockHashHex := flag.String("block", "ad757b2c9cdc89786e36cec2e3a6b2b467e0dc9e083ed5af4cb897ca7bf7623e", "block hash to fetch (hex)")
	timeout := flag.Duration("timeout", 20*time.Second, "per-step deadline")
	flag.Parse()

	var params *chaincfg.Params
	switch *netName {
	case "mainnet":
		params = chaincfg.MainNetParams()
	case "testnet":
		params = chaincfg.TestNet3Params()
	default:
		fmt.Fprintf(os.Stderr, "unknown net %q\n", *netName)
		os.Exit(2)
	}

	wantHash, err := chainhash.NewHashFromStr(*blockHashHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad block hash: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("→ Dialing %s (net=%s, magic=%08x)\n", *addr, *netName, uint32(params.Net))
	conn, err := net.DialTimeout("tcp", *addr, *timeout)
	if err != nil {
		fmt.Printf("✘ Dial failed: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(*timeout))

	verMsg, err := wire.NewMsgVersionFromConn(conn, 0xdeadbeef, 0)
	if err != nil {
		fmt.Printf("✘ NewMsgVersion: %v\n", err)
		os.Exit(1)
	}
	verMsg.AddUserAgent("blockprobe", "0.1")
	if err := wire.WriteMessage(conn, verMsg, wire.ProtocolVersion, params.Net); err != nil {
		fmt.Printf("✘ Write version: %v\n", err)
		os.Exit(1)
	}

	// Drain the handshake: version + verack.
	gotVer, gotVerack := false, false
	for !gotVer || !gotVerack {
		_ = conn.SetReadDeadline(time.Now().Add(*timeout))
		msg, _, err := wire.ReadMessage(conn, wire.ProtocolVersion, params.Net)
		if err != nil {
			fmt.Printf("✘ handshake read: %v\n", err)
			os.Exit(1)
		}
		switch msg.(type) {
		case *wire.MsgVersion:
			gotVer = true
		case *wire.MsgVerAck:
			gotVerack = true
		}
	}
	if err := wire.WriteMessage(conn, wire.NewMsgVerAck(), wire.ProtocolVersion, params.Net); err != nil {
		fmt.Printf("✘ Write verack: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("  ✓ handshake done.")

	// getdata block <wantHash>.
	gd := wire.NewMsgGetData()
	_ = gd.AddInvVect(wire.NewInvVect(wire.InvTypeBlock, wantHash))
	if err := wire.WriteMessage(conn, gd, wire.ProtocolVersion, params.Net); err != nil {
		fmt.Printf("✘ Write getdata: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("→ Sent: getdata block=%s\n", wantHash)

	// Read messages until we see a block (or notfound).
	var block *wire.MsgBlock
	deadline := time.Now().Add(*timeout)
	for block == nil && time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		m, _, err := wire.ReadMessage(conn, wire.ProtocolVersion, params.Net)
		if err != nil {
			fmt.Printf("✘ Read while waiting for block: %v\n", err)
			os.Exit(1)
		}
		switch x := m.(type) {
		case *wire.MsgBlock:
			block = x
		case *wire.MsgPing:
			_ = wire.WriteMessage(conn, wire.NewMsgPong(x.Nonce), wire.ProtocolVersion, params.Net)
		case *wire.MsgNotFound:
			fmt.Printf("✘ Peer says block not found: %v\n", x.InvList)
			os.Exit(1)
		default:
			fmt.Printf("← %s (ignoring while waiting for block)\n", m.Command())
		}
	}
	if block == nil {
		fmt.Println("✘ Timed out waiting for block")
		os.Exit(1)
	}

	got := block.BlockHash()
	fmt.Printf("\n← block received: %s\n", got)
	fmt.Printf("  expected      : %s\n", wantHash)
	if got != *wantHash {
		fmt.Println("  ⚠ block hash MISMATCH — peer gave us the wrong block?!")
	} else {
		fmt.Println("  ✓ block hash matches request.")
	}

	hdr := block.Header
	fmt.Printf("\nHeader.MerkleRoot (committed): %s\n", hdr.MerkleRoot)
	fmt.Printf("Header.StakeRoot              : %s\n", hdr.StakeRoot)
	fmt.Printf("Header.Height                 : %d\n", hdr.Height)
	fmt.Printf("Regular txs : %d\n", len(block.Transactions))
	fmt.Printf("Stake    txs: %d\n", len(block.STransactions))

	// Dump per-tx hashes BEFORE doing anything else, so we can compare with
	// dcrctl getblock output if needed.
	fmt.Println("\nRegular tree:")
	for i, tx := range block.Transactions {
		fmt.Printf("  [%d] prefix=%s  full=%s  ins=%d outs=%d\n",
			i, tx.TxHash(), tx.TxHashFull(), len(tx.TxIn), len(tx.TxOut))
		for j, in := range tx.TxIn {
			ska := "nil"
			if in.SKAValueIn != nil {
				ska = in.SKAValueIn.String()
			}
			fmt.Printf("       in[%d]:  valueIn=%d  ska=%s  prev=%s:%d  sigLen=%d\n",
				j, in.ValueIn, ska, in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index, len(in.SignatureScript))
			if len(in.SignatureScript) > 0 {
				fmt.Printf("                sigScript=%s\n", hex.EncodeToString(in.SignatureScript))
				addr, err := addresshelper.SigScriptSenderAddress(in.SignatureScript, params)
				fmt.Printf("                derived sender addr=%q err=%v\n", addr, err)
			}
		}
		for j, out := range tx.TxOut {
			fmt.Printf("       out[%d]: coin=%d value=%d pkLen=%d\n",
				j, out.CoinType, out.Value, len(out.PkScript))
		}
	}
	fmt.Println("\nStake tree:")
	for i, tx := range block.STransactions {
		fmt.Printf("  [%d] prefix=%s  full=%s  ins=%d outs=%d\n",
			i, tx.TxHash(), tx.TxHashFull(), len(tx.TxIn), len(tx.TxOut))
	}

	// Reconstruct the combined merkle root.
	regularRoot := standalone.CalcTxTreeMerkleRoot(block.Transactions)
	stakeRoot := standalone.CalcTxTreeMerkleRoot(block.STransactions)
	combined := standalone.CalcCombinedTxTreeMerkleRoot(block.Transactions, block.STransactions)

	fmt.Println()
	fmt.Printf("Computed regular tree root : %s\n", regularRoot)
	fmt.Printf("Computed stake   tree root : %s\n", stakeRoot)
	fmt.Printf("Computed combined root     : %s\n", combined)
	fmt.Printf("Header MerkleRoot (committed): %s\n", hdr.MerkleRoot)

	switch {
	case combined == hdr.MerkleRoot:
		fmt.Println("\n✅ DCP0005 combined root matches header — block is internally consistent at SKABigIntVersion.")
		fmt.Println("    If the wallet still rejects this block, the bug is in HOW the wallet parses, not in the chain data.")
		os.Exit(0)
	case regularRoot == hdr.MerkleRoot:
		fmt.Println("\n⚠ Pre-DCP0005 (regular-tree-only) root matches the header — this block was mined under the old rules.")
		fmt.Println("   The wallet's DCP0005MerkleRoot path is wrong to be applied here, but discovery.go falls through MerkleRoots first;")
		fmt.Println("   verify that MerkleRoots() also passes its StakeRoot check.")
		fmt.Printf("   Header.StakeRoot = %s vs computed stake root = %s\n", hdr.StakeRoot, stakeRoot)
		os.Exit(0)
	default:
		fmt.Println("\n❌ NEITHER root matches the header — wire data the peer sent does not reproduce the committed merkle.")
		fmt.Println("   Either: peer is buggy / lying, OR wire serialization on our side differs from how the block was hashed at mine time.")
		os.Exit(1)
	}
}

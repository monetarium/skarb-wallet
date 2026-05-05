// wireprobe sends a Decred-wire `version` handshake to a peer and reports
// whether the peer:
//
//   - accepts our protocol version,
//   - replies with `version` of its own (and what protocol it speaks),
//   - advertises `cf` (compact-filter) service support,
//   - sends `verack` to complete the handshake.
//
// Use it to sanity-check a hardcoded fallback peer before shipping. Sample:
//
//	go run ./cmd/wireprobe -addr 176.9.28.21:19508 -net testnet
//	go run ./cmd/wireprobe -addr 176.113.164.216:9508 -net mainnet
//
// Doesn't require dcrd or dcrwallet — just the upstream wire package.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/wire"
)

func main() {
	addr := flag.String("addr", "176.9.28.21:19508", "host:port of the peer to probe")
	netName := flag.String("net", "testnet", "mainnet | testnet")
	timeout := flag.Duration("timeout", 10*time.Second, "per-step deadline")
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

	fmt.Printf("→ Dialing %s (net=%s, magic=%08x)\n", *addr, *netName, uint32(params.Net))
	conn, err := net.DialTimeout("tcp", *addr, *timeout)
	if err != nil {
		fmt.Printf("✘ Dial failed: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(*timeout))
	fmt.Println("  TCP connected.")

	// Build a version message identifying us as a non-listening client.
	verMsg, err := wire.NewMsgVersionFromConn(conn, 0xdeadbeef, 0)
	if err != nil {
		fmt.Printf("✘ NewMsgVersion: %v\n", err)
		os.Exit(1)
	}
	verMsg.AddUserAgent("wireprobe", "0.1")

	if err := wire.WriteMessage(conn, verMsg, wire.ProtocolVersion, params.Net); err != nil {
		fmt.Printf("✘ Write version: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("→ Sent: version (proto=%d, magic=%08x)\n", wire.ProtocolVersion, uint32(params.Net))

	// Read up to four messages; expect at least version+verack.
	for i := 0; i < 4; i++ {
		_ = conn.SetReadDeadline(time.Now().Add(*timeout))
		msg, _, err := wire.ReadMessage(conn, wire.ProtocolVersion, params.Net)
		if err != nil {
			fmt.Printf("✘ Read %d: %v\n", i+1, err)
			os.Exit(1)
		}
		switch m := msg.(type) {
		case *wire.MsgVersion:
			fmt.Printf("← version: proto=%d, services=%s, ua=%q, lastBlock=%d\n",
				m.ProtocolVersion, m.Services, m.UserAgent, m.LastBlock)
			if m.Services&wire.SFNodeCF == 0 {
				fmt.Println("  ⚠ peer does NOT advertise SFNodeCF — SPV will reject it.")
			} else {
				fmt.Println("  ✓ SFNodeCF advertised — peer is SPV-eligible.")
			}
		case *wire.MsgVerAck:
			fmt.Println("← verack — handshake complete.")
			os.Exit(0)
		default:
			fmt.Printf("← %s\n", m.Command())
		}
	}
	fmt.Println("(stopped after 4 messages without verack)")
}

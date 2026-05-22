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
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
	"github.com/monetarium/monetarium-node/wire"
)

func main() {
	addr := flag.String("addr", "176.9.28.21:19508", "host:port of the peer to probe")
	netName := flag.String("net", "testnet", "mainnet | testnet")
	timeout := flag.Duration("timeout", 10*time.Second, "per-step deadline")
	listen := flag.Duration("listen", 0, "after handshake, keep the connection open and log every inv/tx for this long (e.g. 5m). Use to diagnose 'received unrequested tx' protocol violations.")
	passive := flag.Bool("passive", false, "with -listen, do NOT send getdata for received invs and do NOT ask for the mempool snapshot — only listen. Any MsgTx that arrives in this mode is by definition unsolicited and would be rejected by dcrwallet.")
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
			// Send a verack back and then ask the peer for the genesis
			// cfilter to confirm CF data actually flows, not just that the
			// service bit is advertised.
			if err := wire.WriteMessage(conn, wire.NewMsgVerAck(), wire.ProtocolVersion, params.Net); err != nil {
				fmt.Printf("✘ Write verack: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("→ Sent: verack")
			// getcfilterv2 with start=end=genesis: cheapest possible CF probe.
			gen := params.GenesisHash
			req := wire.NewMsgGetCFsV2(&gen, &gen)
			if err := wire.WriteMessage(conn, req, wire.ProtocolVersion, params.Net); err != nil {
				fmt.Printf("✘ Write getcfsv2: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("→ Sent: getcfsv2 (genesis=%s)\n", gen)
			// Wait for cfsv2 (or anything else) up to timeout.
			_ = conn.SetReadDeadline(time.Now().Add(*timeout))
			for j := 0; j < 6; j++ {
				resp, _, err := wire.ReadMessage(conn, wire.ProtocolVersion, params.Net)
				if err != nil {
					fmt.Printf("✘ Read after getcfsv2: %v\n", err)
					os.Exit(1)
				}
				if cfs, ok := resp.(*wire.MsgCFiltersV2); ok {
					if len(cfs.CFilters) == 0 {
						fmt.Println("✘ cfsv2 returned 0 filters — node likely has no CF index built.")
						os.Exit(1)
					}
					fmt.Printf("← cfsv2 — got %d filter(s), genesis CF size %d bytes ✓\n",
						len(cfs.CFilters), len(cfs.CFilters[0].Data))
					fmt.Println("Node serves CF data end-to-end. SPV should work.")
					if *listen > 0 {
						// Stay alive long enough to catch a tx push. Peer
						// expects empty-but-present responses to getheaders
						// and we have to answer pings — otherwise the node
						// stalls us and closes the connection within
						// seconds.
						_ = wire.WriteMessage(conn, &wire.MsgHeaders{}, wire.ProtocolVersion, params.Net)
						if !*passive {
							_ = wire.WriteMessage(conn, wire.NewMsgMemPool(), wire.ProtocolVersion, params.Net)
						}
						mode := "active (inv → getdata → tx)"
						if *passive {
							mode = "passive (no getdata; any tx body received is an unsolicited push)"
						}
						fmt.Printf("\n→ Listening %s for %s\n", mode, *listen)
						invSeen := map[chainhash.Hash]bool{}
						deadline := time.Now().Add(*listen)
						for time.Now().Before(deadline) {
							_ = conn.SetReadDeadline(deadline)
							m, _, err := wire.ReadMessage(conn, wire.ProtocolVersion, params.Net)
							if err != nil {
								fmt.Printf("✘ Read error during listen: %v\n", err)
								break
							}
							switch x := m.(type) {
							case *wire.MsgInv:
								for _, inv := range x.InvList {
									if inv.Type == wire.InvTypeTx {
										invSeen[inv.Hash] = true
										fmt.Printf("← inv  tx  %s\n", inv.Hash)
										if !*passive {
											// Mirror dcrwallet: ask for the body so we
											// exercise inv→getdata→tx→hash.
											gd := wire.NewMsgGetData()
											hashCopy := inv.Hash
											_ = gd.AddInvVect(wire.NewInvVect(wire.InvTypeTx, &hashCopy))
											_ = wire.WriteMessage(conn, gd, wire.ProtocolVersion, params.Net)
										}
									}
								}
							case *wire.MsgTx:
								computed := x.TxHash()
								if invSeen[computed] {
									fmt.Printf("← tx   %s  ✓ matches inv\n", computed)
								} else {
									fmt.Printf("← tx   %s  ✗ NO PRIOR INV — dcrwallet rejects this as 'received unrequested tx'\n", computed)
								}
							case *wire.MsgPing:
								_ = wire.WriteMessage(conn, wire.NewMsgPong(x.Nonce), wire.ProtocolVersion, params.Net)
							case *wire.MsgGetHeaders:
								_ = wire.WriteMessage(conn, &wire.MsgHeaders{}, wire.ProtocolVersion, params.Net)
							default:
								fmt.Printf("← %s (ignored)\n", m.Command())
							}
						}
						fmt.Printf("Listen window ended. Invs seen: %d.\n", len(invSeen))
					}
					os.Exit(0)
				}
				fmt.Printf("← %s (ignoring, waiting for cfsv2)\n", resp.Command())
			}
			fmt.Println("✘ Got 6 messages but no cfsv2 response — CF service flag is on but cfilter delivery is broken.")
			os.Exit(1)
		default:
			fmt.Printf("← %s\n", m.Command())
		}
	}
	fmt.Println("(stopped after 4 messages without verack)")
}

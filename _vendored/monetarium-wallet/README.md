monetarium-wallet
=================

monetarium-wallet is a daemon handling Monetarium wallet functionality.  All interaction
with the wallet is performed over RPC.

Public and private keys are derived using the hierarchical
deterministic format described by
[BIP0032](https://github.com/bitcoin/bips/blob/master/bip-0032.mediawiki).
Unencrypted private keys are not supported and are never written to
disk.  monetarium-wallet uses the
`m/44'/<coin type>'/<account>'/<branch>/<address index>`
HD path for all derived addresses, as described by
[BIP0044](https://github.com/bitcoin/bips/blob/master/bip-0044.mediawiki).

monetarium-wallet provides two modes of operation to connect to the Monetarium
network.  The first (and default) is to communicate with a single
trusted `monetarium` instance using JSON-RPC.  The second is a
privacy-preserving Simplified Payment Verification (SPV) mode (enabled
with the `--spv` flag) where the wallet connects either to specified
peers (with `--spvconnect`) or peers discovered from seeders and other
peers. Both modes can be switched between with just a restart of the
wallet.  It is advised to avoid SPV mode for heavily-used wallets
which require downloading most blocks regardless.

Not all functionality is available when running in SPV mode.  Some of
these features may become available in future versions, but only if a
consensus vote passes to activate the required changes.  Currently,
the following features are disabled or unavailable to SPV wallets:

  * Voting

  * Revoking tickets before expiry

  * Determining exact number of live and missed tickets (as opposed to
    simply unspent).

Wallet clients interact with the wallet using one of two RPC servers:

  1. A JSON-RPC server inspired by the Bitcoin Core rpc server

     The JSON-RPC server exists to ease the migration of wallet applications
     from Core, but complete compatibility is not guaranteed.  Some portions of
     the API (and especially accounts) have to work differently due to other
     design decisions (mostly due to BIP0044).  However, if you find a
     compatibility issue and feel that it could be reasonably supported, please
     report an issue.  This server is enabled by default as long as a username
     and password are provided.

  2. A gRPC server

     The gRPC server uses a new API built for monetarium-wallet, but the API is not
     stabilized.  This server is enabled by default and may be disabled with
     the config option `--nogrpc`.  If you don't mind applications breaking
     due to API changes, don't want to deal with issues of the JSON-RPC API, or
     need notifications for changes to the wallet, this is the RPC server to
     use. The gRPC server is documented [here](./rpc/documentation/README.md).

## Installing and updating

### Build from source (all platforms)

- **Install Go 1.23 or 1.24**

  Installation instructions can be found here: https://golang.org/doc/install.
  Ensure Go was installed properly and is a supported version:
  ```sh
  $ go version
  $ go env GOROOT GOPATH
  ```
  NOTE: `GOROOT` and `GOPATH` must not be on the same path. It is recommended
  to add `$GOPATH/bin` to your `PATH` according to the Golang.org instructions.

- **Build monetarium-wallet**

  Clone the repository and build:

  ```sh
  $ go build -o monetarium-wallet .
  ```

  The `monetarium-wallet` executable will be created in the current directory.

## Getting Started

monetarium-wallet can connect to the Monetarium blockchain using either monetarium (the node)
or by running in Simple Payment Verification (SPV) mode. Commands should be run
in `cmd.exe` or PowerShell on Windows, or any terminal emulator on *nix.

- Run the following command to create a wallet:

```sh
monetarium-wallet --create
```

- To use monetarium-wallet in SPV mode:

```sh
monetarium-wallet --spv
```

monetarium-wallet will find external full node peers. It will take a few minutes to
download the blockchain headers and filters, but it will not download full blocks.

- To use monetarium-wallet using a localhost monetarium node:

You will need to install both monetarium (the node) and monetarium-ctl.
`monetarium-ctl` is the client that controls `monetarium` and `monetarium-wallet`
via remote procedure call (RPC).

## Running Tests

All tests may be run using the script `run_tests.sh`. Generally, only
the current and previous major versions of Go are supported.

```sh
./run_tests.sh
```

## License

monetarium-wallet is licensed under the liberal ISC License.

# Independent MLS interoperability path

Research and executable checks: 2026-09-27. The optional harness is implemented
in `mls-proof/tests/interop.spec.ts`. Unmodified OpenMLS fails the extensibility
check; an explicitly constrained peer passes the named wire/lifecycle scenarios.
The existing Chromium/Firefox tests use the same ts-mls implementation and do not
satisfy the independent-implementation gate.

## Recommendation

Drive **OpenMLS 0.9.0's existing native gRPC interop client** from the isolated
Playwright test driver, alongside the current browser proof. Start with suite 1,
Basic credentials, private handshakes and an embedded ratchet tree. Keep this
fixture outside the Go server and production browser bundle. This is the shortest
path inferred from the existing APIs: it needs a test-side RPC caller, not a full
new ts-mls gRPC server or a replacement browser cryptographic library.

OpenMLS's client already implements KeyPackage creation, Welcome joining, commits,
message protection/unprotection, epoch authentication and exporters. Its supported
suites include suite 1. The tagged client uses the RustCrypto provider. Several
advanced RPCs remain unimplemented, so select the needed subset explicitly.
[Client source](https://github.com/openmls/openmls/blob/openmls-v0.9.0/interop_client/src/main.rs),
[manifest](https://github.com/openmls/openmls/blob/openmls-v0.9.0/interop_client/Cargo.toml).

## Reproducible fixture setup

Pin OpenMLS to `3a3e35de3feeca8f6605143c464d5452ae584d43` (`openmls-v0.9.0`).
Its workspace declares Rust 1.91.0; use a compatible pinned toolchain and retain
`Cargo.lock`. The lock pins the MLSWG protocol dependency to
`cfd450286d1bfd9cd2519b95c80f9771f94a5b1a`. Fetch the `.proto` from that same
revision. [Workspace](https://github.com/openmls/openmls/blob/3a3e35de3feeca8f6605143c464d5452ae584d43/Cargo.toml),
[lockfile](https://github.com/openmls/openmls/blob/3a3e35de3feeca8f6605143c464d5452ae584d43/Cargo.lock),
[protocol](https://github.com/mlswg/mls-implementations/blob/cfd450286d1bfd9cd2519b95c80f9771f94a5b1a/interop/proto/mls_client.proto).

Proposed commands in an isolated scratch directory, with Rust/Cargo, a working C
build toolchain and `protoc` available:

```sh
git clone --branch openmls-v0.9.0 --depth 1 https://github.com/openmls/openmls.git
cd openmls
test "$(git rev-parse HEAD)" = 3a3e35de3feeca8f6605143c464d5452ae584d43
cargo build --locked -p interop_client
RUST_LOG=error target/debug/interop_client --host 127.0.0.1 --port 50051
```

The actual tagged source defaults to `0.0.0.0`, despite its README showing an
IPv6 loopback default; always supply the host. Choose a free owned port and kill
only the child process started by the test. Avoid running upstream
`test_with_runner.py` unchanged: it uses a broad `killall`, deletes a relative
checkout and clones a moving runner revision.
[README](https://github.com/openmls/openmls/blob/openmls-v0.9.0/interop_client/README.md),
[runner script](https://github.com/openmls/openmls/blob/openmls-v0.9.0/interop_client/test_with_runner.py).

For the first executable check, `grpcurl` can use that local `.proto` directly;
server reflection is unnecessary. Pin the test tool, for example `v1.9.4`, in a
scratch `GOBIN`, without adding it to the product's `go.mod`. Invoke with an argv
array and JSON on stdin from Playwright, with deadlines and bounded output.
[grpcurl usage](https://github.com/fullstorydev/grpcurl/blob/v1.9.4/README.md).

```sh
grpcurl -plaintext -import-path ./proto -proto mls_client.proto \
  -d '{}' 127.0.0.1:50051 mls_client.MLSClient/SupportedCiphersuites
```

The RPC fixture intentionally exposes synthetic private material in KeyPackage
responses. Do not log those responses, enable `crypto-debug`, use real identities,
or expose this service beyond loopback. The application never calls it.
[Response and logging implementation](https://github.com/openmls/openmls/blob/openmls-v0.9.0/interop_client/src/main.rs).

## Implemented test coverage

The following mapping uses the pinned [MLSWG RPC schema](https://github.com/mlswg/mls-implementations/blob/cfd450286d1bfd9cd2519b95c80f9771f94a5b1a/interop/proto/mls_client.proto)
and the local [manual proof API](../mls-proof/src/device.ts):

1. Initialize browser Alice. Call OpenMLS `CreateKeyPackage` with `cipher_suite: 1`
   and synthetic identity Bob; preserve `transaction_id` and `key_package` only.
   Alice explicitly approves Bob's package, creates a group, adds Bob and settles
   the accepted commit. Call `JoinGroup` with its Welcome, Bob's identity and
   transaction, `encrypt_handshake: true`, and no external tree.
2. Send Alice's persisted `proof.send` wire to `Unprotect`. Assert the exact
   plaintext, acknowledge its outbox, then call `Protect` for Bob and pass its
   ciphertext to Alice's ordered `proof.receive`. Assert both plaintexts.
3. Reload/unlock Alice and repeat in both directions. Exchange a browser update
   through `HandleCommit`; exchange an OpenMLS forced-path empty `Commit` through
   Alice's receive path and OpenMLS `HandlePendingCommit`. Verify subsequent
   traffic after each transition.
4. Add a second browser Carol using OpenMLS `Commit` with a by-value `add`
   proposal. Carol pins Alice's and Bob's original signing identities before
   joining. This exercises OpenMLS-generated Welcomes without needing another
   creator-identity API. Alice processes that commit; OpenMLS settles it.
5. Remove a participant, verify surviving members can exchange new messages and
   the removed participant cannot decrypt them. Include replay and tamper
   rejection without allowing failed inputs to advance durable browser state.

`proof.interopState` hashes the current epoch authenticator and a 32-byte MLS
exporter result for a fixed test label/context. The driver compares them with hashes
of OpenMLS `StateAuth`/`Export` results after each transition. It never emits the
underlying secrets in artifacts. A successful test establishes only its named wire/lifecycle scenarios;
it does not establish full RFC conformance or application security.

## KyMessages profile is a separate gate

The manual harness does not include the HTTP profile. Current
[delivery.ts](../mls-proof/src/delivery.ts) adds authenticated metadata and a signed
GroupInfo extension `0xff01`, then requires exactly one matching binding on join.
It also checks account/device pins, room ID, epoch, roster and identity generation.

The standard interop RPC schema has no custom **GroupInfo** extension injection or
inspection field. Its GroupContext extension proposal is a different mechanism;
do not substitute it. OpenMLS represents unknown extensions, but representation
alone does not prove that this private binding is validated or retained by the
interop service. A later profile fixture must expose and check the signed
GroupInfo data and implement the KyMessages credential/metadata checks. In
particular, removing the binding to make a Welcome pass would invalidate that
profile test. [Extension representation](https://github.com/openmls/openmls/blob/openmls-v0.9.0/openmls/src/extensions/mod.rs),
[RPC schema](https://github.com/mlswg/mls-implementations/blob/cfd450286d1bfd9cd2519b95c80f9771f94a5b1a/interop/proto/mls_client.proto).

## Audit evidence and remaining gates

- ts-mls 1.6.4 explicitly says it is unaudited. Interoperability cannot change that
  statement. [Tagged README](https://github.com/LukaJCB/ts-mls/blob/v1.6.4/README.md).
- This research establishes no audit coverage for OpenMLS 0.9.0, its selected
  provider or KyMessages. Do not describe it as an audited substitute. Its published
  persistence advisory also makes reload/key-retention regression coverage relevant.
  [OpenMLS advisory](https://github.com/openmls/openmls/security/advisories/GHSA-qr9h-x63w-vqfm).
- mls-rs 0.56.0 explicitly has no full third-party security audit. Its upstream
  gRPC harness is an alternative, not a reason to introduce a second fixture now.
  [Tagged README](https://github.com/awslabs/mls-rs/blob/0.56.0/mls-rs/README.md),
  [interop workflow](https://github.com/awslabs/mls-rs/blob/0.56.0/.github/workflows/interop_tests.yml).

Keep independent implementation exchange, independent security assessment,
KyMessages profile compatibility, deployed identity and supported-browser evidence
as distinct release results. The evidence below completes only the constrained
wire/lifecycle cases.

## Executed results and reproduction

Both Chromium and Firefox failed immediately when approving the unmodified
OpenMLS KeyPackage: `Malformed or trailing wire bytes`. The tagged OpenMLS fixture
advertises both MLS 1.0 and `ProtocolVersion::Other(999)`. ts-mls 1.6.4's
`decodeCapabilities` uses the closed `decodeProtocolVersion` decoder, which only
accepts 1; this rejects the otherwise usable KeyPackage. That conflicts with the
unknown-capability handling required by [RFC 9420 §13.4](https://www.rfc-editor.org/rfc/rfc9420.html#section-13.4).
[ts-mls decoder](https://github.com/LukaJCB/ts-mls/blob/v1.6.4/src/protocolVersion.ts),
[capabilities](https://github.com/LukaJCB/ts-mls/blob/v1.6.4/src/capabilities.ts).
This is an open dependency/release defect. Do not strip fields from a received
signed KeyPackage, weaken signature verification or report full interoperability.

For diagnostic coverage, `mls-proof/openmls-mls10-only.patch` changes exactly the
native **test fixture's advertisement** to MLS 1.0 before it generates/signs a new
KeyPackage. No cryptographic implementation or receiving wire data is patched.
With that explicit constraint, both browsers passed: browser-generated Welcome,
bidirectional plaintext equality, durable reload, updates authored by each stack,
OpenMLS-generated Welcome for a third browser, agreement on live group secrets,
removal of OpenMLS Bob, surviving-member exchange, tamper/replay rejection and
unchanged receiving state after a rejected ciphertext. This does not test the
KyMessages custom GroupInfo binding or cure the extensibility defect.

Build the unmodified pinned fixture above first. From this repository, install the
isolated test tool and download the pinned RPC schema into an owned scratch path:

```sh
# Set interop_work to an absolute disposable directory and repo to this checkout.
mkdir -p "$interop_work/bin" "$interop_work/proto"
GOBIN="$interop_work/bin" go install github.com/fullstorydev/grpcurl/cmd/grpcurl@v1.9.4
curl -fsS https://raw.githubusercontent.com/mlswg/mls-implementations/cfd450286d1bfd9cd2519b95c80f9771f94a5b1a/interop/proto/mls_client.proto \
  -o "$interop_work/proto/mls_client.proto"
npm ci --prefix "$repo/mls-proof"
npm run build --prefix "$repo/mls-proof"
OPENMLS_INTEROP_BIN="$interop_work/openmls/target/debug/interop_client" \
MLS_INTEROP_PROTO="$interop_work/proto/mls_client.proto" \
GRPCURL_BIN="$interop_work/bin/grpcurl" \
npm run test:interop --prefix "$repo/mls-proof" -- --project=chromium --project=firefox
```

That unmodified run is expected to **fail** on the defect described above. For the
separate constrained run, apply the committed patch and rebuild, then repeat the
same command. Never apply it to production source or silently call it unmodified:

```sh
git -C "$interop_work/openmls" apply --check "$repo/mls-proof/openmls-mls10-only.patch"
git -C "$interop_work/openmls" apply "$repo/mls-proof/openmls-mls10-only.patch"
cargo build --locked --manifest-path "$interop_work/openmls/Cargo.toml" -p interop_client
```

The driver allocates a loopback port and terminates only its owned child process.
Run sequentially with other proof suites because they share the Vite preview port.
Fixture build evidence: Rust/Cargo 1.98.1 on this Linux host, locked upstream graph;
grpcurl v1.9.4; ts-mls 1.6.4 and Playwright 1.63.0. The fixture stays outside the
repository/runtime dependencies; only Node test type declarations were added.

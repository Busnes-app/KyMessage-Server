# Browser MLS library selection

Checked 2026-09-27 against publisher registries, tagged source and upstream documentation.
This recommends a feasibility dependency, not a production cryptography approval.

## Recommendation

Use **`ts-mls` exactly `1.6.4`** for the isolated browser proof. It fits the existing
TypeScript toolchain without introducing Rust/WASM packaging. Its MIT license,
browser support and explicit lack of a formal security audit are documented upstream.
Use ordinary RFC 9420 suite 1 (`MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519`),
then test required browsers; do not infer universal WebCrypto support. Keep the
proof outside the production application until the security and device lifecycle
gates in [PRODUCT.md](PRODUCT.md) are satisfied. This is an implementation-cost
judgment, not evidence that this library is safer than the alternatives.
[Pinned package](https://registry.npmjs.org/ts-mls/1.6.4),
[tagged README](https://github.com/LukaJCB/ts-mls/blob/v1.6.4/README.md).

## Candidates

| Candidate | Browser and persistence evidence | Assessment |
| --- | --- | --- |
| ts-mls 1.6.4, MIT | Direct JavaScript APIs; binary group-state codec. | Shortest browser proof. Explicitly unaudited; application owns durable state and identity policy. [Source](https://github.com/LukaJCB/ts-mls/tree/v1.6.4) |
| OpenMLS 0.9.0, MIT | Rust `js` feature; upstream WASM bindings include group creation, messages and membership. Core persists through `StorageProvider`, reloads with `MlsGroup::load`. | Strong alternative for a future shared native core; browser wrapper/storage needs separate work. Upstream lists WASM as built but unsupported/untested in its platform matrix. [Manifest](https://github.com/openmls/openmls/blob/openmls-v0.9.0/openmls/Cargo.toml), [bindings](https://github.com/openmls/openmls/blob/openmls-v0.9.0/openmls-wasm/src/lib.rs), [platforms](https://github.com/openmls/openmls/blob/openmls-v0.9.0/README.md), [persistence](https://book.openmls.tech/user_manual/persistence.html) |
| mls-rs 0.56.0, Apache-2.0 OR MIT | WASM builds; configurable group/key-package storage; experimental WebCrypto provider. `Group::write_to_storage` and `Client::load_group` provide persistence hooks. | Another native-core candidate, requiring browser bindings/storage integration. Upstream explicitly says no full third-party security audit. [Tagged README](https://github.com/awslabs/mls-rs/blob/0.56.0/mls-rs/README.md), [group API](https://docs.rs/mls-rs/0.56.0/mls_rs/group/struct.Group.html), [client API](https://docs.rs/mls-rs/0.56.0/mls_rs/client/struct.Client.html) |

All three show recent upstream work: the newest commits inspected were dated
2026-09-26, 2026-09-24 and 2026-09-17 respectively. Activity is a maintenance signal,
not a support commitment. [ts-mls commit](https://github.com/LukaJCB/ts-mls/commit/aeb61f3800ce6c8212a430e39e2b8e437a88b0cb),
[OpenMLS commit](https://github.com/openmls/openmls/commit/0cf0b0d667425be978fd8117769ea636f880ed2d),
[mls-rs commit](https://github.com/awslabs/mls-rs/commit/d4c5b19f4bbf8a931face27fc9e30f2db33ce83e).

Do not call OpenMLS audited on the strength of this comparison: no audit coverage
was established here. Its published secret-tree persistence advisory illustrates why
reload/replay tests are required even with established implementations.
[Upstream advisory](https://github.com/openmls/openmls/security/advisories/GHSA-qr9h-x63w-vqfm).

## Pinned ts-mls API and integration traps

Version 1.6.4 uses `generateKeyPackage`, `createGroup`, `createCommit`, `joinGroup`,
`createApplicationMessage` and `processPrivateMessage`. Wire encoding uses
`encodeMlsMessage(value)` and `decodeMlsMessage(bytes, 0)`, which returns
`[value, consumedLength]` or `undefined`. Require the entire bounded input to be
consumed. The development-branch README uses different APIs; follow the installed
types and tagged examples. [Tagged example](https://github.com/LukaJCB/ts-mls/blob/v1.6.4/README.md),
[decoder contract](https://github.com/LukaJCB/ts-mls/blob/v1.6.4/src/codec/tlsDecoder.ts).

`encodeGroupState(state)` serializes secret group material. `decodeGroupState(bytes,
0)` restores `GroupState`, excluding `clientConfig`; reattach trusted configuration.
This is serialization, not encryption at rest. Protect and version the resulting
record locally; never place it in the delivery service. The upstream binary test
checks the restored tree, so independently test subsequent sends, receives and
membership changes after browser reload.
[State codec](https://github.com/LukaJCB/ts-mls/blob/v1.6.4/src/clientState.ts),
[upstream test](https://github.com/LukaJCB/ts-mls/blob/v1.6.4/test/scenario/clientStateSerialization.test.ts).

The default authentication service accepts every credential. Replace it with
verification against approved device identity/signature keys before claiming
authenticated messaging. OIDC sign-in cannot supply this implicitly.
[Default implementation](https://github.com/LukaJCB/ts-mls/blob/v1.6.4/src/authenticationService.ts).

Application design requirements: persist the new ratchet state and pending outbound
ciphertext atomically before sending; retry identical ciphertext; persist inbound
state and cursor together. Give each browser device its own state and prevent two
tabs mutating that same device concurrently. Handle competing membership commits
through an explicit ordered acceptance/retry policy. These are proposed application
contracts to prove, not features supplied automatically by a codec. Consumed-key
zeroing is exposed by the library, but browser storage deletion and JavaScript
garbage collection do not establish physical secure erasure.

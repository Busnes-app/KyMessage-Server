# Devices

## Purpose
Manages 90-second ephemeral QR-code device pairing protocols and push notification client registration.

## Ownership
Owns ephemeral PIN code generation, QR payload creation, device verification, and push token linking.

## Local Contracts
- Verification rejects inactive or password-restricted accounts and returns the pre-consumption user snapshot; session issuance checks that snapshot against concurrent password replacement.
- Pairings are carried only by a 24-byte QR secret with a strict 90-second TTL (`InitPairing`). Never add a short typed code: the anonymous verify route issues a full session, so anything guessable inside the window is an account takeover. Unknown, expired and consumed secrets return the same `ErrPairingNotFound`.
- Pairing requires an authenticated initiating account; successful verification atomically consumes the pending pairing and cannot be replayed.

## Verification
- `go test -v ./internal/devices/...`

## Child DOX Index
None.

# Branding

## Purpose
Pure checks and the one file edit behind the console's product name and logo.

## Ownership
Owns `branding.go`: `ValidateName`, `NormalizePNG`, `PatchElementBrand`, `ElementBrand`,
`ParseBrand`.
`internal/api` owns the routes, storage, `/app-icon.png` and `Server.ReconcileBrand`;
`cmd/server` decides when the reconcile runs.

## Local Contracts
- `ValidateName` trims, then accepts 1–64 runes (`MaxNameRunes`) of valid UTF-8 with no
  control (Cc), format (Cf: bidi overrides and isolates, zero-width characters, BOM) or
  line/paragraph separator (Zl, Zp) character. ZWJ emoji sequences are therefore refused.
- `NormalizePNG` accepts at most `MaxLogoBytes` (1 MiB) starting with the PNG signature, reads
  the dimensions with `png.DecodeConfig` and refuses zero or more than `MaxLogoSide` (1024)
  before decoding, then decodes and re-encodes, so no ancillary chunk (text, EXIF, ICC, APNG)
  survives. Every other format is refused (SVG can carry script); so is a re-encoding over
  `MaxLogoBytes`.
- `PatchElementBrand` changes only the bytes of every top-level `brand` value (inserting one
  first when absent), writes only when that changes the file, and writes through the existing
  inode (`O_WRONLY|O_TRUNC`; no create, rename or chmod): Compose binds that single file into
  Element and the app, and a bind keeps the inode it was given. A missing, non-regular,
  invalid or non-object file is refused untouched. A reader can see the file mid-write (an
  Element starting then copies a broken file); callers serialise writers.
- `ElementBrand` (the file) and `ParseBrand` (a body, as Element serves it) return the last
  top-level `brand`, as `JSON.parse` keeps it, or "" when absent.
- No logging, store access or network.

## Verification
- `go test -race ./internal/branding/`

## Child DOX Index
None.

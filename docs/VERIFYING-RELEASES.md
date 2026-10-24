# Verifying releases

[Product overview](../README.md) · [Running](RUNNING.md) · [Server restore](RESTORE.md)

A KyMessages release `vX.Y.Z` (or `vX.Y.Z.N` for a daily dependency-update release) publishes two
container images from the same tagged commit:

| Image | Built from | What it is |
|---|---|---|
| `ghcr.io/busnes-app/kymessage-server:X.Y.Z[.N]` | `Dockerfile` | The server and operator console |
| `ghcr.io/busnes-app/kymessage-notes:X.Y.Z[.N]` | `notes/Dockerfile` | The optional meeting-notes bot ([Meeting notes](MEETING-NOTES.md)) |

This page shows how to check that an image was built by this repository's release workflow from
the tagged commit, and how to rebuild the server binary yourself and get the same bytes. Commands
run from an empty directory unless they say otherwise.

## What is signed, and by whom

- `release-image.yml` is the only workflow that publishes an image. It refuses a tag whose
  `X.Y.Z` differs from `internal/appversion/VERSION` and any commit on which a ci.yml job in its
  `REQUIRED` list did not pass. It builds both images, publishes a SLSA build-provenance attestation
  for each digest through GitHub's Sigstore integration, and verifies each before it moves that
  image's `:stable`.
- There is no long-lived signing key to download. The signature is tied to a workflow identity,
  and that identity is the thing you check. It is the same for both images:
  `https://github.com/Busnes-app/KyMessage-Server/.github/workflows/release-image.yml@refs/tags/v<version>`.
  `gh` fetches Sigstore's public trust root itself.
- Each image also carries an SPDX software bill of materials made by BuildKit. The SBOM sits in
  the image index and is **not** separately signed; trust it as far as you trust the digest you
  verified.

## Verify a release image

You need `gh` (logged in) and `docker` with buildx. Name the version and the image yourself, and
verify the version tag, not `:stable`: tags can move, so the check resolves the digest first and
then requires the attestation to name the commit the release tag points at.

```bash
v=<release version, e.g. 0.4.5>
i=ghcr.io/busnes-app/kymessage-server   # or ghcr.io/busnes-app/kymessage-notes
sha=$(gh api repos/Busnes-app/KyMessage-Server/commits/refs/tags/v$v --jq .sha) \
  && d=$(docker buildx imagetools inspect $i:$v --format '{{.Manifest.Digest}}') \
  && gh attestation verify "oci://$i@$d" --repo Busnes-app/KyMessage-Server \
       --cert-identity https://github.com/Busnes-app/KyMessage-Server/.github/workflows/release-image.yml@refs/tags/v$v \
  && [ "$(gh attestation verify "oci://$i@$d" --repo Busnes-app/KyMessage-Server \
       --cert-identity https://github.com/Busnes-app/KyMessage-Server/.github/workflows/release-image.yml@refs/tags/v$v \
       --format json --jq '.[0].verificationResult.statement.predicate.buildDefinition.resolvedDependencies[0].digest.gitCommit')" = "$sha" ] \
  && echo "OK: $i@$d is release v$v, built from $sha"
```

Then run that digest, not the tag: `KY_IMAGE=ghcr.io/busnes-app/kymessage-server@$d` (or
`KY_NOTES_IMAGE=ghcr.io/busnes-app/kymessage-notes@$d`) in `.env`. The comment at the top of
[`docker-compose.yml`](../docker-compose.yml) runs the same check for the server and writes
`KY_IMAGE` into `.env` safely.

Any failure means: do not run that image. A different workflow, a branch instead of the tag,
another tag, or another repository all fail the `--cert-identity` or `--repo` check.

To read the SBOM:

```bash
docker buildx imagetools inspect $i@$d --format '{{ json .SBOM }}'
```

## Rebuild the server binary and compare

Releases after 0.4.5 are built with `-trimpath`, so the same commit, Go version and version
string give the same server binary on any machine and in any directory. CI checks this on every
push. Images up to 0.4.5 recorded their build directory (`/app`) and Go's install path in the
binary, so a build elsewhere does not compare with them.

The binary embeds the web interface. The image rebuilds it from `web/` and does not copy the
committed `web/dist`; CI fails any commit whose committed `web/dist` differs from a fresh
`npm run build`, so building from the committed copy gives the image's bytes.

```bash
v=<release version>
git clone --branch v$v https://github.com/Busnes-app/KyMessage-Server.git kymessages-v$v && cd kymessages-v$v
go_version=$(sed -n 's/^FROM golang:\([0-9.]*\)-.*/\1/p' Dockerfile)
GOTOOLCHAIN=go$go_version CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath \
  -ldflags="-s -w -X github.com/Busnes-app/ky_server_base/internal/appversion.release=$v" \
  -o kymessages-from-source ./cmd/server
d=$(docker buildx imagetools inspect ghcr.io/busnes-app/kymessage-server:$v --format '{{.Manifest.Digest}}')
cid=$(docker create ghcr.io/busnes-app/kymessage-server@$d) \
  && docker cp "$cid:/app/kymessages" kymessages-from-image && docker rm "$cid" >/dev/null
sha256sum kymessages-from-source kymessages-from-image
```

The two hashes must match. Run the verify command above on that digest first. `-buildvcs=false`
is needed only because a clone has a `.git` directory; the image build context excludes `.git`,
so Go stamps nothing there. `podman` works in place of `docker` for the `create`/`cp`/`rm` lines.
The image layers around the binary (Alpine, CA certificates, time zones, the PostgreSQL client)
are not rebuilt bit for bit; the binary is what you are checking.

### The meeting-notes image

The notes image has no compiled binary: it is `notes/src` plus the dependencies `npm ci` installs
from `notes/package-lock.json` (whose integrity hashes npm checks) and speech models fetched at the
revisions and hashes pinned in `notes/models.txt`. It is not rebuilt bit for bit; verify its
attestation as above.

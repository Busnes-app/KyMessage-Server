# Backup media

## Purpose
The encrypted local copy of Synapse media: a mirror brought up to date on every run and a
monthly archive of it. Nothing here enters the capsule except the media key.

## Ownership
Owns `media.go`: `Run`, `Restore`, `Sources`, `ErrBusy`, `ErrNoBackup`. The key is
`backup.MediaKeyPath`; callers pass its bytes.

## Local Contracts
- File format: AES-256-GCM, 12-byte random nonce prefix, associated data
  `kymessages-media/v1\x00<relative path>`; the index uses `kymessages-media-index/v1`.
- Layout under `<KY_BACKUP_DIR>/media`: `mirror/`, `mirror/index`, `full-YYYY-MM.tar` (sealed
  index first, then `mirror/<path>`) and `.lock` (flock; contention returns `ErrBusy`).
- Only `local_content` and `local_thumbnails` are kept; symlinks and other subtrees are skipped
  and reads go through `os.Root`.
- The index is written after every run, including one cut short by cancellation or a failed
  file (the run then errors, with no archive or pruning), so the next run resumes.
- The monthly decision: archive only when the UTC month's name is newer than every existing
  archive, so a clock gone backwards never archives, prunes or deletes. Only after a new archive
  does the mirror drop media deleted from the store, index rewritten first, then the newest
  `keep` (at least 1) archives remain. A crash after the archive rename defers pruning to the
  next month: extra data is kept, nothing is lost.
- Every write is temp-then-rename (`writeAtomic`); a run sweeps `.*.tmp-*` leftovers under the lock.
- A run under another key refuses (the index does not open) and rewrites nothing.
- `Restore` applies the newest archive, then the mirror, which wins. `write=false` proves every
  file opens at its own path and changes nothing; `write=true` writes through `os.Root` as
  `uid:gid`. Index and archive entries outside `Sources` are refused.

## Verification
`go test -race ./internal/backup/media/`

## Child DOX Index
None.

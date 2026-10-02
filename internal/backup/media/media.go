// Package media keeps an encrypted copy of Synapse's local media in the backup directory: a
// mirror brought up to date on every run, and a monthly archive of it. Each file is
// AES-256-GCM under the media key with its relative path as associated data, so a ciphertext
// moved to another path does not open. Nothing here is in the capsule except the key.
package media

import (
	"archive/tar"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	mirrorDir  = "mirror"
	indexName  = "index"
	lockName   = ".lock"
	tmpMarker  = ".tmp-"
	fileAAD    = "kymessages-media/v1\x00"
	indexAAD   = "kymessages-media-index/v1"
	archiveFmt = "full-2006-01.tar"
)

// Sources are the media-store subtrees kept: uploads and their thumbnails. Remote media and
// URL previews are caches (Synapse's backup guide), and federation is off.
var Sources = []string{"local_content", "local_thumbnails"}

var (
	ErrBusy     = errors.New("media: another media backup holds the lock")
	ErrNoBackup = errors.New("media: no media backup here (no monthly archive and no mirror index)")
)

// Entry tells a changed file from an unchanged one.
type Entry struct {
	Size  int64 `json:"size"`
	MTime int64 `json:"mtime"` // Unix nanoseconds
}

// Index maps a slash path under the media store to its Entry.
type Index map[string]Entry

// Result is one run: Archive names the monthly archive it wrote, if any.
type Result struct {
	Copied, Unchanged, Pruned int
	Archive                   string
}

// afterScan is a test seam between listing the store and copying from it.
var afterScan = func() {}

// Run brings dir (<KY_BACKUP_DIR>/media) up to date with src, Synapse's media store. New or
// changed files are encrypted into the mirror. Once per UTC calendar month the mirror and its
// index are archived, mirror files whose media is gone from src are dropped, and only the
// newest keep archives remain. ctx is honoured between files; an interrupted run resumes.
func Run(ctx context.Context, src, dir string, key []byte, keep int, now time.Time) (Result, error) {
	var res Result
	a, err := newAEAD(key)
	if err != nil {
		return res, err
	}
	if keep < 1 {
		return res, errors.New("media: keep must be at least 1")
	}
	if err := os.MkdirAll(filepath.Join(dir, mirrorDir), 0o700); err != nil {
		return res, err
	}
	unlock, err := lock(dir)
	if err != nil {
		return res, err
	}
	defer unlock()
	if err := sweepTemp(dir); err != nil {
		return res, err
	}
	indexPath := filepath.Join(dir, mirrorDir, indexName)
	old, err := readIndex(a, indexPath)
	if err != nil {
		return res, err
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return res, err
	}
	defer root.Close()
	cur, err := scan(root)
	if err != nil {
		return res, err
	}
	afterScan()
	next := Index{} // media gone from src stays mirrored until the next archive
	maps.Copy(next, old)
	for _, rel := range slices.Sorted(maps.Keys(cur)) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		e := cur[rel]
		if old[rel] == e && exists(mirrored(dir, rel)) {
			res.Unchanged++
			continue
		}
		err := copyIn(a, root, dir, rel)
		if errors.Is(err, fs.ErrNotExist) {
			delete(cur, rel) // deleted since the scan
			continue
		}
		if err != nil {
			return res, fmt.Errorf("media: %s: %w", rel, err)
		}
		next[rel] = e
		res.Copied++
	}
	if err := writeIndex(a, indexPath, next); err != nil {
		return res, err
	}
	name := now.UTC().Format(archiveFmt)
	if exists(filepath.Join(dir, name)) {
		return res, nil
	}
	if err := writeArchive(dir, name, next); err != nil {
		return res, err
	}
	res.Archive = name
	// The archive holds what the mirror held; now the mirror drops media deleted from src.
	// Index first, so it never lists a file that is gone.
	if err := writeIndex(a, indexPath, cur); err != nil {
		return res, err
	}
	for rel := range next {
		if _, kept := cur[rel]; !kept {
			if err := os.Remove(mirrored(dir, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return res, err
			}
			res.Pruned++
		}
	}
	return res, pruneArchives(dir, keep)
}

func mirrored(dir, rel string) string { return filepath.Join(dir, mirrorDir, filepath.FromSlash(rel)) }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// scan lists the regular files under Sources. Reading through root means a symlink planted in
// the media store cannot pull a file from outside it; symlinks are skipped either way.
func scan(root *os.Root) (Index, error) {
	idx := Index{}
	for _, top := range Sources {
		err := fs.WalkDir(root.FS(), top, func(rel string, d fs.DirEntry, err error) error {
			if rel == top && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipDir // nothing uploaded yet
			}
			if err != nil {
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			idx[rel] = Entry{Size: info.Size(), MTime: info.ModTime().UnixNano()}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return idx, nil
}

func copyIn(a cipher.AEAD, root *os.Root, dir, rel string) error {
	plain, err := root.ReadFile(rel)
	if err != nil {
		return err
	}
	dst := mirrored(dir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return writeBytes(dst, seal(a, plain, fileAAD+rel))
}

// writeAtomic fills a temporary file beside p and renames it into place, so a full disk or a
// crash never leaves a half file under a real name.
func writeAtomic(p string, fill func(io.Writer) error) error {
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+tmpMarker+"*")
	if err != nil {
		return err
	}
	err = fill(f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), p)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

func writeBytes(p string, b []byte) error {
	return writeAtomic(p, func(w io.Writer) error { _, err := w.Write(b); return err })
}

// sweepTemp removes temporary files a killed run left; the lock is held, so none is live.
func sweepTemp(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".") && strings.Contains(d.Name(), tmpMarker) {
			return os.Remove(p)
		}
		return nil
	})
}

func lock(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("media: key is %d bytes, want 32", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func seal(a cipher.AEAD, plain []byte, aad string) []byte {
	nonce := make([]byte, a.NonceSize())
	rand.Read(nonce)
	return a.Seal(nonce, nonce, plain, []byte(aad))
}

func unseal(a cipher.AEAD, sealed []byte, aad string) ([]byte, error) {
	n := a.NonceSize()
	if len(sealed) < n+a.Overhead() {
		return nil, errors.New("too short")
	}
	return a.Open(nil, sealed[:n], sealed[n:], []byte(aad))
}

// validRel accepts only clean slash paths inside Sources, so no index or archive entry can
// name a path outside the kept subtrees of the media store.
func validRel(rel string) bool {
	if !fs.ValidPath(rel) || strings.Contains(rel, `\`) {
		return false
	}
	top, _, found := strings.Cut(rel, "/")
	return found && slices.Contains(Sources, top)
}

func readIndex(a cipher.AEAD, p string) (Index, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Index{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeIndex(a, b)
}

func decodeIndex(a cipher.AEAD, sealed []byte) (Index, error) {
	plain, err := unseal(a, sealed, indexAAD)
	if err != nil {
		return nil, errors.New("media: the index does not open under the media key")
	}
	var idx Index
	if err := json.Unmarshal(plain, &idx); err != nil {
		return nil, fmt.Errorf("media: index: %w", err)
	}
	for rel := range idx {
		if !validRel(rel) {
			return nil, fmt.Errorf("media: index names %q", rel)
		}
	}
	return idx, nil
}

func writeIndex(a cipher.AEAD, p string, idx Index) error {
	b, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	return writeBytes(p, seal(a, b, indexAAD))
}

// writeArchive tars the sealed index, then the sealed mirror files it lists, into dir/name.
// Nothing is decrypted: the archive is exactly as sealed as the mirror.
func writeArchive(dir, name string, idx Index) error {
	return writeAtomic(filepath.Join(dir, name), func(w io.Writer) error {
		tw := tar.NewWriter(w)
		if err := addFile(tw, filepath.Join(dir, mirrorDir, indexName), indexName); err != nil {
			return err
		}
		for _, rel := range slices.Sorted(maps.Keys(idx)) {
			if err := addFile(tw, mirrored(dir, rel), mirrorDir+"/"+rel); err != nil {
				return err
			}
		}
		return tw.Close()
	})
}

func addFile(tw *tar.Writer, src, member string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: member, Mode: 0o600, Size: info.Size(), Typeflag: tar.TypeReg, ModTime: info.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// archives lists dir's monthly archives, newest first (YYYY-MM sorts by name).
func archives(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if _, err := time.Parse(archiveFmt, e.Name()); err == nil && e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	slices.Reverse(names)
	return names, nil
}

func pruneArchives(dir string, keep int) error {
	names, err := archives(dir)
	if err != nil {
		return err
	}
	for _, n := range names[min(keep, len(names)):] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return err
		}
	}
	return nil
}

// Restore writes the newest monthly archive, then the mirror, into dst, owned by uid:gid
// (Synapse's user). With write false it only proves that every file opens under key at its own
// path, so a caller can check everything before changing anything. It returns the file count.
func Restore(ctx context.Context, dir string, key []byte, dst string, uid, gid int, write bool) (int, error) {
	a, err := newAEAD(key)
	if err != nil {
		return 0, err
	}
	var out *os.Root
	if write {
		if out, err = os.OpenRoot(dst); err != nil {
			return 0, err
		}
		defer out.Close()
	}
	put := func(rel string, sealed []byte) error {
		plain, err := unseal(a, sealed, fileAAD+rel)
		if err != nil {
			return fmt.Errorf("media: %s does not open at its path", rel)
		}
		if !write {
			return nil
		}
		return place(out, rel, plain, uid, gid)
	}
	names, err := archives(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	indexPath := filepath.Join(dir, mirrorDir, indexName)
	if len(names) == 0 && !exists(indexPath) {
		return 0, ErrNoBackup
	}
	n := 0
	if len(names) > 0 {
		c, err := fromArchive(ctx, filepath.Join(dir, names[0]), a, put)
		n += c
		if err != nil {
			return n, fmt.Errorf("media: %s: %w", names[0], err)
		}
	}
	idx, err := readIndex(a, indexPath)
	if err != nil {
		return n, err
	}
	for _, rel := range slices.Sorted(maps.Keys(idx)) {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		sealed, err := os.ReadFile(mirrored(dir, rel))
		if err != nil {
			return n, fmt.Errorf("media: the mirror lacks %s: %w", rel, err)
		}
		if err := put(rel, sealed); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func fromArchive(ctx context.Context, p string, a cipher.AEAD, put func(string, []byte) error) (int, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	hdr, err := tr.Next()
	if err != nil || hdr.Name != indexName {
		return 0, errors.New("the archive does not start with its index")
	}
	sealed, err := io.ReadAll(tr)
	if err != nil {
		return 0, err
	}
	idx, err := decodeIndex(a, sealed)
	if err != nil {
		return 0, err
	}
	n := 0
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		rel, ok := strings.CutPrefix(hdr.Name, mirrorDir+"/")
		if _, listed := idx[rel]; !ok || !listed || hdr.Typeflag != tar.TypeReg {
			return n, fmt.Errorf("unexpected member %q", hdr.Name)
		}
		sealed, err := io.ReadAll(tr)
		if err != nil {
			return n, err
		}
		if err := put(rel, sealed); err != nil {
			return n, err
		}
		n++
	}
}

// place writes one file through out, the media store's root, so no path escapes it. The file
// and its directories belong to uid:gid; it lands under a temporary name and is renamed.
func place(out *os.Root, rel string, plain []byte, uid, gid int) error {
	dir := path.Dir(rel)
	if err := out.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for d := dir; d != "."; d = path.Dir(d) {
		if err := out.Chown(d, uid, gid); err != nil {
			return err
		}
	}
	tmp := rel + tmpMarker + "restore"
	f, err := out.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(plain)
	if err == nil {
		err = f.Chown(uid, gid)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = out.Rename(tmp, rel)
	}
	if err != nil {
		out.Remove(tmp)
	}
	return err
}

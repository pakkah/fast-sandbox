// Package blobcache keeps the overlaybd layer blobs a node already holds on
// that node's disk, so a same-node resume hands overlaybd a local file instead
// of fetching the blob from the registry again. Entries are content addressed
// and evicted least recently used. The cache is a copy of published bytes, not
// a source of truth: a missing entry always falls back to the registry.
package blobcache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/opencontainers/go-digest"
)

const (
	// DefaultMaxBytes bounds the cache. Layers are immutable, so the budget
	// only has to cover the checkpoints a node is expected to resume soon.
	DefaultMaxBytes int64 = 16 << 30

	// DefaultMinAge keeps a just-stored or just-opened blob out of a
	// concurrent trim. overlaybd opens a lower by path, and a file that
	// disappears between its access check and open fails the device instead
	// of falling back to the registry, so eviction keeps a grace period.
	DefaultMinAge = 10 * time.Minute

	digestDirName = "sha256"
)

// Root returns the node-scoped cache directory of a runtime state root. It
// intentionally sits beside the manifest cache rather than under a Pod-private
// directory: every Fastlet on the node shares the same layer bytes, and a Pod
// replacement must not drop them.
func Root(stateRoot string) string {
	return filepath.Join(stateRoot, "oci", "blobs")
}

// Path returns the cache file of a digest.
func Path(root string, dgst digest.Digest) string {
	return filepath.Join(root, digestDirName, dgst.Encoded())
}

// Hit reports whether the digest is cached at the expected size and refreshes
// its recency. Modification time is the LRU clock: overlaybd reports no reads,
// and a content-addressed file is otherwise never written again.
func Hit(root string, dgst digest.Digest, size int64) (string, bool) {
	if !supported(dgst) {
		return "", false
	}
	path := Path(root, dgst)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return "", false
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return path, true
}

// Store moves a published blob into the cache. The source is the staging file
// the publisher just uploaded; moving it keeps a single copy on disk. A blob
// that is already cached at the expected size is left untouched, so repeated
// publications of the same layer do not refresh its position in the LRU order.
func Store(root string, dgst digest.Digest, size int64, source string) error {
	if !supported(dgst) {
		return fmt.Errorf("blob cache: unsupported digest %q", dgst)
	}
	path := Path(root, dgst)
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() == size {
		return nil
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return fmt.Errorf("blob cache: %s has size %d, want %d", source, info.Size(), size)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	// Sync before the rename: a crash may persist the directory entry without
	// the data, and a same-size truncated blob would pass the size check.
	if err := syncFile(source); err != nil {
		return err
	}
	if err := os.Rename(source, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// Trim evicts least recently used blobs until the cache fits maxBytes and
// returns the bytes and the number of entries it removed. Blobs touched within
// minAge are never evicted.
func Trim(root string, maxBytes int64, minAge time.Duration) (int64, int, error) {
	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	dir := filepath.Join(root, digestDirName)
	items, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	entries := make([]entry, 0, len(items))
	var total int64
	for _, item := range items {
		if !item.Type().IsRegular() {
			continue
		}
		info, err := item.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return 0, 0, err
		}
		entries = append(entries, entry{path: filepath.Join(dir, item.Name()), size: info.Size(), mod: info.ModTime()})
		total += info.Size()
	}
	if total <= maxBytes {
		return 0, 0, nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod.Before(entries[j].mod) })
	cutoff := time.Now().Add(-minAge)
	var freed int64
	var removed int
	for _, entry := range entries {
		if total <= maxBytes {
			break
		}
		if entry.mod.After(cutoff) {
			break
		}
		if err := os.Remove(entry.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return freed, removed, err
		}
		total -= entry.size
		freed += entry.size
		removed++
	}
	return freed, removed, nil
}

// supported guards the digest before it is turned into a path: only validated
// sha256 digests can name a file.
func supported(dgst digest.Digest) bool {
	return dgst.Algorithm() == digest.SHA256 && dgst.Validate() == nil
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

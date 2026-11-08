package blobcache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// writeSource creates a staging blob with a controlled modification time.
func writeSource(t *testing.T, payload []byte, mod time.Time) (digest.Digest, string) {
	t.Helper()
	dgst := digest.FromBytes(payload)
	path := filepath.Join(t.TempDir(), "blob")
	require.NoError(t, os.WriteFile(path, payload, 0o600))
	require.NoError(t, os.Chtimes(path, mod, mod))
	return dgst, path
}

func TestStoreMovesBlobIntoDigestPath(t *testing.T) {
	root := t.TempDir()
	payload := []byte("layer")
	dgst, source := writeSource(t, payload, time.Now())

	require.NoError(t, Store(root, dgst, int64(len(payload)), source))
	require.NoFileExists(t, source, "the staging file is moved, not copied")
	require.FileExists(t, Path(root, dgst))

	path, ok := Hit(root, dgst, int64(len(payload)))
	require.True(t, ok)
	require.Equal(t, Path(root, dgst), path)

	duplicate, err := os.CreateTemp(t.TempDir(), "duplicate")
	require.NoError(t, err)
	_, err = duplicate.Write(payload)
	require.NoError(t, err)
	require.NoError(t, duplicate.Close())
	require.NoError(t, Store(root, dgst, int64(len(payload)), duplicate.Name()))
	require.FileExists(t, duplicate.Name(), "an existing entry leaves a duplicate source untouched")
}

func TestHitAndStoreRejectWrongContent(t *testing.T) {
	root := t.TempDir()
	payload := []byte("layer")
	dgst, source := writeSource(t, payload, time.Now())
	require.NoError(t, Store(root, dgst, int64(len(payload)), source))

	_, ok := Hit(root, dgst, int64(len(payload))+1)
	require.False(t, ok, "a file of another size is a miss")

	short := filepath.Join(t.TempDir(), "short")
	require.NoError(t, os.WriteFile(short, payload[:2], 0o600))
	err := Store(root, digest.FromBytes([]byte("other")), int64(len(payload)), short)
	require.ErrorContains(t, err, "size")
	err = Store(root, digest.Digest("sha256:../escape"), 1, short)
	require.ErrorContains(t, err, "unsupported digest")
}

func TestTrimEvictsLeastRecentlyUsedAboveBudget(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-time.Hour)
	var cached []digest.Digest
	for _, payload := range [][]byte{[]byte("aaaa"), []byte("bbbb"), []byte("cccc")} {
		dgst, source := writeSource(t, payload, old)
		require.NoError(t, Store(root, dgst, int64(len(payload)), source))
		cached = append(cached, dgst)
	}
	// A cache hit is the access the LRU order tracks.
	path, ok := Hit(root, cached[0], 4)
	require.True(t, ok)

	freed, removed, err := Trim(root, 8, 0)
	require.NoError(t, err)
	require.Equal(t, int64(4), freed)
	require.Equal(t, 1, removed)
	require.FileExists(t, path, "the most recently used entry survives")
	entries, err := os.ReadDir(filepath.Join(root, digestDirName))
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestTrimKeepsFreshBlobs(t *testing.T) {
	root := t.TempDir()
	dgst, source := writeSource(t, []byte("fresh"), time.Now())
	require.NoError(t, Store(root, dgst, int64(len("fresh")), source))

	freed, removed, err := Trim(root, 0, DefaultMinAge)
	require.NoError(t, err)
	require.Zero(t, freed)
	require.Zero(t, removed)
	require.FileExists(t, Path(root, dgst), "a blob within the grace period is never evicted")
}

func TestTrimMissingRootIsEmpty(t *testing.T) {
	freed, removed, err := Trim(filepath.Join(t.TempDir(), "absent"), 0, 0)
	require.NoError(t, err)
	require.Zero(t, freed)
	require.Zero(t, removed)
}

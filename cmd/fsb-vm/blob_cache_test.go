package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"fast-sandbox/internal/blobcache"
)

func TestLowersForUsesCachedBlobFiles(t *testing.T) {
	previous := global
	t.Cleanup(func() { global = previous })
	global.blobCacheDir = t.TempDir()

	payload := []byte("delta layer")
	dgst := digest.FromBytes(payload)
	source := filepath.Join(t.TempDir(), "blob")
	require.NoError(t, os.WriteFile(source, payload, 0o600))
	require.NoError(t, blobcache.Store(global.blobCacheDir, dgst, int64(len(payload)), source))

	missing := digest.FromString("missing")
	lowers := lowersFor(resolvedImage{Lowers: []lowerLayer{
		{Digest: dgst.String(), Size: int64(len(payload))},
		{Digest: missing.String(), Size: 16},
	}})
	require.Len(t, lowers, 2)
	require.Equal(t, blobcache.Path(global.blobCacheDir, dgst), lowers[0].File)
	require.Equal(t, dgst.String(), lowers[0].Digest)
	require.Equal(t, int64(len(payload)), lowers[0].Size)
	require.Empty(t, lowers[1].File, "an absent blob keeps the remote fetch")
	require.Equal(t, missing.String(), lowers[1].Digest)
	require.Equal(t, int64(16), lowers[1].Size)
}

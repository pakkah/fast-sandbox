package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"fast-sandbox/internal/ocitemplate"
)

// A blob store failure must not undo the metadata already written: resume
// metadata stays usable even when the packed layer cannot be cached.
func TestCheckpointCacheStoreFailureKeepsManifestCache(t *testing.T) {
	setupManifestCache(t)
	global.blobCacheDir = t.TempDir()
	raw := cacheTestManifest(t, "blob")
	reference := "registry.invalid/checkpoint@" + digest.FromBytes(raw).String()
	metadata, _ := json.Marshal(map[string]any{"oci": ocitemplate.Pair{Rootfs: reference, Snapfiles: reference}})
	result := &ocitemplate.PublishedCheckpoint{Reference: ocitemplate.Prefix + reference, Metadata: metadata, Manifests: map[string]json.RawMessage{reference: raw}}
	dir := t.TempDir()
	path := filepath.Join(dir, "blobs/sha256", digest.FromString("blob").Encoded())
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte("wrong length"), 0600))

	require.ErrorContains(t, cacheCheckpoint(result, dockerConfig{}, dir), "size")
	_, err := resolveImage(context.Background(), reference, dockerConfig{})
	require.NoError(t, err, "metadata remains usable independently of the blob cache")
}

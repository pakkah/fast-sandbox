package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/go-digest"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"

	"fast-sandbox/internal/blobcache"
	"fast-sandbox/internal/ocitemplate"
)

// Cache only immutable, already-published content. Metadata lands in the
// credential-scoped manifest cache; layer blobs move into the node-scoped blob
// cache, which the next create names in the lowers' file field.
func newCacheCheckpointCommand() *cobra.Command {
	var published, authFile, artifacts string
	cmd := &cobra.Command{Use: "cache-checkpoint", Short: "Cache a locally published OCI checkpoint", Hidden: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			raw, err := os.ReadFile(published)
			if err != nil {
				return err
			}
			var result ocitemplate.PublishedCheckpoint
			if err := json.Unmarshal(raw, &result); err != nil {
				return err
			}
			raw, err = os.ReadFile(authFile)
			if err != nil {
				return err
			}
			config, err := parseDockerConfig(string(raw))
			if err != nil {
				return err
			}
			return cacheCheckpoint(&result, config, artifacts)
		}}
	cmd.Flags().StringVar(&published, "published", "", "published immutable metadata file")
	cmd.Flags().StringVar(&authFile, "docker-auth-file", "", "restore credential file")
	cmd.Flags().StringVar(&artifacts, "artifacts", "", "local packed artifact directory")
	for _, flag := range []string{"published", "docker-auth-file", "artifacts"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}

func cacheCheckpoint(result *ocitemplate.PublishedCheckpoint, config dockerConfig, artifacts string) error {
	ref, err := parseRegistryReference(strings.TrimPrefix(result.Reference, ocitemplate.Prefix))
	if err != nil {
		return err
	}
	pair, err := templatePair(result.Metadata, ref)
	if err != nil {
		return err
	}
	if pair.Snapfiles != strings.TrimPrefix(result.Reference, ocitemplate.Prefix) {
		return fmt.Errorf("published checkpoint pair mismatch")
	}
	// Validate all manifests before writing metadata or moving any blob.
	var layers []oci.Descriptor
	for _, reference := range []string{pair.Rootfs, pair.Snapfiles} {
		imageRef, err := parseRegistryReference(reference)
		if err != nil {
			return err
		}
		raw := result.Manifests[reference]
		if digest.FromBytes(raw).String() != imageRef.Reference {
			return fmt.Errorf("published manifest digest mismatch")
		}
		var manifest oci.Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return err
		}
		layers = append(layers, manifest.Layers...)
	}
	for _, reference := range []string{pair.Rootfs, pair.Snapfiles} {
		imageRef, _ := parseRegistryReference(reference)
		path, err := createManifestCachePath(imageRef, config)
		if err != nil {
			return err
		}
		if err := cacheImageManifest(path, imageRef.Reference, result.Manifests[reference]); err != nil {
			return err
		}
	}
	path, err := createManifestCachePath(ref, config)
	if err != nil {
		return err
	}
	if err := cacheManifestFile(path+".template", result.Metadata); err != nil {
		return err
	}
	return storeCheckpointLayers(layers, artifacts)
}

// storeCheckpointLayers moves the layers this node just packed into the blob
// cache. Inherited layers never existed in the staging directory and stay
// remote: the digest and size remain in the lower, so a missing file falls
// back to the registry fetch.
func storeCheckpointLayers(layers []oci.Descriptor, artifacts string) error {
	root := blobCacheRoot()
	seen := make(map[digest.Digest]bool, len(layers))
	for _, layer := range layers {
		if layer.Digest.Validate() != nil || layer.Digest.Algorithm() != digest.SHA256 || layer.Size <= 0 {
			return fmt.Errorf("invalid checkpoint layer")
		}
		if seen[layer.Digest] {
			continue
		}
		seen[layer.Digest] = true
		source := filepath.Join(artifacts, "blobs/sha256", layer.Digest.Encoded())
		if err := blobcache.Store(root, layer.Digest, layer.Size, source); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // inherited layer, never downloaded into this staging
			}
			return err
		}
	}
	_, _, err := blobcache.Trim(root, blobcache.DefaultMaxBytes, blobcache.DefaultMinAge)
	return err
}

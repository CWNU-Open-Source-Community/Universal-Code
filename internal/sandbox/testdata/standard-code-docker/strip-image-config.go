//go:build ignore

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func read(path string) []byte {
	raw, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return raw
}

func write(path string, data []byte) {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		panic(err)
	}
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func main() {
	if len(os.Args) != 2 {
		panic("usage: strip-image-config <docker-save-directory>")
	}
	directory := os.Args[1]
	blobDirectory := filepath.Join(directory, "blobs", "sha256")
	var entries []struct {
		Config   string   `json:"Config"`
		RepoTags []string `json:"RepoTags"`
		Layers   []string `json:"Layers"`
	}
	if err := json.Unmarshal(read(filepath.Join(directory, "manifest.json")),
		&entries); err != nil || len(entries) != 1 {
		panic("fixture save must contain exactly one manifest entry")
	}
	oldConfigPath := filepath.Join(directory, filepath.FromSlash(entries[0].Config))
	var config map[string]any
	if err := json.Unmarshal(read(oldConfigPath), &config); err != nil {
		panic(err)
	}
	for _, key := range []string{"config", "container_config"} {
		if value, ok := config[key].(map[string]any); ok {
			delete(value, "Env")
			delete(value, "Volumes")
			delete(value, "Labels")
			if key == "config" {
				value["Labels"] = map[string]string{"org.traverse-board.project-actions": "1", "org.traverse-board.project-preparation": "1", "org.traverse-board.pnpm-version": "10.28.1"}
			}
		}
	}
	editedConfig, err := json.Marshal(config)
	if err != nil {
		panic(err)
	}
	configDigest := digestOf(editedConfig)

	var index map[string]any
	if err := json.Unmarshal(read(filepath.Join(directory, "index.json")), &index); err != nil {
		panic(err)
	}
	manifests, ok := index["manifests"].([]any)
	if !ok || len(manifests) != 1 {
		panic("fixture OCI index must contain exactly one manifest")
	}
	manifestEntry, ok := manifests[0].(map[string]any)
	if !ok {
		panic("fixture OCI manifest entry is invalid")
	}
	manifestDigest, ok := manifestEntry["digest"].(string)
	if !ok || len(manifestDigest) != 71 || manifestDigest[:7] != "sha256:" {
		panic("fixture OCI manifest digest is invalid")
	}
	oldManifestPath := filepath.Join(blobDirectory, manifestDigest[7:])
	var manifest map[string]any
	if err := json.Unmarshal(read(oldManifestPath), &manifest); err != nil {
		panic(err)
	}
	configReference, ok := manifest["config"].(map[string]any)
	if !ok {
		panic("fixture OCI manifest has no config descriptor")
	}
	configReference["digest"] = "sha256:" + configDigest
	configReference["size"] = len(editedConfig)
	editedManifest, err := json.Marshal(manifest)
	if err != nil {
		panic(err)
	}
	newManifestDigest := digestOf(editedManifest)
	manifestEntry["digest"] = "sha256:" + newManifestDigest
	manifestEntry["size"] = len(editedManifest)
	if annotations, ok := manifestEntry["annotations"].(map[string]any); ok {
		annotations["config.digest"] = "sha256:" + configDigest
	}
	editedIndex, err := json.Marshal(index)
	if err != nil {
		panic(err)
	}
	write(filepath.Join(directory, "index.json"), editedIndex)
	entries[0].Config = filepath.ToSlash(filepath.Join("blobs", "sha256", configDigest))
	editedEntries, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	write(filepath.Join(directory, "manifest.json"), editedEntries)
	write(filepath.Join(blobDirectory, configDigest), editedConfig)
	write(filepath.Join(blobDirectory, newManifestDigest), editedManifest)
	if filepath.Clean(oldConfigPath) != filepath.Join(blobDirectory, configDigest) {
		if err := os.Remove(oldConfigPath); err != nil {
			panic(err)
		}
	}
	if filepath.Clean(oldManifestPath) != filepath.Join(blobDirectory, newManifestDigest) {
		if err := os.Remove(oldManifestPath); err != nil {
			panic(err)
		}
	}
	fmt.Fprintln(os.Stderr, "fixed config without inherited environment or volumes: sha256:"+configDigest)
}

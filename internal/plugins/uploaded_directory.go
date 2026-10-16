package plugins

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/agentpackages"
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/toolcontract"
)

const (
	MaxUploadedDirectoryRootBytes = 255
	MaxUploadedDirectoryPathBytes = 4096
)

// UploadedDirectory contains package-relative ordinary file bytes. It never
// names a host directory or supplies trusted package metadata or file modes.
type UploadedDirectory struct {
	RootName string                  `json:"root_name"`
	Files    []UploadedDirectoryFile `json:"files"`
}

type UploadedDirectoryFile struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
}

func (f *UploadedDirectoryFile) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Path          string  `json:"path"`
		ContentBase64 *string `json:"content_base64"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.ContentBase64 == nil {
		return errors.New("uploaded file requires content_base64; use an empty string for an empty file")
	}
	*f = UploadedDirectoryFile{Path: wire.Path, ContentBase64: *wire.ContentBase64}
	return nil
}

// CaptureUploadedDirectory uses the same portable encoder and native metadata
// reader as filesystem acquisition. Canonical modes and sorted paths make the
// acquired bytes independent of input order and host filesystem permissions.
func CaptureUploadedDirectory(ctx context.Context, upload UploadedDirectory, packageID string,
	source toolcontract.SourceRef, scratchRoot string,
) (PortablePackage, error) {
	if err := ctx.Err(); err != nil {
		return PortablePackage{}, err
	}
	if !uploadedDirectoryPath(upload.RootName) || strings.Contains(upload.RootName, "/") ||
		len(upload.RootName) > MaxUploadedDirectoryRootBytes || strings.EqualFold(upload.RootName, ".git") ||
		source.Validate() != nil || (toolcontract.ComponentRef{PackageID: packageID, ComponentID: "package"}).Validate() != nil ||
		len(upload.Files) == 0 || len(upload.Files) > MaxSnapshotEntries {
		return PortablePackage{}, invalidUploadedDirectory("uploaded directory identity or file count is invalid")
	}
	entries := make(map[string]SnapshotEntry)
	aliases := make(map[string]string)
	contents := make(map[string][]byte, len(upload.Files))
	total := 0
	add := func(name, kind string) error {
		key := uploadedDirectoryPathKey(name)
		if previous, found := aliases[key]; found && previous != name {
			return invalidUploadedDirectory("uploaded directory contains case-conflicting paths")
		}
		if previous, found := entries[name]; found {
			if previous.Kind != kind || kind == "file" {
				return invalidUploadedDirectory("uploaded directory contains duplicate or conflicting paths")
			}
			return nil
		}
		if len(entries) == MaxSnapshotEntries {
			return invalidUploadedDirectory("uploaded directory exceeds the snapshot entry limit")
		}
		mode := uint32(0o700)
		if kind == "file" {
			mode = 0o600
		}
		entries[name], aliases[key] = SnapshotEntry{Path: name, Kind: kind, Mode: mode}, name
		return nil
	}
	for _, file := range upload.Files {
		if err := ctx.Err(); err != nil {
			return PortablePackage{}, err
		}
		if !uploadedDirectoryPath(file.Path) || strings.EqualFold(strings.Split(file.Path, "/")[0], ".git") {
			return PortablePackage{}, invalidUploadedDirectory("uploaded file path must be a portable package-relative path without Git administration")
		}
		if err := add(file.Path, "file"); err != nil {
			return PortablePackage{}, err
		}
		for parent := path.Dir(file.Path); parent != "."; parent = path.Dir(parent) {
			if err := add(parent, "directory"); err != nil {
				return PortablePackage{}, err
			}
		}
		encoded := file.ContentBase64
		remaining := MaxUncompressedBytes - total
		if len(encoded) > base64.StdEncoding.EncodedLen(remaining) || strings.ContainsAny(encoded, " \t\r\n") {
			return PortablePackage{}, invalidUploadedDirectory("uploaded file encoding exceeds the decoded byte limit or is not canonical base64")
		}
		value, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || len(value) > remaining {
			return PortablePackage{}, invalidUploadedDirectory("uploaded file must contain canonical bounded base64")
		}
		total += len(value)
		entry := entries[file.Path]
		entry.SHA256, entry.Bytes = digest(value), len(value)
		entries[file.Path], contents[file.Path] = entry, value
	}
	inventory := make([]SnapshotEntry, 0, len(entries))
	for _, entry := range entries {
		inventory = append(inventory, entry)
	}
	sort.Slice(inventory, func(i, j int) bool { return inventory[i].Path < inventory[j].Path })
	raw, err := encodeSnapshot(ctx, upload.RootName, inventory, contents)
	if err != nil {
		if ctx.Err() != nil {
			return PortablePackage{}, ctx.Err()
		}
		return PortablePackage{}, apperror.Wrap(apperror.CodeInvalidArgument, "uploaded directory archive exceeds portable limits", err)
	}
	snapshot := PortableSnapshot{ProtocolVersion: PortableSnapshotProtocol, PackageID: packageID,
		Revision: digest(raw), RootName: upload.RootName, Source: source, Inventory: inventory}
	directory, cleanup, err := materializeSnapshot(ctx, snapshot, raw, scratchRoot)
	if err != nil {
		return PortablePackage{}, err
	}
	defer cleanup()
	pkg, err := agentpackages.OpenDirectory(ctx, directory, packageID, source)
	if err != nil {
		if ctx.Err() != nil {
			return PortablePackage{}, ctx.Err()
		}
		return PortablePackage{}, apperror.Wrap(apperror.CodeInvalidArgument, "uploaded Skill directory failed native validation", err)
	}
	defer pkg.Close()
	describeSnapshot(&snapshot, pkg)
	if err := snapshot.Validate(); err != nil {
		return PortablePackage{}, apperror.Wrap(apperror.CodeInvalidArgument, "uploaded directory snapshot is invalid", err)
	}
	return PortablePackage{Snapshot: snapshot, raw: raw}, nil
}

func invalidUploadedDirectory(message string) error {
	return apperror.New(apperror.CodeInvalidArgument, message)
}

func uploadedDirectoryPath(value string) bool {
	if len(value) > MaxUploadedDirectoryPathBytes || !utf8.ValidString(value) || !snapshotPath(value) ||
		strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if len(part) > MaxUploadedDirectoryRootBytes || strings.TrimSpace(part) != part ||
			strings.HasSuffix(part, ".") || strings.ContainsAny(part, `<>"|?*`) {
			return false
		}
		stem, _, _ := strings.Cut(strings.ToUpper(part), ".")
		device := []rune(stem)
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" ||
			stem == "CONIN$" || stem == "CONOUT$" ||
			(len(device) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && strings.ContainsRune("123456789¹²³", device[3])) {
			return false
		}
	}
	return true
}

func uploadedDirectoryPathKey(value string) string {
	return strings.Map(func(r rune) rune {
		minimum := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			minimum = min(minimum, next)
		}
		return minimum
	}, value)
}

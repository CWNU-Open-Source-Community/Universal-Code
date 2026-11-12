package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Operator script hooks are declared only in the operator-owned hooks
// directory. The workspace, repository, project instructions, skills, plugins,
// and tool output can never add or widen them: a repo-controlled file cannot
// turn prompt injection into script execution.
const (
	// OperatorPluginID binds operator script hook audit rows; it is a reserved
	// plugin identity and must not be used by real plugin packages.
	OperatorPluginID      = "operator"
	scriptDeclarationExt  = ".json"
	MaxScriptDeclarations = 64
	MaxDeclarationBytes   = 64 * 1024
)

// LoadOperatorScriptHooks reads operator-owned script hook declarations from
// the given hooks directory. An empty or missing directory means no script
// hooks are configured. Every anomaly — oversized or malformed declarations,
// links, unreadable or hash-drifted scripts — fails closed instead of being
// skipped.
func LoadOperatorScriptHooks(ctx context.Context, dir string) ([]Registration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, nil
	}
	if filepath.Clean(dir) != dir || !filepath.IsAbs(dir) {
		return nil, errors.New("script hook directory must be an absolute clean path")
	}
	rootInfo, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect script hook directory: %w", err)
	}
	if rootInfo.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 || !rootInfo.IsDir() {
		return nil, errors.New("script hook directory is not a real directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open script hook directory: %w", err)
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("list script hook directory: %w", err)
	}
	registrations := make([]Registration, 0)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, scriptDeclarationExt) {
			continue
		}
		if len(registrations) >= MaxScriptDeclarations {
			return nil, fmt.Errorf("script hook directory exceeds %d declarations", MaxScriptDeclarations)
		}
		registration, err := loadScriptDeclaration(root, name)
		if err != nil {
			return nil, fmt.Errorf("script hook declaration %s: %w", name, err)
		}
		registrations = append(registrations, registration)
	}
	return registrations, nil
}

// MergeRegistrations combines enabled plugin hook declarations with
// operator-owned script hook declarations under the shared registry ceiling.
// The combined count is checked here so an over-cap operator configuration
// fails with one diagnosable error instead of locking every hook boundary.
func MergeRegistrations(plugin, operator []Registration) ([]Registration, error) {
	if len(plugin)+len(operator) > MaxDeclarations {
		return nil, fmt.Errorf("enabled plugin hooks (%d) plus operator script hooks (%d) exceed the %d declaration limit; remove script declarations or disable plugin hooks",
			len(plugin), len(operator), MaxDeclarations)
	}
	if len(operator) == 0 {
		return plugin, nil
	}
	return append(plugin, operator...), nil
}

func loadScriptDeclaration(root *os.Root, name string) (Registration, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return Registration{}, fmt.Errorf("inspect declaration: %w", err)
	}
	if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 || !info.Mode().IsRegular() {
		return Registration{}, errors.New("declaration is not a regular file")
	}
	if info.Size() > MaxDeclarationBytes {
		return Registration{}, fmt.Errorf("declaration exceeds %d bytes", MaxDeclarationBytes)
	}
	raw, err := readRootFileTwice(root, name)
	if err != nil {
		return Registration{}, err
	}
	var declaration Declaration
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&declaration); err != nil {
		return Registration{}, fmt.Errorf("declaration is not valid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Registration{}, errors.New("declaration contains trailing JSON")
	}
	if err := declaration.Validate(); err != nil {
		return Registration{}, err
	}
	if declaration.Action != ActionScript || declaration.Script == nil {
		return Registration{}, errors.New("operator declarations must declare a script action")
	}
	content, err := verifyScriptContent(root, declaration.Script.Path)
	if err != nil {
		return Registration{}, err
	}
	if scriptDigest(content) != declaration.Script.SHA256 {
		return Registration{}, errors.New("script content does not match its pinned digest")
	}
	return Registration{PluginID: OperatorPluginID,
		PluginFingerprint: declaration.Script.SHA256, Declaration: declaration}, nil
}

// readRootFileTwice reads one bounded root-relative file and rejects any
// concurrent replacement observed between the two reads.
func readRootFileTwice(root *os.Root, name string) ([]byte, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect declaration: %w", err)
	}
	raw, err := root.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read declaration: %w", err)
	}
	after, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("re-inspect declaration: %w", err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("declaration changed while reading")
	}
	reRead, err := root.ReadFile(name)
	if err != nil || !bytes.Equal(raw, reRead) {
		return nil, errors.New("declaration changed while reading")
	}
	return raw, nil
}

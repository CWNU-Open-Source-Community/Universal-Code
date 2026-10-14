package projectprep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/workspaceidentity"
)

// Detect reads one explicit directory. It does not scan dependencies or execute
// package-manager probes, and os.Root keeps every read inside the workspace.
func Detect(ctx context.Context, workspaceRoot, target string) (Plan, error) {
	if target == "" {
		target = "."
	}
	plan := Plan{Version: ProtocolVersion, Target: target, Lockfiles: []File{}, Sources: []string{}, Packages: []Package{}, Issues: []string{}}
	if err := (workspaceidentity.PathPolicy{}).Check(target); err != nil {
		return plan, err
	}
	if !filepath.IsAbs(workspaceRoot) {
		return plan, errors.New("preparation requires an absolute workspace root")
	}
	if err := ctx.Err(); err != nil {
		return plan, err
	}
	root, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return plan, errors.New("preparation workspace is unavailable")
	}
	defer root.Close()
	// Reject directory indirection as well as file links; no repository link
	// chooses which project is reviewed.
	for current := target; current != "."; current = path.Dir(current) {
		info, err := root.Lstat(filepath.FromSlash(current))
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return plan, errors.New("preparation target must be an existing directory without symlink indirection")
		}
	}
	reader := fileReader{ctx: ctx, root: root, target: target}
	known := []string{"package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb", "go.mod", "go.sum", "go.work", "Cargo.toml", "Cargo.lock", "pyproject.toml", "requirements.txt", "poetry.lock", "uv.lock", "Pipfile", "Pipfile.lock", "deno.json", "deno.jsonc", "composer.json", "composer.lock", "Gemfile", "Gemfile.lock", ".npmrc", ".nvmrc", ".node-version", ".tool-versions"}
	files := make(map[string]sourceFile)
	for _, name := range known {
		value, found, err := reader.read(name)
		if err != nil {
			if ctx.Err() != nil {
				return plan, ctx.Err()
			}
			plan.Issues = append(plan.Issues, fmt.Sprintf("%s: %s", path.Join(target, name), err))
			continue
		}
		if found {
			files[name] = value
		}
	}
	_, npm := files["package.json"]
	_, goModule := files["go.mod"]
	if npm && goModule {
		plan.Issues = append(plan.Issues, "Both package.json and go.mod are present; select a directory with one dependency toolchain")
	} else if npm {
		plan = detectNPM(plan, files)
	} else if goModule {
		plan = detectGo(plan, files)
	} else {
		for _, candidate := range []struct{ name, ecosystem, toolchain string }{
			{"Cargo.toml", "rust", "cargo"}, {"pyproject.toml", "python", "python"}, {"requirements.txt", "python", "pip"}, {"Pipfile", "python", "pipenv"}, {"deno.json", "deno", "deno"}, {"deno.jsonc", "deno", "deno"}, {"composer.json", "php", "composer"}, {"Gemfile", "ruby", "bundler"},
		} {
			if file, found := files[candidate.name]; found {
				plan.Ecosystem, plan.Toolchain, plan.Manifest = candidate.ecosystem, candidate.toolchain, file.File
				plan.Issues = append(plan.Issues, "Dependency preparation for "+candidate.toolchain+" is not supported; configure an explicitly reviewed sandbox command")
				break
			}
		}
		if plan.Ecosystem == "" {
			plan.Issues = append(plan.Issues, "No supported package.json or go.mod was found in the selected directory")
		}
		for name, file := range files {
			if strings.Contains(name, "lock") || name == "go.sum" {
				plan.Lockfiles = append(plan.Lockfiles, file.File)
			}
		}
	}
	if npm || goModule {
		for _, name := range []string{"Cargo.toml", "pyproject.toml", "requirements.txt", "Pipfile", "deno.json", "deno.jsonc", "composer.json", "Gemfile"} {
			if file, found := files[name]; found {
				plan.Lockfiles = append(plan.Lockfiles, file.File)
				plan.Issues = append(plan.Issues, name+" declares another dependency toolchain in this directory; select a standalone project or configure an explicitly reviewed sandbox command")
			}
		}
	}
	sort.Slice(plan.Lockfiles, func(i, j int) bool { return plan.Lockfiles[i].Path < plan.Lockfiles[j].Path })
	sort.Strings(plan.Sources)
	sort.Slice(plan.Packages, func(i, j int) bool { return plan.Packages[i].ID < plan.Packages[j].ID })
	plan.seal()
	return plan, nil
}

type sourceFile struct {
	File
	raw []byte
}
type fileReader struct {
	ctx    context.Context
	root   *os.Root
	target string
}

func (r fileReader) read(name string) (sourceFile, bool, error) {
	if err := r.ctx.Err(); err != nil {
		return sourceFile{}, false, err
	}
	relative := path.Join(r.target, name)
	info, err := r.root.Lstat(filepath.FromSlash(relative))
	if errors.Is(err, fs.ErrNotExist) {
		return sourceFile{}, false, nil
	}
	if err != nil {
		return sourceFile{}, false, errors.New("file cannot be inspected")
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return sourceFile{}, false, errors.New("expected a regular file without symlink indirection")
	}
	if info.Size() > MaxFileBytes {
		return sourceFile{}, false, errors.New("file exceeds the preparation size limit")
	}
	file, err := r.root.Open(filepath.FromSlash(relative))
	if err != nil {
		return sourceFile{}, false, errors.New("file cannot be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return sourceFile{}, false, errors.New("file changed during open")
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil || len(raw) > MaxFileBytes {
		return sourceFile{}, false, errors.New("file cannot be read within the preparation size limit")
	}
	after, err := r.root.Lstat(filepath.FromSlash(relative))
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return sourceFile{}, false, errors.New("file changed during preparation detection")
	}
	if !utf8.Valid(raw) || strings.ContainsRune(string(raw), 0) {
		return sourceFile{}, false, errors.New("expected UTF-8 content without NUL bytes")
	}
	sum := sha256.Sum256(raw)
	return sourceFile{File: File{Path: relative, SHA256: hex.EncodeToString(sum[:])}, raw: raw}, true, nil
}

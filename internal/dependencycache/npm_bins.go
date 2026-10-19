package dependencycache

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"cyberagent-workbench/internal/projectprep"
)

var npmBinName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]*$`)
var npmBinPath = regexp.MustCompile(`^[A-Za-z0-9_./+@-]+$`)

// FinalizeNPM makes ordinary command wrappers after a successful script-free
// npm installation. Every target stays within its installed package directory;
// this reads metadata and writes files but never executes package content.
// The caller holds the same Run lease through the installation and this step.
func FinalizeNPM(ctx context.Context, workspaceRoot, target string, guest bool) error {
	return finalizeNPM(ctx, workspaceRoot, target, guest, nil)
}

// FinalizeNPMWorkspace accepts only the exact internal links from the reviewed
// lock. Member-local installations are inspected along with the shared root.
func FinalizeNPMWorkspace(ctx context.Context, workspaceRoot string, plan projectprep.Plan, guest bool) error {
	if plan.WorkspaceRoot == "" {
		return FinalizeNPM(ctx, workspaceRoot, plan.Target, guest)
	}
	opened, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return err
	}
	for link, member := range plan.NPMWorkspaceLinks {
		if !npmWorkspaceLinkMatches(opened, workspaceRoot, link, member) {
			opened.Close()
			return errors.New("installed npm workspace link is missing or changed")
		}
	}
	if err := opened.Close(); err != nil {
		return err
	}
	directories := map[string]bool{plan.WorkspaceRoot: true}
	for _, member := range plan.NPMWorkspaceLinks {
		directories[member] = true
	}
	names := make([]string, 0, len(directories))
	for directory := range directories {
		names = append(names, directory)
	}
	sort.Strings(names)
	for _, directory := range names {
		if err := finalizeNPM(ctx, workspaceRoot, directory, guest, plan.NPMWorkspaceLinks); err != nil {
			return err
		}
	}
	return nil
}

func finalizeNPM(ctx context.Context, workspaceRoot, target string, guest bool, workspaceLinks map[string]string) error {
	if ctx == nil || !filepath.IsAbs(workspaceRoot) || path.IsAbs(target) || strings.Contains(target, `\`) || (target != "" && target != "." && (path.Clean(target) != target || target == ".." || strings.HasPrefix(target, "../"))) {
		return errors.New("npm finalization target is invalid")
	}
	root, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	modules := path.Join(target, "node_modules")
	info, err := root.Lstat(modules)
	if errors.Is(err, os.ErrNotExist) {
		// A successful npm ci with no applicable dependencies need not create
		// node_modules. The selected project itself must still be an ordinary
		// directory; a missing or linked project is not an empty installation.
		project, projectErr := root.Lstat(path.Clean(target))
		if projectErr != nil || !project.IsDir() || project.Mode()&os.ModeSymlink != 0 {
			return errors.New("installed npm project directory is unavailable")
		}
		return ctx.Err()
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("installed node_modules directory is unavailable")
	}
	count := 0
	workspaceBins := map[string]string{}
	var visit fs.WalkDirFunc
	visit = func(current string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		count++
		if count > 250000 {
			return errors.New("npm installation exceeds its entry limit")
		}
		if entry.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			expected, allowed := workspaceLinks[current]
			if !allowed || !npmWorkspaceLinkMatches(root, workspaceRoot, current, expected) {
				return errors.New("installed npm package contains an unreviewed symbolic link")
			}
			// Read the bound member directly. Windows junctions contain an
			// absolute target, which os.Root intentionally will not traverse.
			manifest := path.Join(expected, "package.json")
			info, err := root.Lstat(manifest)
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("linked npm workspace package manifest is unavailable")
			}
			binDirectory, ok := packageBinDirectory(current)
			if !ok {
				return errors.New("installed npm workspace link has an invalid package location")
			}
			workspaceBins[manifest] = binDirectory
			if err := visit(manifest, fs.FileInfoToDirEntry(info), nil); err != nil {
				return err
			}
			if !npmWorkspaceLinkMatches(root, workspaceRoot, current, expected) {
				return errors.New("installed npm workspace link changed during finalization")
			}
			return nil
		}
		if entry.IsDir() || entry.Name() != "package.json" {
			return nil
		}
		packageDirectory := path.Dir(current)
		binDirectory, ok := packageBinDirectory(packageDirectory)
		if reviewedBin, exists := workspaceBins[current]; exists {
			binDirectory, ok = reviewedBin, true
		}
		if !ok {
			return nil
		}
		file, err := root.Open(current)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(file, 1024*1024+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(body) > 1024*1024 {
			return errors.New("installed npm package manifest is invalid")
		}
		var manifest struct {
			Name string          `json:"name"`
			Bin  json.RawMessage `json:"bin"`
		}
		if err := json.Unmarshal(body, &manifest); err != nil {
			return errors.New("installed npm package manifest is invalid")
		}
		if len(manifest.Bin) == 0 || string(manifest.Bin) == "null" {
			return nil
		}
		bins := map[string]string{}
		if manifest.Bin[0] == '"' {
			var value string
			if err := json.Unmarshal(manifest.Bin, &value); err != nil {
				return err
			}
			bins[path.Base(manifest.Name)] = value
		} else if manifest.Bin[0] == '[' {
			var values []string
			if err := json.Unmarshal(manifest.Bin, &values); err != nil {
				return errors.New("installed npm bin metadata is invalid")
			}
			if len(values) > 256 {
				return errors.New("installed npm package has too many commands")
			}
			for _, value := range values {
				bins[path.Base(value)] = value
			}
		} else if err := json.Unmarshal(manifest.Bin, &bins); err != nil {
			return errors.New("installed npm bin metadata is invalid")
		}
		if len(bins) > 256 {
			return errors.New("installed npm package has too many commands")
		}
		names := make([]string, 0, len(bins))
		for name := range bins {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			bin := strings.TrimPrefix(bins[name], "./")
			if !npmBinName.MatchString(name) || !npmBinPath.MatchString(bin) || path.IsAbs(bin) || path.Clean(bin) != bin || bin == ".." || strings.HasPrefix(bin, "../") {
				return errors.New("installed npm command escaped its package directory")
			}
			binary := path.Join(packageDirectory, bin)
			info, err := root.Lstat(binary)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("installed npm command target is unavailable")
			}
			commandFile, err := root.Open(binary)
			if err != nil {
				return err
			}
			header, readErr := io.ReadAll(io.LimitReader(commandFile, 256))
			closeErr := commandFile.Close()
			if readErr != nil || closeErr != nil {
				return errors.New("installed npm command cannot be inspected")
			}
			line, _, _ := strings.Cut(string(header), "\n")
			line = strings.TrimSuffix(line, "\r")
			nodeCommand := strings.HasSuffix(bin, ".js") || strings.HasSuffix(bin, ".cjs") || strings.HasSuffix(bin, ".mjs") || line == "#!/usr/bin/env node" || line == "#!/usr/bin/node" || line == "#!/usr/local/bin/node"
			// Relative paths are composed solely of validated components. Rooted
			// opens also reject a concurrent replacement that escapes Workspace.
			relative, err := filepath.Rel(filepath.FromSlash(binDirectory), filepath.FromSlash(binary))
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if guest {
				interpreter := ""
				if nodeCommand {
					interpreter = "node "
				} else if err := root.Chmod(binary, 0755); err != nil {
					return err
				}
				body := []byte("#!/bin/sh\nbasedir=$(dirname \"$0\")\nexec " + interpreter + "\"$basedir/" + relative + "\" \"$@\"\n")
				if err := writeAtomic(root, path.Join(binDirectory, name), body, 0755); err != nil {
					return err
				}
				if err := root.Chmod(path.Join(binDirectory, name), 0755); err != nil {
					return err
				}
			} else {
				interpreter := ""
				if nodeCommand {
					interpreter = "node "
				} else if !strings.HasSuffix(strings.ToLower(bin), ".exe") && !strings.HasSuffix(strings.ToLower(bin), ".cmd") && !strings.HasSuffix(strings.ToLower(bin), ".bat") {
					return errors.New("installed npm command requires an unsupported Windows interpreter")
				}
				cmd := []byte("@ECHO off\r\n" + interpreter + "\"%~dp0" + strings.ReplaceAll(relative, "/", `\`) + "\" %*\r\n")
				if err := writeAtomic(root, path.Join(binDirectory, name+".cmd"), cmd, 0600); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return fs.WalkDir(root.FS(), modules, visit)
}

func npmWorkspaceLinkMatches(root *os.Root, workspaceRoot, current, expected string) bool {
	if path.Clean(expected) != expected || path.IsAbs(expected) || expected == ".." || strings.HasPrefix(expected, "../") {
		return false
	}
	for directory := expected; directory != "."; directory = path.Dir(directory) {
		info, err := root.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	target, err := root.Readlink(current)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(workspaceRoot, filepath.FromSlash(path.Dir(current)), target)
	}
	return filepath.Clean(target) == filepath.Join(workspaceRoot, filepath.FromSlash(expected))
}

func packageBinDirectory(directory string) (string, bool) {
	parts := strings.Split(directory, "/")
	index := -1
	for i, part := range parts {
		if part == "node_modules" {
			index = i
		}
	}
	if index < 0 {
		return "", false
	}
	tail := parts[index+1:]
	if len(tail) != 1 && !(len(tail) == 2 && strings.HasPrefix(tail[0], "@")) {
		return "", false
	}
	return path.Join(strings.Join(parts[:index+1], "/"), ".bin"), true
}

package projectprep

import (
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/semver"
)

const CargoRegistrySource = "registry+https://github.com/rust-lang/crates.io-index"
const CargoOrigin = "https://static.crates.io"
const KindCargoCrate = "cargo_crate"

var cargoName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,63}$`)

func ValidCargoPackage(name, version string) bool {
	return cargoName.MatchString(name) && semver.IsValid("v"+version) && semver.Canonical("v"+version) != ""
}

func CargoVendorDirectory(target string) string {
	return path.Join(StageDirectory(target), "cargo-vendor")
}

// The selected directory is always the preparation root. Cargo must not find
// another workspace or local dependency outside that reviewed project.
func detectCargo(plan Plan, reader fileReader, files map[string]sourceFile) Plan {
	plan.Ecosystem, plan.Toolchain, plan.Manifest = "rust", "cargo", files["Cargo.toml"].File
	plan.Install = Command{ID: "install.rust", Display: "cargo fetch --offline --locked", Arguments: []string{"fetch", "--offline", "--locked"}}
	plan.Test = Command{ID: "test.rust", Display: "cargo test --offline --locked", Arguments: []string{"test", "--offline", "--locked"}}
	issue := func(message string) { plan.Issues = append(plan.Issues, message) }
	lockFile, found := files["Cargo.lock"]
	if !found {
		issue("Cargo.lock is missing; select the explicit Cargo project/workspace root and review its lockfile")
		return plan
	}
	plan.Lockfiles = append(plan.Lockfiles, lockFile.File)
	var lock struct {
		Version  int                                                `toml:"version"`
		Packages []struct{ Name, Version, Source, Checksum string } `toml:"package"`
	}
	if err := toml.Unmarshal(lockFile.raw, &lock); err != nil || (lock.Version != 3 && lock.Version != 4) || len(lock.Packages) > MaxPackages {
		issue("Cargo.lock must be a bounded version 3 or 4 lockfile")
		return plan
	}
	manifests := map[string]sourceFile{plan.Target: files["Cargo.toml"]}
	// Bound traversal discovers manifest names only; only members and recursive
	// path dependencies below the selected root are read and included in review.
	directories := []string{}
	entries := 0
	err := fs.WalkDir(reader.root.FS(), plan.Target, func(current string, entry fs.DirEntry, walkErr error) error {
		if err := reader.ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > 25000 {
			return fmt.Errorf("Cargo project discovery exceeds its entry limit")
		}
		if entry.IsDir() && current != plan.Target && (entry.Name() == ".git" || entry.Name() == "target" || entry.Name() == "node_modules" || entry.Name() == ".traverse-preparation") {
			return fs.SkipDir
		}
		if entry.Name() == "Cargo.toml" {
			directories = append(directories, path.Dir(current))
		}
		return nil
	})
	if err != nil {
		issue(err.Error())
		return plan
	}
	readManifest := func(directory string) bool {
		if _, exists := manifests[directory]; exists {
			return true
		}
		if len(manifests) >= 60 {
			issue("Cargo preparation supports at most 60 local manifests")
			return false
		}
		if !cargoWithin(plan.Target, directory) {
			issue("Cargo local dependency is outside the selected project root")
			return false
		}
		for current := directory; current != plan.Target; current = path.Dir(current) {
			info, err := reader.root.Lstat(current)
			if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
				issue("Cargo member directory is missing or linked: " + directory)
				return false
			}
		}
		at := reader
		at.target = directory
		file, found, err := at.read("Cargo.toml")
		if err != nil || !found {
			issue("Cargo member has no regular Cargo.toml: " + directory)
			return false
		}
		manifests[directory] = file
		plan.Lockfiles = append(plan.Lockfiles, file.File)
		return true
	}
	var root map[string]any
	if err := toml.Unmarshal(files["Cargo.toml"].raw, &root); err != nil {
		issue("Cargo.toml is invalid TOML")
		return plan
	}
	workspace, _ := root["workspace"].(map[string]any)
	if workspace != nil {
		members, err := cargoPatterns(workspace["members"])
		if err != nil {
			issue(err.Error())
		}
		exclude, err := cargoPatterns(workspace["exclude"])
		if err != nil {
			issue(err.Error())
		}
		for _, directory := range directories {
			relative := cargoRelative(plan.Target, directory)
			if cargoMatches(members, relative) && !cargoMatches(exclude, relative) {
				readManifest(directory)
			}
		}
		for _, pattern := range members {
			matched := false
			for _, directory := range directories {
				matched = matched || pnpmMatch(pattern, cargoRelative(plan.Target, directory))
			}
			if !matched {
				issue("Cargo workspace member pattern has no manifest: " + pattern)
			}
		}
	}
	local := map[string]bool{}
	visited := map[string]bool{}
	for len(visited) < len(manifests) {
		pending := []string{}
		for directory := range manifests {
			if !visited[directory] {
				pending = append(pending, directory)
			}
		}
		sort.Strings(pending)
		for _, directory := range pending {
			visited[directory] = true
			file := manifests[directory]
			var manifest map[string]any
			if err := toml.Unmarshal(file.raw, &manifest); err != nil {
				issue(file.Path + " is invalid TOML")
				continue
			}
			if manifest["patch"] != nil || manifest["replace"] != nil {
				issue(file.Path + " uses unsupported Cargo patch/replace sources")
			}
			if pkg, ok := manifest["package"].(map[string]any); ok {
				name, _ := pkg["name"].(string)
				version, _ := pkg["version"].(string)
				if inherited, ok := pkg["version"].(map[string]any); ok && inherited["workspace"] == true {
					defaults, _ := workspace["package"].(map[string]any)
					version, _ = defaults["version"].(string)
				}
				if pkg["version"] == nil {
					version = "0.0.0"
				}
				id := name + "@" + version
				if !ValidCargoPackage(name, version) || local[id] {
					issue(file.Path + " has an invalid or duplicate local package identity")
				}
				local[id] = true
				if pkg["workspace"] != nil {
					issue(file.Path + " explicitly redirects its workspace; select that root without a package.workspace override")
				}
			} else if directory != plan.Target || workspace == nil {
				issue(file.Path + " must declare a package")
			}
			var inspectDependencies func(map[string]any)
			inspectDependencies = func(section map[string]any) {
				for _, key := range []string{"dependencies", "dev-dependencies", "build-dependencies"} {
					dependencies, _ := section[key].(map[string]any)
					for _, raw := range dependencies {
						dependency, ok := raw.(map[string]any)
						if !ok {
							continue
						}
						if dependency["git"] != nil || dependency["registry"] != nil || dependency["registry-index"] != nil {
							issue(file.Path + " uses a Git or custom registry dependency")
						}
						if rawPath, exists := dependency["path"]; exists {
							relative, ok := rawPath.(string)
							if !ok || relative == "" || path.IsAbs(relative) || strings.ContainsAny(relative, "\\:\x00\r\n") {
								issue(file.Path + " has an invalid local dependency path")
								continue
							}
							readManifest(path.Join(directory, relative))
						}
					}
				}
			}
			inspectDependencies(manifest)
			if directory == plan.Target && workspace != nil {
				inspectDependencies(workspace)
			}
			if targets, ok := manifest["target"].(map[string]any); ok {
				for _, raw := range targets {
					if target, ok := raw.(map[string]any); ok {
						inspectDependencies(target)
					}
				}
			}
		}
	}
	// Cargo reads configuration from the current directory and its ancestors.
	// Bind these files and reject source/registry overlays and toolchain downloads.
	for directory := plan.Target; ; directory = path.Dir(directory) {
		at := reader
		at.target = directory
		if directory != plan.Target {
			if file, found, err := at.read("Cargo.toml"); err != nil {
				issue("Ancestor Cargo.toml cannot be inspected")
			} else if found {
				plan.Lockfiles = append(plan.Lockfiles, file.File)
				var ancestor map[string]any
				if toml.Unmarshal(file.raw, &ancestor) != nil || ancestor["workspace"] != nil {
					issue("Select the explicit Cargo workspace root instead of a nested member")
				}
			}
		}
		for _, name := range []string{".cargo/config", ".cargo/config.toml", "rust-toolchain", "rust-toolchain.toml"} {
			file, found, err := at.read(name)
			if err != nil {
				issue(path.Join(directory, name) + ": " + err.Error())
			}
			if found {
				plan.Lockfiles = append(plan.Lockfiles, file.File)
				if strings.HasPrefix(name, "rust-toolchain") {
					issue(file.Path + " selects a toolchain; prepare with an explicitly installed toolchain instead")
					continue
				}
				var config map[string]any
				if toml.Unmarshal(file.raw, &config) != nil {
					issue(file.Path + " is invalid Cargo configuration")
				}
				for key := range config {
					if key != "net" && key != "term" && key != "alias" {
						issue(file.Path + " uses unsupported Cargo source, toolchain, environment or path configuration: " + key)
					}
				}
			}
		}
		if directory == "." {
			break
		}
	}
	seen := map[string]bool{}
	for _, pkg := range lock.Packages {
		id := pkg.Name + "@" + pkg.Version
		if !ValidCargoPackage(pkg.Name, pkg.Version) || seen[id] {
			issue("Cargo.lock has an invalid or duplicate package identity")
			continue
		}
		seen[id] = true
		if pkg.Source == "" {
			if !local[id] || pkg.Checksum != "" {
				issue("Cargo.lock local package is outside the bound manifests: " + id)
			}
			continue
		}
		checksum, err := hex.DecodeString(pkg.Checksum)
		if pkg.Source != CargoRegistrySource || err != nil || len(checksum) != 32 || strings.ToLower(pkg.Checksum) != pkg.Checksum {
			issue("Cargo.lock requires crates.io packages with exact SHA-256 checksums: " + id)
			continue
		}
		plan.Packages = append(plan.Packages, Package{ID: id, Kind: KindCargoCrate, ModulePath: pkg.Name, Version: pkg.Version, Integrity: pkg.Checksum, URL: CargoOrigin + "/crates/" + pkg.Name + "/" + pkg.Name + "-" + pkg.Version + ".crate"})
	}
	for id := range local {
		if !seen[id] {
			issue("Cargo.lock is missing local package " + id)
		}
	}
	if len(plan.Packages) > 0 {
		plan.Sources = append(plan.Sources, CargoOrigin)
	}
	if len(plan.Lockfiles) > 63 {
		issue("Cargo preparation supports at most 64 bound files")
	}
	return plan
}

func cargoWithin(root, directory string) bool {
	return directory == root || root == "." && directory != ".." && !strings.HasPrefix(directory, "../") || strings.HasPrefix(directory, root+"/")
}
func cargoRelative(root, directory string) string {
	if root == "." {
		return directory
	}
	if root == directory {
		return "."
	}
	return strings.TrimPrefix(directory, root+"/")
}
func cargoMatches(patterns []string, directory string) bool {
	for _, pattern := range patterns {
		if pnpmMatch(pattern, directory) {
			return true
		}
	}
	return false
}
func cargoPatterns(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok || len(values) > 128 {
		return nil, fmt.Errorf("Cargo members/exclude must be a bounded array of relative paths")
	}
	patterns := []string{}
	for _, raw := range values {
		pattern, ok := raw.(string)
		if !ok || pattern == "" || path.IsAbs(pattern) || path.Clean(pattern) != pattern || pattern == ".." || strings.HasPrefix(pattern, "../") || strings.ContainsAny(pattern, "\\:{}[]!\x00\r\n") {
			return nil, fmt.Errorf("Cargo member pattern is unsupported or outside the selected root")
		}
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

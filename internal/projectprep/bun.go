package projectprep

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/tidwall/jsonc"
)

const BunVersion = "1.4.3"
const KindBunTarball = "bun_tarball"
const BunRegistryOrigin = "https://registry.npmjs.org"

var bunStableVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func BunCacheDirectory(target string) string { return path.Join(StageDirectory(target), "bun-cache") }

// Bun 1.4.3's default-registry cache uses cache version 1. Pre-release/build
// suffixes and custom registries use different keys and are rejected below.
func BunCachePackageDirectory(target, name, version string) string {
	return path.Join(BunCacheDirectory(target), name+"@"+version+"@@@1")
}

func ValidBunPackage(name, version string) bool {
	parsed, actual, ok := pnpmPackageID(name + "@" + version)
	return ok && parsed == name && actual == version && name == strings.ToLower(name) && len(name) <= 214 && len(version) <= 64 && bunStableVersion.MatchString(version)
}

func bunSelected(reader fileReader, files map[string]sourceFile) bool {
	if _, err := reader.root.Lstat(path.Join(reader.target, "bun.lockb")); err == nil {
		return true
	}
	if _, found := files["bun.lock"]; found {
		return true
	}
	var manifest npmManifest
	_ = json.Unmarshal(files["package.json"].raw, &manifest)
	return strings.HasPrefix(manifest.PackageManager, "bun@")
}

func detectBun(plan Plan, reader fileReader, files map[string]sourceFile) Plan {
	plan.Ecosystem, plan.Toolchain, plan.RequestedVersion, plan.Manifest = "bun", "bun", BunVersion, files["package.json"].File
	issue := func(text string) {
		if len(plan.Issues) < 63 {
			plan.Issues = append(plan.Issues, text)
		} else if len(plan.Issues) == 63 {
			plan.Issues = append(plan.Issues, "Further Bun preparation issues omitted; select one supported project and lockfile")
		}
	}
	bound := map[string]bool{plan.Manifest.Path: true}
	bind := func(file File) {
		if bound[file.Path] {
			return
		}
		bound[file.Path] = true
		if len(plan.Lockfiles) == 63 {
			issue("Bun preparation supports at most 64 bound files")
			return
		}
		plan.Lockfiles = append(plan.Lockfiles, file)
	}
	var manifest npmManifest
	var declaration map[string]json.RawMessage
	if json.Unmarshal(files["package.json"].raw, &manifest) != nil || json.Unmarshal(files["package.json"].raw, &declaration) != nil || declaration == nil {
		issue("package.json must be a valid Bun project manifest")
		return plan
	}
	if manifest.PackageManager != "bun@"+BunVersion {
		issue("Bun preparation requires packageManager: bun@" + BunVersion)
	}
	for _, key := range []string{"workspaces", "overrides", "resolutions", "patchedDependencies", "catalog", "catalogs"} {
		if raw := strings.TrimSpace(string(declaration[key])); raw != "" && raw != "null" {
			issue("Bun preparation does not support package.json " + key + "; select a standalone registry-only project")
		}
	}
	for _, name := range []string{"package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb"} {
		if file, found := files[name]; found {
			bind(file.File)
			issue(name + " conflicts with Bun text-lock preparation; keep one reviewed bun.lock")
		}
	}
	// Bun can read configuration from ancestors. No repository config may
	// replace the fixed registry, cache or script policy in this first format.
	for directory := plan.Target; ; directory = path.Dir(directory) {
		at := reader
		at.target = directory
		for _, name := range []string{"bunfig.toml", ".bunfig.toml", ".npmrc", "bun.lockb"} {
			file, found, err := at.read(name)
			if err != nil || found {
				issue(path.Join(directory, name) + " is unsupported; use reviewed bun.lock with the fixed default registry and Bun " + BunVersion)
			}
			if found {
				bind(file.File)
			}
		}
		if directory != plan.Target {
			if file, found, err := at.read("package.json"); err != nil {
				issue("Ancestor package.json cannot be inspected")
			} else if found {
				bind(file.File)
				var parent map[string]json.RawMessage
				if json.Unmarshal(file.raw, &parent) != nil || parent["workspaces"] != nil {
					issue("Bun workspace members are unsupported; select a standalone project root")
				}
			}
		}
		if directory == "." {
			break
		}
	}
	locked, found := files["bun.lock"]
	if !found {
		issue("bun.lock is missing; binary bun.lockb is unsupported, create and review a Bun text lockfile")
		return plan
	}
	bind(locked.File)
	var lock struct {
		LockfileVersion int                                   `json:"lockfileVersion"`
		ConfigVersion   int                                   `json:"configVersion"`
		Workspaces      map[string]map[string]json.RawMessage `json:"workspaces"`
		Packages        map[string][]json.RawMessage          `json:"packages"`
	}
	data := jsonc.ToJSON(locked.raw)
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &lock) != nil || json.Unmarshal(data, &fields) != nil || lock.LockfileVersion != 2 || lock.ConfigVersion != 1 || len(lock.Workspaces) != 1 || lock.Workspaces[""] == nil || lock.Packages == nil || len(lock.Packages) > MaxPackages {
		issue("bun.lock must be a bounded text lockfileVersion 2/configVersion 1 with one root workspace")
		return plan
	}
	for key := range fields {
		if key != "lockfileVersion" && key != "configVersion" && key != "workspaces" && key != "packages" {
			issue("bun.lock contains unsupported configuration: " + key)
		}
	}
	for _, key := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"} {
		var declared, pinned map[string]string
		if json.Unmarshal(declaration[key], &declared) != nil && declaration[key] != nil || json.Unmarshal(lock.Workspaces[""][key], &pinned) != nil && lock.Workspaces[""][key] != nil || !sameDependencies(declared, pinned) {
			issue("bun.lock root " + key + " differs from package.json; regenerate and review the lockfile")
		}
		for name, requirement := range declared {
			if strings.ContainsAny(requirement, ":/\\") {
				issue("Bun root dependency uses an unsupported non-registry source: " + name)
			}
			if _, exists := lock.Packages[name]; !exists {
				issue("Bun root dependency has no pinned registry package: " + name)
			}
		}
	}
	seen := map[string]bool{}
	for locator, values := range lock.Packages {
		var id, registry, integrity string
		var metadata map[string]json.RawMessage
		if len(values) != 4 || json.Unmarshal(values[0], &id) != nil || json.Unmarshal(values[1], &registry) != nil || registry != "" || json.Unmarshal(values[2], &metadata) != nil || metadata == nil || json.Unmarshal(values[3], &integrity) != nil || !validSRI(integrity) {
			issue("Bun locked package " + locator + " requires a default-registry tuple with SHA-256/SHA-512 integrity")
			continue
		}
		name, version, ok := pnpmPackageID(id)
		if !ok || !ValidBunPackage(name, version) || len(locator) > 1024 || path.Clean(locator) != locator || path.IsAbs(locator) || strings.HasPrefix(locator, "../") || strings.ContainsAny(locator, "\\:\x00\r\n") || (locator != name && !strings.HasSuffix(locator, "/"+name)) || seen[id] {
			issue("Bun locked package " + locator + " has an unsupported identity, alias or pre-release/build version")
			continue
		}
		seen[id] = true
		for _, key := range []string{"dependencies", "optionalDependencies", "peerDependencies"} {
			var dependencies map[string]string
			if raw, exists := metadata[key]; exists {
				if json.Unmarshal(raw, &dependencies) != nil {
					issue("Bun package dependency metadata is invalid: " + id)
				}
				for _, requirement := range dependencies {
					if strings.ContainsAny(requirement, ":/\\") {
						issue("Bun package uses an unsupported non-registry dependency: " + id)
					}
				}
			}
		}
		plan.Packages = append(plan.Packages, Package{ID: locator, Kind: KindBunTarball, ModulePath: name, Version: version, Integrity: integrity, URL: BunRegistryOrigin + "/" + name + "/-/" + path.Base(name) + "-" + version + ".tgz"})
	}
	if len(plan.Packages) > 0 {
		plan.Sources = append(plan.Sources, BunRegistryOrigin)
	}
	plan.Install = Command{ID: "install.bun", Display: "bun install --offline --frozen-lockfile --ignore-scripts --force", Arguments: []string{"install", "--offline", "--frozen-lockfile", "--ignore-scripts", "--force"}}
	if strings.TrimSpace(manifest.Scripts["test"]) != "" {
		plan.Test = Command{ID: "test.bun", Display: "bun run --no-install test", Arguments: []string{"run", "--no-install", "test"}}
	}
	return plan
}

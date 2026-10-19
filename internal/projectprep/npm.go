package projectprep

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strings"
)

type npmManifest struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	PackageManager       string            `json:"packageManager"`
	Engines              map[string]string `json:"engines"`
	Scripts              map[string]string `json:"scripts"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	Workspaces           json.RawMessage   `json:"workspaces"`
}

type npmLock struct {
	LockfileVersion int                         `json:"lockfileVersion"`
	Packages        map[string]npmLockedPackage `json:"packages"`
}

type npmLockedPackage struct {
	Resolved             string            `json:"resolved"`
	Integrity            string            `json:"integrity"`
	Link                 bool              `json:"link"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

func detectNPM(plan Plan, files map[string]sourceFile) Plan {
	plan.Ecosystem, plan.Toolchain, plan.Manifest = "npm", "node/npm", files["package.json"].File
	plan.Install = Command{ID: "install.npm", Display: "npm ci --offline --ignore-scripts --bin-links=false --no-audit --no-fund", Arguments: []string{"ci", "--offline", "--ignore-scripts", "--bin-links=false", "--no-audit", "--no-fund"}}
	var manifest npmManifest
	if err := json.Unmarshal(files["package.json"].raw, &manifest); err != nil || !strings.HasPrefix(strings.TrimSpace(string(files["package.json"].raw)), "{") {
		plan.Issues = append(plan.Issues, "package.json is not a valid package manifest")
		return plan
	}
	if manager, _, _ := strings.Cut(manifest.PackageManager, "@"); manifest.PackageManager != "" && manager != "npm" {
		plan.Issues = append(plan.Issues, "packageManager selects an unsupported manager; this preparation action supports npm package-lock.json only")
	}
	plan.RequestedVersion = manifest.Engines["node"]
	if len(plan.RequestedVersion) > 256 || strings.ContainsAny(plan.RequestedVersion, "\r\n\x00") {
		plan.RequestedVersion = ""
		plan.Issues = append(plan.Issues, "engines.node must be a bounded version requirement")
	}
	if strings.TrimSpace(manifest.Scripts["test"]) != "" {
		plan.Test = Command{ID: "test.npm", Display: "npm test --offline --no-audit --no-fund", Arguments: []string{"test", "--offline", "--no-audit", "--no-fund"}}
	}
	if workspace := strings.TrimSpace(string(manifest.Workspaces)); workspace != "" && workspace != "null" && workspace != "[]" {
		plan.Issues = append(plan.Issues, "npm workspaces require a reviewed workspace plan; select a standalone package directory")
	}
	for _, name := range []string{"npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb"} {
		if file, found := files[name]; found {
			plan.Lockfiles = append(plan.Lockfiles, file.File)
			plan.Issues = append(plan.Issues, name+" is unsupported or conflicts with npm package-lock.json; select one supported package manager")
		}
	}
	for _, name := range []string{".npmrc", ".nvmrc", ".node-version", ".tool-versions"} {
		if file, found := files[name]; found {
			plan.Lockfiles = append(plan.Lockfiles, file.File)
		}
	}
	locked, found := files["package-lock.json"]
	if !found {
		plan.Issues = append(plan.Issues, "package-lock.json is missing; create and review an npm lockfile before preparation")
		return plan
	}
	plan.Lockfiles = append(plan.Lockfiles, locked.File)
	var lock npmLock
	if err := json.Unmarshal(locked.raw, &lock); err != nil || (lock.LockfileVersion != 2 && lock.LockfileVersion != 3) || lock.Packages == nil {
		plan.Issues = append(plan.Issues, "package-lock.json must use supported lockfileVersion 2 or 3 with a packages map")
		return plan
	}
	if len(lock.Packages) > MaxPackages {
		plan.Issues = append(plan.Issues, "package-lock.json exceeds the preparation package limit")
		return plan
	}
	root, found := lock.Packages[""]
	if !found || !sameDependencies(root.Dependencies, manifest.Dependencies) || !sameDependencies(root.DevDependencies, manifest.DevDependencies) || !sameDependencies(root.OptionalDependencies, manifest.OptionalDependencies) {
		plan.Issues = append(plan.Issues, "package-lock.json root dependencies differ from package.json; regenerate and review the lockfile")
	}
	names := make([]string, 0, len(lock.Packages))
	for name := range lock.Packages {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	origins := map[string]bool{}
	for _, name := range names {
		pkg := lock.Packages[name]
		if !safeNPMPath(name) || pkg.Link {
			plan.Issues = append(plan.Issues, "Locked package "+name+" uses an unsupported local or workspace link")
			continue
		}
		origin, valid := publicOrigin(pkg.Resolved)
		if !valid || !validSRI(pkg.Integrity) {
			plan.Issues = append(plan.Issues, "Locked package "+name+" needs a public HTTPS tarball URL and SHA-256 or SHA-512 integrity")
			continue
		}
		origins[origin] = true
		plan.Packages = append(plan.Packages, Package{ID: name, URL: pkg.Resolved, Integrity: pkg.Integrity, Kind: KindNPMTarball})
	}
	for origin := range origins {
		plan.Sources = append(plan.Sources, origin)
	}
	return plan
}

func sameDependencies(a, b map[string]string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func safeNPMPath(value string) bool {
	if len(value) > 1024 || path.Clean(value) != value || strings.ContainsAny(value, "\\:\r\n\x00") || !strings.HasPrefix(value, "node_modules/") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." || segment == "." || segment == "" {
			return false
		}
	}
	return true
}

func validSRI(value string) bool {
	algorithm, encoded, found := strings.Cut(value, "-")
	if !found {
		return false
	}
	expected := 0
	switch algorithm {
	case "sha256":
		expected = 32
	case "sha512":
		expected = 64
	default:
		return false
	}
	digest, err := base64.StdEncoding.DecodeString(encoded)
	return err == nil && len(digest) == expected && base64.StdEncoding.EncodeToString(digest) == encoded
}

func publicOrigin(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Port() != "" && parsed.Port() != "443") {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") || strings.HasSuffix(host, ".") || strings.ContainsAny(host, "\r\n\\:") {
		return "", false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".test", ".invalid"} {
		if strings.HasSuffix(host, suffix) {
			return "", false
		}
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return "", false
		}
	}
	return "https://" + host, true
}

// Package projectprep detects dependency preparation without executing project
// content. A Plan is evidence for operator review, never an execution grant.
package projectprep

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
)

const (
	ProtocolVersion      = "project-preparation.v1"
	MaxFileBytes         = 8 * 1024 * 1024
	MaxPackages          = 4096
	GoProxyOrigin        = "https://proxy.golang.org"
	GoProxyArchiveOrigin = "https://storage.googleapis.com"
	KindNPMTarball       = "npm_tarball"
	KindPNPMTarball      = "pnpm_tarball"
	KindYarnTarball      = "yarn_tarball"
	YarnVersion          = "1.22.22"
	PNPMVersion          = "10.28.1"
	KindGoMod            = "go_mod"
	KindGoZip            = "go_zip"
)

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Package describes one exact artifact. Cache paths are derived by the broker,
// and repository metadata never chooses a host path or supplies credentials.
type Package struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	Integrity  string `json:"integrity"`
	Kind       string `json:"kind"`
	ModulePath string `json:"module_path,omitempty"`
	Version    string `json:"version,omitempty"`
}

type Command struct {
	ID        string   `json:"id"`
	Display   string   `json:"display"`
	Arguments []string `json:"arguments"`
}

type Plan struct {
	Version          string    `json:"version"`
	Fingerprint      string    `json:"fingerprint"`
	Target           string    `json:"target"`
	WorkspaceRoot    string    `json:"workspace_root,omitempty"`
	Ecosystem        string    `json:"ecosystem"`
	Toolchain        string    `json:"toolchain"`
	RequestedVersion string    `json:"requested_version,omitempty"`
	Manifest         File      `json:"manifest"`
	Lockfiles        []File    `json:"lockfiles"`
	Sources          []string  `json:"sources"`
	Packages         []Package `json:"-"`
	PNPMSeedLock     []byte    `json:"-"`
	Install          Command   `json:"install"`
	Test             Command   `json:"test"`
	Ready            bool      `json:"ready"`
	Issues           []string  `json:"issues"`
	CacheKey         string    `json:"cache_key"`
}

// StageDirectory is relative to the Run workspace and stable for one target.
// Cache content remains individually integrity checked across changed plans.
func StageDirectory(target string) string {
	sum := sha256.Sum256([]byte(target))
	return path.Join(".traverse-preparation", hex.EncodeToString(sum[:]))
}

func NPMCacheDirectory(target string) string      { return path.Join(StageDirectory(target), "npm-cache") }
func GoModuleCacheDirectory(target string) string { return path.Join(StageDirectory(target), "go-mod") }
func PNPMSeedDirectory(target string) string      { return path.Join(StageDirectory(target), "pnpm-seed") }
func PNPMStoreDirectory(target string) string     { return path.Join(StageDirectory(target), "pnpm-store") }
func YarnMirrorDirectory(target string) string {
	return path.Join(StageDirectory(target), "yarn-mirror")
}
func YarnRCPath(target string) string { return path.Join(StageDirectory(target), "yarnrc") }
func PNPMTarballFile(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:]) + ".tgz"
}

func (p *Plan) seal() {
	p.Ready = len(p.Issues) == 0 && p.Install.ID != ""
	cache, _ := json.Marshal(struct {
		Ecosystem        string
		RequestedVersion string
		Packages         []Package
	}{p.Ecosystem, p.RequestedVersion, p.Packages})
	cacheSum := sha256.Sum256(cache)
	p.CacheKey = hex.EncodeToString(cacheSum[:])
	// Packages are private in the HTTP projection, but are part of the exact
	// review binding so altered URLs and checksums cannot reuse consent.
	encoded, _ := json.Marshal(struct {
		Plan         Plan
		Packages     []Package
		PNPMSeedLock []byte
	}{*p, p.Packages, p.PNPMSeedLock})
	sum := sha256.Sum256(encoded)
	p.Fingerprint = hex.EncodeToString(sum[:])
}

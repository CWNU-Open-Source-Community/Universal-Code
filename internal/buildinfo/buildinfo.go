package buildinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

const DiagnosticProtocolVersion = "portable_build_diagnostic.v1"

// ProductName is the user-facing application identity. Stable protocol,
// module, credential, and data-directory identifiers intentionally retain
// their CyberAgent names for backward compatibility.
const ProductName = "Universal-Code"

// These values are intentionally reproducible inputs. Release builds set them
// with -ldflags; no wall-clock build timestamp is embedded.
var (
	Version         = "v1.0.0"
	Revision        = "unknown"
	SourceDateEpoch = "0"
	Modified        = "unknown"
	CGOEnabled      = "unknown"
)

type ReleaseMetadata struct {
	AppVersion            string `json:"app_version"`
	Revision              string `json:"revision"`
	SourceDateEpoch       int64  `json:"source_date_epoch"`
	SourceDate            string `json:"source_date"`
	Modified              bool   `json:"modified"`
	ModifiedKnown         bool   `json:"modified_known"`
	GoVersion             string `json:"go_version"`
	TargetOS              string `json:"target_os"`
	TargetArch            string `json:"target_arch"`
	CGOEnabled            string `json:"cgo_enabled"`
	Trimpath              bool   `json:"trimpath"`
	ModulePath            string `json:"module_path"`
	BuildFingerprint      string `json:"build_fingerprint"`
	InstallerIncluded     bool   `json:"installer_included"`
	RegistryWrites        bool   `json:"registry_writes"`
	AutoUpdateEnabled     bool   `json:"auto_update_enabled"`
	ManualWindows10Matrix bool   `json:"manual_windows_10_matrix_required"`
}

type Check struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type Diagnostic struct {
	ProtocolVersion string          `json:"protocol_version"`
	Release         ReleaseMetadata `json:"release"`
	Checks          []Check         `json:"checks"`
	ReleaseReady    bool            `json:"release_ready"`
}

func PortableDiagnostic() Diagnostic {
	release := currentReleaseMetadata()
	checks := []Check{
		check("windows_target", release.TargetOS == "windows" &&
			(release.TargetArch == "amd64" || release.TargetArch == "arm64"),
			"Windows amd64 or arm64 target", "current binary is not a supported Windows target"),
		check("revision_pinned", validRevision(release.Revision),
			"source revision is pinned", "source revision is not pinned"),
		check("source_date_epoch_pinned", release.SourceDateEpoch > 0,
			"source date epoch is pinned", "source date epoch is not pinned"),
		check("trimpath_enabled", release.Trimpath,
			"Go trimpath metadata is present", "Go trimpath metadata is absent"),
		check("cgo_recorded", release.CGOEnabled == "0" || release.CGOEnabled == "1",
			"CGO mode is recorded", "CGO mode is not recorded"),
		{ID: "portable_boundary", Status: "pass",
			Detail: "no installer, registry write, startup task, or auto-update authority is included"},
		{ID: "windows_10_runtime_matrix", Status: "manual",
			Detail: "Windows 10, WebView2, display scaling, and launch behavior require a signed manual matrix"},
	}
	ready := true
	for _, current := range checks {
		if current.Status != "pass" {
			ready = false
		}
	}
	return Diagnostic{ProtocolVersion: DiagnosticProtocolVersion, Release: release,
		Checks: checks, ReleaseReady: ready}
}

func currentReleaseMetadata() ReleaseMetadata {
	revision := strings.TrimSpace(Revision)
	epoch, _ := strconv.ParseInt(strings.TrimSpace(SourceDateEpoch), 10, 64)
	modified, modifiedKnown := parseBool(Modified)
	cgo := strings.TrimSpace(CGOEnabled)
	trimpath := false
	modulePath := "cyberagent-workbench"
	goVersion := runtime.Version()
	if info, ok := debug.ReadBuildInfo(); ok {
		if strings.TrimSpace(info.GoVersion) != "" {
			goVersion = info.GoVersion
		}
		if strings.TrimSpace(info.Main.Path) != "" {
			modulePath = info.Main.Path
		}
		settings := make(map[string]string, len(info.Settings))
		for _, setting := range info.Settings {
			settings[setting.Key] = setting.Value
		}
		trimpath = settings["-trimpath"] == "true"
		if !validRevision(revision) && validRevision(settings["vcs.revision"]) {
			revision = settings["vcs.revision"]
		}
		if epoch <= 0 {
			if value, err := time.Parse(time.RFC3339, settings["vcs.time"]); err == nil {
				epoch = value.Unix()
			}
		}
		if !modifiedKnown {
			modified, modifiedKnown = parseBool(settings["vcs.modified"])
		}
		if cgo != "0" && cgo != "1" {
			cgo = settings["CGO_ENABLED"]
		}
	}
	if !validRevision(revision) {
		revision = "unknown"
	}
	if epoch < 0 {
		epoch = 0
	}
	sourceDate := ""
	if epoch > 0 {
		sourceDate = time.Unix(epoch, 0).UTC().Format(time.RFC3339)
	}
	release := ReleaseMetadata{
		AppVersion: strings.TrimSpace(Version), Revision: revision,
		SourceDateEpoch: epoch, SourceDate: sourceDate,
		Modified: modified, ModifiedKnown: modifiedKnown,
		GoVersion: goVersion, TargetOS: runtime.GOOS, TargetArch: runtime.GOARCH,
		CGOEnabled: cgo, Trimpath: trimpath, ModulePath: modulePath,
		InstallerIncluded: false, RegistryWrites: false, AutoUpdateEnabled: false,
		ManualWindows10Matrix: true,
	}
	release.BuildFingerprint = fingerprint(release)
	return release
}

func check(id string, passed bool, success string, warning string) Check {
	if passed {
		return Check{ID: id, Status: "pass", Detail: success}
	}
	return Check{ID: id, Status: "warn", Detail: warning}
}

func parseBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func validRevision(value string) bool {
	if len(value) != 40 || value != strings.ToLower(value) {
		return false
	}
	for _, current := range value {
		if (current < '0' || current > '9') && (current < 'a' || current > 'f') {
			return false
		}
	}
	return true
}

func fingerprint(value ReleaseMetadata) string {
	canonical := strings.Join([]string{value.AppVersion, value.Revision,
		strconv.FormatInt(value.SourceDateEpoch, 10), strconv.FormatBool(value.Modified),
		strconv.FormatBool(value.ModifiedKnown), value.GoVersion, value.TargetOS,
		value.TargetArch, value.CGOEnabled, strconv.FormatBool(value.Trimpath),
		value.ModulePath, "installer=false", "registry=false", "update=false",
		"manual_windows_10=true"}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

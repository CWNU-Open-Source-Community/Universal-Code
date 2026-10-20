package projectprep

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type pythonLock struct {
	Version          string          `toml:"lock-version"`
	CreatedBy        string          `toml:"created-by"`
	RequiresPython   string          `toml:"requires-python"`
	Environments     []string        `toml:"environments"`
	Extras           []string        `toml:"extras"`
	DependencyGroups []string        `toml:"dependency-groups"`
	DefaultGroups    []string        `toml:"default-groups"`
	Packages         []pythonPackage `toml:"packages"`
}

type pythonPackage struct {
	Name           string         `toml:"name" json:"name"`
	Version        string         `toml:"version" json:"version"`
	Marker         string         `toml:"marker" json:"marker"`
	RequiresPython string         `toml:"requires-python" json:"requires_python"`
	Wheels         []pythonWheel  `toml:"wheels" json:"wheels"`
	VCS            map[string]any `toml:"vcs" json:"-"`
	Directory      map[string]any `toml:"directory" json:"-"`
	Archive        map[string]any `toml:"archive" json:"-"`
}

type pythonWheel struct {
	Name   string            `toml:"name" json:"name"`
	URL    string            `toml:"url" json:"-"`
	Path   string            `toml:"path" json:"-"`
	Size   *int64            `toml:"size" json:"size,omitempty"`
	Hashes map[string]string `toml:"hashes" json:"hashes"`
}

type pythonSeed struct {
	Files          []File          `json:"files"`
	RequiresPython []string        `json:"requires_python"`
	Environments   []string        `json:"environments"`
	DefaultGroups  []string        `json:"default_groups"`
	Packages       []pythonPackage `json:"packages"`
}

var pythonName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var pythonWheelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+!-]*\.whl$`)

func ValidPythonWheelName(value string) bool {
	return len(value) <= 255 && pythonWheelName.MatchString(value)
}

// Detect only reads files. The offline installer evaluates conditions using
// the actual selected interpreter and its packaging compatibility rules.
func detectPython(plan Plan, files map[string]sourceFile) Plan {
	manifest, hasManifest := files["pyproject.toml"]
	locked, hasLock := files["pylock.toml"]
	plan.Ecosystem, plan.Toolchain = "python", "Python / pip >=25.3"
	if hasManifest {
		plan.Manifest = manifest.File
	} else if hasLock {
		plan.Manifest = locked.File
	}
	plan.Install = Command{ID: "install.python", Display: "python -I -m pip --isolated install --no-index --no-deps --require-hashes --only-binary=:all: --target <task-private-site>"}
	plan.Test = Command{ID: "test.python", Display: "python -m unittest discover -v", Arguments: []string{"-m", "unittest", "discover", "-v"}}
	if !hasLock {
		plan.Issues = append(plan.Issues, "Python preparation requires pylock.toml 1.0 with exact HTTPS wheel URLs and SHA-256 hashes")
		return plan
	}
	if hasManifest {
		plan.Lockfiles = append(plan.Lockfiles, locked.File)
	}
	var lock pythonLock
	if err := toml.Unmarshal(locked.raw, &lock); err != nil || lock.Version != "1.0" || strings.TrimSpace(lock.CreatedBy) == "" || lock.Packages == nil {
		plan.Issues = append(plan.Issues, "pylock.toml must be a valid version 1.0 lock with created-by and packages")
		return plan
	}
	if len(lock.Packages) > MaxPackages {
		plan.Issues = append(plan.Issues, "Python lock exceeds the preparation package limit")
		return plan
	}
	if len(lock.Extras) != 0 || len(lock.DependencyGroups) != 0 {
		plan.Issues = append(plan.Issues, "Python preparation currently supports default-only locks without selectable extras or dependency groups")
	}
	seed := pythonSeed{Files: append([]File{plan.Manifest}, plan.Lockfiles...), RequiresPython: []string{lock.RequiresPython}, Environments: lock.Environments, DefaultGroups: lock.DefaultGroups, Packages: lock.Packages}
	if hasManifest {
		var project struct {
			Project struct {
				RequiresPython string `toml:"requires-python"`
			} `toml:"project"`
		}
		if err := toml.Unmarshal(manifest.raw, &project); err != nil {
			plan.Issues = append(plan.Issues, "pyproject.toml is invalid TOML")
		} else {
			seed.RequiresPython = append(seed.RequiresPython, project.Project.RequiresPython)
		}
	}
	plan.RequestedVersion = lock.RequiresPython
	origins, artifacts := map[string]bool{}, map[string]Package{}
	for index := range seed.Packages {
		pkg := &seed.Packages[index]
		if !pythonName.MatchString(pkg.Name) || pkg.Version == "" || len(pkg.Version) > 200 || strings.ContainsAny(pkg.Version, "\r\n\x00") {
			plan.Issues = append(plan.Issues, "Python package requires a normalized name and an exact version")
		}
		if len(pkg.Wheels) == 0 || pkg.VCS != nil || pkg.Directory != nil || pkg.Archive != nil {
			plan.Issues = append(plan.Issues, "Python preparation supports wheel artifacts; source builds, VCS, directories and editable installs are unavailable")
		}
		for wheelIndex := range pkg.Wheels {
			wheel := &pkg.Wheels[wheelIndex]
			if wheel.Size != nil && (*wheel.Size < 1 || *wheel.Size > 64*1024*1024) {
				plan.Issues = append(plan.Issues, "Python wheel size must fit the preparation artifact limit")
			}
			origin, valid := publicOrigin(wheel.URL)
			parsed, err := url.Parse(wheel.URL)
			if !valid || err != nil || wheel.Path != "" {
				plan.Issues = append(plan.Issues, "Python wheels require exact public HTTPS URLs without local paths")
				continue
			}
			if wheel.Name == "" {
				wheel.Name = path.Base(parsed.Path)
			}
			digest, err := hex.DecodeString(wheel.Hashes["sha256"])
			if !pythonWheelName.MatchString(wheel.Name) || len(wheel.Name) > 255 || err != nil || len(digest) != 32 || hex.EncodeToString(digest) != wheel.Hashes["sha256"] {
				plan.Issues = append(plan.Issues, "Python wheels require a safe wheel filename and lowercase SHA-256 hash")
				continue
			}
			artifact := Package{ID: wheel.Name, URL: wheel.URL, Kind: KindPythonWheel, ModulePath: pkg.Name, Version: pkg.Version, Integrity: "sha256-" + base64.StdEncoding.EncodeToString(digest)}
			if previous, exists := artifacts[wheel.Name]; exists && previous != artifact {
				plan.Issues = append(plan.Issues, "Python lock has conflicting artifacts for wheel "+wheel.Name)
			}
			artifacts[wheel.Name] = artifact
			origins[origin] = true
		}
	}
	if len(artifacts) > MaxPackages {
		plan.Issues = append(plan.Issues, "Python lock exceeds the preparation wheel limit")
		return plan
	}
	for _, artifact := range artifacts {
		plan.Packages = append(plan.Packages, artifact)
	}
	for origin := range origins {
		plan.Sources = append(plan.Sources, origin)
	}
	encoded, err := json.Marshal(seed)
	if err != nil || len(encoded) > MaxFileBytes {
		plan.Issues = append(plan.Issues, "Python installation plan exceeds the byte limit")
	} else {
		plan.PythonSeed = encoded
	}
	return plan
}

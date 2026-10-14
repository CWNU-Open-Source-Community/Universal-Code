package projectprep

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

func detectGo(plan Plan, files map[string]sourceFile) Plan {
	plan.Ecosystem, plan.Toolchain, plan.Manifest = "go", "go", files["go.mod"].File
	plan.Install = Command{ID: "install.go", Display: "go mod download", Arguments: []string{"mod", "download"}}
	plan.Test = Command{ID: "test.go", Display: "go test -mod=readonly ./...", Arguments: []string{"test", "-mod=readonly", "./..."}}
	manifest, err := modfile.Parse("go.mod", files["go.mod"].raw, nil)
	if err != nil || manifest.Module == nil {
		plan.Issues = append(plan.Issues, "go.mod is invalid or uses unsupported module directives")
		return plan
	}
	if manifest.Go != nil {
		plan.RequestedVersion = manifest.Go.Version
	}
	if manifest.Toolchain != nil && manifest.Toolchain.Name != "default" {
		plan.RequestedVersion = manifest.Toolchain.Name
	}
	if file, found := files["go.work"]; found {
		plan.Lockfiles = append(plan.Lockfiles, file.File)
		plan.Issues = append(plan.Issues, "go.work requires a reviewed workspace plan; select a standalone module")
	}
	for _, replacement := range manifest.Replace {
		if replacement.New.Version == "" {
			plan.Issues = append(plan.Issues, "go.mod contains a local replace directive; prepare its local module inputs explicitly")
		}
	}
	sumFile, found := files["go.sum"]
	if !found {
		if len(manifest.Require) > 0 {
			plan.Issues = append(plan.Issues, "go.sum is missing; generate and review module checksums before preparation")
		}
		return plan
	}
	plan.Lockfiles = append(plan.Lockfiles, sumFile.File)
	checksums := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(sumFile.raw)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 || !validH1(fields[2]) {
			plan.Issues = append(plan.Issues, "go.sum contains an invalid checksum entry")
			return plan
		}
		modulePath, version := fields[0], fields[1]
		kind, extension := KindGoZip, ".zip"
		if strings.HasSuffix(version, "/go.mod") {
			kind, extension, version = KindGoMod, ".mod", strings.TrimSuffix(version, "/go.mod")
		}
		if err := module.Check(modulePath, version); err != nil {
			plan.Issues = append(plan.Issues, "go.sum contains an invalid module path or version")
			return plan
		}
		escapedPath, pathErr := module.EscapePath(modulePath)
		escapedVersion, versionErr := module.EscapeVersion(version)
		if pathErr != nil || versionErr != nil {
			plan.Issues = append(plan.Issues, "go.sum module cannot be represented safely in the proxy cache")
			return plan
		}
		id := modulePath + "@" + version + extension
		if previous, exists := checksums[id]; exists {
			if previous != fields[2] {
				plan.Issues = append(plan.Issues, "go.sum contains conflicting checksums for "+modulePath+"@"+version)
				return plan
			}
			continue
		}
		checksums[id] = fields[2]
		plan.Packages = append(plan.Packages, Package{ID: id, URL: GoProxyOrigin + "/" + escapedPath + "/@v/" + escapedVersion + extension, Integrity: fields[2], Kind: kind, ModulePath: modulePath, Version: version})
		if len(plan.Packages) > MaxPackages {
			plan.Issues = append(plan.Issues, "go.sum exceeds the preparation package limit")
			return plan
		}
	}
	if scanner.Err() != nil {
		plan.Issues = append(plan.Issues, "go.sum contains an oversized checksum line")
		return plan
	}
	for _, requirement := range manifest.Require {
		needed := requirement.Mod
		for _, replacement := range manifest.Replace {
			if replacement.Old.Path == needed.Path && (replacement.Old.Version == "" || replacement.Old.Version == needed.Version) {
				needed = replacement.New
				break
			}
		}
		if needed.Version == "" {
			continue
		}
		if checksums[needed.Path+"@"+needed.Version+".mod"] == "" || checksums[needed.Path+"@"+needed.Version+".zip"] == "" {
			plan.Issues = append(plan.Issues, fmt.Sprintf("go.sum needs module and archive checksums for %s@%s; regenerate and review go.sum", needed.Path, needed.Version))
		}
	}
	if len(plan.Packages) > 0 {
		plan.Sources = append(plan.Sources, GoProxyOrigin)
		// The public Go proxy serves larger archives through its Google Storage
		// origin. List it for independent review; the broker still authorizes
		// each redirect and never expands this to other CDN hosts.
		for _, pkg := range plan.Packages {
			if pkg.Kind == KindGoZip {
				plan.Sources = append(plan.Sources, GoProxyArchiveOrigin)
				break
			}
		}
	}
	return plan
}

func validH1(value string) bool {
	encoded, found := strings.CutPrefix(value, "h1:")
	if !found {
		return false
	}
	digest, err := base64.StdEncoding.DecodeString(encoded)
	return err == nil && len(digest) == 32 && base64.StdEncoding.EncodeToString(digest) == encoded
}

package projectprep

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

type yarnLockedPackage struct {
	version, resolved, integrity string
	dependencies                 map[string]string
}

func selectsYarn(files map[string]sourceFile) bool {
	if _, ok := files["yarn.lock"]; ok {
		return true
	}
	var manifest npmManifest
	_ = json.Unmarshal(files["package.json"].raw, &manifest)
	return strings.HasPrefix(manifest.PackageManager, "yarn@")
}

func detectYarn(plan Plan, files map[string]sourceFile) Plan {
	plan.Ecosystem, plan.Toolchain, plan.Manifest = "yarn", "yarn", files["package.json"].File
	plan.RequestedVersion = YarnVersion
	plan.Install = Command{ID: "install.yarn", Display: "yarn install --offline --frozen-lockfile --ignore-scripts", Arguments: []string{"install", "--offline", "--frozen-lockfile", "--ignore-scripts"}}
	var manifest npmManifest
	if err := json.Unmarshal(files["package.json"].raw, &manifest); err != nil {
		plan.Issues = append(plan.Issues, "package.json is not a valid package manifest")
		return plan
	}
	if manifest.PackageManager != "yarn@"+YarnVersion {
		plan.Issues = append(plan.Issues, "Yarn preparation requires packageManager yarn@"+YarnVersion+"; modern Yarn uses a different cache format")
	}
	if value := strings.TrimSpace(string(manifest.Workspaces)); value != "" && value != "null" && value != "[]" {
		plan.Issues = append(plan.Issues, "Yarn preparation currently requires a standalone package without workspaces")
	}
	if strings.TrimSpace(manifest.Scripts["test"]) != "" {
		plan.Test = Command{ID: "test.yarn", Display: "yarn --offline run test", Arguments: []string{"run", "test"}}
	}
	for _, name := range []string{"package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "bun.lock", "bun.lockb"} {
		if file, ok := files[name]; ok {
			plan.Lockfiles = append(plan.Lockfiles, file.File)
			plan.Issues = append(plan.Issues, name+" conflicts with yarn.lock; select one package manager")
		}
	}
	lock, ok := files["yarn.lock"]
	if !ok {
		plan.Issues = append(plan.Issues, "yarn.lock is missing; create and review the lockfile before preparation")
		return plan
	}
	plan.Lockfiles = append(plan.Lockfiles, lock.File)
	packages, err := readYarnClassicLock(lock.raw)
	if err != nil {
		plan.Issues = append(plan.Issues, err.Error())
		return plan
	}
	checkDependencies := func(dependencies map[string]string) {
		for _, name := range sortedPNPMKeys(dependencies) {
			selector := name + "@" + dependencies[name]
			if !validYarnSelector(selector) || packages[selector] == nil {
				plan.Issues = append(plan.Issues, "yarn.lock must contain a registry entry for "+selector)
			}
		}
	}
	checkDependencies(manifest.Dependencies)
	checkDependencies(manifest.DevDependencies)
	checkDependencies(manifest.OptionalDependencies)
	origins, seen := map[string]bool{}, map[*yarnLockedPackage]bool{}
	mirrors := map[string]string{}
	for _, selector := range sortedPNPMKeys(packages) {
		pkg := packages[selector]
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		checkDependencies(pkg.dependencies)
		parsed, parseErr := url.Parse(pkg.resolved)
		if parseErr != nil {
			plan.Issues = append(plan.Issues, "invalid Yarn tarball URL for "+selector)
			continue
		}
		// Yarn Classic also stores an optional legacy SHA-1 fragment. The
		// broker requires the stronger, separately locked SRI for every archive.
		parsed.Fragment = ""
		origin, public := publicOrigin(parsed.String())
		mirror, mirrorErr := YarnTarballFile(parsed.String())
		if !public || mirrorErr != nil || !validSRI(pkg.integrity) || pkg.version == "" {
			plan.Issues = append(plan.Issues, "Yarn entry "+selector+" needs an HTTPS registry tarball and SHA-256 or SHA-512 integrity")
			continue
		}
		identity := parsed.String() + "\n" + pkg.integrity
		if old, exists := mirrors[mirror]; exists {
			if old != identity {
				plan.Issues = append(plan.Issues, "conflicting Yarn offline mirror filename: "+mirror)
			}
			continue
		}
		mirrors[mirror], origins[origin] = identity, true
		plan.Packages = append(plan.Packages, Package{ID: selector, URL: parsed.String(), Integrity: pkg.integrity, Kind: KindYarnTarball})
	}
	for origin := range origins {
		plan.Sources = append(plan.Sources, origin)
	}
	return plan
}

var yarnPackageName = regexp.MustCompile(`^(?:@[a-z0-9._-]+/)?[a-z0-9._-]+$`)

func validYarnSelector(selector string) bool {
	if len(selector) < 3 || len(selector) > 1024 {
		return false
	}
	index := strings.Index(selector[1:], "@") + 1
	return index > 0 && yarnPackageName.MatchString(selector[:index]) && len(selector[index+1:]) > 0 &&
		!strings.ContainsAny(selector[index+1:], ":/\\#\r\n\x00")
}

// Yarn's offline mirror uses the registry tarball basename, prefixed by scope.
// Limit this reader to canonical npm-style paths rather than guessing arbitrary
// download paths that could collide in the mirror.
func YarnTarballFile(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.RawPath != "" || strings.ContainsAny(u.Path, "\\\x00") {
		return "", fmt.Errorf("invalid Yarn registry archive path")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	filename := ""
	if len(parts) == 3 && yarnPackageName.MatchString(parts[0]) && parts[1] == "-" {
		filename = parts[2]
	} else if len(parts) == 4 && strings.HasPrefix(parts[0], "@") && yarnPackageName.MatchString(parts[0]+"/"+parts[1]) && parts[2] == "-" {
		filename = parts[0] + "-" + parts[3]
	}
	if filename == "" || path.Base(filename) != filename || !strings.HasSuffix(filename, ".tgz") || len(filename) > 240 ||
		strings.ContainsAny(filename, ":\r\n\x00") {
		return "", fmt.Errorf("unsupported Yarn registry archive path")
	}
	return filename, nil
}

// Read only the generated Classic lock grammar used by registry entries.
// Unsupported fields, duplicate selectors/keys and malformed indentation fail
// before any source approval or package-manager execution is requested.
func readYarnClassicLock(raw []byte) (map[string]*yarnLockedPackage, error) {
	if !strings.Contains(string(raw), "# yarn lockfile v1") {
		return nil, fmt.Errorf("Yarn preparation requires a Classic v1 lockfile")
	}
	packages := map[string]*yarnLockedPackage{}
	var current *yarnLockedPackage
	var fields map[string]bool
	section := ""
	for index, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		content := strings.TrimSpace(line)
		if content == "" || strings.HasPrefix(content, "#") {
			continue
		}
		fail := func() (map[string]*yarnLockedPackage, error) {
			return nil, fmt.Errorf("unsupported or malformed yarn.lock entry at line %d", index+1)
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			if !strings.HasSuffix(content, ":") || len(packages) >= MaxPackages {
				return fail()
			}
			current = &yarnLockedPackage{dependencies: map[string]string{}}
			fields, section = map[string]bool{}, ""
			remaining := strings.TrimSuffix(content, ":")
			for {
				selector, rest, err := yarnAtom(remaining)
				if err != nil || !validYarnSelector(selector) || packages[selector] != nil || len(packages) >= MaxPackages {
					return fail()
				}
				packages[selector] = current
				if rest == "" {
					break
				}
				if !strings.HasPrefix(rest, ",") {
					return fail()
				}
				remaining = strings.TrimSpace(rest[1:])
			}
			continue
		}
		if current == nil {
			return fail()
		}
		key, remainder, err := yarnAtom(content)
		if err != nil {
			return fail()
		}
		if indent == 2 && remainder == ":" && (key == "dependencies" || key == "optionalDependencies") {
			if fields[key] {
				return fail()
			}
			fields[key], section = true, key
			continue
		}
		value, rest, err := yarnAtom(remainder)
		if err != nil || rest != "" {
			return fail()
		}
		if indent == 4 && section != "" {
			if _, duplicate := current.dependencies[key]; duplicate || !validYarnSelector(key+"@"+value) {
				return fail()
			}
			current.dependencies[key] = value
		} else if indent == 2 && !fields[key] {
			fields[key], section = true, ""
			switch key {
			case "version":
				current.version = value
			case "resolved":
				current.resolved = value
			case "integrity":
				current.integrity = value
			default:
				return fail()
			}
		} else {
			return fail()
		}
	}
	return packages, nil
}

func yarnAtom(text string) (string, string, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "\"") {
		var value string
		decoder := json.NewDecoder(strings.NewReader(text))
		if err := decoder.Decode(&value); err != nil {
			return "", "", err
		}
		return value, strings.TrimSpace(text[decoder.InputOffset():]), nil
	}
	end := strings.IndexAny(text, " \t,:")
	if end < 0 {
		end = len(text)
	}
	if end == 0 {
		return "", "", fmt.Errorf("missing lockfile value")
	}
	return text[:end], strings.TrimSpace(text[end:]), nil
}

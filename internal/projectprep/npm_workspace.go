package projectprep

import (
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"strings"
)

func npmWorkspacePatterns(raw json.RawMessage) ([]string, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return nil, nil
	}
	var patterns []string
	if strings.HasPrefix(value, "{") {
		var legacy struct {
			Packages []string `json:"packages"`
		}
		if err := json.Unmarshal(raw, &legacy); err != nil || legacy.Packages == nil {
			return nil, fmt.Errorf("npm workspaces must declare a packages array")
		}
		patterns = legacy.Packages
	} else if err := json.Unmarshal(raw, &patterns); err != nil {
		return nil, fmt.Errorf("npm workspaces must be an array of relative package patterns")
	}
	return patterns, nil
}

// npm shares one lock and installation root. Ancestor discovery never crosses
// the Run workspace; every discovered member is bound before native npm runs.
func detectNPMProject(plan Plan, reader fileReader, selectedFiles map[string]sourceFile) Plan {
	workspaceRoot := ""
	for current := reader.target; ; current = path.Dir(current) {
		at := reader
		at.target = current
		file, found, err := at.read("package.json")
		if err != nil {
			plan.Issues = append(plan.Issues, path.Join(current, "package.json")+": "+err.Error())
			break
		}
		var manifest npmManifest
		if found && json.Unmarshal(file.raw, &manifest) == nil {
			patterns, err := npmWorkspacePatterns(manifest.Workspaces)
			if err != nil || len(patterns) > 0 {
				workspaceRoot = current
				break
			}
		}
		if current == "." {
			break
		}
	}
	if workspaceRoot == "" {
		return detectNPM(plan, selectedFiles, nil)
	}
	plan.WorkspaceRoot = workspaceRoot
	rootReader := reader
	rootReader.target = workspaceRoot
	rootFiles := map[string]sourceFile{}
	for _, name := range []string{"package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb", ".npmrc", ".nvmrc", ".node-version", ".tool-versions"} {
		file, found, err := rootReader.read(name)
		if err != nil {
			plan.Issues = append(plan.Issues, path.Join(workspaceRoot, name)+": "+err.Error())
		}
		if found {
			rootFiles[name] = file
		}
	}
	var rootManifest npmManifest
	if json.Unmarshal(rootFiles["package.json"].raw, &rootManifest) != nil {
		return detectNPM(plan, rootFiles, nil)
	}
	patterns, err := npmWorkspacePatterns(rootManifest.Workspaces)
	if err != nil {
		plan.Issues = append(plan.Issues, err.Error())
	}
	directories, err := workspaceMembers(reader, workspaceRoot, patterns, "npm")
	if err != nil {
		plan.Issues = append(plan.Issues, err.Error())
	}
	members := map[string]npmManifest{}
	bound := []File{}
	names := map[string]bool{}
	for _, directory := range directories {
		at := reader
		at.target = directory
		file, found, err := at.read("package.json")
		if err != nil || !found {
			plan.Issues = append(plan.Issues, directory+" has no readable workspace package.json")
			continue
		}
		bound = append(bound, file.File)
		var manifest npmManifest
		if json.Unmarshal(file.raw, &manifest) != nil || !validNPMWorkspaceName(manifest.Name) || names[manifest.Name] {
			plan.Issues = append(plan.Issues, file.Path+" must declare a valid unique workspace package name")
			continue
		}
		names[manifest.Name] = true
		if manifest.PackageManager != "" && !strings.HasPrefix(manifest.PackageManager, "npm@") {
			plan.Issues = append(plan.Issues, file.Path+" selects a different package manager")
		}
		members[pnpmRelative(workspaceRoot, directory)] = manifest
		for _, name := range []string{"package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb", ".npmrc", ".nvmrc", ".node-version", ".tool-versions"} {
			file, found, err := at.read(name)
			if err != nil {
				plan.Issues = append(plan.Issues, path.Join(directory, name)+": "+err.Error())
			}
			if found {
				bound = append(bound, file.File)
				if name == "npm-shrinkwrap.json" || strings.Contains(name, "lock") {
					plan.Issues = append(plan.Issues, file.Path+" conflicts with the npm workspace root lockfile")
				}
			}
		}
	}
	if reader.target != workspaceRoot {
		if _, exists := members[pnpmRelative(workspaceRoot, reader.target)]; !exists {
			plan.Issues = append(plan.Issues, "Selected directory is not a member of the npm workspace")
		}
	}
	plan = detectNPM(plan, rootFiles, members)
	selected := selectedFiles["package.json"]
	if plan.Manifest.Path != selected.Path {
		plan.Lockfiles = append(plan.Lockfiles, plan.Manifest)
	}
	plan.Manifest = selected.File
	plan.Test = Command{}
	var selectedManifest npmManifest
	if json.Unmarshal(selected.raw, &selectedManifest) == nil && strings.TrimSpace(selectedManifest.Scripts["test"]) != "" {
		plan.Test = Command{ID: "test.npm", Display: "npm test --offline --no-audit --no-fund", Arguments: []string{"test", "--offline", "--no-audit", "--no-fund"}}
	}
	for _, file := range bound {
		if file.Path != plan.Manifest.Path {
			plan.Lockfiles = append(plan.Lockfiles, file)
		}
	}
	return plan
}

func validNPMWorkspaceName(name string) bool {
	if len(name) == 0 || len(name) > 214 || strings.ContainsAny(name, "\\:\r\n\x00") {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) > 2 || (len(parts) == 2 && !strings.HasPrefix(parts[0], "@")) {
		return false
	}
	for _, part := range parts {
		part = strings.TrimPrefix(part, "@")
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return false
			}
		}
	}
	return true
}

func npmInstallationPath(name string, members map[string]npmManifest) bool {
	if safeNPMPath(name) {
		return true
	}
	for directory := range members {
		if strings.HasPrefix(name, directory+"/") && safeNPMPath(strings.TrimPrefix(name, directory+"/")) {
			return true
		}
	}
	return false
}

func validateNPMWorkspaceLock(plan Plan, root npmManifest, lock npmLock, members map[string]npmManifest) Plan {
	patterns, _ := npmWorkspacePatterns(root.Workspaces)
	lockedPatterns, err := npmWorkspacePatterns(lock.Packages[""].Workspaces)
	if err != nil || !reflect.DeepEqual(patterns, lockedPatterns) {
		plan.Issues = append(plan.Issues, "package-lock.json workspaces differ from package.json")
	}
	plan.NPMWorkspaceLinks = map[string]string{}
	manifests := map[string]npmManifest{".": root}
	for directory, manifest := range members {
		manifests[directory] = manifest
	}
	for directory, manifest := range manifests {
		for _, group := range []map[string]string{manifest.Dependencies, manifest.DevDependencies, manifest.OptionalDependencies, manifest.PeerDependencies} {
			for name, specifier := range group {
				if strings.HasPrefix(specifier, "workspace:") || strings.HasPrefix(specifier, "link:") {
					plan.Issues = append(plan.Issues, directory+" dependency "+name+" uses an unsupported npm local protocol")
				}
				if local, found := strings.CutPrefix(specifier, "file:"); found {
					member, exists := members[path.Join(directory, local)]
					if strings.ContainsAny(local, "%\\:\r\n\x00") || path.IsAbs(local) || !exists || member.Name != name {
						plan.Issues = append(plan.Issues, directory+" dependency "+name+" is not a declared npm workspace member")
					}
				}
			}
		}
	}
	for directory, manifest := range members {
		pkg, found := lock.Packages[directory]
		if !found || pkg.Link || pkg.Resolved != "" || pkg.Integrity != "" || pkg.Version != manifest.Version || (pkg.Name != "" && pkg.Name != manifest.Name) || !sameDependencies(pkg.Dependencies, manifest.Dependencies) || !sameDependencies(pkg.DevDependencies, manifest.DevDependencies) || !sameDependencies(pkg.OptionalDependencies, manifest.OptionalDependencies) || !sameDependencies(pkg.PeerDependencies, manifest.PeerDependencies) {
			plan.Issues = append(plan.Issues, "package-lock.json workspace "+directory+" differs from package.json")
		}
		link, found := lock.Packages["node_modules/"+manifest.Name]
		if !found || !link.Link || link.Resolved != directory {
			plan.Issues = append(plan.Issues, "package-lock.json is missing workspace link for "+directory)
		}
	}
	for name, pkg := range lock.Packages {
		if name == "" {
			continue
		}
		if _, member := members[name]; member {
			continue
		}
		if !npmInstallationPath(name, members) {
			plan.Issues = append(plan.Issues, "Locked npm package "+name+" is not a declared workspace member or installed package")
			continue
		}
		if !pkg.Link {
			continue
		}
		member, found := members[pkg.Resolved]
		if !found || !strings.HasSuffix(name, "node_modules/"+member.Name) || pkg.Integrity != "" {
			plan.Issues = append(plan.Issues, "Locked npm package "+name+" uses an unsupported local or workspace link")
			continue
		}
		plan.NPMWorkspaceLinks[path.Join(plan.WorkspaceRoot, name)] = path.Join(plan.WorkspaceRoot, pkg.Resolved)
	}
	return plan
}

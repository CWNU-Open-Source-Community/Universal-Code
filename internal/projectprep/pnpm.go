package projectprep

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"cyberagent-workbench/internal/workspaceidentity"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

const maxPNPMMembers = 60

type pnpmDependency struct {
	Specifier string `yaml:"specifier"`
	Version   string `yaml:"version"`
}
type pnpmImporter struct {
	Dependencies         map[string]pnpmDependency `yaml:"dependencies"`
	DevDependencies      map[string]pnpmDependency `yaml:"devDependencies"`
	OptionalDependencies map[string]pnpmDependency `yaml:"optionalDependencies"`
}
type pnpmLockedPackage struct {
	Resolution map[string]string `yaml:"resolution"`
}
type pnpmSnapshot struct {
	Dependencies         map[string]string `yaml:"dependencies"`
	OptionalDependencies map[string]string `yaml:"optionalDependencies"`
}
type pnpmLock struct {
	LockfileVersion string                       `yaml:"lockfileVersion"`
	Importers       map[string]pnpmImporter      `yaml:"importers"`
	Packages        map[string]pnpmLockedPackage `yaml:"packages"`
	Snapshots       map[string]pnpmSnapshot      `yaml:"snapshots"`
}

// Walk only ancestors inside os.Root. A selected member retains its target;
// installation is bound to the nearest shared lock/workspace root.
func findPNPMRoot(reader fileReader, files map[string]sourceFile) (string, bool, error) {
	lockRoot := ""
	for current := reader.target; ; current = path.Dir(current) {
		r := reader
		r.target = current
		for _, name := range []string{"pnpm-workspace.yaml", "pnpm-lock.yaml"} {
			_, found, err := r.read(name)
			if err != nil {
				return current, true, fmt.Errorf("%s: %w", path.Join(current, name), err)
			}
			if found {
				if name == "pnpm-workspace.yaml" {
					return current, true, nil
				}
				if lockRoot == "" {
					lockRoot = current
				}
			}
		}
		if current == "." {
			break
		}
	}
	if lockRoot != "" {
		return lockRoot, true, nil
	}
	var manifest npmManifest
	_ = json.Unmarshal(files["package.json"].raw, &manifest)
	return reader.target, strings.HasPrefix(manifest.PackageManager, "pnpm@"), nil
}

func detectPNPM(plan Plan, reader fileReader, selectedFiles map[string]sourceFile, workspaceRoot string) Plan {
	plan.Ecosystem, plan.Toolchain = "pnpm", "pnpm"
	plan.WorkspaceRoot, plan.RequestedVersion = workspaceRoot, PNPMVersion
	plan.Manifest = selectedFiles["package.json"].File
	plan.Install = Command{ID: "install.pnpm", Display: "pnpm install --offline --frozen-lockfile --ignore-scripts", Arguments: []string{"install", "--offline", "--frozen-lockfile", "--ignore-scripts"}}
	var selected npmManifest
	if json.Unmarshal(selectedFiles["package.json"].raw, &selected) == nil && strings.TrimSpace(selected.Scripts["test"]) != "" {
		plan.Test = Command{ID: "test.pnpm", Display: "pnpm test", Arguments: []string{"test"}}
	}
	r := reader
	r.target = workspaceRoot
	bound := map[string]bool{plan.Manifest.Path: true}
	bind := func(file sourceFile) {
		if !bound[file.Path] {
			if len(bound) < 64 {
				plan.Lockfiles = append(plan.Lockfiles, file.File)
			}
			bound[file.Path] = true
		}
	}
	read := func(at fileReader, name string) (sourceFile, bool) {
		file, found, err := at.read(name)
		if err != nil {
			plan.Issues = append(plan.Issues, path.Join(at.target, name)+": "+err.Error())
		}
		if found {
			bind(file)
		}
		return file, found
	}
	workspace, hasWorkspace := read(r, "pnpm-workspace.yaml")
	patterns := []string{}
	registry := map[string]string{"": "https://registry.npmjs.org"}
	if hasWorkspace {
		var config struct {
			Packages []string `yaml:"packages"`
			Registry string   `yaml:"registry"`
		}
		_, err := pnpmYAML(workspace.raw, &config)
		if err != nil {
			plan.Issues = append(plan.Issues, "pnpm-workspace.yaml is invalid: "+err.Error())
		} else {
			if config.Packages == nil {
				plan.Issues = append(plan.Issues, "pnpm-workspace.yaml must declare an explicit packages list")
			}
			patterns = config.Packages
			if config.Registry != "" {
				registry[""] = strings.TrimSuffix(config.Registry, "/")
			}
		}
	}
	members := []string{workspaceRoot}
	if hasWorkspace {
		found, err := pnpmMembers(reader, workspaceRoot, patterns)
		if err != nil {
			plan.Issues = append(plan.Issues, err.Error())
		} else {
			members = append(members, found...)
		}
	}
	manifests := map[string]npmManifest{}
	memberNames := map[string]string{}
	for _, directory := range members {
		at := reader
		at.target = directory
		manifestFile, found := read(at, "package.json")
		if !found {
			plan.Issues = append(plan.Issues, path.Join(directory, "package.json")+" is required for pnpm preparation")
			continue
		}
		var manifest npmManifest
		if json.Unmarshal(manifestFile.raw, &manifest) != nil || !strings.HasPrefix(strings.TrimSpace(string(manifestFile.raw)), "{") {
			plan.Issues = append(plan.Issues, manifestFile.Path+" is not a valid package manifest")
			continue
		}
		if manifest.PackageManager != "" && manifest.PackageManager != "pnpm@"+PNPMVersion {
			plan.Issues = append(plan.Issues, manifestFile.Path+" packageManager must select pnpm@"+PNPMVersion+"; other versions are not downloaded automatically")
		}
		if directory != workspaceRoot && manifest.Name == "" {
			plan.Issues = append(plan.Issues, manifestFile.Path+" needs a workspace package name")
		}
		if manifest.Name != "" {
			if previous, exists := memberNames[manifest.Name]; exists {
				plan.Issues = append(plan.Issues, "Duplicate workspace package name in "+previous+" and "+directory)
			}
			memberNames[manifest.Name] = directory
		}
		manifests[directory] = manifest
		for _, name := range []string{".npmrc", ".nvmrc", ".node-version", ".tool-versions", ".pnpmfile.cjs"} {
			config, found := read(at, name)
			if found && name == ".npmrc" {
				if err := pnpmRegistries(config.raw, registry, directory == workspaceRoot); err != nil {
					plan.Issues = append(plan.Issues, config.Path+": "+err.Error())
				}
			}
		}
		for _, name := range []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "bun.lock", "bun.lockb"} {
			if file, found := read(at, name); found {
				plan.Issues = append(plan.Issues, file.Path+" conflicts with pnpm-lock.yaml; select one package manager")
			}
		}
		if directory != workspaceRoot {
			for _, name := range []string{"pnpm-lock.yaml", "pnpm-workspace.yaml"} {
				if file, found := read(at, name); found {
					plan.Issues = append(plan.Issues, file.Path+" conflicts with the shared pnpm workspace")
				}
			}
		}
	}
	// Ancestor npm configuration can affect a nested standalone project. Bind
	// it as review input, but registry routing comes from the installation root.
	for ancestor := path.Dir(workspaceRoot); workspaceRoot != "."; ancestor = path.Dir(ancestor) {
		at := reader
		at.target = ancestor
		for _, name := range []string{".npmrc", ".pnpmfile.cjs"} {
			read(at, name)
		}
		if ancestor == "." {
			break
		}
	}
	if _, member := manifests[plan.Target]; !member {
		plan.Issues = append(plan.Issues, "Selected directory is not a member of the pnpm workspace")
	}
	locked, found := read(r, "pnpm-lock.yaml")
	if !found {
		plan.Issues = append(plan.Issues, "pnpm-lock.yaml is missing; create and review a pnpm 10 lockfile before preparation")
		return plan
	}
	var lock pnpmLock
	document, err := pnpmYAML(locked.raw, &lock)
	if err != nil || lock.LockfileVersion != "9.0" || lock.Importers == nil {
		plan.Issues = append(plan.Issues, "pnpm-lock.yaml must use lockfileVersion 9.0 with an importers map")
		return plan
	}
	if len(lock.Packages) > MaxPackages || len(lock.Snapshots) > MaxPackages || len(lock.Importers) > maxPNPMMembers+1 {
		plan.Issues = append(plan.Issues, "pnpm-lock.yaml exceeds the preparation package or workspace member limit")
		return plan
	}
	for _, directory := range members {
		manifest, exists := manifests[directory]
		if !exists {
			continue
		}
		key := pnpmRelative(workspaceRoot, directory)
		importer, exists := lock.Importers[key]
		if !exists {
			plan.Issues = append(plan.Issues, "pnpm-lock.yaml is missing importer "+key)
			continue
		}
		for _, group := range []struct {
			locked map[string]pnpmDependency
			wanted map[string]string
		}{{importer.Dependencies, manifest.Dependencies}, {importer.DevDependencies, manifest.DevDependencies}, {importer.OptionalDependencies, manifest.OptionalDependencies}} {
			if len(group.locked) != len(group.wanted) {
				plan.Issues = append(plan.Issues, "pnpm-lock.yaml importer "+key+" dependencies differ from package.json")
			}
			for _, name := range sortedPNPMKeys(group.wanted) {
				wanted := group.wanted[name]
				dependency, exists := group.locked[name]
				if !exists || dependency.Specifier != wanted {
					plan.Issues = append(plan.Issues, "pnpm-lock.yaml importer "+key+" dependency "+name+" differs from package.json")
					continue
				}
				if err := validatePNPMDependency(name, wanted, dependency.Version, directory, manifests, memberNames, lock); err != nil {
					plan.Issues = append(plan.Issues, key+" dependency "+name+": "+err.Error())
				}
			}
		}
	}
	for _, key := range sortedPNPMKeys(lock.Importers) {
		if key != "." && !pnpmSafeRelative(key) {
			plan.Issues = append(plan.Issues, "pnpm-lock.yaml importer path is invalid: "+key)
			continue
		}
		if _, exists := manifests[path.Join(workspaceRoot, key)]; !exists {
			plan.Issues = append(plan.Issues, "pnpm-lock.yaml importer "+key+" is not a declared workspace member")
		}
	}
	origins := map[string]bool{}
	for _, id := range sortedPNPMKeys(lock.Packages) {
		pkg := lock.Packages[id]
		name, version, valid := pnpmPackageID(id)
		if !valid {
			plan.Issues = append(plan.Issues, "Locked pnpm package "+id+" is not a supported registry package")
			continue
		}
		tarball := pkg.Resolution["tarball"]
		if tarball == "" {
			base := registry[""]
			if strings.HasPrefix(name, "@") {
				if scoped, exists := registry[strings.SplitN(name, "/", 2)[0]]; exists {
					base = scoped
				}
			}
			tarball = strings.TrimSuffix(base, "/") + "/" + name + "/-/" + path.Base(name) + "-" + version + ".tgz"
		}
		origin, valid := publicOrigin(tarball)
		if !valid || !validSRI(pkg.Resolution["integrity"]) || len(pkg.Resolution) > 2 || (len(pkg.Resolution) == 2 && pkg.Resolution["tarball"] == "") {
			plan.Issues = append(plan.Issues, "Locked pnpm package "+id+" needs a public HTTPS tarball URL and SHA-256 or SHA-512 integrity")
			continue
		}
		origins[origin] = true
		plan.Packages = append(plan.Packages, Package{ID: id, URL: tarball, Integrity: pkg.Resolution["integrity"], Kind: KindPNPMTarball, Version: version})
		if resolution := pnpmMapValue(pnpmMapValue(pnpmMapValue(document.Content[0], "packages"), id), "resolution"); resolution != nil {
			pnpmSetScalar(resolution, "tarball", "file:../pnpm-tarballs/"+PNPMTarballFile(id))
		}
	}
	for _, id := range sortedPNPMKeys(lock.Snapshots) {
		base := strings.SplitN(id, "(", 2)[0]
		if _, exists := lock.Packages[base]; !exists {
			plan.Issues = append(plan.Issues, "pnpm snapshot "+id+" has no registry package resolution")
		}
		snapshot := lock.Snapshots[id]
		for _, dependencies := range []map[string]string{snapshot.Dependencies, snapshot.OptionalDependencies} {
			for _, name := range sortedPNPMKeys(dependencies) {
				if !pnpmRegistryReference(name, dependencies[name], lock) {
					plan.Issues = append(plan.Issues, "pnpm snapshot "+id+" has an unsupported or missing dependency "+name)
				}
			}
		}
	}
	for origin := range origins {
		plan.Sources = append(plan.Sources, origin)
	}
	if len(bound) > 64 {
		plan.Issues = append(plan.Issues, "pnpm preparation supports at most 64 bound manifest/configuration files; reduce the workspace scope")
	}
	if len(plan.Issues) == 0 {
		plan.PNPMSeedLock, err = yaml.Marshal(document)
		if err != nil {
			plan.Issues = append(plan.Issues, "pnpm seed lock could not be encoded")
		} else if len(plan.PNPMSeedLock) > MaxFileBytes {
			plan.PNPMSeedLock = nil
			plan.Issues = append(plan.Issues, "pnpm seed lock exceeds the preparation size limit")
		}
	}
	return plan
}

func validatePNPMDependency(name, specifier, version, directory string, manifests map[string]npmManifest, names map[string]string, lock pnpmLock) error {
	if strings.HasPrefix(version, "link:") {
		link := strings.TrimPrefix(version, "link:")
		if link == "" || path.IsAbs(link) || strings.ContainsAny(link, "\\:\x00\r\n") {
			return errors.New("workspace link path is invalid")
		}
		destination := path.Join(directory, link)
		member, exists := manifests[destination]
		if !exists || !strings.HasPrefix(specifier, "workspace:") {
			return errors.New("only declared workspace: members may use local links")
		}
		requirement := strings.TrimPrefix(specifier, "workspace:")
		if requirement != "*" && requirement != "^" && requirement != "~" && requirement != member.Version {
			return errors.New("workspace dependency supports *, ^, ~ or an exact member version")
		}
		if names[name] != destination {
			return errors.New("workspace link does not match the dependency package name")
		}
		return nil
	}
	if strings.HasPrefix(specifier, "workspace:") {
		return errors.New("workspace dependency must resolve to its declared local member")
	}
	if strings.HasPrefix(specifier, "file:") || strings.HasPrefix(specifier, "link:") || strings.HasPrefix(specifier, "catalog:") || strings.HasPrefix(specifier, "git") || strings.HasPrefix(specifier, "github:") {
		return errors.New("local, Git and catalog dependency sources are not supported")
	}
	if !pnpmRegistryReference(name, version, lock) {
		return errors.New("locked registry dependency or snapshot is missing or unsupported")
	}
	if semver.IsValid("v"+specifier) && strings.SplitN(version, "(", 2)[0] != specifier {
		return errors.New("locked version differs from the exact package.json requirement")
	}
	return nil
}

func pnpmRegistryReference(name, version string, lock pnpmLock) bool {
	id := name + "@" + version
	if _, exists := lock.Snapshots[id]; exists {
		_, _, valid := pnpmPackageID(strings.SplitN(id, "(", 2)[0])
		return valid
	}
	// npm aliases carry the real package name in the locked version.
	if _, exists := lock.Snapshots[version]; exists {
		_, _, valid := pnpmPackageID(strings.SplitN(version, "(", 2)[0])
		return valid
	}
	return false
}

func pnpmPackageID(id string) (string, string, bool) {
	at := strings.LastIndex(id, "@")
	if at < 1 {
		return "", "", false
	}
	name, version := id[:at], id[at+1:]
	if !semver.IsValid("v"+version) || strings.Count(version, ".") < 2 {
		return "", "", false
	}
	parts := strings.Split(name, "/")
	if len(parts) > 2 || (len(parts) == 2 && !strings.HasPrefix(parts[0], "@")) {
		return "", "", false
	}
	for _, part := range parts {
		if part == "" || part == "@" || part == "." || part == ".." {
			return "", "", false
		}
		for _, c := range strings.TrimPrefix(part, "@") {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.~", c)) {
				return "", "", false
			}
		}
	}
	return name, version, true
}

func pnpmRegistries(raw []byte, registry map[string]string, apply bool) error {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			return errors.New(".npmrc must contain explicit key=value settings")
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key != "registry" && !strings.HasSuffix(key, ":registry") {
			continue
		}
		value = strings.TrimSuffix(value, "/")
		if _, valid := publicOrigin(value); !valid || strings.Contains(value, "${") {
			return errors.New("registry must be an explicit public HTTPS URL")
		}
		if apply {
			scope := strings.TrimSuffix(key, ":registry")
			if key == "registry" {
				scope = ""
			}
			registry[scope] = value
		}
	}
	return nil
}

func pnpmMembers(reader fileReader, root string, patterns []string) ([]string, error) {
	if len(patterns) > 128 {
		return nil, errors.New("pnpm workspace supports at most 128 package patterns")
	}
	for _, pattern := range patterns {
		value := strings.TrimPrefix(pattern, "!")
		if len(value) > 256 || strings.Count(value, "**") > 1 || !pnpmSafeRelative(value) || strings.ContainsAny(value, "{}[]") {
			return nil, errors.New("pnpm workspace patterns must be relative paths using *, ** or ? without parent traversal")
		}
	}
	members := []string{}
	count := 0
	err := fs.WalkDir(reader.root.FS(), root, func(current string, entry fs.DirEntry, walkErr error) error {
		if err := reader.ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		count++
		if count > 25000 {
			return errors.New("pnpm workspace discovery exceeds 25000 directory entries")
		}
		if current == root {
			return nil
		}
		if entry.Name() == "node_modules" || entry.Name() == ".git" || entry.Name() == ".traverse-preparation" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		relative := pnpmRelative(root, current)
		relevant, matched := false, false
		for _, pattern := range patterns {
			if !strings.HasPrefix(pattern, "!") {
				relevant = relevant || pnpmPatternPrefix(pattern, relative)
				matched = matched || pnpmMatch(pattern, relative)
			}
		}
		for _, pattern := range patterns {
			if strings.HasPrefix(pattern, "!") && pnpmMatch(pattern[1:], relative) {
				matched = false
			}
		}
		if entry.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 && relevant {
			return fmt.Errorf("pnpm workspace member path %s contains symlink indirection", current)
		}
		if !entry.IsDir() {
			return nil
		}
		if !relevant {
			return fs.SkipDir
		}
		if matched {
			at := reader
			at.target = current
			_, found, err := at.read("package.json")
			if err != nil {
				return fmt.Errorf("%s: %w", current, err)
			}
			if found {
				members = append(members, current)
				if len(members) > maxPNPMMembers {
					return errors.New("pnpm workspace supports at most 60 members")
				}
			}
		}
		return nil
	})
	sort.Strings(members)
	return members, err
}

func pnpmRelative(root, current string) string {
	if root == current {
		return "."
	}
	if root == "." {
		return current
	}
	return strings.TrimPrefix(current, root+"/")
}
func pnpmSafeRelative(value string) bool {
	return value != "" && !path.IsAbs(value) && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../") && !strings.ContainsAny(value, "\\:\r\n\x00") && (workspaceidentity.PathPolicy{}).Check(value) == nil
}
func pnpmMatch(pattern, value string) bool {
	p := strings.Split(pattern, "/")
	v := strings.Split(value, "/")
	var match func(int, int) bool
	match = func(i, j int) bool {
		if i == len(p) {
			return j == len(v)
		}
		if p[i] == "**" {
			return match(i+1, j) || (j < len(v) && match(i, j+1))
		}
		if j == len(v) {
			return false
		}
		ok, err := path.Match(p[i], v[j])
		return err == nil && ok && match(i+1, j+1)
	}
	return match(0, 0)
}
func pnpmPatternPrefix(pattern, value string) bool {
	p := strings.Split(pattern, "/")
	v := strings.Split(value, "/")
	for i, part := range v {
		if i >= len(p) {
			return false
		}
		if p[i] == "**" {
			return true
		}
		ok, _ := path.Match(p[i], part)
		if !ok {
			return false
		}
	}
	return true
}
func sortedPNPMKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Reject aliases, duplicate keys and multi-document YAML before decoding. The
// seed is edited as a node tree so all original lock sections remain present.
func pnpmYAML(raw []byte, target any) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("expected one YAML document")
	}
	count := 0
	var check func(*yaml.Node, int) error
	check = func(n *yaml.Node, depth int) error {
		count++
		if depth > 64 || count > 250000 || n.Kind == yaml.AliasNode {
			return errors.New("YAML aliases or excessive nesting are unsupported")
		}
		if n.Kind == yaml.MappingNode {
			keys := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Kind != yaml.ScalarNode || keys[k.Value] {
					return errors.New("duplicate or non-scalar YAML mapping key")
				}
				keys[k.Value] = true
			}
		}
		for _, child := range n.Content {
			if err := check(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(&node, 0); err != nil {
		return nil, err
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("expected a YAML mapping")
	}
	if err := node.Decode(target); err != nil {
		return nil, err
	}
	return &node, nil
}
func pnpmMapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
func pnpmSetScalar(node *yaml.Node, key, value string) {
	if existing := pnpmMapValue(node, key); existing != nil {
		existing.Kind = yaml.ScalarNode
		existing.Tag = "!!str"
		existing.Value = value
		return
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

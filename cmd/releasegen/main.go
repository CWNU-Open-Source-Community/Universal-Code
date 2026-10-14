// Command releasegen emits a deterministic CycloneDX SBOM and a third-party
// NOTICE for the Go module graph and bundled non-Go assets. It is used by the
// Desktop portable ZIP packaging step; output contains no absolute personal
// paths, timestamps, or random identifiers beyond a content-derived serial
// number.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/buildinfo"
)

type module struct {
	Path    string
	Version string
	Dir     string
}

type license struct {
	SPDXID string
	Text   string
	Found  bool
}

type bundledAssetNotice struct {
	Name        string
	NoticeText  string
	LicenseText string
}

var bundledAssetNoticeSources = []struct {
	Name        string
	NoticePath  string
	LicensePath string
}{
	{
		Name:        "HarmonyOS Sans Fonts",
		NoticePath:  "web/public/THIRD-PARTY-NOTICES.txt",
		LicensePath: "web/public/licenses/HarmonyOS-Sans.txt",
	},
}

var licenseNames = []string{
	"LICENSE", "LICENSE.txt", "LICENSE.md", "LICENSE.rst",
	"COPYING", "COPYING.txt", "COPYING.md", "COPYRIGHT",
	"LICENSE-MIT", "LICENSE-APACHE", "LICENSE-BSD", "UNLICENSE",
}

// knownLicenses maps a case-insensitive distinguishing substring to an SPDX id.
var knownLicenses = []struct {
	Needle string
	SPDX   string
}{
	{"Apache License", "Apache-2.0"},
	{"Mozilla Public License", "MPL-2.0"},
	{"Mozilla Public License, v. 2.0", "MPL-2.0"},
	{"MIT License", "MIT"},
	{"Permission is hereby granted, free of charge", "MIT"},
	{"BSD 3-Clause", "BSD-3-Clause"},
	{"Redistribution and use in source and binary forms", "BSD-3-Clause"},
	{"BSD 2-Clause", "BSD-2-Clause"},
	{"ISC License", "ISC"},
	{"Permission to use, copy, modify, and/or distribute", "ISC"},
	{"This is free and unencumbered software released into the public domain", "Unlicense"},
	{"zlib License", "Zlib"},
	{"Creative Commons Legal Code", "CC0-1.0"},
	{"DO WHAT THE FUCK YOU WANT TO PUBLIC LICENSE", "WTFPL"},
	// Bare license declarations (last-resort, e.g. README "## License\n\nMIT").
	{"\nMIT\n", "MIT"},
	{"\nMIT ", "MIT"},
}

var zipTimestamp = time.Unix(315532800, 0).UTC() // 1980-01-01, the ZIP DOS minimum

func main() {
	outDir := flag.String("out", "build/desktop", "output directory for sbom.json and NOTICE")
	appVersion := flag.String("version", "v1.0.0", "application version for the SBOM component")
	pkg := flag.String("pkg", "./cmd/cyberagent-desktop", "Go package whose build list the SBOM covers")
	buildTags := flag.String("tags", "desktop,production,wv2runtime.error", "Go build tags for the dependency list")
	sourceRoot := flag.String("source-root", ".", "repository root containing bundled asset notices")
	zipSource := flag.String("zip", "", "if set, create a deterministic ZIP of this directory into --out/<zip-name>")
	zipName := flag.String("zip-name", "portable.zip", "output ZIP file name")
	flag.Parse()

	if *zipSource != "" {
		if err := createDeterministicZip(*zipSource, filepath.Join(*outDir, *zipName)); err != nil {
			fmt.Fprintln(os.Stderr, "releasegen:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stdout, "zip_written: %s\n", filepath.Join(*outDir, *zipName))
		return
	}

	modules, err := listModules(*pkg, *buildTags)
	if err != nil {
		fmt.Fprintln(os.Stderr, "releasegen:", err)
		os.Exit(1)
	}
	if err := validateLicenses(modules); err != nil {
		fmt.Fprintln(os.Stderr, "releasegen:", err)
		os.Exit(1)
	}
	bundledNotices, err := loadBundledAssetNotices(*sourceRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "releasegen:", err)
		os.Exit(1)
	}
	sbom, err := buildSBOM(modules, *appVersion)
	if err != nil {
		fmt.Fprintln(os.Stderr, "releasegen:", err)
		os.Exit(1)
	}
	notice := buildNotice(modules, bundledNotices)
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "releasegen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(*outDir, "sbom.json"), sbom, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "releasegen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(*outDir, "NOTICE"), notice, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "releasegen:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout, "sbom_written: %s\nnotice_written: %s\nmodules: %d\n",
		filepath.Join(*outDir, "sbom.json"), filepath.Join(*outDir, "NOTICE"), len(modules))
}

// listModules returns the distinct third-party modules actually compiled into
// the target package, using the exact build tags the distributed binary uses.
// It never runs go mod download, so it never rewrites go.sum.
func listModules(pkg, buildTags string) ([]module, error) {
	args := []string{"list", "-deps", "-tags", buildTags, "-json=Module", pkg}
	output, err := exec.Command("go", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("go list -deps: %w", err)
	}
	seen := map[string]bool{}
	var modules []module
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var dependency struct {
			Module *struct {
				Path, Version, Dir string
				Main               bool
			}
		}
		if err := decoder.Decode(&dependency); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode go list module: %w", err)
		}
		listed := dependency.Module
		if listed == nil || listed.Main {
			continue
		}
		key := listed.Path + "@" + listed.Version
		if seen[key] {
			continue
		}
		seen[key] = true
		modules = append(modules, module{Path: listed.Path, Version: listed.Version, Dir: listed.Dir})
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	return modules, nil
}

func detectLicense(dir string) license {
	if dir == "" {
		return license{}
	}
	for _, name := range licenseNames {
		path := filepath.Join(dir, name)
		content, err := os.ReadFile(path)
		if err != nil || len(content) == 0 {
			continue
		}
		text := string(content)
		if len(text) > 32*1024 {
			text = text[:32*1024]
		}
		return license{SPDXID: matchSPDX(text), Text: text, Found: true}
	}
	// Some small modules declare their license only in the README.
	for _, name := range []string{"README.md", "README", "readme.md"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || len(content) == 0 {
			continue
		}
		text := string(content)
		if spdx := matchSPDX(text); spdx != "LicenseRef-unknown" {
			return license{SPDXID: spdx, Text: text, Found: true}
		}
	}
	return license{}
}

// validateLicenses keeps release packaging fail-closed when a compiled module
// has no readable license text or the text cannot be mapped to an SPDX id.
func validateLicenses(modules []module) error {
	var invalid []string
	for _, module := range sortedModules(modules) {
		lic := detectLicense(module.Dir)
		if !lic.Found {
			invalid = append(invalid, module.Path+"@"+module.Version+" (missing)")
			continue
		}
		if lic.SPDXID == "LicenseRef-unknown" {
			invalid = append(invalid, module.Path+"@"+module.Version+" (unrecognized)")
		}
	}
	if len(invalid) != 0 {
		return fmt.Errorf("compiled modules have incomplete license metadata: %s", strings.Join(invalid, ", "))
	}
	return nil
}

// matchSPDX returns a best-effort SPDX id for a license text. It is a heuristic:
// the full NOTICE ships every detected license text for human review.
func matchSPDX(text string) string {
	for _, known := range knownLicenses {
		if strings.Contains(text, known.Needle) {
			return known.SPDX
		}
	}
	return "LicenseRef-unknown"
}

// buildSBOM emits a CycloneDX 1.4 document whose serial number is derived from
// the sorted component list, so identical module graphs yield identical bytes.
func buildSBOM(modules []module, appVersion string) ([]byte, error) {
	modules = sortedModules(modules)
	components := make([]map[string]any, 0, len(modules))
	for _, module := range modules {
		purl := "pkg:golang/" + module.Path + "@" + module.Version
		component := map[string]any{
			"type":    "library",
			"bom-ref": purl,
			"name":    module.Path,
			"version": module.Version,
			"purl":    purl,
		}
		if lic := detectLicense(module.Dir); lic.Found {
			component["licenses"] = []map[string]any{{
				"license": map[string]any{"id": lic.SPDXID},
			}}
		}
		components = append(components, component)
	}
	serialHash := sha256.New()
	for _, module := range modules {
		serialHash.Write([]byte(module.Path + "@" + module.Version + "\n"))
	}
	serialHex := hex.EncodeToString(serialHash.Sum(nil)[:16])
	serial := "urn:uuid:" + serialHex[0:8] + "-" + serialHex[8:12] + "-" +
		serialHex[12:16] + "-" + serialHex[16:20] + "-" + serialHex[20:32]

	document := map[string]any{
		"bomFormat":    "CycloneDX",
		"specVersion":  "1.4",
		"serialNumber": serial,
		"version":      1,
		"metadata": map[string]any{
			"component": map[string]any{
				"type":    "application",
				"bom-ref": "pkg:golang/cyberagent-workbench@" + appVersion,
				"name":    "cyberagent-workbench",
				"version": appVersion,
			},
		},
		"components": components,
	}
	return json.MarshalIndent(document, "", "  ")
}

func sortedModules(modules []module) []module {
	copyModules := append([]module(nil), modules...)
	sort.Slice(copyModules, func(i, j int) bool { return copyModules[i].Path < copyModules[j].Path })
	return copyModules
}

func loadBundledAssetNotices(sourceRoot string) ([]bundledAssetNotice, error) {
	notices := make([]bundledAssetNotice, 0, len(bundledAssetNoticeSources))
	for _, source := range bundledAssetNoticeSources {
		noticeText, err := readRequiredNoticeText(sourceRoot, source.NoticePath)
		if err != nil {
			return nil, fmt.Errorf("%s notice: %w", source.Name, err)
		}
		licenseText, err := readRequiredNoticeText(sourceRoot, source.LicensePath)
		if err != nil {
			return nil, fmt.Errorf("%s license: %w", source.Name, err)
		}
		if !strings.Contains(noticeText, "HarmonyOS Sans Fonts") {
			return nil, fmt.Errorf("%s notice is missing its product-use statement", source.Name)
		}
		if !strings.Contains(licenseText, "HarmonyOS Sans Fonts License Agreement") ||
			!strings.Contains(licenseText, "Copyright 2021 Huawei Device Co., Ltd.") {
			return nil, fmt.Errorf("%s license is incomplete", source.Name)
		}
		notices = append(notices, bundledAssetNotice{
			Name:        source.Name,
			NoticeText:  noticeText,
			LicenseText: licenseText,
		})
	}
	sort.Slice(notices, func(i, j int) bool { return notices[i].Name < notices[j].Name })
	return notices, nil
}

func readRequiredNoticeText(sourceRoot, relativePath string) (string, error) {
	content, err := os.ReadFile(filepath.Join(sourceRoot, filepath.FromSlash(relativePath)))
	if err != nil {
		return "", err
	}
	text := normalizeNoticeText(string(content))
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s is empty", relativePath)
	}
	return text, nil
}

func normalizeNoticeText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimRight(text, "\n") + "\n"
}

func buildNotice(modules []module, bundledNotices []bundledAssetNotice) []byte {
	modules = sortedModules(modules)
	var builder strings.Builder
	builder.WriteString(buildinfo.ProductName + " Desktop — Third-Party Notices\n\n")
	builder.WriteString("The Desktop build includes the following third-party Go modules.\n")
	builder.WriteString("Licenses were detected from each module's LICENSE file; full texts follow.\n\n")
	for _, module := range modules {
		lic := detectLicense(module.Dir)
		spdx := "unknown"
		if lic.Found {
			spdx = lic.SPDXID
		}
		fmt.Fprintf(&builder, "- %s@%s (%s)\n", module.Path, module.Version, spdx)
	}
	builder.WriteString("\n--- Full license texts ---\n\n")
	for _, module := range modules {
		lic := detectLicense(module.Dir)
		if !lic.Found {
			continue
		}
		fmt.Fprintf(&builder, "== %s@%s ==\n%s\n\n", module.Path, module.Version, lic.Text)
	}
	if len(bundledNotices) != 0 {
		builder.WriteString("--- Bundled UI asset notices ---\n\n")
		for _, notice := range bundledNotices {
			fmt.Fprintf(&builder, "== %s ==\n%s\n", notice.Name,
				normalizeNoticeText(notice.NoticeText))
		}
		builder.WriteString("--- Bundled UI asset license texts ---\n\n")
		for _, notice := range bundledNotices {
			fmt.Fprintf(&builder, "== %s ==\n%s\n", notice.Name,
				normalizeNoticeText(notice.LicenseText))
		}
	}
	return []byte(builder.String())
}

// createDeterministicZip writes a ZIP whose entries are sorted and all carry
// the fixed 1980-01-01 timestamp, so the same input directory yields identical
// ZIP bytes. It never records absolute paths or filesystem metadata.
func createDeterministicZip(sourceDir, outputPath string) error {
	var files []string
	err := filepath.Walk(sourceDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if strings.ContainsRune(relative, '\\') {
			return fmt.Errorf("unexpected backslash in ZIP entry %q", relative)
		}
		files = append(files, relative)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)

	output, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer output.Close()
	writer := zip.NewWriter(output)
	for _, name := range files {
		entry, err := writer.CreateHeader(&zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: zipTimestamp,
		})
		if err != nil {
			return err
		}
		source, err := os.Open(filepath.Join(sourceDir, name))
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entry, source)
		source.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return writer.Close()
}

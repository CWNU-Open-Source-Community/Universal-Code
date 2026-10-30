package projectconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	// InstructionScaffoldProtocolVersion identifies the scaffold result contract.
	InstructionScaffoldProtocolVersion = "project_instruction_scaffold.v1"
	// InstructionScaffoldFileName is the standard project instruction file that
	// DiscoverInstructions already treats as the base same-directory source.
	InstructionScaffoldFileName = "AGENTS.md"
	// InstructionScaffoldOverrideFileName shadows AGENTS.md in the same directory,
	// so scaffolding under an override would create a file the model never reads.
	InstructionScaffoldOverrideFileName = "AGENTS.override.md"
	// MaxInstructionScaffoldBytes keeps the draft comfortably inside the
	// per-file discovery limit (MaxInstructionFileBytes).
	MaxInstructionScaffoldBytes = 32 * 1024
)

// InstructionScaffoldDetection is one deterministic workspace observation that
// the draft mentions. Detection only records marker paths and fixed labels; it
// never copies file contents, so secrets cannot leak into the draft.
type InstructionScaffoldDetection struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

// InstructionScaffoldResult reports what the scaffold observed and produced.
// Content is always populated so operators can preview without writing.
type InstructionScaffoldResult struct {
	ProtocolVersion string                         `json:"protocol_version"`
	TargetPath      string                         `json:"target_path"`
	FilePath        string                         `json:"file_path"`
	Written         bool                           `json:"written"`
	ContentSHA256   string                         `json:"content_sha256"`
	ContentBytes    int                            `json:"content_bytes"`
	Detected        []InstructionScaffoldDetection `json:"detected"`
	Content         string                         `json:"content"`
}

// ScaffoldProjectInstructions renders a deterministic AGENTS.md draft for the
// target directory. It is the CLI-side analog of Claude Code /init and
// Codex /init onboarding: a pure, offline, no-model scaffold that bootstraps
// the project instruction file this codebase already discovers.
//
// The scaffold never overwrites: a writing scaffold refuses when
// AGENTS.override.md or AGENTS.md already exists in the target directory,
// including empty files, because an empty AGENTS.md is rejected by discovery
// and an override would shadow a freshly written AGENTS.md entirely. The
// refusal is enforced by an exclusive create, so concurrent scaffolds cannot
// truncate each other's AGENTS.md. A preview scaffold (write=false) always
// renders without touching the disk.
func ScaffoldProjectInstructions(ctx context.Context, workspaceRoot, targetPath string, write bool) (InstructionScaffoldResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	_, target, _, targetRelative, err := instructionBoundary(workspaceRoot, targetPath)
	if err != nil {
		return InstructionScaffoldResult{}, err
	}
	// instructionBoundary falls back to the parent directory when the target
	// itself does not exist; scaffolding must never silently redirect a
	// missing target to its parent, so the target directory must exist here.
	if targetInfo, statErr := os.Lstat(target); statErr != nil || !targetInfo.IsDir() {
		return InstructionScaffoldResult{}, errors.New("project instruction scaffold target must be an existing real directory")
	}
	if err := ctx.Err(); err != nil {
		return InstructionScaffoldResult{}, err
	}
	if write {
		// An existing override shadows a freshly written AGENTS.md, so the
		// scaffold refuses before rendering anything. The scaffold never
		// creates the override itself, so this check needs no exclusive
		// create; the AGENTS.md guard below is the exclusive create.
		overridePath := filepath.Join(target, InstructionScaffoldOverrideFileName)
		if _, statErr := os.Lstat(overridePath); statErr == nil {
			return InstructionScaffoldResult{}, fmt.Errorf("%s already exists in %s; scaffolding never overwrites project files (--print renders a preview)", InstructionScaffoldOverrideFileName, targetRelative)
		}
	}
	detected := detectScaffoldMarkers(target)
	content, err := renderInstructionScaffold(targetRelative, detected)
	if err != nil {
		return InstructionScaffoldResult{}, err
	}
	digest := sha256.Sum256([]byte(content))
	result := InstructionScaffoldResult{
		ProtocolVersion: InstructionScaffoldProtocolVersion,
		TargetPath:      targetRelative,
		FilePath:        canonicalInstructionPath(scaffoldSlashPath(targetRelative, InstructionScaffoldFileName)),
		Written:         false,
		ContentSHA256:   hex.EncodeToString(digest[:]),
		ContentBytes:    len(content),
		Detected:        detected,
		Content:         content,
	}
	if !write {
		return result, nil
	}
	scaffoldPath := filepath.Join(target, InstructionScaffoldFileName)
	if err := writeInstructionScaffoldFile(scaffoldPath, []byte(content)); err != nil {
		if errors.Is(err, os.ErrExist) || scaffoldPathExists(scaffoldPath) {
			return InstructionScaffoldResult{}, fmt.Errorf("%s already exists in %s; scaffolding never overwrites project files (--print renders a preview)", InstructionScaffoldFileName, targetRelative)
		}
		return InstructionScaffoldResult{}, fmt.Errorf("write scaffolded project instructions: %w", err)
	}
	result.Written = true
	return result, nil
}

// writeInstructionScaffoldFile creates path exclusively and writes content.
// O_EXCL makes the create atomic: the filesystem itself refuses to replace an
// existing file, so concurrent scaffolds cannot truncate each other's
// AGENTS.md. A failed write removes the partial file so no truncated draft is
// left behind.
func writeInstructionScaffoldFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// scaffoldPathExists reports whether path is present on disk. It backs the
// refusal mapping for exclusive creates that fail while a concurrently created
// file is still held open by another process.
func scaffoldPathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// scaffoldMarkers is a fixed, ordered detection table; order is part of the
// deterministic output contract. Marker checks use Lstat only, so a marker
// that is itself a symlink is still reported but never followed or read.
// Case-variant table entries (Makefile/makefile, Dockerfile/dockerfile) cover
// real distinct files on case-sensitive filesystems; on case-insensitive
// filesystems they resolve to one file, which os.SameFile collapses back to a
// single detection.
var scaffoldMarkers = []struct {
	path  string
	label string
}{
	{"go.mod", "Go module"},
	{"package.json", "Node.js package"},
	{"bun.lock", "Bun lockfile"},
	{"bun.lockb", "Bun lockfile"},
	{"package-lock.json", "npm lockfile"},
	{"pnpm-lock.yaml", "pnpm lockfile"},
	{"yarn.lock", "Yarn lockfile"},
	{"Cargo.toml", "Rust crate"},
	{"pyproject.toml", "Python project"},
	{"requirements.txt", "Python requirements"},
	{"setup.py", "Python setuptools project"},
	{"Gemfile", "Ruby bundle"},
	{"pom.xml", "Maven project"},
	{"build.gradle", "Gradle project"},
	{"build.gradle.kts", "Gradle project (Kotlin DSL)"},
	{"composer.json", "PHP package"},
	{"CMakeLists.txt", "CMake project"},
	{"Makefile", "Make build"},
	{"GNUmakefile", "Make build"},
	{"makefile", "Make build"},
	{"Dockerfile", "Docker image"},
	{"dockerfile", "Docker image"},
	{"README.md", "README"},
	{"README", "README"},
	{".prayu/config.yaml", "Universal-Code project config"},
	{".prayu/instructions.md", "Universal-Code guidance"},
	{"CLAUDE.md", "Claude Code instructions (also discovered)"},
}

func detectScaffoldMarkers(targetDir string) []InstructionScaffoldDetection {
	detected := []InstructionScaffoldDetection{}
	seenFiles := []os.FileInfo{}
	for _, marker := range scaffoldMarkers {
		info, err := os.Lstat(filepath.Join(targetDir, filepath.FromSlash(marker.path)))
		if err != nil {
			continue
		}
		duplicate := false
		for _, seen := range seenFiles {
			if os.SameFile(info, seen) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		seenFiles = append(seenFiles, info)
		detected = append(detected, InstructionScaffoldDetection{Path: marker.path, Label: marker.label})
	}
	// List the workflows directory only when both path segments are real
	// directories, so a symlinked .github or workflows directory is never
	// followed, matching the Lstat-only contract above.
	githubInfo, githubErr := os.Lstat(filepath.Join(targetDir, ".github"))
	if githubErr != nil || !githubInfo.IsDir() || githubInfo.Mode()&fs.ModeSymlink != 0 {
		return detected
	}
	workflowsInfo, workflowsErr := os.Lstat(filepath.Join(targetDir, ".github", "workflows"))
	if workflowsErr != nil || !workflowsInfo.IsDir() || workflowsInfo.Mode()&fs.ModeSymlink != 0 {
		return detected
	}
	if entries, err := os.ReadDir(filepath.Join(targetDir, ".github", "workflows")); err == nil {
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				detected = append(detected, InstructionScaffoldDetection{Path: ".github/workflows", Label: "GitHub Actions workflows"})
				break
			}
		}
	}
	return detected
}

func renderInstructionScaffold(targetRelative string, detected []InstructionScaffoldDetection) (string, error) {
	var builder strings.Builder
	builder.WriteString("# AGENTS.md\n\n")
	builder.WriteString("<!--\n")
	builder.WriteString("Scaffolded by `cyberagent context instructions scaffold` as a starting draft;\n")
	builder.WriteString("edit this file to describe the real project. Generated content is deterministic\n")
	builder.WriteString("and offline: it records only marker paths, never file contents or secrets.\n")
	builder.WriteString("由 `cyberagent context instructions scaffold` 生成的起始草稿，请按项目实际修改。\n")
	builder.WriteString("生成过程离线且确定性，仅记录标识文件路径，不读取内容，也不写入任何秘密。\n")
	builder.WriteString("-->\n\n")
	builder.WriteString("## Overview / 项目概览\n\n")
	builder.WriteString("Describe the project's purpose, layout, and owners here.\n")
	builder.WriteString("请补充项目目标、目录结构与负责人。\n\n")
	if len(detected) == 0 {
		builder.WriteString("- No common project markers were detected / 未检测到常见项目标识文件\n")
	} else {
		builder.WriteString("- Detected markers / 检测到的标识文件:\n")
		for _, detection := range detected {
			fmt.Fprintf(&builder, "  - `%s`: %s\n", detection.Path, detection.Label)
		}
	}
	builder.WriteString("\n## Build and verification / 构建与验证\n\n")
	builder.WriteString("Record the exact build, test, and format commands the project accepts.\n")
	builder.WriteString("请记录项目认可的构建、测试与格式化命令；下方提示仅为检测推断，使用前先在本地核实。\n\n")
	for _, detection := range detected {
		switch detection.Path {
		case "go.mod":
			builder.WriteString("- go.mod: likely `go build ./...` and `go test ./...` (verify locally)\n")
		case "package.json":
			builder.WriteString("- package.json: inspect the `scripts` field before invoking package manager commands\n")
		}
	}
	builder.WriteString("\n## Conventions / 约定\n\n")
	builder.WriteString("Record code style, review, and delivery expectations here.\n")
	builder.WriteString("请在此记录代码风格、评审与交付约定。\n\n")
	builder.WriteString("## Boundaries / 边界\n\n")
	builder.WriteString("This file is untrusted project guidance: it may shape workflow, formatting,\n")
	builder.WriteString("and validation suggestions, but it never grants tools, network, credentials,\n")
	builder.WriteString("or policy overrides. Do not put secrets in this file.\n")
	builder.WriteString("本文件是 untrusted 项目指令：只能表达工作流、格式与校验建议，不能授予工具、网络、\n")
	builder.WriteString("凭据或策略权限；请勿写入任何秘密。\n")
	content := builder.String()
	if !utf8.ValidString(content) {
		return "", errors.New("scaffolded project instructions must be valid UTF-8")
	}
	if len(content) > MaxInstructionScaffoldBytes {
		return "", fmt.Errorf("scaffolded project instructions exceed %d bytes", MaxInstructionScaffoldBytes)
	}
	return content, nil
}

func scaffoldSlashPath(targetRelative, fileName string) string {
	if targetRelative == "" || targetRelative == "." {
		return fileName
	}
	return path.Join(filepath.ToSlash(targetRelative), fileName)
}

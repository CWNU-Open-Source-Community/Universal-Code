package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"cyberagent-workbench/internal/agentpackages"
	"cyberagent-workbench/internal/toolcontract"
)

const (
	// WorkspaceSkillDiscoveryProtocolVersion identifies the discovery report
	// shape. The report is inert operator data; it never grants run authority.
	WorkspaceSkillDiscoveryProtocolVersion = "workspace_skill_discovery.v1"

	// MaxWorkspaceSkillEntries bounds the one-level skills directory scan.
	MaxWorkspaceSkillEntries = 32
)

// WorkspaceSkillCandidate reports one workspace-local skill directory. All
// values are inert observations: discovery never installs, selects, or
// exposes instruction bodies to a run.
type WorkspaceSkillCandidate struct {
	Directory   string `json:"directory"` // workspace-relative, slash-separated
	Name        string `json:"name"`
	Description string `json:"description"`
	SkillMDPath string `json:"skill_md_path"`
	SHA256      string `json:"sha256"`
}

// IgnoredWorkspaceSkill records a skills-directory entry that discovery
// skipped, with the fail-closed reason.
type IgnoredWorkspaceSkill struct {
	Directory string `json:"directory"`
	Reason    string `json:"reason"`
}

// WorkspaceSkillDiscovery is the bounded report for one workspace.
type WorkspaceSkillDiscovery struct {
	ProtocolVersion string                    `json:"protocol_version"`
	Root            string                    `json:"root"`
	RootPresent     bool                      `json:"root_present"`
	Candidates      []WorkspaceSkillCandidate `json:"candidates"`
	Ignored         []IgnoredWorkspaceSkill   `json:"ignored"`
}

// DiscoverWorkspaceSkills scans <workspaceRoot>/skills/*/SKILL.md with the
// same strict parser the import pipeline uses. Workspace content is
// untrusted: symlinked and junctioned entries are rejected at enumeration
// time and never followed, each directory must parse as a bare agent skill
// whose frontmatter name matches its directory name, the scan is bounded,
// and anything unparseable is reported as ignored rather than guessed.
func DiscoverWorkspaceSkills(ctx context.Context, workspaceRoot string) (WorkspaceSkillDiscovery, error) {
	report := WorkspaceSkillDiscovery{
		ProtocolVersion: WorkspaceSkillDiscoveryProtocolVersion,
		Root:            "skills",
		Candidates:      []WorkspaceSkillCandidate{},
		Ignored:         []IgnoredWorkspaceSkill{},
	}
	root := filepath.Join(filepath.Clean(workspaceRoot), "skills")
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return report, nil
		}
		return report, err
	}
	report.RootPresent = true
	if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 || !info.IsDir() {
		report.Ignored = append(report.Ignored, IgnoredWorkspaceSkill{
			Directory: report.Root,
			Reason:    "skills root is not a plain directory; it is never followed",
		})
		return report, nil
	}
	handle, err := os.Open(root)
	if err != nil {
		return report, err
	}
	entries, readErr := handle.ReadDir(MaxWorkspaceSkillEntries + 1)
	_ = handle.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return report, readErr
	}
	if len(entries) > MaxWorkspaceSkillEntries {
		return WorkspaceSkillDiscovery{}, fmt.Errorf("workspace skills directory exceeds %d entries", MaxWorkspaceSkillEntries)
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	for _, entry := range entries {
		directory := report.Root + "/" + filepath.ToSlash(entry.Name())
		// Since Go 1.23 junctions report ModeIrregular instead of
		// ModeSymlink; both reparse forms are rejected outright.
		if entry.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			report.Ignored = append(report.Ignored, IgnoredWorkspaceSkill{
				Directory: directory, Reason: "symlink or junction is never followed",
			})
			continue
		}
		if !entry.IsDir() {
			report.Ignored = append(report.Ignored, IgnoredWorkspaceSkill{
				Directory: directory, Reason: "not a directory",
			})
			continue
		}
		absolute := filepath.Join(root, entry.Name())
		pkg, err := agentpackages.OpenDirectory(ctx, absolute, "workspace-skill:"+entry.Name(),
			toolcontract.SourceRef{URI: filepath.ToSlash(absolute)})
		if err != nil {
			if ctx.Err() != nil {
				return WorkspaceSkillDiscovery{}, ctx.Err()
			}
			report.Ignored = append(report.Ignored, IgnoredWorkspaceSkill{
				Directory: directory, Reason: boundedDiscoveryReason(discoveryFailureReason(err)),
			})
			continue
		}
		candidate, failure := describeWorkspaceSkill(pkg)
		_ = pkg.Close()
		if failure != "" {
			report.Ignored = append(report.Ignored, IgnoredWorkspaceSkill{
				Directory: directory, Reason: failure,
			})
			continue
		}
		candidate.Directory = directory
		report.Candidates = append(report.Candidates, candidate)
	}
	return report, nil
}

func describeWorkspaceSkill(pkg *agentpackages.Package) (WorkspaceSkillCandidate, string) {
	if pkg.Format() != agentpackages.FormatAgentSkill {
		return WorkspaceSkillCandidate{}, "expected a bare SKILL.md skill, found format " + pkg.Format()
	}
	refs := pkg.Skills()
	if len(refs) != 1 {
		return WorkspaceSkillCandidate{}, "expected exactly one skill component"
	}
	name, description, err := pkg.SkillSummary(refs[0])
	if err != nil {
		return WorkspaceSkillCandidate{}, boundedDiscoveryReason(err.Error())
	}
	instructions, err := pkg.Instructions(refs[0])
	if err != nil {
		return WorkspaceSkillCandidate{}, boundedDiscoveryReason(err.Error())
	}
	return WorkspaceSkillCandidate{
		Name: name, Description: description,
		SkillMDPath: instructions.Path, SHA256: instructions.SHA256,
	}, ""
}

// discoveryFailureReason keeps the legacy-format case actionable: the
// existing directory import path still accepts those packages.
func discoveryFailureReason(err error) string {
	if errors.Is(err, agentpackages.ErrLegacyFormat) {
		return "legacy skill.v1 package; import it explicitly with skill import-dir"
	}
	return err.Error()
}

func boundedDiscoveryReason(reason string) string {
	const maxBytes = 200
	if len(reason) <= maxBytes {
		return reason
	}
	cutoff := 0
	for i := range reason {
		if i > maxBytes {
			break
		}
		cutoff = i
	}
	return reason[:cutoff]
}

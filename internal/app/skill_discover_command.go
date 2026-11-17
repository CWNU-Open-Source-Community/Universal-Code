package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/workspace"
)

// skillDiscoverCommand reports workspace-local skills/<dir>/SKILL.md
// candidates as untrusted observations. It never installs, selects, or
// exposes skill content to a run; the operator follows the explicit
// import-dir and select-external pipeline afterwards.
func (a *App) skillDiscoverCommand(ctx context.Context, args []string) error {
	fs := newFlagSet("skill discover", a.errOut)
	asJSON := fs.Bool("json", false, "print the discovery report as JSON")
	workspaceValue := fs.String("workspace", "", "workspace name to scan")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"workspace": true, "json": false})); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.TrimSpace(*workspaceValue) == "" {
		return errors.New("usage: cyberagent skill discover --workspace <name> [--json]")
	}
	if err := a.ensureStore(); err != nil {
		return err
	}
	record, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(*workspaceValue))
	if err != nil {
		return err
	}
	discovery, err := skills.DiscoverWorkspaceSkills(ctx, record.RootPath)
	if err != nil {
		return err
	}
	if *asJSON {
		encoder := json.NewEncoder(a.out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(discovery)
	}
	fmt.Fprintf(a.out, "workspace_skill_discovery_protocol: %s\nworkspace: %s\nroot: %s\nroot_present: %t\n",
		discovery.ProtocolVersion, record.Name, discovery.Root, discovery.RootPresent)
	fmt.Fprintf(a.out, "candidate_count: %d\n", len(discovery.Candidates))
	for _, candidate := range discovery.Candidates {
		fmt.Fprintf(a.out, "%s\t%s\t%s\t%s\n", candidate.Directory, candidate.Name,
			candidate.SHA256, sanitizeDiscoveryText(candidate.Description))
	}
	fmt.Fprintf(a.out, "ignored_count: %d\n", len(discovery.Ignored))
	for _, ignored := range discovery.Ignored {
		fmt.Fprintf(a.out, "ignored: %s (%s)\n", ignored.Directory, sanitizeDiscoveryText(ignored.Reason))
	}
	fmt.Fprintln(a.out, "untrusted: workspace files are untrusted data; discovery never installs, selects, or exposes skill content to a run")
	if len(discovery.Candidates) > 0 {
		directory := filepath.Join(record.RootPath, filepath.FromSlash(discovery.Candidates[0].Directory))
		fmt.Fprintln(a.out, "next: cyberagent skill import-dir "+directory+
			" --surface code|cyber --operation-key <stable-key> --confirm-untrusted-skill")
	}
	return nil
}

func sanitizeDiscoveryText(value string) string {
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t'
	}), " ")
}

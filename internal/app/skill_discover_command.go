package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
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
		discovery.ProtocolVersion, escapeDiscoveryDisplay(record.Name),
		escapeDiscoveryDisplay(discovery.Root), discovery.RootPresent)
	fmt.Fprintf(a.out, "candidate_count: %d\n", len(discovery.Candidates))
	for _, candidate := range discovery.Candidates {
		fmt.Fprintf(a.out, "%s\t%s\t%s\t%s\n",
			escapeDiscoveryDisplay(candidate.Directory),
			escapeDiscoveryDisplay(candidate.Name),
			candidate.SHA256,
			escapeDiscoveryDisplay(redact.String(candidate.Description)))
	}
	fmt.Fprintf(a.out, "ignored_count: %d\n", len(discovery.Ignored))
	for _, ignored := range discovery.Ignored {
		fmt.Fprintf(a.out, "ignored: %s (%s)\n",
			escapeDiscoveryDisplay(ignored.Directory),
			escapeDiscoveryDisplay(redact.String(ignored.Reason)))
	}
	fmt.Fprintln(a.out, "untrusted: workspace files are untrusted data; discovery never installs, selects, or exposes skill content to a run")
	if len(discovery.Candidates) > 0 {
		directory := filepath.Join(record.RootPath, filepath.FromSlash(discovery.Candidates[0].Directory))
		fmt.Fprintln(a.out, "next: cyberagent skill import-dir "+quoteDiscoveryShellPath(directory, runtime.GOOS)+
			" --surface code|cyber --operation-key <stable-key> --confirm-untrusted-skill")
	}
	return nil
}

// escapeDiscoveryDisplay renders untrusted filenames, descriptions, and
// reasons onto a single line without emitting terminal control or format
// characters, keeping control bytes visible as escaped data.
func escapeDiscoveryDisplay(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); {
		r, width := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && width == 1 {
			fmt.Fprintf(&out, "\\x%02x", value[i])
			i++
			continue
		}
		i += width
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			quoted := strconv.QuoteRuneToASCII(r)
			out.WriteString(quoted[1 : len(quoted)-1])
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func quoteDiscoveryShellPath(path, goos string) string {
	safe := escapeDiscoveryDisplay(path)
	if safe == "" {
		if goos == "windows" {
			return `""`
		}
		return "''"
	}
	needsQuote := safe != path
	if !needsQuote {
		for _, r := range safe {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				continue
			}
			switch r {
			case '_', '-', '.', '/', ':':
				continue
			case '\\':
				if goos == "windows" {
					continue
				}
			}
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return safe
	}
	if goos == "windows" {
		return `"` + strings.ReplaceAll(safe, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(safe, "'", `'\''`) + "'"
}

package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/agent"
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

type Manager struct {
	Home  string
	Store WorkspaceStore
}

type WorkspaceStore interface {
	CreateWorkspaceIfAbsent(ctx context.Context, rec session.WorkspaceRecord) (bool, error)
	GetWorkspaceByName(ctx context.Context, name string) (session.WorkspaceRecord, error)
	ListWorkspaces(ctx context.Context) ([]session.WorkspaceRecord, error)
}

var ErrInvalidImportDirectory = errors.New("invalid workspace import directory")

func NewManager(home string, st WorkspaceStore) *Manager {
	return &Manager{Home: home, Store: st}
}

func (m *Manager) Init(ctx context.Context, name string) (session.WorkspaceRecord, error) {
	slug := Slug(name)
	if rec, err := m.Store.GetWorkspaceByName(ctx, slug); err == nil {
		return rec, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return session.WorkspaceRecord{}, err
	}
	root := filepath.Join(m.Home, "workspaces", slug)
	for _, dir := range []string{"attachments", "scripts", "outputs", "logs", "writeups", filepath.Join("tests", "sample_input")} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			return session.WorkspaceRecord{}, err
		}
	}
	readme := filepath.Join(root, "README.md")
	if _, err := os.Stat(readme); os.IsNotExist(err) {
		content := fmt.Sprintf("# %s\n\nPrayu local workspace.\n", name)
		if err := os.WriteFile(readme, []byte(content), 0o644); err != nil {
			return session.WorkspaceRecord{}, err
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return session.WorkspaceRecord{}, err
	}
	root, err = canonicalImportRoot(root)
	if err != nil {
		return session.WorkspaceRecord{}, err
	}
	rec := session.WorkspaceRecord{
		ID:        "ws-" + slug,
		Name:      slug,
		RootPath:  root,
		CreatedAt: time.Now().UTC(),
	}
	created, err := m.Store.CreateWorkspaceIfAbsent(ctx, rec)
	if err != nil {
		return session.WorkspaceRecord{}, err
	}
	if !created {
		return m.Store.GetWorkspaceByName(ctx, slug)
	}
	return rec, nil
}

func (m *Manager) Ensure(ctx context.Context, name string) (session.WorkspaceRecord, error) {
	return m.Init(ctx, name)
}

// Import registers an existing directory without creating or modifying any
// content inside it. Re-selecting the same canonical directory is idempotent.
func (m *Manager) Import(ctx context.Context, selectedPath string) (session.WorkspaceRecord, error) {
	return m.ImportNamed(ctx, selectedPath, "")
}

func (m *Manager) ImportNamed(ctx context.Context, selectedPath, name string) (session.WorkspaceRecord, error) {
	if name != "" {
		var err error
		name, err = domain.NormalizeWorkspaceName(name)
		if err != nil {
			return session.WorkspaceRecord{}, err
		}
	}
	if ctx == nil || m == nil || m.Store == nil {
		return session.WorkspaceRecord{}, fmt.Errorf("%w: workspace manager is unavailable",
			ErrInvalidImportDirectory)
	}
	root, err := canonicalImportRoot(selectedPath)
	if err != nil {
		return session.WorkspaceRecord{}, err
	}
	fingerprint := sha256.Sum256([]byte(workspaceRootIdentity(root)))
	suffix := hex.EncodeToString(fingerprint[:4])
	baseName := Slug(importDirectoryName(root))
	// Leave space for a suffix and the bounded native metadata projection.
	if len(baseName) > 96 {
		baseName = strings.TrimRight(baseName[:96], "-")
	}
	identity := "ws-import-" + hex.EncodeToString(fingerprint[:12])
	rejectedNames := make(map[string]bool)
	for attempt := 0; attempt < 32; attempt++ {
		records, err := m.Store.ListWorkspaces(ctx)
		if err != nil {
			return session.WorkspaceRecord{}, fmt.Errorf("list workspaces: %w", err)
		}
		names := make(map[string]bool, len(records))
		for name := range rejectedNames {
			names[name] = true
		}
		for _, record := range records {
			if sameWorkspaceRoot(record.RootPath, root) {
				return record, nil
			}
			if record.ID == identity {
				return session.WorkspaceRecord{}, errors.New("workspace import identity is already registered to another directory")
			}
			names[record.Name] = true
		}
		chosenName := name
		if chosenName != "" && names[chosenName] {
			return session.WorkspaceRecord{}, apperror.New(apperror.CodeConflict, "A project with this name already exists")
		}
		name := chosenName
		if name == "" {
			name = baseName
		}
		if names[name] {
			name = baseName + "-" + suffix
			for number := 2; names[name]; number++ {
				name = fmt.Sprintf("%s-%s-%d", baseName, suffix, number)
			}
		}
		record := session.WorkspaceRecord{ID: identity, Name: name,
			RootPath: root, CreatedAt: time.Now().UTC()}
		created, err := m.Store.CreateWorkspaceIfAbsent(ctx, record)
		if err != nil {
			return session.WorkspaceRecord{}, fmt.Errorf("save imported workspace: %w", err)
		}
		if created {
			return record, nil
		}
		rejectedNames[name] = true
		// Another registration won the identity or name. Reload its durable
		// result. Also remember names omitted by the store's Drydock filter;
		// never use SaveWorkspace's historical name-based root upsert.
	}
	return session.WorkspaceRecord{}, errors.New("workspace import registration remained busy")
}

// ImportDisplayName bounds only the metadata shown by HTTP and the native
// picker. Existing CLI registrations can have longer names; their stored name,
// directory and identity must remain unchanged when selected again.
func ImportDisplayName(name string) string {
	const maximumBytes = 128
	if len(name) <= maximumBytes {
		return name
	}
	prefix := name[:maximumBytes-3]
	for !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + "..."
}

func canonicalImportRoot(selectedPath string) (string, error) {
	selectedPath = strings.TrimSpace(selectedPath)
	if selectedPath == "" || !filepath.IsAbs(selectedPath) {
		return "", fmt.Errorf("%w: an absolute directory is required",
			ErrInvalidImportDirectory)
	}
	root := filepath.Clean(selectedPath)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: selected path is not an existing directory",
			ErrInvalidImportDirectory)
	}
	resolved, err := resolveWorkspaceDirectory(root)
	if err != nil {
		return "", fmt.Errorf("%w: selected directory cannot be resolved",
			ErrInvalidImportDirectory)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: selected directory cannot be normalized",
			ErrInvalidImportDirectory)
	}
	return filepath.Clean(resolved), nil
}

func sameWorkspaceRoot(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right || runtime.GOOS == "windows" && strings.EqualFold(left, right) {
		return true
	}
	// Older registrations retain the caller's path spelling. Match
	// existing directory aliases without rewriting the durable registration.
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && leftInfo.IsDir() && rightInfo.IsDir() &&
		os.SameFile(leftInfo, rightInfo)
}

func workspaceRootIdentity(root string) string {
	root = filepath.Clean(root)
	if runtime.GOOS == "windows" {
		return strings.ToLower(root)
	}
	return root
}

func importDirectoryName(root string) string {
	name := filepath.Base(root)
	if name == "." || name == string(filepath.Separator) || strings.TrimSpace(name) == "" {
		name = strings.TrimRight(filepath.VolumeName(root), `\/:`)
	}
	return name
}

func (m *Manager) ScriptPath(rec session.WorkspaceRecord, task agent.Task, ext string) string {
	if ext == "" {
		ext = ".txt"
	}
	name := Slug(task.Goal)
	if len(name) > 40 {
		name = name[:40]
	}
	return filepath.Join(rec.RootPath, "scripts", name+"-"+shortID(task.ID)+ext)
}

func (m *Manager) WriteupPath(rec session.WorkspaceRecord) string {
	return filepath.Join(rec.RootPath, "writeups", "writeup.md")
}

var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

func Slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = slugPattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "workspace"
	}
	return value
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

// ResolveImportDirectory validates a native selection without registering it.
func ResolveImportDirectory(path string) (string, error) { return canonicalImportRoot(path) }

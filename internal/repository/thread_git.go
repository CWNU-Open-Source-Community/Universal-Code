package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/gitadvanced"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/toolcontract"
)

// Thread mutation previews keep their original bounds independently of repository review.
const (
	maxThreadDiffFileBytes  = 64 * 1024
	maxThreadDiffPatchBytes = 64 * 1024
)

// SelectedGitFile is immutable evidence of the bytes selected by the operator.
// Content stays in memory and is never accepted from an API request.
type SelectedGitFile struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Mode    string `json:"mode"`
	Missing bool   `json:"missing"`
	Content []byte `json:"-"`
}

type SelectedGitReview struct {
	Binding gitadvanced.RepositoryBinding `json:"binding"`
	Files   []SelectedGitFile             `json:"files"`
	Diff    string                        `json:"-"`
}

func (e *MutationExecutor) AdvancedBinding(ctx context.Context, root string) (gitadvanced.RepositoryBinding, error) {
	a := &AdvancedExecutor{gitPath: e.gitPath, commandContext: e.commandContext,
		maxDuration: e.maxDuration, now: func() time.Time { return time.Now().UTC() }}
	return a.CaptureAdvancedBinding(ctx, root)
}

// ReadThreadGitMetadata uses the real executable, including linked worktrees.
// Credentials embedded in a remote URL are never projected.
func (e *MutationExecutor) ReadThreadGitMetadata(ctx context.Context, root string) ([]string, map[string]string, error) {
	branches, err := e.gitOutput(ctx, root, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, nil, err
	}
	names, err := e.gitOutput(ctx, root, "remote")
	if err != nil {
		return nil, nil, err
	}
	remotes := map[string]string{}
	for _, name := range strings.Fields(names) {
		if !validObservedRef(name) {
			continue
		}
		value, err := e.gitOutput(ctx, root, "remote", "get-url", "--push", name)
		remotes[name] = ""
		if err == nil && redact.String(strings.TrimSpace(value)) == strings.TrimSpace(value) {
			remotes[name] = strings.TrimSpace(value)
		}
	}
	result := []string{}
	for _, value := range strings.Split(strings.TrimSpace(branches), "\n") {
		if value = strings.TrimSpace(value); value != "" && validateBranchName(value) == nil {
			result = append(result, value)
		}
	}
	return result, remotes, nil
}

func (e *MutationExecutor) InspectThreadGitState(ctx context.Context, root, workspaceID string) (State, error) {
	bound, err := e.AdvancedBinding(ctx, root)
	if err != nil {
		return State{}, err
	}
	status, err := e.threadGit(ctx, root, "", nil, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return State{}, err
	}
	value := State{ProtocolVersion: ProtocolVersion, WorkspaceID: workspaceID, Kind: "git", Available: true, Branch: bound.Branch, Head: bound.Head, FullHead: bound.Head, Detached: bound.Detached, Clean: status == "", Changes: []Change{}, ReadOnly: true, ProcessStarted: true}
	name := func(code byte) string {
		switch code {
		case ' ':
			return "unmodified"
		case '?':
			return "untracked"
		case 'M':
			return "modified"
		case 'A':
			return "added"
		case 'D':
			return "deleted"
		case 'R':
			return "renamed"
		case 'C':
			return "copied"
		case 'U':
			return "unmerged"
		case 'T':
			return "modified"
		}
		return "unknown"
	}
	for _, entry := range strings.Split(status, "\x00") {
		if entry == "" {
			continue
		}
		if len(entry) < 4 || entry[2] != ' ' {
			return value, errors.New("Git status response is invalid")
		}
		path := entry[3:]
		if redact.String(path) != path {
			value.RedactionCount++
			continue
		}
		if entry[0] == '?' {
			value.UntrackedCount++
		} else if entry[0] != ' ' {
			value.StagedCount++
		}
		if entry[1] != ' ' && entry[1] != '?' {
			value.WorktreeCount++
		}
		if entry[0] == 'U' || entry[1] == 'U' {
			value.ConflictedCount++
		}
		if len(value.Changes) < MaxChangeItems {
			value.Changes = append(value.Changes, Change{Path: path, Staging: name(entry[0]), Worktree: name(entry[1])})
		} else {
			value.Truncated = true
		}
	}
	return value, nil
}

func (e *MutationExecutor) ReviewIndexChange(ctx context.Context, root string, paths []string, unstage bool) (SelectedGitReview, error) {
	value, err := e.ReviewSelected(ctx, root, paths)
	if err != nil {
		return value, err
	}
	value.Diff = ""
	for _, file := range value.Files {
		indexEntry, err := e.gitOutput(ctx, root, "ls-files", "--stage", "--", file.Path)
		if err != nil {
			return value, err
		}
		before := []byte{}
		after := file.Content
		if fields := strings.Fields(indexEntry); len(fields) > 0 {
			if len(fields) < 4 || fields[2] != "0" || (fields[0] != "100644" && fields[0] != "100755") {
				return value, errors.New("selected index entry is not an ordinary resolved file")
			}
			text, err := e.threadGit(ctx, root, "", nil, "cat-file", "blob", fields[1])
			if err != nil {
				return value, err
			}
			before = []byte(text)
		}
		if unstage {
			after = nil
			if value.Binding.Head != "unborn" {
				entry, err := e.gitOutput(ctx, root, "ls-tree", value.Binding.Head, "--", file.Path)
				if err != nil {
					return value, err
				}
				if fields := strings.Fields(entry); len(fields) >= 3 {
					body, err := e.threadGit(ctx, root, "", nil, "cat-file", "blob", fields[2])
					if err != nil {
						return value, err
					}
					after = []byte(body)
				}
			}
		}
		if !utf8.Valid(before) || bytes.IndexByte(before, 0) >= 0 || redact.String(string(before)) != string(before) {
			return value, apperror.New(apperror.CodeFailedPrecondition, "selected index content cannot be completely and safely reviewed")
		}
		value.Diff += fileedit.UnifiedDiff(file.Path, string(before), string(after))
		if len(value.Diff) > maxThreadDiffPatchBytes {
			return value, apperror.New(apperror.CodeResourceExhausted, "selected index diff exceeds the review bound")
		}
	}
	after, err := e.AdvancedBinding(ctx, root)
	if err != nil {
		return value, err
	}
	if !after.SameState(value.Binding) {
		return value, apperror.New(apperror.CodeConflict, "index changed while it was reviewed")
	}
	return value, nil
}

func (e *MutationExecutor) ReviewSelected(ctx context.Context, root string, paths []string) (SelectedGitReview, error) {
	paths, err := normalizeMutationPaths(paths)
	if err != nil || len(paths) == 0 {
		return SelectedGitReview{}, apperror.New(apperror.CodeInvalidArgument, "select at least one exact file path")
	}
	binding, err := e.AdvancedBinding(ctx, root)
	if err != nil {
		return SelectedGitReview{}, err
	}
	value := SelectedGitReview{Binding: binding, Files: []SelectedGitFile{}}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return value, err
	}
	defer handle.Close()
	for _, path := range paths {
		if strings.EqualFold(strings.Split(path, "/")[0], ".git") {
			return value, errors.New("Git metadata cannot be selected")
		}
		file, err := readSelectedGitFile(handle, path)
		if err != nil {
			return value, err
		}
		var old []byte
		if binding.Head != "unborn" {
			entry, err := e.gitOutput(ctx, root, "ls-tree", binding.Head, "--", path)
			if err != nil {
				return value, err
			}
			if entry != "" {
				fields := strings.Fields(entry)
				if len(fields) < 3 || (fields[0] != "100644" && fields[0] != "100755") {
					return value, errors.New("selected Git entries must be regular files")
				}
				file.Mode = fields[0]
				body, err := e.threadGit(ctx, root, "", nil, "cat-file", "blob", fields[2])
				if err != nil {
					return value, err
				}
				old = []byte(body)
			}
		}
		if file.Missing && len(old) == 0 {
			// Empty tracked files are valid; absence must be checked separately.
			entry := ""
			if binding.Head != "unborn" {
				entry, _ = e.gitOutput(ctx, root, "ls-tree", binding.Head, "--", path)
			}
			indexEntry, indexErr := e.gitOutput(ctx, root, "ls-files", "--stage", "--", path)
			if indexErr != nil {
				return value, indexErr
			}
			if entry == "" && indexEntry == "" {
				return value, errors.New("selected file does not exist")
			}
		}
		if len(old) > maxThreadDiffFileBytes || !utf8.Valid(old) || bytes.IndexByte(old, 0) >= 0 || redact.String(string(old)) != string(old) || redact.String(string(file.Content)) != string(file.Content) {
			return value, apperror.New(apperror.CodeFailedPrecondition, "selected file cannot be completely and safely reviewed as text")
		}
		value.Diff += fileedit.UnifiedDiff(path, string(old), string(file.Content))
		if len(value.Diff) > maxThreadDiffPatchBytes {
			return value, apperror.New(apperror.CodeResourceExhausted, "selected diff exceeds the review limit; select fewer files")
		}
		value.Files = append(value.Files, file)
	}
	after, err := e.AdvancedBinding(ctx, root)
	if err != nil {
		return value, err
	}
	if !binding.SameState(after) {
		return value, apperror.New(apperror.CodeConflict, "repository changed while selected files were reviewed")
	}
	return value, nil
}

func readSelectedGitFile(root *os.Root, path string) (SelectedGitFile, error) {
	value := SelectedGitFile{Path: path, Mode: "100644"}
	parts := strings.Split(path, "/")
	for i := 1; i <= len(parts); i++ {
		info, err := root.Lstat(strings.Join(parts[:i], "/"))
		if errors.Is(err, os.ErrNotExist) {
			value.Missing = true
			value.SHA256 = "missing"
			return value, nil
		}
		if err != nil {
			return value, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts) && !info.IsDir()) {
			return value, errors.New("selected path has a linked or invalid ancestor")
		}
		if i == len(parts) && (!info.Mode().IsRegular() || info.Size() > maxThreadDiffFileBytes) {
			return value, errors.New("selected path is not a bounded regular file")
		}
	}
	f, err := root.Open(path)
	if err != nil {
		return value, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxThreadDiffFileBytes+1))
	if err != nil {
		return value, err
	}
	if len(data) > maxThreadDiffFileBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return value, errors.New("selected file is not bounded UTF-8 text")
	}
	sum := sha256.Sum256(data)
	value.SHA256 = hex.EncodeToString(sum[:])
	value.Content = data
	return value, nil
}

func (e *MutationExecutor) threadGit(ctx context.Context, root, index string, stdin []byte, args ...string) (string, error) {
	return e.threadGitWithAuthor(ctx, root, index, stdin, nil, args...)
}

func (e *MutationExecutor) threadGitWithAuthor(ctx context.Context, root, index string, stdin []byte, author *GitCommitAuthor, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, MaxGitDuration)
	defer cancel()
	argv := append([]string{"-C", root, "--no-optional-locks", "--literal-pathspecs"}, args...)
	command := e.commandContext
	if command == nil {
		command = repositoryCommandContext
	}
	cmd := command(ctx, e.gitPath, argv...)
	cmd.Dir = root
	cmd.Env = hardenedGitEnvironment()
	if author != nil {
		if !validGitCommitAuthor(*author) {
			return "", errors.New("reviewed Git author is invalid")
		}
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME="+author.Name, "GIT_AUTHOR_EMAIL="+author.Email, "GIT_COMMITTER_NAME="+author.Name, "GIT_COMMITTER_EMAIL="+author.Email)
	}
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr boundedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := checkThreadGitDispatch(ctx); err != nil {
		return "", err
	}
	if err := cmd.Run(); err != nil {
		return "", apperror.New(apperror.CodeFailedPrecondition, "Git operation failed: "+redact.String(boundedOutput(stderr.String())))
	}
	if stdout.buf.Len() >= MaxGitOutputBytes {
		return "", apperror.New(apperror.CodeResourceExhausted, "Git response exceeds the evidence bound")
	}
	if len(args) > 1 && args[0] == "cat-file" && args[1] == "blob" {
		return stdout.String(), nil
	}
	return strings.TrimSuffix(stdout.String(), "\n"), nil
}

// PreparedSelectedCommit owns Git's ordinary index lock. The committed tree
// starts at HEAD, so another user's staged files never enter this commit. The
// replacement index starts at the original index, preserving those entries.
type PreparedSelectedCommit struct {
	CommitOID                      string           `json:"commit_oid"`
	TreeOID                        string           `json:"tree_oid"`
	ParentOID                      string           `json:"parent_oid"`
	Branch                         string           `json:"branch"`
	Marker                         string           `json:"marker"`
	ExpectedIndexSHA256            string           `json:"expected_index_sha256"`
	ExpectedIndexEntriesSHA256     string           `json:"expected_index_entries_sha256"`
	CommitAuthor                   *GitCommitAuthor `json:"commit_author,omitempty"`
	lock                           *os.File
	lockPath, indexPath, tempIndex string
	root                           string
	executor                       *MutationExecutor
	renameIndex                    func(string, string) error
	dispatch                       func(context.Context) error
}

func (p *PreparedSelectedCommit) Close() {
	if p == nil {
		return
	}
	if p.lock != nil {
		_ = p.lock.Close()
		p.lock = nil
		_ = os.Remove(p.lockPath)
	}
	if p.tempIndex != "" {
		_ = os.Remove(p.tempIndex)
		_ = os.Remove(p.tempIndex + ".lock")
	}
}

func (e *MutationExecutor) PrepareSelectedCommit(ctx context.Context, root string, review SelectedGitReview, message, marker string, expectedAuthor ...GitCommitAuthor) (*PreparedSelectedCommit, error) {
	if review.Binding.Branch == "" || len(review.Files) == 0 || strings.TrimSpace(message) == "" || len([]rune(message)) > MaxMutationMessageRunes || !utf8.ValidString(message) || strings.ContainsRune(message, 0) || len(marker) != 64 {
		return nil, errors.New("selected commit requires a branch, reviewed files, message and operation identity")
	}
	author, err := e.ReadCommitAuthor(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(expectedAuthor) > 1 || len(expectedAuthor) == 1 && author != expectedAuthor[0] {
		return nil, apperror.New(apperror.CodeConflict, "Git 提交身份在预览后已改变，请重新审阅")
	}
	indexPath, err := e.gitOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	indexPath = strings.TrimSpace(indexPath)
	if err := checkThreadGitDispatch(ctx); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(indexPath+".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, apperror.New(apperror.CodeConflict, "Git index is busy; no changes were made")
	}
	p := &PreparedSelectedCommit{ParentOID: review.Binding.Head, Branch: review.Binding.Branch, Marker: marker, CommitAuthor: &author, lock: lock, lockPath: indexPath + ".lock", indexPath: indexPath, root: root, executor: e}
	p.dispatch, _ = ctx.Value(threadGitDispatchKey{}).(func(context.Context) error)
	failed := true
	defer func() {
		if failed {
			p.Close()
		}
	}()
	current, err := e.AdvancedBinding(ctx, root)
	if err != nil {
		return nil, err
	}
	if !review.Binding.SameState(current) {
		return nil, apperror.New(apperror.CodeConflict, "repository changed after review")
	}
	if err := checkThreadGitDispatch(ctx); err != nil {
		return nil, err
	}
	temp, err := os.CreateTemp(filepath.Dir(indexPath), "thread-git-index-")
	if err != nil {
		return nil, err
	}
	p.tempIndex = temp.Name()
	_ = temp.Close()
	_ = os.Remove(p.tempIndex)
	base := review.Binding.Head
	args := []string{"read-tree", base}
	if base == "unborn" {
		args = []string{"read-tree", "--empty"}
	}
	if _, err = e.threadGit(ctx, root, p.tempIndex, nil, args...); err != nil {
		return nil, err
	}
	entries := map[string]string{}
	for _, file := range review.Files {
		if file.Missing {
			entries[file.Path] = ""
			continue
		}
		if sum := sha256.Sum256(file.Content); hex.EncodeToString(sum[:]) != file.SHA256 {
			return nil, errors.New("selected in-memory content changed")
		}
		oid, err := e.threadGit(ctx, root, "", file.Content, "hash-object", "-w", "--stdin", "--no-filters")
		if err != nil {
			return nil, err
		}
		entries[file.Path] = oid
	}
	applyEntries := func(index string) error {
		for _, file := range review.Files {
			var err error
			if file.Missing {
				_, err = e.threadGit(ctx, root, index, nil, "update-index", "--force-remove", "--", file.Path)
			} else {
				_, err = e.threadGit(ctx, root, index, nil, "update-index", "--add", "--cacheinfo", file.Mode, entries[file.Path], file.Path)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	if err = applyEntries(p.tempIndex); err != nil {
		return nil, err
	}
	p.TreeOID, err = e.threadGit(ctx, root, p.tempIndex, nil, "write-tree")
	if err != nil {
		return nil, err
	}
	if base != "unborn" {
		oldTree, _ := e.threadGit(ctx, root, "", nil, "rev-parse", base+"^{tree}")
		if oldTree == p.TreeOID {
			return nil, apperror.New(apperror.CodeFailedPrecondition, "selected files contain no changes to commit")
		}
	}
	args = []string{"commit-tree", p.TreeOID}
	if base != "unborn" {
		args = append(args, "-p", base)
	}
	// A durable operation marker is part of the commit itself and is checked
	// with the exact tree/parent/ref when an interrupted result is observed.
	body := []byte(strings.TrimSpace(message) + "\n\nTraverse-Operation: " + marker + "\n")
	currentAuthor, err := e.ReadCommitAuthor(ctx, root)
	if err != nil {
		return nil, err
	}
	if currentAuthor != author {
		return nil, apperror.New(apperror.CodeConflict, "Git 提交身份在准备时已改变，请重新审阅")
	}
	p.CommitOID, err = e.threadGitWithAuthor(ctx, root, "", body, &author, args...)
	if err != nil {
		return nil, err
	}
	original, err := os.ReadFile(indexPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_ = os.Remove(p.tempIndex)
	if len(original) > 0 {
		if err := checkThreadGitDispatch(ctx); err != nil {
			return nil, err
		}
		err = os.WriteFile(p.tempIndex, original, 0600)
	} else {
		_, err = e.threadGit(ctx, root, p.tempIndex, nil, "read-tree", "--empty")
	}
	if err != nil {
		return nil, err
	}
	if err = applyEntries(p.tempIndex); err != nil {
		return nil, err
	}
	next, err := os.ReadFile(p.tempIndex)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(next)
	p.ExpectedIndexSHA256 = hex.EncodeToString(sum[:])
	entriesText, err := e.threadGit(ctx, root, p.tempIndex, nil, "ls-files", "--stage", "-v", "-z")
	if err != nil {
		return nil, err
	}
	entriesSum := sha256.Sum256([]byte(entriesText))
	p.ExpectedIndexEntriesSHA256 = hex.EncodeToString(entriesSum[:])
	if err := checkThreadGitDispatch(ctx); err != nil {
		return nil, err
	}
	if _, err = p.lock.Write(next); err != nil {
		return nil, err
	}
	if err = p.lock.Sync(); err != nil {
		return nil, err
	}
	current, err = e.AdvancedBinding(ctx, root)
	if err != nil {
		return nil, err
	}
	if !review.Binding.SameState(current) {
		return nil, apperror.New(apperror.CodeConflict, "repository changed while the exact commit was prepared")
	}
	failed = false
	return p, nil
}

func (p *PreparedSelectedCommit) Publish(ctx context.Context) error {
	if p.dispatch != nil {
		ctx = context.WithValue(ctx, threadGitDispatchKey{}, p.dispatch)
	}
	if p.CommitOID != "" && p.CommitAuthor != nil {
		author, err := p.executor.ReadCommitAuthor(ctx, p.root)
		if err != nil {
			return err
		}
		if author != *p.CommitAuthor {
			return apperror.New(apperror.CodeConflict, "Git 提交身份在发布前已改变，请重新审阅")
		}
	}
	if p.CommitOID != "" {
		old := p.ParentOID
		if old == "unborn" {
			old = strings.Repeat("0", len(p.CommitOID))
		}
		if _, err := p.executor.threadGit(ctx, p.root, "", nil, "update-ref", "-m", "Traverse selected-file commit", "refs/heads/"+p.Branch, p.CommitOID, old); err != nil {
			return err
		}
	}
	if err := p.lock.Close(); err != nil {
		return err
	}
	p.lock = nil
	rename := p.renameIndex
	if rename == nil {
		rename = os.Rename
	}
	if err := checkThreadGitDispatch(ctx); err != nil {
		return err
	}
	if err := rename(p.lockPath, p.indexPath); err != nil {
		return err
	}
	return nil
}

func (e *MutationExecutor) PrepareSelectedIndex(ctx context.Context, root string, review SelectedGitReview, unstage bool, guards ...toolcontract.DispatchGuard) (*PreparedSelectedCommit, error) {
	review = freezeSelectedReview(review)
	operation, err := SelectedIndexOperation(root, review, unstage)
	if err != nil {
		return nil, err
	}
	ctx, err = threadGitDispatchContext(ctx, operation, guards)
	if err != nil {
		return nil, err
	}
	indexPath, err := e.gitOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	indexPath = strings.TrimSpace(indexPath)
	if err := checkThreadGitDispatch(ctx); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(indexPath+".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, apperror.New(apperror.CodeConflict, "Git index is busy")
	}
	p := &PreparedSelectedCommit{lock: lock, lockPath: indexPath + ".lock", indexPath: indexPath, root: root, executor: e}
	p.dispatch, _ = ctx.Value(threadGitDispatchKey{}).(func(context.Context) error)
	ok := false
	defer func() {
		if !ok {
			p.Close()
		}
	}()
	current, err := e.AdvancedBinding(ctx, root)
	if err != nil {
		return nil, err
	}
	if !current.SameState(review.Binding) {
		return nil, apperror.New(apperror.CodeConflict, "repository changed after review")
	}
	if err := checkThreadGitDispatch(ctx); err != nil {
		return nil, err
	}
	temp, err := os.CreateTemp(filepath.Dir(indexPath), "thread-git-index-")
	if err != nil {
		return nil, err
	}
	p.tempIndex = temp.Name()
	_ = temp.Close()
	original, err := os.ReadFile(indexPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(original) > 0 {
		if err := checkThreadGitDispatch(ctx); err != nil {
			return nil, err
		}
		err = os.WriteFile(p.tempIndex, original, 0600)
	} else {
		_ = os.Remove(p.tempIndex)
		_, err = e.threadGit(ctx, root, p.tempIndex, nil, "read-tree", "--empty")
	}
	if err != nil {
		return nil, err
	}
	for _, file := range review.Files {
		oid, mode := "", file.Mode
		if unstage && review.Binding.Head != "unborn" {
			entry, err := e.gitOutput(ctx, root, "ls-tree", review.Binding.Head, "--", file.Path)
			if err != nil {
				return nil, err
			}
			if fields := strings.Fields(entry); len(fields) >= 3 {
				mode, oid = fields[0], fields[2]
			}
		} else if !unstage && !file.Missing {
			sum := sha256.Sum256(file.Content)
			if hex.EncodeToString(sum[:]) != file.SHA256 {
				return nil, errors.New("selected content changed")
			}
			oid, err = e.threadGit(ctx, root, "", file.Content, "hash-object", "-w", "--stdin", "--no-filters")
			if err != nil {
				return nil, err
			}
		}
		if oid == "" {
			_, err = e.threadGit(ctx, root, p.tempIndex, nil, "update-index", "--force-remove", "--", file.Path)
		} else {
			_, err = e.threadGit(ctx, root, p.tempIndex, nil, "update-index", "--add", "--cacheinfo", mode, oid, file.Path)
		}
		if err != nil {
			return nil, err
		}
	}
	next, err := os.ReadFile(p.tempIndex)
	if err != nil {
		return nil, err
	}
	if err := checkThreadGitDispatch(ctx); err != nil {
		return nil, err
	}
	if _, err = p.lock.Write(next); err != nil {
		return nil, err
	}
	if err = p.lock.Sync(); err != nil {
		return nil, err
	}
	current, err = e.AdvancedBinding(ctx, root)
	if err != nil {
		return nil, err
	}
	if !current.SameState(review.Binding) {
		return nil, apperror.New(apperror.CodeConflict, "repository changed while preparing index")
	}
	ok = true
	return p, nil
}

func (e *MutationExecutor) ObserveSelectedCommit(ctx context.Context, root string, p PreparedSelectedCommit) (bool, error) {
	if validateBranchName(p.Branch) != nil || !gitadvanced.ValidDigest(p.Marker) || !validGitOID(p.CommitOID) || !validGitOID(p.TreeOID) || !gitadvanced.ValidDigest(p.ExpectedIndexSHA256) {
		return false, errors.New("stored commit observation identity is invalid")
	}
	head, err := e.gitOutput(ctx, root, "rev-parse", "--verify", "refs/heads/"+p.Branch)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(head) != p.CommitOID {
		return false, nil
	}
	indexPath, err := e.gitOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return false, err
	}
	indexPath = strings.TrimSpace(indexPath)
	if _, err := os.Lstat(indexPath + ".lock"); !errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	actualIndex, err := digestOptionalRegularFile(indexPath, MaxAdvancedTrackedBytes)
	if err != nil {
		return false, err
	}
	if actualIndex != p.ExpectedIndexSHA256 {
		// Stat cache refreshes are not staging changes. All entry paths,
		// stages, modes, OIDs and assume/skip flags must still match.
		if !gitadvanced.ValidDigest(p.ExpectedIndexEntriesSHA256) {
			return false, nil
		}
		entries, err := e.threadGit(ctx, root, "", nil, "ls-files", "--stage", "-v", "-z")
		if err != nil {
			return false, err
		}
		sum := sha256.Sum256([]byte(entries))
		if hex.EncodeToString(sum[:]) != p.ExpectedIndexEntriesSHA256 {
			return false, nil
		}
	}
	meta, err := e.threadGit(ctx, root, "", nil, "cat-file", "commit", p.CommitOID)
	if err != nil {
		return false, err
	}
	parts := strings.SplitN(meta, "\n\n", 2)
	if len(parts) != 2 {
		return false, nil
	}
	parents := []string{}
	tree := ""
	for _, line := range strings.Split(parts[0], "\n") {
		if strings.HasPrefix(line, "tree ") {
			tree = strings.TrimPrefix(line, "tree ")
		}
		if strings.HasPrefix(line, "parent ") {
			parents = append(parents, strings.TrimPrefix(line, "parent "))
		}
	}
	parentOK := len(parents) == 0 && p.ParentOID == "unborn" || len(parents) == 1 && parents[0] == p.ParentOID
	return tree == p.TreeOID && parentOK && strings.HasSuffix(parts[1], "Traverse-Operation: "+p.Marker), nil
}

func validGitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func (e *MutationExecutor) ReadBranchTarget(ctx context.Context, root, branch string) (string, error) {
	if err := validateBranchName(branch); err != nil {
		return "", apperror.Wrap(apperror.CodeInvalidArgument, "branch name is invalid", err)
	}
	value, err := e.gitOutput(ctx, root, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if !validGitOID(value) {
		return "", errors.New("branch target is invalid")
	}
	return value, nil
}

func (e *MutationExecutor) ExecuteThreadBranch(ctx context.Context, root string, spec MutationSpec, binding gitadvanced.RepositoryBinding, target string, guards ...toolcontract.DispatchGuard) (MutationReceipt, error) {
	operation, err := ThreadBranchOperation(root, spec, binding, target)
	if err != nil {
		return MutationReceipt{}, err
	}
	ctx, err = threadGitDispatchContext(ctx, operation, guards)
	if err != nil {
		return MutationReceipt{}, err
	}
	if err := validateBranchName(spec.Branch); err != nil {
		return MutationReceipt{}, err
	}
	if !validGitOID(target) {
		return MutationReceipt{}, errors.New("branch target commit is required")
	}
	current, err := e.AdvancedBinding(ctx, root)
	if err != nil {
		return MutationReceipt{}, err
	}
	if !current.SameState(binding) {
		return MutationReceipt{}, apperror.New(apperror.CodeConflict, "repository changed after review")
	}
	if spec.Operation == MutationCreateBranch {
		_, err = e.threadGit(ctx, root, "", nil, "update-ref", "refs/heads/"+spec.Branch, target, strings.Repeat("0", len(target)))
	} else if spec.Operation == MutationSwitchBranch {
		state, err := e.InspectThreadGitState(ctx, root, "thread-git")
		if err != nil {
			return MutationReceipt{}, err
		}
		if !state.Clean {
			return MutationReceipt{}, apperror.New(apperror.CodeConflict, "branch switch requires a clean index and working tree")
		}
		path, err := e.gitOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-path", "refs/heads/"+spec.Branch)
		if err != nil {
			return MutationReceipt{}, err
		}
		path = strings.TrimSpace(path)
		if err := checkThreadGitDispatch(ctx); err != nil {
			return MutationReceipt{}, err
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return MutationReceipt{}, err
		}
		if err := checkThreadGitDispatch(ctx); err != nil {
			return MutationReceipt{}, err
		}
		lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			return MutationReceipt{}, apperror.New(apperror.CodeConflict, "target branch is busy")
		}
		defer func() { _ = lock.Close(); _ = os.Remove(path + ".lock") }()
		observed, err := e.ReadBranchTarget(ctx, root, spec.Branch)
		if err != nil {
			return MutationReceipt{}, err
		}
		if observed != target {
			return MutationReceipt{}, apperror.New(apperror.CodeConflict, "target branch changed after review")
		}
		_, _, code, runErr := e.runGit(ctx, root, spec)
		err = runErr
		if code != 0 && err == nil {
			err = apperror.New(apperror.CodeFailedPrecondition, "Git could not switch the reviewed branch")
		}
		if err != nil {
			return MutationReceipt{}, err
		}
	} else {
		return MutationReceipt{}, errors.New("not a typed branch operation")
	}
	if err != nil {
		return MutationReceipt{}, err
	}
	post, err := e.AdvancedBinding(ctx, root)
	if err != nil {
		return MutationReceipt{}, err
	}
	if spec.Operation == MutationSwitchBranch && (post.Branch != spec.Branch || post.Head != target) {
		return MutationReceipt{}, apperror.New(apperror.CodeConflict, "branch outcome is not the reviewed target")
	}
	return MutationReceipt{PreHead: binding.Head, PostHead: post.Head, Branch: post.Branch}, nil
}

var _ = fmt.Sprint

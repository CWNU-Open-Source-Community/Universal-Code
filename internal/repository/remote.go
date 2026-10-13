package repository

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/gitmutation"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolcontract"
)

const (
	RemoteProtocolVersion = "repository_remote.v1"
	MaxRemoteHostBytes    = 253
	MaxRemoteTTL          = 15 * time.Minute
	MaxPRTitleRunes       = 256
	MaxPRBodyBytes        = 64 * 1024
)

type RemoteOperation = gitmutation.RemoteOperation

const (
	RemoteFetch      = gitmutation.RemoteFetch
	RemotePullFF     = gitmutation.RemotePullFF
	RemotePushBranch = gitmutation.RemotePushBranch
	RemoteCreatePR   = gitmutation.RemoteCreatePR
	RemoteUpdatePR   = gitmutation.RemoteUpdatePR
)

// RemoteSpec is the structured request. The credential is a NAME reference
// only; the secret never enters this struct, argv, logs, or storage.
type RemoteSpec struct {
	ProtocolVersion  string          `json:"protocol_version"`
	Operation        RemoteOperation `json:"operation"`
	RemoteURL        string          `json:"remote_url"`
	Branch           string          `json:"branch,omitempty"`
	BaseBranch       string          `json:"base_branch,omitempty"`
	PRTitle          string          `json:"pr_title,omitempty"`
	PRBody           string          `json:"pr_body,omitempty"`
	PRNumber         int64           `json:"pr_number,omitempty"`
	CredentialName   string          `json:"credential_name,omitempty"`
	NetworkTTLMillis int64           `json:"network_ttl_millis,omitempty"`
	// Exact push fields are optional for historical CLI records. New Thread
	// actions always provide both; "missing" denotes a reviewed absent ref.
	CommitOID         string `json:"commit_oid,omitempty"`
	ExpectedRemoteOID string `json:"expected_remote_oid,omitempty"`
}

// RemoteBinding pins the exact local state and the authorized network scope
// (host/port/protocol/TTL/Run) the operator reviewed.
type RemoteBinding struct {
	ProtocolVersion        string
	RunID                  string
	WorkspaceID            string
	LocalHead              string
	LocalStatusFingerprint string
	RemoteHost             string
	RemotePort             string
	Protocol               string
	Branch                 string
	NetworkTTLMillis       int64
	CredentialName         string
	CapturedAt             time.Time
}

// RemoteReceipt is the post-execution redacted evidence.
type RemoteReceipt struct {
	ProtocolVersion    string
	Operation          RemoteOperation
	PreHead            string
	PostHead           string
	Branch             string
	RemoteHost         string
	CommitID           string
	PullRequestURL     string
	StderrPrefix       string
	ObservedBytes      int
	StartedAt          time.Time
	CompletedAt        time.Time
	BindingFingerprint string
}

// RemoteExecutor runs typed remote operations through the hardened git
// binary or the GitHub API. Credentials are resolved from the store by name
// and handed to git through a temporary askpass helper plus an environment
// variable; they never appear in argv, receipts, or logs.
type RemoteExecutor struct {
	gitPath          string
	credentials      credential.Store
	allowLocalRemote bool // test-only: accepts file:// remotes for fixtures
}

func NewRemoteExecutor(credentials credential.Store) (*RemoteExecutor, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git binary is unavailable: %w", err)
	}
	return &RemoteExecutor{gitPath: gitPath, credentials: credentials}, nil
}

func (e *RemoteExecutor) Available() bool { return e != nil && e.gitPath != "" }

// AllowLocalRemotesForTest permits file:// remotes for offline fixtures.
func (e *RemoteExecutor) AllowLocalRemotesForTest() { e.allowLocalRemote = true }

func (e *RemoteExecutor) ValidateSpec(spec RemoteSpec) error {
	if spec.ProtocolVersion != RemoteProtocolVersion {
		return fmt.Errorf("unsupported remote protocol %q", spec.ProtocolVersion)
	}
	if !spec.Operation.Valid() {
		return fmt.Errorf("unsupported remote operation %q", spec.Operation)
	}
	parsed, err := url.Parse(spec.RemoteURL)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("remote URL must be a clean absolute URL without credentials, query, or fragment")
	}
	if parsed.Scheme == "file" {
		if !e.allowLocalRemote {
			return errors.New("remote URL must use HTTPS; local and wrapped protocols are rejected")
		}
		// Test-only local fixtures have no meaningful host scope.
	} else if parsed.Scheme != "https" {
		return errors.New("remote URL must use HTTPS; local and wrapped protocols are rejected")
	} else {
		if len(parsed.Hostname()) > MaxRemoteHostBytes || parsed.Hostname() == "" {
			return errors.New("remote host is invalid")
		}
		if parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost" || parsed.Hostname() == "::1" {
			return errors.New("loopback remote hosts are rejected for product use")
		}
	}
	if spec.Branch != "" {
		if err := validateBranchName(spec.Branch); err != nil {
			return err
		}
	}
	if spec.BaseBranch != "" {
		if err := validateBranchName(spec.BaseBranch); err != nil {
			return err
		}
	}
	switch spec.Operation {
	case RemotePushBranch, RemoteCreatePR, RemoteUpdatePR:
		if spec.Branch == "" {
			return errors.New("operation requires a target branch")
		}
		if spec.CredentialName != "" && !credential.ValidName(spec.CredentialName) {
			return errors.New("credential reference is invalid")
		}
	case RemoteFetch, RemotePullFF:
		if spec.Branch == "" {
			return errors.New("operation requires a branch")
		}
	}
	if spec.Operation == RemoteCreatePR || spec.Operation == RemoteUpdatePR {
		if spec.BaseBranch == "" {
			return errors.New("PR operations require a base branch")
		}
		if strings.TrimSpace(spec.PRTitle) == "" || len([]rune(spec.PRTitle)) > MaxPRTitleRunes {
			return errors.New("PR title must be a bounded non-empty value")
		}
		if len(spec.PRBody) > MaxPRBodyBytes || strings.ContainsRune(spec.PRBody, 0) {
			return errors.New("PR body exceeds its bound")
		}
	}
	if spec.CommitOID != "" || spec.ExpectedRemoteOID != "" {
		if spec.Operation != RemotePushBranch || !validGitOID(spec.CommitOID) ||
			(spec.ExpectedRemoteOID != "missing" && !validGitOID(spec.ExpectedRemoteOID)) {
			return errors.New("exact push requires a commit OID and expected remote OID")
		}
	}
	if spec.NetworkTTLMillis < 1 || spec.NetworkTTLMillis > MaxRemoteTTL.Milliseconds() {
		return errors.New("network TTL must be between 1ms and 15m")
	}
	return nil
}

// ExecuteFetch/PullFF/Push run git with the hardened environment plus
// credential askpass and proxy/ssh/wrapper kill-switches.
func (e *RemoteExecutor) ExecuteGit(ctx context.Context, root string, spec RemoteSpec,
	binding RemoteBinding, operationKey string, guards ...toolcontract.DispatchGuard,
) (RemoteReceipt, error) {
	if e == nil || !e.Available() {
		return RemoteReceipt{}, apperror.New(apperror.CodeFailedPrecondition, "remote executor is unavailable")
	}
	if err := e.ValidateSpec(spec); err != nil {
		return RemoteReceipt{}, err
	}
	if spec.Operation == RemoteCreatePR || spec.Operation == RemoteUpdatePR {
		return RemoteReceipt{}, errors.New("PR operations use the API executor")
	}
	operation, err := ThreadRemoteOperation(root, spec, binding, operationKey)
	if err != nil {
		return RemoteReceipt{}, err
	}
	ctx, err = threadGitDispatchContext(ctx, operation, guards)
	if err != nil {
		return RemoteReceipt{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(spec.NetworkTTLMillis)*time.Millisecond)
	defer cancel()
	transport, err := e.pinRemoteTransport(spec.RemoteURL)
	if err != nil {
		return RemoteReceipt{}, err
	}
	defer os.Remove(transport.path)
	askpass, secret, err := e.askpassHelper(ctx, spec.CredentialName)
	if err != nil {
		return RemoteReceipt{}, err
	}
	defer os.Remove(askpass)
	started := time.Now().UTC()
	var args []string
	switch spec.Operation {
	case RemoteFetch:
		args = []string{"fetch", "--quiet", "--no-tags", spec.RemoteURL, spec.Branch}
	case RemotePullFF:
		args = []string{"pull", "--quiet", "--ff-only", spec.RemoteURL, spec.Branch}
	case RemotePushBranch:
		if spec.CommitOID != "" {
			observed, err := e.readRemoteOID(ctx, root, spec, transport, askpass, secret)
			if err != nil {
				return RemoteReceipt{}, err
			}
			expected := spec.ExpectedRemoteOID
			if expected == "missing" {
				expected = ""
			}
			if observed != expected {
				return RemoteReceipt{}, apperror.New(apperror.CodeConflict, "remote branch changed after review")
			}
			if expected != "" {
				// Explicit CAS protects a concurrent update; the independent
				// ancestry test disallows every history rewrite.
				cmd := e.remoteGitCommand(ctx, root, transport, askpass, secret, "merge-base", "--is-ancestor", expected, spec.CommitOID)
				if err := checkThreadGitDispatch(ctx); err != nil {
					return RemoteReceipt{}, err
				}
				if err := cmd.Run(); err != nil {
					return RemoteReceipt{}, apperror.New(apperror.CodeFailedPrecondition, "push must fast-forward the exact reviewed remote commit; fetch and review first")
				}
			}
			args = []string{"push", "--quiet", "--force-with-lease=refs/heads/" + spec.Branch + ":" + expected, spec.RemoteURL, spec.CommitOID + ":refs/heads/" + spec.Branch}
			break
		}
		// New branch only: refuse to touch a branch that already exists.
		exists, err := e.remoteBranchExists(ctx, root, spec, transport, askpass, secret)
		if err != nil {
			return RemoteReceipt{}, err
		}
		if exists {
			return RemoteReceipt{}, apperror.New(apperror.CodeFailedPrecondition,
				"target branch already exists on the remote; force push and overwrite are not offered")
		}
		args = []string{"push", "--quiet", spec.RemoteURL, "HEAD:refs/heads/" + spec.Branch}
	}
	command := e.remoteGitCommand(ctx, root, transport, askpass, secret, args...)
	var stdout, stderr boundedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := checkThreadGitDispatch(ctx); err != nil {
		return RemoteReceipt{}, err
	}
	runErr := command.Run()
	completed := time.Now().UTC()
	receipt := RemoteReceipt{
		ProtocolVersion: RemoteProtocolVersion, Operation: spec.Operation,
		PreHead: binding.LocalHead, Branch: spec.Branch, RemoteHost: binding.RemoteHost,
		StderrPrefix:  redactRemoteOutput(boundedOutput(stderr.String()), secret),
		ObservedBytes: len(stdout.String()) + len(stderr.String()),
		StartedAt:     started, CompletedAt: completed,
		BindingFingerprint: runmutation.Fingerprint("repository_remote_binding.v1", operationKey,
			binding.RemoteHost, binding.RemotePort, binding.Protocol, spec.Branch),
	}
	if ctx.Err() != nil {
		return receipt, ctx.Err()
	}
	if runErr != nil {
		return receipt, apperror.New(apperror.CodeFailedPrecondition,
			"remote git operation failed: "+redactRemoteOutput(boundedOutput(stderr.String()), secret))
	}
	// Post-readback: verify the local HEAD and status are consistent.
	postHead, err := e.currentHead(ctx, root)
	if err != nil {
		return receipt, err
	}
	receipt.PostHead = postHead
	if spec.Operation == RemotePullFF {
		receipt.CommitID = postHead
	}
	if spec.Operation == RemotePushBranch && spec.CommitOID != "" {
		observed, observeErr := e.readRemoteOID(ctx, root, spec, transport, askpass, secret)
		if observeErr != nil {
			return receipt, observeErr
		}
		if observed != spec.CommitOID {
			return receipt, apperror.New(apperror.CodeConflict, "remote branch does not match the pushed commit")
		}
		receipt.CommitID = observed
	}
	return receipt, nil
}

// ReadRemoteOID performs only a bounded ls-remote using the original named
// credential and validated URL. It never fetches, pushes, or changes config.
func (e *RemoteExecutor) ReadRemoteOID(ctx context.Context, root string, spec RemoteSpec) (string, error) {
	if e == nil || !e.Available() {
		return "", apperror.New(apperror.CodeFailedPrecondition, "remote Git reader is unavailable")
	}
	if err := e.ValidateSpec(spec); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(spec.NetworkTTLMillis)*time.Millisecond)
	defer cancel()
	transport, err := e.pinRemoteTransport(spec.RemoteURL)
	if err != nil {
		return "", err
	}
	defer os.Remove(transport.path)
	askpass, secret, err := e.askpassHelper(ctx, spec.CredentialName)
	if err != nil {
		return "", err
	}
	defer os.Remove(askpass)
	return e.readRemoteOID(ctx, root, spec, transport, askpass, secret)
}

func (e *RemoteExecutor) readRemoteOID(ctx context.Context, root string, spec RemoteSpec,
	transport *remoteTransportConfig, askpass, secret string,
) (string, error) {
	cmd := e.remoteGitCommand(ctx, root, transport, askpass, secret, "ls-remote", "--heads", spec.RemoteURL, "refs/heads/"+spec.Branch)
	var stdout, stderr boundedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := checkThreadGitDispatch(ctx); err != nil {
		return "", err
	}
	if err := cmd.Run(); err != nil {
		return "", apperror.New(apperror.CodeUnavailable, "remote branch could not be observed")
	}
	text := strings.TrimSpace(stdout.String())
	if text == "" {
		return "", nil
	}
	fields := strings.Fields(text)
	if len(fields) != 2 || !validGitOID(fields[0]) || fields[1] != "refs/heads/"+spec.Branch {
		return "", apperror.New(apperror.CodeFailedPrecondition, "remote branch observation is ambiguous")
	}
	return fields[0], nil
}

func (e *RemoteExecutor) remoteBranchExists(ctx context.Context, root string, spec RemoteSpec,
	transport *remoteTransportConfig, askpass, secret string,
) (bool, error) {
	command := e.remoteGitCommand(ctx, root, transport, askpass, secret,
		"ls-remote", "--heads", spec.RemoteURL, "refs/heads/"+spec.Branch)
	var stdout, stderr boundedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := checkThreadGitDispatch(ctx); err != nil {
		return false, err
	}
	err := command.Run()
	if err != nil {
		return false, apperror.New(apperror.CodeFailedPrecondition, "remote branch probe failed")
	}
	return strings.TrimSpace(stdout.String()) != "", nil
}

type remoteTransportConfig struct {
	path     string
	url      string
	protocol string
}

func (e *RemoteExecutor) pinRemoteTransport(rawURL string) (*remoteTransportConfig, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" &&
		(!e.allowLocalRemote || parsed.Scheme != "file" && parsed.Scheme != "http") {
		return nil, errors.New("remote transport requires the validated HTTPS URL")
	}
	file, err := os.CreateTemp("", "cyberagent-remote-config-*.gitconfig")
	if err != nil {
		return nil, err
	}
	// Git chooses the longest insteadOf prefix, retaining the first rule on
	// equal lengths. This full-URL identity rule loads before local, included,
	// and worktree configuration, so later changes cannot redirect transport.
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(rawURL) + `"`
	_, writeErr := fmt.Fprintf(file, "[url %s]\n\tinsteadOf = %s\n\tpushInsteadOf = %s\n"+
		"[remote %s]\n\tuploadpack = git-upload-pack\n\treceivepack = git-receive-pack\n", quoted, quoted, quoted, quoted)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(file.Name())
		return nil, err
	}
	return &remoteTransportConfig{path: file.Name(), url: rawURL, protocol: parsed.Scheme}, nil
}

func (e *RemoteExecutor) remoteGitCommand(ctx context.Context, root string,
	transport *remoteTransportConfig, askpass, secret string,
	args ...string,
) *exec.Cmd {
	hardenedArgs := []string{"-C", root, "--no-optional-locks",
		"-c", "core.autocrlf=false", "-c", "http.proxy=", "-c", "https.proxy=",
		"-c", "core.sshCommand=", "-c", "protocol.ext.allow=never",
		"-c", "fetch.recurseSubmodules=false", "-c", "push.recurseSubmodules=no"}
	remoteKey := "remote." + transport.url + "."
	httpKey := "http." + transport.url + "."
	for _, setting := range []string{
		remoteKey + "url=", remoteKey + "url=" + transport.url,
		remoteKey + "pushurl=", remoteKey + "pushurl=" + transport.url,
		remoteKey + "proxy=", remoteKey + "mirror=false",
		httpKey + "proxy=", httpKey + "followRedirects=false",
		httpKey + "curloptResolve=", httpKey + "sslVerify=true",
	} {
		hardenedArgs = append(hardenedArgs, "-c", setting)
	}
	if transport.protocol != "file" {
		// A local remote named exactly like the URL cannot select another helper.
		hardenedArgs = append(hardenedArgs, "-c", remoteKey+"vcs="+transport.protocol)
	}
	command := exec.CommandContext(ctx, e.gitPath, append(hardenedArgs, args...)...)
	command.Dir = root
	for _, value := range hardenedGitEnvironment() {
		if !strings.HasPrefix(value, "GIT_CONFIG_GLOBAL=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "GIT_CONFIG_GLOBAL="+transport.path,
		"GIT_ALLOW_PROTOCOL="+transport.protocol, "GIT_NO_LAZY_FETCH=1",
		"GIT_ASKPASS="+askpass, "GIT_PASSWORD="+secret)
	return command
}

func (e *RemoteExecutor) currentHead(ctx context.Context, root string) (string, error) {
	command := exec.CommandContext(ctx, e.gitPath, "-C", root, "rev-parse", "HEAD")
	command.Env = hardenedGitEnvironment()
	var stdout, stderr boundedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := checkThreadGitDispatch(ctx); err != nil {
		return "", err
	}
	if err := command.Run(); err != nil {
		return "", apperror.New(apperror.CodeFailedPrecondition, "post-readback failed")
	}
	return strings.TrimSpace(stdout.String()), nil
}

// askpassHelper materializes the referenced credential for git only: the
// secret travels in a child-process environment variable consumed by a
// temporary helper script, never in argv, logs, or storage.
func (e *RemoteExecutor) askpassHelper(ctx context.Context, credentialName string) (string, string, error) {
	if err := checkThreadGitDispatch(ctx); err != nil {
		return "", "", err
	}
	if credentialName == "" || e.credentials == nil || !e.credentials.Available() {
		return "", "", nil
	}
	secret, found, err := e.credentials.Get(ctx, credentialName)
	if err != nil || !found {
		return "", "", apperror.New(apperror.CodeUnavailable, "referenced credential is unavailable")
	}
	digest := runmutation.OperationKeyDigest("remote-askpass", "static", credentialName)[:16]
	var path, script string
	if runtime.GOOS == "windows" {
		path = filepath.Join(os.TempDir(), "askpass-"+digest+".cmd")
		script = "@echo off\r\necho %GIT_PASSWORD%\r\n"
	} else {
		path = filepath.Join(os.TempDir(), "askpass-"+digest+".sh")
		script = "#!/bin/sh\necho \"$GIT_PASSWORD\"\n"
	}
	if err := checkThreadGitDispatch(ctx); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return "", "", err
	}
	return path, secret, nil
}

// redactRemoteOutput replaces the referenced secret with a marker if it
// ever leaks into git output.
func redactRemoteOutput(value, secret string) string {
	if secret != "" && strings.Contains(value, secret) {
		return strings.ReplaceAll(value, secret, "[credential-redacted]")
	}
	return value
}

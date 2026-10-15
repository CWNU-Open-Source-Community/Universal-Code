package domain

import (
	"encoding/hex"
	"errors"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxThreadMessageFiles = 4

// WorkspaceFileReference is an immutable reference to a projected, untrusted
// workspace file snapshot. It carries no filesystem or execution authority.
type WorkspaceFileReference struct {
	SourceKind     string `json:"source_kind"`
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256"`
}

func ValidateThreadMessageFiles(files []WorkspaceFileReference) error {
	if len(files) > MaxThreadMessageFiles {
		return errors.New("at most four Thread file references are allowed")
	}
	seen := map[string]bool{}
	for _, file := range files {
		if file.SourceKind != "workspace_file" || file.Path == "" || file.Path == "." ||
			!utf8.ValidString(file.Path) || utf8.RuneCountInString(file.Path) > 512 ||
			file.Path != strings.TrimSpace(file.Path) || path.Clean(file.Path) != file.Path ||
			strings.HasPrefix(file.Path, "/") || file.Path == ".." || strings.HasPrefix(file.Path, "../") ||
			strings.ContainsAny(file.Path, `\:`) || strings.IndexFunc(file.Path, unicode.IsControl) >= 0 {
			return errors.New("Thread file reference must use a canonical bounded workspace file path")
		}
		decoded, err := hex.DecodeString(file.ExpectedSHA256)
		if err != nil || len(decoded) != 32 || file.ExpectedSHA256 != strings.ToLower(file.ExpectedSHA256) {
			return errors.New("Thread file reference requires a lowercase SHA-256 digest")
		}
		if seen[file.Path] {
			return errors.New("Thread file references cannot repeat a path")
		}
		seen[file.Path] = true
	}
	return nil
}

type ThreadMessageIntentRequest struct {
	ThreadID      string
	Content       string
	OperationKey  string
	RequestedBy   string
	Files         []WorkspaceFileReference
	Images        []ImageReference
	Attachments   []FileAttachmentReference
	MCPReferences []MCPReference
}

// An intent reserves content and files before any message is consumable. Its
// Run/message binding is assigned once, atomically with evidence and enqueue.
type ThreadMessageIntent struct {
	OperationKeyDigest string
	RequestFingerprint string
	RunID              string
	MessageID          string
	Rejected           bool
}

func ThreadMessageFileOperationKey(intentDigest string, index int) string {
	return "thread-file-" + intentDigest + "-" + strconv.Itoa(index)
}

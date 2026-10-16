package approval

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode"

	"cyberagent-workbench/internal/computeruse"
)

type ComputerUseTarget struct {
	AppID          string `json:"app_id"`
	WindowID       string `json:"window_id"`
	ToolName       string `json:"tool_name"`
	Executable     string `json:"executable"`
	SystemShortcut bool   `json:"system_shortcut,omitempty"`
}

// ParseComputerUseTarget checks only the stored target's shape. The native
// executor must compare the full AppID with the actual window on every action.
func ParseComputerUseTarget(tool string, arguments json.RawMessage) (ComputerUseTarget, error) {
	target := ComputerUseTarget{ToolName: tool}
	switch tool {
	case "computer_list_windows":
		return target, nil
	case "computer_observe", "computer_activate", "computer_click", "computer_type", "computer_key", "computer_scroll":
	default:
		return target, errors.New("unsupported built-in Computer Use tool")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(arguments, &fields) != nil || json.Unmarshal(fields["app_id"], &target.AppID) != nil ||
		json.Unmarshal(fields["window_id"], &target.WindowID) != nil || target.WindowID == "" ||
		len(target.WindowID) > 256 || strings.IndexFunc(target.WindowID, unicode.IsSpace) >= 0 ||
		strings.IndexFunc(target.WindowID, unicode.IsControl) >= 0 {
		return target, errors.New("Computer Use requires an exact application and window identity")
	}
	var err error
	target.Executable, err = computeruse.ParseAppID(target.AppID)
	if err != nil {
		return target, err
	}
	if tool == "computer_key" {
		var key string
		if json.Unmarshal(fields["key"], &key) != nil {
			return target, errors.New("Computer Use key chord is invalid")
		}
		target.SystemShortcut, err = computeruse.KeyRequiresDesktopApproval(key)
	}
	return target, err
}

// ComputerUseAppScope is resolved from the durable host-bound MCP intent.
// It identifies an application for one live Run permission and built-in source.
// Coordinates and tool names do not describe the business effect of an action.
type ComputerUseAppScope struct {
	RunID, SessionID, WorkspaceID string
	AppID                         string
	PermissionSnapshotID          string
	PermissionRevision            int64
	PermissionMode                string
	PermissionGeneration          uint64
	PermissionRuntimeEpoch        string
	RunAuthorizationFence         uint64
	ServerID                      string
	DescriptorFingerprint         string
	ServerGeneration              int64
	CapabilityFingerprint         string
}

func (s ComputerUseAppScope) Fingerprint() string {
	return Fingerprint("computer_use_app_scope.v1", s.RunID, s.SessionID, s.WorkspaceID,
		s.AppID, s.PermissionSnapshotID, strconv.FormatInt(s.PermissionRevision, 10),
		s.PermissionMode, strconv.FormatUint(s.PermissionGeneration, 10),
		s.PermissionRuntimeEpoch, strconv.FormatUint(s.RunAuthorizationFence, 10),
		s.ServerID, s.DescriptorFingerprint, strconv.FormatInt(s.ServerGeneration, 10),
		s.CapabilityFingerprint)
}

func (s ComputerUseAppScope) GrantActionClass() string {
	return "computer_use_app:" + s.Fingerprint()
}

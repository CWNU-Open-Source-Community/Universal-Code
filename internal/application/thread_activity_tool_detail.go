package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

const (
	ThreadActivityToolFactsProtocolVersion = "thread_activity_tool_facts.v1"
	MaxThreadActivityFactFields            = 16
	MaxThreadActivityFactValueRunes        = 2048
)

// ThreadActivityToolFacts is a deprecated, private compatibility projection
// used only as a bounded validation adapter by the v2 Go projector. Both the
// Thread conversation and Legacy Inspector HTTP responses use
// ThreadActivityTypedDetail and never emit this generic fact bag.
// Like the v2 projection it contains no raw payload/result, credentials,
// authority JSON, stdout/stderr, or private reasoning.
type ThreadActivityToolFacts struct {
	Version       string
	Kind          string
	Operation     string
	Target        string
	Parameters    []ThreadActivityFactField
	Result        []ThreadActivityFactField
	Authorization string
	ErrorCode     string
	ArtifactRef   string
	Untrusted     bool
}

type ThreadActivityFactField struct {
	Name  string
	Label string
	Value string
}

// ProjectThreadActivityToolFacts projects non-command Supervisor calls. A
// command_runtime call returns found=false so the existing command-specific
// projection remains its sole public representation.
func ProjectThreadActivityToolFacts(call domain.SupervisorToolCall) (
	value ThreadActivityToolFacts, found bool, err error,
) {
	name := toolgateway.ToolName(call.ToolName)
	if name == toolgateway.CommandRuntimeTool {
		return ThreadActivityToolFacts{}, false, nil
	}
	value = ThreadActivityToolFacts{Version: ThreadActivityToolFactsProtocolVersion,
		Kind: threadActivityToolKind(name), Operation: threadActivityToolLabel(name),
		Authorization: "policy_checked", ErrorCode: safeThreadActivityFactIdentity(call.ErrorCode),
		Untrusted: name == toolgateway.MCPToolCallTool || name == toolgateway.MCPToolSearchTool || toolgateway.IsWebEvidenceTool(name) ||
			toolgateway.IsBrowserActionTool(name)}
	if call.Status == domain.SupervisorToolDenied {
		value.Authorization = "denied"
	} else if call.Status == domain.SupervisorToolPending {
		value.Authorization = "pending"
	}
	if value.Kind == "tool" || value.Operation == "" {
		return ThreadActivityToolFacts{}, false, nil
	}
	if err := projectThreadActivityInput(&value, name, json.RawMessage(call.PayloadJSON)); err != nil {
		return ThreadActivityToolFacts{}, true, err
	}
	if strings.TrimSpace(call.ResultJSON) != "" {
		if err := projectThreadActivityResult(&value, name, call.ResultJSON); err != nil {
			return ThreadActivityToolFacts{}, true, err
		}
	}
	if err := value.Validate(); err != nil {
		return ThreadActivityToolFacts{}, true, err
	}
	return value, true, nil
}

// SupportsThreadActivityDetail reports whether a durable Supervisor tool call
// has a deliberately typed public detail projection. Keeping this catalog in
// Go prevents the transcript UI from guessing which raw payloads are safe to
// expose.
func SupportsThreadActivityDetail(toolName string) bool {
	name := toolgateway.ToolName(toolName)
	return name == toolgateway.CommandRuntimeTool ||
		(threadActivityToolKind(name) != "tool" && threadActivityToolLabel(name) != "")
}

func (v ThreadActivityToolFacts) Validate() error {
	if v.Version != ThreadActivityToolFactsProtocolVersion ||
		!threadActivityToolKinds[v.Kind] || !validThreadActivityFactText(v.Operation, 128, false) ||
		!validThreadActivityFactText(v.Target, MaxThreadActivityFactValueRunes, true) ||
		!threadActivityAuthorizations[v.Authorization] ||
		!validThreadActivityFactIdentity(v.ErrorCode, true) ||
		!validThreadActivityFactIdentity(v.ArtifactRef, true) ||
		len(v.Parameters) > MaxThreadActivityFactFields || len(v.Result) > MaxThreadActivityFactFields {
		return errors.New("Thread activity tool facts are invalid")
	}
	for _, fields := range [][]ThreadActivityFactField{v.Parameters, v.Result} {
		seen := make(map[string]struct{}, len(fields))
		for _, field := range fields {
			if !validThreadActivityFactIdentity(field.Name, false) ||
				!validThreadActivityFactText(field.Label, 128, false) ||
				!validThreadActivityFactText(field.Value, MaxThreadActivityFactValueRunes, false) {
				return errors.New("Thread activity fact field is invalid")
			}
			if _, exists := seen[field.Name]; exists {
				return errors.New("Thread activity fact field is duplicated")
			}
			seen[field.Name] = struct{}{}
		}
	}
	return nil
}

var threadActivityToolKinds = map[string]bool{
	"web_search": true, "web_fetch": true, "file_read": true, "file_edit": true,
	"verification": true, "mcp": true, "browser": true,
}

var threadActivityAuthorizations = map[string]bool{
	"policy_checked": true, "pending": true, "denied": true,
}

func threadActivityToolKind(name toolgateway.ToolName) string {
	switch {
	case name == toolgateway.WebSearchTool || name == toolgateway.SourceSearchTool:
		return "web_search"
	case name == toolgateway.WebFetchTool || name == toolgateway.WebCitationTool:
		return "web_fetch"
	case name == toolgateway.WorkspaceReadTool || name == toolgateway.WorkspaceListTool ||
		name == toolgateway.WorkspaceGlobTool || name == toolgateway.WorkspaceGrepTool:
		return "file_read"
	case name == toolgateway.WorkspaceChangeTool || name == toolgateway.WorkspaceApplyTool ||
		name == toolgateway.WorkspaceDeleteTool:
		return "file_edit"
	case toolgateway.IsCodeIntelTool(name) || name == toolgateway.GitHubEvidenceListTool ||
		name == toolgateway.GitHubEvidenceReadTool:
		return "verification"
	case name == toolgateway.MCPToolCallTool || name == toolgateway.MCPToolSearchTool:
		return "mcp"
	case toolgateway.IsBrowserActionTool(name):
		return "browser"
	default:
		return "tool"
	}
}

func threadActivityToolLabel(name toolgateway.ToolName) string {
	labels := map[toolgateway.ToolName]string{
		toolgateway.WorkspaceListTool: "列出目录", toolgateway.WorkspaceReadTool: "读取文件",
		toolgateway.WorkspaceGlobTool: "匹配文件", toolgateway.WorkspaceGrepTool: "搜索工作区",
		toolgateway.WorkspaceChangeTool: "提议文件修改", toolgateway.WorkspaceApplyTool: "应用文件修改",
		toolgateway.WorkspaceDeleteTool: "删除文件", toolgateway.WebSearchTool: "联网搜索",
		toolgateway.SourceSearchTool: "搜索专业来源",
		toolgateway.WebFetchTool:     "抓取网页", toolgateway.WebCitationTool: "验证网页引用",
		toolgateway.MCPToolCallTool: "MCP 调用", toolgateway.MCPToolSearchTool: "搜索 MCP 操作", toolgateway.BrowserStatusTool: "读取浏览器状态",
		toolgateway.GitHubEvidenceListTool: "列出 GitHub 审查证据",
		toolgateway.GitHubEvidenceReadTool: "读取 GitHub 审查证据",
		toolgateway.BrowserNavigateTool:    "浏览器导航", toolgateway.BrowserSnapshotTool: "读取页面快照",
		toolgateway.BrowserClickTool: "点击页面元素", toolgateway.BrowserTypeTool: "填写页面输入",
		toolgateway.BrowserScreenshotTool: "截取页面画面",
	}
	if label := labels[name]; label != "" {
		return label
	}
	if toolgateway.IsCodeIntelTool(name) {
		return "查询代码语义"
	}
	return ""
}

func projectThreadActivityInput(value *ThreadActivityToolFacts, name toolgateway.ToolName,
	raw json.RawMessage,
) error {
	switch {
	case name == toolgateway.MCPToolSearchTool:
		input, _, err := toolgateway.NormalizeMCPSearchPayload(raw)
		if err != nil {
			return err
		}
		value.Target = safeThreadActivityFactValue(input.Query)
		if value.Target == "" {
			value.Target = "当前授权的 MCP 目录"
		}
		appendThreadActivityFact(&value.Parameters, "limit", "结果上限", strconv.Itoa(input.Limit))
		return nil
	case name == toolgateway.MCPToolCallTool:
		// This is a read-only public projection over durable history. Decode the
		// exact payload shape without reapplying today's ingress secret policy so
		// legacy calls can still be rendered through the recursive redactor.
		input, err := decodeThreadActivityMCPPayload(raw)
		if err != nil {
			return err
		}
		value.Target = input.ServerID + " / " + input.ToolName
		keys := safeThreadActivityJSONKeys(input.Arguments)
		if len(keys) > 0 {
			value.Parameters = append(value.Parameters, threadActivityFact("argument_keys", "参数字段",
				strings.Join(keys, ", ")))
		}
		return nil
	case toolgateway.IsWebEvidenceTool(name):
		canonical, err := toolgateway.NormalizeWebEvidencePayload(name, raw)
		if err != nil {
			return err
		}
		return projectThreadActivityWebInput(value, name, canonical)
	case toolgateway.IsCodeIntelTool(name):
		input, _, err := toolgateway.NormalizeCodeIntelPayload(name, raw)
		if err != nil {
			return err
		}
		value.Target = safeThreadActivityPath(input.Path)
		appendThreadActivityFact(&value.Parameters, "query", "查询", input.Query)
		if input.Line != 0 || input.Character != 0 {
			appendThreadActivityFact(&value.Parameters, "position", "位置",
				fmt.Sprintf("%d:%d", input.Line+1, input.Character+1))
		}
		appendThreadActivityFact(&value.Parameters, "direction", "方向", input.Direction)
		appendThreadActivityFact(&value.Parameters, "limit", "上限", strconv.Itoa(input.Limit))
		return nil
	case name == toolgateway.GitHubEvidenceListTool || name == toolgateway.GitHubEvidenceReadTool:
		canonical, err := toolgateway.NormalizeAgentCodePayload(name, raw)
		if err != nil {
			return err
		}
		return projectThreadActivityWorkspaceInput(value, name, canonical)
	case toolgateway.IsBrowserActionTool(name):
		canonical, err := toolgateway.NormalizeBrowserActionPayload(name, raw)
		if err != nil {
			return err
		}
		var input toolgateway.BrowserActionPayload
		_ = json.Unmarshal(canonical, &input)
		value.Target = safeThreadActivityURL(input.URL)
		appendThreadActivityFact(&value.Parameters, "selector", "选择器", input.Selector)
		if name == toolgateway.BrowserTypeTool {
			appendThreadActivityFact(&value.Parameters, "input", "输入内容",
				fmt.Sprintf("已提供 %d 个字符（内容不显示）", utf8.RuneCountInString(input.Value)))
		}
		return nil
	default:
		canonical, err := toolgateway.NormalizeAgentCodePayload(name, raw)
		if err != nil {
			return err
		}
		return projectThreadActivityWorkspaceInput(value, name, canonical)
	}
}

func projectThreadActivityWorkspaceInput(value *ThreadActivityToolFacts, name toolgateway.ToolName,
	raw json.RawMessage,
) error {
	switch name {
	case toolgateway.WorkspaceListTool:
		var input toolgateway.WorkspaceListPayload
		_ = json.Unmarshal(raw, &input)
		value.Target = safeThreadActivityPath(input.Path)
		appendThreadActivityFact(&value.Parameters, "limit", "上限", strconv.Itoa(input.Limit))
	case toolgateway.WorkspaceReadTool:
		var input toolgateway.WorkspaceReadPayload
		_ = json.Unmarshal(raw, &input)
		value.Target = safeThreadActivityPath(input.Path)
		if input.StartLine > 0 || input.EndLine > 0 {
			appendThreadActivityFact(&value.Parameters, "lines", "行范围",
				fmt.Sprintf("%d-%d", input.StartLine, input.EndLine))
		}
	case toolgateway.WorkspaceGlobTool:
		var input toolgateway.WorkspaceGlobPayload
		_ = json.Unmarshal(raw, &input)
		value.Target = safeThreadActivityFactValue(input.Pattern)
		appendThreadActivityFact(&value.Parameters, "limit", "上限", strconv.Itoa(input.Limit))
	case toolgateway.WorkspaceGrepTool:
		var input toolgateway.WorkspaceGrepPayload
		_ = json.Unmarshal(raw, &input)
		value.Target = safeThreadActivityFactValue(input.Query)
		appendThreadActivityFact(&value.Parameters, "files", "文件范围", input.Pattern)
		appendThreadActivityFact(&value.Parameters, "case_sensitive", "区分大小写",
			strconv.FormatBool(input.CaseSensitive))
		appendThreadActivityFact(&value.Parameters, "limit", "上限", strconv.Itoa(input.Limit))
	case toolgateway.WorkspaceChangeTool:
		var input toolgateway.WorkspaceChangePayload
		_ = json.Unmarshal(raw, &input)
		value.Target = safeThreadActivityPath(input.Path)
		appendThreadActivityFact(&value.Parameters, "action", "操作", input.Action)
		appendThreadActivityFact(&value.Parameters, "destination", "目标路径",
			safeThreadActivityPath(input.DestinationPath))
		if input.Content != "" || len(input.Replacements) > 0 {
			appendThreadActivityFact(&value.Parameters, "change", "修改内容", "已提供（内容不显示）")
		}
	case toolgateway.WorkspaceApplyTool:
		var input toolgateway.WorkspaceApplyPayload
		_ = json.Unmarshal(raw, &input)
		value.Target = "已批准的文件修改"
		appendThreadActivityFact(&value.Parameters, "action", "操作", input.ExpectedAction)
	case toolgateway.WorkspaceDeleteTool:
		var input toolgateway.WorkspaceDeletePayload
		_ = json.Unmarshal(raw, &input)
		value.Target = safeThreadActivityPath(input.Path)
		appendThreadActivityFact(&value.Parameters, "action", "操作", input.Action)
	case toolgateway.GitHubEvidenceListTool:
		var input toolgateway.GitHubEvidenceListPayload
		_ = json.Unmarshal(raw, &input)
		value.Target = "当前 Run 的不可变审查证据"
		appendThreadActivityFact(&value.Parameters, "limit", "上限", strconv.Itoa(input.Limit))
		value.Untrusted = true
	case toolgateway.GitHubEvidenceReadTool:
		var input toolgateway.GitHubEvidenceReadPayload
		_ = json.Unmarshal(raw, &input)
		value.Target = safeThreadActivityFactValue(input.EvidenceID)
		value.Untrusted = true
	}
	return nil
}

func projectThreadActivityWebInput(value *ThreadActivityToolFacts, name toolgateway.ToolName,
	raw json.RawMessage,
) error {
	switch name {
	case toolgateway.WebSearchTool:
		var input toolgateway.WebSearchPayload
		_ = json.Unmarshal(raw, &input)
		value.Target = safeThreadActivityFactValue(input.Query)
		appendThreadActivityFact(&value.Parameters, "limit", "结果上限", strconv.Itoa(input.Limit))
	case toolgateway.SourceSearchTool:
		var input toolgateway.SourceSearchPayload
		_ = json.Unmarshal(raw, &input)
		value.Target = safeThreadActivityFactValue(input.Query)
		appendThreadActivityFact(&value.Parameters, "connectors", "来源",
			strings.Join(input.Connectors, ", "))
		appendThreadActivityFact(&value.Parameters, "limit", "结果上限", strconv.Itoa(input.Limit))
	case toolgateway.WebFetchTool:
		var input toolgateway.WebFetchPayload
		_ = json.Unmarshal(raw, &input)
		if input.URL != "" {
			value.Target = safeThreadActivityURL(input.URL)
		} else {
			value.Target = "已发现的网页来源"
		}
	case toolgateway.WebCitationTool:
		var input toolgateway.WebCitationPayload
		_ = json.Unmarshal(raw, &input)
		value.Target = "已抓取的网页快照"
		if input.SpanEnd > input.SpanStart {
			appendThreadActivityFact(&value.Parameters, "span", "引用范围",
				fmt.Sprintf("%d-%d", input.SpanStart, input.SpanEnd))
		}
	}
	return nil
}

type threadActivityResultEnvelope struct {
	Version   string            `json:"version"`
	Tool      string            `json:"tool"`
	Status    string            `json:"status"`
	Stdout    string            `json:"stdout"`
	Metadata  map[string]string `json:"metadata"`
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Truncated bool              `json:"truncated"`
}

func projectThreadActivityResult(value *ThreadActivityToolFacts, name toolgateway.ToolName,
	raw string,
) error {
	var envelope threadActivityResultEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil ||
		envelope.Version != supervisorToolResultVersion || envelope.Tool != string(name) {
		return errors.New("durable tool result envelope is invalid")
	}
	appendThreadActivityFact(&value.Result, "call_status", "结果", envelope.Status)
	if envelope.Truncated {
		appendThreadActivityFact(&value.Result, "truncated", "内容截断", "true")
	}
	if value.ErrorCode == "" {
		value.ErrorCode = safeThreadActivityFactIdentity(envelope.Code)
	}
	metadata := envelope.Metadata
	appendCountMetadata := func(key, label string) {
		if rawValue := metadata[key]; validThreadActivityCount(rawValue) {
			appendThreadActivityFact(&value.Result, key, label, rawValue)
		}
	}
	appendBoolMetadata := func(key, label string) {
		if rawValue := metadata[key]; rawValue == "true" || rawValue == "false" {
			appendThreadActivityFact(&value.Result, key, label, rawValue)
		}
	}
	switch value.Kind {
	case "file_read", "verification":
		appendCountMetadata("result_count", "结果数量")
		appendBoolMetadata("truncated", "结果截断")
	case "file_edit":
		appendEnumMetadata(&value.Result, metadata, "operation", "操作",
			"create", "replace", "delete", "move", "apply")
		appendEnumMetadata(&value.Result, metadata, "status", "修改状态",
			"proposed", "approved", "applied", "completed", "failed")
	case "web_search":
		appendCountMetadata("source_count", "来源数量")
		appendBoolMetadata("citeable", "可直接引用")
		appendSafeMetadata(&value.Result, metadata, "provider", "搜索提供商")
	case "web_fetch":
		appendEnumMetadata(&value.Result, metadata, "state", "快照状态",
			"fetched", "partial", "stale", "blocked", "failed")
		appendEnumMetadata(&value.Result, metadata, "robots", "Robots",
			"allowed", "blocked", "unknown", "not_checked", "not_present",
			"bypassed_disallow", "bypassed_unknown")
		appendBoolMetadata("partial", "部分内容")
		appendBoolMetadata("citeable", "可引用")
	case "mcp":
		appendThreadActivityFact(&value.Result, "trust", "输出信任", "外部不可信数据")
	case "browser":
		appendCountMetadata("artifact_bytes", "截图字节数")
	}
	return nil
}

func appendEnumMetadata(fields *[]ThreadActivityFactField, metadata map[string]string,
	key, label string, allowed ...string,
) {
	value := strings.TrimSpace(metadata[key])
	for _, candidate := range allowed {
		if value == candidate {
			appendThreadActivityFact(fields, key, label, value)
			return
		}
	}
}

func appendSafeMetadata(fields *[]ThreadActivityFactField, metadata map[string]string,
	key, label string,
) {
	appendThreadActivityFact(fields, key, label, metadata[key])
}

func threadActivityFact(name, label, value string) ThreadActivityFactField {
	return ThreadActivityFactField{Name: name, Label: label, Value: safeThreadActivityFactValue(value)}
}

func appendThreadActivityFact(fields *[]ThreadActivityFactField, name, label, value string) {
	value = safeThreadActivityFactValue(value)
	if value == "" {
		return
	}
	for index := range *fields {
		if (*fields)[index].Name == name {
			// A transport envelope and a tool-specific metadata projection can
			// report the same bounded fact (notably truncation). Keep the first
			// trusted label/value instead of producing an invalid duplicate-key
			// public projection.
			return
		}
	}
	if len(*fields) >= MaxThreadActivityFactFields {
		return
	}
	*fields = append(*fields, ThreadActivityFactField{Name: name, Label: label, Value: value})
}

func safeThreadActivityFactValue(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	value = redact.String(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > MaxThreadActivityFactValueRunes {
		value = string(runes[:MaxThreadActivityFactValueRunes-1]) + "…"
	}
	return value
}

func safeThreadActivityPath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return "工作区内路径"
	}
	clean := path.Clean(value)
	if clean == "." {
		return "."
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "工作区内路径"
	}
	return safeThreadActivityFactValue(clean)
}

func safeThreadActivityURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Opaque != "" || parsed.Hostname() == "" ||
		!strings.EqualFold(parsed.Scheme, "https") {
		return "受控网页目标"
	}
	// A public activity URL is for operator navigation, not durable request
	// replay. Preserve ordinary semantic query parameters while dropping all
	// credential-shaped material and non-server identity such as fragments or
	// userinfo before the value crosses the HTTP boundary.
	parsed.User = nil
	parsed.Fragment = ""
	parsed.RawFragment = ""
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "受控网页目标"
	}
	for key, values := range query {
		if sensitiveThreadActivityURLQueryKey(key) {
			query.Del(key)
			continue
		}
		for _, current := range values {
			redacted := redact.Text(current)
			if len(redacted.Findings) > 0 || redacted.Text != current {
				query.Del(key)
				break
			}
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.ForceQuery = false
	canonical, err := webevidence.CanonicalizePublicHTTPSURL(parsed.String())
	if err != nil {
		return "受控网页目标"
	}
	return safeThreadActivityFactValue(canonical)
}

func sensitiveThreadActivityURLQueryKey(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "_", ".", "_", " ", "_", "[", "_", "]", "").Replace(normalized)
	compact := strings.ReplaceAll(normalized, "_", "")
	if compact == "key" || compact == "sig" || compact == "jwt" ||
		compact == "session" || compact == "sessionid" {
		return true
	}
	for _, token := range []string{"apikey", "accesstoken", "refreshtoken", "idtoken",
		"secret", "password", "passwd", "privatekey", "signature", "authorization",
		"credential", "cookie", "setcookie", "bearertoken"} {
		if strings.Contains(compact, token) {
			return true
		}
	}
	return compact == "auth" || strings.HasPrefix(compact, "auth") ||
		strings.HasSuffix(compact, "token")
}

func safeThreadActivityJSONKeys(raw json.RawMessage) []string {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil
	}
	keys := make([]string, 0, min(len(object), MaxThreadActivityFactFields))
	for key := range object {
		key = safeThreadActivityArgumentKey(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > MaxThreadActivityFactFields {
		keys = keys[:MaxThreadActivityFactFields]
	}
	return keys
}

func safeThreadActivityArgumentKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 128 {
		return ""
	}
	for _, current := range value {
		if !(current >= 'a' && current <= 'z') && !(current >= 'A' && current <= 'Z') &&
			!(current >= '0' && current <= '9') && current != '_' && current != '-' &&
			current != '.' && current != ':' {
			return ""
		}
	}
	return value
}

func validThreadActivityCount(value string) bool {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return err == nil && parsed >= 0
}

func safeThreadActivityFactIdentity(value string) string {
	value = strings.TrimSpace(value)
	if !validThreadActivityFactIdentity(value, true) || value == "" {
		return ""
	}
	return value
}

func validThreadActivityFactIdentity(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	if strings.TrimSpace(value) != value || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > 256 || redact.String(value) != value {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func validThreadActivityFactText(value string, maxRunes int, optional bool) bool {
	if value == "" {
		return optional
	}
	if strings.TrimSpace(value) != value || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > maxRunes || redact.String(value) != value {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) && current != '\n' && current != '\t' {
			return false
		}
	}
	return true
}

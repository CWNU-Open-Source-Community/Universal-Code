package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

const (
	MaxThreadActivitySearchSources = 10
	MaxThreadActivityMCPFields     = 16
	MaxThreadActivityDiffSummary   = 512
)

// ThreadActivityTypedDetail is the Go-owned tagged union carried by
// thread_activity_detail.v2. Exactly one branch must match Kind. Raw durable
// payloads and generic name/value fact bags never cross this boundary.
type ThreadActivityTypedDetail struct {
	Kind         string
	Command      *ThreadActivityCommandGroup
	WebSearch    *ThreadActivityWebSearchDetail
	WebFetch     *ThreadActivityWebFetchDetail
	FileRead     *ThreadActivityFileReadDetail
	FileEdit     *ThreadActivityFileEditDetail
	MCP          *ThreadActivityMCPDetail
	Verification *ThreadActivityVerificationDetail
	Browser      *ThreadActivityBrowserDetail
}

type ThreadActivityBoundary struct {
	Authorization string
	ErrorCode     string
	FailureReason string
	Truncated     bool
	Untrusted     bool
}

type ThreadActivityCommandGroup struct {
	Commands []ThreadActivityCommandDetail
}

type ThreadActivitySearchSource struct {
	Rank     int
	Title    string
	URL      string
	Provider string
	State    string
	Citeable bool
}

type ThreadActivityWebSearchDetail struct {
	Operation       string
	Query           string
	Limit           int
	Provider        string
	SearchPolicy    string
	SelectionReason string
	SourceCount     int
	Citeable        bool
	Sources         []ThreadActivitySearchSource
	Boundary        ThreadActivityBoundary
}

type ThreadActivityWebFetchDetail struct {
	Operation    string
	URL          string
	State        string
	HTTPStatus   int
	Robots       string
	RobotsPolicy string
	Redirects    int
	Partial      bool
	Citeable     bool
	Boundary     ThreadActivityBoundary
}

type ThreadActivityFileReadDetail struct {
	Operation   string
	Path        string
	Query       string
	Pattern     string
	StartLine   int
	EndLine     int
	Limit       int
	ResultCount int
	Truncated   bool
	Summary     string
	Boundary    ThreadActivityBoundary
}

type ThreadActivityDiffSummary struct {
	AddedLines   int
	RemovedLines int
	Hunks        int
	Summary      string
}

type ThreadActivityFileEditDetail struct {
	Operation       string
	Action          string
	Path            string
	DestinationPath string
	EditID          string
	DiffAvailable   bool
	ApplyStatus     string
	Applied         bool
	FileWritten     bool
	Replayed        bool
	Diff            ThreadActivityDiffSummary
	Boundary        ThreadActivityBoundary
}

type ThreadActivityJSONFieldSummary struct {
	Name    string
	Type    string
	Summary string
}

type ThreadActivityJSONSummary struct {
	Type    string
	Count   int
	Summary string
	Fields  []ThreadActivityJSONFieldSummary
}

type ThreadActivityMCPDetail struct {
	Operation string
	Server    string
	Tool      string
	Arguments []ThreadActivityJSONFieldSummary
	Result    ThreadActivityJSONSummary
	Boundary  ThreadActivityBoundary
}

type ThreadActivityVerificationDetail struct {
	Operation   string
	Tool        string
	Path        string
	Query       string
	Position    string
	Direction   string
	Limit       int
	ResultCount int
	Truncated   bool
	Summary     string
	Boundary    ThreadActivityBoundary
}

type ThreadActivityBrowserDetail struct {
	Operation     string
	Action        string
	URL           string
	Selector      string
	InputLength   int
	ArtifactBytes int64
	Summary       string
	Boundary      ThreadActivityBoundary
}

// ProjectThreadActivityTypedDetail projects a non-command durable Supervisor
// call into one exact public branch. It reuses the v1 compatibility projector
// only as an input/result validity gate; no generic facts are returned.
func ProjectThreadActivityTypedDetail(call domain.SupervisorToolCall) (
	ThreadActivityTypedDetail, bool, error,
) {
	name := toolgateway.ToolName(call.ToolName)
	if name == toolgateway.CommandRuntimeTool {
		return ThreadActivityTypedDetail{}, false, nil
	}
	facts, found, err := ProjectThreadActivityToolFacts(call)
	if err != nil || !found {
		return ThreadActivityTypedDetail{}, found, err
	}
	envelope, err := decodeThreadActivityResultEnvelope(call, name)
	if err != nil {
		return ThreadActivityTypedDetail{}, true, err
	}
	boundary := threadActivityBoundary(call, facts, envelope)
	var value ThreadActivityTypedDetail
	switch facts.Kind {
	case "web_search":
		detail, projectionErr := projectThreadActivityWebSearch(call, facts, envelope, boundary)
		value = ThreadActivityTypedDetail{Kind: facts.Kind, WebSearch: &detail}
		err = projectionErr
	case "web_fetch":
		detail, projectionErr := projectThreadActivityWebFetch(call, facts, envelope, boundary)
		value = ThreadActivityTypedDetail{Kind: facts.Kind, WebFetch: &detail}
		err = projectionErr
	case "file_read":
		detail, projectionErr := projectThreadActivityFileRead(call, facts, envelope, boundary)
		value = ThreadActivityTypedDetail{Kind: facts.Kind, FileRead: &detail}
		err = projectionErr
	case "file_edit":
		detail, projectionErr := projectThreadActivityFileEdit(call, facts, envelope, boundary)
		value = ThreadActivityTypedDetail{Kind: facts.Kind, FileEdit: &detail}
		err = projectionErr
	case "mcp":
		detail, projectionErr := projectThreadActivityMCP(call, facts, envelope, boundary)
		value = ThreadActivityTypedDetail{Kind: facts.Kind, MCP: &detail}
		err = projectionErr
	case "verification":
		detail, projectionErr := projectThreadActivityVerification(call, facts, envelope, boundary)
		value = ThreadActivityTypedDetail{Kind: facts.Kind, Verification: &detail}
		err = projectionErr
	case "browser":
		detail, projectionErr := projectThreadActivityBrowser(call, facts, envelope, boundary)
		value = ThreadActivityTypedDetail{Kind: facts.Kind, Browser: &detail}
		err = projectionErr
	default:
		return ThreadActivityTypedDetail{}, false, nil
	}
	if err != nil {
		return ThreadActivityTypedDetail{}, true, err
	}
	if err := value.Validate(); err != nil {
		return ThreadActivityTypedDetail{}, true, err
	}
	return value, true, nil
}

func decodeThreadActivityResultEnvelope(call domain.SupervisorToolCall,
	name toolgateway.ToolName,
) (threadActivityResultEnvelope, error) {
	if strings.TrimSpace(call.ResultJSON) == "" {
		return threadActivityResultEnvelope{Version: supervisorToolResultVersion,
			Tool: string(name), Status: string(call.Status), Metadata: map[string]string{}}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(call.ResultJSON))
	var value threadActivityResultEnvelope
	if err := decoder.Decode(&value); err != nil || value.Version != supervisorToolResultVersion ||
		value.Tool != string(name) {
		return threadActivityResultEnvelope{}, errors.New("durable tool result envelope is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return threadActivityResultEnvelope{}, errors.New("durable tool result envelope has trailing JSON")
	}
	if value.Metadata == nil {
		value.Metadata = map[string]string{}
	}
	return value, nil
}

func threadActivityBoundary(call domain.SupervisorToolCall, facts ThreadActivityToolFacts,
	envelope threadActivityResultEnvelope,
) ThreadActivityBoundary {
	errorCode := facts.ErrorCode
	if errorCode == "" {
		errorCode = safeThreadActivityFactIdentity(envelope.Code)
	}
	return ThreadActivityBoundary{Authorization: facts.Authorization,
		ErrorCode: errorCode, FailureReason: threadActivityFailureReason(errorCode),
		Truncated: envelope.Truncated, Untrusted: facts.Untrusted}
}

func threadActivityFailureReason(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "policy_denied", "permission_denied", "forbidden":
		return "当前执行边界未授权此操作"
	case "robots_blocked":
		return "目标站点的 robots 策略阻止了抓取"
	case "network_unavailable", "search_unavailable", "unavailable":
		return "所需的联网能力当前不可用"
	case "invalid_argument":
		return "工具参数未通过校验"
	case "timeout", "deadline_exceeded":
		return "工具调用已超时"
	case "conflict":
		return "执行上下文已变化，请重试"
	default:
		if code != "" {
			return "工具调用未完成"
		}
		return ""
	}
}

func projectThreadActivityWebSearch(call domain.SupervisorToolCall,
	facts ThreadActivityToolFacts, envelope threadActivityResultEnvelope,
	boundary ThreadActivityBoundary,
) (ThreadActivityWebSearchDetail, error) {
	name := toolgateway.ToolName(call.ToolName)
	canonical, err := toolgateway.NormalizeWebEvidencePayload(name,
		json.RawMessage(call.PayloadJSON))
	if err != nil {
		return ThreadActivityWebSearchDetail{}, err
	}
	query, limit := "", 0
	var webInput toolgateway.WebSearchPayload
	var sourceInput toolgateway.SourceSearchPayload
	if name == toolgateway.SourceSearchTool {
		if err := json.Unmarshal(canonical, &sourceInput); err != nil {
			return ThreadActivityWebSearchDetail{}, err
		}
		query, limit = sourceInput.Query, sourceInput.Limit
	} else {
		if err := json.Unmarshal(canonical, &webInput); err != nil {
			return ThreadActivityWebSearchDetail{}, err
		}
		query, limit = webInput.Query, webInput.Limit
	}
	detail := ThreadActivityWebSearchDetail{Operation: facts.Operation,
		Query: query, Limit: limit, Boundary: boundary,
		Provider:        safeThreadActivityFactValue(envelope.Metadata["provider"]),
		SearchPolicy:    safeThreadActivityFactIdentity(envelope.Metadata["search_policy"]),
		SelectionReason: safeThreadActivityFactValue(envelope.Metadata["selection_reason"]),
		SourceCount:     safeThreadActivityCount(envelope.Metadata["source_count"]),
		Citeable:        safeThreadActivityBool(envelope.Metadata["citeable"])}
	if call.Status == domain.SupervisorToolFailed && strings.EqualFold(boundary.ErrorCode, "unavailable") {
		detail.Boundary.FailureReason = threadActivitySearchFailureReason(envelope.Message)
	}
	var sources []ThreadActivitySearchSource
	provider, ok := "", false
	if name == toolgateway.SourceSearchTool {
		sources, provider, ok = safeThreadActivitySourceSearchSources(envelope.Stdout, sourceInput)
	} else {
		sources, provider, ok = safeThreadActivitySearchSources(envelope.Stdout, webInput)
	}
	if ok {
		detail.Sources = sources
		if detail.Provider == "" {
			detail.Provider = provider
		}
		if detail.SourceCount == 0 {
			detail.SourceCount = len(sources)
		}
	}
	if detail.Sources == nil {
		detail.Sources = []ThreadActivitySearchSource{}
	}
	return detail, nil
}

func safeThreadActivitySourceSearchSources(raw string, input toolgateway.SourceSearchPayload) (
	[]ThreadActivitySearchSource, string, bool,
) {
	if strings.TrimSpace(raw) == "" {
		return nil, "", false
	}
	var result webevidence.SourceSearchResult
	decoder := json.NewDecoder(strings.NewReader(raw))
	if decoder.Decode(&result) != nil ||
		result.ProtocolVersion != webevidence.SourceSearchProtocolVersion ||
		result.Query != input.Query || len(result.Sources) > MaxThreadActivitySearchSources {
		return nil, "", false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, "", false
	}
	sources := make([]ThreadActivitySearchSource, 0, len(result.Sources))
	for index, source := range result.Sources {
		if source.Rank != index+1 || source.Rank < 1 ||
			source.Rank > MaxThreadActivitySearchSources ||
			!strings.HasPrefix(source.Provider, "source:") {
			return nil, "", false
		}
		urlValue := safeThreadActivityURL(source.CanonicalURL)
		if urlValue == "" || urlValue == "受控网页目标" {
			return nil, "", false
		}
		title := safeThreadActivityFactValue(source.Title)
		if utf8.RuneCountInString(title) > 512 {
			title = string([]rune(title)[:511]) + "…"
		}
		sources = append(sources, ThreadActivitySearchSource{Rank: source.Rank,
			Title: title, URL: urlValue, Provider: safeThreadActivityFactValue(source.Provider),
			State:    safeThreadActivityFactIdentity(string(webevidence.SourceDiscovered)),
			Citeable: false})
	}
	return sources, "source_connectors", true
}

// Only translate messages constructed by the search service from stable reason
// codes. Raw provider bodies and arbitrary durable error text remain private.
// This also improves existing saved failures without rewriting their records.
func threadActivitySearchFailureReason(message string) string {
	const prefix = "web search provider request failed ("
	const suffix = "); no fallback provider was attempted"
	if !strings.HasPrefix(message, prefix) || !strings.HasSuffix(message, suffix) {
		return "搜索服务未能返回可用结果"
	}
	reason := strings.TrimSuffix(strings.TrimPrefix(message, prefix), suffix)
	switch reason {
	case webevidence.NativeSearchReasonSearchNotPerformed:
		return "搜索服务未返回已完成的联网搜索，未取得有效检索结果"
	case webevidence.NativeSearchReasonResponseIncomplete:
		return "搜索服务未完成本次响应，未取得完整搜索结果"
	case webevidence.NativeSearchReasonResponseInvalid:
		return "搜索服务返回了无法使用的响应，软件未取得有效搜索结果"
	case webevidence.NativeSearchReasonTransportUnavailable:
		return "未能完成与搜索服务的连接或响应读取，请检查网络及服务状态"
	case webevidence.SearchFailureReasonUnreachable:
		return "未能连接到搜索服务，请检查网络与代理设置"
	case webevidence.SearchFailureReasonNoUsableResults:
		return "搜索服务已连接但未返回可用结果"
	case webevidence.NativeSearchReasonProviderRejected:
		return "搜索服务拒绝了请求，请检查模型配置、凭据和服务状态"
	case webevidence.NativeSearchReasonToolUnsupported:
		return "当前模型接口不支持所请求的搜索工具"
	case webevidence.NativeSearchReasonCredentialUnavailable:
		return "无法读取搜索服务凭据，请检查模型设置"
	case webevidence.NativeSearchReasonEndpointUnauthorized:
		return "当前网页访问范围未授权所配置的搜索服务地址"
	case webevidence.NativeSearchReasonModelMappingInvalid:
		return "搜索使用的模型映射无效，请检查模型设置"
	case webevidence.NativeSearchReasonInvalidConfiguration, webevidence.NativeSearchReasonRuntimeInvalid:
		return "搜索服务配置无效，请检查模型设置"
	default:
		return "搜索服务未能返回可用结果"
	}
}

func safeThreadActivitySearchSources(raw string, input toolgateway.WebSearchPayload) (
	[]ThreadActivitySearchSource, string, bool,
) {
	if strings.TrimSpace(raw) == "" {
		return nil, "", false
	}
	var result webevidence.SearchResult
	decoder := json.NewDecoder(strings.NewReader(raw))
	if decoder.Decode(&result) != nil || result.ProtocolVersion != webevidence.SearchProtocolVersion ||
		result.Query != input.Query || len(result.Sources) > MaxThreadActivitySearchSources {
		return nil, "", false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, "", false
	}
	provider := safeThreadActivityFactValue(result.Provider)
	sources := make([]ThreadActivitySearchSource, 0, len(result.Sources))
	for index, source := range result.Sources {
		if source.Rank != index+1 || source.Rank < 1 || source.Rank > MaxThreadActivitySearchSources {
			return nil, "", false
		}
		urlValue := safeThreadActivityURL(source.CanonicalURL)
		if urlValue == "" || urlValue == "受控网页目标" {
			return nil, "", false
		}
		title := safeThreadActivityFactValue(source.Title)
		if utf8.RuneCountInString(title) > 512 {
			title = string([]rune(title)[:511]) + "…"
		}
		state := webevidence.SourceDiscovered
		if source.Fetched {
			state = webevidence.SourceFetched
		}
		sources = append(sources, ThreadActivitySearchSource{Rank: source.Rank,
			Title: title, URL: urlValue, Provider: safeThreadActivityFactValue(source.Provider),
			State:    safeThreadActivityFactIdentity(string(state)),
			Citeable: source.Citeable})
	}
	return sources, provider, true
}

func projectThreadActivityWebFetch(call domain.SupervisorToolCall,
	facts ThreadActivityToolFacts, envelope threadActivityResultEnvelope,
	boundary ThreadActivityBoundary,
) (ThreadActivityWebFetchDetail, error) {
	detail := ThreadActivityWebFetchDetail{Operation: facts.Operation, URL: facts.Target,
		State:        safeThreadActivityFactIdentity(envelope.Metadata["state"]),
		HTTPStatus:   safeThreadActivityHTTPStatus(envelope.Metadata["http_status"]),
		Robots:       safeThreadActivityFactIdentity(envelope.Metadata["robots"]),
		RobotsPolicy: safeThreadActivityFactIdentity(envelope.Metadata["robots_policy"]),
		Redirects:    safeThreadActivityCount(envelope.Metadata["redirects"]),
		Partial:      safeThreadActivityBool(envelope.Metadata["partial"]),
		Citeable:     safeThreadActivityBool(envelope.Metadata["citeable"]), Boundary: boundary}
	if urlValue := safeThreadActivityURL(envelope.Metadata["url"]); urlValue != "" &&
		urlValue != "受控网页目标" {
		detail.URL = urlValue
	}
	if strings.TrimSpace(envelope.Stdout) != "" {
		var output webFetchToolOutput
		if json.Unmarshal([]byte(envelope.Stdout), &output) == nil &&
			output.ProtocolVersion == webevidence.FetchProtocolVersion {
			if urlValue := safeThreadActivityURL(output.Snapshot.URL); urlValue != "" &&
				urlValue != "受控网页目标" {
				detail.URL = urlValue
			}
			detail.State = safeThreadActivityFactIdentity(string(output.Snapshot.State))
			if output.Snapshot.HTTPStatus >= http.StatusContinue && output.Snapshot.HTTPStatus <= 599 {
				detail.HTTPStatus = output.Snapshot.HTTPStatus
			}
			detail.Robots = safeThreadActivityFactIdentity(output.Snapshot.Robots)
			detail.Redirects = max(0, output.Snapshot.Redirects)
			detail.Partial = output.Snapshot.State == webevidence.SourcePartial
			detail.Citeable = output.Snapshot.Citeable
		}
	}
	return detail, nil
}

func projectThreadActivityFileRead(call domain.SupervisorToolCall,
	facts ThreadActivityToolFacts, envelope threadActivityResultEnvelope,
	boundary ThreadActivityBoundary,
) (ThreadActivityFileReadDetail, error) {
	name := toolgateway.ToolName(call.ToolName)
	canonical, err := toolgateway.NormalizeAgentCodePayload(name, json.RawMessage(call.PayloadJSON))
	if err != nil {
		return ThreadActivityFileReadDetail{}, err
	}
	detail := ThreadActivityFileReadDetail{Operation: facts.Operation, Path: facts.Target,
		ResultCount: safeThreadActivityCount(envelope.Metadata["result_count"]),
		Truncated:   boundary.Truncated || safeThreadActivityBool(envelope.Metadata["truncated"]),
		Boundary:    boundary}
	switch name {
	case toolgateway.WorkspaceListTool:
		var input toolgateway.WorkspaceListPayload
		_ = json.Unmarshal(canonical, &input)
		detail.Path, detail.Limit = safeThreadActivityPath(input.Path), input.Limit
	case toolgateway.WorkspaceReadTool:
		var input toolgateway.WorkspaceReadPayload
		_ = json.Unmarshal(canonical, &input)
		detail.Path, detail.StartLine, detail.EndLine = safeThreadActivityPath(input.Path),
			input.StartLine, input.EndLine
	case toolgateway.WorkspaceGlobTool:
		var input toolgateway.WorkspaceGlobPayload
		_ = json.Unmarshal(canonical, &input)
		detail.Pattern, detail.Limit = safeThreadActivityFactValue(input.Pattern), input.Limit
	case toolgateway.WorkspaceGrepTool:
		var input toolgateway.WorkspaceGrepPayload
		_ = json.Unmarshal(canonical, &input)
		detail.Query, detail.Pattern, detail.Limit = safeThreadActivityFactValue(input.Query),
			safeThreadActivityFactValue(input.Pattern), input.Limit
	}
	detail.Summary = threadActivityResultSummary(detail.ResultCount, detail.Truncated,
		"文件读取已完成")
	return detail, nil
}

func projectThreadActivityFileEdit(call domain.SupervisorToolCall,
	facts ThreadActivityToolFacts, envelope threadActivityResultEnvelope,
	boundary ThreadActivityBoundary,
) (ThreadActivityFileEditDetail, error) {
	name := toolgateway.ToolName(call.ToolName)
	canonical, err := toolgateway.NormalizeAgentCodePayload(name, json.RawMessage(call.PayloadJSON))
	if err != nil {
		return ThreadActivityFileEditDetail{}, err
	}
	detail := ThreadActivityFileEditDetail{Operation: facts.Operation, Path: facts.Target,
		EditID:      safeThreadActivityFileEditID(envelope.Metadata["edit_id"]),
		ApplyStatus: safeThreadActivityFactIdentity(envelope.Metadata["status"]),
		Boundary:    boundary}
	switch name {
	case toolgateway.WorkspaceChangeTool:
		var input toolgateway.WorkspaceChangePayload
		_ = json.Unmarshal(canonical, &input)
		detail.Action, detail.Path = safeThreadActivityFactIdentity(input.Action),
			safeThreadActivityPath(input.Path)
		detail.DestinationPath = safeThreadActivityPath(input.DestinationPath)
		if input.Action == "propose_revert" && strings.EqualFold(boundary.ErrorCode, "CONFLICT") {
			detail.Boundary.FailureReason = "撤销请求与当前编辑状态不一致。请先核对来源编辑和当前文件，保留后续修改。"
		}
	case toolgateway.WorkspaceApplyTool:
		var input toolgateway.WorkspaceApplyPayload
		_ = json.Unmarshal(canonical, &input)
		detail.Action = safeThreadActivityFactIdentity(input.ExpectedAction)
		if detail.EditID == "" {
			detail.EditID = safeThreadActivityFileEditID(input.EditID)
		}
	case toolgateway.WorkspaceDeleteTool:
		var input toolgateway.WorkspaceDeletePayload
		_ = json.Unmarshal(canonical, &input)
		detail.Action, detail.Path = safeThreadActivityFactIdentity(input.Action),
			safeThreadActivityPath(input.Path)
		if detail.EditID == "" {
			detail.EditID = safeThreadActivityFileEditID(input.EditID)
		}
	}
	var result struct {
		Version         string `json:"version"`
		EditID          string `json:"edit_id"`
		Operation       string `json:"operation"`
		Path            string `json:"path"`
		DestinationPath string `json:"destination_path"`
		Status          string `json:"status"`
		Diff            string `json:"diff"`
		FileWritten     bool   `json:"file_written"`
		Replayed        bool   `json:"replayed"`
	}
	if json.Unmarshal([]byte(envelope.Stdout), &result) == nil &&
		result.Version == toolgateway.AgentCodeRegistryVersion {
		if detail.EditID == "" {
			detail.EditID = safeThreadActivityFileEditID(result.EditID)
		}
		if result.Operation != "" {
			detail.Action = safeThreadActivityFactIdentity(result.Operation)
		}
		if result.Path != "" {
			detail.Path = safeThreadActivityPath(result.Path)
		}
		detail.DestinationPath = safeThreadActivityPath(result.DestinationPath)
		if result.Status != "" {
			detail.ApplyStatus = safeThreadActivityFactIdentity(result.Status)
		}
		detail.FileWritten, detail.Replayed = result.FileWritten, result.Replayed
		detail.Applied = result.FileWritten || detail.ApplyStatus == "applied"
		detail.Diff = safeThreadActivityDiffSummary(result.Diff, detail.Action,
			detail.Path, detail.DestinationPath)
	}
	if detail.Diff.Summary == "" {
		detail.Diff.Summary = threadActivityEditSummary(detail.Action, detail.Path,
			detail.DestinationPath, detail.ApplyStatus)
	}
	return detail, nil
}

// safeThreadActivityFileEditID preserves only a bounded single path segment.
// The ID remains an opaque reference; the Run binding is re-established by the
// exact file-edit endpoint before any diff is returned.
func safeThreadActivityFileEditID(value string) string {
	value = safeThreadActivityFactIdentity(value)
	if value == "" || strings.ContainsAny(value, `/\\`) {
		return ""
	}
	return value
}

var threadActivityDiffHunkPattern = regexp.MustCompile(`^@@ -\d+(?:,(\d+))? \+\d+(?:,(\d+))? @@.*$`)

func safeThreadActivityDiffSummary(raw, action, source, destination string) ThreadActivityDiffSummary {
	if raw == "" || !utf8.ValidString(raw) {
		return ThreadActivityDiffSummary{}
	}
	value := ThreadActivityDiffSummary{}
	oldRemaining, newRemaining := 0, 0
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if oldRemaining > 0 || newRemaining > 0 {
			switch {
			case strings.HasPrefix(line, "-") && oldRemaining > 0:
				value.RemovedLines++
				oldRemaining--
				continue
			case strings.HasPrefix(line, "+") && newRemaining > 0:
				value.AddedLines++
				newRemaining--
				continue
			case strings.HasPrefix(line, " ") && oldRemaining > 0 && newRemaining > 0:
				oldRemaining--
				newRemaining--
				continue
			case strings.HasPrefix(line, "\\ No newline"):
				continue
			}
		}
		oldRemaining, newRemaining = 0, 0
		if hunk := threadActivityDiffHunkPattern.FindStringSubmatch(line); hunk != nil {
			value.Hunks++
			oldRemaining, newRemaining = 1, 1
			if hunk[1] != "" {
				oldRemaining, _ = strconv.Atoi(hunk[1])
			}
			if hunk[2] != "" {
				newRemaining, _ = strconv.Atoi(hunk[2])
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			value.AddedLines++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			value.RemovedLines++
		}
	}
	value.Summary = threadActivityEditSummary(action, source, destination, "")
	if value.AddedLines > 0 || value.RemovedLines > 0 {
		value.Summary = fmt.Sprintf("%s · +%d −%d", value.Summary,
			value.AddedLines, value.RemovedLines)
	}
	value.Summary = safeThreadActivityFactValue(value.Summary)
	if utf8.RuneCountInString(value.Summary) > MaxThreadActivityDiffSummary {
		value.Summary = string([]rune(value.Summary)[:MaxThreadActivityDiffSummary-1]) + "…"
	}
	return value
}

func threadActivityEditSummary(action, source, destination, status string) string {
	pathValue := source
	if pathValue == "" {
		pathValue = "工作区文件"
	}
	var summary string
	switch action {
	case "move":
		summary = fmt.Sprintf("移动 %s 至 %s", pathValue, destination)
	case "delete":
		summary = "删除 " + pathValue
	case "create":
		summary = "创建 " + pathValue
	default:
		summary = "修改 " + pathValue
	}
	if status != "" {
		summary += " · " + status
	}
	return safeThreadActivityFactValue(summary)
}

func projectThreadActivityMCP(call domain.SupervisorToolCall, facts ThreadActivityToolFacts,
	envelope threadActivityResultEnvelope, boundary ThreadActivityBoundary,
) (ThreadActivityMCPDetail, error) {
	input, err := decodeThreadActivityMCPPayload(json.RawMessage(call.PayloadJSON))
	if err != nil {
		return ThreadActivityMCPDetail{}, err
	}
	arguments := summarizeThreadActivityJSONObject(input.Arguments)
	result := summarizeThreadActivityJSON([]byte(envelope.Stdout))
	if result.Type == "" {
		result = ThreadActivityJSONSummary{Type: "unavailable", Summary: "无可展示的结果摘要",
			Fields: []ThreadActivityJSONFieldSummary{}}
	}
	return ThreadActivityMCPDetail{Operation: facts.Operation,
		Server: input.ServerID, Tool: input.ToolName, Arguments: arguments,
		Result: result, Boundary: boundary}, nil
}

func decodeThreadActivityMCPPayload(raw json.RawMessage) (toolgateway.MCPToolCallPayload, error) {
	if len(raw) < 2 || len(raw) > toolgateway.MaxArgumentValueBytes || !utf8.Valid(raw) {
		return toolgateway.MCPToolCallPayload{}, errors.New("durable MCP payload is not bounded UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value toolgateway.MCPToolCallPayload
	if decoder.Decode(&value) != nil {
		return toolgateway.MCPToolCallPayload{}, errors.New("durable MCP payload does not match its schema")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return toolgateway.MCPToolCallPayload{}, errors.New("durable MCP payload has trailing JSON")
	}
	value.ServerID = strings.TrimSpace(value.ServerID)
	value.ToolName = strings.TrimSpace(value.ToolName)
	value.CapabilityFingerprint = strings.TrimSpace(value.CapabilityFingerprint)
	value.Arguments = append(json.RawMessage(nil), bytes.TrimSpace(value.Arguments)...)
	arguments, validArguments := decodeThreadActivityJSON(value.Arguments)
	_, isObject := arguments.(map[string]any)
	if value.Version != toolgateway.MCPClientToolProtocolVersion ||
		!validThreadActivityMCPIdentity(value.ServerID) ||
		!validThreadActivityMCPIdentity(value.ToolName) ||
		!validThreadActivityDigest(value.CapabilityFingerprint) || !validArguments || !isObject {
		return toolgateway.MCPToolCallPayload{}, errors.New("durable MCP payload is invalid")
	}
	return value, nil
}

func validThreadActivityMCPIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= 256 && !strings.ContainsRune(value, 0) &&
		!strings.ContainsAny(value, "/\\") && safeThreadActivityFactValue(value) == value
}

func validThreadActivityDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, current := range value {
		if !(current >= '0' && current <= '9') && !(current >= 'a' && current <= 'f') {
			return false
		}
	}
	return true
}

func summarizeThreadActivityJSONObject(raw json.RawMessage) []ThreadActivityJSONFieldSummary {
	value, ok := decodeThreadActivityJSON(raw)
	if !ok {
		return []ThreadActivityJSONFieldSummary{}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return []ThreadActivityJSONFieldSummary{}
	}
	return summarizeThreadActivityFields(object)
}

func summarizeThreadActivityJSON(raw []byte) ThreadActivityJSONSummary {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ThreadActivityJSONSummary{}
	}
	value, ok := decodeThreadActivityJSON(raw)
	if !ok {
		preview := safeThreadActivityJSONScalar(string(raw))
		return ThreadActivityJSONSummary{Type: "text", Count: utf8.RuneCount(raw),
			Summary: fmt.Sprintf("文本结果 · %s", preview),
			Fields:  []ThreadActivityJSONFieldSummary{}}
	}
	kind, count, summary := threadActivityJSONShape(value)
	if _, isObject := value.(map[string]any); !isObject {
		if _, isArray := value.([]any); !isArray {
			_, _, summary = threadActivityJSONFieldShape("", value)
		}
	}
	result := ThreadActivityJSONSummary{Type: kind, Count: count, Summary: summary,
		Fields: []ThreadActivityJSONFieldSummary{}}
	if object, isObject := value.(map[string]any); isObject {
		result.Fields = summarizeThreadActivityFields(object)
	}
	return result
}

func decodeThreadActivityJSON(raw []byte) (any, bool) {
	if len(raw) > toolgateway.MaxResultStdoutBytes || !utf8.Valid(raw) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return value, true
}

func summarizeThreadActivityFields(object map[string]any) []ThreadActivityJSONFieldSummary {
	keys := make([]string, 0, len(object))
	for key := range object {
		redactedKey := redact.Text(key)
		if safeThreadActivityArgumentKey(key) != "" && len(redactedKey.Findings) == 0 &&
			redactedKey.Text == key {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > MaxThreadActivityMCPFields {
		keys = keys[:MaxThreadActivityMCPFields]
	}
	result := make([]ThreadActivityJSONFieldSummary, 0, len(keys))
	for _, key := range keys {
		kind, _, summary := threadActivityJSONFieldShape(key, object[key])
		result = append(result, ThreadActivityJSONFieldSummary{Name: key, Type: kind,
			Summary: summary})
	}
	return result
}

func threadActivityJSONFieldShape(key string, value any) (string, int, string) {
	kind, count, shape := threadActivityJSONShape(value)
	if sensitiveThreadActivityJSONKey(key) {
		return kind, count, "[已脱敏]"
	}
	switch typed := value.(type) {
	case nil:
		return kind, count, "null"
	case bool:
		return kind, count, strconv.FormatBool(typed)
	case json.Number:
		return kind, count, typed.String()
	case float64:
		return kind, count, strconv.FormatFloat(typed, 'g', -1, 64)
	case string:
		return kind, count, safeThreadActivityJSONScalar(typed)
	default:
		return kind, count, shape
	}
}

func sensitiveThreadActivityJSONKey(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(normalized)
	compact := strings.ReplaceAll(normalized, "_", "")
	for _, token := range []string{"apikey", "token", "secret", "password", "passwd",
		"privatekey", "authorization", "cookie", "setcookie", "stdin", "environment",
		"credential", "bearer"} {
		if strings.Contains(compact, token) {
			return true
		}
	}
	if normalized == "env" || strings.HasPrefix(normalized, "env_") ||
		strings.HasSuffix(normalized, "_env") {
		return true
	}
	return false
}

func safeThreadActivityJSONScalar(value string) string {
	redacted := redact.Text(value)
	if len(redacted.Findings) > 0 || redacted.Text != value {
		return "[已脱敏]"
	}
	const maxRunes = 160
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > maxRunes {
		value = string([]rune(value)[:maxRunes-1]) + "…"
	}
	if value == "" {
		return "空字符串"
	}
	return value
}

func threadActivityJSONShape(value any) (string, int, string) {
	switch typed := value.(type) {
	case nil:
		return "null", 0, "null"
	case bool:
		return "boolean", 1, "布尔值（内容已隐藏）"
	case json.Number, float64:
		return "number", 1, "数字（内容已隐藏）"
	case string:
		length := utf8.RuneCountInString(typed)
		return "string", length, fmt.Sprintf("字符串 · %d 个字符（内容已隐藏）", length)
	case []any:
		return "array", len(typed), fmt.Sprintf("数组 · %d 项（内容已隐藏）", len(typed))
	case map[string]any:
		return "object", len(typed), fmt.Sprintf("对象 · %d 个字段（值已隐藏）", len(typed))
	default:
		return "unknown", 0, "内容已隐藏"
	}
}

func projectThreadActivityVerification(call domain.SupervisorToolCall,
	facts ThreadActivityToolFacts, envelope threadActivityResultEnvelope,
	boundary ThreadActivityBoundary,
) (ThreadActivityVerificationDetail, error) {
	name := toolgateway.ToolName(call.ToolName)
	detail := ThreadActivityVerificationDetail{Operation: facts.Operation,
		Tool: string(name), Path: facts.Target,
		ResultCount: safeThreadActivityCount(envelope.Metadata["result_count"]),
		Truncated:   boundary.Truncated || safeThreadActivityBool(envelope.Metadata["truncated"]),
		Boundary:    boundary}
	if toolgateway.IsCodeIntelTool(name) {
		input, _, err := toolgateway.NormalizeCodeIntelPayload(name,
			json.RawMessage(call.PayloadJSON))
		if err != nil {
			return ThreadActivityVerificationDetail{}, err
		}
		detail.Path, detail.Query = safeThreadActivityPath(input.Path),
			safeThreadActivityFactValue(input.Query)
		if input.Line != 0 || input.Character != 0 {
			detail.Position = fmt.Sprintf("%d:%d", input.Line+1, input.Character+1)
		}
		detail.Direction, detail.Limit = safeThreadActivityFactIdentity(input.Direction), input.Limit
	}
	detail.Summary = threadActivityResultSummary(detail.ResultCount, detail.Truncated,
		"验证已完成")
	return detail, nil
}

func projectThreadActivityBrowser(call domain.SupervisorToolCall,
	facts ThreadActivityToolFacts, envelope threadActivityResultEnvelope,
	boundary ThreadActivityBoundary,
) (ThreadActivityBrowserDetail, error) {
	name := toolgateway.ToolName(call.ToolName)
	canonical, err := toolgateway.NormalizeBrowserActionPayload(name,
		json.RawMessage(call.PayloadJSON))
	if err != nil {
		return ThreadActivityBrowserDetail{}, err
	}
	var input toolgateway.BrowserActionPayload
	_ = json.Unmarshal(canonical, &input)
	detail := ThreadActivityBrowserDetail{Operation: facts.Operation, Action: string(name),
		URL: safeThreadActivityURL(input.URL), Selector: safeThreadActivityFactValue(input.Selector),
		ArtifactBytes: int64(safeThreadActivityCount(envelope.Metadata["artifact_bytes"])),
		Summary:       "浏览器操作已完成", Boundary: boundary}
	if name == toolgateway.BrowserTypeTool {
		detail.InputLength = utf8.RuneCountInString(input.Value)
	}
	return detail, nil
}

func threadActivityResultSummary(count int, truncated bool, fallback string) string {
	value := fallback
	if count > 0 {
		value = fmt.Sprintf("返回 %d 项结果", count)
	}
	if truncated {
		value += " · 已截断"
	}
	return value
}

func safeThreadActivityCount(value string) int {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil || parsed < 0 {
		return 0
	}
	return int(parsed)
}

func safeThreadActivityHTTPStatus(value string) int {
	status := safeThreadActivityCount(value)
	if status < http.StatusContinue || status > 599 {
		return 0
	}
	return status
}

func safeThreadActivityBool(value string) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && parsed
}

func (v ThreadActivityTypedDetail) Validate() error {
	branches := 0
	for _, present := range []bool{v.Command != nil, v.WebSearch != nil, v.WebFetch != nil,
		v.FileRead != nil, v.FileEdit != nil, v.MCP != nil, v.Verification != nil,
		v.Browser != nil} {
		if present {
			branches++
		}
	}
	if branches != 1 {
		return errors.New("Thread activity detail must carry exactly one typed branch")
	}
	switch v.Kind {
	case "command":
		if v.Command == nil || len(v.Command.Commands) > MaxThreadActivityCommands {
			return errors.New("Thread command activity detail is invalid")
		}
	case "web_search":
		if v.WebSearch == nil || len(v.WebSearch.Sources) > MaxThreadActivitySearchSources ||
			v.WebSearch.Limit < 1 || v.WebSearch.Limit > MaxThreadActivitySearchSources ||
			v.WebSearch.SourceCount < 0 || !validThreadActivityBranchText(v.WebSearch.Operation, false) ||
			!validThreadActivityBranchText(v.WebSearch.Query, false) ||
			!validThreadActivityBoundary(v.WebSearch.Boundary) {
			return errors.New("Thread Web search activity detail is invalid")
		}
		for index, source := range v.WebSearch.Sources {
			if source.Rank != index+1 || !validThreadActivityBranchText(source.URL, false) ||
				!validThreadActivityBranchText(source.Title, true) ||
				!validThreadActivityBranchText(source.Provider, true) {
				return errors.New("Thread Web search source is invalid")
			}
		}
	case "web_fetch":
		if v.WebFetch == nil || v.WebFetch.Redirects < 0 ||
			(v.WebFetch.HTTPStatus != 0 && (v.WebFetch.HTTPStatus < 100 || v.WebFetch.HTTPStatus > 599)) ||
			!validThreadActivityBranchText(v.WebFetch.Operation, false) ||
			!validThreadActivityBoundary(v.WebFetch.Boundary) {
			return errors.New("Thread Web fetch activity detail is invalid")
		}
	case "file_read":
		if v.FileRead == nil || v.FileRead.StartLine < 0 || v.FileRead.EndLine < 0 ||
			v.FileRead.Limit < 0 || v.FileRead.ResultCount < 0 ||
			!validThreadActivityBranchText(v.FileRead.Operation, false) ||
			!validThreadActivityBoundary(v.FileRead.Boundary) {
			return errors.New("Thread file-read activity detail is invalid")
		}
	case "file_edit":
		if v.FileEdit == nil || v.FileEdit.Diff.AddedLines < 0 ||
			v.FileEdit.Diff.RemovedLines < 0 || v.FileEdit.Diff.Hunks < 0 ||
			!validThreadActivityFactIdentity(v.FileEdit.EditID, true) ||
			strings.ContainsAny(v.FileEdit.EditID, `/\\`) ||
			(v.FileEdit.DiffAvailable && v.FileEdit.EditID == "") ||
			!validThreadActivityBranchText(v.FileEdit.Operation, false) ||
			!validThreadActivityBoundary(v.FileEdit.Boundary) {
			return errors.New("Thread file-edit activity detail is invalid")
		}
	case "mcp":
		if v.MCP == nil || len(v.MCP.Arguments) > MaxThreadActivityMCPFields ||
			len(v.MCP.Result.Fields) > MaxThreadActivityMCPFields ||
			!validThreadActivityBranchText(v.MCP.Operation, false) ||
			!validThreadActivityBranchText(v.MCP.Server, false) ||
			!validThreadActivityBranchText(v.MCP.Tool, false) ||
			!validThreadActivityBoundary(v.MCP.Boundary) ||
			!validThreadActivityJSONFields(v.MCP.Arguments) ||
			!validThreadActivityJSONSummary(v.MCP.Result) {
			return errors.New("Thread MCP activity detail is invalid")
		}
	case "verification":
		if v.Verification == nil || v.Verification.Limit < 0 ||
			v.Verification.ResultCount < 0 ||
			!validThreadActivityBranchText(v.Verification.Operation, false) ||
			!validThreadActivityBranchText(v.Verification.Tool, false) ||
			!validThreadActivityBoundary(v.Verification.Boundary) {
			return errors.New("Thread verification activity detail is invalid")
		}
	case "browser":
		if v.Browser == nil || v.Browser.InputLength < 0 || v.Browser.ArtifactBytes < 0 ||
			!validThreadActivityBranchText(v.Browser.Operation, false) ||
			!validThreadActivityBranchText(v.Browser.Action, false) ||
			!validThreadActivityBoundary(v.Browser.Boundary) {
			return errors.New("Thread browser activity detail is invalid")
		}
	default:
		return errors.New("Thread activity detail kind is invalid")
	}
	return nil
}

func validThreadActivityJSONSummary(value ThreadActivityJSONSummary) bool {
	if value.Count < 0 || !validThreadActivityFactIdentity(value.Type, false) ||
		!validThreadActivityBranchText(value.Summary, false) {
		return false
	}
	return validThreadActivityJSONFields(value.Fields)
}

func validThreadActivityBoundary(value ThreadActivityBoundary) bool {
	return threadActivityAuthorizations[value.Authorization] &&
		validThreadActivityFactIdentity(value.ErrorCode, true) &&
		validThreadActivityBranchText(value.FailureReason, true)
}

func validThreadActivityBranchText(value string, optional bool) bool {
	return validThreadActivityFactText(value, MaxThreadActivityFactValueRunes, optional)
}

func validThreadActivityJSONFields(values []ThreadActivityJSONFieldSummary) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if safeThreadActivityArgumentKey(value.Name) != value.Name ||
			!validThreadActivityFactIdentity(value.Type, false) ||
			!validThreadActivityBranchText(value.Summary, false) {
			return false
		}
		if _, exists := seen[value.Name]; exists {
			return false
		}
		seen[value.Name] = struct{}{}
	}
	return true
}

package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

type threadSearchStore interface {
	SearchThreadMessages(context.Context, string, bool, time.Time, string, int) ([]domain.ThreadSearchMatch, error)
	GetThreadSearchMessage(context.Context, string, string) (domain.ThreadSearchMatch, error)
}

type ThreadSearchResultView struct {
	ID           string    `json:"id"`
	ThreadID     string    `json:"thread_id"`
	ThreadTitle  string    `json:"thread_title"`
	ThreadStatus string    `json:"thread_status"`
	WorkspaceID  string    `json:"workspace_id"`
	MessageID    string    `json:"message_id,omitempty"`
	Role         string    `json:"role"`
	Snippet      string    `json:"snippet"`
	CreatedAt    time.Time `json:"created_at"`
}

type ThreadSavedMessageView struct {
	MessageID        string `json:"message_id"`
	Content          string `json:"content"`
	TranscriptCursor string `json:"transcript_cursor"`
}

func (a *API) searchThreadMessages(request *http.Request) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "q", "include_archived", "limit", "cursor"); err != nil {
		return nil, nil, err
	}
	for key, entries := range values {
		if len(entries) != 1 {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument, "query parameter "+strconv.Quote(key)+" must appear exactly once")
		}
	}
	query, err := domain.NormalizeThreadTitleQuery(values.Get("q"))
	if err != nil || query == "" {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument, "Search requires between 1 and 256 characters")
	}
	values.Set("q", query)
	includeArchived := false
	if raw, exists := singleQueryValue(values, "include_archived"); exists {
		includeArchived, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument, "include_archived must be true or false")
		}
	}
	values.Set("include_archived", strconv.FormatBool(includeArchived))
	pageRequest, err := parseStableListPage(values, request.URL.Path)
	if err != nil {
		return nil, nil, err
	}
	reader, ok := a.store.(threadSearchStore)
	if !ok {
		return nil, nil, apperror.New(apperror.CodeNotFound, "Saved message search is unavailable")
	}
	items, err := reader.SearchThreadMessages(request.Context(), query, includeArchived,
		pageRequest.Anchor.BeforeCreatedAt, pageRequest.Anchor.BeforeID, stableListStoreLimit(pageRequest))
	if err != nil {
		return nil, nil, err
	}
	items, page := trimStableListPage(items, pageRequest, func(item domain.ThreadSearchMatch) (time.Time, string) { return item.CreatedAt, item.ID })
	views := make([]ThreadSearchResultView, len(items))
	for index, item := range items {
		views[index] = ThreadSearchResultView{ID: item.ID, ThreadID: item.ThreadID, ThreadTitle: item.ThreadTitle,
			ThreadStatus: string(item.ThreadStatus), WorkspaceID: item.WorkspaceID, MessageID: item.MessageID,
			Role: item.Role, Snippet: searchSnippet(item.Content, query), CreatedAt: item.CreatedAt}
	}
	return views, page, nil
}

func searchSnippet(content, query string) string {
	content = strings.Join(strings.Fields(content), " ")
	runes := []rune(content)
	fold := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, content)
	start := 0
	if at := strings.Index(fold, strings.Join(strings.Fields(query), " ")); at >= 0 {
		start = len([]rune(fold[:at])) - 45
	}
	if start < 0 {
		start = 0
	}
	end := start + 360
	if end > len(runes) {
		end = len(runes)
	}
	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(runes) {
		snippet += "…"
	}
	return snippet
}

func (a *API) savedThreadMessage(request *http.Request, threadID, messageID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	if err := validateIdentity(messageID, "saved message identity"); err != nil {
		return nil, nil, err
	}
	reader, ok := a.store.(threadSearchStore)
	if !ok {
		return nil, nil, apperror.New(apperror.CodeNotFound, "Saved message search is unavailable")
	}
	item, err := reader.GetThreadSearchMessage(request.Context(), threadID, messageID)
	if err != nil {
		return nil, nil, err
	}
	// The existing transcript cursor opens directly at the target source window;
	// no traversal of newer pages or Run boundaries is necessary.
	raw, _ := json.Marshal(threadTranscriptCursor{Version: threadTranscriptCursorVersion,
		Scope:         pageScope(ThreadCollectionPath+"/"+url.PathEscape(threadID)+"/transcript", url.Values{}),
		BeforeOrdinal: item.RunOrdinal, BeforeSequence: item.Sequence + 1, Consumed: 1})
	return ThreadSavedMessageView{MessageID: item.MessageID, Content: item.Content,
		TranscriptCursor: base64.RawURLEncoding.EncodeToString(raw)}, nil, nil
}

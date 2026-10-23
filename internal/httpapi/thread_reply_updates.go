package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

const (
	ThreadReplyUpdatesPath    = "/api/v1/threads/reply-updates"
	ThreadReplyUpdatesVersion = "thread_reply_updates.v1"
)

type threadReplyUpdateStore interface {
	DataStoreID() string
	ListThreadReplyUpdates(context.Context, string, int) ([]domain.ThreadSearchMatch, string, bool, error)
}

type ThreadReplyUpdateView struct {
	ThreadID     string    `json:"thread_id"`
	MessageID    string    `json:"message_id"`
	ThreadStatus string    `json:"thread_status"`
	RunOrdinal   int64     `json:"run_ordinal"`
	Sequence     int64     `json:"sequence"`
	CreatedAt    time.Time `json:"created_at"`
}

type ThreadReplyUpdatesView struct {
	Version     string                  `json:"version"`
	DataStoreID string                  `json:"data_store_id"`
	Cursor      string                  `json:"cursor"`
	HasMore     bool                    `json:"has_more"`
	Items       []ThreadReplyUpdateView `json:"items"`
}

func (a *API) threadReplyUpdates(request *http.Request) (any, *Page, error) {
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "cursor", "limit"); err != nil {
		return nil, nil, err
	}
	limit := DefaultPageLimit
	if raw, exists := singleQueryValue(values, "limit"); exists {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > MaxPageLimit {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument, "reply update limit must be between 1 and 100")
		}
	}
	cursor, present := singleQueryValue(values, "cursor")
	if present && cursor == "" {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument, "reply update cursor must not be empty")
	}
	reader, ok := a.store.(threadReplyUpdateStore)
	if !ok || reader.DataStoreID() == "" {
		return nil, nil, apperror.New(apperror.CodeUnavailable, "reply updates require a stable data store identity")
	}
	items, next, more, err := reader.ListThreadReplyUpdates(request.Context(), cursor, limit)
	if err != nil {
		return nil, nil, err
	}
	view := ThreadReplyUpdatesView{Version: ThreadReplyUpdatesVersion, DataStoreID: reader.DataStoreID(),
		Cursor: next, HasMore: more, Items: make([]ThreadReplyUpdateView, 0, len(items))}
	for _, item := range items {
		view.Items = append(view.Items, ThreadReplyUpdateView{ThreadID: item.ThreadID, MessageID: item.MessageID,
			ThreadStatus: string(item.ThreadStatus), RunOrdinal: item.RunOrdinal, Sequence: item.Sequence, CreatedAt: item.CreatedAt})
	}
	return view, nil, nil
}

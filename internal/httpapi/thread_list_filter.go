package httpapi

import (
	"context"

	"cyberagent-workbench/internal/domain"
)

// Scan stored creation pages rather than filtering the browser's loaded history.
// A bounded scan can return an empty page with a continuation cursor.
func (a *API) filteredThreadList(ctx context.Context, filter domain.ThreadFilter, request stableListPageRequest, state string) (any, *Page, error) {
	const batchSize, scanBudget = 100, 1000
	items := make([]ThreadView, 0, request.Limit)
	page := &Page{Limit: request.Limit}
	anchor := request.Anchor
	scanned := 0
	for scanned < scanBudget && anchor.Consumed < maxStoreCursorOffset {
		size := min(batchSize, scanBudget-scanned, maxStoreCursorOffset-anchor.Consumed)
		filter.Limit = size + 1
		batch, err := a.store.ListThreadsByCreationPage(ctx, filter, anchor.BeforeCreatedAt, anchor.BeforeID)
		if err != nil {
			return nil, nil, err
		}
		more := len(batch) > size
		if more {
			batch = batch[:size]
		}
		views := a.threadListViews(ctx, batch)
		for index, thread := range batch {
			scanned++
			anchor = stableListPageAnchor{BeforeCreatedAt: thread.CreatedAt, BeforeID: thread.ID, Consumed: anchor.Consumed + 1}
			if views[index].ExecutionState == state {
				items = append(items, views[index])
			}
			if len(items) == request.Limit || scanned == scanBudget || anchor.Consumed == maxStoreCursorOffset {
				if more || index+1 < len(batch) {
					var ok bool
					page.NextCursor, ok = encodeStableListCursor(request.Scope, anchor)
					page.Truncated = !ok
				}
				return items, page, nil
			}
		}
		if !more {
			return items, page, nil
		}
	}
	return items, page, nil
}

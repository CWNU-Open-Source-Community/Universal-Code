package store

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

func (s *SQLiteStore) RenameThread(ctx context.Context, id, title string) (domain.Thread, error) {
	title, err := domain.NormalizeThreadTitle(title)
	if err != nil {
		return domain.Thread{}, err
	}
	return s.updateThreadMetadata(ctx, id, "title", title)
}

func (s *SQLiteStore) PinThread(ctx context.Context, id string, pinned bool) (domain.Thread, error) {
	return s.updateThreadMetadata(ctx, id, "pinned", pinned)
}

func (s *SQLiteStore) updateThreadMetadata(ctx context.Context, id, field string, value any) (domain.Thread, error) {
	// field is selected by the two methods above; callers cannot supply SQL.
	result, err := s.db.ExecContext(ctx, `UPDATE threads SET `+field+`=?,version=version+1,updated_at=? WHERE id=? AND status<>'deleted' AND `+field+`<>?`, value, ts(time.Now().UTC()), id, value)
	if err != nil {
		return domain.Thread{}, err
	}
	if _, err = result.RowsAffected(); err != nil {
		return domain.Thread{}, err
	}
	thread, err := s.GetThread(ctx, id)
	if err == nil && thread.Status == domain.ThreadDeleted {
		err = apperror.New(apperror.CodeNotFound, "Conversation was not found")
	}
	return thread, err
}

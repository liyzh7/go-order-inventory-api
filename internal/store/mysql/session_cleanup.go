package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"internshiplab/order-api/internal/sessioncleanup"
)

type SessionCleanupStore struct {
	db      *sql.DB
	timeout time.Duration
}

var _ sessioncleanup.Store = (*SessionCleanupStore)(nil)

func NewSessionCleanupStore(db *sql.DB, timeout time.Duration) *SessionCleanupStore {
	return &SessionCleanupStore{db: db, timeout: timeout}
}

// DeleteBatch is one autocommit statement: concurrent login/logout cannot
// cause a session outside the predicate to be selected and deleted later.
func (s *SessionCleanupStore) DeleteBatch(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if limit < 1 || limit > sessioncleanup.MaxBatchSize {
		return 0, fmt.Errorf("%w: batch size outside safe limits", sessioncleanup.ErrInvalidOptions)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions
		WHERE expires_at <= ? OR (revoked_at IS NOT NULL AND revoked_at <= ?)
		ORDER BY id ASC LIMIT ?`, cutoff.UTC(), cutoff.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("delete eligible sessions: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read deleted session count: %w", err)
	}
	return deleted, nil
}

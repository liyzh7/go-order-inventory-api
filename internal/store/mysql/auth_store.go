package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"internshiplab/order-api/internal/auth"
)

type AuthStore struct {
	db      *sql.DB
	timeout time.Duration
}

func NewAuthStore(db *sql.DB, timeout time.Duration) *AuthStore {
	return &AuthStore{db: db, timeout: timeout}
}
func (s *AuthStore) CreateUser(ctx context.Context, name, hash string) (auth.User, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `INSERT INTO users (username,password_hash,status) VALUES (?,?,'ACTIVE')`, name, hash)
	if isDuplicateKey(err) {
		return auth.User{}, auth.ErrRegistration
	}
	if err != nil {
		return auth.User{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return auth.User{}, err
	}
	return auth.User{ID: uint64(id), Username: name, Status: "ACTIVE"}, nil
}
func (s *AuthStore) FindUser(ctx context.Context, name string) (auth.User, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var user auth.User
	err := s.db.QueryRowContext(ctx, `SELECT id,username,password_hash,status FROM users WHERE username=?`, name).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.User{}, auth.ErrNotFound
	}
	return user, err
}
func (s *AuthStore) CreateSession(ctx context.Context, userID uint64, hash [32]byte, expires time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	// Recheck status at insertion so disabling a user during password work
	// cannot create a usable session. Every subsequent request checks again.
	result, err := s.db.ExecContext(ctx, `INSERT INTO auth_sessions (user_id,token_hash,expires_at) SELECT id,?,? FROM users WHERE id=? AND status='ACTIVE'`, hash[:], expires, userID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return auth.ErrCredentials
	}
	return nil
}
func (s *AuthStore) FindSession(ctx context.Context, hash [32]byte) (auth.Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var id auth.Identity
	err := s.db.QueryRowContext(ctx, `SELECT u.id,s.id,u.username FROM auth_sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.revoked_at IS NULL AND s.expires_at>UTC_TIMESTAMP(6) AND u.status='ACTIVE'`, hash[:]).Scan(&id.UserID, &id.SessionID, &id.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Identity{}, auth.ErrUnauthorized
	}
	return id, err
}
func (s *AuthStore) RevokeSession(ctx context.Context, userID, sessionID uint64) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at=COALESCE(revoked_at,UTC_TIMESTAMP(6)) WHERE id=? AND user_id=?`, sessionID, userID)
	return err
}

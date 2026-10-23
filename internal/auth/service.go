package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrUnauthorized = errors.New("authentication required")
	ErrCredentials  = errors.New("invalid username or password")
	ErrRegistration = errors.New("registration could not be completed")
	ErrInvalidInput = errors.New("invalid authentication input")
	ErrBusy         = errors.New("authentication capacity reached")
	ErrNotFound     = errors.New("auth record not found")
)

type User struct {
	ID           uint64 `json:"id"`
	Username     string `json:"username"`
	Status       string `json:"-"`
	PasswordHash string `json:"-"`
}

type Identity struct {
	UserID    uint64
	SessionID uint64
	Username  string
}

type identityKey struct{}

// WithIdentity is for trusted server components; HTTP input never populates it
// except after the session middleware has validated the token against MySQL.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok && id.UserID > 0
}

type Store interface {
	CreateUser(context.Context, string, string) (User, error)
	FindUser(context.Context, string) (User, error)
	CreateSession(context.Context, uint64, [32]byte, time.Time) error
	FindSession(context.Context, [32]byte) (Identity, error)
	RevokeSession(context.Context, uint64, uint64) error
}

type LoginResult struct {
	User        User      `json:"user"`
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type Service struct {
	store         Store
	dummyHash     string
	passwordSlots chan struct{}
}

func NewService(store Store) (*Service, error) {
	// Missing usernames still incur the same password work as existing users.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	dummy, err := HashPassword(base64.RawURLEncoding.EncodeToString(secret))
	if err != nil {
		return nil, err
	}
	return &Service{store: store, dummyHash: dummy, passwordSlots: make(chan struct{}, 2)}, nil
}

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{2,31}$`)

func normalizeCredentials(username, password string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) || !utf8.ValidString(password) || len(password) < 12 || len(password) > 128 {
		return "", ErrInvalidInput
	}
	return username, nil
}
func (s *Service) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.passwordSlots <- struct{}{}:
		return nil
	default:
		return ErrBusy
	}
}
func (s *Service) Register(ctx context.Context, username, password string) (User, error) {
	name, err := normalizeCredentials(username, password)
	if err != nil {
		return User{}, err
	}
	if err = s.acquire(ctx); err != nil {
		return User{}, err
	}
	defer func() { <-s.passwordSlots }()
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	return s.store.CreateUser(ctx, name, hash)
}
func (s *Service) Login(ctx context.Context, username, password string) (LoginResult, error) {
	name, err := normalizeCredentials(username, password)
	if err != nil {
		return LoginResult{}, ErrCredentials
	}
	if err = s.acquire(ctx); err != nil {
		return LoginResult{}, err
	}
	defer func() { <-s.passwordSlots }()
	user, err := s.store.FindUser(ctx, name)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return LoginResult{}, err
	}
	hash := s.dummyHash
	if err == nil {
		hash = user.PasswordHash
	}
	valid := VerifyPassword(password, hash)
	if !valid || err != nil || user.Status != "ACTIVE" {
		return LoginResult{}, ErrCredentials
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return LoginResult{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	expires := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)
	if err = s.store.CreateSession(ctx, user.ID, sha256.Sum256([]byte(token)), expires); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{User: user, AccessToken: token, TokenType: "Bearer", ExpiresAt: expires}, nil
}
func (s *Service) Authenticate(ctx context.Context, token string) (Identity, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(decoded) != 32 || len(token) != 43 {
		return Identity{}, ErrUnauthorized
	}
	return s.store.FindSession(ctx, sha256.Sum256([]byte(token)))
}
func (s *Service) Logout(ctx context.Context) error {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return ErrUnauthorized
	}
	return s.store.RevokeSession(ctx, id.UserID, id.SessionID)
}

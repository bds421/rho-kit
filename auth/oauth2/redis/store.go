package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/core/v2/secret"
)

const (
	defaultPrefix = "rho-kit:oauth2:"
	maxValueBytes = 128 << 10
)

// Store implements [oauth2.SessionStore] and exposes its state-store view via
// [Store.States]. It is safe for concurrent use when its Redis client is safe
// for concurrent use.
type Store struct {
	client goredis.UniversalClient
	prefix string
	sealer *sealer
}

// Option configures a Store.
type Option func(*Store)

// WithPrefix sets the Redis namespace. The prefix is not derived from a user
// or tenant value, so untrusted input cannot broaden key access.
func WithPrefix(prefix string) Option {
	if !validKeyPart(prefix) {
		panic("oauth2/redis: WithPrefix requires a non-empty printable prefix")
	}
	return func(s *Store) { s.prefix = prefix }
}

// New constructs a durable store from a go-redis client. It does not ping at
// construction time so lifecycle wiring controls dependency readiness.
func New(client goredis.UniversalClient, opts ...Option) *Store {
	if client == nil {
		panic("oauth2/redis: New requires a non-nil Redis client")
	}
	s := &Store{client: client, prefix: defaultPrefix}
	for _, opt := range opts {
		if opt == nil {
			panic("oauth2/redis: New option must not be nil")
		}
		opt(s)
	}
	return s
}

var (
	_ oauth2.SessionStore   = (*Store)(nil)
	_ oauth2.SessionUpdater = (*Store)(nil)
	_ oauth2.RefreshLocker  = (*Store)(nil)
	_ oauth2.SessionIndex   = (*Store)(nil)
	_ oauth2.ReplayCache    = (*Store)(nil)
	_ oauth2.StateStore     = StateStore{}
)

type sessionWire struct {
	SessionID    string         `json:"session_id"`
	UserID       string         `json:"user_id"`
	SID          string         `json:"sid,omitempty"`
	AuthTime     time.Time      `json:"auth_time,omitzero"`
	AMR          []string       `json:"amr,omitempty"`
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token,omitempty"`
	IDToken      string         `json:"id_token,omitempty"`
	Expiry       time.Time      `json:"expiry"`
	Claims       map[string]any `json:"claims,omitempty"`
	Version      uint64         `json:"version,omitempty"`
	CreatedAt    time.Time      `json:"created_at,omitzero"`
	LastActivity time.Time      `json:"last_activity,omitzero"`
}

func (s *Store) Put(ctx context.Context, sessionID string, session oauth2.Session, ttl time.Duration) error {
	if err := validateWrite(ctx, sessionID, ttl); err != nil {
		return err
	}
	key := s.sessionKey(sessionID)
	b, err := s.encodeSession(key, session)
	if err != nil {
		return err
	}
	if _, err := s.client.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		p.Set(ctx, key, b, ttl)
		s.queueIndexAdd(ctx, p, sessionID, session, ttl)
		return nil
	}); err != nil {
		return redact.WrapError("oauth2/redis: store session", err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, sessionID string) (oauth2.Session, error) {
	if err := validateRead(ctx, sessionID); err != nil {
		return oauth2.Session{}, err
	}
	key := s.sessionKey(sessionID)
	b, err := s.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return oauth2.Session{}, oauth2.ErrSessionNotFound
	}
	if err != nil {
		return oauth2.Session{}, redact.WrapError("oauth2/redis: load session", err)
	}
	return s.decodeSession(key, b)
}

// Update implements [oauth2.SessionUpdater] with WATCH/MULTI: the session is
// replaced only if it still exists with expectedVersion and nobody wrote or
// deleted it between the read and the transaction. Returns false when the
// entry is gone, unreadable with the configured keys, or changed.
func (s *Store) Update(ctx context.Context, sessionID string, expectedVersion uint64, session oauth2.Session, ttl time.Duration) (bool, error) {
	if err := validateWrite(ctx, sessionID, ttl); err != nil {
		return false, err
	}
	key := s.sessionKey(sessionID)
	b, err := s.encodeSession(key, session)
	if err != nil {
		return false, err
	}
	updated := false
	err = s.client.Watch(ctx, func(tx *goredis.Tx) error {
		raw, err := tx.Get(ctx, key).Bytes()
		if errors.Is(err, goredis.Nil) {
			return nil
		}
		if err != nil {
			return err
		}
		current, err := s.decodeSession(key, raw)
		if errors.Is(err, oauth2.ErrSessionNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if current.Version != expectedVersion {
			return nil
		}
		if _, err := tx.TxPipelined(ctx, func(p goredis.Pipeliner) error {
			p.Set(ctx, key, b, ttl)
			if current.SID != session.SID || current.UserID != session.UserID {
				s.queueIndexRemove(ctx, p, sessionID, current)
			}
			s.queueIndexAdd(ctx, p, sessionID, session, ttl)
			return nil
		}); err != nil {
			return err
		}
		updated = true
		return nil
	}, key)
	if errors.Is(err, goredis.TxFailedErr) {
		return false, nil
	}
	if err != nil {
		return false, redact.WrapError("oauth2/redis: update session", err)
	}
	return updated, nil
}

func (s *Store) encodeSession(key string, session oauth2.Session) ([]byte, error) {
	w := sessionWire{
		SessionID: session.SessionID, UserID: session.UserID, SID: session.SID, Expiry: session.Expiry, Claims: session.Claims, Version: session.Version,
		CreatedAt: session.CreatedAt, LastActivity: session.LastActivity, AuthTime: session.AuthTime, AMR: session.AMR,
	}
	if session.AccessToken != nil {
		w.AccessToken = session.AccessToken.RevealString()
	}
	if session.RefreshToken != nil {
		w.RefreshToken = session.RefreshToken.RevealString()
	}
	if session.IDToken != nil {
		w.IDToken = session.IDToken.RevealString()
	}
	b, err := json.Marshal(w)
	if err != nil {
		return nil, redact.WrapError("oauth2/redis: encode session", err)
	}
	if len(b) > maxValueBytes {
		return nil, errors.New("oauth2/redis: session exceeds maximum size")
	}
	return s.sealValue(key, b)
}

func (s *Store) decodeSession(key string, b []byte) (oauth2.Session, error) {
	if len(b) > s.maxStoredBytes() {
		return oauth2.Session{}, errors.New("oauth2/redis: stored session exceeds maximum size")
	}
	b, err := s.openValue(key, b)
	if errors.Is(err, errNotDecryptable) {
		return oauth2.Session{}, oauth2.ErrSessionNotFound
	} else if err != nil {
		return oauth2.Session{}, err
	}
	var w sessionWire
	if err := json.Unmarshal(b, &w); err != nil {
		return oauth2.Session{}, errors.New("oauth2/redis: stored session is invalid")
	}
	session := oauth2.Session{
		SessionID: w.SessionID, UserID: w.UserID, SID: w.SID, Expiry: w.Expiry, Claims: w.Claims, Version: w.Version,
		CreatedAt: w.CreatedAt, LastActivity: w.LastActivity, AuthTime: w.AuthTime, AMR: w.AMR,
	}
	if w.AccessToken != "" {
		session.AccessToken = secret.NewFromString(w.AccessToken)
	}
	if w.RefreshToken != "" {
		session.RefreshToken = secret.NewFromString(w.RefreshToken)
	}
	if w.IDToken != "" {
		session.IDToken = secret.NewFromString(w.IDToken)
	}
	return session, nil
}

func (s *Store) Delete(ctx context.Context, sessionID string) error {
	if err := validateRead(ctx, sessionID); err != nil {
		return err
	}
	key := s.sessionKey(sessionID)
	// Read the session first to find its index entries. An unreadable value
	// is still deleted; its index entries are pruned by later lookups.
	var indexed oauth2.Session
	if raw, err := s.client.Get(ctx, key).Bytes(); err == nil {
		indexed, _ = s.decodeSession(key, raw)
	} else if !errors.Is(err, goredis.Nil) {
		return redact.WrapError("oauth2/redis: delete session", err)
	}
	if _, err := s.client.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		p.Del(ctx, key)
		s.queueIndexRemove(ctx, p, sessionID, indexed)
		return nil
	}); err != nil {
		return redact.WrapError("oauth2/redis: delete session", err)
	}
	return nil
}

func (s *Store) PutState(ctx context.Context, state string, entry oauth2.StateEntry, ttl time.Duration) error {
	if err := validateWrite(ctx, state, ttl); err != nil {
		return err
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return redact.WrapError("oauth2/redis: encode state", err)
	}
	if len(b) > maxValueBytes {
		return errors.New("oauth2/redis: state exceeds maximum size")
	}
	key := s.stateKey(state)
	if b, err = s.sealValue(key, b); err != nil {
		return err
	}
	if err := s.client.Set(ctx, key, b, ttl).Err(); err != nil {
		return redact.WrapError("oauth2/redis: store state", err)
	}
	return nil
}

// GetState returns ErrStateNotFound for an absent or expired state token.
func (s *Store) GetState(ctx context.Context, state string) (oauth2.StateEntry, error) {
	if err := validateRead(ctx, state); err != nil {
		return oauth2.StateEntry{}, err
	}
	key := s.stateKey(state)
	b, err := s.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return oauth2.StateEntry{}, oauth2.ErrStateNotFound
	}
	if err != nil {
		return oauth2.StateEntry{}, redact.WrapError("oauth2/redis: load state", err)
	}
	if len(b) > s.maxStoredBytes() {
		return oauth2.StateEntry{}, errors.New("oauth2/redis: stored state exceeds maximum size")
	}
	if b, err = s.openValue(key, b); errors.Is(err, errNotDecryptable) {
		return oauth2.StateEntry{}, oauth2.ErrStateNotFound
	} else if err != nil {
		return oauth2.StateEntry{}, err
	}
	var entry oauth2.StateEntry
	if err := json.Unmarshal(b, &entry); err != nil {
		return oauth2.StateEntry{}, errors.New("oauth2/redis: stored state is invalid")
	}
	return entry, nil
}

func (s *Store) DeleteState(ctx context.Context, state string) error {
	if err := validateRead(ctx, state); err != nil {
		return err
	}
	if err := s.client.Del(ctx, s.stateKey(state)).Err(); err != nil {
		return redact.WrapError("oauth2/redis: delete state", err)
	}
	return nil
}

// StateStore adapts Store to the exact oauth2.StateStore method names without
// making session and state keys overlap in Redis.
type StateStore struct{ *Store }

func (s StateStore) Put(ctx context.Context, state string, entry oauth2.StateEntry, ttl time.Duration) error {
	return s.PutState(ctx, state, entry, ttl)
}
func (s StateStore) Get(ctx context.Context, state string) (oauth2.StateEntry, error) {
	return s.GetState(ctx, state)
}
func (s StateStore) Delete(ctx context.Context, state string) error {
	return s.DeleteState(ctx, state)
}

// States returns the state-store view for use with oauth2.WithStateStore.
func (s *Store) States() oauth2.StateStore { return StateStore{Store: s} }

func (s *Store) sessionKey(id string) string { return s.prefix + "session:" + id }
func (s *Store) stateKey(id string) string   { return s.prefix + "state:" + id }

func validateWrite(ctx context.Context, key string, ttl time.Duration) error {
	if err := validateRead(ctx, key); err != nil {
		return err
	}
	if ttl <= 0 {
		return errors.New("oauth2/redis: TTL must be positive")
	}
	return nil
}

func validateRead(ctx context.Context, key string) error {
	if ctx == nil {
		return errors.New("oauth2/redis: context must not be nil")
	}
	if !validKeyPart(key) {
		return errors.New("oauth2/redis: key must be non-empty printable text")
	}
	return nil
}

func validKeyPart(v string) bool {
	if v == "" || len(v) > 512 || !utf8.ValidString(v) || strings.TrimSpace(v) != v {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// SessionStore returns Store itself for readable app wiring.
func (s *Store) SessionStore() oauth2.SessionStore { return s }

func (s *Store) String() string { return fmt.Sprintf("oauth2/redis store(%s)", s.prefix) }

// sealValue encrypts b for key when [WithEncryption] is configured.
func (s *Store) sealValue(key string, b []byte) ([]byte, error) {
	if s.sealer == nil {
		return b, nil
	}
	return s.sealer.seal(key, b)
}

// openValue decrypts b read from key when [WithEncryption] is configured.
func (s *Store) openValue(key string, b []byte) ([]byte, error) {
	if s.sealer == nil {
		return b, nil
	}
	return s.sealer.open(key, b)
}

func (s *Store) maxStoredBytes() int {
	if s.sealer == nil {
		return maxValueBytes
	}
	return maxValueBytes + sealOverhead
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Identity struct {
	Platform         string
	UserID           string
	ConversationType string
	ConversationID   string
}

type Credential struct {
	ClientID     string
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresAt    time.Time
	Scope        string
	Resource     string
}

type LoginSession struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ClientID                string
	ExpiresAt               time.Time
	IntervalSeconds         int
	Status                  string
}

type Interaction struct {
	RawText string
	Command string
	Args    string
	Handled bool
	Reply   string
	Error   string
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if path == "" {
		path = filepath.Join(".run", "life-ustc-bot.db")
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			platform TEXT NOT NULL,
			external_user_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(platform, external_user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS credentials (
			user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			client_id TEXT NOT NULL,
			access_token TEXT NOT NULL,
			refresh_token TEXT,
			token_type TEXT,
			expires_at TEXT NOT NULL,
			scope TEXT,
			resource TEXT,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS login_sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			device_code TEXT NOT NULL,
			user_code TEXT NOT NULL,
			verification_uri TEXT NOT NULL,
			verification_uri_complete TEXT,
			client_id TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			interval_seconds INTEGER NOT NULL,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_login_sessions_user_status ON login_sessions(user_id, status, expires_at)`,
		`CREATE TABLE IF NOT EXISTS conversation_states (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			platform TEXT NOT NULL,
			conversation_type TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			last_command TEXT NOT NULL,
			state TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS interactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			platform TEXT NOT NULL,
			conversation_type TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			raw_text TEXT NOT NULL,
			command TEXT,
			args TEXT,
			handled INTEGER NOT NULL,
			reply TEXT,
			error TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_interactions_conversation_created
			ON interactions(platform, conversation_type, conversation_id, created_at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) EnsureUser(ctx context.Context, ident Identity) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (platform, external_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(platform, external_user_id) DO UPDATE SET updated_at = excluded.updated_at`,
		ident.Platform, ident.UserID, now, now)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE platform = ? AND external_user_id = ?`, ident.Platform, ident.UserID).Scan(&id)
	return id, err
}

func (s *Store) SaveCredential(ctx context.Context, ident Identity, cred Credential) error {
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `INSERT INTO credentials
		(user_id, client_id, access_token, refresh_token, token_type, expires_at, scope, resource, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			client_id = excluded.client_id,
			access_token = excluded.access_token,
			refresh_token = excluded.refresh_token,
			token_type = excluded.token_type,
			expires_at = excluded.expires_at,
			scope = excluded.scope,
			resource = excluded.resource,
			updated_at = excluded.updated_at`,
		userID, cred.ClientID, cred.AccessToken, cred.RefreshToken, cred.TokenType,
		cred.ExpiresAt.UTC().Format(time.RFC3339), cred.Scope, cred.Resource, now)
	return err
}

func (s *Store) Credential(ctx context.Context, ident Identity) (*Credential, error) {
	var cred Credential
	var expires string
	err := s.db.QueryRowContext(ctx, `SELECT c.client_id, c.access_token, c.refresh_token, c.token_type,
			c.expires_at, c.scope, c.resource
		FROM credentials c
		JOIN users u ON u.id = c.user_id
		WHERE u.platform = ? AND u.external_user_id = ?`,
		ident.Platform, ident.UserID).Scan(&cred.ClientID, &cred.AccessToken, &cred.RefreshToken,
		&cred.TokenType, &expires, &cred.Scope, &cred.Resource)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cred.ExpiresAt, err = time.Parse(time.RFC3339, expires)
	if err != nil {
		return nil, fmt.Errorf("parse credential expiry: %w", err)
	}
	return &cred, nil
}

func (s *Store) DeleteCredential(ctx context.Context, ident Identity) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE user_id IN (
		SELECT id FROM users WHERE platform = ? AND external_user_id = ?
	)`, ident.Platform, ident.UserID)
	return err
}

func (s *Store) SaveLoginSession(ctx context.Context, ident Identity, session LoginSession) error {
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `UPDATE login_sessions SET status = 'superseded', updated_at = ?
		WHERE user_id = ? AND status = 'pending'`, now, userID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO login_sessions
		(user_id, device_code, user_code, verification_uri, verification_uri_complete, client_id,
		 expires_at, interval_seconds, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, session.DeviceCode, session.UserCode, session.VerificationURI,
		session.VerificationURIComplete, session.ClientID, session.ExpiresAt.UTC().Format(time.RFC3339),
		session.IntervalSeconds, session.Status, now, now)
	return err
}

func (s *Store) ActiveLoginSession(ctx context.Context, ident Identity) (*LoginSession, error) {
	var session LoginSession
	var expires string
	err := s.db.QueryRowContext(ctx, `SELECT s.device_code, s.user_code, s.verification_uri,
			s.verification_uri_complete, s.client_id, s.expires_at, s.interval_seconds, s.status
		FROM login_sessions s
		JOIN users u ON u.id = s.user_id
		WHERE u.platform = ? AND u.external_user_id = ? AND s.status = 'pending'
		ORDER BY s.id DESC LIMIT 1`,
		ident.Platform, ident.UserID).Scan(&session.DeviceCode, &session.UserCode, &session.VerificationURI,
		&session.VerificationURIComplete, &session.ClientID, &expires, &session.IntervalSeconds, &session.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	session.ExpiresAt, err = time.Parse(time.RFC3339, expires)
	if err != nil {
		return nil, fmt.Errorf("parse login expiry: %w", err)
	}
	return &session, nil
}

func (s *Store) MarkLoginSession(ctx context.Context, ident Identity, deviceCode, status string) error {
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `UPDATE login_sessions SET status = ?, updated_at = ?
		WHERE user_id = ? AND device_code = ?`, status, now, userID, deviceCode)
	return err
}

func (s *Store) RecordConversationState(ctx context.Context, ident Identity, command, state string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversation_states
		(platform, conversation_type, conversation_id, user_id, last_command, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ident.Platform, ident.ConversationType, ident.ConversationID, ident.UserID, command, state, now)
	return err
}

func (s *Store) RecordInteraction(ctx context.Context, ident Identity, interaction Interaction) error {
	now := time.Now().UTC().Format(time.RFC3339)
	handled := 0
	if interaction.Handled {
		handled = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO interactions
		(platform, conversation_type, conversation_id, user_id, raw_text, command, args, handled, reply, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ident.Platform, ident.ConversationType, ident.ConversationID, ident.UserID,
		interaction.RawText, interaction.Command, interaction.Args, handled, interaction.Reply, interaction.Error, now)
	return err
}

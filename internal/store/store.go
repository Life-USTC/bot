package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	Direction string
	RawText   string
	Command   string
	Args      string
	Handled   bool
	Reply     string
	Status    string
	Error     string
}

type Store struct {
	db *gorm.DB
}

type userRow struct {
	ID             int64  `gorm:"primaryKey"`
	Platform       string `gorm:"not null;uniqueIndex:idx_users_platform_external_user"`
	ExternalUserID string `gorm:"not null;uniqueIndex:idx_users_platform_external_user"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (userRow) TableName() string {
	return "users"
}

type credentialRow struct {
	UserID       int64  `gorm:"primaryKey"`
	ClientID     string `gorm:"not null"`
	AccessToken  string `gorm:"not null"`
	RefreshToken string
	TokenType    string
	ExpiresAt    time.Time `gorm:"not null"`
	Scope        string
	Resource     string
	UpdatedAt    time.Time
}

func (credentialRow) TableName() string {
	return "credentials"
}

type loginSessionRow struct {
	ID                      int64 `gorm:"primaryKey"`
	UserID                  int64 `gorm:"not null;index:idx_login_sessions_user_status"`
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ClientID                string
	ExpiresAt               time.Time `gorm:"not null;index:idx_login_sessions_user_status"`
	IntervalSeconds         int
	Status                  string `gorm:"not null;index:idx_login_sessions_user_status"`
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

func (loginSessionRow) TableName() string {
	return "login_sessions"
}

type conversationStateRow struct {
	ID               int64  `gorm:"primaryKey"`
	Platform         string `gorm:"not null"`
	ConversationType string `gorm:"not null"`
	ConversationID   string `gorm:"not null"`
	UserID           string `gorm:"not null"`
	LastCommand      string `gorm:"not null"`
	State            string
	CreatedAt        time.Time
}

func (conversationStateRow) TableName() string {
	return "conversation_states"
}

type interactionRow struct {
	ID               int64  `gorm:"primaryKey"`
	Platform         string `gorm:"not null;index:idx_interactions_conversation_created"`
	ConversationType string `gorm:"not null;index:idx_interactions_conversation_created"`
	ConversationID   string `gorm:"not null;index:idx_interactions_conversation_created"`
	UserID           string `gorm:"not null"`
	Direction        string `gorm:"not null;default:inbound"`
	RawText          string `gorm:"not null"`
	Command          string
	Args             string
	Handled          bool `gorm:"not null"`
	Reply            string
	Status           string
	Error            string
	CreatedAt        time.Time `gorm:"index:idx_interactions_conversation_created"`
}

func (interactionRow) TableName() string {
	return "interactions"
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
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func (s *Store) migrate() error {
	if err := s.db.Exec(`PRAGMA journal_mode = WAL`).Error; err != nil {
		return err
	}
	return s.db.AutoMigrate(
		&userRow{},
		&credentialRow{},
		&loginSessionRow{},
		&conversationStateRow{},
		&interactionRow{},
	)
}

func (s *Store) EnsureUser(ctx context.Context, ident Identity) (int64, error) {
	now := time.Now().UTC()
	user := userRow{
		Platform:       ident.Platform,
		ExternalUserID: ident.UserID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "platform"}, {Name: "external_user_id"}},
		DoUpdates: clause.Assignments(map[string]any{"updated_at": now}),
	}).Create(&user).Error
	if err != nil {
		return 0, err
	}
	err = s.db.WithContext(ctx).
		Where("platform = ? AND external_user_id = ?", ident.Platform, ident.UserID).
		First(&user).Error
	return user.ID, err
}

func (s *Store) SaveCredential(ctx context.Context, ident Identity, cred Credential) error {
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return err
	}
	row := credentialRow{
		UserID:       userID,
		ClientID:     cred.ClientID,
		AccessToken:  cred.AccessToken,
		RefreshToken: cred.RefreshToken,
		TokenType:    cred.TokenType,
		ExpiresAt:    cred.ExpiresAt.UTC(),
		Scope:        cred.Scope,
		Resource:     cred.Resource,
		UpdatedAt:    time.Now().UTC(),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"client_id",
			"access_token",
			"refresh_token",
			"token_type",
			"expires_at",
			"scope",
			"resource",
			"updated_at",
		}),
	}).Create(&row).Error
}

func (s *Store) Credential(ctx context.Context, ident Identity) (*Credential, error) {
	var row credentialRow
	err := s.db.WithContext(ctx).
		Joins("JOIN users ON users.id = credentials.user_id").
		Where("users.platform = ? AND users.external_user_id = ?", ident.Platform, ident.UserID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Credential{
		ClientID:     row.ClientID,
		AccessToken:  row.AccessToken,
		RefreshToken: row.RefreshToken,
		TokenType:    row.TokenType,
		ExpiresAt:    row.ExpiresAt,
		Scope:        row.Scope,
		Resource:     row.Resource,
	}, nil
}

func (s *Store) DeleteCredential(ctx context.Context, ident Identity) error {
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Delete(&credentialRow{}, "user_id = ?", userID).Error
}

func (s *Store) SaveLoginSession(ctx context.Context, ident Identity, session LoginSession) error {
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&loginSessionRow{}).
			Where("user_id = ? AND status = ?", userID, "pending").
			Updates(map[string]any{"status": "superseded", "updated_at": now}).Error; err != nil {
			return err
		}
		row := loginSessionRow{
			UserID:                  userID,
			DeviceCode:              session.DeviceCode,
			UserCode:                session.UserCode,
			VerificationURI:         session.VerificationURI,
			VerificationURIComplete: session.VerificationURIComplete,
			ClientID:                session.ClientID,
			ExpiresAt:               session.ExpiresAt.UTC(),
			IntervalSeconds:         session.IntervalSeconds,
			Status:                  session.Status,
			CreatedAt:               now,
			UpdatedAt:               now,
		}
		return tx.Create(&row).Error
	})
}

func (s *Store) ActiveLoginSession(ctx context.Context, ident Identity) (*LoginSession, error) {
	var row loginSessionRow
	err := s.db.WithContext(ctx).
		Joins("JOIN users ON users.id = login_sessions.user_id").
		Where("users.platform = ? AND users.external_user_id = ? AND login_sessions.status = ?",
			ident.Platform, ident.UserID, "pending").
		Order("login_sessions.id DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &LoginSession{
		DeviceCode:              row.DeviceCode,
		UserCode:                row.UserCode,
		VerificationURI:         row.VerificationURI,
		VerificationURIComplete: row.VerificationURIComplete,
		ClientID:                row.ClientID,
		ExpiresAt:               row.ExpiresAt,
		IntervalSeconds:         row.IntervalSeconds,
		Status:                  row.Status,
	}, nil
}

func (s *Store) MarkLoginSession(ctx context.Context, ident Identity, deviceCode, status string) error {
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&loginSessionRow{}).
		Where("user_id = ? AND device_code = ?", userID, deviceCode).
		Updates(map[string]any{"status": status, "updated_at": time.Now().UTC()}).Error
}

func (s *Store) RecordConversationState(ctx context.Context, ident Identity, command, state string) error {
	row := conversationStateRow{
		Platform:         ident.Platform,
		ConversationType: ident.ConversationType,
		ConversationID:   ident.ConversationID,
		UserID:           ident.UserID,
		LastCommand:      command,
		State:            state,
		CreatedAt:        time.Now().UTC(),
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

func (s *Store) RecordInteraction(ctx context.Context, ident Identity, interaction Interaction) error {
	direction := interaction.Direction
	if direction == "" {
		direction = "inbound"
	}
	row := interactionRow{
		Platform:         ident.Platform,
		ConversationType: ident.ConversationType,
		ConversationID:   ident.ConversationID,
		UserID:           ident.UserID,
		Direction:        direction,
		RawText:          interaction.RawText,
		Command:          interaction.Command,
		Args:             interaction.Args,
		Handled:          interaction.Handled,
		Reply:            interaction.Reply,
		Status:           interaction.Status,
		Error:            interaction.Error,
		CreatedAt:        time.Now().UTC(),
	}
	if row.RawText == "" {
		return fmt.Errorf("interaction raw text is empty")
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

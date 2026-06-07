package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/Life-USTC/Bot/internal/textutil"
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
	Identity                Identity
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
	CreatedAt time.Time
}

type NotificationSettings struct {
	Identity        Identity
	ClassesEnabled  bool
	HomeworkEnabled bool
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
	Platform                string
	ExternalUserID          string
	ConversationType        string
	ConversationID          string
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

type notificationSettingRow struct {
	UserID           int64  `gorm:"primaryKey"`
	Platform         string `gorm:"not null"`
	ExternalUserID   string `gorm:"not null"`
	ConversationType string
	ConversationID   string
	ClassesEnabled   bool `gorm:"not null"`
	HomeworkEnabled  bool `gorm:"not null"`
	UpdatedAt        time.Time
}

func (notificationSettingRow) TableName() string {
	return "notification_settings"
}

type notificationDeliveryRow struct {
	ID        int64  `gorm:"primaryKey"`
	UserID    int64  `gorm:"not null;uniqueIndex:idx_notification_deliveries_user_kind_key"`
	Kind      string `gorm:"not null;uniqueIndex:idx_notification_deliveries_user_kind_key"`
	ItemKey   string `gorm:"not null;uniqueIndex:idx_notification_deliveries_user_kind_key"`
	CreatedAt time.Time
}

func (notificationDeliveryRow) TableName() string {
	return "notification_deliveries"
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
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
		&notificationSettingRow{},
		&notificationDeliveryRow{},
	)
}

func (s *Store) EnsureUser(ctx context.Context, ident Identity) (int64, error) {
	if err := validateIdentity(ident); err != nil {
		return 0, err
	}
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

func validateIdentity(ident Identity) error {
	if strings.TrimSpace(ident.Platform) == "" {
		return errors.New("identity platform is empty")
	}
	if strings.TrimSpace(ident.UserID) == "" {
		return errors.New("identity user id is empty")
	}
	return nil
}

func validateConversationIdentity(ident Identity) error {
	if err := validateIdentity(ident); err != nil {
		return err
	}
	if strings.TrimSpace(ident.ConversationType) == "" {
		return errors.New("identity conversation type is empty")
	}
	if strings.TrimSpace(ident.ConversationID) == "" {
		return errors.New("identity conversation id is empty")
	}
	return nil
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
			Where("user_id = ? AND status IN ?", userID, activeLoginSessionStatuses()).
			Updates(map[string]any{"status": "superseded", "updated_at": now}).Error; err != nil {
			return err
		}
		row := loginSessionRow{
			UserID:                  userID,
			Platform:                ident.Platform,
			ExternalUserID:          ident.UserID,
			ConversationType:        ident.ConversationType,
			ConversationID:          ident.ConversationID,
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
		Identity: Identity{
			Platform:         textutil.FirstNonEmpty(row.Platform, ident.Platform),
			UserID:           textutil.FirstNonEmpty(row.ExternalUserID, ident.UserID),
			ConversationType: row.ConversationType,
			ConversationID:   row.ConversationID,
		},
	}, nil
}

func (s *Store) PendingLoginSessions(ctx context.Context) ([]LoginSession, error) {
	var rows []loginSessionRow
	err := s.db.WithContext(ctx).
		Joins("JOIN users ON users.id = login_sessions.user_id").
		Where("login_sessions.status IN ?", activeLoginSessionStatuses()).
		Order("login_sessions.id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	sessions := make([]LoginSession, 0, len(rows))
	for _, row := range rows {
		ident := Identity{
			Platform:         textutil.FirstNonEmpty(row.Platform, ""),
			UserID:           textutil.FirstNonEmpty(row.ExternalUserID, ""),
			ConversationType: row.ConversationType,
			ConversationID:   row.ConversationID,
		}
		if ident.Platform == "" || ident.UserID == "" {
			var user userRow
			if err := s.db.WithContext(ctx).First(&user, row.UserID).Error; err != nil {
				return nil, err
			}
			ident.Platform = user.Platform
			ident.UserID = user.ExternalUserID
		}
		sessions = append(sessions, LoginSession{
			DeviceCode:              row.DeviceCode,
			UserCode:                row.UserCode,
			VerificationURI:         row.VerificationURI,
			VerificationURIComplete: row.VerificationURIComplete,
			ClientID:                row.ClientID,
			ExpiresAt:               row.ExpiresAt,
			IntervalSeconds:         row.IntervalSeconds,
			Status:                  row.Status,
			Identity:                ident,
		})
	}
	return sessions, nil
}

func activeLoginSessionStatuses() []string {
	return []string{"pending", "notify_failed"}
}

func (s *Store) MarkLoginSession(ctx context.Context, ident Identity, deviceCode, status string) error {
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return err
	}
	deviceCode, status, err = normalizeLoginSessionUpdate(deviceCode, status)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&loginSessionRow{}).
		Where("user_id = ? AND device_code = ?", userID, deviceCode).
		Updates(map[string]any{"status": status, "updated_at": time.Now().UTC()}).Error
}

func normalizeLoginSessionUpdate(deviceCode, status string) (string, string, error) {
	deviceCode = strings.TrimSpace(deviceCode)
	status = strings.TrimSpace(status)
	if deviceCode == "" {
		return "", "", errors.New("login session device code is empty")
	}
	if status == "" {
		return "", "", errors.New("login session status is empty")
	}
	return deviceCode, status, nil
}

func (s *Store) RecordConversationState(ctx context.Context, ident Identity, command, state string) error {
	if err := validateConversationIdentity(ident); err != nil {
		return err
	}
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
	if err := validateConversationIdentity(ident); err != nil {
		return err
	}
	row := interactionRow{
		Platform:         ident.Platform,
		ConversationType: ident.ConversationType,
		ConversationID:   ident.ConversationID,
		UserID:           ident.UserID,
		Direction:        interactionDirection(interaction.Direction),
		RawText:          interaction.RawText,
		Command:          interaction.Command,
		Args:             interaction.Args,
		Handled:          interaction.Handled,
		Reply:            interaction.Reply,
		Status:           interaction.Status,
		Error:            interaction.Error,
		CreatedAt:        time.Now().UTC(),
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

func interactionDirection(direction string) string {
	trimmed := strings.TrimSpace(direction)
	normalized := strings.ToLower(trimmed)
	switch normalized {
	case "":
		return "inbound"
	case "inbound", "outbound":
		return normalized
	default:
		return trimmed
	}
}

func (s *Store) RecentHandledInteractions(ctx context.Context, ident Identity, limit int) ([]Interaction, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []interactionRow
	err := s.db.WithContext(ctx).
		Where("platform = ? AND conversation_type = ? AND conversation_id = ? AND direction = ? AND handled = ? AND status = ?",
			ident.Platform, ident.ConversationType, ident.ConversationID, "inbound", true, "handled").
		Order("created_at desc").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]Interaction, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		out = append(out, Interaction{
			Direction: row.Direction,
			RawText:   row.RawText,
			Command:   row.Command,
			Args:      row.Args,
			Handled:   row.Handled,
			Reply:     row.Reply,
			Status:    row.Status,
			Error:     row.Error,
			CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

func (s *Store) InteractionCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&interactionRow{}).Count(&count).Error
	return count, err
}

func (s *Store) NotificationSettings(ctx context.Context, ident Identity) (NotificationSettings, error) {
	var row notificationSettingRow
	err := s.db.WithContext(ctx).
		Joins("JOIN users ON users.id = notification_settings.user_id").
		Where("users.platform = ? AND users.external_user_id = ?", ident.Platform, ident.UserID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return NotificationSettings{Identity: ident}, nil
	}
	if err != nil {
		return NotificationSettings{}, err
	}
	return notificationSettingsFromRow(row, ident), nil
}

func (s *Store) SaveNotificationSettings(ctx context.Context, settings NotificationSettings) error {
	userID, err := s.EnsureUser(ctx, settings.Identity)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	row := notificationSettingRow{
		UserID:           userID,
		Platform:         settings.Identity.Platform,
		ExternalUserID:   settings.Identity.UserID,
		ConversationType: settings.Identity.ConversationType,
		ConversationID:   settings.Identity.ConversationID,
		ClassesEnabled:   settings.ClassesEnabled,
		HomeworkEnabled:  settings.HomeworkEnabled,
		UpdatedAt:        now,
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"platform",
			"external_user_id",
			"conversation_type",
			"conversation_id",
			"classes_enabled",
			"homework_enabled",
			"updated_at",
		}),
	}).Create(&row).Error
}

func (s *Store) EnabledNotificationSettings(ctx context.Context) ([]NotificationSettings, error) {
	var rows []notificationSettingRow
	err := s.db.WithContext(ctx).
		Where("(classes_enabled = ? OR homework_enabled = ?) AND conversation_type <> ? AND conversation_id <> ?",
			true, true, "", "").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]NotificationSettings, 0, len(rows))
	for _, row := range rows {
		out = append(out, notificationSettingsFromRow(row, Identity{}))
	}
	return out, nil
}

func (s *Store) TryRecordNotificationDelivery(ctx context.Context, ident Identity, kind, itemKey string) (bool, error) {
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return false, err
	}
	kind, itemKey, err = normalizeNotificationDeliveryKey(kind, itemKey)
	if err != nil {
		return false, err
	}
	row := notificationDeliveryRow{
		UserID:    userID,
		Kind:      kind,
		ItemKey:   itemKey,
		CreatedAt: time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "kind"}, {Name: "item_key"}},
		DoNothing: true,
	}).Create(&row)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (s *Store) NotificationDelivered(ctx context.Context, ident Identity, kind, itemKey string) (bool, error) {
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return false, err
	}
	kind, itemKey, err = normalizeNotificationDeliveryKey(kind, itemKey)
	if err != nil {
		return false, err
	}
	var count int64
	err = s.db.WithContext(ctx).Model(&notificationDeliveryRow{}).
		Where("user_id = ? AND kind = ? AND item_key = ?", userID, kind, itemKey).
		Count(&count).Error
	return count > 0, err
}

func normalizeNotificationDeliveryKey(kind, itemKey string) (string, string, error) {
	kind = strings.TrimSpace(kind)
	itemKey = strings.TrimSpace(itemKey)
	if kind == "" {
		return "", "", errors.New("notification delivery kind is empty")
	}
	if itemKey == "" {
		return "", "", errors.New("notification delivery item key is empty")
	}
	return kind, itemKey, nil
}

func notificationSettingsFromRow(row notificationSettingRow, fallback Identity) NotificationSettings {
	ident := Identity{
		Platform:         textutil.FirstNonEmpty(row.Platform, fallback.Platform),
		UserID:           textutil.FirstNonEmpty(row.ExternalUserID, fallback.UserID),
		ConversationType: textutil.FirstNonEmpty(row.ConversationType, fallback.ConversationType),
		ConversationID:   textutil.FirstNonEmpty(row.ConversationID, fallback.ConversationID),
	}
	return NotificationSettings{
		Identity:        ident,
		ClassesEnabled:  row.ClassesEnabled,
		HomeworkEnabled: row.HomeworkEnabled,
	}
}

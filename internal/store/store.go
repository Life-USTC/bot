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
	// Resource is a space-separated list of requested OAuth resource indicators.
	Resource string
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
	// Resources is a space-separated list of OAuth resource indicators requested
	// during device authorization.
	Resources string
}

type Interaction struct {
	Direction         string
	RawText           string
	Command           string
	Args              string
	Handled           bool
	Reply             string
	Status            string
	Error             string
	PlatformMessageID string
	DeliveryMethod    string
	SourceMessageID   string
	AcceptedAt        time.Time
	CreatedAt         time.Time
}

type MessageAcceptance struct {
	PlatformMessageID string
	DeliveryMethod    string
	SourceMessageID   string
	AcceptedAt        time.Time
}

const (
	InteractionDirectionInbound  = "inbound"
	InteractionDirectionOutbound = "outbound"

	InteractionStatusHandled  = "handled"
	InteractionStatusIgnored  = "ignored"
	InteractionStatusAccepted = "accepted"
	InteractionStatusUnknown  = "unknown"
	// InteractionStatusSent is retained for existing records and callers.
	InteractionStatusSent   = "sent"
	InteractionStatusFailed = "failed"

	DeliveryMethodMediaUpload = "media_upload"
	DeliveryMethodMediaCache  = "media_cache"
	DeliveryMethodForward     = "forward"
)

type NotificationSettings struct {
	Identity        Identity
	ClassesEnabled  bool
	HomeworkEnabled bool
}

type AgentSettings struct {
	Identity        Identity
	ExposeToolCalls bool
}

type BusSettings struct {
	Identity        Identity
	ShowSouthCampus bool
}

type AgentRun struct {
	ID        int64
	Identity  Identity
	RawText   string
	Status    string
	Reply     string
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type FeedbackRecord struct {
	ID          int64
	Identity    Identity
	Source      string
	Category    string
	Content     string
	Context     string
	Status      string
	SentToAdmin bool
	Resolved    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	SentAt      *time.Time
	ResolvedAt  *time.Time
}

type PendingConfirmation struct {
	ID        int64
	Identity  Identity
	Command   string
	Source    string
	Status    string
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type PublicCommandCacheEntry struct {
	Version   string
	Command   string
	Args      string
	Response  string
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

const (
	AgentRunStatusStarted   = "started"
	AgentRunStatusCompleted = "completed"
	AgentRunStatusFailed    = "failed"
	AgentRunStatusIgnored   = "ignored"

	FeedbackStatusOpen     = "open"
	FeedbackStatusResolved = "resolved"

	PendingConfirmationStatusPending    = "pending"
	PendingConfirmationStatusConfirmed  = "confirmed"
	PendingConfirmationStatusSuperseded = "superseded"
	PendingConfirmationStatusFailed     = "failed"
	PendingConfirmationStatusExpired    = "expired"
)

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
	Resources               string
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
	ID                int64  `gorm:"primaryKey"`
	Platform          string `gorm:"not null;index:idx_interactions_conversation_created"`
	ConversationType  string `gorm:"not null;index:idx_interactions_conversation_created"`
	ConversationID    string `gorm:"not null;index:idx_interactions_conversation_created"`
	UserID            string `gorm:"not null"`
	Direction         string `gorm:"not null;default:inbound"`
	RawText           string `gorm:"not null"`
	Command           string
	Args              string
	Handled           bool `gorm:"not null"`
	Reply             string
	Status            string
	Error             string
	PlatformMessageID string
	DeliveryMethod    string
	SourceMessageID   string
	AcceptedAt        *time.Time
	CreatedAt         time.Time `gorm:"index:idx_interactions_conversation_created"`
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

type agentSettingRow struct {
	UserID           int64  `gorm:"primaryKey"`
	Platform         string `gorm:"not null"`
	ExternalUserID   string `gorm:"not null"`
	ConversationType string
	ConversationID   string
	ExposeToolCalls  bool `gorm:"not null"`
	UpdatedAt        time.Time
}

func (agentSettingRow) TableName() string {
	return "agent_settings"
}

type busSettingRow struct {
	UserID          int64  `gorm:"primaryKey"`
	Platform        string `gorm:"not null"`
	ExternalUserID  string `gorm:"not null"`
	ShowSouthCampus bool   `gorm:"not null"`
	UpdatedAt       time.Time
}

func (busSettingRow) TableName() string {
	return "bus_settings"
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

type agentRunRow struct {
	ID               int64  `gorm:"primaryKey"`
	UserID           int64  `gorm:"not null;index"`
	Platform         string `gorm:"not null;index:idx_agent_runs_conversation_created"`
	ExternalUserID   string `gorm:"not null"`
	ConversationType string `gorm:"not null;index:idx_agent_runs_conversation_created"`
	ConversationID   string `gorm:"not null;index:idx_agent_runs_conversation_created"`
	RawText          string `gorm:"not null"`
	Status           string `gorm:"not null;index"`
	Reply            string
	Error            string
	CreatedAt        time.Time `gorm:"index:idx_agent_runs_conversation_created"`
	UpdatedAt        time.Time
}

func (agentRunRow) TableName() string {
	return "agent_runs"
}

type feedbackRecordRow struct {
	ID               int64  `gorm:"primaryKey"`
	UserID           int64  `gorm:"not null;index"`
	Platform         string `gorm:"not null;index:idx_feedback_records_conversation_created"`
	ExternalUserID   string `gorm:"not null"`
	ConversationType string `gorm:"not null;index:idx_feedback_records_conversation_created"`
	ConversationID   string `gorm:"not null;index:idx_feedback_records_conversation_created"`
	Source           string `gorm:"not null;index"`
	Category         string
	Content          string `gorm:"not null"`
	Context          string
	Status           string `gorm:"not null;index"`
	SentToAdmin      bool   `gorm:"not null"`
	Resolved         bool   `gorm:"not null"`
	SentAt           *time.Time
	ResolvedAt       *time.Time
	CreatedAt        time.Time `gorm:"index:idx_feedback_records_conversation_created"`
	UpdatedAt        time.Time
}

func (feedbackRecordRow) TableName() string {
	return "feedback_records"
}

type pendingConfirmationRow struct {
	ID               int64  `gorm:"primaryKey"`
	UserID           int64  `gorm:"not null;index"`
	Platform         string `gorm:"not null;index:idx_pending_confirmations_conversation_status"`
	ExternalUserID   string `gorm:"not null"`
	ConversationType string `gorm:"not null;index:idx_pending_confirmations_conversation_status"`
	ConversationID   string `gorm:"not null;index:idx_pending_confirmations_conversation_status"`
	Command          string `gorm:"not null"`
	Source           string
	Status           string    `gorm:"not null;index:idx_pending_confirmations_conversation_status"`
	ExpiresAt        time.Time `gorm:"not null;index"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (pendingConfirmationRow) TableName() string {
	return "pending_confirmations"
}

type publicCommandCacheRow struct {
	ID        int64     `gorm:"primaryKey"`
	Version   string    `gorm:"not null;uniqueIndex:idx_public_command_cache_key,priority:1"`
	Command   string    `gorm:"not null;uniqueIndex:idx_public_command_cache_key,priority:2"`
	Args      string    `gorm:"not null;uniqueIndex:idx_public_command_cache_key,priority:3"`
	Response  string    `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (publicCommandCacheRow) TableName() string {
	return "public_command_cache"
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
		&agentSettingRow{},
		&busSettingRow{},
		&notificationDeliveryRow{},
		&agentRunRow{},
		&feedbackRecordRow{},
		&pendingConfirmationRow{},
		&publicCommandCacheRow{},
	)
}

func (s *Store) PublicCommandCache(ctx context.Context, version, command, args string, now time.Time) (PublicCommandCacheEntry, bool, error) {
	version = strings.TrimSpace(version)
	command = strings.TrimSpace(command)
	if version == "" || command == "" {
		return PublicCommandCacheEntry{}, false, errors.New("public command cache version and command are required")
	}
	if now.IsZero() {
		now = nowUTC()
	}
	var row publicCommandCacheRow
	err := s.db.WithContext(ctx).
		Where("version = ? AND command = ? AND args = ? AND expires_at > ?", version, command, strings.TrimSpace(args), now.UTC()).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return PublicCommandCacheEntry{}, false, nil
	}
	if err != nil {
		return PublicCommandCacheEntry{}, false, err
	}
	return PublicCommandCacheEntry{
		Version:   row.Version,
		Command:   row.Command,
		Args:      row.Args,
		Response:  row.Response,
		ExpiresAt: row.ExpiresAt,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, true, nil
}

func (s *Store) SavePublicCommandCache(ctx context.Context, entry PublicCommandCacheEntry) error {
	entry.Version = strings.TrimSpace(entry.Version)
	entry.Command = strings.TrimSpace(entry.Command)
	entry.Args = strings.TrimSpace(entry.Args)
	if entry.Version == "" || entry.Command == "" || strings.TrimSpace(entry.Response) == "" || entry.ExpiresAt.IsZero() {
		return errors.New("public command cache entry is incomplete")
	}
	now := nowUTC()
	row := publicCommandCacheRow{
		Version:   entry.Version,
		Command:   entry.Command,
		Args:      entry.Args,
		Response:  entry.Response,
		ExpiresAt: entry.ExpiresAt.UTC(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "version"}, {Name: "command"}, {Name: "args"}},
		DoUpdates: clause.Assignments(map[string]any{
			"response":   row.Response,
			"expires_at": row.ExpiresAt,
			"updated_at": row.UpdatedAt,
		}),
	}).Create(&row).Error
}

func (s *Store) PurgePublicCommandCache(ctx context.Context, version string, now time.Time) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return errors.New("public command cache version is required")
	}
	if now.IsZero() {
		now = nowUTC()
	}
	return s.db.WithContext(ctx).
		Where("version <> ? OR expires_at <= ?", version, now.UTC()).
		Delete(&publicCommandCacheRow{}).Error
}

func (s *Store) EnsureUser(ctx context.Context, ident Identity) (int64, error) {
	if err := validateIdentity(ident); err != nil {
		return 0, err
	}
	ident = normalizeIdentity(ident)
	now := nowUTC()
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

func (s *Store) userID(ctx context.Context, ident Identity) (int64, bool, error) {
	if err := validateIdentity(ident); err != nil {
		return 0, false, err
	}
	ident = normalizeIdentity(ident)
	var user userRow
	err := s.db.WithContext(ctx).
		Where("platform = ? AND external_user_id = ?", ident.Platform, ident.UserID).
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return user.ID, true, nil
}

func normalizeIdentity(ident Identity) Identity {
	ident.Platform = textutil.LowerTrim(ident.Platform)
	ident.UserID = strings.TrimSpace(ident.UserID)
	ident.ConversationType = textutil.LowerTrim(ident.ConversationType)
	ident.ConversationID = strings.TrimSpace(ident.ConversationID)
	return ident
}

func HasUserIdentity(ident Identity) bool {
	return textutil.HasText(ident.Platform) &&
		textutil.HasText(ident.UserID)
}

func HasConversationTarget(ident Identity) bool {
	return textutil.HasText(ident.ConversationType) &&
		textutil.HasText(ident.ConversationID)
}

func HasConversationIdentity(ident Identity) bool {
	return HasUserIdentity(ident) && HasConversationTarget(ident)
}

func IsGroupConversation(ident Identity) bool {
	return textutil.TrimEqualFold(ident.ConversationType, "group")
}

func IsPrivateConversation(ident Identity) bool {
	return textutil.TrimEqualFold(ident.ConversationType, "private")
}

func validateIdentity(ident Identity) error {
	if err := requireIdentityField(ident.Platform, "platform"); err != nil {
		return err
	}
	if err := requireIdentityField(ident.UserID, "user id"); err != nil {
		return err
	}
	return nil
}

func validateConversationIdentity(ident Identity) error {
	if err := validateIdentity(ident); err != nil {
		return err
	}
	if err := requireIdentityField(ident.ConversationType, "conversation type"); err != nil {
		return err
	}
	if err := requireIdentityField(ident.ConversationID, "conversation id"); err != nil {
		return err
	}
	return nil
}

func requireIdentityField(value, name string) error {
	if !textutil.HasText(value) {
		return errors.New("identity " + name + " is empty")
	}
	return nil
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

func (s *Store) SaveCredential(ctx context.Context, ident Identity, cred Credential) error {
	cred, err := normalizeCredentialForSave(cred)
	if err != nil {
		return err
	}
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
		UpdatedAt:    nowUTC(),
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

func normalizeCredentialForSave(cred Credential) (Credential, error) {
	cred.ClientID = strings.TrimSpace(cred.ClientID)
	cred.AccessToken = strings.TrimSpace(cred.AccessToken)
	cred.RefreshToken = strings.TrimSpace(cred.RefreshToken)
	cred.TokenType = strings.TrimSpace(cred.TokenType)
	cred.Scope = strings.TrimSpace(cred.Scope)
	cred.Resource = strings.TrimSpace(cred.Resource)
	if cred.ClientID == "" {
		return Credential{}, errors.New("credential client id is empty")
	}
	if cred.AccessToken == "" {
		return Credential{}, errors.New("credential access token is empty")
	}
	return cred, nil
}

func (s *Store) Credential(ctx context.Context, ident Identity) (*Credential, error) {
	if err := validateIdentity(ident); err != nil {
		return nil, err
	}
	ident = normalizeIdentity(ident)
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
	userID, ok, err := s.userID(ctx, ident)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return s.db.WithContext(ctx).Delete(&credentialRow{}, "user_id = ?", userID).Error
}

func (s *Store) SaveLoginSession(ctx context.Context, ident Identity, session LoginSession) error {
	ident = normalizeIdentity(ident)
	session, err := normalizeLoginSessionForSave(session)
	if err != nil {
		return err
	}
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return err
	}
	now := nowUTC()
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
			Resources:               session.Resources,
			ExpiresAt:               session.ExpiresAt.UTC(),
			IntervalSeconds:         session.IntervalSeconds,
			Status:                  session.Status,
			CreatedAt:               now,
			UpdatedAt:               now,
		}
		return tx.Create(&row).Error
	})
}

func normalizeLoginSessionForSave(session LoginSession) (LoginSession, error) {
	session.DeviceCode = strings.TrimSpace(session.DeviceCode)
	session.UserCode = strings.TrimSpace(session.UserCode)
	session.VerificationURI = strings.TrimSpace(session.VerificationURI)
	session.VerificationURIComplete = strings.TrimSpace(session.VerificationURIComplete)
	session.ClientID = strings.TrimSpace(session.ClientID)
	session.Resources = strings.Join(strings.Fields(session.Resources), " ")
	session.Status = strings.TrimSpace(session.Status)
	if session.DeviceCode == "" {
		return LoginSession{}, errors.New("login session device code is empty")
	}
	if session.ClientID == "" {
		return LoginSession{}, errors.New("login session client id is empty")
	}
	if session.Status == "" {
		return LoginSession{}, errors.New("login session status is empty")
	}
	return session, nil
}

func (s *Store) ActiveLoginSession(ctx context.Context, ident Identity) (*LoginSession, error) {
	if err := validateIdentity(ident); err != nil {
		return nil, err
	}
	ident = normalizeIdentity(ident)
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
		Resources:               row.Resources,
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
			Platform:         textutil.FirstNonEmpty(row.Platform),
			UserID:           textutil.FirstNonEmpty(row.ExternalUserID),
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
			Resources:               row.Resources,
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
	deviceCode, status, err := normalizeLoginSessionUpdate(deviceCode, status)
	if err != nil {
		return err
	}
	userID, ok, err := s.userID(ctx, ident)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return s.db.WithContext(ctx).Model(&loginSessionRow{}).
		Where("user_id = ? AND device_code = ?", userID, deviceCode).
		Updates(map[string]any{"status": status, "updated_at": nowUTC()}).Error
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
	ident = normalizeIdentity(ident)
	row := conversationStateRow{
		Platform:         ident.Platform,
		ConversationType: ident.ConversationType,
		ConversationID:   ident.ConversationID,
		UserID:           ident.UserID,
		LastCommand:      strings.TrimSpace(command),
		State:            state,
		CreatedAt:        nowUTC(),
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

func (s *Store) RecordInteraction(ctx context.Context, ident Identity, interaction Interaction) error {
	if err := validateConversationIdentity(ident); err != nil {
		return err
	}
	ident = normalizeIdentity(ident)
	var acceptedAt *time.Time
	if !interaction.AcceptedAt.IsZero() {
		value := interaction.AcceptedAt.UTC()
		acceptedAt = &value
	}
	row := interactionRow{
		Platform:          ident.Platform,
		ConversationType:  ident.ConversationType,
		ConversationID:    ident.ConversationID,
		UserID:            ident.UserID,
		Direction:         interactionDirection(interaction.Direction),
		RawText:           interaction.RawText,
		Command:           strings.TrimSpace(interaction.Command),
		Args:              strings.TrimSpace(interaction.Args),
		Handled:           interaction.Handled,
		Reply:             interaction.Reply,
		Status:            strings.TrimSpace(interaction.Status),
		Error:             strings.TrimSpace(interaction.Error),
		PlatformMessageID: strings.TrimSpace(interaction.PlatformMessageID),
		DeliveryMethod:    textutil.LowerTrim(interaction.DeliveryMethod),
		SourceMessageID:   strings.TrimSpace(interaction.SourceMessageID),
		AcceptedAt:        acceptedAt,
		CreatedAt:         nowUTC(),
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

func interactionDirection(direction string) string {
	normalized := textutil.LowerTrim(direction)
	switch normalized {
	case "":
		return InteractionDirectionInbound
	case InteractionDirectionInbound, InteractionDirectionOutbound:
		return normalized
	default:
		return strings.TrimSpace(direction)
	}
}

func (s *Store) RecentHandledInteractions(ctx context.Context, ident Identity, limit int) ([]Interaction, error) {
	if err := validateConversationIdentity(ident); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	ident = normalizeIdentity(ident)
	var rows []interactionRow
	err := s.db.WithContext(ctx).
		Where("platform = ? AND conversation_type = ? AND conversation_id = ? AND direction = ? AND handled = ? AND status = ?",
			ident.Platform, ident.ConversationType, ident.ConversationID, InteractionDirectionInbound, true, InteractionStatusHandled).
		Order("created_at desc, id desc").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]Interaction, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		out = append(out, Interaction{
			Direction:         row.Direction,
			RawText:           row.RawText,
			Command:           row.Command,
			Args:              row.Args,
			Handled:           row.Handled,
			Reply:             row.Reply,
			Status:            row.Status,
			Error:             row.Error,
			PlatformMessageID: row.PlatformMessageID,
			DeliveryMethod:    row.DeliveryMethod,
			SourceMessageID:   row.SourceMessageID,
			AcceptedAt:        dereferenceTime(row.AcceptedAt),
			CreatedAt:         row.CreatedAt,
		})
	}
	return out, nil
}

func dereferenceTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func (s *Store) InteractionCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&interactionRow{}).Count(&count).Error
	return count, err
}

func (s *Store) RecordAgentRun(ctx context.Context, ident Identity, rawText string) (int64, error) {
	if err := validateConversationIdentity(ident); err != nil {
		return 0, err
	}
	ident = normalizeIdentity(ident)
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return 0, err
	}
	now := nowUTC()
	row := agentRunRow{
		UserID:           userID,
		Platform:         ident.Platform,
		ExternalUserID:   ident.UserID,
		ConversationType: ident.ConversationType,
		ConversationID:   ident.ConversationID,
		RawText:          strings.TrimSpace(rawText),
		Status:           AgentRunStatusStarted,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if row.RawText == "" {
		return 0, errors.New("agent run raw text is empty")
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return 0, err
	}
	return row.ID, nil
}

func (s *Store) FinishAgentRun(ctx context.Context, id int64, status, reply string, runErr error) error {
	if id <= 0 {
		return nil
	}
	status = textutil.LowerTrim(status)
	if status == "" {
		status = AgentRunStatusCompleted
	}
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	return s.db.WithContext(ctx).Model(&agentRunRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     status,
			"reply":      reply,
			"error":      strings.TrimSpace(errText),
			"updated_at": nowUTC(),
		}).Error
}

func (s *Store) RecordFeedback(ctx context.Context, ident Identity, feedback FeedbackRecord) (int64, error) {
	if err := validateConversationIdentity(ident); err != nil {
		return 0, err
	}
	ident = normalizeIdentity(ident)
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return 0, err
	}
	source := textutil.LowerTrim(feedback.Source)
	if source == "" {
		source = "user"
	}
	status := textutil.LowerTrim(feedback.Status)
	if status == "" {
		status = FeedbackStatusOpen
	}
	content := strings.TrimSpace(feedback.Content)
	if content == "" {
		return 0, errors.New("feedback content is empty")
	}
	now := nowUTC()
	row := feedbackRecordRow{
		UserID:           userID,
		Platform:         ident.Platform,
		ExternalUserID:   ident.UserID,
		ConversationType: ident.ConversationType,
		ConversationID:   ident.ConversationID,
		Source:           source,
		Category:         strings.TrimSpace(feedback.Category),
		Content:          content,
		Context:          strings.TrimSpace(feedback.Context),
		Status:           status,
		SentToAdmin:      feedback.SentToAdmin,
		Resolved:         feedback.Resolved,
		SentAt:           feedback.SentAt,
		ResolvedAt:       feedback.ResolvedAt,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return 0, err
	}
	return row.ID, nil
}

func (s *Store) MarkFeedbackSent(ctx context.Context, id int64) error {
	if id <= 0 {
		return nil
	}
	now := nowUTC()
	return s.db.WithContext(ctx).Model(&feedbackRecordRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"sent_to_admin": true,
			"sent_at":       &now,
			"updated_at":    now,
		}).Error
}

func (s *Store) FeedbackCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&feedbackRecordRow{}).Count(&count).Error
	return count, err
}

func (s *Store) SavePendingConfirmation(ctx context.Context, ident Identity, command, source string, ttl time.Duration) (int64, error) {
	if err := validateConversationIdentity(ident); err != nil {
		return 0, err
	}
	ident = normalizeIdentity(ident)
	command = strings.TrimSpace(command)
	if command == "" {
		return 0, errors.New("pending confirmation command is empty")
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return 0, err
	}
	now := nowUTC()
	var id int64
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&pendingConfirmationRow{}).
			Where("user_id = ? AND conversation_type = ? AND conversation_id = ? AND status = ?",
				userID, ident.ConversationType, ident.ConversationID, PendingConfirmationStatusPending).
			Updates(map[string]any{
				"status":     PendingConfirmationStatusSuperseded,
				"updated_at": now,
			}).Error; err != nil {
			return err
		}
		row := pendingConfirmationRow{
			UserID:           userID,
			Platform:         ident.Platform,
			ExternalUserID:   ident.UserID,
			ConversationType: ident.ConversationType,
			ConversationID:   ident.ConversationID,
			Command:          command,
			Source:           strings.TrimSpace(source),
			Status:           PendingConfirmationStatusPending,
			ExpiresAt:        now.Add(ttl),
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		id = row.ID
		return nil
	})
	return id, err
}

func (s *Store) ActivePendingConfirmation(ctx context.Context, ident Identity) (*PendingConfirmation, error) {
	if err := validateConversationIdentity(ident); err != nil {
		return nil, err
	}
	ident = normalizeIdentity(ident)
	userID, ok, err := s.userID(ctx, ident)
	if err != nil || !ok {
		return nil, err
	}
	now := nowUTC()
	var row pendingConfirmationRow
	err = s.db.WithContext(ctx).
		Where("user_id = ? AND conversation_type = ? AND conversation_id = ? AND status = ? AND expires_at > ?",
			userID, ident.ConversationType, ident.ConversationID, PendingConfirmationStatusPending, now).
		Order("id DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &PendingConfirmation{
		ID:        row.ID,
		Identity:  ident,
		Command:   row.Command,
		Source:    row.Source,
		Status:    row.Status,
		ExpiresAt: row.ExpiresAt,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

func (s *Store) MarkPendingConfirmation(ctx context.Context, id int64, status string) error {
	if id <= 0 {
		return nil
	}
	status = textutil.LowerTrim(status)
	if status == "" {
		return errors.New("pending confirmation status is empty")
	}
	return s.db.WithContext(ctx).Model(&pendingConfirmationRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     status,
			"updated_at": nowUTC(),
		}).Error
}

func (s *Store) NotificationSettings(ctx context.Context, ident Identity) (NotificationSettings, error) {
	if err := validateIdentity(ident); err != nil {
		return NotificationSettings{}, err
	}
	ident = normalizeIdentity(ident)
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
	settings, err := normalizeNotificationSettingsForSave(settings)
	if err != nil {
		return err
	}
	userID, err := s.EnsureUser(ctx, settings.Identity)
	if err != nil {
		return err
	}
	now := nowUTC()
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

func normalizeNotificationSettingsForSave(settings NotificationSettings) (NotificationSettings, error) {
	settings.Identity = normalizeIdentity(settings.Identity)
	if !settings.ClassesEnabled && !settings.HomeworkEnabled {
		return settings, nil
	}
	if settings.Identity.ConversationType == "" {
		return NotificationSettings{}, errors.New("notification settings conversation type is empty")
	}
	if settings.Identity.ConversationID == "" {
		return NotificationSettings{}, errors.New("notification settings conversation id is empty")
	}
	return settings, nil
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

func (s *Store) AgentSettings(ctx context.Context, ident Identity) (AgentSettings, error) {
	if err := validateIdentity(ident); err != nil {
		return AgentSettings{}, err
	}
	ident = normalizeIdentity(ident)
	var row agentSettingRow
	err := s.db.WithContext(ctx).
		Joins("JOIN users ON users.id = agent_settings.user_id").
		Where("users.platform = ? AND users.external_user_id = ?", ident.Platform, ident.UserID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return AgentSettings{Identity: ident}, nil
	}
	if err != nil {
		return AgentSettings{}, err
	}
	return agentSettingsFromRow(row, ident), nil
}

func (s *Store) SaveAgentSettings(ctx context.Context, settings AgentSettings) error {
	settings.Identity = normalizeIdentity(settings.Identity)
	userID, err := s.EnsureUser(ctx, settings.Identity)
	if err != nil {
		return err
	}
	now := nowUTC()
	row := agentSettingRow{
		UserID:           userID,
		Platform:         settings.Identity.Platform,
		ExternalUserID:   settings.Identity.UserID,
		ConversationType: settings.Identity.ConversationType,
		ConversationID:   settings.Identity.ConversationID,
		ExposeToolCalls:  settings.ExposeToolCalls,
		UpdatedAt:        now,
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"platform",
			"external_user_id",
			"conversation_type",
			"conversation_id",
			"expose_tool_calls",
			"updated_at",
		}),
	}).Create(&row).Error
}

func (s *Store) BusSettings(ctx context.Context, ident Identity) (BusSettings, error) {
	if err := validateIdentity(ident); err != nil {
		return BusSettings{}, err
	}
	ident = normalizeIdentity(ident)
	var row busSettingRow
	err := s.db.WithContext(ctx).
		Joins("JOIN users ON users.id = bus_settings.user_id").
		Where("users.platform = ? AND users.external_user_id = ?", ident.Platform, ident.UserID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return BusSettings{Identity: ident}, nil
	}
	if err != nil {
		return BusSettings{}, err
	}
	return busSettingsFromRow(row, ident), nil
}

func (s *Store) SaveBusSettings(ctx context.Context, settings BusSettings) error {
	settings.Identity = normalizeIdentity(settings.Identity)
	userID, err := s.EnsureUser(ctx, settings.Identity)
	if err != nil {
		return err
	}
	now := nowUTC()
	row := busSettingRow{
		UserID:          userID,
		Platform:        settings.Identity.Platform,
		ExternalUserID:  settings.Identity.UserID,
		ShowSouthCampus: settings.ShowSouthCampus,
		UpdatedAt:       now,
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"platform",
			"external_user_id",
			"show_south_campus",
			"updated_at",
		}),
	}).Create(&row).Error
}

func (s *Store) TryRecordNotificationDelivery(ctx context.Context, ident Identity, kind, itemKey string) (bool, error) {
	kind, itemKey, err := normalizeNotificationDeliveryKey(kind, itemKey)
	if err != nil {
		return false, err
	}
	userID, err := s.EnsureUser(ctx, ident)
	if err != nil {
		return false, err
	}
	row := notificationDeliveryRow{
		UserID:    userID,
		Kind:      kind,
		ItemKey:   itemKey,
		CreatedAt: nowUTC(),
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
	kind, itemKey, err := normalizeNotificationDeliveryKey(kind, itemKey)
	if err != nil {
		return false, err
	}
	userID, ok, err := s.userID(ctx, ident)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	var count int64
	err = s.db.WithContext(ctx).Model(&notificationDeliveryRow{}).
		Where("user_id = ? AND kind = ? AND item_key = ?", userID, kind, itemKey).
		Count(&count).Error
	return count > 0, err
}

func normalizeNotificationDeliveryKey(kind, itemKey string) (string, string, error) {
	kind = textutil.LowerTrim(kind)
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

func agentSettingsFromRow(row agentSettingRow, fallback Identity) AgentSettings {
	ident := Identity{
		Platform:         textutil.FirstNonEmpty(row.Platform, fallback.Platform),
		UserID:           textutil.FirstNonEmpty(row.ExternalUserID, fallback.UserID),
		ConversationType: textutil.FirstNonEmpty(row.ConversationType, fallback.ConversationType),
		ConversationID:   textutil.FirstNonEmpty(row.ConversationID, fallback.ConversationID),
	}
	return AgentSettings{
		Identity:        ident,
		ExposeToolCalls: row.ExposeToolCalls,
	}
}

func busSettingsFromRow(row busSettingRow, fallback Identity) BusSettings {
	ident := Identity{
		Platform: textutil.FirstNonEmpty(row.Platform, fallback.Platform),
		UserID:   textutil.FirstNonEmpty(row.ExternalUserID, fallback.UserID),
	}
	return BusSettings{
		Identity:        ident,
		ShowSouthCampus: row.ShowSouthCampus,
	}
}

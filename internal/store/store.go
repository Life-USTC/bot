package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/Life-USTC/Bot/internal/delivery"
	"github.com/Life-USTC/Bot/internal/message"
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

type LoginStatus string

const (
	LoginStatusPending    LoginStatus = "pending"
	LoginStatusApproved   LoginStatus = "approved"
	LoginStatusExpired    LoginStatus = "expired"
	LoginStatusDenied     LoginStatus = "denied"
	LoginStatusInvalid    LoginStatus = "invalid"
	LoginStatusSuperseded LoginStatus = "superseded"
)

type LoginTransition struct {
	Status     LoginStatus
	Credential *Credential
	Outbound   *message.Outbound
}

type Interaction struct {
	ID                int64
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

	InteractionStatusHandled             = "handled"
	InteractionStatusWaitingAuth         = "waiting_auth"
	InteractionStatusWaitingConfirmation = "waiting_confirmation"
	InteractionStatusIgnored             = "ignored"
	InteractionStatusAccepted            = "accepted"
	InteractionStatusUnknown             = "unknown"
	InteractionStatusSent                = "sent"
	InteractionStatusFailed              = "failed"

	DeliveryMethodMediaUpload = "media_upload"
	DeliveryMethodMediaCache  = "media_cache"
	DeliveryMethodForward     = "forward"
)

type NotificationSettings struct {
	Identity        Identity
	ClassesEnabled  bool
	HomeworkEnabled bool
	ReauthRequired  bool
}

type BusSettings struct {
	Identity        Identity
	ShowSouthCampus bool
}

type AgentRun struct {
	ID               int64
	JobID            int64
	Identity         Identity
	RawText          string
	Provider         string
	Model            string
	Currency         string
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CostNanoCNY      int64
	ModelRequests    int64
	ToolCalls        int64
	Status           string
	Reply            string
	Error            string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

const SpendingCurrencyCNY = "CNY"

// CurrentSchemaVersion is the schema version written to SQLite user_version
// after a successful startup migration.
const CurrentSchemaVersion = 3

var requiredSchemaModels = []any{
	&userRow{},
	&credentialRow{},
	&loginSessionRow{},
	&conversationStateRow{},
	&interactionRow{},
	&notificationSettingRow{},
	&busSettingRow{},
	&agentRunRow{},
	&feedbackRecordRow{},
	&outgoingMessageRow{},
	&conversationJobSequenceRow{},
	&conversationJobRow{},
	&publicCommandCacheRow{},
	&conversationEventRow{},
	&agentCheckpointRow{},
	&capabilityExecutionRow{},
	&conversationCompactionRow{},
}

var obsoleteSchemaTables = []string{
	"conversation_summaries",
	"pending_confirmations",
	"pending_requests",
	"notification_deliveries",
	"agent_settings",
}

var obsoleteFeedbackColumns = []string{"sent_to_admin", "sent_at", "resolved"}

type AgentSpending struct {
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CostNanoCNY      int64
	ModelRequests    int64
	ToolCalls        int64
	Currency         string
}

type FeedbackRecord struct {
	ID         int64
	Identity   Identity
	Source     string
	Category   string
	Content    string
	Context    string
	Status     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ResolvedAt *time.Time
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
	AgentRunStatusStarted     = "started"
	AgentRunStatusCompleted   = "completed"
	AgentRunStatusFailed      = "failed"
	AgentRunStatusIgnored     = "ignored"
	AgentRunStatusInterrupted = "interrupted"

	FeedbackStatusOpen     = "open"
	FeedbackStatusResolved = "resolved"
)

type Store struct {
	db                *gorm.DB
	conversationJobMu sync.Mutex
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
	ReauthRequired   bool `gorm:"not null;default:false"`
	UpdatedAt        time.Time
}

func (notificationSettingRow) TableName() string {
	return "notification_settings"
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

type agentRunRow struct {
	ID               int64  `gorm:"primaryKey"`
	JobID            int64  `gorm:"not null;default:0;index"`
	UserID           int64  `gorm:"not null;index"`
	Platform         string `gorm:"not null;index:idx_agent_runs_conversation_created"`
	ExternalUserID   string `gorm:"not null"`
	ConversationType string `gorm:"not null;index:idx_agent_runs_conversation_created"`
	ConversationID   string `gorm:"not null;index:idx_agent_runs_conversation_created"`
	RawText          string `gorm:"not null"`
	Provider         string `gorm:"not null;default:''"`
	Model            string `gorm:"not null;default:''"`
	Currency         string `gorm:"not null;default:CNY"`
	PromptTokens     int64  `gorm:"not null;default:0"`
	CachedTokens     int64  `gorm:"not null;default:0"`
	CompletionTokens int64  `gorm:"not null;default:0"`
	TotalTokens      int64  `gorm:"not null;default:0"`
	CostNanoCNY      int64  `gorm:"not null;default:0"`
	ModelRequests    int64  `gorm:"not null;default:0"`
	ToolCalls        int64  `gorm:"not null;default:0"`
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
	ResolvedAt       *time.Time
	CreatedAt        time.Time `gorm:"index:idx_feedback_records_conversation_created"`
	UpdatedAt        time.Time
}

type outgoingMessageRow struct {
	ID                int64      `gorm:"primaryKey"`
	DedupeKey         string     `gorm:"not null;uniqueIndex"`
	Kind              string     `gorm:"not null;index"`
	Platform          string     `gorm:"not null;index:idx_outgoing_messages_due"`
	ConversationType  string     `gorm:"not null"`
	ConversationID    string     `gorm:"not null"`
	PayloadJSON       string     `gorm:"not null"`
	Status            string     `gorm:"not null;index:idx_outgoing_messages_due"`
	Attempts          int        `gorm:"not null;default:0"`
	NextAttemptAt     *time.Time `gorm:"index:idx_outgoing_messages_due"`
	AttemptStartedAt  *time.Time
	ExpiresAt         *time.Time `gorm:"index"`
	PlatformMessageID string
	DeliveryMethod    string
	SourceMessageID   string
	ErrorCode         string
	ErrorMessage      string
	AcceptedAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (outgoingMessageRow) TableName() string {
	return "outgoing_messages"
}

func (feedbackRecordRow) TableName() string {
	return "feedback_records"
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
	s, err := openStore(path)
	if err != nil {
		return nil, err
	}
	if err := s.migrate(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// OpenForSchemaMaintenance opens the database without running any schema
// changes. The migrate command uses this entry point so a production v2
// database can be explicitly prepared before the bot starts; normal startup
// continues to verify the complete schema and never repairs it implicitly.
func OpenForSchemaMaintenance(path string) (*Store, error) {
	return openStore(path)
}

func openStore(path string) (*Store, error) {
	if path == "" {
		path = filepath.Join(".run", "life-ustc-bot.db")
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	query := url.Values{
		"_busy_timeout": {"5000"},
		"_journal_mode": {"WAL"},
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// SQLite has one writer. A larger in-process pool lets a deferred
	// transaction read on one connection while another connection commits,
	// after which SQLite must reject the first connection's write upgrade with
	// SQLITE_BUSY_SNAPSHOT; busy_timeout cannot make that snapshot valid again.
	// Serialize this process at the pool boundary and retain busy_timeout for
	// coordination with external readers/writers such as deployment backup.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.PingContext(ctx)
}

// VerifySchema confirms that startup migration reached the expected schema
// and that SQLite can read every page of the database.
func (s *Store) VerifySchema() error {
	var version int
	if err := s.db.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
		return fmt.Errorf("read sqlite schema version: %w", err)
	}
	if version != CurrentSchemaVersion {
		return fmt.Errorf("unsupported sqlite schema version %d (want %d)", version, CurrentSchemaVersion)
	}
	if err := verifySchemaShape(s.db); err != nil {
		return err
	}
	var integrity string
	if err := s.db.Raw("PRAGMA integrity_check").Scan(&integrity).Error; err != nil {
		return fmt.Errorf("run sqlite integrity check: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("sqlite integrity check failed: %s", integrity)
	}
	return nil
}

func (s *Store) migrate() error {
	if err := s.db.Exec(`PRAGMA journal_mode = WAL`).Error; err != nil {
		return err
	}
	return s.migrateSchema()
}

func (s *Store) migrateSchema() error {
	var version int
	if err := s.db.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	switch version {
	case CurrentSchemaVersion:
		return verifySchemaShape(s.db)
	case 0:
		empty, err := sqliteSchemaIsEmpty(s.db)
		if err != nil {
			return err
		}
		if !empty {
			return fmt.Errorf("unsupported nonempty database schema version 0 (want %d)", CurrentSchemaVersion)
		}
	default:
		return fmt.Errorf("unsupported database schema version %d (want %d)", version, CurrentSchemaVersion)
	}
	if err := s.initializeCurrentSchema(); err != nil {
		return err
	}
	return verifySchemaShape(s.db)
}

// PrepareSchemaForMaintenance applies the explicitly requested schema setup
// and then verifies the complete current schema. A current-version database only
// receives the independent conversation_compactions table; it is never
// silently altered during normal startup.
func (s *Store) PrepareSchemaForMaintenance(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("store is unavailable")
	}
	if err := s.db.Exec(`PRAGMA journal_mode = WAL`).Error; err != nil {
		return err
	}
	var version int
	if err := s.db.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	switch version {
	case CurrentSchemaVersion:
		// Verify all existing tables before adding the one new required table.
		// This keeps maintenance from masking an unrelated malformed or
		// obsolete production schema.
		if err := verifySchemaShapeWithoutConversationCompaction(s.db); err != nil {
			return err
		}
		if err := s.EnsureConversationCompactionSchema(ctx); err != nil {
			return err
		}
	case 0:
		empty, err := sqliteSchemaIsEmpty(s.db)
		if err != nil {
			return err
		}
		if !empty {
			return fmt.Errorf("unsupported nonempty database schema version 0 (want %d)", CurrentSchemaVersion)
		}
		if err := s.initializeCurrentSchema(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported database schema version %d (want %d)", version, CurrentSchemaVersion)
	}
	return s.VerifySchema()
}

func (s *Store) initializeCurrentSchema() error {
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(requiredSchemaModels...); err != nil {
			return fmt.Errorf("initialize schema tables: %w", err)
		}
		if err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", CurrentSchemaVersion)).Error; err != nil {
			return fmt.Errorf("write schema version: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func sqliteSchemaIsEmpty(db *gorm.DB) (bool, error) {
	var objects int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&objects).Error; err != nil {
		return false, fmt.Errorf("inspect unversioned sqlite schema: %w", err)
	}
	return objects == 0, nil
}

func verifySchemaShape(db *gorm.DB) error {
	return verifySchemaShapeWithModels(db, requiredSchemaModels)
}

func verifySchemaShapeWithoutConversationCompaction(db *gorm.DB) error {
	models := make([]any, 0, len(requiredSchemaModels)-1)
	for _, model := range requiredSchemaModels {
		if _, ok := model.(*conversationCompactionRow); ok {
			continue
		}
		models = append(models, model)
	}
	return verifySchemaShapeWithModels(db, models)
}

func verifySchemaShapeWithModels(db *gorm.DB, models []any) error {
	for _, model := range models {
		if err := verifyModelSchema(db, model); err != nil {
			return err
		}
	}
	for _, table := range obsoleteSchemaTables {
		if db.Migrator().HasTable(table) {
			return fmt.Errorf("sqlite schema still contains obsolete table %s", table)
		}
	}
	for _, column := range obsoleteFeedbackColumns {
		if db.Migrator().HasColumn("feedback_records", column) {
			return fmt.Errorf("sqlite schema still contains obsolete column feedback_records.%s", column)
		}
	}
	return nil
}

func verifyModelSchema(db *gorm.DB, model any) error {
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(model); err != nil {
		return fmt.Errorf("parse required sqlite model: %w", err)
	}
	table := statement.Schema.Table
	if !db.Migrator().HasTable(model) {
		return fmt.Errorf("sqlite schema is missing required table %s", table)
	}
	for _, field := range statement.Schema.Fields {
		if field.DBName == "" {
			continue
		}
		if !db.Migrator().HasColumn(model, field.DBName) {
			return fmt.Errorf("sqlite schema is missing required column %s.%s", table, field.DBName)
		}
	}
	for _, index := range statement.Schema.ParseIndexes() {
		columns := make([]string, 0, len(index.Fields))
		for _, field := range index.Fields {
			if field.Expression != "" || field.DBName == "" {
				return fmt.Errorf("sqlite schema verification does not support expression index %s on %s", index.Name, table)
			}
			columns = append(columns, field.DBName)
		}
		if err := verifySQLiteIndex(db, table, index.Name, strings.EqualFold(index.Class, "UNIQUE"), columns); err != nil {
			return err
		}
	}
	return nil
}

func verifySQLiteIndex(db *gorm.DB, table, name string, unique bool, columns []string) error {
	type indexListRow struct {
		Name    string `gorm:"column:name"`
		Unique  int    `gorm:"column:unique"`
		Partial int    `gorm:"column:partial"`
	}
	var indexes []indexListRow
	if err := db.Raw("PRAGMA index_list(" + quoteSQLiteIdentifier(table) + ")").Scan(&indexes).Error; err != nil {
		return fmt.Errorf("inspect sqlite indexes on %s: %w", table, err)
	}
	found := false
	for _, index := range indexes {
		if index.Name != name {
			continue
		}
		found = true
		if (index.Unique != 0) != unique {
			return fmt.Errorf("sqlite index %s on %s has wrong uniqueness", name, table)
		}
		if index.Partial != 0 {
			return fmt.Errorf("sqlite index %s on %s is unexpectedly partial", name, table)
		}
		break
	}
	if !found {
		return fmt.Errorf("sqlite schema is missing required index %s on %s", name, table)
	}
	type indexInfoRow struct {
		Sequence int    `gorm:"column:seqno"`
		Name     string `gorm:"column:name"`
	}
	var fields []indexInfoRow
	if err := db.Raw("PRAGMA index_info(" + quoteSQLiteIdentifier(name) + ")").Scan(&fields).Error; err != nil {
		return fmt.Errorf("inspect sqlite index %s on %s: %w", name, table, err)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Sequence < fields[j].Sequence })
	actual := make([]string, 0, len(fields))
	for _, field := range fields {
		actual = append(actual, field.Name)
	}
	if !slices.Equal(actual, columns) {
		return fmt.Errorf("sqlite index %s on %s has columns %v, want %v", name, table, actual, columns)
	}
	return nil
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
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
	return ensureUser(s.db.WithContext(ctx), ident, nowUTC())
}

func ensureUser(db *gorm.DB, ident Identity, now time.Time) (int64, error) {
	user := userRow{
		Platform:       ident.Platform,
		ExternalUserID: ident.UserID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "platform"}, {Name: "external_user_id"}},
		DoUpdates: clause.Assignments(map[string]any{"updated_at": now}),
	}).Create(&user).Error
	if err != nil {
		return 0, err
	}
	err = db.
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

// ConversationSurface is the product-level privacy boundary. Transport names
// stay at the adapters; policy code only needs to know whether participants
// share the conversation.
type ConversationSurface string

const (
	ConversationSurfaceUnknown ConversationSurface = "unknown"
	ConversationSurfaceDirect  ConversationSurface = "direct"
	ConversationSurfaceShared  ConversationSurface = "shared"
)

func SurfaceForConversation(ident Identity) ConversationSurface {
	switch textutil.LowerTrim(ident.ConversationType) {
	case "private", "guild_private":
		return ConversationSurfaceDirect
	case "group", "channel":
		return ConversationSurfaceShared
	default:
		return ConversationSurfaceUnknown
	}
}

func IsDirectConversation(ident Identity) bool {
	return SurfaceForConversation(ident) == ConversationSurfaceDirect
}

func IsSharedConversation(ident Identity) bool {
	return SurfaceForConversation(ident) == ConversationSurfaceShared
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
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return saveCredentialWithDB(tx, userID, cred, nowUTC())
	})
}

func saveCredentialWithDB(db *gorm.DB, userID int64, cred Credential, now time.Time) error {
	row := credentialRow{
		UserID: userID, ClientID: cred.ClientID, AccessToken: cred.AccessToken,
		RefreshToken: cred.RefreshToken, TokenType: cred.TokenType,
		ExpiresAt: cred.ExpiresAt.UTC(), Scope: cred.Scope, Resource: cred.Resource,
		UpdatedAt: now,
	}
	if err := db.Clauses(clause.OnConflict{
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
	}).Create(&row).Error; err != nil {
		return err
	}
	return db.Model(&notificationSettingRow{}).
		Where("user_id = ? AND reauth_required = ?", userID, true).
		Updates(map[string]any{"reauth_required": false, "updated_at": now}).Error
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
	now := nowUTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&credentialRow{}, "user_id = ?", userID).Error; err != nil {
			return err
		}
		return tx.Model(&notificationSettingRow{}).
			Where("user_id = ? AND (classes_enabled = ? OR homework_enabled = ?)", userID, true, true).
			Updates(map[string]any{"reauth_required": true, "updated_at": now}).Error
	})
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
			Where("user_id = ? AND status = ?", userID, string(LoginStatusPending)).
			Updates(map[string]any{"status": string(LoginStatusSuperseded), "updated_at": now}).Error; err != nil {
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
	if session.Status != string(LoginStatusPending) {
		return LoginSession{}, fmt.Errorf("new login session status must be %q", LoginStatusPending)
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
		Where("login_sessions.status = ?", string(LoginStatusPending)).
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

// TransitionLoginSession atomically commits a pending login's terminal state,
// optional credential, and optional durable result message. A false return
// means another actor already completed or superseded the session.
func (s *Store) TransitionLoginSession(
	ctx context.Context,
	ident Identity,
	deviceCode string,
	transition LoginTransition,
) (bool, error) {
	deviceCode = strings.TrimSpace(deviceCode)
	if deviceCode == "" {
		return false, errors.New("login session device code is empty")
	}
	if !terminalLoginStatus(transition.Status) {
		return false, fmt.Errorf("invalid terminal login status %q", transition.Status)
	}
	if transition.Status == LoginStatusApproved && transition.Credential == nil {
		return false, errors.New("approved login transition requires a credential")
	}
	if transition.Status != LoginStatusApproved && transition.Credential != nil {
		return false, errors.New("only approved login transition may save a credential")
	}
	var credential Credential
	var err error
	if transition.Credential != nil {
		credential, err = normalizeCredentialForSave(*transition.Credential)
		if err != nil {
			return false, err
		}
	}
	userID, ok, err := s.userID(ctx, ident)
	if err != nil || !ok {
		return false, err
	}
	now := nowUTC()
	transitioned := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&loginSessionRow{}).
			Where("user_id = ? AND device_code = ? AND status = ?", userID, deviceCode, string(LoginStatusPending)).
			Updates(map[string]any{"status": string(transition.Status), "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		transitioned = true
		if transition.Credential != nil {
			if err := saveCredentialWithDB(tx, userID, credential, now); err != nil {
				return err
			}
		}
		if transition.Outbound != nil {
			if _, _, err := enqueueWithDB(tx, *transition.Outbound, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return transitioned, err
}

func terminalLoginStatus(status LoginStatus) bool {
	switch status {
	case LoginStatusApproved, LoginStatusExpired, LoginStatusDenied, LoginStatusInvalid:
		return true
	default:
		return false
	}
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
	return s.handledInteractions(ctx, ident, 0, limit, true)
}

func (s *Store) HandledInteractionsAfter(ctx context.Context, ident Identity, afterID int64) ([]Interaction, error) {
	return s.handledInteractions(ctx, ident, afterID, 0, false)
}

func (s *Store) RecentHandledInteractionsAfter(ctx context.Context, ident Identity, afterID int64, limit int) ([]Interaction, error) {
	return s.handledInteractions(ctx, ident, afterID, limit, true)
}

func (s *Store) handledInteractions(ctx context.Context, ident Identity, afterID int64, limit int, newestFirst bool) ([]Interaction, error) {
	if err := validateConversationIdentity(ident); err != nil {
		return nil, err
	}
	if newestFirst && limit <= 0 {
		return nil, nil
	}
	ident = normalizeIdentity(ident)
	var rows []interactionRow
	query := s.db.WithContext(ctx).
		Where("platform = ? AND conversation_type = ? AND conversation_id = ? AND direction = ? AND handled = ? AND status = ?",
			ident.Platform, ident.ConversationType, ident.ConversationID, InteractionDirectionInbound, true, InteractionStatusHandled)
	if afterID > 0 {
		query = query.Where("id > ?", afterID)
	}
	if newestFirst {
		query = query.Order("created_at desc, id desc").Limit(limit)
	} else {
		query = query.Order("created_at asc, id asc")
	}
	err := query.Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]Interaction, 0, len(rows))
	appendRow := func(row interactionRow) {
		out = append(out, Interaction{
			ID:                row.ID,
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
	if newestFirst {
		for i := len(rows) - 1; i >= 0; i-- {
			appendRow(rows[i])
		}
	} else {
		for _, row := range rows {
			appendRow(row)
		}
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

func (s *Store) RecordAgentRun(ctx context.Context, ident Identity, run AgentRun) (int64, error) {
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
		JobID:            run.JobID,
		Platform:         ident.Platform,
		ExternalUserID:   ident.UserID,
		ConversationType: ident.ConversationType,
		ConversationID:   ident.ConversationID,
		RawText:          strings.TrimSpace(run.RawText),
		Provider:         textutil.LowerTrim(run.Provider),
		Model:            strings.TrimSpace(run.Model),
		Currency:         strings.ToUpper(strings.TrimSpace(run.Currency)),
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

func (s *Store) FinishAgentRun(ctx context.Context, id int64, status, reply string, runErr error, spending AgentSpending) error {
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
			"status":   status,
			"reply":    reply,
			"error":    strings.TrimSpace(errText),
			"currency": SpendingCurrencyCNY,
			// Usage may have been persisted immediately after a successful
			// provider response. Keep those monotonic observations when the run
			// finishes, even if its final cleanup snapshot is incomplete.
			"prompt_tokens":     gorm.Expr("MAX(prompt_tokens, ?)", spending.PromptTokens),
			"cached_tokens":     gorm.Expr("MAX(cached_tokens, ?)", spending.CachedTokens),
			"completion_tokens": gorm.Expr("MAX(completion_tokens, ?)", spending.CompletionTokens),
			"total_tokens":      gorm.Expr("MAX(total_tokens, ?)", spending.TotalTokens),
			"cost_nano_cny":     gorm.Expr("MAX(cost_nano_cny, ?)", spending.CostNanoCNY),
			// model_requests includes durable reservations made before physical
			// provider calls. Finishing a run may add observed usage, but must
			// never roll a crash-reserved attempt back down.
			"model_requests": gorm.Expr("MAX(model_requests, ?)", spending.ModelRequests),
			"tool_calls":     gorm.Expr("MAX(tool_calls, ?)", spending.ToolCalls),
			"updated_at":     nowUTC(),
		}).Error
}

// InterruptStartedAgentRuns closes runs left open by a previous process. It is
// intended to run once during startup before new agent work is accepted.
func (s *Store) InterruptStartedAgentRuns(ctx context.Context) (int64, error) {
	now := nowUTC()
	result := s.db.WithContext(ctx).Model(&agentRunRow{}).
		Where("status = ?", AgentRunStatusStarted).
		Updates(map[string]any{
			"status":     AgentRunStatusInterrupted,
			"error":      "process interrupted",
			"updated_at": now,
		})
	return result.RowsAffected, result.Error
}

func (s *Store) ConversationSpending(ctx context.Context, ident Identity) (AgentSpending, error) {
	if err := validateConversationIdentity(ident); err != nil {
		return AgentSpending{}, err
	}
	ident = normalizeIdentity(ident)
	return s.sumAgentSpending(s.db.WithContext(ctx).Model(&agentRunRow{}).
		Where("platform = ? AND conversation_type = ? AND conversation_id = ?",
			ident.Platform, ident.ConversationType, ident.ConversationID))
}

func (s *Store) UserSpending(ctx context.Context, ident Identity) (AgentSpending, error) {
	if err := validateIdentity(ident); err != nil {
		return AgentSpending{}, err
	}
	ident = normalizeIdentity(ident)
	return s.sumAgentSpending(s.db.WithContext(ctx).Model(&agentRunRow{}).
		Where("platform = ? AND external_user_id = ?", ident.Platform, ident.UserID))
}

func (s *Store) AgentJobSpending(ctx context.Context, jobID int64) (AgentSpending, error) {
	if jobID <= 0 {
		return AgentSpending{Currency: SpendingCurrencyCNY}, nil
	}
	return s.sumAgentSpending(s.db.WithContext(ctx).Model(&agentRunRow{}).Where("job_id = ?", jobID))
}

func (s *Store) sumAgentSpending(query *gorm.DB) (AgentSpending, error) {
	var total AgentSpending
	err := query.Select(
		"COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens, " +
			"COALESCE(SUM(cached_tokens), 0) AS cached_tokens, " +
			"COALESCE(SUM(completion_tokens), 0) AS completion_tokens, " +
			"COALESCE(SUM(total_tokens), 0) AS total_tokens, " +
			"COALESCE(SUM(cost_nano_cny), 0) AS cost_nano_cny, " +
			"COALESCE(SUM(model_requests), 0) AS model_requests, " +
			"COALESCE(SUM(tool_calls), 0) AS tool_calls",
	).Scan(&total).Error
	total.Currency = SpendingCurrencyCNY
	return total, err
}

func (s *Store) CreateFeedbackWithOutbounds(
	ctx context.Context,
	ident Identity,
	feedback FeedbackRecord,
	buildOutbounds func(int64) []message.Outbound,
) (int64, int, error) {
	if err := validateConversationIdentity(ident); err != nil {
		return 0, 0, err
	}
	ident = normalizeIdentity(ident)
	source := textutil.LowerTrim(feedback.Source)
	if source != "user" {
		return 0, 0, fmt.Errorf("invalid feedback source %q", source)
	}
	status := textutil.LowerTrim(feedback.Status)
	if status == "" {
		status = FeedbackStatusOpen
	}
	if status != FeedbackStatusOpen && status != FeedbackStatusResolved {
		return 0, 0, fmt.Errorf("invalid feedback status %q", status)
	}
	content := strings.TrimSpace(feedback.Content)
	if content == "" {
		return 0, 0, errors.New("feedback content is empty")
	}
	now := nowUTC()
	var row feedbackRecordRow
	intentCount := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		userID, err := ensureUser(tx, ident, now)
		if err != nil {
			return err
		}
		row = feedbackRecordRow{
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
			ResolvedAt:       feedback.ResolvedAt,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if buildOutbounds == nil {
			return nil
		}
		for _, outbound := range buildOutbounds(row.ID) {
			_, created, err := enqueueWithDB(tx, outbound, now)
			if err != nil {
				return err
			}
			if created {
				intentCount++
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return row.ID, intentCount, nil
}

func (s *Store) Enqueue(ctx context.Context, outbound message.Outbound) (delivery.Record, bool, error) {
	return enqueueWithDB(s.db.WithContext(ctx), outbound, nowUTC())
}

// ResolveResponseContext verifies that a referenced platform message is an
// accepted Bot output in the same conversation. A non-nil empty context still
// means the user replied to Presto; public invocations additionally carry the
// arguments needed for deterministic follow-up handling.
func (s *Store) ResolveResponseContext(ctx context.Context, conversation message.Conversation, platformMessageID string) (*message.ResponseContext, error) {
	platform := strings.ToLower(strings.TrimSpace(conversation.Platform))
	conversationType := strings.ToLower(strings.TrimSpace(conversation.Type))
	conversationID := strings.TrimSpace(conversation.ID)
	platformMessageID = strings.TrimSpace(platformMessageID)
	if platform == "" || conversationType == "" || conversationID == "" || platformMessageID == "" {
		return nil, nil
	}
	var row outgoingMessageRow
	err := s.db.WithContext(ctx).
		Where("platform = ? AND conversation_type = ? AND conversation_id = ? AND platform_message_id = ? AND status = ?",
			platform, conversationType, conversationID, platformMessageID, string(delivery.StatusAccepted)).
		Order("id DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record, err := outgoingMessageRecord(row)
	if err != nil {
		return nil, err
	}
	if record.Message.Context == nil {
		return &message.ResponseContext{}, nil
	}
	return &message.ResponseContext{
		Capability: strings.TrimSpace(record.Message.Context.Capability),
		Arguments:  append([]string(nil), record.Message.Context.Arguments...),
	}, nil
}

func enqueueWithDB(db *gorm.DB, outbound message.Outbound, now time.Time) (delivery.Record, bool, error) {
	if strings.TrimSpace(outbound.DedupeKey) == "" {
		return delivery.Record{}, false, errors.New("outgoing message dedupe key is empty")
	}
	if strings.TrimSpace(outbound.Target.Platform) == "" || strings.TrimSpace(outbound.Target.Type) == "" || strings.TrimSpace(outbound.Target.ID) == "" {
		return delivery.Record{}, false, errors.New("outgoing message target is incomplete")
	}
	if !outbound.Content.HasContent() {
		return delivery.Record{}, false, errors.New("outgoing message content is empty")
	}
	payload, err := json.Marshal(outbound)
	if err != nil {
		return delivery.Record{}, false, fmt.Errorf("encode outgoing message: %w", err)
	}
	row := outgoingMessageRow{
		DedupeKey:        strings.TrimSpace(outbound.DedupeKey),
		Kind:             strings.ToLower(strings.TrimSpace(outbound.Kind)),
		Platform:         strings.ToLower(strings.TrimSpace(outbound.Target.Platform)),
		ConversationType: strings.ToLower(strings.TrimSpace(outbound.Target.Type)),
		ConversationID:   strings.TrimSpace(outbound.Target.ID),
		PayloadJSON:      string(payload),
		Status:           string(delivery.StatusPending),
		NextAttemptAt:    &now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if !outbound.ExpiresAt.IsZero() {
		expiresAt := outbound.ExpiresAt.UTC()
		row.ExpiresAt = &expiresAt
	}
	result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil {
		return delivery.Record{}, false, result.Error
	}
	created := result.RowsAffected == 1
	if !created {
		if err := db.Where("dedupe_key = ?", row.DedupeKey).First(&row).Error; err != nil {
			return delivery.Record{}, false, err
		}
	}
	record, err := outgoingMessageRecord(row)
	return record, created, err
}

func (s *Store) ClaimDue(ctx context.Context, now time.Time, limit int) ([]delivery.Record, error) {
	if limit <= 0 {
		return nil, nil
	}
	now = now.UTC()
	records := make([]delivery.Record, 0, limit)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []outgoingMessageRow
		if err := tx.Where(
			"status IN ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?) AND (expires_at IS NULL OR expires_at > ?)",
			[]string{string(delivery.StatusPending), string(delivery.StatusRetryWait)}, now, now,
		).Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			row := &rows[i]
			result := tx.Model(&outgoingMessageRow{}).
				Where("id = ? AND status IN ?", row.ID, []string{string(delivery.StatusPending), string(delivery.StatusRetryWait)}).
				Updates(map[string]any{
					"status":             string(delivery.StatusDelivering),
					"attempts":           gorm.Expr("attempts + 1"),
					"attempt_started_at": now,
					"updated_at":         now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			row.Status = string(delivery.StatusDelivering)
			row.Attempts++
			row.AttemptStartedAt = &now
			record, err := outgoingMessageRecord(*row)
			if err != nil {
				return err
			}
			records = append(records, record)
		}
		return nil
	})
	return records, err
}

func (s *Store) Complete(ctx context.Context, id int64, outcome delivery.Outcome, nextAttemptAt time.Time) error {
	if id <= 0 {
		return errors.New("outgoing message id must be positive")
	}
	now := nowUTC()
	status := delivery.StatusUnknown
	switch outcome.State {
	case delivery.OutcomeAccepted:
		status = delivery.StatusAccepted
	case delivery.OutcomeRetryable:
		status = delivery.StatusRetryWait
	case delivery.OutcomeRejected:
		status = delivery.StatusRejected
	case delivery.OutcomeUnknown:
		status = delivery.StatusUnknown
	}
	errorMessage := ""
	if outcome.Err != nil {
		errorMessage = outcome.Err.Error()
		if len(errorMessage) > 4000 {
			errorMessage = errorMessage[:4000]
		}
	}
	updates := map[string]any{
		"status":              string(status),
		"next_attempt_at":     nil,
		"attempt_started_at":  nil,
		"platform_message_id": strings.TrimSpace(outcome.Receipt.PlatformMessageID),
		"delivery_method":     strings.TrimSpace(outcome.Receipt.DeliveryMethod),
		"source_message_id":   strings.TrimSpace(outcome.Receipt.SourceMessageID),
		"error_code":          strings.TrimSpace(outcome.Code),
		"error_message":       errorMessage,
		"updated_at":          now,
	}
	if status == delivery.StatusRetryWait && !nextAttemptAt.IsZero() {
		updates["next_attempt_at"] = nextAttemptAt.UTC()
	}
	if status == delivery.StatusAccepted {
		acceptedAt := outcome.Receipt.AcceptedAt.UTC()
		if acceptedAt.IsZero() {
			acceptedAt = now
		}
		updates["accepted_at"] = acceptedAt
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row outgoingMessageRow
		if err := tx.Where("id = ? AND status = ?", id, string(delivery.StatusDelivering)).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("outgoing message %d is not delivering", id)
			}
			return err
		}
		result := tx.Model(&outgoingMessageRow{}).
			Where("id = ? AND status = ?", id, string(delivery.StatusDelivering)).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("outgoing message %d is not delivering", id)
		}
		return nil
	})
}

func (s *Store) ExpireDue(ctx context.Context, now time.Time) error {
	now = now.UTC()
	return s.db.WithContext(ctx).Model(&outgoingMessageRow{}).
		Where("status IN ? AND expires_at IS NOT NULL AND expires_at <= ?",
			[]string{string(delivery.StatusPending), string(delivery.StatusRetryWait)}, now).
		Updates(map[string]any{
			"status":     string(delivery.StatusExpired),
			"error_code": "expired",
			"updated_at": now,
		}).Error
}

// OutgoingMessageRetention is how long a terminal outbox row is kept as the
// delivery audit trail.
const OutgoingMessageRetention = 30 * 24 * time.Hour

// outgoingAttachmentSlimThreshold skips rows that were never large enough to
// matter, so pruning does not rewrite every short text message.
const outgoingAttachmentSlimThreshold = 16 << 10

// PruneOutgoingMessages bounds what the outbox retains. It deletes terminal
// rows past the retention window and strips attachment bytes from the rest.
//
// The payload cannot simply be cleared: an accepted row is still read to
// resolve a user's reply back to the Bot message and its invocation. Only the
// rendered bytes are dropped, and only once no retry can consume them. A single
// rendered bus card is nearly 3 MB of base64, which is how 335 rows grew into
// 37 MB of a 45 MB production database.
func (s *Store) PruneOutgoingMessages(ctx context.Context, now time.Time) error {
	terminal := []string{
		string(delivery.StatusAccepted), string(delivery.StatusRejected),
		string(delivery.StatusUnknown), string(delivery.StatusExpired),
	}
	if err := s.db.WithContext(ctx).
		Where("status IN ? AND updated_at <= ?", terminal, now.UTC().Add(-OutgoingMessageRetention)).
		Delete(&outgoingMessageRow{}).Error; err != nil {
		return err
	}
	var rows []outgoingMessageRow
	if err := s.db.WithContext(ctx).
		Where("status IN ? AND length(payload_json) > ?", terminal, outgoingAttachmentSlimThreshold).
		Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		slimmed, changed, err := payloadWithoutAttachmentData(row.PayloadJSON)
		if err != nil || !changed {
			continue
		}
		if err := s.db.WithContext(ctx).Model(&outgoingMessageRow{}).
			Where("id = ? AND status IN ?", row.ID, terminal).
			Update("payload_json", slimmed).Error; err != nil {
			return err
		}
	}
	return nil
}

// payloadWithoutAttachmentData drops the rendered bytes while keeping every
// field a reply lookup or an audit needs, including the attachment's type and
// alt text.
func payloadWithoutAttachmentData(payloadJSON string) (string, bool, error) {
	var outbound message.Outbound
	if err := json.Unmarshal([]byte(payloadJSON), &outbound); err != nil {
		return "", false, err
	}
	changed := false
	for index, part := range outbound.Content.Parts {
		if part.Attachment == nil || len(part.Attachment.Data) == 0 {
			continue
		}
		attachment := *part.Attachment
		attachment.Data = nil
		outbound.Content.Parts[index].Attachment = &attachment
		changed = true
	}
	if !changed {
		return "", false, nil
	}
	encoded, err := json.Marshal(outbound)
	if err != nil {
		return "", false, err
	}
	return string(encoded), true, nil
}

func (s *Store) RecoverStale(ctx context.Context, before time.Time) error {
	now := nowUTC()
	return s.db.WithContext(ctx).Model(&outgoingMessageRow{}).
		Where("status = ? AND attempt_started_at IS NOT NULL AND attempt_started_at <= ?", string(delivery.StatusDelivering), before.UTC()).
		Updates(map[string]any{
			"status":             string(delivery.StatusUnknown),
			"attempt_started_at": nil,
			"error_code":         "worker_interrupted",
			"error_message":      "delivery worker stopped while the platform outcome was unknown",
			"updated_at":         now,
		}).Error
}

func outgoingMessageRecord(row outgoingMessageRow) (delivery.Record, error) {
	var outbound message.Outbound
	if err := json.Unmarshal([]byte(row.PayloadJSON), &outbound); err != nil {
		return delivery.Record{}, fmt.Errorf("decode outgoing message %d: %w", row.ID, err)
	}
	return delivery.Record{
		ID:               row.ID,
		Message:          outbound,
		Status:           delivery.Status(row.Status),
		Attempts:         row.Attempts,
		NextAttemptAt:    dereferenceTime(row.NextAttemptAt),
		AttemptStartedAt: dereferenceTime(row.AttemptStartedAt),
		Receipt: message.Receipt{
			PlatformMessageID: row.PlatformMessageID,
			DeliveryMethod:    row.DeliveryMethod,
			SourceMessageID:   row.SourceMessageID,
			AcceptedAt:        dereferenceTime(row.AcceptedAt),
		},
		ErrorCode:    row.ErrorCode,
		ErrorMessage: row.ErrorMessage,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

func (s *Store) FeedbackCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&feedbackRecordRow{}).Count(&count).Error
	return count, err
}

func normalizeStoreTime(now time.Time) time.Time {
	if now.IsZero() {
		return nowUTC()
	}
	return now.UTC()
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
	enabled := settings.ClassesEnabled || settings.HomeworkEnabled
	var credentialCount int64
	if enabled {
		if err := s.db.WithContext(ctx).Model(&credentialRow{}).
			Where("user_id = ?", userID).
			Count(&credentialCount).Error; err != nil {
			return err
		}
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
		ReauthRequired:   enabled && credentialCount == 0,
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
			"reauth_required",
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
		Where("(classes_enabled = ? OR homework_enabled = ?) AND reauth_required = ? AND conversation_type <> ? AND conversation_id <> ?",
			true, true, false, "", "").
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

func (s *Store) PauseNotificationsForReauth(ctx context.Context, ident Identity) error {
	userID, ok, err := s.userID(ctx, ident)
	if err != nil || !ok {
		return err
	}
	return s.db.WithContext(ctx).Model(&notificationSettingRow{}).
		Where("user_id = ?", userID).
		Updates(map[string]any{"reauth_required": true, "updated_at": nowUTC()}).Error
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
		ReauthRequired:  row.ReauthRequired,
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

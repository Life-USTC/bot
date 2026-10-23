-- Schema v7 from the deployed 57722fb database; no production row values.

CREATE TABLE `agent_checkpoints` (`id` text,`job_id` integer NOT NULL DEFAULT 0,`revision` integer NOT NULL DEFAULT 0,`lease_token` text NOT NULL DEFAULT "",`payload` blob NOT NULL,`created_at` datetime,`updated_at` datetime,PRIMARY KEY (`id`));

CREATE TABLE `agent_runs` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer NOT NULL,`platform` text NOT NULL,`external_user_id` text NOT NULL,`conversation_type` text NOT NULL,`conversation_id` text NOT NULL,`raw_text` text NOT NULL,`status` text NOT NULL,`reply` text,`error` text,`created_at` datetime,`updated_at` datetime, `provider` text NOT NULL DEFAULT "", `model` text NOT NULL DEFAULT "", `currency` text NOT NULL DEFAULT "CNY", `prompt_tokens` integer NOT NULL DEFAULT 0, `cached_tokens` integer NOT NULL DEFAULT 0, `completion_tokens` integer NOT NULL DEFAULT 0, `total_tokens` integer NOT NULL DEFAULT 0, `cost_nano_cny` integer NOT NULL DEFAULT 0, `model_requests` integer NOT NULL DEFAULT 0, `tool_calls` integer NOT NULL DEFAULT 0, `job_id` integer NOT NULL DEFAULT 0);

CREATE TABLE `bus_settings` (`user_id` integer PRIMARY KEY AUTOINCREMENT,`platform` text NOT NULL,`external_user_id` text NOT NULL,`show_south_campus` numeric NOT NULL,`updated_at` datetime);

CREATE TABLE `capability_executions` (`id` text,`user_id` integer NOT NULL,`platform` text NOT NULL,`external_user_id` text NOT NULL,`conversation_type` text NOT NULL,`conversation_id` text NOT NULL,`job_id` integer NOT NULL,`sequence` integer NOT NULL,`dedupe_key` text NOT NULL,`tool_call_id` text NOT NULL DEFAULT "",`lease_token` text,`capability` text NOT NULL,`arguments_json` text NOT NULL,`effect` text NOT NULL,`state` text NOT NULL,`receipt_json` text NOT NULL DEFAULT "{}",`result` text,`error` text,`confirmed_at` datetime,`started_at` datetime,`finished_at` datetime,`receipt_sent_at` datetime,`receipt_state` text NOT NULL DEFAULT "",`created_at` datetime,`updated_at` datetime, confirmation_outbox_id INTEGER, confirmation_event_id TEXT,PRIMARY KEY (`id`));

CREATE TABLE command_images (id text, platform text NOT NULL, user_id text NOT NULL, conversation_type text NOT NULL, conversation_id text NOT NULL, key text NOT NULL, image_json text NOT NULL, created_at datetime, updated_at datetime, PRIMARY KEY (id));

CREATE TABLE `conversation_compactions` (`id` integer PRIMARY KEY AUTOINCREMENT,`platform` text NOT NULL,`conversation_type` text NOT NULL,`conversation_id` text NOT NULL,`covered_event_id` integer NOT NULL DEFAULT 0,`summary` text NOT NULL,`claim_token` text NOT NULL DEFAULT "",`claim_expires_at` datetime,`created_at` datetime,`updated_at` datetime);

CREATE TABLE `conversation_events` (`id` integer PRIMARY KEY AUTOINCREMENT,`platform` text NOT NULL,`conversation_type` text NOT NULL,`conversation_id` text NOT NULL,`external_user_id` text NOT NULL,`job_id` integer NOT NULL DEFAULT 0,`dedupe_key` text NOT NULL,`type` text NOT NULL,`content` text,`name` text,`tool_call_id` text,`tool_name` text,`tool_calls_json` text NOT NULL DEFAULT "[]",`parts_json` text NOT NULL DEFAULT "[]",`created_at` datetime, actor_display_name text, source text, occurred_at datetime);

CREATE TABLE `conversation_job_sequences` (`id` integer PRIMARY KEY AUTOINCREMENT,`platform` text NOT NULL,`conversation_type` text NOT NULL,`conversation_id` text NOT NULL,`next_sequence` integer NOT NULL DEFAULT 0,`created_at` datetime,`updated_at` datetime);

CREATE TABLE `conversation_jobs` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer NOT NULL,`platform` text NOT NULL,`external_user_id` text NOT NULL,`conversation_type` text NOT NULL,`conversation_id` text NOT NULL,`sequence` integer NOT NULL,`source_event_id` text NOT NULL,`state` text NOT NULL,`wait_reason` text NOT NULL DEFAULT "",`input_json` text NOT NULL,`invocation_json` text NOT NULL,`lease_token` text,`claimed_at` datetime,`revision` integer NOT NULL DEFAULT 1,`attempts` integer NOT NULL DEFAULT 0,`max_attempts` integer NOT NULL DEFAULT 0,`retry_at` datetime,`expires_at` datetime,`last_error` text,`created_at` datetime,`updated_at` datetime, `terminal_lease_token` text NOT NULL DEFAULT "");

CREATE TABLE `conversation_states` (`id` integer PRIMARY KEY AUTOINCREMENT,`platform` text NOT NULL,`conversation_type` text NOT NULL,`conversation_id` text NOT NULL,`user_id` text NOT NULL,`last_command` text NOT NULL,`state` text,`created_at` datetime);

CREATE TABLE `credentials` (`user_id` integer PRIMARY KEY AUTOINCREMENT,`client_id` text NOT NULL,`access_token` text NOT NULL,`refresh_token` text,`token_type` text,`expires_at` datetime NOT NULL,`scope` text,`resource` text,`updated_at` datetime);

CREATE TABLE `feedback_records` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer NOT NULL,`platform` text NOT NULL,`external_user_id` text NOT NULL,`conversation_type` text NOT NULL,`conversation_id` text NOT NULL,`source` text NOT NULL,`category` text,`content` text NOT NULL,`context` text,`status` text NOT NULL,`resolved_at` datetime,`created_at` datetime,`updated_at` datetime);

CREATE TABLE `interactions` (`id` integer PRIMARY KEY AUTOINCREMENT,`platform` text NOT NULL,`conversation_type` text NOT NULL,`conversation_id` text NOT NULL,`user_id` text NOT NULL,`direction` text NOT NULL DEFAULT "inbound",`raw_text` text NOT NULL,`command` text,`args` text,`handled` numeric NOT NULL,`reply` text,`status` text,`error` text,`created_at` datetime, `platform_message_id` text, `accepted_at` datetime, `delivery_method` text, `source_message_id` text);

CREATE TABLE `login_sessions` (`id` integer PRIMARY KEY AUTOINCREMENT,`user_id` integer NOT NULL,`platform` text,`external_user_id` text,`conversation_type` text,`conversation_id` text,`device_code` text,`user_code` text,`verification_uri` text,`verification_uri_complete` text,`client_id` text,`expires_at` datetime NOT NULL,`interval_seconds` integer,`status` text NOT NULL,`created_at` datetime,`updated_at` datetime, `resources` text);

CREATE TABLE "notification_settings"  (`user_id` integer PRIMARY KEY AUTOINCREMENT,`platform` text NOT NULL,`external_user_id` text NOT NULL,`conversation_type` text,`conversation_id` text,`classes_enabled` numeric NOT NULL,`homework_enabled` numeric NOT NULL,`updated_at` datetime,`reauth_required` numeric NOT NULL DEFAULT false, `young_enabled` numeric NOT NULL DEFAULT false, `todos_enabled` numeric NOT NULL DEFAULT false);

CREATE TABLE `outgoing_messages` (`id` integer PRIMARY KEY AUTOINCREMENT,`dedupe_key` text NOT NULL,`kind` text NOT NULL,`platform` text NOT NULL,`conversation_type` text NOT NULL,`conversation_id` text NOT NULL,`payload_json` text NOT NULL,`status` text NOT NULL,`attempts` integer NOT NULL DEFAULT 0,`next_attempt_at` datetime,`attempt_started_at` datetime,`expires_at` datetime,`platform_message_id` text,`delivery_method` text,`source_message_id` text,`error_code` text,`error_message` text,`accepted_at` datetime,`created_at` datetime,`updated_at` datetime);

CREATE TABLE `public_command_cache` (`id` integer PRIMARY KEY AUTOINCREMENT,`version` text NOT NULL,`command` text NOT NULL,`args` text NOT NULL,`response` text NOT NULL,`expires_at` datetime NOT NULL,`created_at` datetime,`updated_at` datetime);

CREATE TABLE `users` (`id` integer PRIMARY KEY AUTOINCREMENT,`platform` text NOT NULL,`external_user_id` text NOT NULL,`created_at` datetime,`updated_at` datetime, `display_name` text, `display_name_seen_at` datetime);

CREATE INDEX `idx_agent_checkpoints_job_id` ON `agent_checkpoints`(`job_id`);

CREATE INDEX `idx_agent_checkpoints_lease_token` ON `agent_checkpoints`(`lease_token`);

CREATE INDEX `idx_agent_checkpoints_revision` ON `agent_checkpoints`(`revision`);

CREATE INDEX `idx_agent_runs_conversation_created` ON `agent_runs`(`platform`,`conversation_type`,`conversation_id`,`created_at`);

CREATE INDEX `idx_agent_runs_job_id` ON `agent_runs`(`job_id`);

CREATE INDEX `idx_agent_runs_status` ON `agent_runs`(`status`);

CREATE INDEX `idx_agent_runs_user_id` ON `agent_runs`(`user_id`);

CREATE UNIQUE INDEX idx_capability_executions_confirmation_event ON capability_executions(platform, confirmation_event_id);

CREATE INDEX idx_capability_executions_confirmation_outbox_id ON capability_executions(confirmation_outbox_id);

CREATE UNIQUE INDEX `idx_capability_executions_dedupe` ON `capability_executions`(`platform`,`dedupe_key`);

CREATE INDEX `idx_capability_executions_identity_state` ON `capability_executions`(`platform`,`conversation_type`,`conversation_id`,`external_user_id`,`state`);

CREATE INDEX `idx_capability_executions_job_sequence` ON `capability_executions`(`job_id`,`sequence`);

CREATE INDEX `idx_capability_executions_lease_token` ON `capability_executions`(`lease_token`);

CREATE INDEX `idx_capability_executions_receipt_sent_at` ON `capability_executions`(`receipt_sent_at`);

CREATE INDEX `idx_capability_executions_tool_call_id` ON `capability_executions`(`tool_call_id`);

CREATE INDEX `idx_capability_executions_user_id` ON `capability_executions`(`user_id`);

CREATE UNIQUE INDEX idx_command_images_scope_key ON command_images(platform,user_id,conversation_type,conversation_id,key);

CREATE INDEX `idx_conversation_compactions_claim_expires_at` ON `conversation_compactions`(`claim_expires_at`);

CREATE UNIQUE INDEX `idx_conversation_compactions_identity` ON `conversation_compactions`(`platform`,`conversation_type`,`conversation_id`);

CREATE UNIQUE INDEX `idx_conversation_events_dedupe` ON `conversation_events`(`platform`,`dedupe_key`);

CREATE INDEX `idx_conversation_events_identity_id` ON `conversation_events`(`platform`,`conversation_type`,`conversation_id`,`created_at`);

CREATE INDEX `idx_conversation_events_job_id` ON `conversation_events`(`job_id`);

CREATE INDEX idx_conversation_events_occurred_at ON conversation_events(occurred_at);

CREATE UNIQUE INDEX `idx_conversation_job_sequences_identity` ON `conversation_job_sequences`(`platform`,`conversation_type`,`conversation_id`);

CREATE INDEX `idx_conversation_jobs_claimed_at` ON `conversation_jobs`(`claimed_at`);

CREATE UNIQUE INDEX `idx_conversation_jobs_conversation_sequence` ON `conversation_jobs`(`platform`,`conversation_type`,`conversation_id`,`sequence`);

CREATE INDEX `idx_conversation_jobs_conversation_state` ON `conversation_jobs`(`platform`,`conversation_type`,`conversation_id`,`state`);

CREATE INDEX `idx_conversation_jobs_created_at` ON `conversation_jobs`(`created_at`);

CREATE INDEX `idx_conversation_jobs_lease_token` ON `conversation_jobs`(`lease_token`);

CREATE INDEX `idx_conversation_jobs_sequence` ON `conversation_jobs`(`sequence`);

CREATE UNIQUE INDEX `idx_conversation_jobs_source_event` ON `conversation_jobs`(`platform`,`source_event_id`);

CREATE INDEX `idx_conversation_jobs_state_expiry` ON `conversation_jobs`(`state`,`retry_at`,`expires_at`);

CREATE INDEX `idx_conversation_jobs_user_id` ON `conversation_jobs`(`user_id`);

CREATE INDEX `idx_feedback_records_conversation_created` ON `feedback_records`(`platform`,`conversation_type`,`conversation_id`,`created_at`);

CREATE INDEX `idx_feedback_records_source` ON `feedback_records`(`source`);

CREATE INDEX `idx_feedback_records_status` ON `feedback_records`(`status`);

CREATE INDEX `idx_feedback_records_user_id` ON `feedback_records`(`user_id`);

CREATE INDEX `idx_interactions_conversation_created` ON `interactions`(`platform`,`conversation_type`,`conversation_id`,`created_at`);

CREATE INDEX `idx_login_sessions_user_status` ON `login_sessions`(`user_id`,`expires_at`,`status`);

CREATE UNIQUE INDEX `idx_outgoing_messages_dedupe_key` ON `outgoing_messages`(`dedupe_key`);

CREATE INDEX `idx_outgoing_messages_due` ON `outgoing_messages`(`platform`,`status`,`next_attempt_at`);

CREATE INDEX `idx_outgoing_messages_expires_at` ON `outgoing_messages`(`expires_at`);

CREATE INDEX `idx_outgoing_messages_kind` ON `outgoing_messages`(`kind`);

CREATE INDEX `idx_public_command_cache_expires_at` ON `public_command_cache`(`expires_at`);

CREATE UNIQUE INDEX `idx_public_command_cache_key` ON `public_command_cache`(`version`,`command`,`args`);

CREATE UNIQUE INDEX `idx_users_platform_external_user` ON `users`(`platform`,`external_user_id`);

PRAGMA user_version = 7;

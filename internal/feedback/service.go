package feedback

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/store"
)

const SourceUser = "user"

type Submission struct {
	Category string
	Content  string
	Context  string
}

type Result struct {
	ID           int64
	AdminIntents int
}

type Recorder interface {
	Record(context.Context, store.Identity, Submission) (Result, error)
}

type Repository interface {
	CreateFeedbackWithOutbounds(
		context.Context,
		store.Identity,
		store.FeedbackRecord,
		func(int64) []message.Outbound,
	) (int64, int, error)
}

type Target struct {
	Platform         string
	ConversationType string
	ConversationID   string
}

type Config struct {
	Targets []Target
}

type Service struct {
	repository Repository
	targets    []Target
}

func New(repository Repository, cfg Config) (*Service, error) {
	if repository == nil {
		return nil, errors.New("feedback repository is unavailable")
	}
	targets := make([]Target, 0, len(cfg.Targets))
	seen := make(map[string]struct{}, len(cfg.Targets))
	for _, target := range cfg.Targets {
		target.Platform = strings.ToLower(strings.TrimSpace(target.Platform))
		target.ConversationType = strings.ToLower(strings.TrimSpace(target.ConversationType))
		target.ConversationID = strings.TrimSpace(target.ConversationID)
		if target.Platform == "" || target.ConversationID == "" {
			return nil, errors.New("feedback target platform and conversation id are required")
		}
		if target.ConversationType != "private" && target.ConversationType != "group" {
			return nil, fmt.Errorf("invalid feedback target conversation type %q", target.ConversationType)
		}
		key := target.Platform + ":" + target.ConversationType + ":" + target.ConversationID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, target)
	}
	return &Service{repository: repository, targets: targets}, nil
}

func (s *Service) Record(ctx context.Context, ident store.Identity, submission Submission) (Result, error) {
	if s == nil || s.repository == nil {
		return Result{}, errors.New("feedback service is unavailable")
	}
	submission.Category = strings.TrimSpace(submission.Category)
	submission.Content = strings.TrimSpace(submission.Content)
	submission.Context = strings.TrimSpace(submission.Context)
	if submission.Content == "" {
		return Result{}, errors.New("feedback content is required")
	}

	id, intents, err := s.repository.CreateFeedbackWithOutbounds(ctx, ident, store.FeedbackRecord{
		Source:   SourceUser,
		Category: submission.Category,
		Content:  submission.Content,
		Context:  submission.Context,
		Status:   store.FeedbackStatusOpen,
	}, func(id int64) []message.Outbound {
		content := formatAdminMessage(ident, id, submission)
		outbounds := make([]message.Outbound, 0, len(s.targets))
		for _, target := range s.targets {
			outbounds = append(outbounds, message.Outbound{
				Kind: "feedback_admin",
				Target: message.Conversation{
					Platform: target.Platform,
					Type:     target.ConversationType,
					ID:       target.ConversationID,
				},
				Content: message.Content{Parts: []message.ContentPart{{Text: content}}},
				DedupeKey: fmt.Sprintf("feedback:%d:%s:%s:%s",
					id, target.Platform, target.ConversationType, target.ConversationID),
			})
		}
		return outbounds
	})
	if err != nil {
		return Result{}, err
	}
	return Result{ID: id, AdminIntents: intents}, nil
}

func formatAdminMessage(ident store.Identity, id int64, submission Submission) string {
	source := strings.TrimSpace(ident.ConversationType)
	if conversationID := strings.TrimSpace(ident.ConversationID); conversationID != "" {
		source += ":" + conversationID
	}
	if source == "" {
		source = "unknown"
	}
	userID := strings.TrimSpace(ident.UserID)
	if userID == "" {
		userID = "unknown"
	}
	lines := []string{"用户反馈", "来源：" + source, "用户：" + userID}
	if submission.Category != "" {
		lines = append(lines, "分类："+submission.Category)
	}
	lines = append(lines, "内容："+submission.Content)
	if submission.Context != "" {
		lines = append(lines, "最近对话：", submission.Context)
	}
	lines = append(lines, fmt.Sprintf("编号：#%d", id))
	return strings.Join(lines, "\n")
}

func AdminTargets(platform string, users, groups []string) []Target {
	platform = strings.ToLower(strings.TrimSpace(platform))
	targets := make([]Target, 0, len(users)+len(groups))
	for _, id := range users {
		if id = strings.TrimSpace(id); id != "" {
			targets = append(targets, Target{Platform: platform, ConversationType: "private", ConversationID: id})
		}
	}
	for _, id := range groups {
		if id = strings.TrimSpace(id); id != "" {
			targets = append(targets, Target{Platform: platform, ConversationType: "group", ConversationID: id})
		}
	}
	return targets
}

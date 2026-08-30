package commands

import (
	"context"
	"errors"
	"strings"

	"github.com/Life-USTC/Bot/internal/store"
)

// BeginLoginForRequest persists the original text before starting (or reusing)
// device login. The login poller replays that text through BotApp after the
// credential is committed.
func (h Handler) BeginLoginForRequest(ctx context.Context, input Input) (Response, error) {
	if h.Auth == nil || h.Auth.Store == nil {
		return Response{}, errors.New("login is unavailable")
	}
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return Response{}, errors.New("pending login request is empty")
	}
	stateStore := h.Store
	if stateStore == nil {
		stateStore = h.Auth.Store
	}
	if _, err := stateStore.SavePendingRequest(ctx, input.Identity, text, store.PendingRequestTTL); err != nil {
		return Response{}, err
	}
	session, err := stateStore.ActiveLoginSession(ctx, input.Identity)
	if err != nil {
		return Response{}, err
	}
	if session == nil {
		if err := h.Auth.Logout(ctx, input.Identity); err != nil {
			return Response{}, err
		}
		session, err = h.Auth.BeginDeviceLogin(ctx, input.Identity)
		if err != nil {
			return Response{}, err
		}
	}
	return Response{Text: loginResumeInstructions(*session), Kind: "login"}, nil
}

func loginResumeInstructions(session store.LoginSession) string {
	link := strings.TrimSpace(session.VerificationURIComplete)
	if link == "" {
		link = strings.TrimSpace(session.VerificationURI)
	}
	return strings.Join([]string{
		"需要登录 Life @ USTC：",
		link,
		"验证码：" + strings.TrimSpace(session.UserCode),
		"完成后我会自动继续刚才的请求，无需重发。",
	}, "\n")
}

func replyRequiresLogin(reply string) bool {
	return strings.Contains(reply, "需要先登录。发送：登录") ||
		strings.Contains(reply, "登录已过期。发送：登录") ||
		strings.Contains(reply, "请发送：登录") ||
		strings.Contains(reply, "请发送“登录”")
}

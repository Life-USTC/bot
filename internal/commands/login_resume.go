package commands

import (
	"context"
	"errors"
	"strings"

	"github.com/Life-USTC/Bot/internal/store"
)

// BeginLoginForRequest starts or reuses device login for the current durable
// conversation job. The coordinator owns the original request and moves that
// same job to waiting_auth after this response is persisted.
func (h Handler) BeginLoginForRequest(ctx context.Context, input Input) (Response, error) {
	if h.Auth == nil || h.Auth.Store == nil {
		return Response{}, errors.New("login is unavailable")
	}
	stateStore := h.Auth.Store
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
	return Response{Text: loginResumeInstructions(*session), Kind: ResponseKindAuthWait}, nil
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

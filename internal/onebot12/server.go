package onebot12

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	libob "github.com/botuniverse/go-libonebot"

	"github.com/Life-USTC/Bot/internal/auth"
	"github.com/Life-USTC/Bot/internal/life"
	"github.com/Life-USTC/Bot/internal/store"
)

const actionPrefix = "life_ustc"

type Server struct {
	onebot *libob.OneBot
	life   *life.Client
	auth   *auth.Manager
}

type Config struct {
	Host        string
	Port        uint16
	AccessToken string
	SelfID      string
	Auth        *auth.Manager
}

func New(cfg Config, lifeClient *life.Client) *Server {
	config := &libob.Config{
		Comm: libob.ConfigComm{
			HTTP: []libob.ConfigCommHTTP{{
				Host:            cfg.Host,
				Port:            cfg.Port,
				AccessToken:     cfg.AccessToken,
				EventEnabled:    true,
				EventBufferSize: 100,
			}},
		},
		Heartbeat: libob.ConfigHeartbeat{
			Enabled:  true,
			Interval: 30000,
		},
	}
	self := &libob.Self{Platform: "life-ustc", UserID: cfg.SelfID}
	s := &Server{
		onebot: libob.NewOneBot("life-ustc-bot", self, config),
		life:   lifeClient,
		auth:   cfg.Auth,
	}
	s.onebot.Handle(s.mux())
	return s
}

func (s *Server) Run() {
	s.onebot.Run()
}

func (s *Server) Shutdown() {
	s.onebot.Shutdown()
}

func (s *Server) mux() *libob.ActionMux {
	mux := libob.NewActionMux()
	mux.HandleFunc(libob.ActionGetStatus, func(w libob.ResponseWriter, r *libob.Request) {
		if s.life == nil {
			w.WriteData(map[string]any{"good": false, "online": false})
			return
		}
		ctx, cancel := ContextWithTimeout()
		defer cancel()
		err := s.life.Health(ctx)
		w.WriteData(map[string]any{"good": err == nil, "online": err == nil})
	})
	mux.HandleFunc(actionPrefix+".catalog_semester_current", s.currentSemester)
	mux.HandleFunc(actionPrefix+".catalog_course_search", s.searchCourses)
	mux.HandleFunc(actionPrefix+".catalog_section_search", s.searchSections)
	mux.HandleFunc(actionPrefix+".catalog_bus_timetable_get", s.bus)
	mux.HandleFunc(actionPrefix+".catalog_link_list", s.catalogLinks)
	mux.HandleFunc(actionPrefix+".account_login_begin", s.beginLogin)
	mux.HandleFunc(actionPrefix+".account_login_poll", s.pollLogin)
	mux.HandleFunc(actionPrefix+".account_profile_get", s.me)
	mux.HandleFunc(actionPrefix+".workspace_todo_list", s.todos)
	mux.HandleFunc(actionPrefix+".workspace_link_pin_list", s.linkPins)
	mux.HandleFunc(actionPrefix+".workspace_link_pin_set", s.setLinkPin)
	return mux
}

func (s *Server) currentSemester(w libob.ResponseWriter, r *libob.Request) {
	lifeClient, ok := s.lifeClientForAction(w)
	if !ok {
		return
	}
	ctx, cancel := ContextWithTimeout()
	defer cancel()
	data, err := lifeClient.CurrentSemester(ctx)
	write(w, data, err)
}

func (s *Server) searchCourses(w libob.ResponseWriter, r *libob.Request) {
	lifeClient, ok := s.lifeClientForAction(w)
	if !ok {
		return
	}
	p := libob.NewParamGetter(w, r)
	search, ok := p.GetString("search")
	if !ok {
		return
	}
	ctx, cancel := ContextWithTimeout()
	defer cancel()
	data, err := lifeClient.SearchCourses(ctx, search, 5)
	write(w, data, err)
}

func (s *Server) searchSections(w libob.ResponseWriter, r *libob.Request) {
	lifeClient, ok := s.lifeClientForAction(w)
	if !ok {
		return
	}
	p := libob.NewParamGetter(w, r)
	search, ok := p.GetString("search")
	if !ok {
		return
	}
	ctx, cancel := ContextWithTimeout()
	defer cancel()
	data, err := lifeClient.SearchSections(ctx, search, 5)
	write(w, data, err)
}

func (s *Server) bus(w libob.ResponseWriter, r *libob.Request) {
	lifeClient, ok := s.lifeClientForAction(w)
	if !ok {
		return
	}
	ctx, cancel := ContextWithTimeout()
	defer cancel()
	data, err := lifeClient.Bus(ctx)
	write(w, data, err)
}

func (s *Server) catalogLinks(w libob.ResponseWriter, _ *libob.Request) {
	lifeClient, ok := s.lifeClientForAction(w)
	if !ok {
		return
	}
	ctx, cancel := ContextWithTimeout()
	defer cancel()
	data, err := lifeClient.CatalogLinks(ctx)
	write(w, data, err)
}

func (s *Server) beginLogin(w libob.ResponseWriter, r *libob.Request) {
	if s.auth == nil {
		w.WriteFailed(libob.RetCodeUnsupportedAction, fmt.Errorf("login is not configured"))
		return
	}
	ident, ok := identityFromParams(w, r)
	if !ok {
		return
	}
	ctx, cancel := ContextWithTimeout()
	defer cancel()
	data, err := s.auth.BeginDeviceLogin(ctx, ident)
	write(w, data, err)
}

func (s *Server) pollLogin(w libob.ResponseWriter, r *libob.Request) {
	if s.auth == nil {
		w.WriteFailed(libob.RetCodeUnsupportedAction, fmt.Errorf("login is not configured"))
		return
	}
	ident, ok := identityFromParams(w, r)
	if !ok {
		return
	}
	ctx, cancel := ContextWithTimeout()
	defer cancel()
	data, err := s.auth.PollDeviceLogin(ctx, ident)
	write(w, data, err)
}

func (s *Server) me(w libob.ResponseWriter, r *libob.Request) {
	lifeClient, ok := s.lifeClientForAction(w)
	if !ok {
		return
	}
	ctx, cancel := ContextWithTimeout()
	defer cancel()
	ident, token, ok := s.accessTokenForAction(ctx, w, r)
	if !ok {
		return
	}
	data, err := auth.WithRefresh(ctx, s.auth, ident, token, func(token string) (map[string]any, error) {
		return lifeClient.Me(ctx, token)
	})
	write(w, data, err)
}

func (s *Server) todos(w libob.ResponseWriter, r *libob.Request) {
	lifeClient, ok := s.lifeClientForAction(w)
	if !ok {
		return
	}
	ctx, cancel := ContextWithTimeout()
	defer cancel()
	ident, token, ok := s.accessTokenForAction(ctx, w, r)
	if !ok {
		return
	}
	data, err := auth.WithRefresh(ctx, s.auth, ident, token, func(token string) ([]map[string]any, error) {
		return lifeClient.Todos(ctx, token, "false")
	})
	write(w, data, err)
}

func (s *Server) linkPins(w libob.ResponseWriter, r *libob.Request) {
	s.withAccessToken(w, r, func(
		ctx context.Context,
		client *life.Client,
		token string,
	) (map[string]any, error) {
		return client.LinkPins(ctx, token)
	})
}

func (s *Server) setLinkPin(w libob.ResponseWriter, r *libob.Request) {
	params := libob.NewParamGetter(w, r)
	slug, ok := params.GetString("slug")
	if !ok {
		return
	}
	action, ok := params.GetString("action")
	if !ok {
		return
	}
	if action != "pin" && action != "unpin" {
		w.WriteFailed(
			libob.RetCodeBadParam,
			fmt.Errorf("action must be pin or unpin"),
		)
		return
	}
	s.withAccessToken(w, r, func(
		ctx context.Context,
		client *life.Client,
		token string,
	) (map[string]any, error) {
		return client.SetLinkPin(ctx, token, slug, action)
	})
}

func (s *Server) withAccessToken(
	w libob.ResponseWriter,
	r *libob.Request,
	fetch func(context.Context, *life.Client, string) (map[string]any, error),
) {
	lifeClient, ok := s.lifeClientForAction(w)
	if !ok {
		return
	}
	ctx, cancel := ContextWithTimeout()
	defer cancel()
	ident, token, ok := s.accessTokenForAction(ctx, w, r)
	if !ok {
		return
	}
	data, err := auth.WithRefresh(
		ctx,
		s.auth,
		ident,
		token,
		func(token string) (map[string]any, error) {
			return fetch(ctx, lifeClient, token)
		},
	)
	write(w, data, err)
}

func (s *Server) lifeClientForAction(w libob.ResponseWriter) (*life.Client, bool) {
	if s.life == nil {
		w.WriteFailed(libob.RetCodeUnsupportedAction, fmt.Errorf("Life @ USTC API is not configured"))
		return nil, false
	}
	return s.life, true
}

func (s *Server) accessTokenForAction(ctx context.Context, w libob.ResponseWriter, r *libob.Request) (store.Identity, string, bool) {
	if s.auth == nil {
		w.WriteFailed(libob.RetCodeUnsupportedAction, fmt.Errorf("login is not configured"))
		return store.Identity{}, "", false
	}
	ident, ok := identityFromParams(w, r)
	if !ok {
		return store.Identity{}, "", false
	}
	token, err := s.auth.AccessToken(ctx, ident)
	if err != nil {
		w.WriteFailed(libob.RetCodeBadParam, err)
		return store.Identity{}, "", false
	}
	return ident, token, true
}

func identityFromParams(w libob.ResponseWriter, r *libob.Request) (store.Identity, bool) {
	p := libob.NewParamGetter(w, r)
	userID, ok := p.GetString("user_id")
	if !ok {
		return store.Identity{}, false
	}
	platform := "onebot"
	value, ok := optionalStringParam(w, r, "platform", platform)
	if !ok {
		return store.Identity{}, false
	}
	platform = value
	conversationType := "private"
	value, ok = optionalStringParam(w, r, "conversation_type", conversationType)
	if !ok {
		return store.Identity{}, false
	}
	conversationType = value
	conversationID := userID
	value, ok = optionalStringParam(w, r, "conversation_id", conversationID)
	if !ok {
		return store.Identity{}, false
	}
	conversationID = value
	return store.Identity{
		Platform:         strings.TrimSpace(platform),
		UserID:           strings.TrimSpace(userID),
		ConversationType: strings.TrimSpace(conversationType),
		ConversationID:   strings.TrimSpace(conversationID),
	}, true
}

func optionalStringParam(w libob.ResponseWriter, r *libob.Request, key, defaultValue string) (string, bool) {
	if _, ok := r.Params.Value()[key]; !ok {
		return defaultValue, true
	}
	value, err := r.Params.GetString(key)
	if err != nil {
		w.WriteFailed(libob.RetCodeBadParam, fmt.Errorf("参数错误: %v", err))
		return "", false
	}
	return value, true
}

func write(w libob.ResponseWriter, data any, err error) {
	if err == nil {
		w.WriteData(data)
		return
	}
	retCode := errorRetCode(err)
	if retCode == libob.RetCodeNetworkError {
		w.WriteFailed(retCode, err)
		return
	}
	if isLifeAPIError(err) {
		w.WriteFailed(retCode, fmt.Errorf("Life @ USTC API error: %w", err))
		return
	}
	w.WriteFailed(retCode, fmt.Errorf("Life @ USTC action error: %w", err))
}

func isLifeAPIError(err error) bool {
	var httpErr life.HTTPError
	return errors.As(err, &httpErr)
}

func errorRetCode(err error) int {
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return libob.RetCodeNetworkError
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
		return libob.RetCodeNetworkError
	}
	return libob.RetCodeInternalHandlerError
}

func ContextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

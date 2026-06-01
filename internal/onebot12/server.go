package onebot12

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	libob "github.com/botuniverse/go-libonebot"

	"github.com/Life-USTC/Bot/internal/life"
)

const actionPrefix = "life_ustc"

type Server struct {
	onebot *libob.OneBot
	life   *life.Client
}

type Config struct {
	Host        string
	Port        uint16
	AccessToken string
	SelfID      string
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
		err := s.life.Health(context.Background())
		w.WriteData(map[string]any{"good": err == nil, "online": err == nil})
	})
	mux.HandleFunc(actionPrefix+".get_current_semester", s.currentSemester)
	mux.HandleFunc(actionPrefix+".search_courses", s.searchCourses)
	mux.HandleFunc(actionPrefix+".search_sections", s.searchSections)
	mux.HandleFunc(actionPrefix+".get_bus", s.bus)
	return mux
}

func (s *Server) currentSemester(w libob.ResponseWriter, r *libob.Request) {
	data, err := s.life.CurrentSemester(context.Background())
	write(w, data, err)
}

func (s *Server) searchCourses(w libob.ResponseWriter, r *libob.Request) {
	p := libob.NewParamGetter(w, r)
	search, ok := p.GetString("search")
	if !ok {
		return
	}
	data, err := s.life.SearchCourses(context.Background(), search, 5)
	write(w, data, err)
}

func (s *Server) searchSections(w libob.ResponseWriter, r *libob.Request) {
	p := libob.NewParamGetter(w, r)
	search, ok := p.GetString("search")
	if !ok {
		return
	}
	data, err := s.life.SearchSections(context.Background(), search, 5)
	write(w, data, err)
}

func (s *Server) bus(w libob.ResponseWriter, r *libob.Request) {
	data, err := s.life.Bus(context.Background())
	write(w, data, err)
}

func write(w libob.ResponseWriter, data any, err error) {
	if err == nil {
		w.WriteData(data)
		return
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		w.WriteFailed(libob.RetCodeNetworkError, err)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
		w.WriteFailed(libob.RetCodeNetworkError, err)
		return
	}
	w.WriteFailed(libob.RetCodeInternalHandlerError, fmt.Errorf("Life @ USTC API error: %w", err))
}

func ContextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.seankhliao.com/usvc"
)

const (
	redirectURL = "https://seankhliao.com/"
)

type event struct {
	Time     time.Time
	Remote   string
	Trigger  string
	Src, Dst string
	Dur      string
}

func main() {
	s := NewServer(os.Args)
	s.svc.Log.Error().Err(usvc.Run(usvc.SignalContext(), s)).Msg("exited")
}

type Server struct {
	// metrics
	trigger *prometheus.CounterVec

	// server
	svc *usvc.ServerSimple
}

func NewServer(args []string) *Server {
	fs := flag.NewFlagSet(args[0], flag.ExitOnError)
	s := &Server{
		trigger: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "statslogger_trigger_count",
			Help: "trigger of a event record",
		},
			[]string{"trigger"},
		),
		svc: usvc.NewServerSimple(usvc.NewConfig(fs)),
	}

	s.svc.Mux.HandleFunc("/form", s.form)
	s.svc.Mux.HandleFunc("/json", s.json)
	s.svc.Mux.Handle("/metrics", promhttp.Handler())
	s.svc.Mux.Handle("/", http.RedirectHandler(redirectURL, http.StatusFound))

	fs.Parse(args[1:])

	s.svc.Log.Info().Msg("configured")
	return s
}

func (s *Server) json(w http.ResponseWriter, r *http.Request) {
	// filter methods
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodPost:
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	defer r.Body.Close()
	h := r.URL.Path
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		s.svc.Log.Error().Err(err).Str("handler", h).Msg("read body")
		return
	}
	if !json.Valid(b) {
		s.svc.Log.Error().Str("handler", h).Err(errors.New("invalid json")).Msg("validate body")
		return
	}

	remote := r.Header.Get("x-forwarded-for")
	if remote == "" {
		remote = r.RemoteAddr
	}

	s.svc.Log.Debug().Str("handler", h).Str("remote", remote).RawJSON("data", b).Msg("received")
	s.trigger.WithLabelValues(h).Inc()
}

// ServeHTTP handles recording of events
// /record?trigger=ping&src=...&dst=...
func (s *Server) form(w http.ResponseWriter, r *http.Request) {
	// filter methods
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodGet, http.MethodPost:
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// get data
	h := r.URL.Path
	r.ParseForm()
	b, err := json.Marshal(r.Form)
	if err != nil {
		s.svc.Log.Error().Str("handler", h).Err(err).Msg("marshal to json")
	}

	remote := r.Header.Get("x-forwarded-for")
	if remote == "" {
		remote = r.RemoteAddr
	}

	s.svc.Log.Debug().Str("handler", h).Str("remote", remote).RawJSON("data", b).Msg("received")
	s.trigger.WithLabelValues(h).Inc()
}

func (s *Server) Run() error {
	return s.svc.Run()
}

func (s *Server) Shutdown() error {
	err1 := s.svc.Shutdown()
	if err1 != nil {
		return fmt.Errorf("svc shutdown: %v", err1)
	}
	return nil
}

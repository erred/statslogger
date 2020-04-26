package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	"cloud.google.com/go/storage"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
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

	w    io.WriteCloser
	data zerolog.Logger

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

	var bucket string
	fs.StringVar(&bucket, "bucket", "statslogger-seankhliao-com", "gcs bucket")
	fs.Parse(args[1:])

	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		s.svc.Log.Fatal().Err(err).Msg("configure storage client")
	}
	obj := client.Bucket(bucket).Object(fmt.Sprintf("log.%v.json", time.Now().Format(time.RFC3339)))
	s.w = obj.NewWriter(context.Background())
	s.data = zerolog.New(s.w).With().Timestamp().Logger()

	s.svc.Log.Info().Str("bucket", obj.BucketName()).Str("obj", obj.ObjectName()).Msg("configured")
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
	s.data.Log().Str("handler", h).Str("remote", remote).RawJSON("data", b).Msg("received")
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
	s.data.Log().Str("handler", h).Str("remote", remote).RawJSON("data", b).Msg("received")
	s.trigger.WithLabelValues(h).Inc()
}

func (s *Server) Run() error {
	return s.svc.Run()
}

func (s *Server) Shutdown() error {
	err1 := s.svc.Shutdown()
	err2 := s.w.Close()
	if err1 != nil || err2 != nil {
		return fmt.Errorf("svc shutdown: %v, writer shutdown: %v", err1, err2)
	}
	return nil
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloud.google.com/go/storage"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"google.golang.org/api/option"
)

const (
	redirectURL = "https://seankhliao.com/"
)

var (
	port = func() string {
		port := os.Getenv("PORT")
		if port == "" {
			port = ":8080"
		} else if port[0] != ':' {
			port = ":" + port
		}
		return port
	}()
)

type event struct {
	Time     time.Time
	Remote   string
	Trigger  string
	Src, Dst string
	Dur      string
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		cancel()
	}()

	// server
	NewServer(ctx, os.Args).Run()
}

type Server struct {
	// metrics
	trigger *prometheus.CounterVec

	// server
	ctx context.Context

	w    *storage.Writer
	data zerolog.Logger

	log zerolog.Logger

	mux *http.ServeMux
	srv *http.Server
}

func NewServer(ctx context.Context, args []string) *Server {
	s := &Server{
		trigger: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "statslogger_trigger_count",
			Help: "trigger of a event record",
		},
			[]string{"trigger"},
		),
		ctx: ctx,
		// w set below
		// data set below
		log: zerolog.New(zerolog.ConsoleWriter{
			Out: os.Stdout, NoColor: true, TimeFormat: time.RFC3339,
		}).With().Timestamp().Logger(),
		mux: http.NewServeMux(),
		srv: &http.Server{
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      5 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}

	s.mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	s.mux.HandleFunc("/form", s.form)
	s.mux.HandleFunc("/json", s.json)
	s.mux.Handle("/metrics", promhttp.Handler())
	s.mux.Handle("/", http.RedirectHandler(redirectURL, http.StatusFound))

	s.srv.Handler = s.mux
	s.srv.ErrorLog = log.New(s.log, "", 0)

	var bucket, cred string
	fs := flag.NewFlagSet(args[0], flag.ExitOnError)
	fs.StringVar(&s.srv.Addr, "addr", port, "host:port to serve on")
	fs.StringVar(&bucket, "bucket", "statslogger-seankhliao-com", "gcs bucket")
	fs.StringVar(&cred, "cred", "/var/secrets/google/sa.json", "service account json file path")
	fs.Parse(args[1:])

	client, err := storage.NewClient(ctx, option.WithCredentialsFile(cred))
	if err != nil {
		s.log.Fatal().Err(err).Str("cred", cred).Msg("configure storage client")
	}
	bkt := client.Bucket(bucket)
	// attr, err := bkt.Attrs(ctx)
	// s.log.Trace().Interface("attr", attr).Err(err).Msg("bucket attrs")
	o := bkt.Object(fmt.Sprintf("log.%v.json", time.Now().Format(time.RFC3339)))
	s.w = o.NewWriter(ctx)
	// n, err := s.w.Write([]byte(`{"hello":"world"}` + "\n"))
	// s.log.Trace().Int("n", n).Err(err).Msg("write")

	s.data = zerolog.New(s.w).With().Timestamp().Logger()

	s.log.Info().Str("addr", s.srv.Addr).Str("obj", o.ObjectName()).Msg("configured")
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
		s.log.Error().Err(err).Str("handler", h).Msg("read body")
		return
	}
	if !json.Valid(b) {
		s.log.Error().Str("handler", h).Err(errors.New("invalid json")).Msg("validate body")
		return
	}

	remote := r.Header.Get("x-forwarded-for")
	if remote == "" {
		remote = r.RemoteAddr
	}

	s.log.Debug().Str("handler", h).Str("remote", remote).RawJSON("data", b).Msg("received")
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
		s.log.Error().Str("handler", h).Err(err).Msg("marshal to json")
	}

	remote := r.Header.Get("x-forwarded-for")
	if remote == "" {
		remote = r.RemoteAddr
	}

	s.log.Debug().Str("handler", h).Str("remote", remote).RawJSON("data", b).Msg("received")
	s.data.Log().Str("handler", h).Str("remote", remote).RawJSON("data", b).Msg("received")
	s.trigger.WithLabelValues(h).Inc()
}

func (s *Server) Run() {
	errc := make(chan error)
	go func() {
		errc <- s.srv.ListenAndServe()
	}()

	var err error
	select {
	case err = <-errc:
	case <-s.ctx.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = s.srv.Shutdown(ctx)
	}
	s.log.Error().Err(err).Msg("exit")

	err = s.w.Close()
	if err != nil {
		s.log.Error().Err(err).Msg("close cloud writer")
	}
}

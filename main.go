package main

import (
	"context"
	"encoding/json"
	"flag"
	stdlog "log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
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
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, NoColor: true})
	slg := stdlog.New(log.Logger, "", 0)

	s := NewServer(os.Args)
	go s.saver()

	// prometheus
	promhandler := promhttp.InstrumentMetricHandler(
		prometheus.DefaultRegisterer,
		promhttp.HandlerFor(
			prometheus.DefaultGatherer,
			promhttp.HandlerOpts{ErrorLog: slg},
		),
	)

	// routes
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/health", s.healthcheck)
	mux.Handle("/metrics", promhandler)
	mux.Handle("/api", s)

	// server
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slg,
	}
	go func() {
		err := srv.ListenAndServe()
		log.Info().Err(err).Msg("serve exit")
	}()

	// shutdown
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT)
	sig := <-sigs
	log.Info().Str("signal", sig.String()).Msg("shutting down")
	srv.Shutdown(context.Background())
	s.shutdown()
}

type Server struct {
	save chan event
	done chan struct{}

	// config
	addr string
	data string

	// metrics
	trigger *prometheus.CounterVec
}

func NewServer(args []string) *Server {
	s := &Server{
		save: make(chan event),
		done: make(chan struct{}),
		trigger: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "com_seabkhliao_log_trigger",
			Help: "trigger of a event record",
		},
			[]string{"trigger"},
		),
	}

	fs := flag.NewFlagSet(args[0], flag.ExitOnError)
	fs.StringVar(&s.addr, "addr", ":80", "host:port to serve on")
	fs.StringVar(&s.data, "data", "/data/log.json", "path to save file")
	err := fs.Parse(args[1:])
	if err != nil {
		log.Fatal().Err(err).Msg("parse flags")
	}

	log.Info().Str("addr", s.addr).Str("data", s.data).Msg("configured")
	return s
}

// ServeHTTP handles recording of events
// /record?trigger=ping&src=...&dst=...
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// filter methods
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	} else if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusNoContent)

	// get data
	r.ParseForm()
	remote := r.Header.Get("x-forwarded-for")
	if remote == "" {
		remote = r.RemoteAddr
	}

	// record
	e := event{
		Time:    time.Now(),
		Remote:  remote,
		Trigger: r.Form.Get("trigger"),
		Src:     r.Form.Get("src"),
		Dst:     r.Form.Get("dst"),
		Dur:     r.Form.Get("dur"),
	}
	s.save <- e
	log.Debug().Str("trigger", e.Trigger).Str("src", e.Src).Str("dst", e.Dst).Str("remote", e.Remote).Str("dur", e.Dur).Msg("recorded")
	s.trigger.WithLabelValues(e.Trigger).Inc()
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

func (s *Server) healthcheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) saver() {
	defer close(s.done)
	f, err := os.OpenFile(s.data, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal().Str("data", s.data).Err(err).Msg("open save file")
	}
	j := json.NewEncoder(f)
	defer f.Close()
	for e := range s.save {
		err = j.Encode(e)
		if err != nil {
			log.Fatal().Interface("event", e).Err(err).Msg("encode")
		}
	}
}

func (s *Server) shutdown() {
	close(s.save)
	<-s.done
}

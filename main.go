package main

import (
	"context"
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
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
		signal.Notify(sigs, syscall.SIGINT)
		<-sigs
		cancel()
	}()

	// server
	s := NewServer(os.Args)
	go s.saver()
	s.Run(ctx)

}

type Server struct {
	save chan event
	done chan struct{}

	// config
	data string

	// metrics
	trigger *prometheus.CounterVec

	// server
	log zerolog.Logger
	mux *http.ServeMux
	srv *http.Server
}

func NewServer(args []string) *Server {
	s := &Server{
		save: make(chan event),
		done: make(chan struct{}),
		trigger: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "statslogger_trigger_count",
			Help: "trigger of a event record",
		},
			[]string{"trigger"},
		),
		log: zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, NoColor: true, TimeFormat: time.RFC3339}).With().Timestamp().Logger(),
		mux: http.NewServeMux(),
		srv: &http.Server{
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      5 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}

	s.mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	s.mux.Handle("/metrics", promhttp.Handler())
	s.mux.Handle("/api", s)
	s.mux.HandleFunc("/json", s.json)
	s.mux.Handle("/", http.RedirectHandler(redirectURL, http.StatusFound))

	s.srv.Handler = s.mux
	s.srv.ErrorLog = log.New(s.log, "", 0)

	fs := flag.NewFlagSet(args[0], flag.ExitOnError)
	fs.StringVar(&s.srv.Addr, "addr", port, "host:port to serve on")
	fs.StringVar(&s.data, "data", "/data/log.json", "path to save file")
	fs.Parse(args[1:])

	s.log.Info().Str("addr", s.srv.Addr).Str("data", s.data).Msg("configured")
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
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		s.log.Error().Err(err).Str("handler", "json").Msg("read body")
		return
	}
	if !json.Valid(b) {
		s.log.Error().Str("handler", "json").Msg("invalid json")
		return
	}
	s.log.Debug().RawJSON("data", b).Msg("got")

	s.trigger.WithLabelValues("json").Inc()
}

// ServeHTTP handles recording of events
// /record?trigger=ping&src=...&dst=...
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	s.log.Debug().Str("trigger", e.Trigger).Str("src", e.Src).Str("dst", e.Dst).Str("remote", e.Remote).Str("dur", e.Dur).Msg("recorded")
	s.trigger.WithLabelValues(e.Trigger).Inc()
}

func (s *Server) Run(ctx context.Context) {
	errc := make(chan error)
	go func() {
		errc <- s.srv.ListenAndServe()
	}()

	var err error
	select {
	case err = <-errc:
	case <-ctx.Done():
		err = s.srv.Shutdown(ctx)
		close(s.save)
		<-s.done
	}
	s.log.Error().Err(err).Msg("server exit")
}

func (s *Server) saver() {
	defer close(s.done)
	f, err := os.OpenFile(s.data, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		s.log.Fatal().Str("data", s.data).Err(err).Msg("open save file")
	}
	j := json.NewEncoder(f)
	defer f.Close()
	for e := range s.save {
		err = j.Encode(e)
		if err != nil {
			s.log.Fatal().Interface("event", e).Err(err).Msg("encode")
		}
	}
}

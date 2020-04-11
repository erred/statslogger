package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
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
	s := NewServer(os.Args[1:])
	go s.saver()

	// prometheus
	promhandler := promhttp.InstrumentMetricHandler(
		prometheus.DefaultRegisterer,
		promhttp.HandlerFor(
			prometheus.DefaultGatherer,
			promhttp.HandlerOpts{ErrorLog: s.stdlog},
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
		ErrorLog:          s.stdlog,
	}
	go func() {
		s.log.Errorw("serve exit", "err", srv.ListenAndServe())
	}()

	// shutdown
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT)
	s.log.Errorw("caught", "signal", <-sigs)
	srv.Shutdown(context.Background())
	s.shutdown()
}

type Server struct {
	log    *zap.SugaredLogger
	stdlog *log.Logger
	save   chan event
	done   chan struct{}

	// config
	addr string
	data string

	// metrics
	trigger *prometheus.CounterVec
}

func NewServer(args []string) *Server {
	loggerp, err := zap.NewDevelopment()
	if err != nil {
		log.Fatal(err)
	}
	loggers, err := zap.NewStdLogAt(loggerp, zapcore.ErrorLevel)
	if err != nil {
		log.Fatal(err)
	}

	s := &Server{
		log:    loggerp.Sugar(),
		stdlog: loggers,
		save:   make(chan event),
		done:   make(chan struct{}),
		trigger: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "com_seabkhliao_log_trigger",
			Help: "trigger of a event record",
		},
			[]string{"trigger"},
		),
	}

	fs := flag.NewFlagSet("com-seankhliao-log", flag.ExitOnError)
	fs.StringVar(&s.addr, "addr", ":8080", "host:port to serve on")
	fs.StringVar(&s.data, "data", "/data/log.json", "path to save file")
	err = fs.Parse(args)
	if err != nil {
		s.log.Fatalw("parse args", "err", err)
	}
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
	s.log.Infow("record", "trigger", e.Trigger, "src", e.Src, "dst", e.Dst, "remote", e.Remote, "dur", e.Dur)
	s.save <- e
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
		s.log.Fatalw("open save file", "data", s.data, "err", err)
	}
	j := json.NewEncoder(f)
	defer f.Close()
	for e := range s.save {
		err = j.Encode(e)
		if err != nil {
			s.log.Fatalw("encode", "event", e, "err", err)
		}
	}
}

func (s *Server) shutdown() {
	close(s.save)
	<-s.done
}

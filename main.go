package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/api/metric"
	"go.opentelemetry.io/otel/api/unit"
	"go.opentelemetry.io/otel/exporters/metric/prometheus"
	"go.opentelemetry.io/otel/label"

	"github.com/rs/zerolog"
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
	var s Server
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fs.StringVar(&s.addr, "addr", ":8080", "listen addr")
	fs.StringVar(&s.tlsCert, "tls-cert", "", "tls cert file")
	fs.StringVar(&s.tlsKey, "tls-key", "", "tls key file")
	fs.Parse(os.Args[1:])

	promExporter, _ := prometheus.InstallNewPipeline(prometheus.Config{
		DefaultHistogramBoundaries: []float64{1, 5, 10, 50, 100},
	})
	s.meter = global.Meter(os.Args[0])
	s.endpoint = metric.Must(s.meter).NewInt64Counter(
		"endpoint_hit",
		metric.WithDescription("hits per endpoint"),
	)
	s.latency = metric.Must(s.meter).NewInt64ValueRecorder(
		"serve_latency",
		metric.WithDescription("http response latency"),
		metric.WithUnit(unit.Milliseconds),
	)

	s.log = zerolog.New(os.Stdout).With().Timestamp().Logger()

	m := http.NewServeMux()
	m.HandleFunc("/form", s.form)
	m.HandleFunc("/json", s.json)
	m.Handle("/metrics", promExporter)
	m.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	m.HandleFunc("/debug/pprof/", pprof.Index)
	m.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	m.HandleFunc("/debug/pprof/profile", pprof.Profile)
	m.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	m.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           cors(m),
		ReadTimeout:       5 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		TLSConfig: &tls.Config{
			MinVersion:               tls.VersionTLS13,
			PreferServerCipherSuites: true,
		},
	}

	if s.tlsKey != "" && s.tlsCert != "" {
		cert, err := tls.LoadX509KeyPair(s.tlsCert, s.tlsKey)
		if err != nil {
			s.log.Error().Err(err).Msg("laod tls keys")
			return
		}
		srv.TLSConfig.Certificates = []tls.Certificate{cert}
	}

	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		<-c
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		go func() {
			<-c
			cancel()
		}()
		err := srv.Shutdown(ctx)
		if err != nil {
			s.log.Error().Err(err).Msg("unclean shutdown")
		}
	}()

	err := srv.ListenAndServe()
	if err != nil {
		s.log.Error().Err(err).Msg("serve")
	}
}

type Server struct {
	meter    metric.Meter
	endpoint metric.Int64Counter
	latency  metric.Int64ValueRecorder

	log zerolog.Logger

	addr    string
	tlsCert string
	tlsKey  string
}

func (s *Server) json(w http.ResponseWriter, r *http.Request) {
	// get data
	t := time.Now()
	h := r.URL.Path
	remote := r.Header.Get("x-forwarded-for")
	if remote == "" {
		remote = r.RemoteAddr
	}
	ua := r.Header.Get("user-agent")

	defer func() {
		s.latency.Record(r.Context(), time.Since(t).Milliseconds())
		s.endpoint.Add(r.Context(), 1, label.String("endpoint", "json"))

		s.log.Debug().Str("path", r.URL.Path).Str("src", remote).Str("endpoint", "json").Str("user-agent", ua).Msg("served")
	}()

	defer r.Body.Close()
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		s.log.Error().Err(err).Str("handler", h).Msg("read body")
		return
	}
	if !json.Valid(b) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		s.log.Error().Str("handler", h).Err(errors.New("invalid json")).Msg("validate body")
		return
	}

	w.WriteHeader(http.StatusOK)
	s.log.Info().RawJSON("data", b).Msg(log(b))
}

// ServeHTTP handles recording of events
// /record?trigger=ping&src=...&dst=...
func (s *Server) form(w http.ResponseWriter, r *http.Request) {
	// get data
	t := time.Now()
	h := r.URL.Path
	remote := r.Header.Get("x-forwarded-for")
	if remote == "" {
		remote = r.RemoteAddr
	}
	ua := r.Header.Get("user-agent")

	defer func() {
		s.latency.Record(r.Context(), time.Since(t).Milliseconds())
		s.endpoint.Add(r.Context(), 1, label.String("endpoint", "form"))

		s.log.Debug().Str("path", r.URL.Path).Str("src", remote).Str("endpoint", "json").Str("user-agent", ua).Msg("served")
	}()

	// get data
	r.ParseForm()
	b, err := json.Marshal(r.Form)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		s.log.Error().Str("handler", h).Err(err).Msg("marshal to json")
	}

	w.WriteHeader(http.StatusOK)
	s.log.Info().RawJSON("data", b).Msg(log(b))
}

func log(b []byte) string {
	var m map[string]interface{}
	err := json.Unmarshal(b, &m)
	if err != nil {
		return "received"
	}
	if report, ok := m["csp-report"]; ok {
		m2, ok := report.(map[string]interface{})
		if !ok {
			return "received"
		}
		return fmt.Sprintf("csp policy %v blocked %v on %v", m2["violated-directive"], m2["blocked-uri"], m2["document-uri"])
	} else if view, ok := m["trigger"]; ok {
		m2, ok := view.(map[string]interface{})
		if !ok {
			return "received"
		}
		return fmt.Sprintf("viewed %v for %v", m2["src"].([]string)[0], m2["dur"].([]string)[0])
	}
	return "received"
}

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodOptions:
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		case http.MethodGet, http.MethodPost:
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
			w.Header().Set("Access-Control-Max-Age", "86400")
			h.ServeHTTP(w, r)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
	})
}

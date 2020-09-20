package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/api/metric"
	"go.seankhliao.com/usvc"
)

func main() {
	var s Server

	srvc := usvc.DefaultConf()
	s.log = srvc.Logger()

	s.endpoint = metric.Must(global.Meter(os.Args[0])).NewInt64Counter(
		"endpoint_hit",
		metric.WithDescription("hits per endpoint"),
	)

	m := http.NewServeMux()
	m.HandleFunc("/form", s.form)
	m.HandleFunc("/json", s.json)

	err := srvc.RunHTTP(context.Background(), m)
	if err != nil {
		s.log.Fatal().Err(err).Msg("run server")
	}
}

type Server struct {
	endpoint metric.Int64Counter

	log zerolog.Logger
}

func (s *Server) json(w http.ResponseWriter, r *http.Request) {
	h := r.URL.Path

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
	h := r.URL.Path

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

type event struct {
	Time     time.Time
	Remote   string
	Trigger  string
	Src, Dst string
	Dur      string
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

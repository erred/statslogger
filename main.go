package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/api/metric"
	"go.seankhliao.com/stream"
	"go.seankhliao.com/usvc"
	"google.golang.org/grpc"
)

func main() {
	var s Server

	srvc := usvc.DefaultConf(&s)
	s.log = srvc.Logger()

	s.endpoint = metric.Must(global.Meter(os.Args[0])).NewInt64Counter(
		"endpoint_hit",
		metric.WithDescription("hits per endpoint"),
	)

	cc, err := grpc.Dial(s.streamAddr, grpc.WithInsecure())
	if err != nil {
		s.log.Error().Err(err).Msg("connect to stream")
	}
	defer cc.Close()
	s.client = stream.NewStreamClient(cc)

	m := http.NewServeMux()
	m.HandleFunc("/csp", s.csp)
	m.HandleFunc("/beacon", s.beacon)

	err = srvc.RunHTTP(context.Background(), m)
	if err != nil {
		s.log.Error().Err(err).Msg("run server")
	}
}

type Server struct {
	endpoint metric.Int64Counter

	log zerolog.Logger

	streamAddr string
	client     stream.StreamClient
}

func (s *Server) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&s.streamAddr, "stream.addr", "stream:80", "url to connect to stream")
}

type CSPReport struct {
	CspReport struct {
		OriginalPolicy     string `json:"original-policy"`
		ViolatedDirective  string `json:"violated-directive"`
		Referrer           string `json:"referrer"`
		ScriptSample       string `json:"script-sample"`
		StatusCode         int64  `json:"status-code"`
		LineNumber         int64  `json:"line-number"`
		Disposition        string `json:"disposition"`
		BlockedURI         string `json:"blocked-uri"`
		EffectiveDirective string `json:"effective-directive"`
		DocumentURI        string `json:"document-uri"`
		SourceFile         string `json:"source-file"`
	} `json:"csp-report"`
}

func (s *Server) csp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	h := r.URL.Path
	remote := r.Header.Get("x-forwarded-for")
	if remote == "" {
		remote = r.RemoteAddr
	}

	var cspReport CSPReport
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&cspReport)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		s.log.Error().Str("handler", h).Err(err).Msg("unmarshal csp report")
		return
	}

	cspRequest := &stream.CSPRequest{
		Timestamp:          time.Now().Format(time.RFC3339),
		Remote:             remote,
		UserAgent:          r.UserAgent(),
		Referrer:           r.Referer(),
		Enforce:            cspReport.CspReport.Disposition,
		BlockedUri:         cspReport.CspReport.BlockedURI,
		SourceFile:         cspReport.CspReport.SourceFile,
		DocumentUri:        cspReport.CspReport.DocumentURI,
		ViolatedDirective:  cspReport.CspReport.ViolatedDirective,
		EffectiveDirective: cspReport.CspReport.EffectiveDirective,
		StatusCode:         cspReport.CspReport.StatusCode,
		LineNumber:         cspReport.CspReport.LineNumber,
	}

	_, err = s.client.LogCSP(ctx, cspRequest)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		s.log.Error().Str("handler", h).Err(err).Msg("write to stream")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) beacon(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	h := r.URL.Path
	remote := r.Header.Get("x-forwarded-for")
	if remote == "" {
		remote = r.RemoteAddr
	}

	// get data
	r.ParseForm()
	dur, err := strconv.ParseInt(strings.TrimSuffix(r.FormValue("dur"), "ms"), 10, 64)
	if err != nil {
		s.log.Warn().Str("handler", h).Err(err).Msg("parse duration")
	}
	beaconRequest := &stream.BeaconRequest{
		DurationMs: dur,
		SrcPage:    r.FormValue("src"),
		DstPage:    r.FormValue("dst"),
		Remote:     remote,
		UserAgent:  r.UserAgent(),
		Referrer:   r.FormValue("referrer"),
	}

	_, err = s.client.LogBeacon(ctx, beaconRequest)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		s.log.Error().Str("handler", h).Err(err).Msg("write to stream")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/api/metric"
	"go.seankhliao.com/apis/saver/v1"
	"go.seankhliao.com/usvc"
	"google.golang.org/grpc"
)

const (
	name = "statslogger"
)

func main() {
	usvc.Run(context.Background(), name, &Server{}, false)
}

type Server struct {
	endpoint metric.Int64Counter

	log zerolog.Logger

	saverAddr string
	client    saver.SaverClient
	cc        *grpc.ClientConn
}

func (s *Server) Flag(fs *flag.FlagSet) {
	fs.StringVar(&s.saverAddr, "saver.addr", "saver:443", "url to connect to stream")
}

func (s *Server) Register(c *usvc.Components) error {
	s.log = c.Log

	s.endpoint = metric.Must(global.Meter(os.Args[0])).NewInt64Counter(
		"endpoint_hit",
		metric.WithDescription("hits per endpoint"),
	)

	c.HTTP.HandleFunc("/csp", s.csp)
	c.HTTP.HandleFunc("/beacon", s.beacon)

	var err error
	s.cc, err = grpc.Dial(s.saverAddr, grpc.WithInsecure())
	if err != nil {
		return fmt.Errorf("connect to stream: %w", err)
	}
	s.client = saver.NewSaverClient(s.cc)
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.cc.Close()
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

	cspRequest := &saver.CSPRequest{
		HttpRemote: &saver.HTTPRemote{
			Timestamp: time.Now().Format(time.RFC3339),
			Remote:    remote,
			UserAgent: r.UserAgent(),
			Referrer:  r.Referer(),
		},
		Disposition:        cspReport.CspReport.Disposition,
		BlockedUri:         cspReport.CspReport.BlockedURI,
		SourceFile:         cspReport.CspReport.SourceFile,
		DocumentUri:        cspReport.CspReport.DocumentURI,
		ViolatedDirective:  cspReport.CspReport.ViolatedDirective,
		EffectiveDirective: cspReport.CspReport.EffectiveDirective,
		StatusCode:         cspReport.CspReport.StatusCode,
		LineNumber:         cspReport.CspReport.LineNumber,
	}

	_, err = s.client.CSP(ctx, cspRequest)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		s.log.Error().Str("handler", h).Err(err).Msg("write to saver")
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
	beaconRequest := &saver.BeaconRequest{
		HttpRemote: &saver.HTTPRemote{
			Timestamp: time.Now().Format(time.RFC3339),
			Remote:    remote,
			UserAgent: r.UserAgent(),
			Referrer:  r.Referer(),
		},
		DurationMs: dur,
		SrcPage:    r.FormValue("src"),
		DstPage:    r.FormValue("dst"),
	}

	_, err = s.client.Beacon(ctx, beaconRequest)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		s.log.Error().Str("handler", h).Err(err).Msg("write to saver")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

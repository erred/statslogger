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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/api/trace"
	"go.seankhliao.com/apis/saver/v1"
	"go.seankhliao.com/usvc"
	"google.golang.org/grpc"
)

const (
	name = "go.seankhliao.com/statslogger"
)

func main() {
	os.Exit(usvc.Exec(context.Background(), &Server{}, os.Args))
}

type Server struct {
	saverAddr string
	client    saver.SaverClient
	cc        *grpc.ClientConn

	log    zerolog.Logger
	tracer trace.Tracer

	cspc    prometheus.Counter
	beaconc prometheus.Counter
}

func (s *Server) Flags(fs *flag.FlagSet) {
	fs.StringVar(&s.saverAddr, "saver", "saver:443", "url to connect to stream")
}

func (s *Server) Setup(ctx context.Context, u *usvc.USVC) error {
	s.log = u.Logger
	s.tracer = global.Tracer(name)

	s.cspc = promauto.NewCounter(prometheus.CounterOpts{
		Name: "statslogger_csp_requests",
	})
	s.beaconc = promauto.NewCounter(prometheus.CounterOpts{
		Name: "statslogger_beacon_requests",
	})

	u.ServiceMux.HandleFunc("/csp", s.csp)
	u.ServiceMux.HandleFunc("/beacon", s.beacon)

	var err error
	s.cc, err = grpc.Dial(s.saverAddr, grpc.WithInsecure())
	if err != nil {
		return fmt.Errorf("connect to stream: %w", err)
	}
	s.client = saver.NewSaverClient(s.cc)

	go func() {
		<-ctx.Done()
		s.cc.Close()
	}()

	return nil
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
	ctx, span := s.tracer.Start(r.Context(), "csp")
	defer span.End()

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
	ctx, span := s.tracer.Start(r.Context(), "beacon")
	defer span.End()

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

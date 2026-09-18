// Command example drives the deploy/docker-compose.yml stack.
//
// It wires all three optional modules onto one client and makes a call every
// few seconds, so the Grafana panels and the Jaeger trace view have something
// real in them. A dashboard of empty panels tells you nothing about whether
// the wiring works.
//
// Deliberately small, and deliberately complete: this is the only place in the
// repository where tracing, metrics and caching are shown composed, and the
// order they are composed in matters.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/typesafecache"
	"github.com/nibir1/typesafe-go/typesafeotel"
	"github.com/nibir1/typesafe-go/typesafeprom"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := initTracing(ctx)
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}
	defer func() {
		// A fresh context: the one above is already cancelled by the time
		// this runs, and an exporter given a dead context drops the spans it
		// was about to flush.
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(flushCtx); err != nil {
			log.Printf("tracing shutdown: %v", err)
		}
	}()

	metrics := typesafeprom.New(typesafeprom.WithConstLabels(prometheus.Labels{
		"service": "typesafe-example",
	}))
	reg := prometheus.NewRegistry()
	reg.MustRegister(metrics)

	cache, err := typesafecache.New(
		typesafecache.WithTTL(2*time.Minute),
		typesafecache.WithMaxEntries(512),
		typesafecache.WithObserver(cacheObserver(metrics)),
	)
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	defer cache.Close()

	tracer := typesafeotel.New()

	client, err := typesafe.NewClient(
		// Order matters. Tracing is outermost so its span is the parent of
		// everything; metrics next, so a cached call still shows up in the
		// latency histogram as the fast call it was; the cache innermost, so
		// a hit is visible to both rather than invisible to both.
		typesafe.WithInterceptor(
			tracer.Interceptor(),
			metrics.Interceptor(),
			cache.Interceptor(),
		),
		typesafe.WithRetryObserver(func(ctx context.Context, info typesafe.AttemptInfo) {
			tracer.RetryObserver()(ctx, info)
			metrics.RetryObserver()(ctx, info)
		}),
	)
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}

	addr := envOr("METRICS_ADDR", ":2112")
	srv := &http.Server{
		Addr:              addr,
		Handler:           promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("metrics on %s/metrics", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("metrics server: %v", err)
		}
	}()
	defer srv.Close()

	log.Print("generating traffic; ctrl-c to stop")
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			ask(ctx, client)
		}
	}
}

// tickets are the sample states. A small pool on purpose: repeats are what
// give the cache something to hit, which is what makes the cache panel move.
var tickets = []string{
	"My payouts have been failing for three days and nobody has replied.",
	"How do I add a second seat to my plan?",
	"The dashboard returns a 500 on every page load since this morning.",
	"Cancel my subscription please.",
	"Charged twice for January. Refund?",
}

func ask(ctx context.Context, client *typesafe.Client) {
	ctx, span := otel.Tracer("example").Start(ctx, "handle-ticket")
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req := &typesafe.SystemOneRequest{
		State: tickets[rand.Intn(len(tickets))],
		Questions: typesafe.Questions{
			"is_urgent": typesafe.Noul{
				Instructions: "Does this message convey urgency?",
				Criteria: &typesafe.NoulCriteria{
					True:  "The sender needs a response today",
					False: "The sender can wait",
				},
			},
			"team": typesafe.Choice{
				Instructions: "Which team should handle this?",
				Criteria: typesafe.Options{
					"billing":   "Payments, invoicing, refunds",
					"technical": "Bugs, outages, integrations",
					"sales":     "Pricing, upgrades, new accounts",
				},
			},
			"severity": typesafe.Score{
				Instructions: "How severe is the problem described here?",
				Criteria: typesafe.Levels{
					"No impact", "Minor annoyance", "Blocking one workflow", "Product unusable",
				},
			},
		},
	}

	resp, err := client.SystemOne(ctx, req)
	if err != nil {
		log.Printf("call failed: %v", err)
		return
	}
	team, _ := resp.Choice("team")
	log.Printf("model=%s team=%s confidence=%.2f tokens=%d",
		resp.Model, team.Choice, team.Confidence, resp.Usage.InputTokens)
}

// cacheObserver adapts a typesafecache.Event to the shape typesafeprom wants.
//
// typesafeprom takes the four values rather than the Event type so that a
// build with metrics but no cache does not have to pull the cache module in.
func cacheObserver(m *typesafeprom.Metrics) func(typesafecache.Event) {
	record := m.CacheObserver()
	return func(e typesafecache.Event) {
		record(e.Hit, e.Tier, e.AliasMoved, e.Err)
	}
}

func initTracing(ctx context.Context) (func(context.Context) error, error) {
	// OTEL_EXPORTER_OTLP_ENDPOINT is read by the exporter itself.
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("typesafe-example"),
	))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

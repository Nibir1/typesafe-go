// Package typesafeprom exposes TypeSafe call metrics to Prometheus.
//
//	m := typesafeprom.New()
//	prometheus.MustRegister(m)
//
//	client, err := typesafe.NewClient(
//	    typesafe.WithInterceptor(m.Interceptor()),
//	    typesafe.WithRetryObserver(m.RetryObserver()),
//	)
//
// # What to build a dashboard on
//
// Latency and error rate are the obvious ones, and they are here. The one
// worth putting next to them is **confidence**, which has no equivalent in an
// ordinary API client: a drift in the distribution of answer confidence is the
// earliest visible sign that the inputs have changed shape. It moves well
// before anyone notices the answers are wrong, and long before it shows up in
// latency or errors.
//
// Watch the p10 of typesafe_answer_confidence. A fall there means the model is
// increasingly unsure, which usually means the state being sent stopped
// looking like what the questions were written for.
//
// # A note on token metrics
//
// Only input tokens are billed. typesafe_tokens_total is labelled by kind so a
// cost panel can sum the input series alone; summing both would overstate
// spend by whatever fraction the output happens to be.
package typesafeprom

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/prometheus/client_golang/prometheus"
)

// Namespace prefixes every metric.
const Namespace = "typesafe"

// DefaultDurationBuckets covers the documented end-to-end latency range.
//
// TypeSafe documents 70–500ms for a typical System One call, so the buckets
// are dense across that span and then coarse: the interesting question in
// production is "did the body of the distribution move", and a uniform
// exponential ladder would answer it with two buckets. The long tail is kept
// because a retried call lands there and it should be visible, not clipped.
var DefaultDurationBuckets = []float64{
	0.05, 0.07, 0.1, 0.15, 0.2, 0.3, 0.4, 0.5, 0.75, 1, 2, 5, 10,
}

// DefaultConfidenceBuckets are ten even steps across [0,1].
//
// Confidence is a probability, so an exponential ladder would put almost every
// observation in one bucket. Even steps make the quantiles mean what a reader
// expects.
var DefaultConfidenceBuckets = prometheus.LinearBuckets(0, 0.1, 11)

// Option configures Metrics.
type Option func(*Metrics)

// WithDurationBuckets overrides the latency buckets.
func WithDurationBuckets(b []float64) Option {
	return func(m *Metrics) {
		if len(b) > 0 {
			m.durationBuckets = b
		}
	}
}

// WithConfidenceBuckets overrides the confidence buckets.
func WithConfidenceBuckets(b []float64) Option {
	return func(m *Metrics) {
		if len(b) > 0 {
			m.confidenceBuckets = b
		}
	}
}

// WithQuestionIDLabel controls the question_id label on the confidence
// histogram. On by default.
//
// Question ids are chosen in your code and are effectively a fixed set, so the
// cardinality is bounded by how many distinct questions you ask — typically a
// couple of dozen, which Prometheus handles without noticing. Turn it off if
// you build ids dynamically, because an unbounded label is how a Prometheus
// server runs out of memory.
func WithQuestionIDLabel(on bool) Option {
	return func(m *Metrics) { m.questionIDLabel = on }
}

// WithConstLabels adds labels to every metric — a deployment or region, say.
func WithConstLabels(l prometheus.Labels) Option {
	return func(m *Metrics) { m.constLabels = l }
}

// Metrics is the collector set. It implements prometheus.Collector, so it is
// registered as one unit rather than a metric at a time.
type Metrics struct {
	durationBuckets   []float64
	confidenceBuckets []float64
	constLabels       prometheus.Labels
	questionIDLabel   bool

	duration    *prometheus.HistogramVec
	confidence  *prometheus.HistogramVec
	retries     *prometheus.CounterVec
	errs        *prometheus.CounterVec
	questions   *prometheus.CounterVec
	tokens      *prometheus.CounterVec
	batchItems  *prometheus.CounterVec
	inFlight    prometheus.Gauge
	cacheEvents *prometheus.CounterVec
}

// New builds the collector set.
func New(opts ...Option) *Metrics {
	m := &Metrics{
		durationBuckets:   DefaultDurationBuckets,
		confidenceBuckets: DefaultConfidenceBuckets,
		questionIDLabel:   true,
	}
	for _, o := range opts {
		o(m)
	}

	m.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace:   Namespace,
		Name:        "request_duration_seconds",
		Help:        "End-to-end duration of one logical System One call, retries included.",
		Buckets:     m.durationBuckets,
		ConstLabels: m.constLabels,
	}, []string{"model", "outcome"})

	m.confidence = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: Namespace,
		Name:      "answer_confidence",
		Help: "Confidence of each answer, by question type. A Noul has no confidence — " +
			"its probability is the uncertainty — so Nouls are recorded as type \"noul\" " +
			"with the probability itself.",
		Buckets:     m.confidenceBuckets,
		ConstLabels: m.constLabels,
	}, []string{"question_type", "question_id"})

	m.retries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   Namespace,
		Name:        "retries_total",
		Help:        "Retried attempts, by the HTTP status that caused the retry.",
		ConstLabels: m.constLabels,
	}, []string{"status"})

	m.errs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   Namespace,
		Name:        "errors_total",
		Help:        "Failed calls, by error class.",
		ConstLabels: m.constLabels,
	}, []string{"class"})

	m.questions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   Namespace,
		Name:        "questions_total",
		Help:        "Questions asked, by primitive type.",
		ConstLabels: m.constLabels,
	}, []string{"type"})

	m.tokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "tokens_total",
		Help: "Tokens reported by the API, by kind. Only input tokens are billed; " +
			"sum the input series alone for a cost panel.",
		ConstLabels: m.constLabels,
	}, []string{"kind", "model"})

	m.batchItems = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   Namespace,
		Name:        "batch_items_total",
		Help:        "Items completed in a batch, by outcome.",
		ConstLabels: m.constLabels,
	}, []string{"outcome"})

	m.inFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   Namespace,
		Name:        "requests_in_flight",
		Help:        "Logical calls currently in progress.",
		ConstLabels: m.constLabels,
	})

	m.cacheEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "cache_events_total",
		Help: "Response-cache lookups, by result. The alias_moved result means a model " +
			"alias started resolving elsewhere, which invalidated every entry under the " +
			"previous model id.",
		ConstLabels: m.constLabels,
	}, []string{"result"})

	return m
}

// Describe implements prometheus.Collector.
func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	m.duration.Describe(ch)
	m.confidence.Describe(ch)
	m.retries.Describe(ch)
	m.errs.Describe(ch)
	m.questions.Describe(ch)
	m.tokens.Describe(ch)
	m.batchItems.Describe(ch)
	m.inFlight.Describe(ch)
	m.cacheEvents.Describe(ch)
}

// Collect implements prometheus.Collector.
func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	m.duration.Collect(ch)
	m.confidence.Collect(ch)
	m.retries.Collect(ch)
	m.errs.Collect(ch)
	m.questions.Collect(ch)
	m.tokens.Collect(ch)
	m.batchItems.Collect(ch)
	m.inFlight.Collect(ch)
	m.cacheEvents.Collect(ch)
}

// Interceptor returns the interceptor that records per-call metrics.
func (m *Metrics) Interceptor() typesafe.Interceptor {
	return func(next typesafe.Handler) typesafe.Handler {
		return func(ctx context.Context, req *typesafe.SystemOneRequest) (*typesafe.SystemOneResponse, error) {
			m.inFlight.Inc()
			defer m.inFlight.Dec()

			if req != nil {
				for _, t := range questionTypeCounts(req.Questions) {
					m.questions.WithLabelValues(t.typ).Add(float64(t.n))
				}
			}

			start := time.Now()
			resp, err := next(ctx, req)
			elapsed := time.Since(start).Seconds()

			// The model label comes from the *response*, because that is the
			// model that actually answered. Labelling by the requested alias
			// would put two model versions in one series across an alias move
			// and hide the step change that caused.
			model := "unknown"
			outcome := "error"
			if err == nil && resp != nil {
				model = resp.Model
				outcome = "ok"
			}
			m.duration.WithLabelValues(model, outcome).Observe(elapsed)

			if err != nil {
				m.errs.WithLabelValues(ErrorClass(err)).Inc()
				return resp, err
			}
			if resp != nil {
				m.tokens.WithLabelValues("input", model).Add(float64(resp.Usage.InputTokens))
				m.tokens.WithLabelValues("output", model).Add(float64(resp.Usage.OutputTokens))
				m.observeConfidence(resp)
			}
			return resp, err
		}
	}
}

// RetryObserver returns the observer that counts retries.
//
//	typesafe.WithRetryObserver(m.RetryObserver())
func (m *Metrics) RetryObserver() func(context.Context, typesafe.AttemptInfo) {
	return func(_ context.Context, info typesafe.AttemptInfo) {
		m.retries.WithLabelValues(statusLabel(info.Status)).Inc()
	}
}

// BatchCallback returns the per-item callback for a batch.
//
//	client.SystemOneBatch(ctx, states, qs, typesafe.WithItemCallback(m.BatchCallback()))
//
// Batch items do not pass through the interceptor chain as a batch — each is
// an ordinary call — so this records the batch-level outcome that the
// per-call metrics cannot express.
func (m *Metrics) BatchCallback() func(typesafe.ItemResult) {
	return func(item typesafe.ItemResult) {
		if item.Err != nil {
			m.batchItems.WithLabelValues("error").Inc()
			return
		}
		m.batchItems.WithLabelValues("ok").Inc()
	}
}

// CacheObserver returns an observer for typesafecache.
//
//	typesafecache.WithObserver(m.CacheObserver())
//
// Typed as the structural shape rather than importing typesafecache, so
// wiring metrics does not drag the cache module into a build that has no
// cache in it.
func (m *Metrics) CacheObserver() func(hit bool, tier string, aliasMoved bool, err error) {
	return func(hit bool, tier string, aliasMoved bool, err error) {
		switch {
		case aliasMoved:
			m.cacheEvents.WithLabelValues("alias_moved").Inc()
		case err != nil:
			m.cacheEvents.WithLabelValues("error").Inc()
		case hit && tier != "":
			m.cacheEvents.WithLabelValues("hit_" + tier).Inc()
		case hit:
			m.cacheEvents.WithLabelValues("hit").Inc()
		default:
			m.cacheEvents.WithLabelValues("miss").Inc()
		}
	}
}

// observeConfidence records one observation per answer.
//
// A Noul has no confidence field — its probability *is* its uncertainty — so
// the probability is recorded under the "noul" type rather than skipping the
// answer or inventing a confidence for it.
func (m *Metrics) observeConfidence(resp *typesafe.SystemOneResponse) {
	ids := make([]string, 0, len(resp.Answers))
	for id := range resp.Answers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		a, err := resp.Answer(id)
		if err != nil {
			continue
		}
		switch v := a.(type) {
		case typesafe.NoulAnswer:
			m.confidence.WithLabelValues(typesafe.TypeNoul, m.idLabel(id)).Observe(v.Noul)
		case typesafe.ChoiceAnswer:
			m.confidence.WithLabelValues(typesafe.TypeChoice, m.idLabel(id)).Observe(v.Confidence)
		case typesafe.ScoreAnswer:
			m.confidence.WithLabelValues(typesafe.TypeScore, m.idLabel(id)).Observe(v.Confidence)
		}
	}
}

// idLabel is the question id, or "" when the label is turned off.
func (m *Metrics) idLabel(id string) string {
	if m.questionIDLabel {
		return id
	}
	return ""
}

// typeFromJSON reads the wire discriminator out of a marshalled question.
func typeFromJSON(b []byte) string {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &probe); err != nil || probe.Type == "" {
		return "unknown"
	}
	return probe.Type
}

// ErrorClass reduces an error to a bounded label.
//
// Bounded is the point: a label built from err.Error() would carry request
// ids and server messages straight into the label set and produce a new time
// series per failure, which is how a Prometheus server runs out of memory.
func ErrorClass(err error) string {
	if err == nil {
		return "none"
	}
	switch {
	case errors.Is(err, typesafe.ErrRateLimit):
		return "rate_limit"
	case errors.Is(err, typesafe.ErrOverloaded):
		return "overloaded"
	case errors.Is(err, typesafe.ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, typesafe.ErrBudgetExceeded):
		return "budget_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}

	var authErr *typesafe.AuthenticationError
	var permErr *typesafe.PermissionDeniedError
	var notFound *typesafe.NotFoundError
	var serverErr *typesafe.InternalServerError
	var connErr *typesafe.ConnectionError
	var timeoutErr *typesafe.TimeoutError
	var respErr *typesafe.ResponseValidationError
	var panicErr *typesafe.PanicError

	switch {
	case errors.As(err, &authErr):
		return "authentication"
	case errors.As(err, &permErr):
		return "permission_denied"
	case errors.As(err, &notFound):
		return "not_found"
	case errors.As(err, &serverErr):
		return "server_error"
	case errors.As(err, &connErr):
		return "connection"
	case errors.As(err, &timeoutErr):
		return "timeout"
	case errors.As(err, &respErr):
		return "response_validation"
	case errors.As(err, &panicErr):
		return "panic"
	}
	return "other"
}

func statusLabel(status int) string {
	switch {
	case status == 0:
		return "none"
	case status == 408:
		return "408"
	case status == 429:
		return "429"
	case status >= 500 && status < 600:
		return "5xx"
	case status >= 400 && status < 500:
		return "4xx"
	default:
		return "other"
	}
}

type typeCount struct {
	typ string
	n   int
}

// questionTypeCounts counts questions per primitive, in a fixed order.
func questionTypeCounts(qs map[string]typesafe.Question) []typeCount {
	counts := map[string]int{}
	for _, q := range qs {
		counts[questionType(q)]++
	}
	types := make([]string, 0, len(counts))
	for t := range counts {
		types = append(types, t)
	}
	sort.Strings(types)

	out := make([]typeCount, 0, len(types))
	for _, t := range types {
		out = append(out, typeCount{t, counts[t]})
	}
	return out
}

func questionType(q typesafe.Question) string {
	switch q.(type) {
	case typesafe.Noul, typesafe.NoulBuilder:
		return typesafe.TypeNoul
	case typesafe.Choice, typesafe.ChoiceBuilder:
		return typesafe.TypeChoice
	case typesafe.Score, typesafe.ScoreBuilder:
		return typesafe.TypeScore
	case typesafe.RawQuestion:
		return "raw"
	default:
		// The typed wrappers embed the plain question, so they match none of
		// the cases above. Ask the wire form rather than enumerating every
		// wrapper here and going stale when one is added.
		if m, ok := q.(interface{ MarshalJSON() ([]byte, error) }); ok {
			if b, err := m.MarshalJSON(); err == nil {
				return typeFromJSON(b)
			}
		}
		return "unknown"
	}
}

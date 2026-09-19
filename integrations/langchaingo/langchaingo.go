// Package langchaingo exposes TypeSafe as a LangChainGo tool and chain.
//
//	classifier := tslangchain.NewClassifier(client, typesafe.Questions{
//	    "is_spam": typesafe.Noul{Instructions: "Is this spam?"},
//	    "team":    typesafe.Choice{...},
//	},
//	    tslangchain.WithName("triage"),
//	    tslangchain.WithDescription("Classify a support ticket. Input: the ticket text."),
//	)
//
//	agent := agents.NewOneShotAgent(llm, []tools.Tool{classifier})
//
// # Why put TypeSafe behind an agent at all
//
// An agent asked to classify something will do it by generating text and
// hoping the text parses. That is the failure mode TypeSafe exists to remove:
// the answer comes back as a probability over a set you defined, with a
// confidence, and it cannot be a value you did not declare.
//
// Giving an agent this tool means the classification step stops being a
// parsing problem. The agent decides *when* to classify; the classification
// itself is not generated.
//
// # Two types, because LangChainGo has two Call methods
//
// tools.Tool declares Call(ctx, string) (string, error) and chains.Chain
// declares Call(ctx, map, ...opt) (map, error). Same name, different
// signatures, so no single Go type can satisfy both. Classifier is the tool;
// Chain is the chain. They share their configuration and their behavior, and
// differ only in how they are called.
package langchaingo

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/memory"
	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/tools"

	typesafe "github.com/nibir1/typesafe-go"
)

// Defaults for the chain's input and output keys.
const (
	// DefaultInputKey is the chain input holding the text to classify.
	DefaultInputKey = "state"

	// DefaultOutputKey is the chain output holding the decoded answers.
	DefaultOutputKey = "answers"
)

// Option configures a Classifier or a Chain.
type Option func(*config)

type config struct {
	name        string
	description string
	model       string
	inputKey    string
	outputKey   string
}

func newConfig(opts ...Option) config {
	c := config{
		name:      "typesafe_classify",
		inputKey:  DefaultInputKey,
		outputKey: DefaultOutputKey,
	}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// WithName sets the tool name the agent sees.
//
// Name it for the decision, not for the vendor: an agent picks a tool by its
// name and description, and "typesafe_classify" tells it nothing about when to
// reach for it. "classify_support_ticket" does.
func WithName(name string) Option {
	return func(c *config) { c.name = name }
}

// WithDescription sets the tool description the agent sees.
//
// This is the single most load-bearing string in the integration. It is the
// only thing telling the model when the tool applies and what to put in it, so
// say what the input should be, not just what the tool does.
func WithDescription(d string) Option {
	return func(c *config) { c.description = d }
}

// WithModel pins the model. Empty uses the client's default.
func WithModel(m string) Option {
	return func(c *config) { c.model = m }
}

// WithKeys sets the chain's input and output keys. Ignored by Classifier,
// which has no keys.
func WithKeys(input, output string) Option {
	return func(c *config) {
		if input != "" {
			c.inputKey = input
		}
		if output != "" {
			c.outputKey = output
		}
	}
}

// Classifier is a LangChainGo tool that answers a fixed question set about
// whatever text it is given.
type Classifier struct {
	client    *typesafe.Client
	questions typesafe.Questions
	cfg       config
}

var _ tools.Tool = (*Classifier)(nil)

// NewClassifier builds the tool.
func NewClassifier(client *typesafe.Client, questions typesafe.Questions, opts ...Option) *Classifier {
	return &Classifier{client: client, questions: questions, cfg: newConfig(opts...)}
}

// Name implements tools.Tool.
func (c *Classifier) Name() string { return c.cfg.name }

// Description implements tools.Tool.
//
// Falls back to a description generated from the questions when none was
// given. The generated one is serviceable and better than an empty string, but
// it describes the questions rather than when to use them — write your own.
func (c *Classifier) Description() string {
	if c.cfg.description != "" {
		return c.cfg.description
	}
	return generatedDescription(c.questions)
}

// Call implements tools.Tool.
//
// The answers come back as JSON, because that is the only shape an agent can
// reliably act on. Confidence is included: a tool that returns only the winning
// option throws away the number that decides whether to act on it.
func (c *Classifier) Call(ctx context.Context, input string) (string, error) {
	resp, err := c.evaluate(ctx, input)
	if err != nil {
		return "", err
	}
	out, err := summarize(resp)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("typesafe/langchaingo: encoding answers: %w", err)
	}
	return string(b), nil
}

// Response runs the classification and returns the raw SDK response, for
// callers that want the typed accessors rather than JSON.
func (c *Classifier) Response(ctx context.Context, input string) (*typesafe.SystemOneResponse, error) {
	return c.evaluate(ctx, input)
}

func (c *Classifier) evaluate(ctx context.Context, input string) (*typesafe.SystemOneResponse, error) {
	if c.client == nil {
		return nil, fmt.Errorf("typesafe/langchaingo: no client")
	}
	if strings.TrimSpace(input) == "" {
		return nil, fmt.Errorf("typesafe/langchaingo: empty input; there is nothing to classify")
	}
	return c.client.SystemOne(ctx, &typesafe.SystemOneRequest{
		State:     input,
		Model:     c.cfg.model,
		Questions: c.questions,
	})
}

// Chain is the same classification as a LangChainGo chain.
type Chain struct {
	classifier *Classifier
	mem        schema.Memory
}

var _ chains.Chain = (*Chain)(nil)

// NewChain builds the chain.
func NewChain(client *typesafe.Client, questions typesafe.Questions, opts ...Option) *Chain {
	return &Chain{
		classifier: NewClassifier(client, questions, opts...),
		mem:        memory.NewSimple(),
	}
}

// Call implements chains.Chain.
//
// Reads the text under the input key and returns the decoded answers under the
// output key, as a map[string]any so a downstream chain can index it.
func (c *Chain) Call(ctx context.Context, inputs map[string]any, _ ...chains.ChainCallOption) (map[string]any, error) {
	raw, ok := inputs[c.classifier.cfg.inputKey]
	if !ok {
		return nil, fmt.Errorf("typesafe/langchaingo: chain input %q is missing",
			c.classifier.cfg.inputKey)
	}
	text, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("typesafe/langchaingo: chain input %q is %T, want string",
			c.classifier.cfg.inputKey, raw)
	}

	resp, err := c.classifier.evaluate(ctx, text)
	if err != nil {
		return nil, err
	}
	answers, err := summarize(resp)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		c.classifier.cfg.outputKey: answers,
		"model":                    resp.Model,
		"input_tokens":             resp.Usage.InputTokens,
	}, nil
}

// GetMemory implements chains.Chain.
//
// A classification has no conversational state — the same text yields the same
// answers regardless of what came before — so this is the no-op memory rather
// than a buffer that would accumulate for nothing.
func (c *Chain) GetMemory() schema.Memory { return c.mem }

// GetInputKeys implements chains.Chain.
func (c *Chain) GetInputKeys() []string { return []string{c.classifier.cfg.inputKey} }

// GetOutputKeys implements chains.Chain.
func (c *Chain) GetOutputKeys() []string {
	return []string{c.classifier.cfg.outputKey, "model", "input_tokens"}
}

// Answer is one decoded answer, in the shape an agent or a downstream chain
// can act on.
type Answer struct {
	// Type is "noul", "choice" or "score".
	Type string `json:"type"`

	// Value is the probability for a Noul, the winning option for a Choice,
	// or the weighted position for a Score.
	Value any `json:"value"`

	// Confidence is present for a Choice and a Score. A Noul has none — its
	// probability is the uncertainty — and the field is omitted rather than
	// being reported as zero, which would read as "completely unsure".
	Confidence *float64 `json:"confidence,omitempty"`

	// Distribution is every option or level with its probability.
	Distribution map[string]float64 `json:"distribution,omitempty"`
}

// summarize decodes a response into the flat shape the tool and chain return.
func summarize(resp *typesafe.SystemOneResponse) (map[string]Answer, error) {
	if resp == nil {
		return nil, fmt.Errorf("typesafe/langchaingo: nil response")
	}
	out := make(map[string]Answer, len(resp.Answers))
	for id := range resp.Answers {
		a, err := resp.Answer(id)
		if err != nil {
			// An answer type this SDK does not model is skipped rather than
			// failing the whole call, matching SystemOneResponse.All.
			continue
		}
		switch v := a.(type) {
		case typesafe.NoulAnswer:
			out[id] = Answer{Type: typesafe.TypeNoul, Value: v.Noul}
		case typesafe.ChoiceAnswer:
			conf := v.Confidence
			out[id] = Answer{
				Type: typesafe.TypeChoice, Value: v.Choice,
				Confidence: &conf, Distribution: v.Probabilities,
			}
		case typesafe.ScoreAnswer:
			conf := v.Confidence
			out[id] = Answer{
				Type: typesafe.TypeScore, Value: v.Score,
				Confidence: &conf, Distribution: v.Probabilities,
			}
		}
	}
	return out, nil
}

// generatedDescription builds a tool description from the questions.
func generatedDescription(qs typesafe.Questions) string {
	ids := make([]string, 0, len(qs))
	for id := range qs {
		ids = append(ids, id)
	}
	sort.Strings(ids) // map order is random; a description that varies is noise

	var b strings.Builder
	b.WriteString("Classify a piece of text. Input: the text itself. ")
	b.WriteString("Returns JSON with one entry per question: ")
	b.WriteString(strings.Join(ids, ", "))
	b.WriteString(". Each entry carries a value and, except for yes/no questions, a confidence in [0,1].")
	return b.String()
}

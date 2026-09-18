package nethttp_test

import (
	"encoding/json"
	"log"
	"net/http"

	typesafe "github.com/nibir1/typesafe-go"
	"github.com/nibir1/typesafe-go/integrations/nethttp"
)

// A service that triages inbound tickets, with the inbound request id carried
// through to TypeSafe.
//
// No Output comment, so this compiles and is type-checked but is not executed:
// running it would need a real API key and a live endpoint.
func Example() {
	client, err := typesafe.NewClient()
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /triage", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Ticket string `json:"ticket"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// The same context carries the client and the inbound correlation id.
		ts := nethttp.MustFrom(r.Context())
		resp, err := ts.SystemOne(r.Context(), &typesafe.SystemOneRequest{
			State: body.Ticket,
			Questions: typesafe.Questions{
				"team": typesafe.Choice{
					Instructions: "Which team should handle this?",
					Criteria: typesafe.Options{
						"billing":   "Payments, invoicing, refunds",
						"technical": "Bugs, outages, integrations",
					},
				},
			},
		})
		if err != nil {
			http.Error(w, "upstream failed", http.StatusBadGateway)
			return
		}

		team, err := resp.Choice("team")
		if err != nil {
			http.Error(w, "unexpected answer", http.StatusBadGateway)
			return
		}
		// Confidence is the second axis: route automatically when sure, ask a
		// person when not.
		if team.Confidence < 0.7 {
			_ = json.NewEncoder(w).Encode(map[string]any{"team": "human_review"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"team":       team.Choice,
			"confidence": team.Confidence,
		})
	})

	// Correlation outermost, so the id is on the context before anything uses
	// the client.
	handler := nethttp.CorrelationMiddleware()(nethttp.Middleware(client)(mux))
	log.Fatal(http.ListenAndServe(":8080", handler))
}

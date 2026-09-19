// Command speculative_fanout asks every question up front instead of walking a
// decision tree one call at a time.
//
//	go run ./speculative_fanout
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	typesafe "github.com/nibir1/typesafe-go"
)

func main() {
	client, err := typesafe.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	if err := run(context.Background(), client, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

const document = `INVOICE #4471
Acme Corporation
Due: 2026-02-14
Amount due: $12,400.00
Terms: Net 30. Late payments accrue 1.5% monthly interest.
Remit to: Acme Corp, Account 8829-11`

func run(ctx context.Context, client *typesafe.Client, out io.Writer) error {
	// The naive shape is a chain: ask "is it a financial document?", and only
	// if yes ask "is it an invoice?", and only then ask about the terms. That
	// is three round trips of 70-500ms each, serialized.
	//
	// Every question here refers to the same state, and questions in one
	// request are evaluated in parallel and cost only their own tokens. So
	// ask them all now and discard what the answers make irrelevant: one
	// round trip, for a few hundred extra tokens.
	resp, err := client.SystemOne(ctx, &typesafe.SystemOneRequest{
		State: document,
		Questions: typesafe.Questions{
			"is_financial":     typesafe.Noul{Instructions: "Is this a financial document?"},
			"is_invoice":       typesafe.Noul{Instructions: "Is this an invoice, as opposed to a receipt or a quote?"},
			"is_overdue_terms": typesafe.Noul{Instructions: "Does this document mention a penalty for late payment?"},
			"has_bank_details": typesafe.Noul{Instructions: "Does this document contain bank or account details?"},
			"doc_type": typesafe.Choice{
				Instructions: "What kind of document is this?",
				Criteria: typesafe.Options{
					"invoice": "A request for payment",
					"receipt": "A record of a payment already made",
					"quote":   "A price offered, not yet owed",
					"other":   nil,
				},
			},
		},
	})
	if err != nil {
		return err
	}

	financial, err := resp.Noul("is_financial")
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "financial:       %.2f\n", financial.Noul)

	// The cheap part: the branch is now local, with no further round trip
	// whichever way it goes.
	if financial.Noul < 0.5 {
		fmt.Fprintln(out, "-> not a financial document; the rest is discarded")
		return nil
	}

	docType, err := resp.Choice("doc_type")
	if err != nil {
		return err
	}
	penalty, err := resp.Noul("is_overdue_terms")
	if err != nil {
		return err
	}
	bank, err := resp.Noul("has_bank_details")
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "type:            %s (confidence %.2f)\n", docType.Choice, docType.Confidence)
	fmt.Fprintf(out, "late penalty:    %.2f\n", penalty.Noul)
	fmt.Fprintf(out, "bank details:    %.2f\n", bank.Noul)

	if bank.Noul > 0.7 {
		fmt.Fprintln(out, "-> contains payment details; route to the restricted queue")
	}
	fmt.Fprintf(out, "\ncost: %d input tokens, one round trip\n", resp.Usage.InputTokens)
	return nil
}

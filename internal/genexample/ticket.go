// Package genexample is the worked example typesafe-gen generates from.
//
// It is internal because it exists to be generated from and tested against,
// not to be imported. The generated file beside it is checked in, and a test
// regenerates it and fails on any difference — a generator whose output is not
// asserted to be current is a generator that quietly rots.
package genexample

//go:generate go run github.com/nibir1/typesafe-go/cmd/typesafe-gen -type TicketQuestions

// Topic is the team that should handle a ticket.
type Topic string

const (
	// TopicBilling covers payments, invoicing and refunds.
	TopicBilling Topic = "billing"

	// TopicTechnical covers bugs, outages and integration failures.
	TopicTechnical Topic = "technical"

	// TopicSales covers pricing, upgrades and new accounts.
	TopicSales Topic = "sales"

	// TopicOther is anything the other options do not cover.
	TopicOther Topic = "other"
)

// Severity is how bad an incident is.
type Severity int

const (
	// SevNone means no customer impact.
	SevNone Severity = iota

	// SevMinor means a degraded experience with a workaround.
	SevMinor

	// SevMajor means a core feature is unusable.
	SevMajor

	// SevCritical means the product is down or data is at risk.
	SevCritical
)

// TicketQuestions is everything asked about one support ticket.
type TicketQuestions struct {
	// Which team should handle this ticket?
	Department Topic `typesafe:"choice"`

	// How severe is the problem described here?
	Severity Severity `typesafe:"score,id=severity"`
}

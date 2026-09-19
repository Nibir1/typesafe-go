// The runnable examples.
//
// One module for all of them rather than one each: they share a cassette
// harness and a CI job, and ten go.mod files carrying the same single
// dependency would be ceremony without benefit.
//
// Its own module all the same, so that nothing here can creep into the SDK's
// dependency graph.
module github.com/nibir1/typesafe-go/examples

go 1.23

replace github.com/nibir1/typesafe-go => ../

require github.com/nibir1/typesafe-go v0.0.0

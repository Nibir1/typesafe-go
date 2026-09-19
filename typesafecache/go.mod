// The response cache lives in its own module so the core stays importable
// with nothing behind it, and so a caller who does not want a cache does not
// carry one.
//
// It has no third-party dependencies either — the LRU and the disk tier are
// written here rather than pulled in — so adding it to a build adds exactly
// this package and nothing else.
module github.com/nibir1/typesafe-go/typesafecache

go 1.23

replace github.com/nibir1/typesafe-go => ../

// The cache hashes requests with the same canonical form the cassette matcher
// uses, so it tracks the SDK in this repository rather than a published
// version.

require github.com/nibir1/typesafe-go v0.0.0

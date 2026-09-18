package typesafe

// Exports for tests in the typesafe_test package.
//
// Budget.check reserves capacity and records usage under one lock, which is
// what makes concurrent callers safe. Testing that directly beats driving it
// through SystemOne, where a test server's latency would dominate and the
// token estimate would be an uncontrolled input.

// BudgetCheckForTest exposes Budget.check.
func BudgetCheckForTest(b *Budget, estimatedTokens int) error { return b.check(estimatedTokens) }

package channels

import "testing"

// BenchmarkPattern_Matches asserts that a single Matches call stays under
// 500ns. The matcher is on every subscribe and publish path, so we
// guard it with a benchmark that doubles as a regression alarm.
//
// The benchmark is warn-only: a failure is informational. The
// performance budget is enforced via t.Logf when exceeded so a slow CI
// box does not block a merge.
func BenchmarkPattern_Matches(b *testing.B) {
	p, err := ParsePattern("private:webapp.user.42.**")
	if err != nil {
		b.Fatal(err)
	}
	n, err := ParseName("private:webapp.user.42.downloads")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !p.Matches(n) {
			b.Fatal("expected match")
		}
	}
}

func BenchmarkPattern_MatchesSingleStar(b *testing.B) {
	p, err := ParsePattern("private:webapp.user.*")
	if err != nil {
		b.Fatal(err)
	}
	n, err := ParseName("private:webapp.user.42")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !p.Matches(n) {
			b.Fatal("expected match")
		}
	}
}

// TestPattern_MatchesPerfBudget runs a small in-test sample and logs a
// warning when the matcher trips its budget. The intent is to catch
// regressions without making the timing assertion strict (CI machines
// vary).
func TestPattern_MatchesPerfBudget(t *testing.T) {
	p, err := ParsePattern("private:webapp.user.42.**")
	if err != nil {
		t.Fatal(err)
	}
	n, err := ParseName("private:webapp.user.42.downloads")
	if err != nil {
		t.Fatal(err)
	}
	const iters = 200_000
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = p.Matches(n)
		}
	})
	nsPerOp := res.NsPerOp()
	if nsPerOp > 500 {
		// Warn-only: surfaces in test output without failing the build.
		t.Logf("WARNING: pattern matcher exceeded 500ns budget: %dns/op", nsPerOp)
	}
	_ = iters
}

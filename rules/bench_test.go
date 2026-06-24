package rules

import (
	"context"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

// benchSink defends evaluate results from dead-code elimination.
var benchSink any

// benchRule is a representative rule exercising a non-trivial CEL condition:
// a string method call plus a custom function over headers.
var benchRule = Rule{
	ID:        "bench-rule",
	Condition: `subject.contains("URGENT") && has_header(headers, "X-Priority")`,
}

// newBenchEngine builds an Engine over an in-memory store and a message
// activation, returning both. The message matches benchRule so evaluate does
// the full eval, not an early-out.
func newBenchEngine(b *testing.B) (*Engine, map[string]any) {
	b.Helper()
	st := memory.New()
	if err := st.Connect(context.Background()); err != nil {
		b.Fatalf("connect store: %v", err)
	}
	b.Cleanup(func() { _ = st.Close(context.Background()) })
	msg, err := st.CreateMessage(context.Background(), store.MessageData{
		OwnerID:      "recipient1",
		SenderID:     "sender1",
		RecipientIDs: []string{"recipient1"},
		Subject:      "URGENT: invoice overdue",
		Body:         "please review",
		Headers:      map[string]string{"X-Priority": "1"},
		Status:       store.MessageStatusDelivered,
		FolderID:     store.FolderInbox,
	})
	if err != nil {
		b.Fatalf("seed message: %v", err)
	}
	engine, err := NewEngine(NewStaticProvider(nil, nil), st)
	if err != nil {
		b.Fatalf("new engine: %v", err)
	}
	return engine, buildActivation(msg)
}

// clearCache empties the engine's compiled-program cache, forcing the next
// compileOrCache call to recompile (a cache miss).
func clearCache(e *Engine) {
	e.mu.Lock()
	e.cache = make(map[string]cel.Program)
	e.mu.Unlock()
}

// BenchmarkRuleEvaluate measures rule evaluation in two regimes:
//
//   - compile-cold: the program cache is cleared before each evaluate call, so
//     compileOrCache always misses and recompiles the CEL expression. This is
//     the cost paid the first time a rule (by ID+condition) is seen.
//   - cache-warm: the program is compiled once and every evaluate call is a
//     cache hit, measuring just CEL program execution over the activation.
//
// The delta between the two is the compilation cost the cache amortizes away.
func BenchmarkRuleEvaluate(b *testing.B) {
	b.Run("compile-cold", func(b *testing.B) {
		engine, activation := newBenchEngine(b)
		b.ReportAllocs()
		for b.Loop() {
			clearCache(engine)
			matched, err := engine.evaluate(benchRule, activation)
			if err != nil {
				b.Fatal(err)
			}
			benchSink = matched
		}
	})

	b.Run("cache-warm", func(b *testing.B) {
		engine, activation := newBenchEngine(b)
		// Prime the cache so every loop iteration is a cache hit.
		if _, err := engine.evaluate(benchRule, activation); err != nil {
			b.Fatalf("prime: %v", err)
		}
		b.ReportAllocs()
		for b.Loop() {
			matched, err := engine.evaluate(benchRule, activation)
			if err != nil {
				b.Fatal(err)
			}
			benchSink = matched
		}
	})
}

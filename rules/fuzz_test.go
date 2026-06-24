package rules

import (
	"context"
	"testing"

	"github.com/rbaliyan/mailbox/store/memory"
)

// FuzzCompile feeds arbitrary strings into the CEL compile + evaluate path
// (compileOrCache -> evaluate). Oracle: neither compilation of an arbitrary
// expression nor evaluation against a fixed activation may panic. Invalid
// expressions and non-bool results must surface as errors, never crashes.
//
// Seeds are structure-aware (valid CEL drawn from the rules tests and the
// package doc variable table) so the fuzzer mutates around real grammar rather
// than starting from random noise.
func FuzzCompile(f *testing.F) {
	seeds := []string{
		// Valid, from engine/cache tests.
		"true",
		"false",
		`sender == "sender1"`,
		`subject.contains("Invoice")`,
		`subject.contains("Project")`,
		// Custom functions from cel_functions.go.
		`matches_regex(subject, "^Re:")`,
		`has_header(headers, "X-Priority")`,
		`header_value(headers, "X-Priority") == "high"`,
		`has_metadata(metadata, "source")`,
		`metadata_value(metadata, "source") == "api"`,
		`contains_tag(tags, "urgent")`,
		// Exercising declared variables (rules/doc.go variable table).
		`has_attachments && attachment_count > 2`,
		`is_reply && thread_id != ""`,
		`folder == "inbox"`,
		`size(recipients) > 1`,
		`body.startsWith("URGENT")`,
		// Invalid / adversarial.
		"invalid syntax !!!",
		"",
		`subject ==`,
		`undefined_var > 1`,
		`1 + "string"`,
		`subject.contains(`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	// Build a real engine once, outside the fuzz loop. NewEngine creates the
	// shared CEL environment with all variable and function declarations.
	store := memory.New()
	if err := store.Connect(context.Background()); err != nil {
		f.Fatalf("connect store: %v", err)
	}
	f.Cleanup(func() { _ = store.Close(context.Background()) })

	engine, err := NewEngine(NewStaticProvider(nil, nil), store)
	if err != nil {
		f.Fatalf("new engine: %v", err)
	}

	// A fully-populated activation matching the declared CEL variable types,
	// so evaluation of a successfully compiled expression exercises real code.
	activation := map[string]any{
		"sender":           "alice",
		"subject":          "Re: Invoice #42",
		"body":             "URGENT please review",
		"recipients":       []string{"bob", "carol"},
		"headers":          map[string]string{"X-Priority": "high"},
		"metadata":         map[string]any{"source": "api"},
		"has_attachments":  true,
		"attachment_count": int64(3),
		"thread_id":        "thread-1",
		"is_reply":         true,
		"folder":           "inbox",
		"tags":             []string{"urgent", "work"},
	}

	f.Fuzz(func(t *testing.T, condition string) {
		rule := Rule{ID: "fuzz", Condition: condition}

		// compileOrCache must never panic regardless of the expression.
		prg, err := engine.compileOrCache(rule)
		if err != nil {
			// Compilation errors are an expected outcome for arbitrary input.
			return
		}
		// A program that compiled must be evaluable without panicking. The
		// result may be a non-bool or an eval error; both are fine.
		_, _, _ = prg.Eval(activation)
	})
}

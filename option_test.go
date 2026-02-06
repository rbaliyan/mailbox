package mailbox

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestNewOptions(t *testing.T) {
	t.Run("returns defaults without options", func(t *testing.T) {
		opts := newOptions()

		if opts.trashRetention != DefaultTrashRetention {
			t.Errorf("expected trashRetention %v, got %v", DefaultTrashRetention, opts.trashRetention)
		}
		if opts.maxSubjectLength != DefaultMaxSubjectLength {
			t.Errorf("expected maxSubjectLength %v, got %v", DefaultMaxSubjectLength, opts.maxSubjectLength)
		}
		if opts.maxBodySize != DefaultMaxBodySize {
			t.Errorf("expected maxBodySize %v, got %v", DefaultMaxBodySize, opts.maxBodySize)
		}
		if opts.maxAttachmentSize != DefaultMaxAttachmentSize {
			t.Errorf("expected maxAttachmentSize %v, got %v", DefaultMaxAttachmentSize, opts.maxAttachmentSize)
		}
		if opts.maxAttachmentCount != DefaultMaxAttachmentCount {
			t.Errorf("expected maxAttachmentCount %v, got %v", DefaultMaxAttachmentCount, opts.maxAttachmentCount)
		}
		if opts.maxRecipientCount != DefaultMaxRecipientCount {
			t.Errorf("expected maxRecipientCount %v, got %v", DefaultMaxRecipientCount, opts.maxRecipientCount)
		}
		if opts.maxMetadataSize != DefaultMaxMetadataSize {
			t.Errorf("expected maxMetadataSize %v, got %v", DefaultMaxMetadataSize, opts.maxMetadataSize)
		}
		if opts.maxMetadataKeys != DefaultMaxMetadataKeys {
			t.Errorf("expected maxMetadataKeys %v, got %v", DefaultMaxMetadataKeys, opts.maxMetadataKeys)
		}
		if opts.maxQueryLimit != DefaultMaxQueryLimit {
			t.Errorf("expected maxQueryLimit %v, got %v", DefaultMaxQueryLimit, opts.maxQueryLimit)
		}
		if opts.defaultQueryLimit != DefaultQueryLimit {
			t.Errorf("expected defaultQueryLimit %v, got %v", DefaultQueryLimit, opts.defaultQueryLimit)
		}
		if opts.maxConcurrentSends != DefaultMaxConcurrentSends {
			t.Errorf("expected maxConcurrentSends %v, got %v", DefaultMaxConcurrentSends, opts.maxConcurrentSends)
		}
	})

}

func TestWithLogger(t *testing.T) {
	t.Run("sets custom logger", func(t *testing.T) {
		customLogger := slog.Default()
		opts := newOptions(WithLogger(customLogger))
		if opts.logger != customLogger {
			t.Error("expected custom logger to be set")
		}
	})

	t.Run("ignores nil logger", func(t *testing.T) {
		opts := newOptions(WithLogger(nil))
		if opts.logger == nil {
			t.Error("expected default logger when nil passed")
		}
	})
}

func TestWithTrashRetention(t *testing.T) {
	t.Run("sets custom retention", func(t *testing.T) {
		retention := 7 * 24 * time.Hour
		opts := newOptions(WithTrashRetention(retention))
		if opts.trashRetention != retention {
			t.Errorf("expected retention %v, got %v", retention, opts.trashRetention)
		}
	})

	t.Run("ignores retention below minimum", func(t *testing.T) {
		opts := newOptions(WithTrashRetention(1 * time.Hour))
		if opts.trashRetention != DefaultTrashRetention {
			t.Errorf("expected default retention %v, got %v", DefaultTrashRetention, opts.trashRetention)
		}
	})
}

func TestWithTracing(t *testing.T) {
	t.Run("enables tracing", func(t *testing.T) {
		opts := newOptions(WithTracing(true))
		if !opts.tracingEnabled {
			t.Error("expected tracing to be enabled")
		}
	})

	t.Run("disables tracing", func(t *testing.T) {
		opts := newOptions(WithTracing(false))
		if opts.tracingEnabled {
			t.Error("expected tracing to be disabled")
		}
	})
}

func TestWithMetrics(t *testing.T) {
	t.Run("enables metrics", func(t *testing.T) {
		opts := newOptions(WithMetrics(true))
		if !opts.metricsEnabled {
			t.Error("expected metrics to be enabled")
		}
	})

	t.Run("disables metrics", func(t *testing.T) {
		opts := newOptions(WithMetrics(false))
		if opts.metricsEnabled {
			t.Error("expected metrics to be disabled")
		}
	})
}

func TestWithOTel(t *testing.T) {
	t.Run("enables both tracing and metrics", func(t *testing.T) {
		opts := newOptions(WithOTel(true))
		if !opts.tracingEnabled {
			t.Error("expected tracing to be enabled")
		}
		if !opts.metricsEnabled {
			t.Error("expected metrics to be enabled")
		}
	})

	t.Run("disables both tracing and metrics", func(t *testing.T) {
		opts := newOptions(WithOTel(false))
		if opts.tracingEnabled {
			t.Error("expected tracing to be disabled")
		}
		if opts.metricsEnabled {
			t.Error("expected metrics to be disabled")
		}
	})
}

func TestWithServiceName(t *testing.T) {
	t.Run("sets service name", func(t *testing.T) {
		name := "my-mailbox"
		opts := newOptions(WithServiceName(name))
		if opts.serviceName != name {
			t.Errorf("expected service name %q, got %q", name, opts.serviceName)
		}
	})

	t.Run("ignores empty service name", func(t *testing.T) {
		opts := newOptions(WithServiceName(""))
		if opts.serviceName != "" {
			t.Errorf("expected empty service name, got %q", opts.serviceName)
		}
	})
}

func TestMessageLimitOptions(t *testing.T) {
	t.Run("WithMaxBodySize", func(t *testing.T) {
		opts := newOptions(WithMaxBodySize(5 * 1024 * 1024))
		if opts.maxBodySize != 5*1024*1024 {
			t.Errorf("expected maxBodySize 5MB, got %d", opts.maxBodySize)
		}
	})

	t.Run("WithMaxAttachmentSize", func(t *testing.T) {
		opts := newOptions(WithMaxAttachmentSize(50 * 1024 * 1024))
		if opts.maxAttachmentSize != 50*1024*1024 {
			t.Errorf("expected maxAttachmentSize 50MB, got %d", opts.maxAttachmentSize)
		}
	})

	t.Run("WithMaxRecipients", func(t *testing.T) {
		opts := newOptions(WithMaxRecipients(50))
		if opts.maxRecipientCount != 50 {
			t.Errorf("expected maxRecipientCount 50, got %d", opts.maxRecipientCount)
		}
	})
}

func TestWithMaxConcurrentSends(t *testing.T) {
	t.Run("sets custom concurrent sends limit", func(t *testing.T) {
		opts := newOptions(WithMaxConcurrentSends(20))
		if opts.maxConcurrentSends != 20 {
			t.Errorf("expected maxConcurrentSends 20, got %d", opts.maxConcurrentSends)
		}
	})

	t.Run("ignores zero or negative", func(t *testing.T) {
		opts := newOptions(WithMaxConcurrentSends(0))
		if opts.maxConcurrentSends != DefaultMaxConcurrentSends {
			t.Errorf("expected default maxConcurrentSends, got %d", opts.maxConcurrentSends)
		}
	})
}

func TestWithShutdownTimeout(t *testing.T) {
	t.Run("sets custom shutdown timeout", func(t *testing.T) {
		timeout := 60 * time.Second
		opts := newOptions(WithShutdownTimeout(timeout))
		if opts.shutdownTimeout != timeout {
			t.Errorf("expected shutdownTimeout %v, got %v", timeout, opts.shutdownTimeout)
		}
	})

	t.Run("ignores timeout below minimum", func(t *testing.T) {
		opts := newOptions(WithShutdownTimeout(500 * time.Millisecond))
		if opts.shutdownTimeout != DefaultShutdownTimeout {
			t.Errorf("expected default shutdownTimeout %v, got %v", DefaultShutdownTimeout, opts.shutdownTimeout)
		}
	})

	t.Run("uses default when not specified", func(t *testing.T) {
		opts := newOptions()
		if opts.shutdownTimeout != DefaultShutdownTimeout {
			t.Errorf("expected default shutdownTimeout %v, got %v", DefaultShutdownTimeout, opts.shutdownTimeout)
		}
	})
}

func TestWithPlugin(t *testing.T) {
	t.Run("adds plugin", func(t *testing.T) {
		plugin := &mockPlugin{}
		opts := newOptions(WithPlugin(plugin))
		if len(opts.plugins) != 1 {
			t.Errorf("expected 1 plugin, got %d", len(opts.plugins))
		}
	})

	t.Run("ignores nil plugin", func(t *testing.T) {
		opts := newOptions(WithPlugin(nil))
		if len(opts.plugins) != 0 {
			t.Errorf("expected 0 plugins, got %d", len(opts.plugins))
		}
	})
}

func TestWithPlugins(t *testing.T) {
	t.Run("adds multiple plugins", func(t *testing.T) {
		p1 := &mockPlugin{}
		p2 := &mockPlugin{}
		opts := newOptions(WithPlugins(p1, p2))
		if len(opts.plugins) != 2 {
			t.Errorf("expected 2 plugins, got %d", len(opts.plugins))
		}
	})

	t.Run("filters nil plugins", func(t *testing.T) {
		p1 := &mockPlugin{}
		opts := newOptions(WithPlugins(p1, nil, nil))
		if len(opts.plugins) != 1 {
			t.Errorf("expected 1 plugin (nil filtered), got %d", len(opts.plugins))
		}
	})
}

func TestOptionsGetLimits(t *testing.T) {
	opts := newOptions(
		WithMaxBodySize(1024),
		WithMaxAttachmentSize(2048),
		WithMaxRecipients(10),
	)

	limits := opts.getLimits()

	if limits.MaxBodySize != 1024 {
		t.Errorf("expected MaxBodySize 1024, got %d", limits.MaxBodySize)
	}
	if limits.MaxAttachmentSize != 2048 {
		t.Errorf("expected MaxAttachmentSize 2048, got %d", limits.MaxAttachmentSize)
	}
	if limits.MaxRecipientCount != 10 {
		t.Errorf("expected MaxRecipientCount 10, got %d", limits.MaxRecipientCount)
	}
	// Other limits should have default values
	if limits.MaxSubjectLength != DefaultMaxSubjectLength {
		t.Errorf("expected default MaxSubjectLength, got %d", limits.MaxSubjectLength)
	}
}

// mockPlugin implements Plugin for testing
type mockPlugin struct{}

func (p *mockPlugin) Name() string                    { return "mock" }
func (p *mockPlugin) Init(ctx context.Context) error  { return nil }
func (p *mockPlugin) Close(ctx context.Context) error { return nil }
func (p *mockPlugin) Priority() int                   { return 0 }

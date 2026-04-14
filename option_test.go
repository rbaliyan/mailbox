package mailbox

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestConfigApplyDefaults(t *testing.T) {
	t.Run("fills all defaults for zero config", func(t *testing.T) {
		cfg := Config{}
		cfg.applyDefaults()

		if cfg.TrashRetention != DefaultTrashRetention {
			t.Errorf("TrashRetention = %v, want %v", cfg.TrashRetention, DefaultTrashRetention)
		}
		if cfg.MaxSubjectLength != DefaultMaxSubjectLength {
			t.Errorf("MaxSubjectLength = %v, want %v", cfg.MaxSubjectLength, DefaultMaxSubjectLength)
		}
		if cfg.MaxBodySize != DefaultMaxBodySize {
			t.Errorf("MaxBodySize = %v, want %v", cfg.MaxBodySize, DefaultMaxBodySize)
		}
		if cfg.MaxAttachmentSize != DefaultMaxAttachmentSize {
			t.Errorf("MaxAttachmentSize = %v, want %v", cfg.MaxAttachmentSize, DefaultMaxAttachmentSize)
		}
		if cfg.MaxAttachmentCount != DefaultMaxAttachmentCount {
			t.Errorf("MaxAttachmentCount = %v, want %v", cfg.MaxAttachmentCount, DefaultMaxAttachmentCount)
		}
		if cfg.MaxRecipientCount != DefaultMaxRecipientCount {
			t.Errorf("MaxRecipientCount = %v, want %v", cfg.MaxRecipientCount, DefaultMaxRecipientCount)
		}
		if cfg.MaxMetadataSize != DefaultMaxMetadataSize {
			t.Errorf("MaxMetadataSize = %v, want %v", cfg.MaxMetadataSize, DefaultMaxMetadataSize)
		}
		if cfg.MaxMetadataKeys != DefaultMaxMetadataKeys {
			t.Errorf("MaxMetadataKeys = %v, want %v", cfg.MaxMetadataKeys, DefaultMaxMetadataKeys)
		}
		if cfg.MaxQueryLimit != DefaultMaxQueryLimit {
			t.Errorf("MaxQueryLimit = %v, want %v", cfg.MaxQueryLimit, DefaultMaxQueryLimit)
		}
		if cfg.DefaultQueryLimit != DefaultQueryLimit {
			t.Errorf("DefaultQueryLimit = %v, want %v", cfg.DefaultQueryLimit, DefaultQueryLimit)
		}
		if cfg.MaxConcurrentSends != DefaultMaxConcurrentSends {
			t.Errorf("MaxConcurrentSends = %v, want %v", cfg.MaxConcurrentSends, DefaultMaxConcurrentSends)
		}
		if cfg.ShutdownTimeout != DefaultShutdownTimeout {
			t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, DefaultShutdownTimeout)
		}
		if cfg.MinTTL != 1*time.Minute {
			t.Errorf("MinTTL = %v, want 1m", cfg.MinTTL)
		}
		if cfg.StatsRefreshInterval != DefaultStatsRefreshInterval {
			t.Errorf("StatsRefreshInterval = %v, want %v", cfg.StatsRefreshInterval, DefaultStatsRefreshInterval)
		}
		if cfg.ClaimInterval != DefaultClaimInterval {
			t.Errorf("ClaimInterval = %v, want %v", cfg.ClaimInterval, DefaultClaimInterval)
		}
		if cfg.ClaimMinIdle != DefaultClaimMinIdle {
			t.Errorf("ClaimMinIdle = %v, want %v", cfg.ClaimMinIdle, DefaultClaimMinIdle)
		}
		if cfg.ClaimBatchSize != DefaultClaimBatchSize {
			t.Errorf("ClaimBatchSize = %v, want %v", cfg.ClaimBatchSize, DefaultClaimBatchSize)
		}
		if cfg.MaxHeaderCount != DefaultMaxHeaderCount {
			t.Errorf("MaxHeaderCount = %v, want %v", cfg.MaxHeaderCount, DefaultMaxHeaderCount)
		}
		if cfg.MaxHeaderKeyLength != DefaultMaxHeaderKeyLength {
			t.Errorf("MaxHeaderKeyLength = %v, want %v", cfg.MaxHeaderKeyLength, DefaultMaxHeaderKeyLength)
		}
		if cfg.MaxHeaderValueLength != DefaultMaxHeaderValueLength {
			t.Errorf("MaxHeaderValueLength = %v, want %v", cfg.MaxHeaderValueLength, DefaultMaxHeaderValueLength)
		}
		if cfg.MaxHeadersTotalSize != DefaultMaxHeadersTotalSize {
			t.Errorf("MaxHeadersTotalSize = %v, want %v", cfg.MaxHeadersTotalSize, DefaultMaxHeadersTotalSize)
		}
	})

	t.Run("preserves non-zero values", func(t *testing.T) {
		cfg := Config{
			TrashRetention:     7 * 24 * time.Hour,
			MaxBodySize:        5 * 1024 * 1024,
			MaxConcurrentSends: 20,
			ShutdownTimeout:    60 * time.Second,
		}
		cfg.applyDefaults()

		if cfg.TrashRetention != 7*24*time.Hour {
			t.Errorf("TrashRetention = %v, want 7d", cfg.TrashRetention)
		}
		if cfg.MaxBodySize != 5*1024*1024 {
			t.Errorf("MaxBodySize = %v, want 5MB", cfg.MaxBodySize)
		}
		if cfg.MaxConcurrentSends != 20 {
			t.Errorf("MaxConcurrentSends = %v, want 20", cfg.MaxConcurrentSends)
		}
		if cfg.ShutdownTimeout != 60*time.Second {
			t.Errorf("ShutdownTimeout = %v, want 60s", cfg.ShutdownTimeout)
		}
	})

	t.Run("trash retention below minimum resets to default", func(t *testing.T) {
		cfg := Config{TrashRetention: 1 * time.Hour}
		cfg.applyDefaults()
		if cfg.TrashRetention != DefaultTrashRetention {
			t.Errorf("TrashRetention = %v, want %v", cfg.TrashRetention, DefaultTrashRetention)
		}
	})

	t.Run("shutdown timeout below minimum resets to default", func(t *testing.T) {
		cfg := Config{ShutdownTimeout: 500 * time.Millisecond}
		cfg.applyDefaults()
		if cfg.ShutdownTimeout != DefaultShutdownTimeout {
			t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, DefaultShutdownTimeout)
		}
	})

	t.Run("message retention below minimum resets to disabled", func(t *testing.T) {
		cfg := Config{MessageRetention: 1 * time.Hour}
		cfg.applyDefaults()
		if cfg.MessageRetention != 0 {
			t.Errorf("MessageRetention = %v, want 0 (disabled)", cfg.MessageRetention)
		}
	})

	t.Run("default query limit capped to max query limit", func(t *testing.T) {
		cfg := Config{MaxQueryLimit: 10, DefaultQueryLimit: 50}
		cfg.applyDefaults()
		if cfg.DefaultQueryLimit != 10 {
			t.Errorf("DefaultQueryLimit = %v, want 10 (capped to MaxQueryLimit)", cfg.DefaultQueryLimit)
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

func TestConfigGetLimits(t *testing.T) {
	cfg := Config{
		MaxBodySize:       1024,
		MaxAttachmentSize: 2048,
		MaxRecipientCount: 10,
	}
	cfg.applyDefaults()

	limits := cfg.getLimits()

	if limits.MaxBodySize != 1024 {
		t.Errorf("expected MaxBodySize 1024, got %d", limits.MaxBodySize)
	}
	if limits.MaxAttachmentSize != 2048 {
		t.Errorf("expected MaxAttachmentSize 2048, got %d", limits.MaxAttachmentSize)
	}
	if limits.MaxRecipientCount != 10 {
		t.Errorf("expected MaxRecipientCount 10, got %d", limits.MaxRecipientCount)
	}
	if limits.MaxSubjectLength != DefaultMaxSubjectLength {
		t.Errorf("expected default MaxSubjectLength, got %d", limits.MaxSubjectLength)
	}
}

func TestConfigGetLimits_Headers(t *testing.T) {
	cfg := Config{
		MaxHeaderCount:       10,
		MaxHeaderKeyLength:   64,
		MaxHeaderValueLength: 2048,
		MaxHeadersTotalSize:  16 * 1024,
	}
	cfg.applyDefaults()

	limits := cfg.getLimits()

	if limits.MaxHeaderCount != 10 {
		t.Errorf("expected MaxHeaderCount 10, got %d", limits.MaxHeaderCount)
	}
	if limits.MaxHeaderKeyLength != 64 {
		t.Errorf("expected MaxHeaderKeyLength 64, got %d", limits.MaxHeaderKeyLength)
	}
	if limits.MaxHeaderValueLength != 2048 {
		t.Errorf("expected MaxHeaderValueLength 2048, got %d", limits.MaxHeaderValueLength)
	}
	if limits.MaxHeadersTotalSize != 16*1024 {
		t.Errorf("expected MaxHeadersTotalSize 16384, got %d", limits.MaxHeadersTotalSize)
	}
}

// mockPlugin implements Plugin for testing
type mockPlugin struct{}

func (p *mockPlugin) Name() string                    { return "mock" }
func (p *mockPlugin) Init(ctx context.Context) error  { return nil }
func (p *mockPlugin) Close(ctx context.Context) error { return nil }
func (p *mockPlugin) Priority() int                   { return 0 }

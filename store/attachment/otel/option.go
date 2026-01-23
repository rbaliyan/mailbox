package otel

import (
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// options holds OTel instrumentation configuration.
type options struct {
	tracingEnabled bool
	metricsEnabled bool
	serviceName    string
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
}

// Option configures the OTel instrumented store.
type Option func(*options)

// WithTracing enables or disables tracing.
// Default is enabled.
func WithTracing(enabled bool) Option {
	return func(o *options) {
		o.tracingEnabled = enabled
	}
}

// WithMetrics enables or disables metrics.
// Default is enabled.
func WithMetrics(enabled bool) Option {
	return func(o *options) {
		o.metricsEnabled = enabled
	}
}

// WithServiceName sets the service name attribute for telemetry.
// Default is "mailbox".
func WithServiceName(name string) Option {
	return func(o *options) {
		if name != "" {
			o.serviceName = name
		}
	}
}

// WithTracerProvider sets a custom tracer provider.
// Default uses the global tracer provider from otel.GetTracerProvider().
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *options) {
		if tp != nil {
			o.tracerProvider = tp
		}
	}
}

// WithMeterProvider sets a custom meter provider.
// Default uses the global meter provider from otel.GetMeterProvider().
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(o *options) {
		if mp != nil {
			o.meterProvider = mp
		}
	}
}

// WithTracingOnly enables only tracing (disables metrics).
func WithTracingOnly() Option {
	return func(o *options) {
		o.tracingEnabled = true
		o.metricsEnabled = false
	}
}

// WithMetricsOnly enables only metrics (disables tracing).
func WithMetricsOnly() Option {
	return func(o *options) {
		o.tracingEnabled = false
		o.metricsEnabled = true
	}
}

// WithDisabled disables both tracing and metrics.
// Useful for testing or development environments.
func WithDisabled() Option {
	return func(o *options) {
		o.tracingEnabled = false
		o.metricsEnabled = false
	}
}

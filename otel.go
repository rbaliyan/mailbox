package mailbox

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const (
	instrumentationName = "github.com/rbaliyan/mailbox"
)

// otelInstrumentation holds OpenTelemetry instrumentation for the mailbox service.
type otelInstrumentation struct {
	enabled bool

	// Tracing
	tracingEnabled bool
	tracer         trace.Tracer

	// Metrics
	metricsEnabled bool

	// Message operations
	sendLatency   metric.Float64Histogram
	sendCount     metric.Int64Counter
	sendErrors    metric.Int64Counter
	getLatency    metric.Float64Histogram
	getCount      metric.Int64Counter
	getErrors     metric.Int64Counter
	listLatency   metric.Float64Histogram
	listCount     metric.Int64Counter
	listErrors    metric.Int64Counter
	searchLatency metric.Float64Histogram
	searchCount   metric.Int64Counter
	searchErrors  metric.Int64Counter

	// Message actions
	updateLatency metric.Float64Histogram
	updateCount   metric.Int64Counter
	updateErrors  metric.Int64Counter
	deleteLatency metric.Float64Histogram
	deleteCount   metric.Int64Counter
	deleteErrors  metric.Int64Counter
	moveLatency   metric.Float64Histogram
	moveCount     metric.Int64Counter
	moveErrors    metric.Int64Counter
}

// newOtelInstrumentation creates new OTel instrumentation from options.
func newOtelInstrumentation(opts *options) (*otelInstrumentation, error) {
	o := &otelInstrumentation{
		enabled:        opts.tracingEnabled || opts.metricsEnabled,
		tracingEnabled: opts.tracingEnabled,
		metricsEnabled: opts.metricsEnabled,
	}

	if !o.enabled {
		return o, nil
	}

	serviceName := opts.serviceName
	if serviceName == "" {
		serviceName = "mailbox"
	}

	// Initialize tracer
	if opts.tracingEnabled {
		tp := opts.tracerProvider
		if tp == nil {
			tp = otel.GetTracerProvider()
		}
		o.tracer = tp.Tracer(instrumentationName)
	}

	// Initialize metrics
	if opts.metricsEnabled {
		mp := opts.meterProvider
		if mp == nil {
			mp = otel.GetMeterProvider()
		}
		if err := o.initMetrics(mp); err != nil {
			return nil, err
		}
	}

	return o, nil
}

// initMetrics initializes all metric instruments.
func (o *otelInstrumentation) initMetrics(mp metric.MeterProvider) error {
	meter := mp.Meter(instrumentationName)

	var err error

	// Send metrics
	o.sendLatency, err = meter.Float64Histogram(
		"mailbox.send.duration",
		metric.WithDescription("Duration of send operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	o.sendCount, err = meter.Int64Counter(
		"mailbox.send.count",
		metric.WithDescription("Number of messages sent"),
	)
	if err != nil {
		return err
	}

	o.sendErrors, err = meter.Int64Counter(
		"mailbox.send.errors",
		metric.WithDescription("Number of send errors"),
	)
	if err != nil {
		return err
	}

	// Get metrics
	o.getLatency, err = meter.Float64Histogram(
		"mailbox.get.duration",
		metric.WithDescription("Duration of get operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	o.getCount, err = meter.Int64Counter(
		"mailbox.get.count",
		metric.WithDescription("Number of get operations"),
	)
	if err != nil {
		return err
	}

	o.getErrors, err = meter.Int64Counter(
		"mailbox.get.errors",
		metric.WithDescription("Number of get errors"),
	)
	if err != nil {
		return err
	}

	// List metrics
	o.listLatency, err = meter.Float64Histogram(
		"mailbox.list.duration",
		metric.WithDescription("Duration of list operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	o.listCount, err = meter.Int64Counter(
		"mailbox.list.count",
		metric.WithDescription("Number of list operations"),
	)
	if err != nil {
		return err
	}

	o.listErrors, err = meter.Int64Counter(
		"mailbox.list.errors",
		metric.WithDescription("Number of list errors"),
	)
	if err != nil {
		return err
	}

	// Search metrics
	o.searchLatency, err = meter.Float64Histogram(
		"mailbox.search.duration",
		metric.WithDescription("Duration of search operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	o.searchCount, err = meter.Int64Counter(
		"mailbox.search.count",
		metric.WithDescription("Number of search operations"),
	)
	if err != nil {
		return err
	}

	o.searchErrors, err = meter.Int64Counter(
		"mailbox.search.errors",
		metric.WithDescription("Number of search errors"),
	)
	if err != nil {
		return err
	}

	// Update metrics
	o.updateLatency, err = meter.Float64Histogram(
		"mailbox.update.duration",
		metric.WithDescription("Duration of update operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	o.updateCount, err = meter.Int64Counter(
		"mailbox.update.count",
		metric.WithDescription("Number of update operations"),
	)
	if err != nil {
		return err
	}

	o.updateErrors, err = meter.Int64Counter(
		"mailbox.update.errors",
		metric.WithDescription("Number of update errors"),
	)
	if err != nil {
		return err
	}

	// Delete metrics
	o.deleteLatency, err = meter.Float64Histogram(
		"mailbox.delete.duration",
		metric.WithDescription("Duration of delete operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	o.deleteCount, err = meter.Int64Counter(
		"mailbox.delete.count",
		metric.WithDescription("Number of delete operations"),
	)
	if err != nil {
		return err
	}

	o.deleteErrors, err = meter.Int64Counter(
		"mailbox.delete.errors",
		metric.WithDescription("Number of delete errors"),
	)
	if err != nil {
		return err
	}

	// Move metrics
	o.moveLatency, err = meter.Float64Histogram(
		"mailbox.move.duration",
		metric.WithDescription("Duration of move operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	o.moveCount, err = meter.Int64Counter(
		"mailbox.move.count",
		metric.WithDescription("Number of move operations"),
	)
	if err != nil {
		return err
	}

	o.moveErrors, err = meter.Int64Counter(
		"mailbox.move.errors",
		metric.WithDescription("Number of move errors"),
	)
	if err != nil {
		return err
	}

	return nil
}

// startSpan starts a new span if tracing is enabled.
// Returns the context and a boolean indicating if a span was started.
// Caller should call endSpan() with the returned span when done.
func (o *otelInstrumentation) startSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func(error)) {
	if !o.tracingEnabled || o.tracer == nil {
		return ctx, func(error) {}
	}
	ctx, span := o.tracer.Start(ctx, name,
		trace.WithAttributes(attrs...),
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}
}

// recordSend records send operation metrics.
func (o *otelInstrumentation) recordSend(ctx context.Context, duration time.Duration, recipientCount int, err error) {
	if !o.metricsEnabled {
		return
	}

	attrs := metric.WithAttributes(
		attribute.Int("recipient_count", recipientCount),
	)

	o.sendLatency.Record(ctx, duration.Seconds(), attrs)
	o.sendCount.Add(ctx, 1, attrs)
	if err != nil {
		o.sendErrors.Add(ctx, 1, attrs)
	}
}

// recordGet records get operation metrics.
func (o *otelInstrumentation) recordGet(ctx context.Context, duration time.Duration, err error) {
	if !o.metricsEnabled {
		return
	}

	o.getLatency.Record(ctx, duration.Seconds())
	o.getCount.Add(ctx, 1)
	if err != nil {
		o.getErrors.Add(ctx, 1)
	}
}

// recordList records list operation metrics.
func (o *otelInstrumentation) recordList(ctx context.Context, duration time.Duration, folder string, resultCount int, err error) {
	if !o.metricsEnabled {
		return
	}

	attrs := metric.WithAttributes(
		attribute.String("folder", folder),
		attribute.Int("result_count", resultCount),
	)

	o.listLatency.Record(ctx, duration.Seconds(), attrs)
	o.listCount.Add(ctx, 1, attrs)
	if err != nil {
		o.listErrors.Add(ctx, 1, attrs)
	}
}

// recordSearch records search operation metrics.
func (o *otelInstrumentation) recordSearch(ctx context.Context, duration time.Duration, resultCount int, err error) {
	if !o.metricsEnabled {
		return
	}

	attrs := metric.WithAttributes(
		attribute.Int("result_count", resultCount),
	)

	o.searchLatency.Record(ctx, duration.Seconds(), attrs)
	o.searchCount.Add(ctx, 1, attrs)
	if err != nil {
		o.searchErrors.Add(ctx, 1, attrs)
	}
}

// recordUpdate records update operation metrics.
func (o *otelInstrumentation) recordUpdate(ctx context.Context, duration time.Duration, operation string, err error) {
	if !o.metricsEnabled {
		return
	}

	attrs := metric.WithAttributes(
		attribute.String("operation", operation),
	)

	o.updateLatency.Record(ctx, duration.Seconds(), attrs)
	o.updateCount.Add(ctx, 1, attrs)
	if err != nil {
		o.updateErrors.Add(ctx, 1, attrs)
	}
}

// recordDelete records delete operation metrics.
func (o *otelInstrumentation) recordDelete(ctx context.Context, duration time.Duration, permanent bool, err error) {
	if !o.metricsEnabled {
		return
	}

	attrs := metric.WithAttributes(
		attribute.Bool("permanent", permanent),
	)

	o.deleteLatency.Record(ctx, duration.Seconds(), attrs)
	o.deleteCount.Add(ctx, 1, attrs)
	if err != nil {
		o.deleteErrors.Add(ctx, 1, attrs)
	}
}

// recordMove records move operation metrics.
func (o *otelInstrumentation) recordMove(ctx context.Context, duration time.Duration, toFolder string, err error) {
	if !o.metricsEnabled {
		return
	}

	attrs := metric.WithAttributes(
		attribute.String("to_folder", toFolder),
	)

	o.moveLatency.Record(ctx, duration.Seconds(), attrs)
	o.moveCount.Add(ctx, 1, attrs)
	if err != nil {
		o.moveErrors.Add(ctx, 1, attrs)
	}
}


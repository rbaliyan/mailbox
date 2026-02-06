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

// operationMetrics groups the latency, count, and error instruments for a single operation type.
type operationMetrics struct {
	latency metric.Float64Histogram
	count   metric.Int64Counter
	errors  metric.Int64Counter
}

// newOperationMetrics creates the standard instrument triplet for an operation.
func newOperationMetrics(meter metric.Meter, name, description string) (operationMetrics, error) {
	var om operationMetrics
	var err error

	om.latency, err = meter.Float64Histogram(
		"mailbox."+name+".duration",
		metric.WithDescription("Duration of "+description),
		metric.WithUnit("s"),
	)
	if err != nil {
		return om, err
	}

	om.count, err = meter.Int64Counter(
		"mailbox."+name+".count",
		metric.WithDescription("Number of "+description),
	)
	if err != nil {
		return om, err
	}

	om.errors, err = meter.Int64Counter(
		"mailbox."+name+".errors",
		metric.WithDescription("Number of "+name+" errors"),
	)
	if err != nil {
		return om, err
	}

	return om, nil
}

// record records a single operation's duration, count, and optional error.
func (om *operationMetrics) record(ctx context.Context, duration time.Duration, err error, attrs ...attribute.KeyValue) {
	opt := metric.WithAttributes(attrs...)
	om.latency.Record(ctx, duration.Seconds(), opt)
	om.count.Add(ctx, 1, opt)
	if err != nil {
		om.errors.Add(ctx, 1, opt)
	}
}

// otelInstrumentation holds OpenTelemetry instrumentation for the mailbox service.
type otelInstrumentation struct {
	enabled bool

	// Tracing
	tracingEnabled bool
	tracer         trace.Tracer

	// Metrics
	metricsEnabled bool
	send           operationMetrics
	get            operationMetrics
	list           operationMetrics
	search         operationMetrics
	update         operationMetrics
	del            operationMetrics
	move           operationMetrics
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
	for _, entry := range []struct {
		target *operationMetrics
		name   string
		desc   string
	}{
		{&o.send, "send", "send operations"},
		{&o.get, "get", "get operations"},
		{&o.list, "list", "list operations"},
		{&o.search, "search", "search operations"},
		{&o.update, "update", "update operations"},
		{&o.del, "delete", "delete operations"},
		{&o.move, "move", "move operations"},
	} {
		*entry.target, err = newOperationMetrics(meter, entry.name, entry.desc)
		if err != nil {
			return err
		}
	}

	return nil
}

// startSpan starts a new span if tracing is enabled.
// Returns the context and a function to end the span.
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
	o.send.record(ctx, duration, err,
		attribute.Int("recipient_count", recipientCount),
	)
}

// recordGet records get operation metrics.
func (o *otelInstrumentation) recordGet(ctx context.Context, duration time.Duration, err error) {
	if !o.metricsEnabled {
		return
	}
	o.get.record(ctx, duration, err)
}

// recordList records list operation metrics.
func (o *otelInstrumentation) recordList(ctx context.Context, duration time.Duration, folder string, resultCount int, err error) {
	if !o.metricsEnabled {
		return
	}
	o.list.record(ctx, duration, err,
		attribute.String("folder", folder),
		attribute.Int("result_count", resultCount),
	)
}

// recordSearch records search operation metrics.
func (o *otelInstrumentation) recordSearch(ctx context.Context, duration time.Duration, resultCount int, err error) {
	if !o.metricsEnabled {
		return
	}
	o.search.record(ctx, duration, err,
		attribute.Int("result_count", resultCount),
	)
}

// recordUpdate records update operation metrics.
func (o *otelInstrumentation) recordUpdate(ctx context.Context, duration time.Duration, operation string, err error) {
	if !o.metricsEnabled {
		return
	}
	o.update.record(ctx, duration, err,
		attribute.String("operation", operation),
	)
}

// recordDelete records delete operation metrics.
func (o *otelInstrumentation) recordDelete(ctx context.Context, duration time.Duration, permanent bool, err error) {
	if !o.metricsEnabled {
		return
	}
	o.del.record(ctx, duration, err,
		attribute.Bool("permanent", permanent),
	)
}

// recordMove records move operation metrics.
func (o *otelInstrumentation) recordMove(ctx context.Context, duration time.Duration, toFolder string, err error) {
	if !o.metricsEnabled {
		return
	}
	o.move.record(ctx, duration, err,
		attribute.String("to_folder", toFolder),
	)
}

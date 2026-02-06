// Package otel provides OpenTelemetry instrumentation for attachment stores.
package otel

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const (
	instrumentationName = "github.com/rbaliyan/mailbox/store/attachment/otel"
)

// Store wraps an AttachmentFileStore with OpenTelemetry instrumentation.
type Store struct {
	backend store.AttachmentFileStore
	opts    *options

	// Tracing
	tracer trace.Tracer

	// Metrics
	uploadLatency metric.Float64Histogram
	uploadCount   metric.Int64Counter
	uploadBytes   metric.Int64Counter
	uploadErrors  metric.Int64Counter
	loadLatency   metric.Float64Histogram
	loadCount     metric.Int64Counter
	loadBytes     metric.Int64Counter
	loadErrors    metric.Int64Counter
	deleteLatency metric.Float64Histogram
	deleteCount   metric.Int64Counter
	deleteErrors  metric.Int64Counter
}

// Ensure Store implements AttachmentFileStore.
var _ store.AttachmentFileStore = (*Store)(nil)

// New creates a new OTel-instrumented attachment store wrapping the given backend.
func New(backend store.AttachmentFileStore, opts ...Option) (*Store, error) {
	o := &options{
		tracingEnabled: true,
		metricsEnabled: true,
		serviceName:    "mailbox",
		tracerProvider: otel.GetTracerProvider(),
		meterProvider:  otel.GetMeterProvider(),
	}
	for _, opt := range opts {
		opt(o)
	}

	s := &Store{
		backend: backend,
		opts:    o,
	}

	// Initialize tracer
	if o.tracingEnabled {
		s.tracer = o.tracerProvider.Tracer(instrumentationName)
	}

	// Initialize metrics
	if o.metricsEnabled {
		if err := s.initMetrics(o.meterProvider); err != nil {
			return nil, fmt.Errorf("init metrics: %w", err)
		}
	}

	return s, nil
}

// initMetrics initializes all metric instruments.
func (s *Store) initMetrics(mp metric.MeterProvider) error {
	meter := mp.Meter(instrumentationName)

	var err error

	// Upload metrics
	s.uploadLatency, err = meter.Float64Histogram(
		"attachment.upload.duration",
		metric.WithDescription("Duration of attachment upload operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	s.uploadCount, err = meter.Int64Counter(
		"attachment.upload.count",
		metric.WithDescription("Number of attachment upload operations"),
	)
	if err != nil {
		return err
	}

	s.uploadBytes, err = meter.Int64Counter(
		"attachment.upload.bytes",
		metric.WithDescription("Total bytes uploaded"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return err
	}

	s.uploadErrors, err = meter.Int64Counter(
		"attachment.upload.errors",
		metric.WithDescription("Number of upload errors"),
	)
	if err != nil {
		return err
	}

	// Load metrics
	s.loadLatency, err = meter.Float64Histogram(
		"attachment.load.duration",
		metric.WithDescription("Duration of attachment load operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	s.loadCount, err = meter.Int64Counter(
		"attachment.load.count",
		metric.WithDescription("Number of attachment load operations"),
	)
	if err != nil {
		return err
	}

	s.loadBytes, err = meter.Int64Counter(
		"attachment.load.bytes",
		metric.WithDescription("Total bytes loaded"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return err
	}

	s.loadErrors, err = meter.Int64Counter(
		"attachment.load.errors",
		metric.WithDescription("Number of load errors"),
	)
	if err != nil {
		return err
	}

	// Delete metrics
	s.deleteLatency, err = meter.Float64Histogram(
		"attachment.delete.duration",
		metric.WithDescription("Duration of attachment delete operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	s.deleteCount, err = meter.Int64Counter(
		"attachment.delete.count",
		metric.WithDescription("Number of attachment delete operations"),
	)
	if err != nil {
		return err
	}

	s.deleteErrors, err = meter.Int64Counter(
		"attachment.delete.errors",
		metric.WithDescription("Number of delete errors"),
	)
	if err != nil {
		return err
	}

	return nil
}

// Upload uploads content with tracing and metrics.
func (s *Store) Upload(ctx context.Context, filename, contentType string, content io.Reader) (string, error) {
	attrs := []attribute.KeyValue{
		attribute.String("attachment.filename", filename),
		attribute.String("attachment.content_type", contentType),
		attribute.String("service.name", s.opts.serviceName),
	}

	// Start span if tracing is enabled
	if s.opts.tracingEnabled && s.tracer != nil {
		var span trace.Span
		ctx, span = s.tracer.Start(ctx, "attachment.upload",
			trace.WithAttributes(attrs...),
			trace.WithSpanKind(trace.SpanKindClient),
		)
		defer span.End()
	}

	start := time.Now()

	// Wrap reader to count bytes
	countingReader := &countingReader{reader: content}

	uri, err := s.backend.Upload(ctx, filename, contentType, countingReader)

	duration := time.Since(start).Seconds()

	// Record metrics
	if s.opts.metricsEnabled {
		metricAttrs := metric.WithAttributes(attrs...)
		s.uploadLatency.Record(ctx, duration, metricAttrs)
		s.uploadCount.Add(ctx, 1, metricAttrs)
		s.uploadBytes.Add(ctx, countingReader.bytes, metricAttrs)

		if err != nil {
			s.uploadErrors.Add(ctx, 1, metricAttrs)
		}
	}

	// Record span status
	if s.opts.tracingEnabled && s.tracer != nil {
		span := trace.SpanFromContext(ctx)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetAttributes(
				attribute.String("attachment.uri", uri),
				attribute.Int64("attachment.bytes", countingReader.bytes),
			)
			span.SetStatus(codes.Ok, "")
		}
	}

	return uri, err
}

// Load returns a reader for the attachment content with tracing and metrics.
func (s *Store) Load(ctx context.Context, uri string) (io.ReadCloser, error) {
	attrs := []attribute.KeyValue{
		attribute.String("attachment.uri", uri),
		attribute.String("service.name", s.opts.serviceName),
	}

	// Start span if tracing is enabled
	var span trace.Span
	if s.opts.tracingEnabled && s.tracer != nil {
		ctx, span = s.tracer.Start(ctx, "attachment.load",
			trace.WithAttributes(attrs...),
			trace.WithSpanKind(trace.SpanKindClient),
		)
		// Note: span.End() is called when the reader is closed
	}

	start := time.Now()

	reader, err := s.backend.Load(ctx, uri)

	duration := time.Since(start).Seconds()

	// Record metrics for the initial request
	if s.opts.metricsEnabled {
		metricAttrs := metric.WithAttributes(attrs...)
		s.loadLatency.Record(ctx, duration, metricAttrs)
		s.loadCount.Add(ctx, 1, metricAttrs)

		if err != nil {
			s.loadErrors.Add(ctx, 1, metricAttrs)
		}
	}

	if err != nil {
		if span != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			span.End()
		}
		return nil, err
	}

	// Wrap reader to track bytes and end span on close
	return &instrumentedReader{
		reader: reader,
		span:   span,
		store:  s,
		ctx:    ctx,
		attrs:  attrs,
	}, nil
}

// Delete removes the attachment with tracing and metrics.
func (s *Store) Delete(ctx context.Context, uri string) error {
	attrs := []attribute.KeyValue{
		attribute.String("attachment.uri", uri),
		attribute.String("service.name", s.opts.serviceName),
	}

	// Start span if tracing is enabled
	if s.opts.tracingEnabled && s.tracer != nil {
		var span trace.Span
		ctx, span = s.tracer.Start(ctx, "attachment.delete",
			trace.WithAttributes(attrs...),
			trace.WithSpanKind(trace.SpanKindClient),
		)
		defer span.End()
	}

	start := time.Now()

	err := s.backend.Delete(ctx, uri)

	duration := time.Since(start).Seconds()

	// Record metrics
	if s.opts.metricsEnabled {
		metricAttrs := metric.WithAttributes(attrs...)
		s.deleteLatency.Record(ctx, duration, metricAttrs)
		s.deleteCount.Add(ctx, 1, metricAttrs)

		if err != nil {
			s.deleteErrors.Add(ctx, 1, metricAttrs)
		}
	}

	// Record span status
	if s.opts.tracingEnabled && s.tracer != nil {
		span := trace.SpanFromContext(ctx)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
	}

	return err
}

// countingReader wraps an io.Reader and counts bytes read.
type countingReader struct {
	reader io.Reader
	bytes  int64
}

func (r *countingReader) Read(p []byte) (n int, err error) {
	n, err = r.reader.Read(p)
	r.bytes += int64(n)
	return n, err
}

// instrumentedReader wraps an io.ReadCloser with instrumentation.
type instrumentedReader struct {
	reader io.ReadCloser
	span   trace.Span
	store  *Store
	ctx    context.Context
	attrs  []attribute.KeyValue
	bytes  int64
	closed bool
}

func (r *instrumentedReader) Read(p []byte) (n int, err error) {
	n, err = r.reader.Read(p)
	r.bytes += int64(n)
	return n, err
}

func (r *instrumentedReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true

	err := r.reader.Close()

	// Record bytes loaded
	if r.store.opts.metricsEnabled {
		r.store.loadBytes.Add(r.ctx, r.bytes, metric.WithAttributes(r.attrs...))
	}

	// End span
	if r.span != nil {
		r.span.SetAttributes(attribute.Int64("attachment.bytes", r.bytes))
		if err != nil {
			r.span.RecordError(err)
			r.span.SetStatus(codes.Error, err.Error())
		} else {
			r.span.SetStatus(codes.Ok, "")
		}
		r.span.End()
	}

	return err
}

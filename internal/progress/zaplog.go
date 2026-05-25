package progress

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ZapReporter logs every Report call as a zap INFO line. Used in
// non-TTY environments (the daemon, CI) where the Spinner is
// silent so progress is invisible. Stage transitions get logged
// immediately; intra-stage progress (current/total) gets logged on
// transition AND every progressInterval seconds so a slow stage
// emits a heartbeat instead of going quiet.
type ZapReporter struct {
	logger   *zap.Logger
	prefix   string
	interval time.Duration

	mu          sync.Mutex
	lastStage   string
	stageStart  time.Time
	lastEmitted time.Time
	lastCur     int
	lastTotal   int
}

// NewZapReporter creates a reporter that logs to the given logger.
// prefix is added to every log line ("indexer", "multi-repo", …).
// interval is the heartbeat cadence for intra-stage progress
// (0 disables heartbeats — only stage transitions log).
func NewZapReporter(logger *zap.Logger, prefix string, interval time.Duration) *ZapReporter {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ZapReporter{
		logger:   logger,
		prefix:   prefix,
		interval: interval,
	}
}

// Report records a stage advancement. Always logs on a stage
// transition; logs intra-stage updates at most once per interval.
func (r *ZapReporter) Report(stage string, cur, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if stage != r.lastStage {
		if r.lastStage != "" {
			r.logger.Info(r.prefix+": stage end",
				zap.String("stage", r.lastStage),
				zap.Duration("elapsed", now.Sub(r.stageStart)),
			)
		}
		r.lastStage = stage
		r.stageStart = now
		r.lastEmitted = now
		r.lastCur = cur
		r.lastTotal = total
		r.logger.Info(r.prefix+": stage start",
			zap.String("stage", stage),
			zap.Int("current", cur),
			zap.Int("total", total),
		)
		return
	}
	// Same stage — heartbeat at most once per interval.
	if r.interval > 0 && now.Sub(r.lastEmitted) < r.interval {
		return
	}
	r.lastEmitted = now
	r.lastCur = cur
	r.lastTotal = total
	r.logger.Info(r.prefix+": stage progress",
		zap.String("stage", stage),
		zap.Int("current", cur),
		zap.Int("total", total),
		zap.Duration("elapsed", now.Sub(r.stageStart)),
	)
}

// StartHeartbeat runs a goroutine that logs an "alive" line every
// interval until the context is done. Useful when the indexer is
// inside a long-running phase that doesn't call Report itself
// (e.g. ladybug's per-row Cypher writes during a slow drain).
func StartHeartbeat(ctx context.Context, logger *zap.Logger, prefix string, interval time.Duration, snapshot func() map[string]any) {
	if logger == nil || interval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		start := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				fields := []zap.Field{
					zap.Duration("elapsed", time.Since(start)),
				}
				if snapshot != nil {
					for k, v := range snapshot() {
						fields = append(fields, zap.Any(k, v))
					}
				}
				logger.Info(prefix+": heartbeat", fields...)
			}
		}
	}()
}

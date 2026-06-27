package indexer

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// Cold-index GC tuning.
//
// A full (cold) index allocates hard: every parsed file produces nodes,
// edges, and tree-sitter C scratch that churns the Go heap. The default GC
// pacing collects often and keeps RSS low, which is the wrong trade during
// this one-shot burst — we would rather run fewer, larger GC cycles and let
// RSS climb toward a sane ceiling. These knobs are installed for the duration
// of IndexCtx and restored on exit, so they never leak into the long-running
// daemon's steady state.
//
// The knobs are GC-timing only: they change when collection happens and how
// high RSS is allowed to climb, never what the indexer produces — node and
// edge counts are identical with tuning on or off. Set GORTEX_INDEX_GC_TUNE=0
// to skip the tuning entirely (for A/B measurement against the untuned run).

const (
	// defaultIndexGCPercent raises the GC percent window during a cold index
	// so collection runs less often (fewer, larger cycles) than the runtime
	// default of 100. Override via GORTEX_INDEX_GC_PERCENT.
	defaultIndexGCPercent = 300

	// budgetDivisor halves the available-memory figure to derive the soft
	// memory limit. Half leaves headroom for the off-heap working set the Go
	// memory limit does not account for — tree-sitter C allocations and the
	// disk backend's buffers — so the process trends toward the budget without
	// the limit forcing a GC death-spiral.
	budgetDivisor = 2

	// minIndexMemoryBudget is the floor below which a soft limit would force
	// near-constant GC and hurt more than it helps; below it we skip the
	// memory-limit knob (GC percent still applies).
	minIndexMemoryBudget = 512 << 20 // 512 MiB

	// maxPlausibleMemoryBytes bounds a sane physical-memory / cgroup figure.
	// cgroup v1 reports a near-int64-max sentinel (~9.2e18) when uncapped;
	// anything above this ceiling is treated as "unset" rather than a real
	// limit.
	maxPlausibleMemoryBytes = 1 << 50 // 1 PiB

	// maxPlausibleCPUCores bounds a sane cgroup CPU-quota core count: a quota
	// far larger than any real machine signals a malformed file and is treated
	// as "unset" (no clamp) rather than a real allotment.
	maxPlausibleCPUCores = 1 << 16 // 65536
)

// gcTuneEnabled reports whether cold-index GC tuning is active. On by default;
// GORTEX_INDEX_GC_TUNE=0 (or "false") disables it so a run can be A/B-compared
// against the untuned baseline.
func gcTuneEnabled() bool {
	v := os.Getenv("GORTEX_INDEX_GC_TUNE")
	if v == "" {
		return true
	}
	return v != "0" && !strings.EqualFold(v, "false")
}

// indexGCPercent returns the GC percent target for the cold-index window.
// GORTEX_INDEX_GC_PERCENT overrides the default; non-numeric or non-positive
// values fall back to the default (a value <= 0 is rejected — disabling GC
// outright is never the right call for a long-lived daemon).
func indexGCPercent() int {
	if v := os.Getenv("GORTEX_INDEX_GC_PERCENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultIndexGCPercent
}

// indexMemoryBudget computes the soft memory-limit budget (in bytes) for the
// cold-index window from the host's physical RAM and an optional cgroup memory
// limit (0 = no cgroup limit detected). The cgroup limit wins when it is a
// real cap below host RAM (a container's ceiling is the true budget);
// otherwise the host figure is used. Returns 0 when no sane budget can be
// derived — the caller then leaves the runtime memory limit untouched.
//
// Pure and deterministic: every input is a parameter, so it is exhaustively
// testable without touching the real host.
func indexMemoryBudget(hostRAM, cgroupLimit uint64) int64 {
	effective := hostRAM
	if cgroupLimit > 0 && cgroupLimit <= maxPlausibleMemoryBytes {
		if effective == 0 || cgroupLimit < effective {
			effective = cgroupLimit
		}
	}
	if effective == 0 || effective > maxPlausibleMemoryBytes {
		return 0
	}
	budget := int64(effective / budgetDivisor)
	if budget < minIndexMemoryBudget {
		return 0
	}
	return budget
}

// cgroupMemoryLimit returns the active cgroup memory ceiling in bytes, or 0
// when the process is not under a cgroup memory limit (or detection fails).
// cgroup v2 (`memory.max`) is consulted first, then v1
// (`memory/memory.limit_in_bytes`). Missing files, the literal "max", and
// implausible sentinels all degrade to 0. Linux-only in practice; on other
// platforms the files are absent and this returns 0.
func cgroupMemoryLimit() uint64 {
	return cgroupMemoryLimitFrom(os.ReadFile)
}

// cgroupMemoryLimitFrom is cgroupMemoryLimit with an injectable file reader,
// so the cgroup-detection logic is testable without a real cgroup hierarchy.
func cgroupMemoryLimitFrom(readFile func(string) ([]byte, error)) uint64 {
	for _, path := range []string{
		"/sys/fs/cgroup/memory.max",                   // cgroup v2 unified hierarchy
		"/sys/fs/cgroup/memory/memory.limit_in_bytes", // cgroup v1
	} {
		if v, ok := parseCgroupMemoryLimit(readFile, path); ok {
			return v
		}
	}
	return 0
}

// parseCgroupMemoryLimit reads and parses one cgroup memory-limit file. It
// reports ok=false for a missing/empty file, the literal "max", a zero value,
// a non-numeric body, or an implausibly large sentinel (cgroup v1 reports a
// near-int64-max value when uncapped).
func parseCgroupMemoryLimit(readFile func(string) ([]byte, error), path string) (uint64, bool) {
	b, err := readFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(b))
	if s == "" || s == "max" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n == 0 || n > maxPlausibleMemoryBytes {
		return 0, false
	}
	return n, true
}

// Cold-index worker clamp.
//
// idx.config.Workers defaults to the host's runtime.NumCPU() and sizes the
// parse worker pool. In a CPU-limited container the host core count exceeds the
// allotted cgroup CPU quota, so the pool over-subscribes and the CFS scheduler
// throttles it — fewer, larger time slices and worse throughput than sizing the
// pool to the real quota. When a finite quota is present the effective worker
// count is clamped down to it. The clamp only ever LOWERS the count (never
// raises it) and never drops below 1; it changes scheduling pressure only, not
// what the indexer produces — node and edge counts are identical whether the
// clamp is active or not. Set GORTEX_INDEX_CPU_CLAMP=0 to skip it.

// cpuClampEnabled reports whether the cold-index worker pool is clamped to the
// cgroup CPU quota. On by default; GORTEX_INDEX_CPU_CLAMP=0 (or "false")
// disables it so a run can be A/B-compared against the unclamped baseline.
func cpuClampEnabled() bool {
	v := os.Getenv("GORTEX_INDEX_CPU_CLAMP")
	if v == "" {
		return true
	}
	return v != "0" && !strings.EqualFold(v, "false")
}

// cgroupCPUQuota returns the active cgroup CPU quota as an integer
// core-equivalent (at least 1), or 0 when the process is not under a finite CPU
// quota (or detection fails). cgroup v2 (`cpu.max`) is consulted first, then v1
// (`cpu/cpu.cfs_quota_us` + `cpu/cpu.cfs_period_us`). An unlimited quota ("max"
// / -1), missing files, and unparsable bodies all degrade to 0 — no clamp.
// Linux-only in practice; on other platforms the files are absent and this
// returns 0.
func cgroupCPUQuota() int {
	return cgroupCPUQuotaFrom(os.ReadFile)
}

// cgroupCPUQuotaFrom is cgroupCPUQuota with an injectable file reader, so the
// cgroup-detection logic is testable without a real cgroup hierarchy.
func cgroupCPUQuotaFrom(readFile func(string) ([]byte, error)) int {
	if cores, ok := parseCgroupCPUMaxV2(readFile, "/sys/fs/cgroup/cpu.max"); ok {
		return cores
	}
	if cores, ok := parseCgroupCPUQuotaV1(readFile,
		"/sys/fs/cgroup/cpu/cpu.cfs_quota_us",
		"/sys/fs/cgroup/cpu/cpu.cfs_period_us"); ok {
		return cores
	}
	return 0
}

// parseCgroupCPUMaxV2 reads a cgroup v2 `cpu.max` file, whose body is
// "<quota> <period>" (microseconds) or "max <period>" when unlimited. Reports
// ok=false for a missing/empty/malformed file, the literal "max" quota
// (unlimited), or a non-positive quota/period.
func parseCgroupCPUMaxV2(readFile func(string) ([]byte, error), path string) (int, bool) {
	b, err := readFile(path)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) != 2 || fields[0] == "max" {
		return 0, false // missing/malformed, or unlimited
	}
	quota, qerr := strconv.ParseInt(fields[0], 10, 64)
	period, perr := strconv.ParseInt(fields[1], 10, 64)
	if qerr != nil || perr != nil {
		return 0, false
	}
	return cpuQuotaCores(quota, period)
}

// parseCgroupCPUQuotaV1 reads the cgroup v1 `cpu.cfs_quota_us` and
// `cpu.cfs_period_us` files (microseconds). A quota of -1 means unlimited.
// Reports ok=false for missing/malformed files, an unlimited (non-positive)
// quota, or a non-positive period.
func parseCgroupCPUQuotaV1(readFile func(string) ([]byte, error), quotaPath, periodPath string) (int, bool) {
	qb, err := readFile(quotaPath)
	if err != nil {
		return 0, false
	}
	quota, err := strconv.ParseInt(strings.TrimSpace(string(qb)), 10, 64)
	if err != nil || quota <= 0 {
		return 0, false // -1 (or 0) means unlimited / unset
	}
	pb, err := readFile(periodPath)
	if err != nil {
		return 0, false
	}
	period, err := strconv.ParseInt(strings.TrimSpace(string(pb)), 10, 64)
	if err != nil {
		return 0, false
	}
	return cpuQuotaCores(quota, period)
}

// cpuQuotaCores converts a (quota, period) microsecond pair into an integer
// core count. It rounds UP — ceil(quota/period) — so a fractional allotment
// like 1.5 cores sizes the pool to 2 rather than starving it at 1; the result
// is floored at 1. Reports ok=false when quota or period is non-positive (the
// period guard also rules out divide-by-zero) or the rounded count exceeds a
// sane ceiling (a malformed file masquerading as an enormous quota).
func cpuQuotaCores(quota, period int64) (int, bool) {
	if quota <= 0 || period <= 0 {
		return 0, false
	}
	cores := (quota + period - 1) / period // ceil(quota/period)
	if cores < 1 {
		cores = 1
	}
	if cores > maxPlausibleCPUCores {
		return 0, false
	}
	return int(cores), true
}

// clampWorkersToCPUQuota returns the effective parse-worker count after
// clamping `configured` down to the cgroup CPU quota `quotaCores` (an integer
// core count, 0 when no finite quota was detected). The clamp applies to the
// effective value regardless of whether Workers came from the runtime.NumCPU()
// default or an explicit config override — both over-subscribe a quota and
// invite CFS throttling — but it only ever LOWERS the count, never raises it,
// and never drops below 1. quotaCores<=0 leaves `configured` unchanged, so a
// non-limited host behaves exactly as before.
func clampWorkersToCPUQuota(configured, quotaCores int) int {
	if configured < 1 {
		configured = 1
	}
	if quotaCores > 0 && quotaCores < configured {
		return quotaCores
	}
	return configured
}

// gcTune state guards the process-global GC knobs across concurrent index
// calls. Multi-repo warmup runs IndexCtx in parallel goroutines, so the knobs
// are reference-counted: the first concurrent applier captures the prior
// settings and installs the tuned ones; the last to finish restores them.
var (
	gcTuneMu      sync.Mutex
	gcTuneDepth   int
	gcTunePrevPct int
	gcTunePrevLim int64
)

// applyIndexGCTuning installs the cold-index GC knobs and returns a closure
// that restores the prior settings. Defer the returned closure; it reverts at
// most once. Returns a no-op closure when tuning is disabled.
//
// Because debug.SetGCPercent / debug.SetMemoryLimit are process-global and
// IndexCtx can run concurrently across repos, the knobs are reference-counted:
// only the first concurrent caller mutates the runtime, and only the last
// restore reverts it — so a sibling index can't clobber another's restore.
func applyIndexGCTuning(logger *zap.Logger) func() {
	if !gcTuneEnabled() {
		return func() {}
	}

	gcPct := indexGCPercent()
	budget := indexMemoryBudget(hostPhysicalMemory(), cgroupMemoryLimit())

	gcTuneMu.Lock()
	if gcTuneDepth == 0 {
		// SetGCPercent returns the prior percent; SetMemoryLimit(-1) reads the
		// current limit without changing it. Capture both before mutating so
		// the restore is exact.
		gcTunePrevPct = debug.SetGCPercent(gcPct)
		gcTunePrevLim = debug.SetMemoryLimit(-1)
		if budget > 0 {
			debug.SetMemoryLimit(budget)
		}
		if logger != nil {
			logger.Debug("indexer: cold-index GC tuning applied",
				zap.Int("gc_percent", gcPct),
				zap.Int("prev_gc_percent", gcTunePrevPct),
				zap.Int64("mem_limit_bytes", budget),
				zap.Int64("prev_mem_limit_bytes", gcTunePrevLim),
			)
		}
	}
	gcTuneDepth++
	gcTuneMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			gcTuneMu.Lock()
			gcTuneDepth--
			if gcTuneDepth == 0 {
				debug.SetGCPercent(gcTunePrevPct)
				debug.SetMemoryLimit(gcTunePrevLim)
			}
			gcTuneMu.Unlock()
		})
	}
}

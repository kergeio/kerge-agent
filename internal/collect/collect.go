// Package collect samples the host metrics with gopsutil. Every category
// is collected under its own timeout so that one stuck collector,
// typically the disk on an unreachable network mount, never delays or
// blocks the rest of the sample.
package collect

import (
	"context"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kergeio/kerge-protocol"
	"github.com/kergeio/kerge-protocol/ifacefilter"
)

// DefaultTimeout is the per-category collection timeout.
const DefaultTimeout = 2 * time.Second

// Options configure a Collector.
type Options struct {
	// Interval is the sampling interval used by Run and reported in
	// host_info. It must be within the range the configuration accepts.
	Interval time.Duration
	// IfaceExclude holds the wildcard patterns of interfaces not to report.
	IfaceExclude []string
	// Version is the agent version reported in host_info.
	Version string
	// Timeout is the per-category timeout; zero means DefaultTimeout.
	Timeout time.Duration
	// Logger receives collection failures; zero means slog.Default().
	Logger *slog.Logger
}

// Collector produces metrics samples. It is safe for concurrent use, though
// the agent samples from a single goroutine.
type Collector struct {
	opts    Options
	now     func() time.Time
	started time.Time

	cpu    *probe[cpuTimes]
	mem    *probe[memStat]
	swap   *probe[swapStat]
	disk   *probe[diskStat]
	net    *probe[[]netStat]
	load   *probe[loadStat]
	uptime *probe[uint64]
	host   *probe[hostStat]

	mu      sync.Mutex
	prevCPU *cpuTimes
	// capped records that the interface count was capped, so the warning
	// is logged once rather than every cycle.
	capped bool
}

// New returns a Collector reading from the host.
func New(opts Options) *Collector {
	return newWith(opts, defaultSources(), time.Now)
}

func newWith(opts Options, src sources, now func() time.Time) *Collector {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	c := &Collector{opts: opts, now: now, started: now()}
	c.cpu = probeFor(c, "cpu", src.cpuTimes)
	c.mem = probeFor(c, "mem", src.mem)
	c.swap = probeFor(c, "swap", src.swap)
	c.disk = probeFor(c, "disk", src.disk)
	c.net = probeFor(c, "net", src.net)
	c.load = probeFor(c, "load", src.load)
	c.uptime = probeFor(c, "uptime", src.uptime)
	c.host = probeFor(c, "host", src.host)
	return c
}

func probeFor[T any](c *Collector, name string, fn func(context.Context) (T, error)) *probe[T] {
	return &probe[T]{name: name, fn: fn, timeout: c.opts.Timeout, log: c.opts.Logger, now: c.now}
}

// Run samples every Interval and hands each sample to fn. A tick that
// arrives while the previous sample is still being taken is dropped rather
// than queued, so the agent never tries to catch up.
func (c *Collector) Run(ctx context.Context, fn func(*protocol.Metrics)) {
	t := time.NewTicker(c.opts.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(c.Metrics(ctx))
		}
	}
}

// Metrics takes one sample. Categories are collected concurrently and a
// category that fails is simply absent from the result.
func (c *Collector) Metrics(ctx context.Context) *protocol.Metrics {
	now := c.now()
	m := &protocol.Metrics{
		Type:   protocol.TypeMetrics,
		TS:     now.Unix(),
		MonoMS: now.Sub(c.started).Milliseconds(),
	}

	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}

	run(func() {
		if t, ok := c.cpu.get(ctx); ok {
			if pct, ok := c.cpuPercent(t); ok {
				m.CPUPercent = &pct
			}
		}
	})
	run(func() {
		if v, ok := c.mem.get(ctx); ok {
			total, used := v.Total, uint64(0)
			if v.Available < total {
				used = total - v.Available
			}
			m.MemTotal, m.MemUsed = &total, &used
		}
	})
	run(func() {
		if v, ok := c.swap.get(ctx); ok {
			total, used := v.Total, min(v.Used, v.Total)
			m.SwapTotal, m.SwapUsed = &total, &used
		}
	})
	run(func() {
		if v, ok := c.disk.get(ctx); ok {
			total, used, free := v.Total, min(v.Used, v.Total), min(v.Free, v.Total)
			m.DiskTotal, m.DiskUsed, m.DiskFree = &total, &used, &free
		}
	})
	run(func() {
		if v, ok := c.net.get(ctx); ok {
			m.Net = c.filterNet(v)
		}
	})
	if loadSupported {
		run(func() {
			if v, ok := c.load.get(ctx); ok {
				l1, l5, l15 := round2(max(v.Load1, 0)), round2(max(v.Load5, 0)), round2(max(v.Load15, 0))
				m.Load1, m.Load5, m.Load15 = &l1, &l5, &l15
			}
		})
	}
	run(func() {
		if v, ok := c.uptime.get(ctx); ok {
			m.Uptime = &v
		}
	})
	wg.Wait()
	return m
}

// HostInfo collects the host description. The agent sends it once per
// connection; fields whose collection failed are empty.
func (c *Collector) HostInfo(ctx context.Context) *protocol.HostInfo {
	info := &protocol.HostInfo{
		Type:         protocol.TypeHostInfo,
		AgentVersion: c.opts.Version,
		IntervalMS:   int(c.opts.Interval.Milliseconds()),
	}
	if v, ok := c.host.get(ctx); ok {
		info.Hostname = v.Hostname
		info.OS = v.OS
		info.Platform = v.Platform
		info.PlatformVersion = v.PlatformVersion
		info.Kernel = v.Kernel
		info.Arch = v.Arch
		info.CPUModel = v.CPUModel
		info.CPUCores = v.CPUCores
	}
	info.Normalize()
	return info
}

// cpuPercent turns two consecutive counter readings into a percentage. The
// first sample of a run has nothing to compare against and is left out.
func (c *Collector) cpuPercent(t cpuTimes) (float64, bool) {
	c.mu.Lock()
	prev := c.prevCPU
	c.prevCPU = &t
	c.mu.Unlock()
	if prev == nil {
		return 0, false
	}
	prevTotal, prevBusy := prev.split()
	total, busy := t.split()
	dTotal := total - prevTotal
	if dTotal <= 0 {
		// The counters did not advance, or went backwards after a CPU was
		// hot-plugged. Report nothing rather than a made-up number.
		return 0, false
	}
	return round2(min(max((busy-prevBusy)/dTotal*100, 0), 100)), true
}

// round2 keeps two decimals. Full float precision would add about fifty
// bytes to every message without changing anything the panel displays, and
// at a 5-second interval the agent should send no more than about 10 MB a
// day.
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// split returns the total and the busy time. Guest and guest_nice are left
// out of the total because Linux already counts them inside user and nice;
// steal counts as busy.
func (t cpuTimes) split() (total, busy float64) {
	total = t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
	return total, total - t.Idle - t.Iowait
}

// filterNet drops the excluded interfaces. If more than the protocol
// allows remain, the first by name are kept: the panel would otherwise
// reject the whole message.
func (c *Collector) filterNet(stats []netStat) map[string]protocol.NetCounters {
	kept := make([]netStat, 0, len(stats))
	for _, s := range stats {
		if s.Name == "" || ifacefilter.Match(c.opts.IfaceExclude, s.Name) {
			continue
		}
		kept = append(kept, s)
	}
	if len(kept) > protocol.MaxInterfaces {
		slices.SortFunc(kept, func(a, b netStat) int { return strings.Compare(a.Name, b.Name) })
		c.warnCapped(len(kept))
		kept = kept[:protocol.MaxInterfaces]
	}
	if len(kept) == 0 {
		return nil
	}
	out := make(map[string]protocol.NetCounters, len(kept))
	for _, s := range kept {
		out[s.Name] = protocol.NetCounters{RX: s.RX, TX: s.TX}
	}
	return out
}

func (c *Collector) warnCapped(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.capped {
		return
	}
	c.capped = true
	c.opts.Logger.Warn("too many network interfaces, reporting only some",
		"found", n, "reported", protocol.MaxInterfaces)
}

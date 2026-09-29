package collect

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kergeio/kerge-protocol"
	"github.com/kergeio/kerge-protocol/ifacefilter"
)

func fakeSources() sources {
	return sources{
		cpuTimes: func(context.Context) (cpuTimes, error) { return cpuTimes{User: 10, Idle: 90}, nil },
		mem:      func(context.Context) (memStat, error) { return memStat{Total: 2000, Available: 1200}, nil },
		swap:     func(context.Context) (swapStat, error) { return swapStat{Total: 1000, Used: 100}, nil },
		disk:     func(context.Context) (diskStat, error) { return diskStat{Total: 100, Used: 60, Free: 30}, nil },
		net: func(context.Context) ([]netStat, error) {
			return []netStat{{"eth0", 1, 2}, {"lo", 3, 4}, {"docker0", 5, 6}}, nil
		},
		load:   func(context.Context) (loadStat, error) { return loadStat{1, 2, 3}, nil },
		uptime: func(context.Context) (uint64, error) { return 1234, nil },
		host: func(context.Context) (hostStat, error) {
			return hostStat{Hostname: "web-1", OS: "linux", Platform: "debian",
				PlatformVersion: "12", Kernel: "6.1.0", Arch: "x86_64",
				CPUModel: "Xeon", CPUCores: 4}, nil
		},
	}
}

func testOptions() Options {
	return Options{
		Interval:     5 * time.Second,
		IfaceExclude: ifacefilter.DefaultExclude,
		Version:      "0.1.0",
		Timeout:      200 * time.Millisecond,
		Logger:       slog.New(slog.DiscardHandler),
	}
}

func newTestCollector(t *testing.T, src sources) *Collector {
	t.Helper()
	return newWith(testOptions(), src, time.Now)
}

func TestMetricsFields(t *testing.T) {
	c := newTestCollector(t, fakeSources())
	m := c.Metrics(t.Context())

	if got, want := deref(t, "mem_total", m.MemTotal), uint64(2000); got != want {
		t.Errorf("mem_total = %d, want %d", got, want)
	}
	// used is total - available, not gopsutil's own Used field.
	if got, want := deref(t, "mem_used", m.MemUsed), uint64(800); got != want {
		t.Errorf("mem_used = %d, want %d", got, want)
	}
	if got, want := deref(t, "swap_used", m.SwapUsed), uint64(100); got != want {
		t.Errorf("swap_used = %d, want %d", got, want)
	}
	// A file system with reserved blocks has total > used + free; the agent
	// passes all three through unchanged.
	if deref(t, "disk_total", m.DiskTotal) != 100 ||
		deref(t, "disk_used", m.DiskUsed) != 60 ||
		deref(t, "disk_free", m.DiskFree) != 30 {
		t.Errorf("disk = %d/%d/%d", *m.DiskTotal, *m.DiskUsed, *m.DiskFree)
	}
	if got, want := deref(t, "uptime", m.Uptime), uint64(1234); got != want {
		t.Errorf("uptime = %d, want %d", got, want)
	}
	if m.CPUPercent != nil {
		t.Errorf("cpu_percent = %v on the first sample, want absent", *m.CPUPercent)
	}
	if loadSupported {
		if deref(t, "load1", m.Load1) != 1 || deref(t, "load15", m.Load15) != 3 {
			t.Errorf("load = %v/%v/%v", m.Load1, m.Load5, m.Load15)
		}
	} else if m.Load1 != nil {
		t.Error("load reported on a platform without load average")
	}
	if len(m.Net) != 1 {
		t.Fatalf("net = %v, want only eth0", m.Net)
	}
	if n := m.Net["eth0"]; n.RX != 1 || n.TX != 2 {
		t.Errorf("net[eth0] = %+v", n)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("sample rejected by the protocol: %v", err)
	}
}

func deref[T any](t *testing.T, name string, p *T) T {
	t.Helper()
	if p == nil {
		t.Fatalf("%s is absent", name)
	}
	return *p
}

// The busy share excludes idle and iowait, counts steal as busy, and leaves
// guest and guest_nice out of the total because user and nice already
// include them.
func TestCPUPercent(t *testing.T) {
	cases := []struct {
		name       string
		prev, next cpuTimes
		want       float64
	}{
		{"idle only", cpuTimes{Idle: 100}, cpuTimes{Idle: 200}, 0},
		{"fully busy", cpuTimes{}, cpuTimes{User: 100}, 100},
		{"half busy", cpuTimes{}, cpuTimes{User: 50, Idle: 50}, 50},
		{"iowait is not busy", cpuTimes{}, cpuTimes{Iowait: 50, Idle: 50}, 0},
		{"steal is busy", cpuTimes{}, cpuTimes{Steal: 25, Idle: 75}, 25},
		{"system and nice are busy", cpuTimes{}, cpuTimes{System: 10, Nice: 10, Idle: 80}, 20},
		{"irq and softirq are busy", cpuTimes{}, cpuTimes{Irq: 5, Softirq: 5, Idle: 90}, 10},
		{
			"guest is already inside user",
			cpuTimes{},
			cpuTimes{User: 40, Guest: 40, Idle: 60},
			40,
		},
		{
			"guest_nice is already inside nice",
			cpuTimes{},
			cpuTimes{Nice: 20, GuestNice: 20, Idle: 80},
			20,
		},
	}
	for _, tc := range cases {
		src := fakeSources()
		readings := []cpuTimes{tc.prev, tc.next}
		var i atomic.Int32
		src.cpuTimes = func(context.Context) (cpuTimes, error) {
			return readings[min(int(i.Add(1))-1, len(readings)-1)], nil
		}
		c := newTestCollector(t, src)
		if m := c.Metrics(t.Context()); m.CPUPercent != nil {
			t.Errorf("%s: first sample reported %v, want absent", tc.name, *m.CPUPercent)
		}
		m := c.Metrics(t.Context())
		if m.CPUPercent == nil {
			t.Errorf("%s: second sample has no cpu_percent", tc.name)
			continue
		}
		if *m.CPUPercent != tc.want {
			t.Errorf("%s: cpu_percent = %v, want %v", tc.name, *m.CPUPercent, tc.want)
		}
	}
}

// Counters that do not advance, or that go backwards after a CPU is
// hot-plugged, produce no value rather than a made-up one.
func TestCPUPercentNeedsProgress(t *testing.T) {
	for _, next := range []cpuTimes{{User: 10, Idle: 90}, {User: 1, Idle: 9}} {
		src := fakeSources()
		readings := []cpuTimes{{User: 10, Idle: 90}, next}
		var i atomic.Int32
		src.cpuTimes = func(context.Context) (cpuTimes, error) {
			return readings[min(int(i.Add(1))-1, len(readings)-1)], nil
		}
		c := newTestCollector(t, src)
		c.Metrics(t.Context())
		if m := c.Metrics(t.Context()); m.CPUPercent != nil {
			t.Errorf("next = %+v: cpu_percent = %v, want absent", next, *m.CPUPercent)
		}
	}
}

func TestMemoryAvailableAboveTotal(t *testing.T) {
	src := fakeSources()
	src.mem = func(context.Context) (memStat, error) { return memStat{Total: 100, Available: 200}, nil }
	m := newTestCollector(t, src).Metrics(t.Context())
	if got := deref(t, "mem_used", m.MemUsed); got != 0 {
		t.Errorf("mem_used = %d, want 0", got)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("sample rejected by the protocol: %v", err)
	}
}

func TestSwapDisabled(t *testing.T) {
	src := fakeSources()
	src.swap = func(context.Context) (swapStat, error) { return swapStat{}, nil }
	m := newTestCollector(t, src).Metrics(t.Context())
	if deref(t, "swap_total", m.SwapTotal) != 0 || deref(t, "swap_used", m.SwapUsed) != 0 {
		t.Errorf("swap = %d/%d, want 0/0", *m.SwapTotal, *m.SwapUsed)
	}
}

func TestNetFiltering(t *testing.T) {
	src := fakeSources()
	src.net = func(context.Context) ([]netStat, error) {
		return []netStat{
			{"eth0", 1, 1}, {"enp3s0", 2, 2}, {"bond0", 3, 3},
			{"lo", 0, 0}, {"docker0", 0, 0}, {"veth9a8b", 0, 0}, {"br-1a2b", 0, 0},
			{"virbr0", 0, 0}, {"tun0", 0, 0}, {"wg0", 0, 0}, {"tailscale0", 0, 0},
			{"zt7c8d", 0, 0}, {"dummy0", 0, 0}, {"kube-ipvs0", 0, 0}, {"", 9, 9},
		}, nil
	}
	m := newTestCollector(t, src).Metrics(t.Context())
	want := []string{"bond0", "enp3s0", "eth0"}
	if len(m.Net) != len(want) {
		t.Fatalf("net = %v, want %v", m.Net, want)
	}
	for _, name := range want {
		if _, ok := m.Net[name]; !ok {
			t.Errorf("net is missing %q", name)
		}
	}
}

func TestNetAllExcluded(t *testing.T) {
	src := fakeSources()
	src.net = func(context.Context) ([]netStat, error) { return []netStat{{"lo", 1, 1}}, nil }
	if m := newTestCollector(t, src).Metrics(t.Context()); m.Net != nil {
		t.Errorf("net = %v, want absent", m.Net)
	}
}

// More interfaces than the protocol allows would make the panel reject the
// whole message, so the agent caps its own report.
func TestNetCappedAtProtocolLimit(t *testing.T) {
	src := fakeSources()
	src.net = func(context.Context) ([]netStat, error) {
		var out []netStat
		for i := range protocol.MaxInterfaces + 5 {
			out = append(out, netStat{Name: "eth" + strconv.Itoa(i)})
		}
		return out, nil
	}
	var logs bytes.Buffer
	opts := testOptions()
	opts.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	c := newWith(opts, src, time.Now)
	m := c.Metrics(t.Context())
	if len(m.Net) != protocol.MaxInterfaces {
		t.Errorf("net has %d interfaces, want %d", len(m.Net), protocol.MaxInterfaces)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("sample rejected by the protocol: %v", err)
	}
	c.Metrics(t.Context())
	if n := strings.Count(logs.String(), "too many network interfaces"); n != 1 {
		t.Errorf("logged the cap %d times, want 1", n)
	}
}

// A collector that times out leaves its own fields out and does not hold up
// the rest of the sample.
func TestTimeoutDoesNotBlockOtherCollectors(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	src := fakeSources()
	src.disk = func(context.Context) (diskStat, error) {
		<-release
		return diskStat{}, nil
	}
	c := newTestCollector(t, src)

	start := time.Now()
	m := c.Metrics(t.Context())
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("sample took %v, want about the timeout", elapsed)
	}
	if m.DiskTotal != nil || m.DiskUsed != nil || m.DiskFree != nil {
		t.Error("disk fields present although the collector hung")
	}
	if m.MemTotal == nil || m.Uptime == nil || m.Net == nil {
		t.Error("a healthy collector was dropped along with the stuck one")
	}
	if err := m.Validate(); err != nil {
		t.Errorf("sample rejected by the protocol: %v", err)
	}
}

// A collection stuck in an uncancellable system call strands its
// goroutine. Only one may ever be stranded per collector, no matter how
// many cycles run, so neither goroutines nor memory grow over time.
func TestStuckCollectorStartsOnlyOneGoroutine(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int64
	src := fakeSources()
	src.disk = func(context.Context) (diskStat, error) {
		calls.Add(1)
		<-release
		return diskStat{}, nil
	}
	c := newTestCollector(t, src)

	c.Metrics(t.Context())
	settled := settle(t)
	const cycles = 30
	for range cycles {
		if m := c.Metrics(t.Context()); m.DiskTotal != nil {
			t.Fatal("disk reported while the collector is stuck")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("started %d collections, want 1", got)
	}
	if got := settle(t); got > settled {
		t.Errorf("goroutines grew from %d to %d over %d cycles", settled, got, cycles)
	}
}

// settle waits for the goroutines of finished samples to be reaped and
// returns the resulting count.
func settle(t *testing.T) int {
	t.Helper()
	n := runtime.NumGoroutine()
	for range 100 {
		time.Sleep(5 * time.Millisecond)
		if got := runtime.NumGoroutine(); got == n {
			return n
		} else {
			n = got
		}
	}
	return n
}

// A collector that recovers reports again.
func TestCollectorRecovers(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	src := fakeSources()
	src.uptime = func(context.Context) (uint64, error) {
		if fail.Load() {
			return 0, errors.New("boom")
		}
		return 42, nil
	}
	c := newTestCollector(t, src)
	if m := c.Metrics(t.Context()); m.Uptime != nil {
		t.Error("uptime present although the collector failed")
	}
	fail.Store(false)
	if got := deref(t, "uptime", c.Metrics(t.Context()).Uptime); got != 42 {
		t.Errorf("uptime = %d, want 42", got)
	}
}

// Failures are logged once, then at most once per logInterval, and once
// more on recovery.
func TestFailureLoggingIsThrottled(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	src := fakeSources()
	src.uptime = func(context.Context) (uint64, error) {
		if fail.Load() {
			return 0, errors.New("boom")
		}
		return 1, nil
	}
	var logs bytes.Buffer
	opts := testOptions()
	opts.Logger = slog.New(slog.NewTextHandler(&logs, nil))

	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	c := newWith(opts, src, func() time.Time { return now })

	for range 10 {
		c.Metrics(t.Context())
		now = now.Add(20 * time.Second)
	}
	if got := strings.Count(logs.String(), "collection failed"); got != 1 {
		t.Errorf("first failure logged %d times, want 1", got)
	}
	if got := strings.Count(logs.String(), "collection still failing"); got != 0 {
		t.Errorf("logged %d repeats within %v, want 0", got, logInterval)
	}

	now = now.Add(logInterval)
	c.Metrics(t.Context())
	if got := strings.Count(logs.String(), "collection still failing"); got != 1 {
		t.Errorf("logged %d repeats after %v, want 1", got, logInterval)
	}

	fail.Store(false)
	c.Metrics(t.Context())
	if got := strings.Count(logs.String(), "collection recovered"); got != 1 {
		t.Errorf("recovery logged %d times, want 1", got)
	}
}

func TestHostInfo(t *testing.T) {
	c := newTestCollector(t, fakeSources())
	info := c.HostInfo(t.Context())
	if info.Type != protocol.TypeHostInfo || info.Hostname != "web-1" || info.Arch != "x86_64" {
		t.Errorf("host_info = %+v", info)
	}
	if info.CPUCores != 4 || info.AgentVersion != "0.1.0" || info.IntervalMS != 5000 {
		t.Errorf("host_info = %+v", info)
	}
	if err := info.Validate(); err != nil {
		t.Errorf("host_info rejected by the protocol: %v", err)
	}
}

// Control characters and over-long strings from the host are cleaned up
// before they reach the panel.
func TestHostInfoNormalizes(t *testing.T) {
	src := fakeSources()
	src.host = func(context.Context) (hostStat, error) {
		return hostStat{Hostname: "we\x00b\n-1", CPUModel: strings.Repeat("x", 200)}, nil
	}
	info := newTestCollector(t, src).HostInfo(t.Context())
	if info.Hostname != "web-1" {
		t.Errorf("hostname = %q", info.Hostname)
	}
	if len([]rune(info.CPUModel)) != 128 {
		t.Errorf("cpu_model length = %d, want 128", len([]rune(info.CPUModel)))
	}
}

func TestHostInfoWhenCollectionFails(t *testing.T) {
	src := fakeSources()
	src.host = func(context.Context) (hostStat, error) { return hostStat{}, errors.New("boom") }
	info := newTestCollector(t, src).HostInfo(t.Context())
	if info.Hostname != "" {
		t.Errorf("hostname = %q, want empty", info.Hostname)
	}
	// The interval and version come from the agent, so the message is still
	// valid and the panel learns the reporting interval.
	if err := info.Validate(); err != nil {
		t.Errorf("host_info rejected by the protocol: %v", err)
	}
}

func TestRunSamplesUntilContextIsDone(t *testing.T) {
	opts := testOptions()
	opts.Interval = 10 * time.Millisecond
	c := newWith(opts, fakeSources(), time.Now)

	ctx, cancel := context.WithCancel(t.Context())
	samples := make(chan *protocol.Metrics, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx, func(m *protocol.Metrics) { samples <- m })
	}()

	for range 3 {
		select {
		case m := <-samples:
			if m.MonoMS < 0 {
				t.Errorf("mono_ms = %d", m.MonoMS)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for a sample")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

// Ticks that arrive while a sample is still being taken are dropped rather
// than queued, so the agent never tries to catch up.
func TestRunSkipsMissedTicks(t *testing.T) {
	opts := testOptions()
	opts.Interval = 5 * time.Millisecond
	c := newWith(opts, fakeSources(), time.Now)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var samples atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx, func(*protocol.Metrics) {
			samples.Add(1)
			time.Sleep(50 * time.Millisecond)
		})
	}()

	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	// Without dropping, 300ms at 5ms would queue about 60 samples.
	if got := samples.Load(); got > 12 {
		t.Errorf("took %d samples, want the missed ticks to be dropped", got)
	}
}

// The agent must be able to hand the collector exactly what agent.conf
// produced.
func TestDefaultExcludeListFiltersVirtualInterfaces(t *testing.T) {
	for _, name := range []string{"lo", "docker0", "veth1", "br-x", "cni0", "flannel.1",
		"cali123", "kube-ipvs0", "virbr0", "vnet1", "tun0", "tap0", "wg0",
		"tailscale0", "zt1", "dummy0"} {
		if !matchesDefault(name) {
			t.Errorf("%q is not excluded by the default list", name)
		}
	}
	for _, name := range []string{"eth0", "enp3s0", "wlan0", "bond0", "ens5"} {
		if matchesDefault(name) {
			t.Errorf("%q is excluded by the default list", name)
		}
	}
}

func matchesDefault(name string) bool {
	src := fakeSources()
	src.net = func(context.Context) ([]netStat, error) { return []netStat{{name, 1, 1}}, nil }
	c := newWith(testOptions(), src, time.Now)
	return c.Metrics(context.Background()).Net == nil
}

// Values are rounded to two decimals: full float precision would add about
// fifty bytes to every message without changing what the panel shows.
func TestValuesAreRoundedForTheWire(t *testing.T) {
	src := fakeSources()
	readings := []cpuTimes{{}, {User: 21.026282850882698, Idle: 78.9737171491173}}
	var i atomic.Int32
	src.cpuTimes = func(context.Context) (cpuTimes, error) {
		return readings[min(int(i.Add(1))-1, len(readings)-1)], nil
	}
	src.load = func(context.Context) (loadStat, error) {
		return loadStat{3.9658203125, 3.46240234375, 2.94091796875}, nil
	}
	c := newTestCollector(t, src)
	c.Metrics(t.Context())
	m := c.Metrics(t.Context())

	if got := deref(t, "cpu_percent", m.CPUPercent); got != 21.03 {
		t.Errorf("cpu_percent = %v, want 21.03", got)
	}
	if loadSupported {
		if got := deref(t, "load1", m.Load1); got != 3.97 {
			t.Errorf("load1 = %v, want 3.97", got)
		}
	}
	data, err := protocol.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "21.026282850882698") {
		t.Errorf("full precision reached the wire: %s", data)
	}
}

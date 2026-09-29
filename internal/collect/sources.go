package collect

import (
	"context"
	"errors"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// rootPath is the only mount point collected in v1.
const rootPath = "/"

// The types below are the platform-independent shape of one collection.
// Keeping them free of gopsutil types is what lets the tests drive the
// collector without touching the host.

// cpuTimes holds the cumulative CPU time counters in seconds.
type cpuTimes struct {
	User, System, Idle, Nice, Iowait, Irq, Softirq, Steal, Guest, GuestNice float64
}

type memStat struct{ Total, Available uint64 }

type swapStat struct{ Total, Used uint64 }

type diskStat struct{ Total, Used, Free uint64 }

type netStat struct {
	Name   string
	RX, TX uint64
}

type loadStat struct{ Load1, Load5, Load15 float64 }

type hostStat struct {
	Hostname        string
	OS              string
	Platform        string
	PlatformVersion string
	Kernel          string
	Arch            string
	CPUModel        string
	CPUCores        int
}

// sources are the platform probes. Tests replace them.
type sources struct {
	cpuTimes func(context.Context) (cpuTimes, error)
	mem      func(context.Context) (memStat, error)
	swap     func(context.Context) (swapStat, error)
	disk     func(context.Context) (diskStat, error)
	net      func(context.Context) ([]netStat, error)
	load     func(context.Context) (loadStat, error)
	uptime   func(context.Context) (uint64, error)
	host     func(context.Context) (hostStat, error)
}

func defaultSources() sources {
	return sources{
		cpuTimes: collectCPUTimes,
		mem:      collectMem,
		swap:     collectSwap,
		disk:     collectDisk,
		net:      collectNet,
		load:     collectLoad,
		uptime:   host.UptimeWithContext,
		host:     collectHost,
	}
}

func collectCPUTimes(ctx context.Context) (cpuTimes, error) {
	// percpu is false: only the aggregate is reported.
	ts, err := cpu.TimesWithContext(ctx, false)
	if err != nil {
		return cpuTimes{}, err
	}
	if len(ts) == 0 {
		return cpuTimes{}, errors.New("collect: no cpu times returned")
	}
	t := ts[0]
	return cpuTimes{
		User: t.User, System: t.System, Idle: t.Idle, Nice: t.Nice,
		Iowait: t.Iowait, Irq: t.Irq, Softirq: t.Softirq, Steal: t.Steal,
		Guest: t.Guest, GuestNice: t.GuestNice,
	}, nil
}

func collectMem(ctx context.Context) (memStat, error) {
	v, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return memStat{}, err
	}
	// Available, not Used: used is MemTotal - MemAvailable so that the
	// figure matches free and htop.
	return memStat{Total: v.Total, Available: v.Available}, nil
}

func collectSwap(ctx context.Context) (swapStat, error) {
	v, err := mem.SwapMemoryWithContext(ctx)
	if err != nil {
		return swapStat{}, err
	}
	return swapStat{Total: v.Total, Used: v.Used}, nil
}

func collectDisk(ctx context.Context) (diskStat, error) {
	// gopsutil computes exactly the figures the agent reports:
	// total = Blocks x bsize, used = (Blocks - Bfree) x bsize and
	// free = Bavail x bsize, so total exceeds used + free on a file system
	// with reserved blocks.
	v, err := disk.UsageWithContext(ctx, rootPath)
	if err != nil {
		return diskStat{}, err
	}
	return diskStat{Total: v.Total, Used: v.Used, Free: v.Free}, nil
}

func collectNet(ctx context.Context) ([]netStat, error) {
	cs, err := net.IOCountersWithContext(ctx, true)
	if err != nil {
		return nil, err
	}
	out := make([]netStat, 0, len(cs))
	for _, c := range cs {
		out = append(out, netStat{Name: c.Name, RX: c.BytesRecv, TX: c.BytesSent})
	}
	return out, nil
}

func collectHost(ctx context.Context) (hostStat, error) {
	info, err := host.InfoWithContext(ctx)
	if err != nil {
		return hostStat{}, err
	}
	s := hostStat{
		Hostname:        info.Hostname,
		OS:              info.OS,
		Platform:        info.Platform,
		PlatformVersion: info.PlatformVersion,
		Kernel:          info.KernelVersion,
		Arch:            info.KernelArch,
	}
	// The CPU model and count are a separate call and matter less than the
	// rest, so a failure here only leaves those two fields empty.
	if cs, err := cpu.InfoWithContext(ctx); err == nil && len(cs) > 0 {
		s.CPUModel = cs[0].ModelName
	}
	if n, err := cpu.CountsWithContext(ctx, true); err == nil {
		s.CPUCores = n
	}
	return s, nil
}

//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package collect

import (
	"context"

	"github.com/shirou/gopsutil/v4/load"
)

// loadSupported reports whether this platform has a load average. Windows
// does not, and the field is then left out entirely.
const loadSupported = true

func collectLoad(ctx context.Context) (loadStat, error) {
	v, err := load.AvgWithContext(ctx)
	if err != nil {
		return loadStat{}, err
	}
	return loadStat{Load1: v.Load1, Load5: v.Load5, Load15: v.Load15}, nil
}

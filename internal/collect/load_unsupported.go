//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly)

package collect

import (
	"context"
	"errors"
)

const loadSupported = false

func collectLoad(context.Context) (loadStat, error) {
	return loadStat{}, errors.New("collect: no load average on this platform")
}

package collect

import (
	"bytes"
	"compress/flate"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/kergeio/kerge-protocol"
	"github.com/kergeio/kerge-protocol/ifacefilter"
)

// TestRealHostNoGrowth samples the real host through gopsutil for many
// cycles and checks that neither goroutines nor heap grow. It is skipped
// unless KERGE_REALHOST is set, because it needs a Linux host to be
// meaningful and takes a while; cross-compile it with
//
//	GOOS=linux go test -c ./internal/collect
//
// and run the binary where the agent will run.
func TestRealHostNoGrowth(t *testing.T) {
	if os.Getenv("KERGE_REALHOST") == "" {
		t.Skip("set KERGE_REALHOST=1 to sample the real host")
	}
	c := New(Options{
		Interval:     5 * time.Second,
		IfaceExclude: ifacefilter.DefaultExclude,
		Version:      "test",
	})
	info := c.HostInfo(t.Context())
	t.Logf("host_info: %+v", info)
	if err := info.Validate(); err != nil {
		t.Errorf("host_info rejected by the protocol: %v", err)
	}

	const cycles = 1500
	var base, baseHeap uint64
	var withCPU int
	for i := 1; i <= cycles; i++ {
		m := c.Metrics(t.Context())
		if err := m.Validate(); err != nil {
			t.Fatalf("cycle %d: sample rejected by the protocol: %v", i, err)
		}
		if m.CPUPercent != nil {
			withCPU++
		}
		// The kernel counts CPU time in ticks, so back-to-back samples
		// would often see no progress at all and report nothing.
		time.Sleep(10 * time.Millisecond)
		if i == 1 || i%500 == 0 {
			goroutines, heap := measure()
			cpu := "absent"
			if m.CPUPercent != nil {
				cpu = strconv.FormatFloat(*m.CPUPercent, 'f', 1, 64)
			}
			t.Logf("cycle %4d: goroutines=%d heap=%d KiB net=%d cpu=%s%%",
				i, goroutines, heap/1024, len(m.Net), cpu)
			if i == 1 {
				base, baseHeap = goroutines, heap
				continue
			}
			if goroutines > base {
				t.Errorf("cycle %d: goroutines grew from %d to %d", i, base, goroutines)
			}
			if heap > baseHeap*2 {
				t.Errorf("cycle %d: heap grew from %d to %d bytes", i, baseHeap, heap)
			}
		}
	}
	if withCPU < cycles/2 {
		t.Errorf("cpu_percent present in %d of %d samples", withCPU, cycles)
	}
	reportSize(t, c.Metrics(t.Context()))
}

// reportSize prints the wire size of one sample and the daily outbound
// traffic it implies at the default interval, with and without the
// WebSocket compression the agent enables.
func reportSize(t *testing.T, m *protocol.Metrics) {
	t.Helper()
	data, err := protocol.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	w, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	// permessage-deflate keeps the window between messages, so a steady
	// stream of near-identical samples compresses far better than one
	// message on its own. Ten copies approximate the steady state.
	for range 10 {
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	perDay := func(size int) string {
		const overhead = 50 // WebSocket frame, TLS record and TCP headers
		const perDay = 24 * 60 * 60 / 5
		return strconv.FormatFloat(float64(perDay*(size+overhead))/1e6, 'f', 1, 64) + " MB/day"
	}
	steady := compressed.Len() / 10
	t.Logf("sample: %d bytes raw (%s), %d bytes compressed (%s), %d interfaces",
		len(data), perDay(len(data)), steady, perDay(steady), len(m.Net))
	t.Logf("sample body: %s", data)
}

func measure() (goroutines, heap uint64) {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return uint64(runtime.NumGoroutine()), ms.HeapAlloc
}

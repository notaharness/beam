package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/notaharness/beam/internal/devderp"
	"tailscale.com/tailcfg"
	"tailscale.com/types/logger"
)

// A fleet with one process per machine as in production, each with one Server
// and a Client per peer, every pair connected in both directions, with UDP
// blocked and allowed. It proves the topology across processes and reports
// what docs/03's footprint table records. Numbers under -race are inflated;
// the table comes from
//
//	go test -count=1 -run TestFootprint -v ./internal/transport
func TestFootprint(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	for _, tc := range []struct {
		machines int
		relay    bool
	}{
		{2, true},
		{5, true},
		{5, false},
	} {
		name := fmt.Sprintf("%d machines, UDP blocked %v", tc.machines, tc.relay)
		t.Run(name, func(t *testing.T) {
			logFootprint(t, runFleet(t, tc.machines, tc.relay))
		})
	}
}

// footprintReport is what each machine process prints once connected.
type footprintReport struct {
	StartMs int64   `json:"startMs"` // Start until the Server listens
	DialMs  []int64 `json:"dialMs"`  // Dial through hello, per peer
	RSSKB   int64   `json:"rssKB"`   // VmRSS with every tunnel up
	HWMKB   int64   `json:"hwmKB"`   // VmHWM
}

func runFleet(t *testing.T, machines int, forceRelay bool) []footprintReport {
	region, _ := json.Marshal(relay.Region)
	type child struct {
		cmd  *exec.Cmd
		in   *os.File
		out  *bufio.Scanner
		addr string
	}
	children := make([]*child, machines)
	for i := range children {
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), "BEAM_FOOTPRINT_REGION="+string(region),
			"TS_DEBUG_ALWAYS_USE_DERP="+strconv.FormatBool(forceRelay))
		cmd.Stdin, cmd.Stderr = pr, os.Stderr
		stdout, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pr.Close()
		t.Cleanup(func() { pw.Close(); cmd.Wait() })
		children[i] = &child{cmd: cmd, in: pw, out: bufio.NewScanner(stdout)}
	}
	var addrs []string
	for _, c := range children {
		if !c.out.Scan() {
			t.Fatal("machine exited before listening")
		}
		c.addr = c.out.Text()
		addrs = append(addrs, c.addr)
	}
	for _, c := range children {
		var peers []string
		for _, a := range addrs {
			if a != c.addr {
				peers = append(peers, a)
			}
		}
		b, _ := json.Marshal(peers)
		fmt.Fprintf(c.in, "%s\n", b)
	}
	// Measure only once every machine has dialed every other.
	for i, c := range children {
		if !c.out.Scan() || c.out.Text() != "connected" {
			t.Fatalf("machine %d: %q", i, c.out.Text())
		}
	}
	var reports []footprintReport
	for i, c := range children {
		fmt.Fprintln(c.in, "measure")
		var r footprintReport
		if !c.out.Scan() || json.Unmarshal(c.out.Bytes(), &r) != nil {
			t.Fatalf("machine %d: no report", i)
		}
		reports = append(reports, r)
	}
	return reports
}

func logFootprint(t *testing.T, reports []footprintReport) {
	var dials, starts, rss, hwm []int64
	for _, r := range reports {
		dials = append(dials, r.DialMs...)
		starts = append(starts, r.StartMs)
		rss = append(rss, r.RSSKB/1024)
		hwm = append(hwm, r.HWMKB/1024)
	}
	for _, s := range [][]int64{dials, starts, rss, hwm} {
		slices.Sort(s)
	}
	t.Logf("%d machines, %d tunnels, %s/%s", len(reports), len(dials), runtime.GOOS, runtime.GOARCH)
	t.Logf("start (ms): %v", starts)
	t.Logf("dial through hello (ms): median %d, max %d", dials[len(dials)/2], dials[len(dials)-1])
	t.Logf("RSS per machine (MB): %v; peak %v", rss, hwm)
}

// footprintChild runs one machine of TestFootprint when the parent asked for
// it, and reports whether it did.
func footprintChild() bool {
	region := os.Getenv("BEAM_FOOTPRINT_REGION")
	if region == "" {
		return false
	}
	if err := runFootprintChild(region); err != nil {
		fmt.Fprintln(os.Stderr, "footprint machine:", err)
		os.Exit(1)
	}
	return true
}

func runFootprintChild(region string) error {
	devderp.Isolate() // the parent's environment decides TS_DEBUG_ALWAYS_USE_DERP
	var reg tailcfg.DERPRegion
	if err := json.Unmarshal([]byte(region), &reg); err != nil {
		return err
	}
	k := NewKey(&reg)
	id, entry := entryFor(k)
	t0 := time.Now()
	n, err := Start(Config{Key: k, Entry: entry, Admit: admitFake, Handle: echoCaller, Logf: logger.Discard})
	if err != nil {
		return err
	}
	defer n.Close()
	report := footprintReport{StartMs: time.Since(t0).Milliseconds()}
	fmt.Println(k.Address())

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(nil, 1<<20)
	var peers []string
	if !in.Scan() || json.Unmarshal(in.Bytes(), &peers) != nil {
		return fmt.Errorf("no peer list")
	}
	if report.DialMs, err = dialAll(n, id, peers); err != nil {
		return err
	}
	fmt.Println("connected")
	if !in.Scan() {
		return fmt.Errorf("no measure request")
	}
	report.RSSKB, report.HWMKB = procStatus("VmRSS:"), procStatus("VmHWM:")
	b, _ := json.Marshal(report)
	fmt.Println(string(b))
	in.Scan() // hold every tunnel up until the parent is done
	return nil
}

func dialAll(n *Node, id string, peers []string) ([]int64, error) {
	ms := make([]int64, len(peers))
	errs := make([]error, len(peers))
	var wg sync.WaitGroup
	for i, a := range peers {
		wg.Go(func() {
			t0 := time.Now()
			tun, err := n.Dial(context.Background(), a)
			ms[i] = time.Since(t0).Milliseconds()
			if err == nil {
				err = roundTrip(context.Background(), tun, id)
			}
			errs[i] = err
		})
	}
	wg.Wait()
	return ms, errors.Join(errs...)
}

func procStatus(field string) int64 {
	b, _ := os.ReadFile("/proc/self/status")
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, field); ok {
			kb, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return kb
		}
	}
	return 0
}

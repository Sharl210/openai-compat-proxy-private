package perfbench

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"openai-compat-proxy/internal/config"
)

type capacityTraffic string

const (
	capacityTrafficManyUsers    capacityTraffic = "many_users"
	capacityTrafficOneUserBurst capacityTraffic = "one_user_burst"
	capacityRepetitions                         = 5
)

type capacityWorkload struct {
	Scenario    scenario
	Traffic     capacityTraffic
	Concurrency int
	Repetitions int
}

func (w capacityWorkload) Name() string {
	return fmt.Sprintf("%s/%s/%d", w.Scenario.ID, w.Traffic, w.Concurrency)
}

func capacityWorkloads() ([]capacityWorkload, error) {
	tiersByScenario := map[string][]int{
		"chat-chat-proxy_buffer-1mib-plain":     {1, 2, 4, 8},
		"responses-responses-stream-8mib-plain": {1, 2, 4},
	}
	workloads := make([]capacityWorkload, 0, 14)
	for _, item := range scenarioCatalog() {
		tiers, ok := tiersByScenario[item.ID]
		if !ok {
			continue
		}
		for _, traffic := range []capacityTraffic{capacityTrafficManyUsers, capacityTrafficOneUserBurst} {
			for _, concurrency := range tiers {
				workloads = append(workloads, capacityWorkload{
					Scenario:    item,
					Traffic:     traffic,
					Concurrency: concurrency,
					Repetitions: capacityRepetitions,
				})
			}
		}
	}
	if len(workloads) != 14 {
		return nil, fmt.Errorf("capacity workload count = %d, want 14", len(workloads))
	}
	return workloads, nil
}

func capacityScenario(id string) (scenario, error) {
	for _, item := range scenarioCatalog() {
		if item.ID == id {
			return item, nil
		}
	}
	return scenario{}, fmt.Errorf("capacity scenario %q not found", id)
}

type capacityUpstreamGate struct {
	target        int32
	active        atomic.Int32
	peak          atomic.Int32
	reached       chan struct{}
	releaseSignal chan struct{}

	reachedOnce sync.Once
	releaseOnce sync.Once
}

func newCapacityUpstreamGate(target int) *capacityUpstreamGate {
	return &capacityUpstreamGate{
		target:        int32(target),
		reached:       make(chan struct{}),
		releaseSignal: make(chan struct{}),
	}
}

func (g *capacityUpstreamGate) wait(ctx context.Context) error {
	active := g.active.Add(1)
	defer g.active.Add(-1)
	for {
		peak := g.peak.Load()
		if active <= peak || g.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	if active >= g.target {
		g.reachedOnce.Do(func() { close(g.reached) })
	}
	select {
	case <-g.releaseSignal:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *capacityUpstreamGate) waitForPeak(ctx context.Context) error {
	select {
	case <-g.reached:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *capacityUpstreamGate) release() {
	g.releaseOnce.Do(func() { close(g.releaseSignal) })
}

func (g *capacityUpstreamGate) peakInFlight() int {
	return int(g.peak.Load())
}

type capacityProxyFixture struct {
	config               config.Config
	fake                 *semanticFakeUpstream
	proxyChild           *perfWorkerProcess
	proxyURL             string
	proxyClient          *http.Client
	proxyPID             int
	proxyExited          bool
	proxyHeapUnavailable bool
	gate                 *capacityUpstreamGate
	tempRoot             string
}

func newCapacityProxyFixture(item scenario, gate *capacityUpstreamGate) (_ *capacityProxyFixture, err error) {
	tempRoot, err := os.MkdirTemp("", "perfbench-capacity-")
	if err != nil {
		return nil, fmt.Errorf("create capacity temp root: %w", err)
	}
	fake := newSemanticFakeUpstreamWithGate(item, gate)
	cfg, err := semanticScenarioConfig(item, fake.url(), tempRoot)
	if err != nil {
		fake.close()
		return nil, errors.Join(err, os.RemoveAll(tempRoot))
	}
	cfg.LogEnable = false
	cfg.DebugArchiveRootDir = ""
	proxyChild, err := startPerfWorkerProcess(context.Background(), workerRequest{
		Action:      workerActionProxy,
		Scenario:    item,
		ProxyConfig: &cfg,
		UpstreamURL: fake.url(),
	}, nil)
	if err != nil {
		fake.close()
		return nil, errors.Join(fmt.Errorf("start capacity proxy child: %w", err), os.RemoveAll(tempRoot))
	}
	if err := proxyChild.startOperation(); err != nil {
		childErr := proxyChild.reap(fmt.Errorf("start capacity proxy operation: %w", err))
		fake.close()
		return nil, errors.Join(childErr, os.RemoveAll(tempRoot))
	}
	return &capacityProxyFixture{
		config:               cfg,
		fake:                 fake,
		proxyChild:           proxyChild,
		proxyURL:             proxyChild.run.BaseURL,
		proxyClient:          &http.Client{},
		proxyPID:             proxyChild.run.PID,
		proxyHeapUnavailable: true,
		gate:                 gate,
		tempRoot:             tempRoot,
	}, nil
}

func (f *capacityProxyFixture) requestHeapSnapshot() (workerHeapSnapshot, error) {
	if f.proxyChild == nil {
		return workerHeapSnapshot{}, errors.New("capacity proxy child is unavailable")
	}
	snapshot, err := f.proxyChild.requestHeapSnapshot()
	if err != nil {
		return workerHeapSnapshot{}, err
	}
	f.proxyHeapUnavailable = false
	return snapshot, nil
}

func (f *capacityProxyFixture) close() error {
	if f.gate != nil {
		f.gate.release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var childErr error
	if f.proxyChild != nil {
		var run workerRun
		run, childErr = f.proxyChild.stopProxy(ctx)
		f.proxyExited = run.Exited
		f.proxyChild = nil
	}
	if f.fake != nil {
		f.fake.close()
		f.fake = nil
	}
	if f.proxyClient != nil {
		f.proxyClient.CloseIdleConnections()
	}
	tempRoot := f.tempRoot
	f.tempRoot = ""
	if tempRoot == "" {
		return childErr
	}
	return errors.Join(childErr, os.RemoveAll(tempRoot))
}

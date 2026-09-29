/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package harnessplane

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/railgrid/railgrid/pkg/agent/discovery"
)

const controllerName = "harness-plane"

// DefaultResync is how often the plane reconciles without a watch event. It is
// short because readiness is a live probe of a supervised process, not a cached
// fact, and because detection re-runs with it: a harness installed after
// onboarding becomes usable within a minute without touching the hub.
const DefaultResync = 60 * time.Second

// DefaultBasePort is the first loopback port a runner is offered. Allocation
// walks up from here so two harnesses on one machine never collide and nobody
// has to assign ports by hand.
const DefaultBasePort = 8787

const (
	defaultProbeDeadline = 3 * time.Second
	defaultProbeTimeout  = 2 * time.Second
	// stopTimeout bounds a stop. It is longer than the supervisor's SIGTERM
	// grace so the child gets its full drain window before the agent gives up
	// waiting — an in-flight attempt is persisted and parked in that window.
	stopTimeout = 30 * time.Second
)

// Options configures a Manager.
type Options struct {
	// EdgeName is this edge's name and half of each runner's identity.
	EdgeName string
	// EdgeGVR addresses the agent's own edge object, which it watches for
	// spec.harness changes. Zero disables the watch (used by tests).
	EdgeGVR schema.GroupVersionResource
	// Executable is the agent's own binary, supervised as `<exe> runner run`.
	Executable string
	// Account is the non-root account every runner child runs as.
	Account RunAsAccount
	// CachePath is where the last OBSERVED setting is remembered, under the
	// agent's own home. See CachePath.
	CachePath string
	// Seed is the --harness flag value. It is written to CachePath only when no
	// cache exists yet and is never consulted again, which is the whole of the
	// "config wins over flags" rule.
	Seed Setting
	// Resync, BasePort, probe and backoff knobs; zero means the default.
	Resync         time.Duration
	BasePort       int
	ProbeDeadline  time.Duration
	ProbeTimeout   time.Duration
	InitialBackoff time.Duration
	// HTTPClient probes the loopback runners; nil builds one.
	HTTPClient *http.Client
	// Detect reports the installed harnesses; nil uses Detect(account home).
	// Tests replace it.
	Detect func() Detection
	// PortFree reports whether a loopback port can be bound; nil dials to find
	// out. Tests replace it.
	PortFree func(port int) bool
}

// Manager keeps the machine's supervised runners in step with spec.harness.
//
// It is the only writer of the harness cache file and the only owner of the
// runner children, so "what is this machine offering" has exactly one answer at
// any moment, whatever order the hub and the local clock deliver events in.
type Manager struct {
	hub  dynamic.Interface
	opts Options
	cfg  childConfig

	// reconcileMu serializes converge passes. The watch handler, the resync
	// ticker and the initial apply all call the same code, and two of them
	// running at once could start the same harness twice or race on a port.
	reconcileMu sync.Mutex

	mu       sync.Mutex
	setting  Setting
	observed bool
	detected Detection
	enabled  []string
	// reasons holds why an enabled harness is not running — most importantly
	// "enabled but not installed", which must be reported rather than dropped.
	reasons map[string][]string
	// children are the supervised runners, keyed by harness. A child is created
	// when its harness is first enabled and kept (stopped) afterwards so its
	// port and bearer stay stable across an off/on cycle.
	children map[string]*child
	ports    map[string]int
}

// NewManager builds a manager. hub may be nil, in which case no edge is watched
// and the cached setting is all there is; that is the shape tests use.
func NewManager(hub dynamic.Interface, opts Options) (*Manager, error) {
	if opts.EdgeName == "" {
		return nil, fmt.Errorf("harness plane: edge name is required")
	}
	if opts.Executable == "" {
		return nil, fmt.Errorf("harness plane: executable path is required")
	}
	if opts.CachePath == "" {
		return nil, fmt.Errorf("harness plane: a cache path is required")
	}
	if opts.Account.Home == "" || !filepath.IsAbs(opts.Account.Home) || opts.Account.Home == "/" {
		return nil, fmt.Errorf("harness plane: an absolute non-root home is required, got %q", opts.Account.Home)
	}
	if !opts.Account.Inherited() && (opts.Account.UID == 0 || opts.Account.GID == 0) {
		return nil, fmt.Errorf("harness plane: refusing to run a harness as uid 0")
	}
	if err := opts.Seed.Validate(); err != nil {
		return nil, fmt.Errorf("harness plane: %w", err)
	}
	if opts.Resync <= 0 {
		opts.Resync = DefaultResync
	}
	if opts.BasePort <= 0 {
		opts.BasePort = DefaultBasePort
	}
	if opts.ProbeDeadline <= 0 {
		opts.ProbeDeadline = defaultProbeDeadline
	}
	if opts.ProbeTimeout <= 0 {
		opts.ProbeTimeout = defaultProbeTimeout
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: opts.ProbeTimeout}
	}
	if opts.Detect == nil {
		home := opts.Account.Home
		opts.Detect = func() Detection { return Detect(home) }
	}
	if opts.PortFree == nil {
		opts.PortFree = portFree
	}
	return &Manager{
		hub:  hub,
		opts: opts,
		cfg: childConfig{
			EdgeName:       opts.EdgeName,
			Executable:     opts.Executable,
			Account:        opts.Account,
			ProbeDeadline:  opts.ProbeDeadline,
			ProbeTimeout:   opts.ProbeTimeout,
			InitialBackoff: opts.InitialBackoff,
			HTTPClient:     opts.HTTPClient,
		},
		setting:  DefaultSetting(),
		detected: Detection{},
		reasons:  map[string][]string{},
		children: map[string]*child{},
		ports:    map[string]int{},
	}, nil
}

// Run starts the plane and blocks until ctx is cancelled, at which point every
// supervised runner is stopped with its full drain window.
//
// The order here is the cache-versus-spec precedence, and it is the only place
// the seed is read:
//
//  1. Seed the cache from --harness, but ONLY when no cache exists.
//  2. Apply whatever the cache says, before the first watch event, so a machine
//     that boots while the hub is unreachable runs what it was last told.
//  3. Watch the edge. The first observed spec.harness overwrites the cache, and
//     from then on nothing but an observation can change it.
func (m *Manager) Run(ctx context.Context) error {
	defer utilruntime.HandleCrash()
	logger := klog.FromContext(ctx).WithName(controllerName)

	seeded, err := SeedCache(m.opts.CachePath, m.opts.Seed)
	if err != nil {
		logger.Error(err, "seeding the harness cache", "path", m.opts.CachePath)
	} else if seeded {
		logger.Info("Seeded the harness cache from --harness; the first observed spec.harness replaces it",
			"path", m.opts.CachePath, "harness", m.opts.Seed.String())
	}
	cached, found, err := LoadCache(m.opts.CachePath)
	if err != nil {
		logger.Error(err, "reading the harness cache; falling back to the default", "path", m.opts.CachePath)
	}
	if !found || err != nil {
		cached = DefaultSetting()
	}
	m.mu.Lock()
	m.setting = cached
	m.mu.Unlock()
	logger.Info("Starting the harness plane", "edge", m.opts.EdgeName, "harness", cached.String(),
		"runAs", m.AccountDescription())
	if err := m.Reconcile(ctx); err != nil {
		logger.Error(err, "first harness reconcile")
	}

	if m.hub != nil && m.opts.EdgeGVR.Resource != "" {
		if err := m.watchEdge(ctx, logger); err != nil {
			// A failed watch is not fatal: the cached setting is already
			// applied, and losing the ability to see a change is better than
			// dropping the harnesses the machine was told to run.
			logger.Error(err, "watching this edge for spec.harness changes")
		}
	}

	ticker := time.NewTicker(m.opts.Resync)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("Shutting down the harness plane; stopping supervised runners")
			m.StopAll()
			return nil
		case <-ticker.C:
			if err := m.Reconcile(ctx); err != nil {
				logger.Error(err, "harness reconcile")
			}
		}
	}
}

// watchEdge starts an informer on this agent's OWN edge object. The agent holds
// list/watch on the kind and get on its own name; the field selector keeps the
// list to the one object it cares about.
func (m *Manager) watchEdge(ctx context.Context, logger klog.Logger) error {
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		m.hub, m.opts.Resync, metav1.NamespaceAll,
		func(opts *metav1.ListOptions) {
			opts.FieldSelector = "metadata.name=" + m.opts.EdgeName
		},
	)
	informer := factory.ForResource(m.opts.EdgeGVR).Informer()
	handle := func(obj interface{}) {
		setting, ok := settingFromObject(obj, m.opts.EdgeName)
		if !ok {
			return
		}
		m.Observe(ctx, setting)
	}
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    handle,
		UpdateFunc: func(_, obj interface{}) { handle(obj) },
		// A deleted edge is not an instruction to change anything: the object is
		// gone, the agent is about to lose its identity with it, and stopping
		// the harnesses on a transient cache delete would be wrong.
	}); err != nil {
		return err
	}
	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())
	logger.Info("Watching this edge for spec.harness changes", "resource", m.opts.EdgeGVR.Resource)
	return nil
}

// settingFromObject decodes spec.harness from an edge object. The edge API is
// in a separate module, so only the fields this agent needs are decoded.
func settingFromObject(obj interface{}, edgeName string) (Setting, bool) {
	accessor, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return Setting{}, false
	}
	var view edgeView
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(accessor.UnstructuredContent(), &view); err != nil {
		klog.Background().Error(err, "decoding this edge's spec.harness", "edge", edgeName)
		return Setting{}, false
	}
	if view.Name != "" && view.Name != edgeName {
		return Setting{}, false
	}
	if view.Spec.Harness == nil {
		// No spec.harness at all means the API default, which is auto. Treating
		// it as "nothing" would disable every harness on an edge created before
		// the field existed.
		return DefaultSetting(), true
	}
	return *view.Spec.Harness, true
}

// edgeView is the subset of the edge object the plane reads.
type edgeView struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              struct {
		Harness *Setting `json:"harness,omitempty"`
	} `json:"spec,omitempty"`
}

// Observe applies a setting seen on the edge object and records it on disk.
//
// This is the ONLY way the setting changes after start, and it always persists:
// the cache exists to remember the hub's last word, so an observation that
// disagrees with the flag that seeded it must win, permanently.
func (m *Manager) Observe(ctx context.Context, setting Setting) {
	logger := klog.FromContext(ctx).WithName(controllerName)
	if err := setting.Validate(); err != nil {
		// Keep running what we have: a spec this agent build cannot act on is
		// not a reason to tear down working harnesses.
		logger.Error(err, "ignoring an unusable spec.harness", "edge", m.opts.EdgeName)
		return
	}
	m.mu.Lock()
	changed := !m.observed || !reflect.DeepEqual(m.setting, setting)
	m.setting = setting
	m.observed = true
	m.mu.Unlock()
	if !changed {
		return
	}
	logger.Info("Observed spec.harness", "edge", m.opts.EdgeName, "harness", setting.String())
	if err := WriteCache(m.opts.CachePath, setting); err != nil {
		logger.Error(err, "caching the observed harness setting", "path", m.opts.CachePath)
	}
	if err := m.Reconcile(ctx); err != nil {
		logger.Error(err, "harness reconcile after a spec.harness change")
	}
}

// Setting returns the effective setting.
func (m *Manager) Setting() Setting {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.setting
}

// Reconcile converges the machine on the current setting: it re-runs detection,
// starts what is enabled and installed, stops what is not, and probes what is
// running. It is idempotent and safe to call on every event and every tick.
func (m *Manager) Reconcile(ctx context.Context) error {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()

	logger := klog.FromContext(ctx).WithName(controllerName)
	m.mu.Lock()
	setting := m.setting
	m.mu.Unlock()

	// Detection runs on every pass, not once at startup: a harness installed
	// after the machine joined has to become usable without touching the hub.
	// Under mode explicit the resolution ignores it, so a named-but-missing
	// harness survives as an entry to report rather than being dropped.
	detection := m.opts.Detect()
	wanted := setting.Resolve(detection.Names())

	reasons := map[string][]string{}
	var errs []error

	// Stop first: a harness that is no longer wanted must let go of its port
	// before another one could be offered it.
	m.mu.Lock()
	children := make(map[string]*child, len(m.children))
	for name, c := range m.children {
		children[name] = c
	}
	m.mu.Unlock()
	for name, c := range children {
		if slices.Contains(wanted, name) && detection[name] != "" {
			continue
		}
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
		if err := c.stop(stopCtx); err != nil {
			logger.Error(err, "stopping a runner", "harness", name)
		}
		cancel()
	}

	for _, name := range wanted {
		binary := detection[name]
		if binary == "" {
			reasons[name] = []string{fmt.Sprintf("%s is enabled but its executable is not installed on this machine", name)}
			continue
		}
		c, err := m.childFor(name)
		if err != nil {
			errs = append(errs, err)
			reasons[name] = []string{err.Error()}
			continue
		}
		if err := c.ensure(ctx, binary, permissionLimits{
			mode:         setting.ResolvePermissionMode(),
			allowedTools: setting.AllowedTools,
		}); err != nil {
			errs = append(errs, fmt.Errorf("harness %s: %w", name, err))
			reasons[name] = []string{err.Error()}
			continue
		}
		if err := c.probe(ctx); err != nil {
			reason := err.Error()
			if last := c.lastError(); last != nil {
				reason = last.Error()
			}
			reasons[name] = []string{reason}
			logger.V(2).Info("runner not answering yet", "harness", name, "port", c.port, "err", reason)
		}
	}

	m.mu.Lock()
	m.detected = detection
	m.enabled = wanted
	m.reasons = reasons
	m.mu.Unlock()

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// childFor returns the supervised runner for a harness, creating it (and
// allocating its port) on first use. The port is sticky: a harness switched off
// and on again comes back on the same port, so a cached Service keeps working.
func (m *Manager) childFor(name string) (*child, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.children[name]; ok {
		return c, nil
	}
	port, err := m.allocatePortLocked(name)
	if err != nil {
		return nil, err
	}
	c := newChild(name, port, m.cfg)
	m.children[name] = c
	return c, nil
}

// allocatePortLocked picks the first free loopback port from BasePort that this
// manager has not already handed out. Deterministic order (Names) means one
// machine's claude runner lands on 8787 and its codex runner on 8788, which is
// what makes two harnesses on one host work with no configuration.
func (m *Manager) allocatePortLocked(name string) (int, error) {
	if port, ok := m.ports[name]; ok && port > 0 {
		return port, nil
	}
	taken := map[int]bool{}
	for _, port := range m.ports {
		taken[port] = true
	}
	for port := m.opts.BasePort; port < m.opts.BasePort+64; port++ {
		if taken[port] || !m.opts.PortFree(port) {
			continue
		}
		m.ports[name] = port
		return port, nil
	}
	return 0, fmt.Errorf("no free loopback port for harness %q in %d..%d", name, m.opts.BasePort, m.opts.BasePort+63)
}

// portFree reports whether a loopback port can be bound right now. A port held
// by something else on the machine is skipped rather than fought over.
func portFree(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// Statuses is what the heartbeat publishes on the edge's status.harnesses: one
// entry per harness this build knows, whether or not it is installed or enabled.
// Reporting all of them is the point — "installed but switched off" and "asked
// for but not installed" are both answers an operator needs.
func (m *Manager) Statuses() []HarnessStatus {
	m.mu.Lock()
	detected := m.detected
	enabled := append([]string(nil), m.enabled...)
	reasons := m.reasons
	children := make(map[string]*child, len(m.children))
	for name, c := range m.children {
		children[name] = c
	}
	m.mu.Unlock()

	out := make([]HarnessStatus, 0, len(Names))
	for _, name := range Names {
		status := HarnessStatus{
			Name:     name,
			Detected: detected[name] != "",
			Enabled:  slices.Contains(enabled, name),
			Reasons:  reasons[name],
		}
		if c, ok := children[name]; ok && status.Enabled {
			status.Port = int32(c.port) //nolint:gosec // an allocated loopback port is far inside int32
			observed := c.snapshot()
			status.Version = observed.Version
			status.Ready = observed.Ready && c.supervised()
			if !status.Ready && len(status.Reasons) == 0 {
				status.Reasons = observed.Reasons
			}
		}
		out = append(out, status)
	}
	return out
}

// Services advertises every RUNNING runner over the discovery channel the edges
// provider already pulls, so the runner becomes an ordinary Service with no new
// machinery. auth is none on that Service because the bearer never leaves this
// host: the agent's own /svc proxy injects it (see RunnerToken).
func (m *Manager) Services() []discovery.DiscoveredService {
	m.mu.Lock()
	enabled := append([]string(nil), m.enabled...)
	children := make(map[string]*child, len(m.children))
	for name, c := range m.children {
		children[name] = c
	}
	m.mu.Unlock()

	var out []discovery.DiscoveredService
	for _, name := range Names {
		if !slices.Contains(enabled, name) {
			continue
		}
		c, ok := children[name]
		if !ok || !c.supervised() {
			continue
		}
		observed := c.snapshot()
		out = append(out, discovery.DiscoveredService{
			Name:    name,
			Type:    discovery.ServiceTypeRunner,
			Harness: name,
			Scheme:  "http",
			Port:    int32(c.port), //nolint:gosec // an allocated loopback port is far inside int32
			Version: observed.Version,
			Ready:   observed.Ready,
			Reasons: observed.Reasons,
		})
	}
	return out
}

// RunnerToken returns the bearer for a loopback port this manager supervises.
// The /svc proxy calls it to inject Authorization on the way to a runner, which
// is why the published Service carries no credential: a token the hub never
// holds is a token the hub cannot leak.
//
// A port that is not one of this manager's RUNNING runners gets nothing, so a
// Service pointed at some other local port can never borrow a runner's identity.
func (m *Manager) RunnerToken(port int) (string, bool) {
	m.mu.Lock()
	enabled := append([]string(nil), m.enabled...)
	children := make(map[string]*child, len(m.children))
	for name, c := range m.children {
		children[name] = c
	}
	m.mu.Unlock()
	for name, c := range children {
		if c.port != port || !slices.Contains(enabled, name) {
			continue
		}
		if !c.supervised() {
			return "", false
		}
		if token := c.token(); token != "" {
			return token, true
		}
	}
	return "", false
}

// StopAll stops every supervised runner, keeping all durable state. It uses its
// own context because the agent's is already cancelled by the time shutdown
// runs, and a stop still has to be able to wait out the drain window.
func (m *Manager) StopAll() {
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	m.mu.Lock()
	children := make(map[string]*child, len(m.children))
	for name, c := range m.children {
		children[name] = c
	}
	m.enabled = nil
	m.mu.Unlock()
	for name, c := range children {
		if err := c.stop(stopCtx); err != nil {
			klog.Background().Error(err, "stopping a runner", "harness", name)
		}
	}
}

// AccountDescription is a log-safe description of the runner account.
func (m *Manager) AccountDescription() string {
	if m.opts.Account.Inherited() {
		return "the agent's own account (home " + m.opts.Account.Home + ")"
	}
	return fmt.Sprintf("uid %d gid %d (home %s)", m.opts.Account.UID, m.opts.Account.GID, m.opts.Account.Home)
}

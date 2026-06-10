// Copyright 2025 Fredrik Steen <fredrik@tty.se>
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// version is set via ldflags during build
var version = "dev"

// Prometheus metrics
var (
	nodeEventsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "k8s_node_watcher_events_total",
			Help: "Total number of node events processed by type",
		},
		[]string{"event_type"},
	)

	rendersTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "k8s_node_watcher_renders_total",
			Help: "Total number of template renders by result",
		},
		[]string{"result"},
	)

	commandExecutionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "k8s_node_watcher_command_executions_total",
			Help: "Total number of command executions by result",
		},
		[]string{"result"},
	)

	currentNodeCount = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "k8s_node_watcher_nodes_current",
			Help: "Current number of nodes with an external IP",
		},
	)

	watcherStartTime = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "k8s_node_watcher_start_time_seconds",
			Help: "Unix timestamp of when the watcher started",
		},
	)
)

func init() {
	// Register prom metrics
	prometheus.MustRegister(nodeEventsTotal)
	prometheus.MustRegister(rendersTotal)
	prometheus.MustRegister(commandExecutionsTotal)
	prometheus.MustRegister(currentNodeCount)
	prometheus.MustRegister(watcherStartTime)
}

// Config is the application configuration
type Config struct {
	LogLevel       string   `yaml:"logLevel"`
	KubeConfig     string   `yaml:"kubeConfig"`
	TemplatePath   string   `yaml:"templatePath"`
	OutputPath     string   `yaml:"outputPath"`
	Command        string   `yaml:"command"`
	StaticIPs      []string `yaml:"staticIPs"`
	ResyncInterval int      `yaml:"resyncInterval"` // in seconds
	MinNodeCount   int      `yaml:"minNodeCount"`   // minimum nodes to prevent empty list
	MetricsAddr    string   `yaml:"metricsAddr"`    // address for metrics/health HTTP server
	CommandTimeout int      `yaml:"commandTimeout"` // max command runtime in seconds
}

// NodeData is the template data
type NodeData struct {
	Nodes     []NodeInfo
	StaticIPs []string
	AllIPs    []string
	Timestamp time.Time
}

// NodeInfo contains information about a node
type NodeInfo struct {
	Name       string
	ExternalIP string
}

// defaultDebounce is how long the render loop waits after the first
// pending change so bursts of node events (e.g. the informer replaying
// all nodes at startup) coalesce into a single render+command
const defaultDebounce = 2 * time.Second

// Watcher manages the node watching logic
type Watcher struct {
	config      *Config
	client      kubernetes.Interface
	logger      *slog.Logger
	mu          sync.RWMutex
	currentHash string
	nodeIPs     map[string]string // node name -> external IP
	tmpl        *template.Template
	renderCh    chan struct{} // signals the render loop that state changed
	debounce    time.Duration
}

func main() {
	configFile := flag.String("config", "config.yaml", "Path to configuration file")
	logLevel := flag.String("log-level", "", "Log level (debug, info, warn, error)")
	kubeConfig := flag.String("kubeconfig", "", "Path to kubeconfig file")
	templatePath := flag.String("template", "", "Path to template file")
	outputPath := flag.String("output", "", "Path to output file")
	metricsAddr := flag.String("metrics-addr", "", "Address for metrics and health (default: localhost:8089)")
	showVersion := flag.Bool("version", false, "Show version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	cfg, err := loadConfig(*configFile, *logLevel, *kubeConfig, *templatePath, *outputPath, *metricsAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	logger := setupLogger(cfg.LogLevel)
	logger.Info("Starting k8s-node-external-ip-watcher", "version", version, "config", *configFile)

	// Set start time metric
	watcherStartTime.Set(float64(time.Now().Unix()))

	// Start HTTP local http server for metrics and health checks
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	httpServer, err := startHTTPServer(cfg.MetricsAddr, logger)
	if err != nil {
		logger.Error("Failed to start HTTP server", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP server shutdown error", "error", err)
		}
	}()

	// Create watcher
	watcher, err := NewWatcher(cfg, logger)
	if err != nil {
		logger.Error("Failed to create watcher", "error", err)
		os.Exit(1)
	}

	// Run watcher
	if err := watcher.Run(ctx); err != nil {
		logger.Error("Watcher failed", "error", err)
		os.Exit(1)
	}

	logger.Info("Shutting down gracefully")
}

// loadConfig load configuration from file and applies flag overrides
func loadConfig(configFile, logLevel, kubeConfig, templatePath, outputPath, metricsAddr string) (*Config, error) {
	cfg := &Config{
		LogLevel:       "info",
		ResyncInterval: 300,              // 5 minutes default
		MinNodeCount:   1,                // at least 1 node by default (safety net?)
		MetricsAddr:    "localhost:8089", // default metric listener address
		CommandTimeout: 30,               // 30 seconds default
	}

	// Load from file if it exists
	if _, err := os.Stat(configFile); err == nil {
		data, err := os.ReadFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("read config file: %w", err)
		}

		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config file: %w", err)
		}
	}

	// Apply flag overrides
	if logLevel != "" {
		cfg.LogLevel = logLevel
	}
	if kubeConfig != "" {
		cfg.KubeConfig = kubeConfig
	}
	if templatePath != "" {
		cfg.TemplatePath = templatePath
	}
	if outputPath != "" {
		cfg.OutputPath = outputPath
	}
	if metricsAddr != "" {
		cfg.MetricsAddr = metricsAddr
	}

	// Validate required fields
	if cfg.TemplatePath == "" {
		return nil, fmt.Errorf("templatePath is required")
	}
	if cfg.OutputPath == "" {
		return nil, fmt.Errorf("outputPath is required")
	}
	if cfg.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	if cfg.ResyncInterval < 0 {
		return nil, fmt.Errorf("resyncInterval must not be negative, got %d", cfg.ResyncInterval)
	}
	if cfg.MinNodeCount < 0 {
		return nil, fmt.Errorf("minNodeCount must not be negative, got %d", cfg.MinNodeCount)
	}
	if cfg.CommandTimeout <= 0 {
		return nil, fmt.Errorf("commandTimeout must be positive, got %d", cfg.CommandTimeout)
	}

	return cfg, nil
}

// setupLogger creates a logger with the specified level
func setupLogger(level string) *slog.Logger {
	var logLevel slog.Level

	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: logLevel,
	}

	handler := slog.NewTextHandler(os.Stdout, opts)
	return slog.New(handler)
}

// startHTTPServer starts the HTTP server for metrics and health endpoints.
// The listener is opened synchronously so a bad address fails at startup;
// the returned server's Addr holds the actual listen address
func startHTTPServer(addr string, logger *slog.Logger) (*http.Server, error) {
	mux := http.NewServeMux()

	// Simple 200 OK health check endpoint
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	// Metrics endpoint
	mux.Handle("/metrics", promhttp.Handler())

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}

	server := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("Starting HTTP server", "addr", server.Addr)
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
		}
	}()

	return server, nil
}

// buildRestConfig resolves a kubeconfig path (explicit config, KUBECONFIG env
// var, then ~/.kube/config) and falls back to in-cluster configuration when
// no kubeconfig file is found
func buildRestConfig(kubeConfig string) (*rest.Config, error) {
	if kubeConfig == "" {
		kubeConfig = os.Getenv("KUBECONFIG")
	}
	if kubeConfig == "" {
		if homeDir, err := os.UserHomeDir(); err == nil {
			candidate := filepath.Join(homeDir, ".kube", "config")
			if _, err := os.Stat(candidate); err == nil {
				kubeConfig = candidate
			}
		}
	}

	if kubeConfig == "" {
		config, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("no kubeconfig found and in-cluster config failed: %w", err)
		}
		return config, nil
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("build kubeconfig from %s: %w", kubeConfig, err)
	}
	return config, nil
}

// NewWatcher creates a new API Watcher instance
func NewWatcher(cfg *Config, logger *slog.Logger) (*Watcher, error) {
	config, err := buildRestConfig(cfg.KubeConfig)
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}

	tmpl, err := template.ParseFiles(cfg.TemplatePath)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	return &Watcher{
		config:   cfg,
		client:   clientset,
		logger:   logger,
		nodeIPs:  make(map[string]string),
		tmpl:     tmpl,
		renderCh: make(chan struct{}, 1),
		debounce: defaultDebounce,
	}, nil
}

// nodeFromObj extracts a Node from an informer event object, unwrapping
// DeletedFinalStateUnknown tombstones delivered when a delete was missed.
func nodeFromObj(obj any) (*corev1.Node, bool) {
	if node, ok := obj.(*corev1.Node); ok {
		return node, true
	}
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		node, ok := tombstone.Obj.(*corev1.Node)
		return node, ok
	}
	return nil, false
}

// Run starts the watcher
func (w *Watcher) Run(ctx context.Context) error {
	w.logger.Info("Starting node watcher")

	// Create informer factory
	factory := informers.NewSharedInformerFactory(w.client, time.Duration(w.config.ResyncInterval)*time.Second)
	nodeInformer := factory.Core().V1().Nodes().Informer()

	// Add event handlers for node events
	// TODO:
	// 	- Watch for NodeNotReady conditions?
	//  - Handle taints?
	//  - Handle node cordoning/draining?
	_, err := nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if node, ok := nodeFromObj(obj); ok {
				w.handleNodeEvent("ADD", node)
			} else {
				w.logger.Warn("Received non-node object in ADD event")
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			if node, ok := nodeFromObj(newObj); ok {
				w.handleNodeEvent("UPDATE", node)
			} else {
				w.logger.Warn("Received non-node object in UPDATE event")
			}
		},
		DeleteFunc: func(obj any) {
			if node, ok := nodeFromObj(obj); ok {
				w.handleNodeEvent("DELETE", node)
			} else {
				w.logger.Warn("Received non-node object in DELETE event")
			}
		},
	})
	if err != nil {
		return fmt.Errorf("add event handler: %w", err)
	}

	// Start the debounced render loop; node events only signal it
	go w.renderLoop(ctx)

	// Start informer. It replays every existing node as an ADD event,
	// so no separate initial sync is needed
	factory.Start(ctx.Done())

	// Wait for cache sync
	w.logger.Info("Waiting for cache sync")
	if !cache.WaitForCacheSync(ctx.Done(), nodeInformer.HasSynced) {
		return fmt.Errorf("failed to sync cache")
	}

	w.logger.Info("Cache synced, watching for node changes")

	<-ctx.Done()
	return nil
}

// renderLoop renders and executes after node changes, debouncing bursts of
// events into a single render
func (w *Watcher) renderLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}
	pending := false

	for {
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-w.renderCh:
			if !pending {
				timer.Reset(w.debounce)
				pending = true
			}
		case <-timer.C:
			pending = false
			if err := w.renderAndExecute(); err != nil {
				w.logger.Error("Failed to render and execute", "error", err)
			}
		}
	}
}

// signalRender wakes the render loop without blocking; if a render is
// already pending the signal is coalesced
func (w *Watcher) signalRender() {
	select {
	case w.renderCh <- struct{}{}:
	default:
	}
}

// handleNodeEvent processes node events
func (w *Watcher) handleNodeEvent(eventType string, node *corev1.Node) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Increment event counter
	nodeEventsTotal.WithLabelValues(eventType).Inc()

	nodeName := node.Name
	oldIP := w.nodeIPs[nodeName]

	// Extract external IP
	var newIP string
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeExternalIP {
			newIP = addr.Address
			break
		}
	}

	w.logger.Debug("Node event received",
		"type", eventType,
		"node", nodeName,
		"oldIP", oldIP,
		"newIP", newIP,
	)

	// Update internal state
	changed := false
	if eventType == "DELETE" {
		if _, exists := w.nodeIPs[nodeName]; exists {
			delete(w.nodeIPs, nodeName)
			changed = true
			w.logger.Info("Node removed", "node", nodeName, "ip", oldIP)
		}
	} else if newIP != "" {
		if oldIP != newIP {
			w.nodeIPs[nodeName] = newIP
			changed = true
			if oldIP == "" {
				w.logger.Info("New node added", "node", nodeName, "ip", newIP)
			} else {
				w.logger.Info("Node IP changed", "node", nodeName, "oldIP", oldIP, "newIP", newIP)
			}
		}
	} else if oldIP != "" {
		// Safety net: a node that loses its external IP keeps its last
		// known IP until the node object is deleted, so a transient
		// cloud-provider hiccup never drops it from the rendered list
		w.logger.Warn("Node lost its external IP, retaining last known IP until node is deleted",
			"node", nodeName, "ip", oldIP)
	}

	// Update node count gauge
	currentNodeCount.Set(float64(len(w.nodeIPs)))

	// If nothing changed, skip rendering
	if !changed {
		w.logger.Debug("No IP changes detected, skipping render")
		return
	}

	// Wake the debounced render loop
	w.signalRender()
}

// renderAndExecute renders the template and executes the command.
// It snapshots state under the read lock so event handling is never
// blocked by file writes or command execution
func (w *Watcher) renderAndExecute() error {
	// Build node data from a snapshot of current state
	w.mu.RLock()
	nodes := make([]NodeInfo, 0, len(w.nodeIPs))
	for name, ip := range w.nodeIPs {
		nodes = append(nodes, NodeInfo{
			Name:       name,
			ExternalIP: ip,
		})
	}
	w.mu.RUnlock()

	// Sort by node name so the rendered output is deterministic
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})

	allIPs := make([]string, 0, len(nodes)+len(w.config.StaticIPs))
	for _, node := range nodes {
		allIPs = append(allIPs, node.ExternalIP)
	}

	// Safety check: never render an empty/short node list
	if len(nodes) < w.config.MinNodeCount {
		w.logger.Warn("Safety check failed: node count below minimum, skipping render",
			"current", len(nodes),
			"minimum", w.config.MinNodeCount,
		)
		return nil
	}

	// Add our static IPs
	allIPs = append(allIPs, w.config.StaticIPs...)

	data := NodeData{
		Nodes:     nodes,
		StaticIPs: w.config.StaticIPs,
		AllIPs:    allIPs,
		Timestamp: time.Now(),
	}

	// Calculate hash to compare with previous render
	dataHash := w.calculateHash(data)
	if dataHash == w.currentHash {
		w.logger.Debug("Data hash unchanged, skipping render")
		return nil
	}

	// Render template to a buffer first so a failed render never
	// touches the existing output file
	w.logger.Info("Rendering template", "output", w.config.OutputPath, "nodeCount", len(nodes))

	var buf bytes.Buffer
	if err := w.tmpl.Execute(&buf, data); err != nil {
		rendersTotal.WithLabelValues("failure").Inc()
		return fmt.Errorf("execute template: %w", err)
	}

	// Write to a temp file in the same directory and rename it over the
	// output path so consumers never see a partial file
	if err := w.writeOutputAtomic(buf.Bytes()); err != nil {
		rendersTotal.WithLabelValues("failure").Inc()
		return err
	}

	w.currentHash = dataHash
	rendersTotal.WithLabelValues("success").Inc()

	// Execute command
	return w.executeCommand()
}

// writeOutputAtomic writes content to a temp file in the output directory,
// fsyncs it and renames it over the output path
func (w *Watcher) writeOutputAtomic(content []byte) error {
	dir := filepath.Dir(w.config.OutputPath)
	tmpFile, err := os.CreateTemp(dir, ".node-ips-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp output file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName) // no-op after successful rename

	if _, err := tmpFile.Write(content); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write temp output file: %w", err)
	}
	if err := tmpFile.Chmod(0644); err != nil {
		tmpFile.Close()
		return fmt.Errorf("chmod temp output file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("sync temp output file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp output file: %w", err)
	}

	if err := os.Rename(tmpName, w.config.OutputPath); err != nil {
		return fmt.Errorf("rename temp output file: %w", err)
	}
	return nil
}

func (w *Watcher) calculateHash(data NodeData) string {
	h := sha256.New()

	// Sort nodes for consistent hashing
	nodes := make([]NodeInfo, len(data.Nodes))
	copy(nodes, data.Nodes)
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})

	// Separate fields with a NUL byte so distinct states never
	// concatenate to the same byte stream
	sep := []byte{0}

	for _, node := range nodes {
		h.Write([]byte(node.Name))
		h.Write(sep)
		h.Write([]byte(node.ExternalIP))
		h.Write(sep)
	}

	// Section marker between nodes and static IPs
	h.Write([]byte{1})

	// Sort IPs for consistent hashing
	ips := make([]string, len(data.StaticIPs))
	copy(ips, data.StaticIPs)
	sort.Strings(ips)

	for _, ip := range ips {
		h.Write([]byte(ip))
		h.Write(sep)
	}

	return hex.EncodeToString(h.Sum(nil))
}

// executeCommand runs the configured command with the output file as argument
func (w *Watcher) executeCommand() error {
	w.logger.Info("Executing command",
		"command", w.config.Command,
		"arg", w.config.OutputPath,
	)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(w.config.CommandTimeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, w.config.Command, w.config.OutputPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		commandExecutionsTotal.WithLabelValues("failure").Inc()
		return fmt.Errorf("execute command: %w", err)
	}

	commandExecutionsTotal.WithLabelValues("success").Inc()
	w.logger.Info("Command executed successfully")
	return nil
}

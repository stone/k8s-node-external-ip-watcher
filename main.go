// Copyright 2025 Fredrik Steen <fredrik@tty.se>
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// version is set via ldflags during build
var version = "dev"

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

// Watcher manages the node watching logic
type Watcher struct {
	config      *Config
	client      kubernetes.Interface
	logger      *slog.Logger
	mu          sync.RWMutex
	currentHash string
	nodeIPs     map[string]string // node name -> external IP
	tmpl        *template.Template
}

func main() {
	configFile := flag.String("config", "config.yaml", "Path to configuration file")
	logLevel := flag.String("log-level", "", "Log level (debug, info, warn, error)")
	kubeConfig := flag.String("kubeconfig", "", "Path to kubeconfig file")
	templatePath := flag.String("template", "", "Path to template file")
	outputPath := flag.String("output", "", "Path to output file")
	showVersion := flag.Bool("version", false, "Show version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	cfg, err := loadConfig(*configFile, *logLevel, *kubeConfig, *templatePath, *outputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	logger := setupLogger(cfg.LogLevel)
	logger.Info("Starting k8s-node-external-ip-watcher", "version", version, "config", *configFile)

	// Create watcher
	watcher, err := NewWatcher(cfg, logger)
	if err != nil {
		logger.Error("Failed to create watcher", "error", err)
		os.Exit(1)
	}

	// Run watcher
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := watcher.Run(ctx); err != nil {
		logger.Error("Watcher failed", "error", err)
		os.Exit(1)
	}

	logger.Info("Shutting down gracefully")
}

// loadConfig load configuration from file and applies flag overrides
func loadConfig(configFile, logLevel, kubeConfig, templatePath, outputPath string) (*Config, error) {
	cfg := &Config{
		LogLevel:       "info",
		ResyncInterval: 300, // 5 minutes default
		MinNodeCount:   1,   // at least 1 node by default (saftey net?)
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

// NewWatcher creates a new API Watcher instance
func NewWatcher(cfg *Config, logger *slog.Logger) (*Watcher, error) {
	kubeconfig := cfg.KubeConfig
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			homeDir, err := os.UserHomeDir()
			if err == nil {
				kubeconfig = filepath.Join(homeDir, ".kube", "config")
			}
		}
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("build kubeconfig: %w", err)
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
		config:  cfg,
		client:  clientset,
		logger:  logger,
		nodeIPs: make(map[string]string),
		tmpl:    tmpl,
	}, nil
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
			node := obj.(*corev1.Node)
			w.handleNodeEvent("ADD", node)
		},
		UpdateFunc: func(oldObj, newObj any) {
			node := newObj.(*corev1.Node)
			w.handleNodeEvent("UPDATE", node)
		},
		DeleteFunc: func(obj any) {
			node := obj.(*corev1.Node)
			w.handleNodeEvent("DELETE", node)
		},
	})
	if err != nil {
		return fmt.Errorf("add event handler: %w", err)
	}

	// Start informer
	factory.Start(ctx.Done())

	// Wait for cache sync
	w.logger.Info("Waiting for cache sync")
	if !cache.WaitForCacheSync(ctx.Done(), nodeInformer.HasSynced) {
		return fmt.Errorf("failed to sync cache")
	}

	w.logger.Info("Cache synced, performing initial sync")

	// Perform initial sync to get all current nodes
	// This will not fail even if there are no nodes yet
	if err := w.initialSync(nodeInformer); err != nil {
		w.logger.Error("Initial sync failed, continuing to watch", "error", err)
	} else {
		w.logger.Info("Initial sync complete, watching for node changes")
	}

	<-ctx.Done()
	return nil
}

// initialSync fetches all current nodes and renders the initial template
func (w *Watcher) initialSync(informer cache.SharedIndexInformer) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// List all nodes from informer's store
	items := informer.GetStore().List()
	w.logger.Info("Initial node discovery", "count", len(items))

	// Extract external IPs from all nodes
	for _, item := range items {
		node, ok := item.(*corev1.Node)
		if !ok {
			w.logger.Warn("Unexpected object type in store")
			continue
		}
		var externalIP string
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeExternalIP {
				externalIP = addr.Address
				break
			}
		}

		if externalIP != "" {
			w.nodeIPs[node.Name] = externalIP
			w.logger.Info("Discovered node", "node", node.Name, "ip", externalIP)
		} else {
			w.logger.Debug("Node has no external IP", "node", node.Name)
		}
	}

	// Check minimum node count (warning only, don't fail on startup)
	if len(w.nodeIPs) < w.config.MinNodeCount {
		w.logger.Warn("Node count below minimum, skipping initial render",
			"current", len(w.nodeIPs),
			"minimum", w.config.MinNodeCount,
		)
		return nil
	}

	// Render and execute for initial state
	if len(w.nodeIPs) > 0 {
		if err := w.renderAndExecute(); err != nil {
			w.logger.Error("Initial render failed, will retry on node changes", "error", err)
			// Don't return error - continue watching
		}
	}

	return nil
}

// handleNodeEvent processes node events
func (w *Watcher) handleNodeEvent(eventType string, node *corev1.Node) {
	w.mu.Lock()
	defer w.mu.Unlock()

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
	}

	// If nothing changed, skip rendering
	if !changed {
		w.logger.Debug("No IP changes detected, skipping render")
		return
	}

	// Safety check: prevent removing all nodes
	if len(w.nodeIPs) < w.config.MinNodeCount {
		w.logger.Error("Safety check failed: node count below minimum",
			"current", len(w.nodeIPs),
			"minimum", w.config.MinNodeCount,
		)
		return
	}

	// Render and execute
	if err := w.renderAndExecute(); err != nil {
		w.logger.Error("Failed to render and execute", "error", err)
	}
}

// renderAndExecute renders the template and executes the command
func (w *Watcher) renderAndExecute() error {
	// Build node data
	nodes := make([]NodeInfo, 0, len(w.nodeIPs))
	allIPs := make([]string, 0, len(w.nodeIPs)+len(w.config.StaticIPs))

	for name, ip := range w.nodeIPs {
		nodes = append(nodes, NodeInfo{
			Name:       name,
			ExternalIP: ip,
		})
		allIPs = append(allIPs, ip)
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

	// Render template to file
	w.logger.Info("Rendering template", "output", w.config.OutputPath, "nodeCount", len(nodes))

	outputFile, err := os.Create(w.config.OutputPath)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer outputFile.Close()

	if err := w.tmpl.Execute(outputFile, data); err != nil {
		return fmt.Errorf("execute template: %w", err)
	}

	if err := outputFile.Sync(); err != nil {
		return fmt.Errorf("sync output file: %w", err)
	}

	w.currentHash = dataHash

	// Execute command
	return w.executeCommand()
}

func (w *Watcher) calculateHash(data NodeData) string {
	h := sha256.New()

	// Sort nodes for consistent hashing
	nodes := make([]NodeInfo, len(data.Nodes))
	copy(nodes, data.Nodes)
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})

	for _, node := range nodes {
		h.Write([]byte(node.Name))
		h.Write([]byte(node.ExternalIP))
	}

	// Sort IPs for consistent hashing
	ips := make([]string, len(data.StaticIPs))
	copy(ips, data.StaticIPs)
	sort.Strings(ips)

	for _, ip := range ips {
		h.Write([]byte(ip))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// executeCommand runs the configured command with the output file as argument
func (w *Watcher) executeCommand() error {
	w.logger.Info("Executing command",
		"command", w.config.Command,
		"arg", w.config.OutputPath,
	)

	cmd := exec.Command(w.config.Command, w.config.OutputPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("execute command: %w", err)
	}

	w.logger.Info("Command executed successfully")
	return nil
}

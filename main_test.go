package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

func TestNodeFromObj(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}

	t.Run("plain node object", func(t *testing.T) {
		got, ok := nodeFromObj(node)
		if !ok || got != node {
			t.Errorf("expected node back, got %v ok=%v", got, ok)
		}
	})

	t.Run("tombstone wrapping a node", func(t *testing.T) {
		tombstone := cache.DeletedFinalStateUnknown{Key: "node1", Obj: node}
		got, ok := nodeFromObj(tombstone)
		if !ok || got != node {
			t.Errorf("expected node from tombstone, got %v ok=%v", got, ok)
		}
	})

	t.Run("tombstone wrapping garbage", func(t *testing.T) {
		tombstone := cache.DeletedFinalStateUnknown{Key: "x", Obj: "not a node"}
		got, ok := nodeFromObj(tombstone)
		if ok || got != nil {
			t.Errorf("expected nil,false for garbage tombstone, got %v ok=%v", got, ok)
		}
	})

	t.Run("unrelated type", func(t *testing.T) {
		got, ok := nodeFromObj(42)
		if ok || got != nil {
			t.Errorf("expected nil,false for unrelated type, got %v ok=%v", got, ok)
		}
	})
}

func TestCalculateHash(t *testing.T) {
	w := &Watcher{}

	t.Run("identical data must produce same hash", func(t *testing.T) {
		data1 := NodeData{
			Nodes: []NodeInfo{
				{Name: "node1", ExternalIP: "1.2.3.4"},
				{Name: "node2", ExternalIP: "5.6.7.8"},
			},
			StaticIPs: []string{"10.0.0.1", "10.0.0.2"},
			Timestamp: time.Now(), // Timestamp should NOT affect hash
		}

		data2 := NodeData{
			Nodes: []NodeInfo{
				{Name: "node1", ExternalIP: "1.2.3.4"},
				{Name: "node2", ExternalIP: "5.6.7.8"},
			},
			StaticIPs: []string{"10.0.0.1", "10.0.0.2"},
			Timestamp: time.Now().Add(1 * time.Hour), // Different timestamp
		}

		hash1 := w.calculateHash(data1)
		hash2 := w.calculateHash(data2)

		if hash1 != hash2 {
			t.Errorf("identical data produced different hashes:\n%s\n%s", hash1, hash2)
		}
	})

	t.Run("order not affecting hash", func(t *testing.T) {
		data1 := NodeData{
			Nodes: []NodeInfo{
				{Name: "node1", ExternalIP: "1.2.3.4"},
				{Name: "node2", ExternalIP: "5.6.7.8"},
			},
			StaticIPs: []string{"10.0.0.1"},
		}

		data2 := NodeData{
			Nodes: []NodeInfo{
				{Name: "node2", ExternalIP: "5.6.7.8"},
				{Name: "node1", ExternalIP: "1.2.3.4"},
			},
			StaticIPs: []string{"10.0.0.1"},
		}

		hash1 := w.calculateHash(data1)
		hash2 := w.calculateHash(data2)

		if hash1 != hash2 {
			t.Errorf("node order affected hash:\n%s\n%s", hash1, hash2)
		}
	})

	t.Run("static IP order not affecting hash", func(t *testing.T) {
		data1 := NodeData{
			Nodes:     []NodeInfo{{Name: "node1", ExternalIP: "1.2.3.4"}},
			StaticIPs: []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"},
		}

		data2 := NodeData{
			Nodes:     []NodeInfo{{Name: "node1", ExternalIP: "1.2.3.4"}},
			StaticIPs: []string{"10.0.0.3", "10.0.0.1", "10.0.0.2"},
		}

		hash1 := w.calculateHash(data1)
		hash2 := w.calculateHash(data2)

		if hash1 != hash2 {
			t.Errorf("static IP order affected hash:\n%s\n%s", hash1, hash2)
		}
	})

	t.Run("different node IP produces different hash", func(t *testing.T) {
		data1 := NodeData{
			Nodes:     []NodeInfo{{Name: "node1", ExternalIP: "1.2.3.4"}},
			StaticIPs: []string{"10.0.0.1"},
		}

		data2 := NodeData{
			Nodes:     []NodeInfo{{Name: "node1", ExternalIP: "1.2.3.5"}},
			StaticIPs: []string{"10.0.0.1"},
		}

		hash1 := w.calculateHash(data1)
		hash2 := w.calculateHash(data2)

		if hash1 == hash2 {
			t.Errorf("different node IP produced same hash: %s", hash1)
		}
	})

	t.Run("different node name produces different hash", func(t *testing.T) {
		data1 := NodeData{
			Nodes:     []NodeInfo{{Name: "node1", ExternalIP: "1.2.3.4"}},
			StaticIPs: []string{"10.0.0.1"},
		}

		data2 := NodeData{
			Nodes:     []NodeInfo{{Name: "node2", ExternalIP: "1.2.3.4"}},
			StaticIPs: []string{"10.0.0.1"},
		}

		hash1 := w.calculateHash(data1)
		hash2 := w.calculateHash(data2)

		if hash1 == hash2 {
			t.Errorf("different node name produced same hash: %s", hash1)
		}
	})

	t.Run("different static IP produces different hash", func(t *testing.T) {
		data1 := NodeData{
			Nodes:     []NodeInfo{{Name: "node1", ExternalIP: "1.2.3.4"}},
			StaticIPs: []string{"10.0.0.1"},
		}

		data2 := NodeData{
			Nodes:     []NodeInfo{{Name: "node1", ExternalIP: "1.2.3.4"}},
			StaticIPs: []string{"10.0.0.2"},
		}

		hash1 := w.calculateHash(data1)
		hash2 := w.calculateHash(data2)

		if hash1 == hash2 {
			t.Errorf("different static IP produced same hash: %s", hash1)
		}
	})

	t.Run("field boundaries affect hash (no concatenation collisions)", func(t *testing.T) {
		// Without separators both hash the bytes "node110.0.0.1"
		data1 := NodeData{
			Nodes: []NodeInfo{{Name: "node1", ExternalIP: "10.0.0.1"}},
		}

		data2 := NodeData{
			Nodes: []NodeInfo{{Name: "node11", ExternalIP: "0.0.0.1"}},
		}

		hash1 := w.calculateHash(data1)
		hash2 := w.calculateHash(data2)

		if hash1 == hash2 {
			t.Errorf("different data produced same hash (missing field separator): %s", hash1)
		}
	})

	t.Run("node section and static IP section are distinct", func(t *testing.T) {
		// A node entry must not collide with a static IP carrying the same bytes
		data1 := NodeData{
			Nodes:     []NodeInfo{{Name: "node1", ExternalIP: "1.2.3.4"}},
			StaticIPs: []string{},
		}

		data2 := NodeData{
			Nodes:     []NodeInfo{},
			StaticIPs: []string{"node1", "1.2.3.4"},
		}

		hash1 := w.calculateHash(data1)
		hash2 := w.calculateHash(data2)

		if hash1 == hash2 {
			t.Errorf("node entry collided with static IPs: %s", hash1)
		}
	})

	t.Run("empty data produces consistent hash", func(t *testing.T) {
		data1 := NodeData{
			Nodes:     []NodeInfo{},
			StaticIPs: []string{},
		}

		data2 := NodeData{
			Nodes:     []NodeInfo{},
			StaticIPs: []string{},
		}

		hash1 := w.calculateHash(data1)
		hash2 := w.calculateHash(data2)

		if hash1 != hash2 {
			t.Errorf("empty data produced different hashes:\n%s\n%s", hash1, hash2)
		}

		if hash1 == "" {
			t.Error("hash should not be empty string")
		}
	})
}

// newTestWatcher builds a Watcher suitable for unit tests, with the template
// parsed from a string and a no-op command.
func newTestWatcher(t *testing.T, tmplText string, cfg *Config) *Watcher {
	t.Helper()
	tmpl, err := template.New("test").Parse(tmplText)
	if err != nil {
		t.Fatalf("parse test template: %v", err)
	}
	if cfg.Command == "" {
		cfg.Command = "true"
	}
	if cfg.MinNodeCount == 0 {
		cfg.MinNodeCount = 1
	}
	if cfg.CommandTimeout == 0 {
		cfg.CommandTimeout = 30
	}
	return &Watcher{
		config:   cfg,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		nodeIPs:  map[string]string{"node1": "1.2.3.4"},
		tmpl:     tmpl,
		renderCh: make(chan struct{}, 1),
		debounce: defaultDebounce,
	}
}

func TestRenderAndExecute(t *testing.T) {
	t.Run("failed render preserves previous output file", func(t *testing.T) {
		outputPath := filepath.Join(t.TempDir(), "out.conf")
		previous := "previous good content"
		if err := os.WriteFile(outputPath, []byte(previous), 0644); err != nil {
			t.Fatal(err)
		}

		w := newTestWatcher(t, "{{.NoSuchField}}", &Config{OutputPath: outputPath})

		if err := w.renderAndExecute(); err == nil {
			t.Fatal("expected render error, got nil")
		}

		got, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("read output file: %v", err)
		}
		if string(got) != previous {
			t.Errorf("previous output destroyed by failed render: got %q", string(got))
		}
	})

	t.Run("successful render writes expected content", func(t *testing.T) {
		dir := t.TempDir()
		outputPath := filepath.Join(dir, "out.conf")

		w := newTestWatcher(t, "{{range .AllIPs}}{{.}}\n{{end}}", &Config{OutputPath: outputPath})

		if err := w.renderAndExecute(); err != nil {
			t.Fatalf("renderAndExecute: %v", err)
		}

		got, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("read output file: %v", err)
		}
		if string(got) != "1.2.3.4\n" {
			t.Errorf("unexpected output content: %q", string(got))
		}

		info, err := os.Stat(outputPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0644 {
			t.Errorf("expected mode 0644, got %v", info.Mode().Perm())
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("expected only the output file in dir, found %d entries", len(entries))
		}
	})
}

const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://127.0.0.1:6443
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user: {}
`

func TestBuildRestConfig(t *testing.T) {
	t.Run("falls back to in-cluster config when no kubeconfig found", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("KUBECONFIG", "")

		_, err := buildRestConfig("")
		if !errors.Is(err, rest.ErrNotInCluster) {
			t.Errorf("expected in-cluster fallback (ErrNotInCluster outside a cluster), got: %v", err)
		}
	})

	t.Run("uses explicit kubeconfig path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "kubeconfig")
		if err := os.WriteFile(path, []byte(testKubeconfig), 0600); err != nil {
			t.Fatal(err)
		}

		cfg, err := buildRestConfig(path)
		if err != nil {
			t.Fatalf("buildRestConfig: %v", err)
		}
		if cfg.Host != "https://127.0.0.1:6443" {
			t.Errorf("unexpected host: %s", cfg.Host)
		}
	})

	t.Run("uses KUBECONFIG env var", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "kubeconfig")
		if err := os.WriteFile(path, []byte(testKubeconfig), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("KUBECONFIG", path)

		cfg, err := buildRestConfig("")
		if err != nil {
			t.Fatalf("buildRestConfig: %v", err)
		}
		if cfg.Host != "https://127.0.0.1:6443" {
			t.Errorf("unexpected host: %s", cfg.Host)
		}
	})
}

func TestExecuteCommandTimeout(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hang.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 30\n"), 0755); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t, "x", &Config{
		OutputPath:     filepath.Join(dir, "out.conf"),
		Command:        script,
		CommandTimeout: 1,
	})

	start := time.Now()
	err := w.executeCommand()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from timed-out command, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("command did not time out promptly, took %v", elapsed)
	}
}

func testNode(name, externalIP string) *corev1.Node {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if externalIP != "" {
		node.Status.Addresses = []corev1.NodeAddress{
			{Type: corev1.NodeExternalIP, Address: externalIP},
		}
	}
	return node
}

func TestDebouncedRender(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")
	script := filepath.Join(dir, "count.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho run >> "+countFile+"\n"), 0755); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t, "{{range .AllIPs}}{{.}}\n{{end}}", &Config{
		OutputPath: filepath.Join(dir, "out.conf"),
		Command:    script,
	})
	w.nodeIPs = map[string]string{}
	w.debounce = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.renderLoop(ctx)

	// Burst of events (like informer replay at startup) must coalesce
	// into a single render+command
	for i := range 5 {
		w.handleNodeEvent("ADD", testNode(fmt.Sprintf("node%d", i), fmt.Sprintf("10.0.0.%d", i)))
	}

	time.Sleep(300 * time.Millisecond)

	data, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("command never ran: %v", err)
	}
	if got := strings.Count(string(data), "run"); got != 1 {
		t.Errorf("expected exactly 1 command execution for event burst, got %d", got)
	}

	// A later change triggers exactly one more render
	w.handleNodeEvent("UPDATE", testNode("node0", "10.0.1.99"))
	time.Sleep(300 * time.Millisecond)

	data, err = os.ReadFile(countFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "run"); got != 2 {
		t.Errorf("expected 2 command executions after second change, got %d", got)
	}
}

func TestNodeLosesExternalIP(t *testing.T) {
	var logBuf strings.Builder

	w := newTestWatcher(t, "x", &Config{OutputPath: filepath.Join(t.TempDir(), "out.conf")})
	w.logger = slog.New(slog.NewTextHandler(&logBuf, nil))
	w.nodeIPs = map[string]string{"node1": "1.2.3.4"}

	w.handleNodeEvent("UPDATE", testNode("node1", ""))

	// Safety net: the last known IP is retained so a transient cloud-provider
	// hiccup never drops a node from the rendered list
	if got := w.nodeIPs["node1"]; got != "1.2.3.4" {
		t.Errorf("expected stale IP to be retained, got %q", got)
	}

	// But the operator must be told about it
	if !strings.Contains(logBuf.String(), "level=WARN") {
		t.Errorf("expected a WARN log when a node loses its external IP, logs:\n%s", logBuf.String())
	}
}

func TestLoadConfig(t *testing.T) {
	writeConfig := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	valid := "templatePath: tmpl\noutputPath: out\ncommand: /bin/echo\n"

	t.Run("defaults are applied", func(t *testing.T) {
		cfg, err := loadConfig(writeConfig(t, valid), "", "", "", "", "")
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.LogLevel != "info" || cfg.ResyncInterval != 300 || cfg.MinNodeCount != 1 ||
			cfg.MetricsAddr != "localhost:8089" || cfg.CommandTimeout != 30 {
			t.Errorf("unexpected defaults: %+v", cfg)
		}
	})

	t.Run("missing required fields fail", func(t *testing.T) {
		for _, content := range []string{
			"outputPath: out\ncommand: c\n",    // no templatePath
			"templatePath: t\ncommand: c\n",    // no outputPath
			"templatePath: t\noutputPath: o\n", // no command
		} {
			if _, err := loadConfig(writeConfig(t, content), "", "", "", "", ""); err == nil {
				t.Errorf("expected error for config %q, got nil", content)
			}
		}
	})

	t.Run("flag overrides beat file values", func(t *testing.T) {
		cfg, err := loadConfig(writeConfig(t, valid), "debug", "/kc", "/tmpl2", "/out2", "0.0.0.0:9999")
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.LogLevel != "debug" || cfg.KubeConfig != "/kc" || cfg.TemplatePath != "/tmpl2" ||
			cfg.OutputPath != "/out2" || cfg.MetricsAddr != "0.0.0.0:9999" {
			t.Errorf("flag overrides not applied: %+v", cfg)
		}
	})

	t.Run("negative or zero numeric values are rejected", func(t *testing.T) {
		for _, content := range []string{
			valid + "resyncInterval: -1\n",
			valid + "minNodeCount: -1\n",
			valid + "commandTimeout: 0\n",
			valid + "commandTimeout: -5\n",
		} {
			if _, err := loadConfig(writeConfig(t, content), "", "", "", "", ""); err == nil {
				t.Errorf("expected validation error for config %q, got nil", content)
			}
		}
	})

	t.Run("minNodeCount zero is allowed to disable the safety check", func(t *testing.T) {
		cfg, err := loadConfig(writeConfig(t, valid+"minNodeCount: 0\n"), "", "", "", "", "")
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.MinNodeCount != 0 {
			t.Errorf("expected minNodeCount 0, got %d", cfg.MinNodeCount)
		}
	})
}

func TestHandleNodeEvent(t *testing.T) {
	newWatcher := func(t *testing.T) *Watcher {
		w := newTestWatcher(t, "x", &Config{OutputPath: filepath.Join(t.TempDir(), "out.conf")})
		w.nodeIPs = map[string]string{}
		return w
	}

	signalled := func(w *Watcher) bool {
		select {
		case <-w.renderCh:
			return true
		default:
			return false
		}
	}

	t.Run("ADD with external IP stores node and signals render", func(t *testing.T) {
		w := newWatcher(t)
		w.handleNodeEvent("ADD", testNode("node1", "1.2.3.4"))
		if w.nodeIPs["node1"] != "1.2.3.4" {
			t.Errorf("node not stored: %v", w.nodeIPs)
		}
		if !signalled(w) {
			t.Error("expected render signal")
		}
	})

	t.Run("ADD without external IP is ignored", func(t *testing.T) {
		w := newWatcher(t)
		w.handleNodeEvent("ADD", testNode("node1", ""))
		if len(w.nodeIPs) != 0 {
			t.Errorf("expected no nodes stored, got %v", w.nodeIPs)
		}
		if signalled(w) {
			t.Error("expected no render signal")
		}
	})

	t.Run("UPDATE with changed IP replaces it and signals", func(t *testing.T) {
		w := newWatcher(t)
		w.nodeIPs["node1"] = "1.2.3.4"
		w.handleNodeEvent("UPDATE", testNode("node1", "5.6.7.8"))
		if w.nodeIPs["node1"] != "5.6.7.8" {
			t.Errorf("IP not updated: %v", w.nodeIPs)
		}
		if !signalled(w) {
			t.Error("expected render signal")
		}
	})

	t.Run("UPDATE with same IP does not signal", func(t *testing.T) {
		w := newWatcher(t)
		w.nodeIPs["node1"] = "1.2.3.4"
		w.handleNodeEvent("UPDATE", testNode("node1", "1.2.3.4"))
		if signalled(w) {
			t.Error("expected no render signal for unchanged IP")
		}
	})

	t.Run("DELETE removes node and signals", func(t *testing.T) {
		w := newWatcher(t)
		w.nodeIPs["node1"] = "1.2.3.4"
		w.handleNodeEvent("DELETE", testNode("node1", "1.2.3.4"))
		if len(w.nodeIPs) != 0 {
			t.Errorf("node not removed: %v", w.nodeIPs)
		}
		if !signalled(w) {
			t.Error("expected render signal")
		}
	})

	t.Run("DELETE of unknown node does not signal", func(t *testing.T) {
		w := newWatcher(t)
		w.handleNodeEvent("DELETE", testNode("ghost", "1.2.3.4"))
		if signalled(w) {
			t.Error("expected no render signal")
		}
	})
}

func TestRenderSkippedBelowMinNodeCount(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "out.conf")
	w := newTestWatcher(t, "x", &Config{OutputPath: outputPath, MinNodeCount: 2})

	if err := w.renderAndExecute(); err != nil {
		t.Fatalf("renderAndExecute: %v", err)
	}

	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Errorf("expected no output file below minNodeCount, stat err: %v", err)
	}
}

func TestRenderOutputIsSorted(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "out.conf")
	w := newTestWatcher(t, "{{range .Nodes}}{{.Name}}={{.ExternalIP}}\n{{end}}{{range .AllIPs}}{{.}}\n{{end}}", &Config{
		OutputPath: outputPath,
		StaticIPs:  []string{"192.168.0.1"},
	})
	w.nodeIPs = map[string]string{
		"zeta": "10.0.0.3", "alpha": "10.0.0.1", "mike": "10.0.0.2",
		"echo": "10.0.0.9", "kilo": "10.0.0.7", "bravo": "10.0.0.5",
		"xray": "10.0.0.8", "golf": "10.0.0.6",
	}

	if err := w.renderAndExecute(); err != nil {
		t.Fatalf("renderAndExecute: %v", err)
	}

	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}

	want := "alpha=10.0.0.1\nbravo=10.0.0.5\necho=10.0.0.9\ngolf=10.0.0.6\n" +
		"kilo=10.0.0.7\nmike=10.0.0.2\nxray=10.0.0.8\nzeta=10.0.0.3\n" +
		"10.0.0.1\n10.0.0.5\n10.0.0.9\n10.0.0.6\n10.0.0.7\n10.0.0.2\n10.0.0.8\n10.0.0.3\n" +
		"192.168.0.1\n"
	if string(got) != want {
		t.Errorf("output not sorted by node name:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestHTTPEndpoints(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	// Port 0 lets the kernel pick a free port; the listener is open before
	// startHTTPServer returns, so no sleep is needed
	server, err := startHTTPServer("localhost:0", logger)
	if err != nil {
		t.Fatalf("startHTTPServer: %v", err)
	}
	defer server.Close()
	addr := server.Addr

	t.Run("healthz endpoint returns 200 OK", func(t *testing.T) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err != nil {
			t.Fatalf("error calling /healthz: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read response body: %v", err)
		}

		if string(body) != "ok\n" {
			t.Errorf("expected body 'ok\\n', got %q", string(body))
		}
	})

	t.Run("metrics endpoint must return Prometheus format", func(t *testing.T) {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err != nil {
			t.Fatalf("error calling /metrics: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read response body: %v", err)
		}

		bodyStr := string(body)

		// Check for always metrics
		expectedGauges := []string{
			"k8s_node_watcher_nodes_current",
			"k8s_node_watcher_start_time_seconds",
		}

		for _, metric := range expectedGauges {
			if !strings.Contains(bodyStr, metric) {
				t.Errorf("expected gauge metric %q not found in response", metric)
			}
		}

		// Check for Prometheus format comments
		if !strings.Contains(bodyStr, "# HELP") {
			t.Error("expected Prometheus HELP comments")
		}
		if !strings.Contains(bodyStr, "# TYPE") {
			t.Error("expected Prometheus TYPE comments")
		}

		// Check for standard Go metrics
		if !strings.Contains(bodyStr, "go_goroutines") {
			t.Error("expected standard go metrics (go_goroutines) in response")
		}
	})
}

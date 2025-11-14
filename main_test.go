package main

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

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

func TestHTTPEndpoints(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	addr := "localhost:18080"

	server := startHTTPServer(addr, logger)
	defer server.Close()

	// Allow some time for the server to start
	time.Sleep(100 * time.Millisecond)

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

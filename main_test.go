package main

import (
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

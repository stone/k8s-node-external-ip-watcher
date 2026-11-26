# 2. Safety nets against a shrinking node list

## Status

Accepted

## Context

The rendered file is a backend list for a load balancer or cache (Varnish,
nginx, HAProxy). If it suddenly lists too few nodes, or none, traffic is
dropped. Two things can shrink the list without a node really leaving:

- A cloud provider briefly reports a node without an external IP.
- The API or informer returns an empty or partial view (startup, outages).

## Decision

1. A node that loses its external IP keeps its last known IP in the list
   until the node object is deleted. A warning is logged.
2. Rendering is skipped when fewer than `minNodeCount` nodes (default 1)
   have an IP. The previous output file stays in place.

## Consequences

- Short provider glitches and empty API views no longer empty the list.
- A node whose external IP is removed on purpose stays listed until the
  node is deleted. To remove it sooner, delete the node or restart the
  watcher.
- The `k8s_node_watcher_nodes_current` gauge counts nodes with a retained
  IP too, not only nodes that currently report one.
- If the cluster really shrinks below `minNodeCount`, the output keeps the
  old nodes until the count recovers. Set `minNodeCount: 0` to disable.

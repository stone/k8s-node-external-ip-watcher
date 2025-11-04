```
 _   _           _      ___ ____   __        __    _       _               
| \ | | ___   __| | ___|_ _|  _ \  \ \      / /_ _| |_ ___| |__   ___ _ __ 
|  \| |/ _ \ / _` |/ _ \| || |_) |  \ \ /\ / / _` | __/ __| '_ \ / _ \ '__|
| |\  | (_) | (_| |  __/| ||  __/    \ V  V / (_| | || (__| | | |  __/ |   
|_| \_|\___/ \__,_|\___|___|_|        \_/\_/ \__,_|\__\___|_| |_|\___|_|   
                                                                           
```


A lightweight Kubernetes event watcher that monitors node external IP
changes and executes actions based on a templated configuration.

- Uses Kubernetes informers for optimal API usage
- Hash-based checks prevents redundant executions
- Go template rendering for output
- Prevents accidental removal of all nodes with a minimum node count setting
- Allows for additional static IPs in the output
- Waits for Kubernetes informer cache to sync, full sync on startup

What is monitored:

- Node Added: New node joins the cluster
- Node Updated: Node IP changes
- Node Deleted: Node removed from cluster

The first use case is to update a external DNS loadbalancer configuration,
when nodes are added/removed from the cluster. Needed when the cloud provider 
does not provice a UDP load balancer. (I'm looking at you, Scaleway!)

But it can be used for any use case where node external IPs need to
be monitored and acted upon.

- Generate DNS records from node IPs 
- Update external firewall rules based on cluster topology
- Synchronize external systems with Kubernetes node changes


## Configuration

### Config File (config.yaml)

```yaml
# Log level: debug, info, warn, error
logLevel: info

# Path to kubeconfig file (optional)
kubeConfig: /path/to/kubeconfig

# Path to the Go template file
templatePath: template.tmpl

# Path where the rendered output will be written
outputPath: /tmp/node-ips.conf

# Command to execute after rendering
command: /usr/local/bin/reload-config.sh

# Static IPs to always include
staticIPs:
  - "192.168.1.100"
  - "192.168.1.101"

# Resync interval in seconds
resyncInterval: 300

# Minimum node count (safety net)
minNodeCount: 1
```

### Command-Line Flags

Flags will override config file values:

```bash
./k8s-node-external-ip-watcher \
  --config config.yaml \
  --log-level debug \
  --kubeconfig ~/.kube/config \
  --template template.tmpl \
  --output /etc/nginx/backends.conf
```

## Template Format

Templates use Go's `text/template` package. Available data:

```go
type NodeData struct {
    Nodes     []NodeInfo  // Kubernetes nodes with external IPs
    StaticIPs []string    // Static IPs from config
    AllIPs    []string    // Combined list of all IPs
    Timestamp time.Time   // When the template was rendered
}

type NodeInfo struct {
    Name       string  // Node name
    ExternalIP string  // External IP address
}
```

### Example Templates

#### Simple IP List

```
{{- range .AllIPs }}
{{ . }}
{{- end }}
```

#### Nginx Upstream Configuration

```
# Generated: {{ .Timestamp.Format "2006-01-02 15:04:05 MST" }}

upstream k8s_nodes {
{{- range .Nodes }}
    server {{ .ExternalIP }}:80;  # {{ .Name }}
{{- end }}
{{- range .StaticIPs }}
    server {{ . }}:80;  # static
{{- end }}
}
```

#### Detailed Configuration

```
# Generated: {{ .Timestamp.Format "2006-01-02 15:04:05 MST" }}
# Total IPs: {{ len .AllIPs }}

# Kubernetes Nodes
{{- range .Nodes }}
# {{ .Name }}
server {{ .ExternalIP }};
{{- end }}

{{- if .StaticIPs }}
# Static IPs
{{- range .StaticIPs }}
server {{ . }};
{{- end }}
{{- end }}
```

## Examples

### Varnish Backend configuration
**config.yaml:**
```yaml
logLevel: info
templatePath: varnish-backend.tmpl
outputPath: /etc/varnish/backends.vcl
command: /usr/bin/varnishreload
minNodeCount: 3
```

**varnish-backend.tmpl:**
```backend k8s_nodes {
{{- range .Nodes }}
    .host = "{{ .ExternalIP }}";
    .port = "8080";
{{- end }}
}
```

### Nginx Backend Updates

**config.yaml:**
```yaml
logLevel: info
templatePath: nginx-backend.tmpl
outputPath: /etc/nginx/conf.d/backends.conf
command: /usr/local/bin/reload-nginx.sh
minNodeCount: 2
```

**reload-nginx.sh:**
```bash
#!/bin/bash
# Validate and reload nginx configuration
nginx -t && nginx -s reload
```

**nginx-backend.tmpl:**
```
upstream k8s_backends {
{{- range .Nodes }}
    server {{ .ExternalIP }}:8080;
{{- end }}
}
```

### HAProxy Configuration

**config.yaml:**
```yaml
templatePath: haproxy-backend.tmpl
outputPath: /etc/haproxy/backends.cfg
command: /usr/local/bin/reload-haproxy.sh
```

**reload-haproxy.sh:**
```bash
#!/bin/bash
# Validate and reload haproxy configuration
haproxy -c -f /etc/haproxy/haproxy.cfg && systemctl reload haproxy
```

**haproxy-backend.tmpl:**
```
backend k8s_nodes
    balance roundrobin
{{- range .Nodes }}
    server {{ .Name }} {{ .ExternalIP }}:80 check
{{- end }}
```

// Package metrics is a tiny dependency-free Prometheus-text-format registry for HTTP metrics.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var buckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type key struct {
	method, route string
	status        int
}

type series struct {
	count   uint64
	sum     float64
	buckets []uint64
}

type Registry struct {
	mu     sync.Mutex
	series map[key]*series
	start  time.Time
}

func New() *Registry { return &Registry{series: map[key]*series{}, start: time.Now()} }

func (r *Registry) Observe(method, route string, status int, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key{method, route, status}
	s := r.series[k]
	if s == nil {
		s = &series{buckets: make([]uint64, len(buckets))}
		r.series[k] = s
	}
	sec := d.Seconds()
	s.count++
	s.sum += sec
	for i, b := range buckets {
		if sec <= b {
			s.buckets[i]++
		}
	}
}

func esc(s string) string { return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) }

func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		keys := make([]key, 0, len(r.series))
		for k := range r.series {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := keys[i], keys[j]
			if a.route != b.route {
				return a.route < b.route
			}
			if a.method != b.method {
				return a.method < b.method
			}
			return a.status < b.status
		})
		var sb strings.Builder
		sb.WriteString("# HELP pms_http_requests_total Total HTTP requests.\n# TYPE pms_http_requests_total counter\n")
		for _, k := range keys {
			fmt.Fprintf(&sb, "pms_http_requests_total{method=\"%s\",route=\"%s\",status=\"%d\"} %d\n", esc(k.method), esc(k.route), k.status, r.series[k].count)
		}
		sb.WriteString("# HELP pms_http_request_duration_seconds HTTP request latency.\n# TYPE pms_http_request_duration_seconds histogram\n")
		for _, k := range keys {
			s := r.series[k]
			lbl := fmt.Sprintf("method=\"%s\",route=\"%s\",status=\"%d\"", esc(k.method), esc(k.route), k.status)
			for i, b := range buckets {
				fmt.Fprintf(&sb, "pms_http_request_duration_seconds_bucket{%s,le=\"%s\"} %d\n", lbl, strconv.FormatFloat(b, 'f', -1, 64), s.buckets[i])
			}
			fmt.Fprintf(&sb, "pms_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", lbl, s.count)
			fmt.Fprintf(&sb, "pms_http_request_duration_seconds_sum{%s} %g\n", lbl, s.sum)
			fmt.Fprintf(&sb, "pms_http_request_duration_seconds_count{%s} %d\n", lbl, s.count)
		}
		fmt.Fprintf(&sb, "# TYPE pms_uptime_seconds gauge\npms_uptime_seconds %d\n", int(time.Since(r.start).Seconds()))
		r.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(sb.String()))
	})
}

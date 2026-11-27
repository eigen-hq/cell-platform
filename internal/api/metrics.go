package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/eigen-hq/cell-platform/internal/cell"
)

// The cell's metrics store (E-87): the collector on every machine sends the
// processor and memory of the machine and of its containers, every 15 s.
// The API asks the store in PromQL. A query holds only fixed text and the
// names of objects the API has read itself, never request input.

type series struct {
	Metric map[string]string `json:"metric"`
	Value  []any             `json:"value"`  // instant query: [unix seconds, "number"]
	Values [][]any           `json:"values"` // range query
}

// query asks the store; path is /api/v1/query or /api/v1/query_range.
func (s *Server) query(ctx context.Context, path string, params url.Values) ([]series, error) {
	base := s.Metrics
	if base == "" {
		base = cell.MetricsURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	var out struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []series `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("the metrics store answered %d: %w", res.StatusCode, err)
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("the metrics store answered %d: %s", res.StatusCode, out.Error)
	}
	return out.Data.Result, nil
}

// sample reads one [unix seconds, "number"] pair of the store.
func sample(p []any) (time.Time, float64, bool) {
	if len(p) != 2 {
		return time.Time{}, 0, false
	}
	at, okT := p[0].(float64)
	text, okV := p[1].(string)
	v, err := strconv.ParseFloat(text, 64)
	if !okT || !okV || err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return time.Time{}, 0, false
	}
	return time.UnixMilli(int64(at * 1000)).UTC(), v, true
}

// current asks the queries at once and returns each one's values, keyed by
// key of the series' labels.
func (s *Server) current(ctx context.Context, key func(map[string]string) string, queries ...string) ([]map[string]float64, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out := make([]map[string]float64, len(queries))
	errs := make([]error, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Go(func() {
			res, err := s.query(ctx, "/api/v1/query", url.Values{"query": {q}})
			out[i], errs[i] = map[string]float64{}, err
			for _, r := range res {
				if _, v, ok := sample(r.Value); ok {
					out[i][key(r.Metric)] = v
				}
			}
		})
	}
	wg.Wait()
	return out, errors.Join(errs...)
}

// What the machines use now: processor over the last minute, memory as last
// measured.
func (s *Server) nodesNow(ctx context.Context) (cpu, memory map[string]float64, err error) {
	v, err := s.current(ctx, func(m map[string]string) string { return m["node"] },
		`max by (node) (rate(node_cpu_usage_seconds_total[1m]))`,
		`max by (node) (last_over_time(node_memory_working_set_bytes[1m]))`)
	return v[0], v[1], err
}

// What the containers in namespace ns use now, keyed by pod/container.
func (s *Server) containersNow(ctx context.Context, ns string) (cpu, memory map[string]float64, err error) {
	sel := fmt.Sprintf(`{namespace=%q}`, ns)
	v, err := s.current(ctx, func(m map[string]string) string { return m["pod"] + "/" + m["container"] },
		`max by (pod, container) (rate(container_cpu_usage_seconds_total`+sel+`[1m]))`,
		`max by (pod, container) (last_over_time(container_memory_working_set_bytes`+sel+`[1m]))`)
	return v[0], v[1], err
}

// The spans a history covers, each in 120 points.
var spans = map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}

type usagePoint struct {
	At     time.Time `json:"at"`
	CPU    *float64  `json:"cpu,omitempty"`    // cores
	Memory *int64    `json:"memory,omitempty"` // bytes
}

// nodeHistory is what one machine used over a span.
func (s *Server) nodeHistory(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	var n corev1.Node
	if err := s.Kube.Get(r.Context(), client.ObjectKey{Name: r.PathValue("name")}, &n); err != nil {
		if apierrors.IsNotFound(err) {
			return errorf(http.StatusNotFound, "no machine %q", r.PathValue("name"))
		}
		return err
	}
	// max: a machine that changed its address has two series for a while.
	return s.history(w, r, "max", "node", fmt.Sprintf(`{node=%q}`, n.Name))
}

// appUsage is what all containers of one app used over a span.
func (s *Server) appUsage(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	app, err := s.app(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	return s.history(w, r, "sum", "container", fmt.Sprintf(`{namespace=%q}`, "app-"+app.Name))
}

// history answers {step, points} for the metrics <prefix>_cpu_usage_seconds_total
// and <prefix>_memory_working_set_bytes of selector sel, combined by agg.
// Each point averages its step over what ran at the same time: the series
// are combined each minute first, so a pod and its replacement within one
// step count once, not twice (both count for at most a minute).
func (s *Server) history(w http.ResponseWriter, r *http.Request, agg, prefix, sel string) error {
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "24h"
	}
	span, ok := spans[name]
	if !ok {
		return errorf(http.StatusBadRequest, "range must be 1h, 24h or 7d")
	}
	step := span / 120
	window := fmt.Sprintf("[%ds:%ds]", int(max(step, time.Minute).Seconds()), int(min(step, time.Minute).Seconds()))
	end := s.now()
	params := func(q string) url.Values {
		return url.Values{"query": {q}, "start": {strconv.FormatInt(end.Add(-span).Unix(), 10)},
			"end": {strconv.FormatInt(end.Unix(), 10)}, "step": {strconv.Itoa(int(step.Seconds()))}}
	}
	queries := []string{
		"avg_over_time(" + agg + "(rate(" + prefix + "_cpu_usage_seconds_total" + sel + "[1m]))" + window + ")",
		"avg_over_time(" + agg + "(" + prefix + "_memory_working_set_bytes" + sel + ")" + window + ")",
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	results := make([][]series, len(queries))
	errs := make([]error, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Go(func() { results[i], errs[i] = s.query(ctx, "/api/v1/query_range", params(q)) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return errorf(http.StatusServiceUnavailable, "the metrics store does not answer: %v", err)
	}
	byTime := map[time.Time]*usagePoint{}
	for i, res := range results {
		for _, ser := range res {
			for _, p := range ser.Values {
				at, v, ok := sample(p)
				if !ok {
					continue
				}
				pt := byTime[at]
				if pt == nil {
					pt = &usagePoint{At: at}
					byTime[at] = pt
				}
				if i == 0 {
					pt.CPU = &v
				} else {
					b := int64(v)
					pt.Memory = &b
				}
			}
		}
	}
	points := make([]usagePoint, 0, len(byTime))
	for _, p := range byTime {
		points = append(points, *p)
	}
	slices.SortFunc(points, func(a, b usagePoint) int { return a.At.Compare(b.At) })
	writeJSON(w, http.StatusOK, map[string]any{"step": int(step.Seconds()), "points": points})
	return nil
}

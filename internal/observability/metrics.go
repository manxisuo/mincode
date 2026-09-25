package observability

import (
	"fmt"
	"strings"
	"time"
)

// Metrics aggregates session-level counters from events.
type Metrics struct {
	LLMCalls     int
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	LLMDuration  time.Duration
	Errors       int
	// ParallelBatches counts tool.batch_started events with parallel=true.
	ParallelBatches int
	// ParallelToolCalls counts tool.* events flagged parallel.
	ParallelToolCalls int
	// StreamCalls counts llm.request_finished with streamed=true.
	StreamCalls int
	// StreamDeltas counts llm.stream_delta events.
	StreamDeltas int
	// LastTTFTMS is the most recent time-to-first-token in milliseconds.
	LastTTFTMS int64
	// RepoMapBuilds counts repo_map.built events (startup + agent tool).
	RepoMapBuilds int
	// RepoMapCacheHits / RepoMapCacheMisses aggregate incremental-cache reuse
	// across repository-map builds.
	RepoMapCacheHits   int
	RepoMapCacheMisses int
}

// RepoMapCacheTotal returns cache lookups (hits + misses) this session.
func (m Metrics) RepoMapCacheTotal() int {
	return m.RepoMapCacheHits + m.RepoMapCacheMisses
}

// RepoMapHitRate returns hits / (hits+misses) in [0,1]; 0 when no lookups.
func (m Metrics) RepoMapHitRate() float64 {
	total := m.RepoMapCacheTotal()
	if total == 0 {
		return 0
	}
	return float64(m.RepoMapCacheHits) / float64(total)
}

// MetricsCollector folds events into Metrics. Safe for sequential bus delivery.
type MetricsCollector struct {
	m Metrics
}

// NewMetricsCollector creates an empty collector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{}
}

// Handle implements Handler.
func (c *MetricsCollector) Handle(e Event) {
	switch e.Type {
	case EventLLMRequestFinished:
		c.m.LLMCalls++
		if data, ok := asLLMData(e.Data); ok {
			c.m.InputTokens += data.InputTokens
			c.m.OutputTokens += data.OutputTokens
			c.m.TotalTokens += data.TotalTokens
			c.m.LLMDuration += time.Duration(data.DurationMS) * time.Millisecond
			if data.Streamed {
				c.m.StreamCalls++
				if data.TTFTMS > 0 {
					c.m.LastTTFTMS = data.TTFTMS
				}
			}
		}
	case EventLLMStreamDelta:
		c.m.StreamDeltas++
	case EventLLMRequestFailed:
		c.m.Errors++
		if data, ok := asLLMData(e.Data); ok {
			c.m.LLMDuration += time.Duration(data.DurationMS) * time.Millisecond
		}
	case EventToolBatchStarted:
		if data, ok := asBatchData(e.Data); ok && data.Parallel {
			c.m.ParallelBatches++
		}
	case EventToolStarted:
		if data, ok := asToolData(e.Data); ok && data.Parallel {
			c.m.ParallelToolCalls++
		}
	case EventRepoMapBuilt:
		if data, ok := asRepoMapData(e.Data); ok {
			c.m.RepoMapBuilds++
			c.m.RepoMapCacheHits += data.CacheHits
			c.m.RepoMapCacheMisses += data.CacheMisses
		}
	}
}

// Reset clears accumulated metrics (used when switching sessions).
func (c *MetricsCollector) Reset() {
	c.m = Metrics{}
}

// Snapshot returns a copy of current metrics.
func (c *MetricsCollector) Snapshot() Metrics {
	return c.m
}

// Format renders a human-readable metrics block for /metrics.
func (m Metrics) Format() string {
	var b strings.Builder
	b.WriteString("Session Metrics\n\n")
	fmt.Fprintf(&b, "LLM Calls        %d\n", m.LLMCalls)
	fmt.Fprintf(&b, "Errors           %d\n", m.Errors)
	fmt.Fprintf(&b, "Input Tokens     %s\n", formatInt(m.InputTokens))
	fmt.Fprintf(&b, "Output Tokens    %s\n", formatInt(m.OutputTokens))
	fmt.Fprintf(&b, "Total Tokens     %s\n", formatInt(m.TotalTokens))
	fmt.Fprintf(&b, "LLM Time         %s\n", m.LLMDuration.Round(time.Millisecond))
	fmt.Fprintf(&b, "Parallel Batches %d\n", m.ParallelBatches)
	fmt.Fprintf(&b, "Parallel Tools   %d\n", m.ParallelToolCalls)
	fmt.Fprintf(&b, "Stream Calls     %d\n", m.StreamCalls)
	fmt.Fprintf(&b, "Stream Deltas    %d\n", m.StreamDeltas)
	if m.LastTTFTMS > 0 {
		fmt.Fprintf(&b, "Last TTFT        %dms\n", m.LastTTFTMS)
	}
	if m.RepoMapBuilds > 0 {
		fmt.Fprintf(&b, "Repo Map Builds  %d\n", m.RepoMapBuilds)
		fmt.Fprintf(&b, "Repo Map Cache   %s\n", formatHitRate(m.RepoMapHitRate(), m.RepoMapCacheHits, m.RepoMapCacheTotal()))
	}
	return b.String()
}

// formatHitRate renders "88% (7/8)" for one cache hit-rate metric.
func formatHitRate(rate float64, hits, total int) string {
	if total == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%% (%d/%d)", rate*100, hits, total)
}

func formatInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	// Insert thousands separators.
	var out []byte
	for i, ch := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, ch)
	}
	return string(out)
}

func asLLMData(v any) (LLMRequestData, bool) {
	switch d := v.(type) {
	case LLMRequestData:
		return d, true
	case *LLMRequestData:
		if d == nil {
			return LLMRequestData{}, false
		}
		return *d, true
	case map[string]any:
		// After JSON round-trip (e.g. if someone re-parses).
		var out LLMRequestData
		if s, ok := d["provider"].(string); ok {
			out.Provider = s
		}
		if s, ok := d["model"].(string); ok {
			out.Model = s
		}
		out.InputTokens = intFromAny(d["input_tokens"])
		out.OutputTokens = intFromAny(d["output_tokens"])
		out.TotalTokens = intFromAny(d["total_tokens"])
		out.DurationMS = int64(intFromAny(d["duration_ms"]))
		if b, ok := d["streamed"].(bool); ok {
			out.Streamed = b
		}
		out.TTFTMS = int64(intFromAny(d["ttft_ms"]))
		return out, true
	}
	return LLMRequestData{}, false
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func asBatchData(v any) (ToolBatchData, bool) {
	switch d := v.(type) {
	case ToolBatchData:
		return d, true
	case *ToolBatchData:
		if d == nil {
			return ToolBatchData{}, false
		}
		return *d, true
	case map[string]any:
		out := ToolBatchData{}
		out.Size = intFromAny(d["size"])
		if b, ok := d["parallel"].(bool); ok {
			out.Parallel = b
		}
		return out, true
	}
	return ToolBatchData{}, false
}

// asRepoMapData extracts repo_map.built cache counters (struct or JSON map).
func asRepoMapData(v any) (RepoMapData, bool) {
	switch d := v.(type) {
	case RepoMapData:
		return d, true
	case *RepoMapData:
		if d == nil {
			return RepoMapData{}, false
		}
		return *d, true
	case map[string]any:
		out := RepoMapData{}
		out.CacheHits = intFromAny(d["cache_hits"])
		out.CacheMisses = intFromAny(d["cache_misses"])
		return out, true
	}
	return RepoMapData{}, false
}

func asToolData(v any) (ToolEventData, bool) {
	switch d := v.(type) {
	case ToolEventData:
		return d, true
	case *ToolEventData:
		if d == nil {
			return ToolEventData{}, false
		}
		return *d, true
	case map[string]any:
		out := ToolEventData{}
		if s, ok := d["tool"].(string); ok {
			out.Tool = s
		}
		if b, ok := d["parallel"].(bool); ok {
			out.Parallel = b
		}
		return out, true
	}
	return ToolEventData{}, false
}

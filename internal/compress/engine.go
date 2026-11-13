package compress

import (
	"sync/atomic"

	"seavxrouter/internal/backend"
)

const (
	defaultMinBytes   = 2000
	defaultMinSavings = 0.20
)

// Options controls which tool results the engine rewrites.
type Options struct {
	Enabled    bool
	MinBytes   int
	MinSavings float64
}

// Stats is the compression delta for one request.
type Stats struct {
	ToolResults int64
	Compressed  int64
	BytesBefore int64
	BytesAfter  int64
}

// Snapshot is the cumulative engine state exposed by GET /v1/compression.
type Snapshot struct {
	Enabled        bool
	Requests       int64
	ToolResults    int64
	Compressed     int64
	BytesBefore    int64
	BytesAfter     int64
	BytesSaved     int64
	EstTokensSaved int64
	SavedPercent   float64
	MinBytes       int
	MinSavings     float64
}

// Engine compresses tool-result content before a request is forwarded to a
// backend. Every rewrite is a pure function of the message content and its
// originating command, so provider prompt-cache prefixes stay byte-identical
// across turns.
type Engine struct {
	opts Options

	requests    atomic.Int64
	toolResults atomic.Int64
	compressed  atomic.Int64
	bytesBefore atomic.Int64
	bytesAfter  atomic.Int64
}

func New(o Options) *Engine {
	if o.MinBytes <= 0 {
		o.MinBytes = defaultMinBytes
	}
	if o.MinSavings <= 0 || o.MinSavings >= 1 {
		o.MinSavings = defaultMinSavings
	}
	return &Engine{opts: o}
}

func NewDefault() *Engine {
	return New(Options{Enabled: true})
}

func (e *Engine) Enabled() bool {
	return e != nil && e.opts.Enabled
}

// CompressRequest rewrites qualifying tool messages in place and returns the
// per-request statistics.
func (e *Engine) CompressRequest(req *backend.ChatRequest) Stats {
	if !e.Enabled() || req == nil || len(req.Messages) == 0 {
		return Stats{}
	}
	e.requests.Add(1)
	commands := collectCommands(req.Messages)
	var st Stats
	for i := range req.Messages {
		m := &req.Messages[i]
		if m.Role != "tool" {
			continue
		}
		st.ToolResults++
		e.toolResults.Add(1)
		if len(m.Content) < e.opts.MinBytes || len(m.Images) > 0 {
			continue
		}
		normalized := normalize(m.Content)
		kind := classify(commands[m.ToolCallID], normalized)
		var out string
		isJSON := kind == kindGeneric && looksLikeJSON(m.Content)
		if isJSON {
			// Minify the original: normalize could rewrite repeated pretty-printed
			// lines and corrupt the document.
			out = minifyJSON(m.Content)
		} else {
			out = applyFilter(kind, normalized)
		}
		before, after := len(m.Content), len(out)
		if out == "" || after >= before {
			continue
		}
		// Any byte saved from JSON is worth taking; the 20% rule targets
		// structural filters that drop context.
		if !isJSON && float64(before-after)/float64(before) < e.opts.MinSavings {
			continue
		}
		m.Content = out
		st.Compressed++
		st.BytesBefore += int64(before)
		st.BytesAfter += int64(after)
		e.compressed.Add(1)
		e.bytesBefore.Add(int64(before))
		e.bytesAfter.Add(int64(after))
	}
	return st
}

func (e *Engine) Snapshot() Snapshot {
	if e == nil {
		return Snapshot{}
	}
	s := Snapshot{
		Enabled:     e.opts.Enabled,
		Requests:    e.requests.Load(),
		ToolResults: e.toolResults.Load(),
		Compressed:  e.compressed.Load(),
		BytesBefore: e.bytesBefore.Load(),
		BytesAfter:  e.bytesAfter.Load(),
		MinBytes:    e.opts.MinBytes,
		MinSavings:  e.opts.MinSavings,
	}
	s.BytesSaved = s.BytesBefore - s.BytesAfter
	s.EstTokensSaved = s.BytesSaved / 4
	if s.BytesBefore > 0 {
		s.SavedPercent = float64(s.BytesSaved) * 100 / float64(s.BytesBefore)
	}
	return s
}

func applyFilter(kind outputKind, content string) string {
	switch kind {
	case kindDiff:
		return compressDiff(content)
	case kindStatus:
		return compressStatus(content)
	case kindLog:
		return compressLog(content)
	case kindShow:
		return compressShow(content)
	case kindGrep:
		return compressGrep(content)
	case kindList:
		return compressListing(content)
	case kindTest:
		return compressTest(content)
	case kindLint:
		return compressLint(content)
	default:
		return compressGeneric(content)
	}
}

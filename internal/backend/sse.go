package backend

import (
	"bufio"
	"io"
	"sync"
)

// sseReaderSize covers typical provider event lines without the 1 MiB
// per-stream allocation the adapters used to make.
const sseReaderSize = 64 << 10

var sseReaderPool = sync.Pool{
	New: func() any { return bufio.NewReaderSize(nil, sseReaderSize) },
}

// AcquireSSEReader returns a pooled reader for one upstream event stream.
// Callers must ReleaseSSEReader once the stream is fully consumed.
func AcquireSSEReader(r io.Reader) *bufio.Reader {
	br := sseReaderPool.Get().(*bufio.Reader)
	br.Reset(r)
	return br
}

// ReleaseSSEReader returns a reader to the pool and drops its body reference.
func ReleaseSSEReader(br *bufio.Reader) {
	if br == nil {
		return
	}
	br.Reset(nil)
	sseReaderPool.Put(br)
}

package agent

import (
	"io"
	"time"
)

// rateLimitedReader caps the long-term read rate while allowing one second of
// burst. Uploads remain streaming and never buffer the recording in memory.
type rateLimitedReader struct {
	reader    io.Reader
	bytesPerS int64
	started   time.Time
	read      int64
}

func newRateLimitedReader(reader io.Reader, bytesPerSecond int64) io.Reader {
	if bytesPerSecond <= 0 {
		return reader
	}
	return &rateLimitedReader{reader: reader, bytesPerS: bytesPerSecond, started: time.Now()}
}

func (r *rateLimitedReader) Read(buffer []byte) (int, error) {
	if int64(len(buffer)) > r.bytesPerS {
		buffer = buffer[:r.bytesPerS]
	}
	n, err := r.reader.Read(buffer)
	r.read += int64(n)
	expected := time.Duration(float64(r.read) / float64(r.bytesPerS) * float64(time.Second))
	if wait := expected - time.Since(r.started); wait > 0 {
		time.Sleep(wait)
	}
	return n, err
}

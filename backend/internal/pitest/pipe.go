package pitest

import (
	"io"
	"sync"
)

// pipe is one end of a duplex pipe: reads come from r, writes go to w.
type pipe struct {
	r         *io.PipeReader
	w         *io.PipeWriter
	writeOnce sync.Once
	closeOnce sync.Once
}

// Read implements io.Reader.
func (p *pipe) Read(b []byte) (int, error) { return p.r.Read(b) }

// Write implements io.Writer.
func (p *pipe) Write(b []byte) (int, error) { return p.w.Write(b) }

// CloseWrite half-closes the write direction only, which is what the bridge does
// to tell a peer "no more commands, finish up". Reads keep working.
func (p *pipe) CloseWrite() error {
	p.writeOnce.Do(func() { _ = p.w.Close() })
	return nil
}

// Close shuts both directions down. Closing is idempotent and also implies
// CloseWrite, so a caller can defer it next to an explicit half-close.
func (p *pipe) Close() error {
	_ = p.CloseWrite()
	p.closeOnce.Do(func() { _ = p.r.Close() })
	return nil
}

// NewPipe returns two connected full-duplex endpoints backed by io.Pipe. They
// are the in-process stand-in for the stdio channel between the bridge and a Pi
// process: writing to one end is readable from the other, and closing one end
// EOFs the other. Reads and writes block until the peer consumes them, so every
// endpoint must be driven by its own goroutine, exactly as with real stdio.
func NewPipe() (io.ReadWriteCloser, io.ReadWriteCloser) {
	// aToB: end A writes, end B reads. bToA: end B writes, end A reads.
	aToBReader, aToBWriter := io.Pipe()
	bToAReader, bToAWriter := io.Pipe()
	a := &pipe{r: bToAReader, w: aToBWriter}
	b := &pipe{r: aToBReader, w: bToAWriter}
	return a, b
}

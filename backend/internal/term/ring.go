package term

import "sync"

// ringBuffer is a fixed-size circular byte buffer with absolute offsets.
//
// Offsets never wrap: byte N of the stream always has offset N, even after the
// buffer has dropped older data. That is what lets a reconnecting client say
// "give me everything after 4096" and get exactly the missing bytes (PRD
// FR-10.3).
//
// The zero value is not usable: use newRingBuffer. All methods are safe for
// concurrent use — the producer goroutine writes while HTTP handlers read.
type ringBuffer struct {
	mu sync.Mutex

	buf  []byte
	size int

	head    int   // index in buf of the oldest byte
	n       int   // number of valid bytes (<= size)
	start   int64 // absolute offset of buf[head]
	dropped int64 // total bytes evicted since creation
}

func newRingBuffer(size int) *ringBuffer {
	if size <= 0 {
		size = MinRingBytes
	}
	return &ringBuffer{buf: make([]byte, size), size: size}
}

// write appends p, evicting the oldest bytes when the buffer is full.
func (r *ringBuffer) write(p []byte) {
	r.mu.Lock()
	r.writeLocked(p)
	r.mu.Unlock()
}

func (r *ringBuffer) writeLocked(p []byte) {
	if len(p) == 0 {
		return
	}
	if len(p) >= r.size {
		// Only the tail of p can survive.
		keep := p[len(p)-r.size:]
		copy(r.buf, keep)
		evicted := int64(r.n) + int64(len(p)) - int64(r.size)
		r.head = 0
		r.n = r.size
		r.start += evicted
		r.dropped += evicted
		return
	}

	// Append at the tail, wrapping through the front of the buffer.
	writeAt := (r.head + r.n) % r.size
	first := copy(r.buf[writeAt:], p)
	copy(r.buf[:], p[first:])

	overflow := r.n + len(p) - r.size
	if overflow > 0 {
		r.head = (r.head + overflow) % r.size
		r.start += int64(overflow)
		r.dropped += int64(overflow)
		r.n = r.size
		return
	}
	r.n += len(p)
}

// read returns up to max bytes starting at the absolute offset from, the next
// offset to read, and how many requested bytes had already been dropped.
func (r *ringBuffer) read(from int64, max int) (data []byte, next int64, lost int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readLocked(from, max)
}

func (r *ringBuffer) readLocked(from int64, max int) (data []byte, next int64, lost int64) {
	end := r.endLocked()
	if from < r.start {
		lost = r.start - from
		from = r.start
	}
	if from > end {
		from = end
	}
	avail := end - from
	if avail <= 0 {
		return nil, from, lost
	}
	if max > 0 && avail > int64(max) {
		avail = int64(max)
	}
	out := make([]byte, avail)
	// Physical position of the first requested byte.
	off := int((int64(r.head) + (from - r.start)) % int64(r.size))
	first := copy(out, r.buf[off:])
	if first < len(out) {
		copy(out[first:], r.buf[:])
	}
	return out, from + avail, lost
}

// endOffset is the absolute offset one past the newest byte.
func (r *ringBuffer) endOffset() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.endLocked()
}

func (r *ringBuffer) endLocked() int64 { return r.start + int64(r.n) }

// window returns the absolute range currently replayable.
func (r *ringBuffer) window() (start, end int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.start, r.endLocked()
}

// droppedBytes reports how many bytes were evicted since creation.
func (r *ringBuffer) droppedBytes() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

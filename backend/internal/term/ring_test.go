package term

import (
	"bytes"
	"testing"
)

func TestRingBufferOffsets(t *testing.T) {
	r := newRingBuffer(64)
	r.write([]byte("hello world"))

	data, next, lost := r.read(0, 0)
	if lost != 0 {
		t.Fatalf("lost = %d, want 0", lost)
	}
	if string(data) != "hello world" || next != 11 {
		t.Fatalf("read = %q next=%d", data, next)
	}
	// Reading the same offset twice must return identical bytes: that is what
	// makes a reconnect replay safe.
	again, next2, _ := r.read(0, 0)
	if !bytes.Equal(data, again) || next2 != next {
		t.Fatalf("second read differs: %q vs %q", data, again)
	}
	// Reading at the end yields nothing but the same offset.
	empty, end, _ := r.read(next, 0)
	if len(empty) != 0 || end != next {
		t.Fatalf("read at end = %q %d", empty, end)
	}
	// Reading past the end clamps.
	_, clamped, _ := r.read(9999, 0)
	if clamped != next {
		t.Fatalf("clamped = %d, want %d", clamped, next)
	}
}

func TestRingBufferEviction(t *testing.T) {
	r := newRingBuffer(MinRingBytes)
	if r.size != MinRingBytes {
		t.Fatalf("size = %d", r.size)
	}
	r2 := newRingBuffer(8)
	r2.write([]byte("abcdefgh"))
	r2.write([]byte("ij")) // evicts "ab"

	start, end := r2.window()
	if start != 2 || end != 10 {
		t.Fatalf("window = %d..%d, want 2..10", start, end)
	}
	if got := r2.droppedBytes(); got != 2 {
		t.Errorf("dropped = %d, want 2", got)
	}

	data, next, lost := r2.read(0, 0)
	if lost != 2 {
		t.Errorf("lost = %d, want 2", lost)
	}
	if string(data) != "cdefghij" || next != 10 {
		t.Errorf("data = %q next=%d", data, next)
	}
	// A fresh reader from the window start sees everything still buffered.
	data, _, lost = r2.read(2, 0)
	if lost != 0 || string(data) != "cdefghij" {
		t.Errorf("resume = %q lost=%d", data, lost)
	}
}

func TestRingBufferWriteLargerThanBuffer(t *testing.T) {
	r := newRingBuffer(4)
	r.write([]byte("0123456789"))
	data, next, _ := r.read(0, 0)
	if string(data) != "6789" {
		t.Errorf("data = %q, want 6789", data)
	}
	if next != 10 {
		t.Errorf("next = %d, want 10", next)
	}
	if r.droppedBytes() != 6 {
		t.Errorf("dropped = %d, want 6", r.droppedBytes())
	}
	// Wrapping writes keep offsets monotonic.
	r.write([]byte("ab"))
	_, end := r.window()
	if end != 12 {
		t.Errorf("end = %d, want 12", end)
	}
}

func TestRingBufferChunking(t *testing.T) {
	r := newRingBuffer(16)
	r.write([]byte("0123456789"))
	data, next, _ := r.read(2, 3)
	if string(data) != "234" || next != 5 {
		t.Fatalf("chunk = %q %d", data, next)
	}
	data, next, _ = r.read(next, 100)
	if string(data) != "56789" || next != 10 {
		t.Fatalf("rest = %q %d", data, next)
	}
}

func TestRingBufferWrapAround(t *testing.T) {
	r := newRingBuffer(8)
	// Force the read window to wrap in the middle of the buffer.
	r.write([]byte("01234"))
	r.write([]byte("56789")) // evicts "01", head moves to index 2
	start, end := r.window()
	if start != 2 || end != 10 {
		t.Fatalf("window = %d..%d", start, end)
	}
	data, _, lost := r.read(2, 0)
	if lost != 0 || string(data) != "23456789" {
		t.Errorf("data = %q lost=%d", data, lost)
	}
	data, _, _ = r.read(6, 0)
	if string(data) != "6789" {
		t.Errorf("tail = %q", data)
	}
	// A reader that resumes in the middle of the wrapped region gets the right
	// bytes in the right order.
	data, _, _ = r.read(3, 0)
	if string(data) != "3456789" {
		t.Errorf("mid = %q", data)
	}
}

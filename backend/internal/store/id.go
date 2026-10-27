package store

import (
	"crypto/rand"
	"time"
)

// crockford is Crockford's base32 alphabet: no I, L, O or U, so ids survive
// being read aloud or copied out of a log.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// idLen is 10 characters of 48-bit millisecond timestamp followed by 16
// characters of randomness (80 bits), i.e. the ULID layout.
const idLen = 26

// NewID returns a ULID-like identifier: 10 Crockford base32 characters of
// millisecond timestamp plus 80 bits of crypto/rand randomness, joined to an
// optional prefix with an underscore ("ses_01J...", "run_01J...").
//
// The encoding keeps ids lexicographically sortable by creation time, which is
// what makes "ORDER BY id" a usable tiebreaker and keeps cursor pagination
// stable. The random half makes them unguessable enough that an id cannot be
// enumerated, and it removes the same-millisecond collision problem that a
// pure timestamp would have.
//
// crypto/rand failures are treated as fatal: on every platform Go supports, a
// failure here means the process cannot generate session tokens or passwords
// either, so continuing would be unsafe.
func NewID(prefix string) string {
	var out [idLen]byte

	ms := uint64(time.Now().UnixMilli())
	for i := 9; i >= 0; i-- {
		out[i] = crockford[ms&0x1f]
		ms >>= 5
	}

	var buf [10]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("store: crypto/rand: " + err.Error())
	}
	var acc uint64
	bits := 0
	idx := 10
	for _, b := range buf {
		acc = acc<<8 | uint64(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[idx] = crockford[(acc>>uint(bits))&0x1f]
			idx++
		}
	}

	if prefix == "" {
		return string(out[:])
	}
	id := make([]byte, 0, len(prefix)+1+idLen)
	id = append(id, prefix...)
	id = append(id, '_')
	id = append(id, out[:]...)
	return string(id)
}

// randomToken returns n bytes of crypto/rand encoded with Crockford base32.
// Used for generated initial passwords and for session/csrf tokens.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, 0, (n*8+4)/5)
	var acc uint64
	bits := 0
	for _, b := range buf {
		acc = acc<<8 | uint64(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out = append(out, crockford[(acc>>uint(bits))&0x1f])
		}
	}
	if bits > 0 {
		out = append(out, crockford[(acc<<uint(5-bits))&0x1f])
	}
	return string(out), nil
}

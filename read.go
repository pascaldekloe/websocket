package websocket

import (
	"encoding/binary"
	"errors"
	"io"
)

// ErrUnderflow enables non-blocking behaviour.
var ErrUnderflow = errors.New("next WebSocket frame needs more data")

// ErrOverflow may be dealt with by a SkipPayload.
var ErrOverflow = errors.New("next WebSocket frame exceeds buffer capacity")

// ErrReserved signals entension activity on the current frame.
var ErrReserved = errors.New("WebSocket frame with reserved flags")

// Reader parses input from a WebSocket connection.
// Reader can not handle frames beyond its buffer in size.
type Reader struct {
	buf []byte
	err error // pending from last read

	bufI int // index of position in buffer
	bufN int // byte count of buffered data
	next int // first index after current frame
}

func NewReader(buf []byte) *Reader {
	return &Reader{buf: buf}
}

// Buffered returns the size of the input remaining after the current frame.
func (r *Reader) Buffered() (byteN int) {
	return r.bufN - r.next
}

// PassFrame moves on with the buffer position.
func (r *Reader) passFrame() {
	if r.next < r.bufN {
		r.bufI = r.next
	} else {
		r.bufI = 0
		r.bufN = 0
		r.next = 0
	}
}

// ReadSome does one Read. Errors come from conn exclusively. Read may, however,
// delay an error from conn until the next invocation in exceptional cases.
func (r *Reader) ReadSome(conn io.Reader) error {
	if err := r.err; err != nil {
		r.err = nil // clear
		return err
	}

	if r.bufI > 0 && len(r.buf)-r.bufN < 1024 {
		// move to buffer start
		r.bufN = copy(r.buf, r.buf[r.bufI:r.bufN])
		r.next -= r.bufI
		r.bufI = 0
	}

	if r.bufN < len(r.buf) {
		// got buffer space to fil
		n, err := conn.Read(r.buf[r.bufN:])
		if n == 0 {
			return err
		}
		r.bufN += n
		r.err = err
	}
	return nil
}

// IsFinal returns whether the current frame is the last one of the message.
// Fragmented messages span their payload over one or more non-final frames,
// combined with the final one.
func (r *Reader) IsFinal() bool { return r.buf[r.bufI]&0x80 != 0 }

// Reserved1 returns the first reserved bit value of the current frame.
func (r *Reader) Reserved1() bool { return r.buf[r.bufI]&0x40 != 0 }

// Reserved2 returns the second reserved bit value of the current frame.
func (r *Reader) Reserved2() bool { return r.buf[r.bufI]&0x20 != 0 }

// Reserved2 returns the third reserved bit value of the current frame.
func (r *Reader) Reserved3() bool { return r.buf[r.bufI]&0x10 != 0 }

// Opcode returns the payload indicator of the current frame, range [0..15].
// Note how fragmented messages propgate their payload only in the first frame
// of their sequence, with Continuation on all of the following frames.
func (r *Reader) Opcode() uint { return uint(r.buf[r.bufI]) & 15 }

// NextFrame slices the payload from the following frame of the read buffer. The
// bytes stop being valid at the next invocation. Use both Opcode and FinalFrame
// to determine on how to act upon each payload. The Reader needs more data from
// ReadSome on ErrUnderflow. ErrOverflow should be followed up by CloseCode with
// TooBig. ErrReserved is always returned together with the payload.
func (r *Reader) NextFrame() (payload []byte, err error) {
	if r.next > 0 {
		r.passFrame()
	}

	// payload start and size encoded in header
	var offset, byteN int
	// frames may or may not mask their payload
	var maskKey *[4]byte

	i := r.bufI
	if i+1 >= r.bufN {
		return nil, ErrUnderflow
	}
	switch sizeHead := r.buf[i+1]; {

	case sizeHead < 126:
		// size is 7-bit length, no mask
		offset = i + 2
		byteN = int(uint(sizeHead))

	default:
		// size is 7-bit length, with mask
		offset = i + 6
		if offset > r.bufN {
			return nil, ErrUnderflow
		}
		maskKey = (*[4]byte)(r.buf[i+2 : offset])
		byteN = int(uint(sizeHead & 0x7f))

	case sizeHead == 126:
		// 16-bit length follows, no mask
		offset = i + 4
		if offset > r.bufN {
			return nil, ErrUnderflow
		}
		byteN = int(uint(binary.BigEndian.Uint16(r.buf[i+2 : offset])))

	case sizeHead == 126|128:
		// 16-bit length follows, with mask
		offset = i + 8
		if offset > r.bufN {
			return nil, ErrUnderflow
		}
		byteN = int(uint(binary.BigEndian.Uint16(r.buf[i+2 : i+4])))
		maskKey = (*[4]byte)(r.buf[i+4 : offset])

	case sizeHead == 127:
		// 63-bit length follows, no mask
		offset = i + 10
		if offset > r.bufN {
			return nil, ErrUnderflow
		}
		n := binary.BigEndian.Uint64(r.buf[i+2 : offset])
		if n > uint64(len(r.buf)) {
			// spec allows up to 8 PiB 🤡
			return nil, ErrOverflow
		}
		byteN = int(n)

	case sizeHead == 127|128:
		// 63-bit length follows, with mask
		offset = i + 14
		if offset > r.bufN {
			return nil, ErrUnderflow
		}
		n := binary.BigEndian.Uint64(r.buf[i+2 : i+10])
		if n > uint64(len(r.buf)) {
			// spec allows up to 8 PiB 🤡
			return nil, ErrOverflow
		}
		byteN = int(n)
		maskKey = (*[4]byte)(r.buf[i+10 : offset])
	}

	end := offset + byteN
	if end > r.bufN {
		return nil, ErrUnderflow
	}
	payload = r.buf[offset:end:end]
	// frame cursor accepted by setting next
	r.next = end

	if maskKey != nil {
		xorWith(payload, maskKey)
	}
	if r.buf[r.bufI]&0x70 != 0 {
		return payload, ErrReserved
	}
	return payload, nil
}

// XorWith masks/unmasks a payload inline with the key.
func xorWith(p []byte, key *[4]byte) {
	r32 := binary.NativeEndian.Uint32(key[:4])
	r64 := uint64(r32)<<32 | uint64(r32)
	for len(p) > 7 {
		flipped := binary.NativeEndian.Uint64(p) ^ r64
		binary.NativeEndian.PutUint64(p, flipped)
		p = p[8:]
	}
	if len(p) > 3 {
		flipped := binary.NativeEndian.Uint32(p) ^ r32
		binary.NativeEndian.PutUint32(p, flipped)
		p = p[4:]
	}
	for i := range p {
		p[i] ^= key[i]
	}
}

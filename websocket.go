// Package websocket implements “The WebSocket Protocol” RFC 6455, version 13.
package websocket

import (
	"errors"
	"io"
	"net"
	"time"
	"unicode/utf8"
)

// Opcode defines the interpretation of a frame payload.
const (
	// Continuation for streaming data.
	Continuation = iota
	// Text for UTF-8 encoded data.
	Text
	// Binary for opaque data.
	Binary
	// Reserved3 is reserved for further non-control frames.
	Reserved3
	// Reserved4 is reserved for further non-control frames.
	Reserved4
	// Reserved5 is reserved for further non-control frames.
	Reserved5
	// Reserved6 is reserved for further non-control frames.
	Reserved6
	// Reserved7 is reserved for further non-control frames.
	Reserved7
	// Close for disconnect notification.
	Close
	// Ping to request Pong.
	Ping
	// Pong may be send unsolicited too.
	Pong
	// Reserved11 is reserved for further control frames.
	Reserved11
	// Reserved12 is reserved for further control frames.
	Reserved12
	// Reserved13 is reserved for further control frames.
	Reserved13
	// Reserved14 is reserved for further control frames.
	Reserved14
	// Reserved15 is reserved for further control frames.
	Reserved15
)

// Defined Status Codes
const (
	// NormalClose means that the purpose for which the connection was
	// established has been fulfilled.
	NormalClose = 1000
	// GoingAway is a leave like a server going down or a browser moving on.
	GoingAway = 1001
	// ProtocolError rejects standard violation.
	ProtocolError = 1002
	// CannotAccept rejects a data type receival.
	CannotAccept = 1003
	// NoStatusCode is allowed by the protocol.
	NoStatusCode = 1005
	// AbnormalClose signals a disconnect without Close.
	AbnormalClose = 1006
	// Malformed rejects data that is not consistent with it's type, like an
	// illegal UTF-8 sequence for Text.
	Malformed = 1007
	// Policy rejects a message due to a violation.
	Policy = 1008
	// TooBig rejects a message due to size constraints.
	TooBig = 1009
	// WantExtension signals the client's demand for the server to negotiate
	// one or more extensions.
	WantExtension = 1010
	// Unexpected condition prevented the server from fulfilling the request.
	Unexpected = 1011
)

// ErrUTF8 signals malformed text.
var ErrUTF8 = errors.New("websocket: invalid UTF-8 sequence")

// ClosedError is a status code. Atomic Close support prevents Go issue 4373.
// Even after receiving a ClosedError, Conn.Close must still be called.
type ClosedError uint

// Error honors the error interface.
func (e ClosedError) Error() string {
	msg := "websocket: connection closed"
	switch e {
	case NoStatusCode:
		break
	case AbnormalClose:
		msg += " abnormally"
	default:
		msg += ", status code "
		if e >= 10000 {
			msg += string('0' + (e/10000)%10)
		}
		msg += string('0'+(e/1000)%10) + string('0'+(e/100)%10) + string('0'+(e/10)%10) + string('0'+(e)%10)
	}

	return msg
}

// Timeout honors the net.Error interface.
func (e ClosedError) Timeout() bool { return false }

// Temporary honors the net.Error interface.
func (e ClosedError) Temporary() bool { return false }

// ErrOverflow signals an incomming message larger than the provided buffer.
var ErrOverflow = errors.New("websocket: message exceeds buffer size")

// Receive is a high-level abstraction (from Read) for safety and convenience.
// The opcode return is in range [1, 7]. Control frames are dealed with.
// Size defines the amount of bytes in Reader or negative when unknown.
//
// Receive must be called sequentially. Reader must be fully consumed before
// the next call to Receive. Interruptions from other calls to Receive or Read
// may cause protocol violations.
//
// WireTimeout is the limit for Read [frame receival] and idleTimeout limits
// the amount of time to wait for arrival.
func (c *Conn) Receive(buf []byte, wireTimeout, idleTimeout time.Duration) (opcode uint, n int, err error) {
	n, opcode, final, err := c.readWithRetry(buf, idleTimeout)
	if err != nil {
		return opcode, 0, err
	}

	for !final {
		if n >= len(buf) {
			c.WriteClose(TooBig, "")
			return opcode, n, ErrOverflow
		}

		var more int
		more, _, final, err = c.readWithRetry(buf[n:], wireTimeout)
		n += more
		if err != nil {
			return opcode, n, err
		}
	}

	if opcode == Text && !utf8.Valid(buf[:n]) {
		return opcode, n, ErrUTF8
	}

	return opcode, n, nil
}

// ReceiveStream is a high-level abstraction (from Read) for safety and
// convenience. The opcode return is in range [1, 7]. Control frames are dealed
// with.
//
// Receive must be called sequentially. Reader must be fully consumed before
// the next call to Receive. Interruptions from other calls to Receive or Read
// may cause protocol violations.
//
// WireTimeout is the limit for Read [frame receival] and idleTimeout limits
// the amount of time to wait for arrival.
func (c *Conn) ReceiveStream(wireTimeout, idleTimeout time.Duration) (opcode uint, r io.Reader, err error) {
	_, opcode, final, err := c.readWithRetry(nil, idleTimeout)
	if err != nil {
		return 0, nil, err
	}

	switch {
	case final:
		r = readEOF{}
	case opcode == Text:
		r = &textReader{
			conn:        c,
			wireTimeout: wireTimeout,
		}
	default:
		r = &messageReader{
			conn:        c,
			wireTimeout: wireTimeout,
		}
	}
	return opcode, r, nil
}

type messageReader struct {
	conn        *Conn
	wireTimeout time.Duration
	err         error
}

func (r *messageReader) Read(p []byte) (n int, err error) {
	if r.err != nil {
		return 0, r.err
	}

	n, _, final, err := r.conn.readWithRetry(p, r.wireTimeout)
	if final {
		r.err = io.EOF
		if err == nil {
			err = io.EOF
		}
	}
	return n, err
}

type textReader struct {
	conn        *Conn
	wireTimeout time.Duration
	err         error
	tail        [utf8.UTFMax - 1]byte
	tailN       int
}

func (r *textReader) Read(p []byte) (n int, err error) {
	if r.err != nil {
		return 0, r.err
	}

	// start with remainder
	n = r.tailN
	for i := range p {
		if i >= n {
			break
		}
		p[i] = r.tail[i]
	}

	// actual read
	more, _, final, err := r.conn.readWithRetry(p[n:], r.wireTimeout)
	n += more

	// validation overrules I/O errors; received payload shoud be valid
	if !utf8.Valid(p[:n]) {
		if final {
			return n, ErrUTF8
		}
		// last rune might be partial

		end := n
		for end--; end >= 0; end-- {
			if p[end]&0xc0 != 0xc0 {
				break // multi-byte start
			}
		}

		if end+utf8.UTFMax >= n || !utf8.Valid(p[:end]) {
			return n, ErrUTF8
		}

		r.tailN = copy(r.tail[:], p[end:])
		n = end
	}

	if final {
		r.err = io.EOF
		if err == nil {
			err = io.EOF
		}
	}

	return n, err
}

// Send is a high-level abstraction (from Write) for safety and convenience.
// Operation must complete before any other call is made to either Send,
// SendStream, Write or WriteMode.
// WireTimeout is the limit for Write [frame submission].
func (c *Conn) Send(opcode uint, message []byte, wireTimeout time.Duration) error {
	c.SetWriteMode(opcode, true)

	_, err := c.writeWithRetry(message, wireTimeout)
	return err
}

// Send is a high-level abstraction (from Write) for safety and convenience.
// The stream must be closed before any other call is made to either SendStream,
// Send, Write or WriteMode.
// WireTimeout is the limit for Write and Close [frame submission].
func (c *Conn) SendStream(opcode uint, wireTimeout time.Duration) io.WriteCloser {
	c.SetWriteMode(opcode, false)
	return &messageWriter{conn: c, wireTimeout: wireTimeout}
}

type messageWriter struct {
	conn        *Conn
	wireTimeout time.Duration
	closed      bool
}

func (w *messageWriter) Write(p []byte) (n int, err error) {
	if w.closed {
		return 0, io.ErrClosedPipe
	}

	return w.conn.writeWithRetry(p, w.wireTimeout)
}

func (w messageWriter) Close() error {
	if w.closed {
		return nil
	}

	w.conn.writeHead |= finalFlag

	_, err := w.Write(nil)
	if err != nil {
		return err
	}
	w.closed = true
	return nil
}

type readEOF struct{}

func (r readEOF) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (c *Conn) readWithRetry(p []byte, timeout time.Duration) (n int, opcode uint, final bool, err error) {
	var retryDelay time.Duration = time.Microsecond

	for {
		c.SetReadDeadline(time.Now().Add(timeout))
		n, err = c.Read(p)
		for err != nil {
			e, ok := err.(net.Error)
			if ok && e.Timeout() {
				c.WriteClose(Policy, "read timeout")
				return
			}
			if !ok || !e.Temporary() {
				return
			}

			time.Sleep(retryDelay)
			if retryDelay < time.Second {
				retryDelay *= 2
			}

			var more int
			more, err = c.Read(p)
			n += more
		}

		opcode, final = c.ReadMode()
		if opcode&ctrlFlag == 0 {
			return
		}

		err = c.gotCtrl(opcode, n)
		if err != nil {
			return
		}
	}
}

func (c *Conn) writeWithRetry(p []byte, timeout time.Duration) (n int, err error) {
	var retryDelay time.Duration = time.Microsecond

	c.SetWriteDeadline(time.Now().Add(timeout))
	n, err = c.Write(p)
	for err != nil {
		e, ok := err.(net.Error)
		if ok && e.Timeout() {
			c.setClose(Policy, "write timeout")
		}
		if !ok || !e.Temporary() {
			return
		}

		time.Sleep(retryDelay)
		if retryDelay < time.Second {
			retryDelay *= 2
		}

		var more int
		more, err = c.Write(p[n:])
		n += more
	}

	return
}

// GotCtrl deals with the controll frame in the read buffer.
func (c *Conn) gotCtrl(opcode uint, readN int) error {
	switch opcode {
	case Ping:
		// reuse read buffer for pong frame
		c.readBuf[4] = Pong | finalFlag
		c.readBuf[5] = byte(readN + c.readPayloadN)
		pongFrame := c.readBuf[4 : 6+readN+c.readPayloadN]

		c.writeMutex.Lock()
		defer c.writeMutex.Unlock()
		n, err := c.Conn.Write(pongFrame)
		for err != nil {
			e, ok := err.(net.Error)
			if ok && e.Timeout() {
				c.setClose(Policy, "write timeout")
				return err
			}
			if !ok || !e.Temporary() {
				return err
			}

			time.Sleep(100 * time.Microsecond)
			var more int
			more, err = c.Conn.Write(pongFrame[n:])
			n += more
		}
	}

	// flush payload
	c.readBufDone += c.readPayloadN
	c.readPayloadN = 0

	return nil
}

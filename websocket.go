// Package websocket implements “The WebSocket Protocol” RFC 6455, version 13.
package websocket

import (
	"errors"
	"io"
	"net"
	"sync/atomic"
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

// SendClose is a high-level abstraction for safety and convenience. The client
// is notified on best effort basis, including the optional free-form reason.
// Use 123 bytes of UTF-8 or less for submission.
//
// When the connectection already received or send a Close then only the first
// status code remains in effect. Redundant status codes are discarded.
// The return always is a CloseError with the first status code.
//
// Multiple goroutines may invoke SendClose simultaneously. SendClose may be
// invoked simultaneously with any other method from Conn.
func (c *Conn) SendClose(statusCode uint, reason string) error {
	if !atomic.CompareAndSwapUint32(&c.statusCode, 0, uint32(statusCode|statusCodeSetFlag)) {
		// already closed
		return c.closeError()
	}

	// control frame payload limit is 125 bytes; status code takes 2
	if len(reason) > 123 || !utf8.ValidString(reason) {
		reason = ""
	}

	c.writeMutex.Lock()
	// best effort close notification; no pending errors
	if c.writeBufN == 0 && c.writePayloadN == 0 {
		c.writeBuf[0] = Close | finalFlag
		switch statusCode {
		case NoStatusCode, AbnormalClose, 1015:
			c.writeBuf[1] = 0
			c.Conn.Write(c.writeBuf[:2])
		default:
			c.writeBuf[1] = byte(len(reason) + 2)
			byteOrder.PutUint16(c.writeBuf[2:4], uint16(statusCode))
			copy(c.writeBuf[4:], reason)
			c.Conn.Write(c.writeBuf[:4+len(reason)])
		}
	}
	c.writeMutex.Unlock()

	return ClosedError(statusCode)
}

// Send is a high-level abstraction for safety and convenience.
// The opcode must be in range [1, 15] like Text, Binary or Ping.
// WireTimeout limits the frame transmission time. On expiry, the connection
// is closed with status code 1008 [Policy].
// All error returns are fatal to the connection.
//
// Multiple goroutines may invoke Send simultaneously. Send may be invoked
// simultaneously with any other high-level method from Conn. Note that when
// Send interrupts SendStream, then the opcode of Send is further reduced to
// range [8, 15]. Simultaneous invokation of any of the low-level net.Conn
// methods can currupt the connection state.
func (c *Conn) Send(opcode uint, message []byte, wireTimeout time.Duration) error {
	c.writeMutex.Lock()
	c.SetWriteMode(opcode, true)
	_, err := c.writeWithRetry(message, wireTimeout)
	c.writeMutex.Unlock()
	return err
}

// SendStream is an alternative to Send.
// The opcode must be in range [1, 7] like Text or Binary.
// WireTimeout limits the frame transmission time. On expiry, the connection
// is closed with status code 1008 [Policy].
// All errors from the io.WriteCloser other than io.ErrClosedPipe are fatal to
// the connection.
//
// The stream must be closed before any other invocation to SendStream is made
// and Send may only interrupt with control frames—opcode range [8, 15].
// Multiple goroutines may invoke the io.WriteCloser methods simultaneously.
// Simultaneous invokation of either SendStream or the io.WriteCloser with any
// of the low-level net.Conn methods can currupt the connection state.
func (c *Conn) SendStream(opcode uint, wireTimeout time.Duration) io.WriteCloser {
	c.SetWriteMode(opcode, false)
	return &messageWriter{c, wireTimeout, opcode}
}

type messageWriter struct {
	conn        *Conn
	wireTimeout time.Duration
	opcode      uint
}

func (w *messageWriter) Write(p []byte) (n int, err error) {
	w.conn.writeMutex.Lock()
	if w.opcode == Close {
		err = io.ErrClosedPipe
	} else {
		w.conn.SetWriteMode(w.opcode, false)
		w.opcode = Continuation
		n, err = w.conn.writeWithRetry(p, w.wireTimeout)
	}
	w.conn.writeMutex.Unlock()

	return
}

func (w messageWriter) Close() (err error) {
	w.conn.writeMutex.Lock()
	if w.opcode != Close {
		w.conn.SetWriteMode(w.opcode, true)
		w.opcode = Close
		_, err = w.conn.writeWithRetry(nil, w.wireTimeout)
	}
	w.conn.writeMutex.Unlock()

	return
}

// caller must hold the writeMutex lock
func (c *Conn) writeWithRetry(p []byte, timeout time.Duration) (n int, err error) {
	var retryDelay = time.Microsecond

	c.SetWriteDeadline(time.Now().Add(timeout))
	n, err = c.write(p)
	for err != nil {
		e, ok := err.(net.Error)
		if ok && e.Timeout() {
			c.setClose(Policy, "write timeout")
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
		more, err = c.write(p[n:])
		n += more
	}

	return
}

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
			c.SendClose(TooBig, "")
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

type readEOF struct{}

func (r readEOF) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (c *Conn) readWithRetry(p []byte, timeout time.Duration) (n int, opcode uint, final bool, err error) {
	var retryDelay = time.Microsecond

	for {
		c.SetReadDeadline(time.Now().Add(timeout))
		n, err = c.Read(p)
		for err != nil {
			e, ok := err.(net.Error)
			if ok && e.Timeout() {
				c.SendClose(Policy, "read timeout")
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

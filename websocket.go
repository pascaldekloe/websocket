// Package websocket implements “The WebSocket Protocol” RFC 6455, version 13.
package websocket

import (
	"errors"
	"io"
	"net"
	"time"
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

var pongFrame = []byte{Pong | finalFlag, 0}

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
	for {
		c.SetReadDeadline(time.Now().Add(idleTimeout))
		_, err := c.Read(nil)
		for err != nil {
			e, ok := err.(net.Error)
			if ok && e.Timeout() {
				c.WriteClose(Policy, "idle timeout")
			}
			if !ok || !e.Temporary() {
				return 0, 0, err
			}

			time.Sleep(100 * time.Microsecond)
			_, err = c.Read(nil)
		}

		opcode, final := c.ReadMode()

		// deal with conrol frames
		if opcode&ctrlFlag != 0 {
			if err := c.gotCtrl(opcode); err != nil {
				return 0, 0, err
			}
			continue
		}

		c.SetReadDeadline(time.Now().Add(wireTimeout))
		for !final {
			if n >= len(buf) {
				return opcode, n, ErrOverflow
			}

			var more int
			more, err := c.Read(buf[n:])
			n += more
			if err != nil {
				e, ok := err.(net.Error)
				if ok && e.Timeout() {
					c.WriteClose(Policy, "read timeout")
				}
				if !ok || !e.Temporary() {
					return 0, 0, err
				}

				time.Sleep(100 * time.Microsecond)
				continue
			}

			_, final = c.ReadMode()
		}

		return opcode, n, nil
	}
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
	for {
		c.SetReadDeadline(time.Now().Add(idleTimeout))
		_, err := c.Read(nil)
		for err != nil {
			e, ok := err.(net.Error)
			if ok && e.Timeout() {
				c.WriteClose(Policy, "idle timeout")
			}
			if !ok || !e.Temporary() {
				return 0, nil, err
			}

			time.Sleep(100 * time.Microsecond)
			_, err = c.Read(nil)
		}

		opcode, final := c.ReadMode()

		// deal with conrol frames
		if opcode&ctrlFlag != 0 {
			if err := c.gotCtrl(opcode); err != nil {
				return 0, nil, err
			}
			continue
		}

		if final {
			return opcode, readEOF{}, nil
		}
		return opcode, &messageReader{
			conn:        c,
			wireTimeout: wireTimeout,
			idleTimeout: idleTimeout,
		}, nil
	}
}

type messageReader struct {
	conn        *Conn
	wireTimeout time.Duration
	idleTimeout time.Duration // TODO: deadline?
	err         error
}

func (r *messageReader) Read(p []byte) (n int, err error) {
	if r.err != nil {
		return 0, r.err
	}

	r.conn.SetReadDeadline(time.Now().Add(r.wireTimeout))
	n, err = r.conn.Read(p)
	for err != nil {
		e, ok := err.(net.Error)
		if ok && e.Timeout() {
			r.conn.WriteClose(Policy, "read timeout")
		}
		if !ok || !e.Temporary() {
			r.err = err
			return
		}

		time.Sleep(100 * time.Microsecond)
		var more int
		more, err = r.conn.Read(p[n:])
		n += more
	}

	if _, final := r.conn.ReadMode(); final {
		err = io.EOF
		r.err = err
	}
	return
}

// Send is a high-level abstraction (from Write) for safety and convenience.
// Operation must complete before any other call is made to either Send,
// SendStream, Write or WriteMode.
// WireTimeout is the limit for Write [frame submission].
func (c *Conn) Send(opcode uint, message []byte, wireTimeout time.Duration) error {
	c.SetWriteMode(opcode, true)

	c.SetWriteDeadline(time.Now().Add(wireTimeout))
	n, err := c.Write(message)
	for err != nil {
		e, ok := err.(net.Error)
		if ok && e.Timeout() {
			c.setClose(Policy, "write timeout")
		}
		if !ok || !e.Temporary() {
			return err
		}

		time.Sleep(100 * time.Microsecond)
		var more int
		more, err = c.Write(message[n:])
		n += more
	}
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

	w.conn.SetWriteDeadline(time.Now().Add(w.wireTimeout))
	n, err = w.conn.Write(p)
	for err != nil {
		e, ok := err.(net.Error)
		if ok && e.Timeout() {
			w.conn.setClose(Policy, "write timeout")
		}
		if !ok || !e.Temporary() {
			return
		}

		time.Sleep(100 * time.Microsecond)
		var more int
		more, err = w.conn.Write(p[n:])
		n += more
	}

	return
}

func (w messageWriter) Close() error {
	if w.closed {
		return nil
	}

	head := w.conn.writeHead
	if head&opcodeMask != Continuation {
		// nothing written yet
		w.closed = true
		return nil
	}

	w.conn.writeHead = head | finalFlag
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

// GotCtrl deals with the controll frame in the read buffer.
func (c *Conn) gotCtrl(opcode uint) error {
	switch opcode {
	case Ping:
		// reuse read buffer for pong frame
		c.readBuf[c.readBufDone-2] = Pong | finalFlag
		c.readBuf[c.readBufDone-1] = byte(c.readPayloadN)
		pongFrame := c.readBuf[c.readBufDone-2 : c.readBufDone+c.readPayloadN]

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

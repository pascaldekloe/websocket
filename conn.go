package websocket

import (
	"encoding/binary"
	"errors"
	"io"
	"math/bits"
	"net"
	"sync"
	"sync/atomic"
)

// first (frame) byte layout
const (
	opcodeMask   = 0x0f
	ctrlFlag     = 0x08
	reservedMask = 0x70
	finalFlag    = 0x80
)

// second (frame) byte layout
const (
	sizeMask = 0x7f
	maskFlag = 0x80
)

// ErrRetry rejects a write. See method documentation!
var errRetry = errors.New("websocket: retry after error with differend payload size")

// AcceptV13 is a Conn.Accept value for all non-reserved opcodes from version 13.
const AcceptV13 = 1<<Continuation | 1<<Text | 1<<Binary | 1<<Close | 1<<Ping | 1<<Pong

// Conn is a low-level network abstraction confrom the net.Conn interface.
// Connections must be read consecutively for correct operation and closure.
//
// Conn does not act uppon control frames, except for Close. Write returns
// ClosedError after a Close frame was either send or received. Write also
// returns ClosedError (with NoStatusCode) when Read got io.EOF without any
// Close frame occurrence.
//
// Multiple goroutines may invoke methods on a Conn simultaneously.
type Conn struct {
	net.Conn
	// read & write lock
	readMutex, writeMutex sync.Mutex

	// pending number of bytes
	readPayloadN, writePayloadN int

	// When not zero, then receival of opcodes without a flag are rejected
	// with a connection Close, status code 1003—CannotAccept. Flags follow
	// little-endian bit order as in 1 << opcode. Use AcceptV13 to disable
	// all reserved opcodes.
	Accept uint

	// first byte of last frame read
	readHead uint32
	// first byte of next frame written
	writeHead uint32

	// read mask byte position
	maskI uint
	// read mask key
	mask uint32

	// set once a close frame is send or received.
	statusCode uint32

	// Pending number of bytes in buffer.
	readBufN, writeBufN int
	// Read number of bytes in buffer.
	readBufDone int
	// Read buffer fits compact frame: 2B header + 4B mask + 125B payload limit
	readBuf [131]byte
	// Write buffer fits compact frame: 2B header + 125B payload limit
	writeBuf [127]byte
}

// WriteClose sends a Close frame with best-effort. Operation does not block and
// the return is always a CloseError. When the connectection already received or
// send a Close then only the first status code remains in effect. The redundant
// status codes are discarded, including any corresponding Close notifications.
// Conn.Close must still be called, even after WriteClose.
func (c *Conn) WriteClose(statusCode uint, reason string) error {
	if !atomic.CompareAndSwapUint32(&c.statusCode, 0, uint32(statusCode)) {
		// already closed
		return c.closeError()
	}

	// The payload of control frames is limited to 125 bytes
	// and the status code takes 2.
	if len(reason) > 123 {
		reason = reason[:123]
	}

	go func() {
		c.writeMutex.Lock()
		defer c.writeMutex.Unlock()

		// best effort close notification; no pending errors
		if c.writeBufN <= 0 && c.writePayloadN <= 0 {
			c.writeBuf[0] = Close & finalFlag
			if statusCode == NoStatusCode {
				c.writeBuf[1] = 0
				c.Conn.Write(c.writeBuf[:2])
			} else {
				c.writeBuf[1] = byte(len(reason) + 2)
				c.writeBuf[2] = byte(statusCode >> 8)
				c.writeBuf[3] = byte(statusCode)
				copy(c.writeBuf[4:], reason)
				c.Conn.Write(c.writeBuf[:4+len(reason)])
			}
		}

		// Both *tls.Conn and *net.TCPConn offer CloseWrite.
		type CloseWriter interface {
			CloseWrite() error
		}
		if cc, ok := c.Conn.(CloseWriter); ok {
			cc.CloseWrite()
		}
	}()

	return ClosedError(statusCode)
}

// CloseError returns an error if c is closed.
func (c *Conn) closeError() error {
	statusCode := atomic.LoadUint32(&c.statusCode)
	if statusCode != 0 {
		return ClosedError(statusCode)
	}
	return nil
}

// SetWriteMode controls the send frame/fragment layout. When final, then each
// Write sends a message of the given type. The opcode must be in range [1, 15]
// like Text, Binary or Ping.
//
//	// send two text messages
//	c.SetWriteMode(websocket.Text, true)
//	io.WriteString(c, "hello")
//	io.WriteString(c, "👋")
//
// When not final then each Write sends a fragment of the message until a final
// Write concludes the message. This mode allows for sending a message that is
// of unknown size when the message is started without having to buffer. Another
// use-case is messages that would block the channel for too long, as control
// frames would have to wait in such case.
//
//	// send a binary message/stream
//	c.SetWriteMode(websocket.Binary, false)
//	io.Copy(c, blob)
//	c.SetWriteMode(websocket.Binary, true)
//	c.Write(nil)
//
// The opcode is written on the first Write after SetWriteMode. For the previous
// example, in case Copy did not receive any data, then the opcode of the second
// call to SetWriteMode would apply. Therefore it is recommended to use the same
// opcode when finalizing a message.
func (c *Conn) SetWriteMode(opcode uint, final bool) {
	head := opcode
	if final {
		head &= opcodeMask
		head |= finalFlag
	} else {
		head &= opcodeMask &^ ctrlFlag
	}
	atomic.StoreUint32(&c.writeHead, uint32(head))
}

// Write sends p in one frame conform the io.Writer interface. Error retries
// must continue with the same p(ayload), minus the n(umber) of bytes done.
// Control frames—opcode range [8, 15]—must not exceed 125 bytes.
// Zero payload causes an empty frame/fragment.
func (c *Conn) Write(p []byte) (n int, err error) {
	c.writeMutex.Lock()
	n, err = c.write(p)
	c.writeMutex.Unlock()
	return
}

func (c *Conn) write(p []byte) (n int, err error) {
	if err := c.closeError(); err != nil {
		return 0, err
	}

	// BUG(pascaldekloe): UTF-8 submission is not validated.

	// pending state/frame
	if c.writeBufN > 0 || c.writePayloadN > 0 {
		// inconsistent payload length breaks frame
		if c.writePayloadN != len(p) {
			return 0, errRetry
		}

		// write frame header
		if c.writeBufN > 0 {
			n, err := c.Conn.Write(c.writeBuf[:c.writeBufN])
			c.writeBufN -= n
			if err != nil {
				// shift out written bytes
				copy(c.writeBuf[:c.writeBufN], c.writeBuf[n:])
				return 0, err
			}
		}

		// write payload
		if c.writePayloadN > 0 {
			n, err = c.Conn.Write(p)
			c.writePayloadN -= n
		}
		return
	}

	// load buffer with header
	head := atomic.LoadUint32(&c.writeHead)
	c.writeBuf[0] = byte(head)
	if head&finalFlag == 0 && head&opcodeMask != Continuation {
		atomic.StoreUint32(&c.writeHead, Continuation|head&reservedMask)
	}
	if len(p) < 126 {
		// frame fits buffer; send one packet
		c.writeBuf[1] = byte(len(p))
		c.writeBufN = 2 + copy(c.writeBuf[2:], p)
		c.writePayloadN = 0
	} else if len(p) < 1<<16 {
		// encode 16-bit payload length
		c.writeBuf[1] = 126
		binary.BigEndian.PutUint16(c.writeBuf[2:4], uint16(len(p)))
		c.writeBufN = 4
		c.writePayloadN = len(p)
	} else {
		// encode 64-bit payload length
		c.writeBuf[1] = 127
		binary.BigEndian.PutUint64(c.writeBuf[2:10], uint64(len(p)))
		c.writeBufN = 10
		c.writePayloadN = len(p)
	}

	// send TCP packet
	n, err = c.Conn.Write(c.writeBuf[:c.writeBufN])
	c.writeBufN -= n
	if err != nil {
		// shift out written bytes
		copy(c.writeBuf[:c.writeBufN], c.writeBuf[n:])
		// undo payload in first TCP package
		c.writeBufN -= len(p) - c.writePayloadN
		if c.writeBufN >= 0 {
			return 0, err
		}
		return -c.writeBufN, err
	}

	// send payload remainder if writeBuf size exceeded
	if c.writePayloadN <= 0 {
		return len(p), nil
	}
	n, err = c.Conn.Write(p[len(p)-c.writePayloadN:])
	c.writePayloadN -= n
	return len(p) - c.writePayloadN, err
}

// ReadMode returns state information about the last Read. Read spans one
// message at a time. Final indicates that message is received in full.
func (c *Conn) ReadMode() (opcode uint, final bool) {
	head := uint(atomic.LoadUint32(&c.readHead))
	opcode = head & opcodeMask
	final = head&finalFlag != 0 && c.readPayloadN == 0
	return
}

// Read receives WebSocket frames confrom the io.Reader interface. ReadMode is
// updated on each call.
func (c *Conn) Read(p []byte) (n int, err error) {
	c.readMutex.Lock()
	n, err = c.read(p)
	c.readMutex.Unlock()
	return
}

func (c *Conn) read(p []byte) (n int, err error) {
	if c.readPayloadN == 0 {
		err := c.nextFrame()
		if err != nil {
			return 0, err
		}
	}

	// limit read to payload size
	if len(p) > c.readPayloadN {
		p = p[:c.readPayloadN]
	}

	// use buffer remainder
	n = copy(p, c.readBuf[c.readBufDone:c.readBufN])
	c.readBufDone += n
	// read from network
	if n < len(p) {
		var done int
		done, err = c.Conn.Read(p[n:])
		n += done
	}
	// register result
	c.readPayloadN -= n

	// deal with payload
	c.unmaskN(p[:n])
	// BUG(pascaldekloe): Broken UTF-8 receival is not rejected with Malformed.

	if err == io.EOF {
		if c.readPayloadN != 0 {
			err = io.ErrUnexpectedEOF
		}
		c.WriteClose(AbnormalClose, err.Error())
	}

	return
}

func (c *Conn) nextFrame() error {
	if err := c.ensureBufN(6); err != nil {
		// TODO: check mask missing?
		return err
	}

	// get frame header
	head := uint(c.readBuf[0])

	// Conn.readHead marker to distinguish between the zero value
	const headSetFlag = 1 << 8
	lastHead := uint(atomic.LoadUint32(&c.readHead))
	atomic.StoreUint32(&c.readHead, uint32(head|headSetFlag))

	// sequence validation
	if lastHead&headSetFlag != 0 {
		if lastHead&finalFlag == 0 {
			if head&opcodeMask != Continuation && head&ctrlFlag == 0 {
				return c.WriteClose(ProtocolError, "fragmented message interrupted")
			}
		} else {
			if head&opcodeMask == Continuation {
				return c.WriteClose(ProtocolError, "continuation of final message")
			}
		}
	}

	if head&reservedMask != 0 {
		return c.WriteClose(ProtocolError, "reserved bit set")
	}

	// second byte has mask flag and payload size
	head2 := uint(c.readBuf[1])
	c.readPayloadN = int(head2 & sizeMask)
	if head2&maskFlag == 0 {
		return c.WriteClose(ProtocolError, "no mask")
	}

	if head&ctrlFlag == 0 {
		// non-control frame

		if c.Accept != 0 && c.Accept&1<<(head&opcodeMask) == 0 {
			return c.WriteClose(CannotAccept, "opcode "+string('0'+head&opcodeMask))
		}

		switch c.readPayloadN {
		default:
			c.mask = maskOrder.Uint32(c.readBuf[2:6])
			c.readBufDone = 6
		case 126:
			if err := c.ensureBufN(8); err != nil {
				return err
			}
			c.readPayloadN = int(binary.BigEndian.Uint16(c.readBuf[2:4]))
			c.mask = maskOrder.Uint32(c.readBuf[4:8])
			c.readBufDone = 8
		case 127:
			if err := c.ensureBufN(14); err != nil {
				return err
			}
			size := binary.BigEndian.Uint64(c.readBuf[2:10])
			if size > uint64((^uint(0))>>1) {
				return c.WriteClose(TooBig, "word size exceeded")
			}
			c.readPayloadN = int(size)
			c.mask = maskOrder.Uint32(c.readBuf[10:14])
			c.readBufDone = 14
		}
		c.maskI = 0

		return nil
	}
	// control frame

	if head&finalFlag == 0 {
		return c.WriteClose(ProtocolError, "control frame not final")
	}

	if c.readPayloadN > 125 {
		return c.WriteClose(ProtocolError, "control frame size")
	}

	if err := c.ensureBufN(c.readPayloadN + 6); err != nil {
		return err
	}
	c.mask = maskOrder.Uint32(c.readBuf[2:6])
	c.maskI = 0
	c.readBufDone = 6
	c.unmaskN(c.readBuf[6 : 6+c.readPayloadN])

	if head&opcodeMask == Close {
		if c.readPayloadN < 2 {
			return c.WriteClose(NoStatusCode, "")
		}
		return c.WriteClose(uint(binary.BigEndian.Uint16(c.readBuf[6:8])), string(c.readBuf[8:c.readBufN]))
	}

	return nil
}

// EnsureBufN reads until buf has at least n bytes.
// Any remaining data is moved to the beginning of the buffer.
func (c *Conn) ensureBufN(n int) error {
	if c.readBufDone != 0 {
		c.readBufN = copy(c.readBuf[:], c.readBuf[c.readBufDone:c.readBufN])
		c.readBufDone = 0
	}
	for c.readBufN < n {
		done, err := c.Conn.Read(c.readBuf[c.readBufN:])
		c.readBufN += done
		if err != nil {
			if err == io.EOF {
				if c.readBufN != 0 {
					err = io.ErrUnexpectedEOF
				}
				c.WriteClose(AbnormalClose, err.Error())
				if c.readBufN >= n {
					return nil
				}
			}
			return err
		}
	}

	return nil
}

var maskOrder = binary.LittleEndian

func (c *Conn) unmaskN(p []byte) {
	if len(p) < 8 {
		for i := range p {
			p[i] ^= byte(c.mask >> ((c.maskI & 3) * 8))
			c.maskI++
		}
		return
	}

	word := uint64(c.mask)
	word |= word << 32
	word = bits.RotateLeft64(word, -int(8*c.maskI))

	var i int
	for ; len(p)-i > 7; i += 8 {
		maskOrder.PutUint64(p[i:], maskOrder.Uint64(p[i:])^word)
	}
	// multipe of 8 does not change maskI

	for ; i < len(p); i++ {
		p[i] ^= byte(c.mask >> ((c.maskI & 3) * 8))
		c.maskI++
	}
}

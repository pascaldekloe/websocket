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
	opcodeBits   = 0x0f
	ctrlFlag     = 0x08
	reservedBits = 0x70
	finalFlag    = 0x80
)

// second (frame) byte layout
const (
	sizeBits = 0x7f
	maskFlag = 0x80
)

// ErrRetry rejects a write. See method documentation!
var errRetry = errors.New("websocket: retry after error with differend payload size")

// AcceptV13 is a Conn.Accept value for all non-reserved opcodes from version 13.
const AcceptV13 = 1<<Continuation | 1<<Text | 1<<Binary | 1<<Close | 1<<Ping | 1<<Pong

// Conn is a low-level network abstraction confrom the net.Conn interface.
// Go connections must be read consecutively.
type Conn struct {
	net.Conn
	// read & write lock
	rMux, wMux sync.Mutex

	// pending number of bytes
	rPayloadN uint64
	wPayloadN int

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
	rBufN, wBufN int
	// Read number of bytes in buffer.
	rBufDone int
	// Read buffer fits compact frame: 2B header + 4B mask + 125B payload limit
	rBuf [131]byte
	// Write buffer fits compact frame: 2B header + 125B payload limit
	wBuf [127]byte
}

func (c *Conn) writeClose(statusCode uint, reason string) error {
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
		c.wMux.Lock()
		defer c.wMux.Unlock()

		// best effort close notification; no pending errors
		if c.wBufN <= 0 && c.wPayloadN <= 0 {
			c.wBuf[0] = Close & finalFlag
			if statusCode == NoStatusCode {
				c.wBuf[1] = 0
				c.Conn.Write(c.wBuf[:2])
			} else {
				c.wBuf[1] = byte(len(reason) + 2)
				c.wBuf[2] = byte(statusCode >> 8)
				c.wBuf[3] = byte(statusCode)
				copy(c.wBuf[4:], reason)
				c.Conn.Write(c.wBuf[:4+len(reason)])
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

// WriteFinal sets a Write mode in which each call sends a message of the given
// content type. The opcode must be in range [1, 15] like Text, Binary or Ping.
//
//	// send two text messages
//	c.WriteFinal(websocket.Text)
//	io.WriteString(c, "hello")
//	io.WriteString(c, "👋")
//
// When in streaming mode (with WriteStream), then the first Write call after
// WriteFinal concludes the message fragments.
//
//	// send a binary message/stream
//	c.WriteStream(websocket.Binary)
//	io.Copy(c, blob)
//	c.WriteFinal(websocket.Binary)
//	c.Write(nil)
//
// The opcode from WriteStream applies when at least one Write occurred before
// Write final. Otherwise the opcode of Write final is used for the last Write.
// Therefore, it is recommended to use the same opcode on both.
func (c *Conn) WriteFinal(opcode uint) {
	head := opcode&opcodeBits | finalFlag
	atomic.StoreUint32(&c.writeHead, uint32(head))
}

// WriteStream sets a Write mode in which each call sends the next fragment until
// WriteFinal is called. The first Write after WriteFinal concludes the message.
// The opcode must be in range [1, 7] like Text or Binary.
func (c *Conn) WriteStream(opcode uint) {
	head := opcode & (opcodeBits &^ ctrlFlag)
	atomic.StoreUint32(&c.writeHead, uint32(head))
}

// Write sends p in one frame conform the io.Writer interface. Error retries
// must continue with the same p(ayload), minus the n(umber) of bytes done.
// Control frames—opcode range [8, 15]—must not exceed 125 bytes.
// Zero payload causes an empty frame/fragment.
func (c *Conn) Write(p []byte) (n int, err error) {
	c.wMux.Lock()
	defer c.wMux.Unlock()

	if err := c.closeError(); err != nil {
		return 0, err
	}

	// BUG(pascaldekloe): UTF-8 submission is not validated.

	// pending state/frame
	if c.wBufN > 0 || c.wPayloadN > 0 {
		// inconsistent payload length breaks frame
		if c.wPayloadN != len(p) {
			return 0, errRetry
		}

		// write frame header
		if c.wBufN > 0 {
			n, err := c.Conn.Write(c.wBuf[:c.wBufN])
			c.wBufN -= n
			if err != nil {
				// shift out written bytes
				copy(c.wBuf[:c.wBufN], c.wBuf[n:])
				return 0, err
			}
		}

		// write payload
		if c.wPayloadN > 0 {
			n, err = c.Conn.Write(p)
			c.wPayloadN -= n
		}
		return
	}

	// load buffer with header
	head := atomic.LoadUint32(&c.writeHead)
	c.wBuf[0] = byte(head)
	if head&finalFlag == 0 && head&opcodeBits != Continuation {
		atomic.StoreUint32(&c.writeHead, Continuation|head&reservedBits)
	}
	if len(p) < 126 {
		// frame fits buffer; send one packet
		c.wBuf[1] = byte(len(p))
		c.wBufN = 2 + copy(c.wBuf[2:], p)
		c.wPayloadN = 0
	} else if len(p) < 1<<16 {
		// encode 16-bit payload length
		c.wBuf[1] = 126
		binary.BigEndian.PutUint16(c.wBuf[2:4], uint16(len(p)))
		c.wBufN = 4
		c.wPayloadN = len(p)
	} else {
		// encode 64-bit payload length
		c.wBuf[1] = 127
		binary.BigEndian.PutUint64(c.wBuf[2:10], uint64(len(p)))
		c.wBufN = 10
		c.wPayloadN = len(p)
	}

	// send TCP packet
	n, err = c.Conn.Write(c.wBuf[:c.wBufN])
	c.wBufN -= n
	if err != nil {
		// shift out written bytes
		copy(c.wBuf[:c.wBufN], c.wBuf[n:])
		// undo payload in first TCP package
		c.wBufN -= len(p) - c.wPayloadN
		if c.wBufN >= 0 {
			return 0, err
		}
		return -c.wBufN, err
	}

	// send payload remainder if wBuf size exceeded
	if c.wPayloadN <= 0 {
		return len(p), nil
	}
	n, err = c.Conn.Write(p[len(p)-c.wPayloadN:])
	c.wPayloadN -= n
	return len(p) - c.wPayloadN, err
}

// ReadMode returns state information about message receival. Read spans one
// message at a time. When final then the last Read concluded the message.
// The opcode is in range [1, 15]—Continuation is hidden—and the code does not
// change until a final passed.
func (c *Conn) ReadMode() (opcode uint, final bool) {
	head := uint(atomic.LoadUint32(&c.readHead))
	opcode = head & opcodeBits
	final = head&finalFlag != 0 && c.rPayloadN == 0
	return
}

// Read receives WebSocket frames confrom the io.Reader interface. ReadMode is
// updated on each call.
func (c *Conn) Read(p []byte) (n int, err error) {
	c.rMux.Lock()
	defer c.rMux.Unlock()

	if c.rPayloadN == 0 {
		err := c.nextFrame()
		if err != nil {
			return 0, err
		}
	}

	// limit read to payload size
	if uint64(len(p)) > c.rPayloadN {
		p = p[:c.rPayloadN]
	}

	// use buffer remainder
	n = copy(p, c.rBuf[c.rBufDone:c.rBufN])
	c.rBufDone += n
	// read from network
	if n < len(p) {
		var done int
		done, err = c.Conn.Read(p[n:])
		n += done
	}
	// register result
	c.rPayloadN -= uint64(n)

	// deal with payload
	c.unmaskN(p[:n])
	// BUG(pascaldekloe): Broken UTF-8 receival is not rejected with Malformed.

	if err == io.EOF {
		if c.rPayloadN != 0 {
			err = io.ErrUnexpectedEOF
		}
		c.writeClose(AbnormalClose, err.Error())
		if c.rPayloadN == 0 {
			// delay error
			err = nil
		}
	}

	return
}

func (c *Conn) nextFrame() error {
	if err := c.ensureBufN(6); err != nil {
		// TODO: check mask missing?
		return err
	}

	// get frame header
	head := uint(c.rBuf[0])

	// Conn.readHead marker to distinguish between the zero value
	const headSetFlag = 1 << 8
	lastHead := uint(atomic.LoadUint32(&c.readHead))
	atomic.StoreUint32(&c.readHead, uint32(head|headSetFlag))

	// sequence validation
	if lastHead&headSetFlag != 0 {
		if lastHead&finalFlag == 0 {
			if head&opcodeBits != Continuation && head&ctrlFlag == 0 {
				return c.writeClose(ProtocolError, "fragmented message interrupted")
			}
		} else {
			if head&opcodeBits == Continuation {
				return c.writeClose(ProtocolError, "continuation of final message")
			}
		}
	}

	if head&reservedBits != 0 {
		return c.writeClose(ProtocolError, "reserved bit set")
	}

	// second byte has mask flag and payload size
	head2 := uint(c.rBuf[1])
	c.rPayloadN = uint64(head2 & sizeBits)
	if head2&maskFlag == 0 {
		return c.writeClose(ProtocolError, "no mask")
	}

	if head&ctrlFlag == 0 {
		// non-control frame

		if c.Accept != 0 && c.Accept&1<<(head&opcodeBits) == 0 {
			return c.writeClose(CannotAccept, "opcode "+string('0'+head&opcodeBits))
		}

		switch c.rPayloadN {
		default:
			c.mask = maskOrder.Uint32(c.rBuf[2:6])
			c.rBufDone = 6
		case 126:
			if err := c.ensureBufN(8); err != nil {
				return err
			}
			c.rPayloadN = uint64(binary.BigEndian.Uint16(c.rBuf[2:4]))
			c.mask = maskOrder.Uint32(c.rBuf[4:8])
			c.rBufDone = 8
		case 127:
			if err := c.ensureBufN(14); err != nil {
				return err
			}
			c.rPayloadN = binary.BigEndian.Uint64(c.rBuf[2:10])
			c.mask = maskOrder.Uint32(c.rBuf[10:14])
			c.rBufDone = 14
		}
		c.maskI = 0

		return nil
	}
	// control frame

	if head&finalFlag == 0 {
		return c.writeClose(ProtocolError, "control frame not final")
	}

	if c.rPayloadN > 125 {
		return c.writeClose(ProtocolError, "control frame size")
	}

	if err := c.ensureBufN(int(c.rPayloadN) + 6); err != nil {
		return err
	}
	c.mask = maskOrder.Uint32(c.rBuf[2:6])
	c.maskI = 0
	c.rBufDone = 6
	c.unmaskN(c.rBuf[6 : 6+int(c.rPayloadN)])

	if head&opcodeBits == Close {
		if c.rPayloadN < 2 {
			return c.writeClose(NoStatusCode, "")
		}
		return c.writeClose(uint(binary.BigEndian.Uint16(c.rBuf[6:8])), string(c.rBuf[8:c.rBufN]))
	}

	return nil
}

// EnsureBufN reads until buf has at least n bytes.
// Any remaining data is moved to the beginning of the buffer.
func (c *Conn) ensureBufN(n int) error {
	if c.rBufDone != 0 {
		c.rBufN = copy(c.rBuf[:], c.rBuf[c.rBufDone:c.rBufN])
		c.rBufDone = 0
	}
	for c.rBufN < n {
		done, err := c.Conn.Read(c.rBuf[c.rBufN:])
		c.rBufN += done
		if err != nil {
			if err == io.EOF {
				if c.rBufN != 0 {
					err = io.ErrUnexpectedEOF
				}
				c.writeClose(AbnormalClose, err.Error())
				if c.rBufN >= n {
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

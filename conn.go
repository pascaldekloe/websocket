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

const (
	// distinguish between the zero value
	statusCodeSetFlag = 0x10000
	statusCodeMask    = 0xffff
)

var byteOrder = binary.BigEndian

// ErrRetry rejects a write. See method documentation!
var errRetry = errors.New("websocket: retry after error with differend payload size")

// AcceptV13 is a Conn.Accept value for all non-reserved opcodes from version 13.
const AcceptV13 = 1<<Continuation | 1<<Text | 1<<Binary | 1<<Close | 1<<Ping | 1<<Pong

// Conn offers a low-level network abstraction confrom the net.Conn interface.
// These opeartions do not act uppon control frames, except for Close. Write
// returns a ClosedError after a Close frame was either send or received. Write
// also returns a ClosedError (with NoStatusCode) when Read got io.EOF without
// any Close frame occurrence. Multiple goroutines may invoke net.Conn methods
// simultaneously.
//
// Conn also offers high-level abstraction with the Receive and Send methods,
// including ErrUTF8 protection.
//
// Connections must be read consecutively for correct operation and closure.
type Conn struct {
	net.Conn

	// When not zero, then receival of opcodes without a flag are rejected
	// with a connection Close, status code 1003—CannotAccept. Flags follow
	// little-endian bit order as in 1 << opcode. Use AcceptV13 to disable
	// all reserved opcodes.
	Accept uint

	// read & write lock
	readMutex, writeMutex sync.Mutex

	// pending number of bytes
	readPayloadN, writePayloadN int

	// first byte of last frame read
	readHead uint32
	// first byte of next frame written
	writeHead uint32

	// read mask byte position
	maskI uint
	// read mask key
	mask uint64

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

func (c *Conn) setClose(statusCode uint, reason string) bool {
	return atomic.CompareAndSwapUint32(&c.statusCode, 0, uint32(statusCode|statusCodeSetFlag))
}

// CloseError returns an error if c is closed.
func (c *Conn) closeError() error {
	statusCode := atomic.LoadUint32(&c.statusCode)
	if statusCode != 0 {
		return ClosedError(statusCode & statusCodeMask)
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
	c.writeBuf[0] = byte(atomic.LoadUint32(&c.writeHead))
	if len(p) < 126 {
		// frame fits buffer; send one packet
		c.writeBuf[1] = byte(len(p))
		c.writeBufN = 2 + copy(c.writeBuf[2:], p)
		c.writePayloadN = 0
	} else if len(p) < 1<<16 {
		// encode 16-bit payload length
		c.writeBuf[1] = 126
		byteOrder.PutUint16(c.writeBuf[2:4], uint16(len(p)))
		c.writeBufN = 4
		c.writePayloadN = len(p)
	} else {
		// encode 64-bit payload length
		c.writeBuf[1] = 127
		byteOrder.PutUint64(c.writeBuf[2:10], uint64(len(p)))
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
	} else {
		// set opcode to Continue/zero
		atomic.StoreUint32(&c.readHead, atomic.LoadUint32(&c.readHead)&finalFlag)
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

	if err == io.EOF {
		if c.readPayloadN != 0 {
			err = io.ErrUnexpectedEOF
		}
		c.SendClose(AbnormalClose, err.Error())
	}

	return
}

func (c *Conn) nextFrame() error {
	if c.readBufDone != 0 {
		// move read ahead to beginning of buffer
		c.readBufN = copy(c.readBuf[:], c.readBuf[c.readBufDone:c.readBufN])
		c.readBufDone = 0
	}

	if err := c.ensureBufN(6); err != nil {
		// TODO: check mask missing?
		return err
	}

	// get frame header
	head := uint(c.readBuf[0])
	atomic.StoreUint32(&c.readHead, uint32(head))

	if head&reservedMask != 0 {
		return c.SendClose(ProtocolError, "reserved bit set")
	}

	// second byte has mask flag and payload size
	head2 := uint(c.readBuf[1])
	c.readPayloadN = int(head2 & sizeMask)
	if head2&maskFlag == 0 {
		return c.SendClose(ProtocolError, "no mask")
	}

	if c.Accept != 0 && c.Accept&(1<<(head&opcodeMask)) == 0 {
		var raeson string
		opcode := head & opcodeMask
		if opcode < 10 {
			raeson = "opcode " + string('0'+opcode)
		} else {
			raeson = "opcode 1" + string('0'+opcode/10)
		}
		return c.SendClose(CannotAccept, raeson)
	}

	if head&ctrlFlag == 0 {
		// non-control frame
		switch c.readPayloadN {
		default:
			c.mask = uint64(byteOrder.Uint32(c.readBuf[2:6]))
			c.readBufDone = 6
		case 126:
			if err := c.ensureBufN(8); err != nil {
				return err
			}
			c.readPayloadN = int(byteOrder.Uint16(c.readBuf[2:4]))
			c.mask = uint64(byteOrder.Uint32(c.readBuf[4:8]))
			c.readBufDone = 8
		case 127:
			if err := c.ensureBufN(14); err != nil {
				return err
			}
			size := byteOrder.Uint64(c.readBuf[2:10])
			if size > uint64((^uint(0))>>1) {
				return c.SendClose(TooBig, "word size exceeded")
			}
			c.readPayloadN = int(size)
			c.mask = uint64(byteOrder.Uint32(c.readBuf[10:14]))
			c.readBufDone = 14
		}
		c.mask |= c.mask << 32
		c.maskI = 0

		return nil
	}
	// control frame

	if head&finalFlag == 0 {
		return c.SendClose(ProtocolError, "control frame not final")
	}

	if c.readPayloadN > 125 {
		return c.SendClose(ProtocolError, "control frame size")
	}

	if err := c.ensureBufN(c.readPayloadN + 6); err != nil {
		return err
	}
	c.mask = uint64(byteOrder.Uint32(c.readBuf[2:6]))
	c.mask |= c.mask << 32
	c.maskI = 0
	c.readBufDone = 6

	c.unmaskN(c.readBuf[6 : 6+c.readPayloadN])

	if head&opcodeMask == Close {
		if c.readPayloadN < 2 {
			return c.SendClose(NoStatusCode, "")
		}
		return c.SendClose(uint(byteOrder.Uint16(c.readBuf[6:8])), string(c.readBuf[8:6+c.readPayloadN]))
	}

	return nil
}

// EnsureBufN reads until buf has at least n bytes.
// Any remaining data is moved to the beginning of the buffer.
func (c *Conn) ensureBufN(n int) error {
	for c.readBufN < n {
		done, err := c.Conn.Read(c.readBuf[c.readBufN:])
		c.readBufN += done
		if err != nil {
			if err == io.EOF {
				if c.readBufN != 0 {
					err = io.ErrUnexpectedEOF
				}
				c.SendClose(AbnormalClose, err.Error())
				if c.readBufN >= n {
					return nil
				}
			}
			return err
		}
	}

	return nil
}

func (c *Conn) unmaskN(p []byte) {
	if len(p) < 8 {
		for i := range p {
			p[i] ^= byte(c.mask >> ((^c.maskI & 3) * 8))
			c.maskI++
		}
		return
	}

	word := bits.RotateLeft64(c.mask, int(8*c.maskI))

	var i int
	for ; len(p)-i > 7; i += 8 {
		byteOrder.PutUint64(p[i:], byteOrder.Uint64(p[i:])^word)
	}
	// multipe of 8 does not change maskI

	for ; i < len(p); i++ {
		p[i] ^= byte(c.mask >> ((^c.maskI & 3) * 8))
		c.maskI++
	}
}

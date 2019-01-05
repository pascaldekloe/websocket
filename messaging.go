// Package websocket implements the server-side of “The WebSocket Protocol” RFC 6455.
package websocket

import (
	"io"
	"net"
	"time"
)

// Listener gets called on message receival. The size is negative when unknown.
// Implementations should read r and return with minimal delay.
type Listener func(r io.Reader, size int)

// Messaging is a high-level network abstraction for safety and convenience.
type Messaging struct {
	conn           *Conn
	writeSemaphore chan struct{}
	notify         [16]Listener
	idleTimeout    time.Duration
	wireTimeout    time.Duration
}

// Take possession of a Conn.
func Take(c *Conn, notify [16]Listener, wireTimeout, idleTimeout time.Duration) *Messaging {
	c.Accept = 1<<Continuation | 1<<Close | 1<<Ping | 1<<Pong
	for i, f := range notify {
		if f != nil {
			c.Accept |= 1 << uint(i)
		}
	}

	m := &Messaging{
		conn:           c,
		writeSemaphore: make(chan struct{}, 1),
		notify:         notify,
		idleTimeout:    idleTimeout,
		wireTimeout:    wireTimeout,
	}

	go m.run()

	return m
}
func (m *Messaging) read(p []byte) (n int, err error) {
	m.conn.SetReadDeadline(time.Now().Add(m.wireTimeout))

	n, err = m.conn.read(p)
	for err != nil {
		e, ok := err.(net.Error)
		if !ok {
			return
		}
		if e.Timeout() {
			err = m.conn.WriteClose(Policy, "read timout")
			return
		}
		if !e.Temporary() {
			return
		}

		time.Sleep(100 * time.Microsecond)
		var more int
		more, err = m.conn.read(p[n:])
		n += more
	}

	return
}
func (m *Messaging) write(p []byte) (n int, err error) {
	m.conn.SetWriteDeadline(time.Now().Add(m.wireTimeout))

	n, err = m.conn.write(p)
	for err != nil {
		e, ok := err.(net.Error)
		if !ok {
			return
		}
		if e.Timeout() {
			err = m.conn.WriteClose(Policy, "write timout")
			return
		}
		if !e.Temporary() {
			return
		}

		time.Sleep(100 * time.Microsecond)
		var more int
		more, err = m.conn.write(p[n:])
		n += more
	}

	return
}

func (m *Messaging) run() error {
	var buf [125]byte

	for {
		m.conn.SetReadDeadline(time.Now().Add(m.idleTimeout))
		_, err := m.conn.read(nil)
		for err != nil {
			e, ok := err.(net.Error)
			if !ok {
				return err
			}
			if e.Timeout() {
				return m.conn.WriteClose(Policy, "idle timout")
			}
			if !e.Temporary() {
				return err
			}

			time.Sleep(100 * time.Microsecond)
			_, err = m.conn.read(nil)
		}

		opcode, final := m.conn.ReadMode()

		if opcode&ctrlFlag != 0 {
			// read payload
			n, err := m.read(buf[:])
			if err != nil {
				return err
			}

			// react
			switch opcode {
			case Ping:
				go func() {
					m.conn.writeMutex.Lock()
					head := m.conn.writeHead
					m.conn.SetWriteMode(Pong, true)
					m.write(buf[:n])
					m.conn.writeHead = head
					m.conn.writeMutex.Unlock()
				}()
			}

			continue
		}

		ln := m.notify[opcode]
		if final {
			ln(readEOF{}, 0)
			continue
		}

		size := -1
		if m.conn.readHead&finalFlag != 0 {
			size = m.conn.readPayloadN
		}

		r := &messageReader{messaging: m}
		ln(r, size)
		// flush
		for r.err == nil {
			r.Read(buf[:])
		}
	}
}

type messageReader struct {
	messaging *Messaging
	err       error
}

func (r *messageReader) Read(p []byte) (n int, err error) {
	if r.err != nil {
		return 0, r.err
	}

	n, err = r.messaging.read(p)
	if err != nil {
		r.err = err
		return
	}

	if _, final := r.messaging.conn.ReadMode(); final {
		err = io.EOF
		r.err = err
	}
	return
}

func (m *Messaging) Send(opcode uint, message []byte) error {
	m.writeSemaphore <- struct{}{}

	m.conn.SetWriteMode(opcode, true)
	_, err := m.write(message)

	<-m.writeSemaphore

	return err
}

func (m *Messaging) SendStream(opcode uint) io.WriteCloser {
	m.writeSemaphore <- struct{}{}

	m.conn.SetWriteMode(opcode, false)

	return &messageWriter{messaging: m}
}

type messageWriter struct {
	messaging *Messaging
	closed    bool
}

func (w *messageWriter) Write(p []byte) (n int, err error) {
	if w.closed {
		return 0, io.ErrClosedPipe
	}

	w.messaging.conn.writeMutex.Lock()
	n, err = w.messaging.write(p)
	w.messaging.conn.writeMutex.Unlock()
	return
}

func (w messageWriter) Close() error {
	if w.closed {
		return nil
	}

	head := w.messaging.conn.writeHead
	if head&opcodeMask != Continuation {
		// nothing written yet
		w.closed = true
		<-w.messaging.writeSemaphore
		return nil
	}

	w.messaging.conn.writeHead = head | finalFlag

	_, err := w.Write(nil)
	if err == nil {
		w.closed = true
		<-w.messaging.writeSemaphore
	}
	return err
}

type readEOF struct{}

func (r readEOF) Read([]byte) (int, error) {
	return 0, io.EOF
}

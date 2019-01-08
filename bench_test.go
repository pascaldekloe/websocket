package websocket

import (
	"io"
	"net"
	"testing"
	"time"
)

func BenchmarkReceive(b *testing.B) {
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		b.Fatal(err)
	}

	// concatenate all golden frames
	var frames []byte
	var messageCount, messageSize int
	for _, gold := range GoldenFrames {
		frames = append(frames, gold.Masked...)
		messageCount++
		messageSize += len(gold.Message)
	}
	for _, gold := range GoldenFragments {
		for _, s := range gold.Maskeds {
			frames = append(frames, s...)
			messageCount++
			messageSize += len(s)
		}
	}

	// feed testEnd
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			for err == nil {
				_, err = conn.Write(frames)
			}
		}
	}()

	b.Run("buffer", func(b *testing.B) {
		b.SetBytes(int64(messageSize / messageCount))
		b.ReportAllocs()

		conn := dialListener(b, ln)
		buf := make([]byte, 100*1024)
		for i := 0; i < b.N; i++ {
			_, _, err := conn.Receive(buf, time.Millisecond, time.Millisecond)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("stream", func(b *testing.B) {
		b.SetBytes(int64(messageSize / messageCount))
		b.ReportAllocs()

		conn := dialListener(b, ln)
		buf := make([]byte, 1024)
		for i := 0; i < b.N; i++ {
			_, r, err := conn.ReceiveStream(time.Millisecond, time.Millisecond)
			if err != nil {
				b.Fatal(err)
			}
			for {
				_, err = r.Read(buf)
				if err == io.EOF {
					break
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	})

	b.Run("tcp", func(b *testing.B) {
		b.SetBytes(int64(messageSize / messageCount))
		b.ReportAllocs()

		conn := dialListener(b, ln).Conn
		buf := make([]byte, 100*1024)
		for i := 0; i < b.N; i++ {
			conn.SetReadDeadline(time.Now().Add(time.Millisecond))
			size := messageSize / messageCount
			for {
				n, err := conn.Read(buf[:size])
				if err != nil {
					b.Fatal(err)
				}
				if size -= n; size == 0 {
					break
				}
			}
		}
	})
}

func BenchmarkSend(b *testing.B) {
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		b.Fatal(err)
	}

	// concatenate all golden messages
	var messages [][]byte
	var opcodes []uint
	var messageCount, messageSize int
	for _, gold := range GoldenFrames {
		messages = append(messages, []byte(gold.Message))
		opcodes = append(opcodes, gold.Opcode)
		messageCount++
		messageSize += len(gold.Message)
	}
	for _, gold := range GoldenFragments {
		for _, s := range gold.Messages {
			messages = append(messages, []byte(s))
			opcodes = append(opcodes, gold.Opcode)
			messageCount++
			messageSize += len(s)
		}
	}

	// drain testEnd
	go func() {
		buf := make([]byte, 1024)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			for err == nil {
				_, err = conn.Read(buf)
			}
		}
	}()

	b.Run("buffer", func(b *testing.B) {
		b.SetBytes(int64(messageSize / messageCount))
		b.ReportAllocs()

		conn := dialListener(b, ln)
		for i := 0; i < b.N; i++ {
			err := conn.Send(opcodes[i%len(opcodes)], messages[i%len(messages)], time.Millisecond)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("stream", func(b *testing.B) {
		b.SetBytes(int64(messageSize / messageCount))
		b.ReportAllocs()

		conn := dialListener(b, ln)
		for i := 0; i < b.N; i++ {
			w := conn.SendStream(opcodes[i%len(opcodes)], time.Millisecond)
			_, err := w.Write(messages[i%len(messages)])
			if err != nil {
				b.Fatal(err)
			}
			if err := w.Close(); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("tcp", func(b *testing.B) {
		b.SetBytes(int64(messageSize / messageCount))
		b.ReportAllocs()

		conn := dialListener(b, ln).Conn
		for i := 0; i < b.N; i++ {
			conn.SetWriteDeadline(time.Now().Add(time.Millisecond))
			_, err := conn.Write(messages[i%len(messages)])
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func dialListener(tb testing.TB, ln net.Listener) *Conn {
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		tb.Fatal(err)
	}
	return &Conn{Conn: c}
}

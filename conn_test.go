package websocket

import (
	"bytes"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

var GoldenFrames = []struct {
	Opcode  uint
	Message string
	Frame   string
	Masked  string
}{
	0: {Text, "",
		"\x81\x00",
		"\x81\x80\x12\x34\x56\x78"},
	1: {Binary, "\a",
		"\x82\x01\a",
		"\x82\x81\x12\x34\x56\x78\x15"},
	2: {Text, "hello",
		"\x81\x05hello",
		"\x81\x85\x12\x34\x56\x78\x7a\x51\x3a\x14\x7d"},
	3: {Text, strings.Repeat("!", 126),
		"\x81\x7e\x00\x7e" + strings.Repeat("!", 126),
		"\x81\xfe\x00\x7e\x12\x34\x56\x78" + strings.Repeat("\x33\x15\x77\x59", 31) + "\x33\x15"},
	4: {Binary, string(make([]byte, 1<<16)),
		"\x82\x7f\x00\x00\x00\x00\x00\x01\x00\x00" + string(make([]byte, 1<<16)),
		"\x82\xff\x00\x00\x00\x00\x00\x01\x00\x00\x12\x34\x56\x78" + strings.Repeat("\x12\x34\x56\x78", 1<<16/4)},
}

func TestWrite(t *testing.T) {
	for _, gold := range GoldenFrames {
		conn, testEnd := pipeConn()

		// collect from test end
		done := make(chan *bytes.Buffer)
		go func() {
			var got bytes.Buffer
			_, err := got.ReadFrom(iotest.OneByteReader(testEnd))
			if err != nil {
				t.Errorf("%#x: test end read error: %s", gold.Frame, err)
			}
			done <- &got
		}()

		// sumbit message
		conn.SetWriteMode(gold.Opcode, true)
		n, err := conn.Write([]byte(gold.Message))
		if err != nil {
			t.Errorf("%#x: connection write error: %s", gold.Frame, err)
		}
		if want := len(gold.Message); n != want {
			t.Errorf("%#x: connection wrote %d bytes, want %d", gold.Frame, n, want)
		}

		// close connection to EOF test end
		if err := conn.Close(); err != nil {
			t.Errorf("%#x: connection close error: %s", gold.Frame, err)
		}

		if got := <-done; got.String() != gold.Frame {
			t.Errorf("%#x: got %#x", gold.Frame, got.String())
			t.Errorf("%#x: want %#x", gold.Frame, gold.Frame)
		}
	}
}

func TestRead(t *testing.T) {
	for _, gold := range GoldenFrames {
		conn, testEnd := pipeConn()

		// submit from test end
		done := make(chan struct{})
		go func() {
			defer close(done)

			_, err := io.Copy(testEnd, iotest.OneByteReader(strings.NewReader(gold.Masked)))
			switch err {
			case nil:
				break
			case io.ErrClosedPipe:
				t.Errorf("%#x: test end write timeout", gold.Masked)
			default:
				t.Errorf("%#x: test end write error: %s", gold.Masked, err)
			}
		}()

		// collect message
		var got []byte
		for readCount := 0; ; readCount++ {
			buf := make([]byte, 1024)
			n, err := conn.Read(buf)
			if err != nil {
				t.Errorf("%#x: connection read %d error: %s", gold.Masked, readCount, err)
				break
			}
			got = append(got, buf[:n]...)

			opcode, final := conn.ReadMode()
			if readCount == 0 && opcode != gold.Opcode {
				t.Errorf("%#x: connection read %d got opcode %d, want %d", gold.Masked, readCount, opcode, gold.Opcode)
			} else if readCount != 0 && opcode != Continuation {
				t.Errorf("%#x: connection read %d got opcode %d, want %d", gold.Masked, readCount, opcode, Continuation)
			}
			if final {
				break
			}
		}

		if string(got) != gold.Message {
			t.Errorf("%#x: got %#x", gold.Masked, got)
			t.Errorf("%#x: want %#x", gold.Masked, gold.Message)
		}

		<-done
	}
}

var GoldenFragments = []struct {
	Opcode   uint
	Messages []string
	Frames   []string
	Maskeds  []string
}{
	0: {Text, []string{"", ""},
		[]string{"\x01\x00", "\x80\x00"},
		[]string{"\x01\x80\x12\x34\x56\x78", "\x80\x80\x12\x34\x56\x78"}},
	1: {Binary, []string{"", "\a"},
		[]string{"\x02\x00", "\x80\x01\a"},
		[]string{"\x02\x80\x12\x34\x56\x78", "\x80\x81\x12\x34\x56\x78\x15"}},
	2: {Binary, []string{"\a", ""},
		[]string{"\x02\x01\a", "\x80\x00"},
		[]string{"\x02\x81\x12\x34\x56\x78\x15", "\x80\x80\x12\x34\x56\x78"}},
}

func TestFragment(t *testing.T) {
	for goldIndex, gold := range GoldenFragments {
		conn, testEnd := pipeConn()

		var wg sync.WaitGroup

		// write on test end
		wg.Add(1)
		go func() {
			defer wg.Done()

			var bytes []byte
			for _, frame := range gold.Maskeds {
				bytes = append(bytes, frame...)
			}

			if _, err := testEnd.Write([]byte(bytes)); err != nil {
				t.Errorf("%d: test end write error: %s", goldIndex, err)
			}
		}()

		// validate and echo on connection
		wg.Add(1)
		go func() {
			defer wg.Done()

			for _, want := range gold.Messages {
				// read
				var buf [1024]byte
				n, err := conn.Read(buf[:])
				if err != nil {
					t.Errorf("%d: connection read error: %s", goldIndex, err)
					return
				}

				// validate
				if got := string(buf[:n]); got != want {
					t.Errorf("%d: connection read message %#x", goldIndex, got)
					t.Errorf("%d: connection want message %#x", goldIndex, want)
					return
				}

				// echo
				opcode, final := conn.ReadMode()
				conn.SetWriteMode(opcode, final)
				if _, err = conn.Write(buf[:n]); err != nil {
					t.Errorf("%d: connection write error: %s", goldIndex, err)
					return
				}
			}
		}()

		// read (and compare) on test end
		for _, want := range gold.Frames {
			// read
			buf := make([]byte, 1024)
			n, err := testEnd.Read(buf)
			if err != nil {
				t.Errorf("%d: test end read error: %s", goldIndex, err)
				break
			}

			// validate
			if got := string(buf[:n]); got != want {
				t.Errorf("%d: got frame %#x", goldIndex, got)
				t.Errorf("%d: want frame %#x", goldIndex, want)
			}
		}

		wg.Wait()
	}
}

func TestConnInterface(t *testing.T) {
	if _, ok := interface{}(new(Conn)).(net.Conn); !ok {
		t.Error("Conn does not implement net.Conn")
	}
}

// PipeConn returns a connection with a test endpoint.
func pipeConn() (*Conn, net.Conn) {
	testConn, testEnd := net.Pipe()

	// timeout protection (against hanging tests)
	time.AfterFunc(time.Second, func() { testConn.Close() })

	return &Conn{Conn: testConn}, testEnd
}

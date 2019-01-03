package websocket

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

var GoldenWrites = []struct {
	Type    uint
	Message string
	Frame   string
}{
	0: {Text, "", "\x81\x00"},
	1: {Binary, "\a", "\x82\x01\a"},
	2: {Text, "hello", "\x81\x05hello"},
	3: {Text, strings.Repeat("!", 126), "\x81\x7e\x00\x7e" + strings.Repeat("!", 126)},
	4: {Binary, string(make([]byte, 1<<16)), "\x82\x7f\x00\x00\x00\x00\x00\x01\x00\x00" + string(make([]byte, 1<<16))},
}

var GoldenReads = []struct {
	Type    uint
	Message string
	Frame   string
}{
	0: {Text, "", "\x81\x80\x12\x34\x56\x78"},
	1: {Binary, "\a", "\x82\x81\x12\x34\x56\x78\x15"},
	2: {Text, "hello", "\x81\x85\x12\x34\x56\x78\x7a\x51\x3a\x14\x7d"},
	3: {Text, strings.Repeat("!", 126), "\x81\xfe\x00\x7e\x12\x34\x56\x78" + strings.Repeat("\x33\x15\x77\x59", 31) + "\x33\x15"},
	4: {Binary, string(make([]byte, 1<<16)), "\x82\xff\x00\x00\x00\x00\x00\x01\x00\x00\x12\x34\x56\x78" + strings.Repeat("\x12\x34\x56\x78", 1<<16/4)},
}

func TestWrite(t *testing.T) {
	for _, gold := range GoldenWrites {
		// create a connection with an endpoint
		testConn, testEnd := net.Pipe()
		// timeout protection (against hanging tests)
		time.AfterFunc(time.Second, func() { testConn.Close() })

		// collect from test end
		var got bytes.Buffer
		done := make(chan struct{})
		go func() {
			defer close(done)

			_, err := got.ReadFrom(iotest.OneByteReader(testEnd))
			if err != nil {
				t.Errorf("%#x: test receival error: %s", gold.Frame, err)
			}
		}()

		// sumbit message
		ws := Conn{Conn: testConn}
		ws.SetWriteMode(gold.Type, true)
		n, err := ws.Write([]byte(gold.Message))
		if err != nil {
			t.Errorf("%#x: connection write error: %s", gold.Frame, err)
		}
		if want := len(gold.Message); n != want {
			t.Errorf("%#x: connection wrote %d bytes, want %d", gold.Frame, n, want)
		}

		// close connection to EOF test end
		if err := ws.Close(); err != nil {
			t.Errorf("%#x: connection close error: %s", gold.Frame, err)
			continue
		}
		<-done

		if got.String() != gold.Frame {
			t.Errorf("%#x: got %#x", gold.Frame, got.String())
			t.Errorf("%#x: want %#x", gold.Frame, gold.Frame)
		}
	}
}

func TestRead(t *testing.T) {
	for _, gold := range GoldenReads {
		// create a connection with an endpoint
		testConn, testEnd := net.Pipe()
		// timeout protection (against hanging tests)
		time.AfterFunc(time.Second, func() { testConn.Close() })

		// submit from test end
		done := make(chan struct{})
		go func() {
			defer close(done)

			_, err := io.Copy(testEnd, iotest.OneByteReader(strings.NewReader(gold.Frame)))
			switch err {
			case nil:
				break
			case io.ErrClosedPipe:
				t.Errorf("%#x: test submission timeout", gold.Frame)
			default:
				t.Errorf("%#x: test submission error: %s", gold.Frame, err)
			}
		}()

		// collect message
		c := &Conn{Conn: testConn}
		var got []byte
		for readCount := 1; ; readCount++ {
			buf := make([]byte, 1024)
			n, err := c.Read(buf)
			if err != nil {
				t.Errorf("%#x: connection read %d got error: %s", gold.Frame, readCount, err)
				break
			}
			got = append(got, buf[:n]...)

			contentType, final := c.ReadMode()
			if contentType != gold.Type {
				t.Errorf("%#x: read %d got content type %d, want %d", gold.Frame, readCount, contentType, gold.Type)
			}
			if final {
				break
			}
		}

		if string(got) != gold.Message {
			t.Errorf("%#x: got %#x", gold.Frame, got)
			t.Errorf("%#x: want %#x", gold.Frame, gold.Message)
		}

		<-done
	}
}

func TestConnInterface(t *testing.T) {
	if _, ok := interface{}(new(Conn)).(net.Conn); !ok {
		t.Error("Conn does not implement net.Conn")
	}
}

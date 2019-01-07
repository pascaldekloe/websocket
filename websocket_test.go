package websocket

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestCloseErrorInterface(t *testing.T) {
	e, ok := interface{}(ClosedError(0)).(net.Error)
	if !ok {
		t.Error("ClosedError does not implement net.Error")
	}
	if e.Temporary() {
		t.Error("ClosedError is temporary.")
	}
	if e.Timeout() {
		t.Error("ClosedError is timeout.")
	}
	const want = "websocket: connection closed, status code 0000"
	if got := e.Error(); got != want {
		t.Errorf("got error %q, want %q", got, want)
	}
}

func TestReceiveCtrlInteruption(t *testing.T) {
	conn, testEnd := pipeConn()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		var buf bytes.Buffer
		buf.ReadFrom(testEnd)
		if got, want := buf.String(), "\x8a\x01."; got != want {
			t.Errorf("test end received %q, want %q", got, want)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		_, err := io.WriteString(testEnd,
			"\x01\x85\x00\x00\x00\x00Hello"+
				"\x89\x81\x00\x00\x00\x00."+
				"\x80\x86\x00\x00\x00\x00 World")
		if err != nil {
			t.Error("test end write error:", err)
		}
	}()

	var buf [100]byte
	opcode, n, err := conn.Receive(buf[:], time.Second, time.Second)
	if err != nil {
		t.Error("receive error:", err)
	}
	if opcode != Text {
		t.Errorf("got opcode %d, want %d", opcode, Text)
	}
	if got, want := string(buf[:n]), "Hello World"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	if err := conn.Close(); err != nil {
		t.Error("connection close error:", err)
	}
	wg.Wait()
}

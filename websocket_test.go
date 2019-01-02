package websocket

import (
	"net"
	"testing"
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

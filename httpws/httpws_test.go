package httpws

import (
	"net/http"
	"testing"
)

// deal with sloppy specification variations
var (
	ConnectionHeaders = [][]string{
		[]string{"Upgrade"},
		[]string{"uPGRaDe"},

		[]string{"a,upgrade"},
		[]string{"upgrade,a"},
		[]string{"a", "upgrade,b"},
		[]string{"upgrade,a", "b"},
		[]string{"a", "b,upgrade"},
		[]string{"a,upgrade", "b"},

		[]string{"a, upgrade"},
		[]string{"upgrade, a"},
		[]string{"a,\tupgrade"},
		[]string{"upgrade,\ta"},
		[]string{"a ,upgrade"},
		[]string{"upgrade ,a"},
		[]string{"a,\tupgrade"},
		[]string{"upgrade\t,a"},
		[]string{"a, \tupgrade \t,b"},
		[]string{"a,\t upgrade\t ,b"},
	}

	NotConnectionHeaders = [][]string{
		nil,
		[]string{"keep-alive, close"},
		[]string{"aupgrade, b"},
		[]string{"a, bupgrade"},
		[]string{"upgradeb, c"},
		[]string{"a, upgradec"},
	}

	UpgradeHeaders = [][]string{
		[]string{"websocket"},
		[]string{"websocket/13"},

		[]string{"a,websocket"},
		[]string{"websocket,a"},
		[]string{"a", "websocket,b"},
		[]string{"websocket,a", "b"},
		[]string{"a", "b,websocket"},
		[]string{"a,websocket", "b"},

		[]string{"a, websocket"},
		[]string{"websocket, a"},
		[]string{"a,\twebsocket"},
		[]string{"websocket,\ta"},
		[]string{"a ,websocket"},
		[]string{"websocket ,a"},
		[]string{"a,\twebsocket"},
		[]string{"websocket\t,a"},
		[]string{"a, \twebsocket \t,b"},
		[]string{"a,\t websocket\t ,b"},
	}
	NotUpgradeHeaders = [][]string{
		nil,
		[]string{"WebSocket"},
		[]string{"websocket/12"},
		[]string{"websocket/14"},
		[]string{"awebsocket, b"},
		[]string{"a, bwebsocket"},
	}
)

func TestIsUpgradeRequest(t *testing.T) {
	verify := func(connection, upgrade []string, want bool) {
		r := &http.Request{Header: make(http.Header, 2)}
		r.Header["Connection"] = connection
		r.Header["Upgrade"] = upgrade

		if want && !IsUpgradeRequest(r) {
			t.Errorf("didn't recognise Connection %q and Upgrade %q as a WebSocket upgrade", connection, upgrade)
		}
		if !want && IsUpgradeRequest(r) {
			t.Errorf("recognised Connection %q and Upgrade %q as a WebSocket upgrade", connection, upgrade)
		}
	}

	for _, connection := range ConnectionHeaders {
		for _, upgrade := range NotUpgradeHeaders {
			verify(connection, upgrade, false)
		}

		for _, upgrade := range UpgradeHeaders {
			verify(connection, upgrade, true)

			for _, connection := range NotConnectionHeaders {
				verify(connection, upgrade, false)
			}
		}
	}
}

func TestSubprotocols(t *testing.T) {
	r := &http.Request{Header: make(http.Header, 2)}
	if got := Subprotocols(r); len(got) != 0 {
		t.Errorf("got %q for empty request", got)
	}

	r.Header.Set("Sec-WebSocket-Protocol", "chat")
	if got := Subprotocols(r); len(got) != 1 || got[0] != "chat" {
		t.Errorf(`got %q for "chat"`, got)
	}

	r.Header.Add("Sec-WebSocket-Protocol", "chatv2, chatv3")
	if got := Subprotocols(r); len(got) != 3 || got[0] != "chat" || got[1] != "chatv2" || got[2] != "chatv3" {
		t.Errorf(`got %q for "chat" and "chatv2, chatv2"`, got)
	}
}

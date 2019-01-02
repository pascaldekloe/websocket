package httpws

import (
	"net/http"
	"strings"
)

// IsUpgradeRequest returns whether the client requested a WebSocket upgrade.
// See HTTP 426 Upgrade Required from “HTTP/1.1 Semantics and Content” RFC 7231.
func IsUpgradeRequest(r *http.Request) bool {
	return isConnectionUpgrade(r) && isUpgradeWebSocket(r)
}

func isConnectionUpgrade(r *http.Request) bool {
	// “Connection options are case-insensitive.”
	// — “HTTP/1.1 Message Syntax and Routing” RFC 7230, subsection 6.1
	header := headerList(r, "Connection")

	var offset int
	for i, c := range header {
		switch c {
		case ',', ' ', '\t':
			if strings.EqualFold(header[offset:i], "Upgrade") {
				return true
			}

			offset = i + 1
		}
	}

	return strings.EqualFold(header[offset:], "Upgrade")
}

func isUpgradeWebSocket(r *http.Request) bool {
	header := headerList(r, "Upgrade")

	var offset int
	for i, c := range header {
		switch c {
		case ',', ' ', '\t':
			switch header[offset:i] {
			case "websocket", "websocket/13":
				return true
			}

			offset = i + 1
		}
	}

	switch header[offset:] {
	case "websocket", "websocket/13":
		return true
	}
	return false
}

// Subprotocols returns the application-level options acceptable to the client.
// The server propagates the selection with the Sec-WebSocket-Protocol response
// header in the response.
func Subprotocols(r *http.Request) []string {
	header := headerList(r, "Sec-Websocket-Protocol")

	a := make([]string, 0, 1+strings.Count(header, ","))

	var offset int
	for i, c := range header {
		switch c {
		case ',', ' ', '\t':
			if i > offset {
				a = append(a, header[offset:i])
			}
			offset = i + 1
		}
	}

	if len(header) > offset {
		a = append(a, header[offset:])
	}

	return a
}

func headerList(r *http.Request, name string) string {
	// “Multiple message-header fields with the same field-name MAY be
	// present in a message if and only if the entire field-value for that
	// header field is defined as a comma-separated list [i.e., #(values)].
	// It MUST be possible to combine the multiple header fields into one
	// "field-name: field-value" pair, without changing the semantics of the
	// message, by appending each subsequent field-value to the first, each
	// separated by a comma.”
	// — “HTTP/1.1” RFC 2616, subsection 4.2
	return strings.Join(r.Header[name], ",")
}

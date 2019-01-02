package httpws

import (
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pascaldekloe/websocket"
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

var keyGUID = []byte("258EAFA5-E914-47DA-95CA-C5AB0DC85B11")

// ErrUpgrade means the HTTP request was rejected based on contstraints.
var ErrUpgrade = errors.New("websocket: HTTP request rejected")

// Upgrade the HTTP server connection to the WebSocket protocol. The request
// method must be GET.
//
// The responseHeader is included in the response to the client's upgrade
// request. Use the responseHeader to specify cookies (Set-Cookie) and the
// application negotiated subprotocol (Sec-WebSocket-Protocol).
func Upgrade(w http.ResponseWriter, r *http.Request, responseHeader http.Header, timeout time.Duration) (*websocket.Conn, error) {
	if !IsUpgradeRequest(r) {
		h := w.Header()
		h["Connection"] = []string{"Upgrade"}
		h["Upgrade"] = []string{"websocket"}
		http.Error(w, "This service requires use of the WebSocket protocol.", http.StatusUpgradeRequired)
		return nil, ErrUpgrade
	}

	if headerList(r, "Sec-Websocket-Version") != "13" {
		http.Error(w, "The Sec-WebSocket-Version header MUST be set to 13.", http.StatusBadRequest)
		return nil, ErrUpgrade
	}

	challengeKey := headerList(r, "Sec-Websocket-Key")
	if challengeKey == "" {
		http.Error(w, "The Sec-WebSocket-Key header MUST be set.", http.StatusBadRequest)
		return nil, ErrUpgrade
	}

	h, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "The server is incompatible with the WebSocket implementation.", http.StatusInternalServerError)
		return nil, errors.New("websocket: http.Hijacker not implemented")
	}
	conn, rw, err := h.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, err
	}

	if rw.Reader.Buffered() > 0 {
		conn.Close()
		return nil, errors.New("websocket: data before handshake")
	}

	conn.SetDeadline(time.Time{})
	conn.SetWriteDeadline(time.Now().Add(timeout))

	rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Accept: ")

	// challenge
	digest := sha1.New()
	digest.Write([]byte(challengeKey))
	digest.Write(keyGUID)
	var buf [28]byte
	base64.StdEncoding.Encode(buf[:], digest.Sum(buf[8:8]))
	rw.Write(buf[:])
	rw.WriteString("\r\n")

	if len(responseHeader) != 0 {
		if err = responseHeader.Write(rw); err != nil {
			conn.Close()
			return nil, err
		}
	}
	// terminate header & response (with double CRLF)
	rw.WriteString("\r\n")

	if err = rw.Flush(); err != nil {
		conn.Close()
		return nil, err
	}

	return &websocket.Conn{Conn: conn}, nil
}

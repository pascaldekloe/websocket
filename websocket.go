// Package websocket implements “The WebSocket Protocol” RFC 6455, version 13.
package websocket

// Opcode defines the interpretation of a frame payload.
const (
	// Continuation for streaming data.
	Continuation = iota
	// Text for UTF-8 encoded data.
	Text
	// Binary for opaque data.
	Binary
	// Reserved3 is reserved for further non-control frames.
	Reserved3
	// Reserved4 is reserved for further non-control frames.
	Reserved4
	// Reserved5 is reserved for further non-control frames.
	Reserved5
	// Reserved6 is reserved for further non-control frames.
	Reserved6
	// Reserved7 is reserved for further non-control frames.
	Reserved7
	// Close for disconnect notification.
	Close
	// Ping to request Pong.
	Ping
	// Pong may be send unsolicited too.
	Pong
	// Reserved11 is reserved for further control frames.
	Reserved11
	// Reserved12 is reserved for further control frames.
	Reserved12
	// Reserved13 is reserved for further control frames.
	Reserved13
	// Reserved14 is reserved for further control frames.
	Reserved14
	// Reserved15 is reserved for further control frames.
	Reserved15
)

// Defined Status Codes
const (
	// NormalClose means that the purpose for which the connection was
	// established has been fulfilled.
	NormalClose = 1000
	// GoingAway is a leave like a server going down or a browser moving on.
	GoingAway = 1001
	// ProtocolError rejects standard violation.
	ProtocolError = 1002
	// CannotAccept rejects a data type receival.
	CannotAccept = 1003
	// NoStatusCode is allowed by the protocol.
	NoStatusCode = 1005
	// AbnormalClose signals a disconnect without Close.
	AbnormalClose = 1006
	// Malformed rejects data that is not consistent with it's type, like an
	// illegal UTF-8 sequence for Text.
	Malformed = 1007
	// Policy rejects a message due to a violation.
	Policy = 1008
	// TooBig rejects a message due to size constraints.
	TooBig = 1009
	// WantExtension signals the client's demand for the server to negotiate
	// one or more extensions.
	WantExtension = 1010
	// Unexpected condition prevented the server from fulfilling the request.
	Unexpected = 1011
)

// ClosedError works around Go issue 4373.
type ClosedError uint

// Error honors the error interface.
func (e ClosedError) Error() string {
	msg := "websocket: connection closed"
	switch e {
	case NoStatusCode:
		break
	case AbnormalClose:
		msg += " abnormally"
	default:
		msg += ", status code "
		if e >= 10000 {
			msg += string('0' + (e/10000)%10)
		}
		msg += string('0'+(e/1000)%10) + string('0'+(e/100)%10) + string('0'+(e/10)%10) + string('0'+(e)%10)
	}

	return msg
}

// Timeout honors the net.Error interface.
func (e ClosedError) Timeout() bool { return false }

// Temporary honors the net.Error interface.
func (e ClosedError) Temporary() bool { return false }

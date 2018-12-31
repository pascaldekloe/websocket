package httpws

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Origin identifies the origin of a URI as a tripple.
// See “The Web Origin Concept” RFC 6454, subsection 4.
type Origin struct {
	// Scheme component of the URI, converted to lowercase, or a GUID if the
	// implementation doesn't support the protocol given by the URI scheme.
	Scheme string
	// Host component of the URI, converted to lowercase.
	Host string
	// Port component of the URI, or the default for the protocol given by
	// the URI scheme.
	Port int
}

// ABNF: "null" / scheme "://" host [ ":" port ]
func parseOrigin(s string) (o *Origin, ok bool) {
	if s == "null" {
		return nil, true
	}
	o = new(Origin)

	i := strings.Index(s, "://")
	if i <= 0 {
		return nil, false
	}
	o.Scheme = s[:i]

	authority := s[i+3:]
	i = strings.LastIndexByte(authority, ':')
	if i >= 0 && authority[len(authority)-1] != ']' /* IPv6 */ {
		o.Host = authority[:i]
		port, err := strconv.Atoi(authority[i+1:])
		if err != nil {
			return nil, false
		}
		o.Port = port
	} else {
		o.Host = authority
		o.Port, _ = net.LookupPort("tcp", o.Scheme)
	}
	if o.Host == "" {
		return nil, false
	}

	return o, true
}

// AllowOrigin parses all entries from the Origin header and calls check until
// the first pass. The return is false for malformed content. No content defaults
// to passNone.
// Note that the origin argument for check can be ("null", nil), conform “The Web
// Origin Concept” RFC 6454, subsection 6.
func AllowOrigin(r *http.Request, check func(serial string, o *Origin) (pass bool), passNone bool) bool {
	var header string
	switch a := r.Header["Origin"]; len(a) {
	case 0:
		return passNone
	case 1:
		header = a[0]
	default:
		// subsection 7.3, prohibits multiple headers
		return false
	}
	if header == "" {
		return passNone
	}
	// no leading nor trailing space; one or more entries

	var allow bool
	end := len(header)
	for i := end - 2; i > 0; i-- {
		if header[i] != ' ' {
			continue
		}
		s := header[i+1 : end]

		origin, ok := parseOrigin(s)
		if !ok {
			return false
		}
		if !allow && check(s, origin) {
			allow = true
		}

		end = i
	}
	s := header[:end]

	origin, ok := parseOrigin(s)
	if !ok {
		return false
	}
	return allow || check(s, origin)
}

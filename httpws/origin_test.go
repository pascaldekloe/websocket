package httpws

import (
	"net/http"
	"testing"
)

var GoldenOrigins = []struct {
	Header string
	Want   []*Origin
}{
	{"http://example.com", []*Origin{{"http", "example.com", 80}}},
	{"http://example.com:8080", []*Origin{{"http", "example.com", 8080}}},
	{"https://127.0.0.1 https://127.0.0.1:8443", []*Origin{
		{"https", "127.0.0.1", 8443},
		{"https", "127.0.0.1", 443},
	}},
	{"http://[::1] http://[::1]:8080", []*Origin{
		{"http", "[::1]", 8080},
		{"http", "[::1]", 80},
	}},
	{"null", []*Origin{nil}},
	{"file:/home", nil},
	{"https://", nil},
	{"https://:80", nil},
}

func TestAllowOriginCheck(t *testing.T) {
	for _, gold := range GoldenOrigins {
		r := new(http.Request)
		r.Header = make(http.Header)
		r.Header.Set("Origin", gold.Header)

		var calls int
		AllowOrigin(r, func(serial string, o *Origin) (pass bool) {
			calls++
			if calls > len(gold.Want) {
				t.Errorf("%q unwanted call %d with %v", gold.Header, calls, o)
				return
			}
			want := gold.Want[calls-1]
			if want == nil && o != nil || want != nil && o == nil || want != nil && *o != *want {
				t.Errorf("%q call %d with %v, want %v", gold.Header, calls, o, want)
			}
			return false
		}, false)

		if calls < len(gold.Want) {
			for _, o := range gold.Want[calls:] {
				t.Errorf("%q missed call %v", gold.Header, o)
			}
		}
	}
}

func TestAllowOrigin(t *testing.T) {
	var allowed = []string{
		"http://example.com",
		"http://example.com:8080",
		"http://example.net https://example.com",
		"http://example.com https://example.net",
	}
	for _, header := range allowed {
		r := new(http.Request)
		r.Header = make(http.Header)
		r.Header.Set("Origin", header)

		got := AllowOrigin(r, func(serial string, o *Origin) (pass bool) {
			return o != nil && o.Host == "example.com"
		}, false)
		if !got {
			t.Errorf("disallowed %q", header)
		}
	}

	var disallowed = []string{
		"http://example.net",
		"http://example.net:8080",
		"http://example.net https://example.org",
		"null",
		"broken example.com",
		"example.com broken",
	}
	for _, header := range disallowed {
		r := new(http.Request)
		r.Header = make(http.Header)
		r.Header.Set("Origin", header)

		got := AllowOrigin(r, func(serial string, o *Origin) (pass bool) {
			return o != nil && o.Host == "example.com"
		}, true)
		if got {
			t.Errorf("allowed %q", header)
		}
	}
}

## About

A high-performance WebSocket library for the Go programming language
with built-in retry logic and timeout protection.

This is free and unencumbered software released into the
[public domain](http://creativecommons.org/publicdomain/zero/1.0).

[![API](https://pkg.go.dev/badge/github.com/pascaldekloe/websocket.svg)](https://pkg.go.dev/github.com/pascaldekloe/websocket)
[![CI](https://github.com/pascaldekloe/websocket/actions/workflows/go.yml/badge.svg)](https://github.com/pascaldekloe/websocket/actions/workflows/go.yml)


## Use

Most implementations will boot from an
[HTTP Upgrade](https://godoc.org/github.com/pascaldekloe/websocket/httpws#Upgrade).

```go
http.HandleFunc("/echo", func(resp http.ResponseWriter, req *http.Request) {
	// client safety support
	originCheck := func(s string, o *httpws.Origin) (pass bool) {
		return o != nil && o.Host == "example.com" && o.Scheme == "https"
	}
	if !httpws.AllowOrigin(req, originCheck) {
		http.Error(w, "HTTP Origin rejected", http.StatusForbidden)
		return
	}

	// switch protocol to WebSocket
	conn, err := httpws.Upgrade(resp, req, nil, time.Second)
	if err != nil {
		return
	}
	defer conn.Close()

	// limit to standard types
	conn.Accept = websocket.AcceptV13

	var buf [2048]byte
	for {
		// read message
		opcode, n, err := conn.Receive(buf[:], time.Second, time.Minute)
		if err != nil {
			if _, ok := err.(websocket.ClosedError); !ok && err != io.EOF {
				log.Print("receive error: ", err)
			}
			return
		}

		// write message
		err = conn.Send(opcode, buf[:n], time.Second)
		if err != nil {
			if _, ok := err.(websocket.ClosedError); !ok {
				log.Print("send error: ", err)
			}
			return
		}
	}
})
```


## Performance

The `/tcp` variants wire the raw messages to display WebSocket protocol overhead.

```
name               time/op
Receive/buffer-12   6.73µs ± 5%
Receive/stream-12   45.7µs ± 3%
Receive/tcp-12      6.28µs ± 3%
Send/buffer-12      10.0µs ± 1%
Send/stream-12      23.0µs ± 1%
Send/tcp-12         10.0µs ± 1%

name               speed
Receive/buffer-12  888MB/s ± 5%
Receive/stream-12  131MB/s ± 3%
Receive/tcp-12     951MB/s ± 3%
Send/buffer-12     598MB/s ± 1%
Send/stream-12     260MB/s ± 1%
Send/tcp-12        595MB/s ± 1%

name               alloc/op
Receive/buffer-12    0.00B     
Receive/stream-12    34.0B ± 0%
Receive/tcp-12       0.00B     
Send/buffer-12       0.00B     
Send/stream-12       32.0B ± 0%
Send/tcp-12          0.00B     

name               allocs/op
Receive/buffer-12     0.00     
Receive/stream-12     0.00     
Receive/tcp-12        0.00     
Send/buffer-12        0.00     
Send/stream-12        1.00 ± 0%
Send/tcp-12           0.00     
```

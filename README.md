[![API Documentation](https://godoc.org/github.com/pascaldekloe/websocket?status.svg)](https://godoc.org/github.com/pascaldekloe/websocket)
[![Build Status](https://travis-ci.org/pascaldekloe/websocket.svg?branch=master)](https://travis-ci.org/pascaldekloe/websocket)

A WebSocket library for the Go programming language.

### Performance on a Mac Pro (late 2013)

```
name               time/op
Receive/buffer-12  6.51µs ± 5%
Receive/stream-12  44.6µs ± 2%
Receive/read-12    6.04µs ± 4%
Send/buffer-12     12.1µs ± 1%
Send/stream-12     23.0µs ± 1%
Send/write-12      12.0µs ± 1%
```

This is free and unencumbered software released into the
[public domain](http://creativecommons.org/publicdomain/zero/1.0).

[![API Documentation](https://godoc.org/github.com/pascaldekloe/websocket?status.svg)](https://godoc.org/github.com/pascaldekloe/websocket)
[![Build Status](https://travis-ci.org/pascaldekloe/websocket.svg?branch=master)](https://travis-ci.org/pascaldekloe/websocket)

A WebSocket library for the Go programming language.

This is free and unencumbered software released into the
[public domain](http://creativecommons.org/publicdomain/zero/1.0).


### Performance on a Mac Pro (late 2013)

The `/tcp` variants wire the raw messages to display WebSocket protocol overhead.

```
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

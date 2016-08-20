## tftp2http

So it's 2016 and I decided to teach myself Go, but I wanted to write something that could actually prove useful.  TFTP is still used for a variety of reasons, and I've occasionally wished I could skip TFTP and use an HTTP server instead.  Thus, tftp2http...

This daemon transforms TFTP requests to HTTP requests on the fly.  TFTP RRQs are turned in to HTTP GETs, and WRQs become HTTP PUTs.  It uses the excellent [Go TFTP library](https://github.com/pin/tftp) developed by [Dmitri Popov](https://github.com/pin).  

### Building

Building the daemon is currently an exercise left to the reader; I've not done a release and provided binaries.  Let me know if you'd like me to.

### Can I see it work?

Let's proxy all requests to Google!

```
$ tftp2http -listen=:10000 http://www.google.com/
2016/08/19 23:35:37 proxying TFTP requests on :10000 to http://www.google.com/
```

Now, in another shell, let's make a request:

```
$ tftp 127.0.0.1 10000
tftp> bin
tftp> get index.html
Received 11158 bytes in 0.0 seconds
```

And back on the proxy we see:

```
2016/08/19 23:41:39 {127.0.0.1:59102} received RRQ 'index.html'
2016/08/19 23:41:39 {127.0.0.1:59102} completed RRQ 'index.html' bytes:11158,duration:80.241477ms
```

### TFTP details

The TFTP server supports the `tsize` and `blksize` options.  The RRQ `tsize` option will not be acknowledged if the HTTP GET response lacks the Content-Length header.

### HTTP details

HTTP requests include headers to convey the original request source.  They are:

* X-Forwarded-For: source IP address and port
* X-Forwarded-Proto: tftp
* Forwarded: per [RFC 7239](https://tools.ietf.org/html/rfc7239)

HTTP PUTs use chunked encoding to transmit the request body as it is received from the TFTP client.  Content-Length is not provided even if the client included it as part of its WRQ options.

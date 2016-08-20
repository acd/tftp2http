package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
	"log"

	"github.com/pin/tftp"
)

var (
	listenAddr  = flag.String("listen", ":69", "Listen address and port")
	httpTimeout = flag.Int("http-timeout", 5, "HTTP timeout")
	httpMaxIdle = flag.Int("http-max-idle", 10, "HTTP max idle connections")
	tftpTimeout = flag.Float64("tftp-timeout", 1, "TFTP timeout")
	tftpRetries = flag.Int("tftp-retries", 5, "TFTP retries")
	serverUrl   *url.URL
)

var client *http.Client

func processFlags() error {
	flag.Parse()

	var err error
	if len(flag.Args()) != 1 {
		return fmt.Errorf("must provide server")
	}
	server := flag.Args()[0]
	serverUrl, err = url.Parse(server)
	if err != nil {
		return fmt.Errorf("url: error parsing: '%s'", server)
	}
	if serverUrl.Scheme != "http" && serverUrl.Scheme != "https" {
		return fmt.Errorf("url: invalid scheme: '%s'", serverUrl.Scheme)
	}
	if len(serverUrl.Host) == 0 {
		return fmt.Errorf("url: host must be provided")
	}

	return nil
}

func setForwardedHeader(req *http.Request, from string) {
	req.Header.Add("X-Forwarded-From", from)
	req.Header.Add("X-Forwarded-Proto", "tftp")
	req.Header.Add("Forwarded", "for=\""+from+"\",proto=tftp")
}

func readHandler(filename string, rf io.ReaderFrom) error {
	raddr := rf.(tftp.OutgoingTransfer).RemoteAddr()
	from := raddr.String()

	log.Printf("{%s} received RRQ '%s'", from, filename)

	u := *serverUrl
	u.Path += filename

	start := time.Now()
	var n int64
	defer func() {
		elapsed := time.Since(start)
		log.Printf("{%s} completed RRQ '%s' bytes:%d,duration:%s", from, filename, n, elapsed)
	}()

	req, err := http.NewRequest("GET", u.String(), nil)
	setForwardedHeader(req, from)

	res, err := client.Do(req)
	if err != nil {
		log.Printf("{%s} error on HTTP GET: %s", from, err)
		return err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case 200:
		break
	case 404:
		return fmt.Errorf("File not found")
	default:
		log.Printf("{%s} unexpected response status code is: %d", from, res.StatusCode)
		return fmt.Errorf("Unexpected response code: %d", res.StatusCode)
	}

	if res.ContentLength >= 0 {
		rf.(tftp.OutgoingTransfer).SetSize(res.ContentLength)
	}

	n, err = rf.ReadFrom(res.Body)
	if err != nil {
		log.Printf("{%s} ReadFrom returned error: %s", from, err)
		return err
	}

	return nil
}

func writeHandler(filename string, wt io.WriterTo) error {
	raddr := wt.(tftp.IncomingTransfer).RemoteAddr()
	from := raddr.String()

	log.Printf("{%s} received WRQ '%s'", filename, from)

	u := *serverUrl
	u.Path += filename

	start := time.Now()
	var n int64
	defer func() {
		elapsed := time.Since(start)
		log.Printf("{%s} completed WRQ '%s' bytes:%d,duration:%s", from, filename, n, elapsed)
	}()

	r, w := io.Pipe()
	done := make(chan int64)
	go func() {
		defer w.Close()
		n, _ := wt.WriteTo(w)
		done <- n
	}()

	req, err := http.NewRequest("PUT", u.String(), r)
	setForwardedHeader(req, from)
	req.Header.Add("Content-Type", "application/octet-stream")
	req.ContentLength = -1

	res, err := client.Do(req)
	if err != nil {
		log.Printf("error on HTTP PUT: %s", err)
		return err
	}
	defer res.Body.Close()

	n = <-done
	return nil
}

func main() {
	if err := processFlags(); err != nil {
		fmt.Println(err)
		flag.PrintDefaults()
		os.Exit(1)
	}

	client = &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: *httpMaxIdle,
		},
		Timeout: time.Duration(*httpTimeout) * time.Second,
	}

	s := tftp.NewServer(readHandler, writeHandler)
	s.SetTimeout(time.Duration(*tftpTimeout) * time.Second)
	s.SetRetries(*tftpRetries)
	// s.SetBackoff(func (int) time.Duration { return 0 })

	log.Printf("proxying TFTP requests on %s to %s", *listenAddr, serverUrl)
	err := s.ListenAndServe(*listenAddr)
	if err != nil {
		log.Fatalf("server: %v\n", err)
	}
}

package main

import (
	"flag"
	"errors"
	"fmt"
	"gopkg.in/yaml.v2"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"github.com/pin/tftp"
)

var (
	map_file    = flag.String("map-file", "", "YAML file which maps TFTP path regular-expressions into HTTP urls")
	listenAddr  = flag.String("listen", ":69", "Listen address and port")
	httpTimeout = flag.Int("http-timeout", 5, "HTTP timeout")
	httpMaxIdle = flag.Int("http-max-idle", 10, "HTTP max idle connections")
	tftpTimeout = flag.Float64("tftp-timeout", 1, "TFTP timeout")
	tftpRetries = flag.Int("tftp-retries", 5, "TFTP retries")
)

type RedirectItem struct {
	tftp_regexp	regexp.Regexp
	http_url	url.URL
}
var redirect_map = make(map[string]RedirectItem)
var client *http.Client

func processFlags() error {
	flag.Parse()

	var path_map = make(map[string]string)
	if *map_file != "" {
		cfg_yaml, err := ioutil.ReadFile(*map_file)
		err = yaml.Unmarshal(cfg_yaml, &path_map)
		if err != nil {
			return err
		}
	}
	if len(flag.Args()) == 1 {
		server := flag.Args()[0]
		path_map["/"] = server
	}
	if len(path_map) == 0 {
		return fmt.Errorf("No servers defined")
	}
	for k, v := range path_map {
		log.Printf("\t%s\t%s", k, v)
		http_url, err := url.Parse(v)
		if err != nil {
			return fmt.Errorf("url: error parsing: '%s'", v)
		}
		if http_url.Scheme != "http" && http_url.Scheme != "https" {
			return fmt.Errorf("url: invalid scheme: '%s'", http_url.Scheme)
		}
		if len(http_url.Host) == 0 {
			return fmt.Errorf("url: host must be provided")
		}
		regexp_path, err := regexp.Compile(k)
		if err != nil {
			return fmt.Errorf("url: illegal regexp '" + k + "'")
		}
		redirect_map[k] = RedirectItem{
			tftp_regexp: *regexp_path,
			http_url: *http_url,
			}
	}
	return nil
}

func map_tftp_path(tftp_path string) (url.URL, error) {
	for k, redirect_item := range redirect_map {
		path_regexp := redirect_item.tftp_regexp
		if ! path_regexp.MatchString(tftp_path) {
			continue
		}
		new_url := redirect_item.http_url
		result_path := path_regexp.ReplaceAllString(tftp_path, new_url.Path)
		log.Printf("map_tftp_path: Matched '%s' (result_path '%s')", k, result_path)
		new_url.Path = result_path
		log.Printf("map_tftp_path: translate '%s' -> '%s'", tftp_path, new_url.String())
		return new_url, nil
	}
	log.Printf("map_tftp_path: did not find '%s'", tftp_path)
	return *new(url.URL), errors.New("Cannot map")
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
	if ! strings.HasPrefix(filename, "/") {
		filename = "/" + filename
	}
	http_url, err := map_tftp_path(filename)
	if err != nil {
		log.Printf("Cannot map '%s'", filename)
		return err
	}
	log.Printf("Translate to '%s'", http_url.String())

	start := time.Now()
	var n int64
	defer func() {
		elapsed := time.Since(start)
		log.Printf("{%s} completed RRQ '%s' bytes:%d,duration:%s", from, filename, n, elapsed)
	}()

	req, err := http.NewRequest("GET", http_url.String(), nil)
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
	if ! strings.HasPrefix(filename, "/") {
		filename = "/" + filename
	}
	http_url, err := map_tftp_path(filename)
	if err != nil {
		log.Printf("Cannot map '%s'", filename)
		return err
	}
	log.Printf("Translate to '%s'", http_url.String())

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

	req, err := http.NewRequest("PUT", http_url.String(), r)
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

	log.Printf("proxying TFTP requests on %s", *listenAddr)
	err := s.ListenAndServe(*listenAddr)
	if err != nil {
		log.Fatalf("server: %v\n", err)
	}
}

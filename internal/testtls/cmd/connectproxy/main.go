// Command connectproxy is a test-only HTTP CONNECT proxy for the docker tests of
// gmx. It forwards every CONNECT to the host and port asked for (names resolve on
// the docker networks the proxy container is attached to) and logs one line per
// CONNECT to stdout: "CONNECT <host:port> from <ip>". It refuses everything else.
//
//	connectproxy -addr :3128
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := flag.String("addr", ":3128", "listen address")

	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Fprintf(os.Stdout, "listening %s\n", ln.Addr())

	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}

		go handle(c)
	}
}

func handle(c net.Conn) {
	defer c.Close()

	br := bufio.NewReader(c)

	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}

	client, _, _ := net.SplitHostPort(c.RemoteAddr().String())

	if req.Method != http.MethodConnect {
		fmt.Fprintf(os.Stdout, "REFUSED %s %s from %s\n", req.Method, req.Host, client)

		_, _ = io.WriteString(c, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")

		return
	}

	fmt.Fprintf(os.Stdout, "CONNECT %s from %s\n", req.Host, client)

	up, err := net.DialTimeout("tcp", req.Host, 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stdout, "DIAL-FAILED %s: %v\n", req.Host, err)

		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")

		return
	}
	defer up.Close()

	_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")

	done := make(chan struct{}, 2)

	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()

	<-done
}

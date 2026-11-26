// The loopback bridge of local cells: forwards TCP from the pod's loopback
// to the cell ingress.
package main

import (
	"io"
	"log"
	"net"
	"os"
	"sync"
)

func main() {
	target := os.Getenv("BRIDGE_TARGET")
	if target == "" {
		target = "traefik.kube-system.svc.cluster.local:443"
	}
	bridge(target, "127.0.0.1:443", "[::1]:443")
}

// bridge forwards TCP from the pod's loopback to the cell ingress.
//
// Local cells use *.localhost, and libcurl (PHP, curl, ...) resolves every
// *.localhost name to loopback without asking DNS. Inside a pod that would
// miss the ingress; the bridge sends it there. Only local cells need this;
// cells with a real domain resolve through cluster DNS.
func bridge(target string, listen ...string) {
	var wg sync.WaitGroup
	for _, addr := range listen {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Printf("bridge: skip %s: %v", addr, err) // e.g. no IPv6 loopback in this pod
			continue
		}
		log.Printf("bridge: %s -> %s", addr, target)
		wg.Add(1)
		go func() {
			defer wg.Done()
			serve(ln, target)
		}()
	}
	wg.Wait()
	log.Fatal("bridge: no listener")
}

func serve(ln net.Listener, target string) {
	for {
		in, err := ln.Accept()
		if err != nil {
			log.Printf("bridge: accept: %v", err)
			return
		}
		go func() {
			defer func() { _ = in.Close() }()
			out, err := net.Dial("tcp", target)
			if err != nil {
				log.Printf("bridge: dial %s: %v", target, err)
				return
			}
			defer func() {
				if err := out.Close(); err != nil {
					log.Printf("bridge: close %s: %v", target, err)
				}
			}()
			go func() {
				_, _ = io.Copy(out, in)
				if c, ok := out.(*net.TCPConn); ok {
					_ = c.CloseWrite()
				}
			}()
			_, _ = io.Copy(in, out)
		}()
	}
}

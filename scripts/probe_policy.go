//go:build devtools
// +build devtools

package main

import (
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

func main() {
	addr := "127.0.0.1:5000"
	fmt.Println("probe policy on", addr)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		fmt.Println("dial failed:", err)
		return
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = conn.Write([]byte("<policy-file-request/>\x00"))

	buf, err := io.ReadAll(conn)
	if err != nil && !strings.Contains(err.Error(), "i/o timeout") {
		fmt.Println("read failed:", err)
		return
	}

	s := string(buf)
	fmt.Println("recv bytes:", len(buf))
	if strings.Contains(s, "<cross-domain-policy>") {
		fmt.Println("OK: got cross-domain-policy")
	} else {
		fmt.Println("NOT OK: no cross-domain-policy, first 120 chars:")
		if len(s) > 120 {
			fmt.Println(s[:120])
		} else {
			fmt.Println(s)
		}
	}
}


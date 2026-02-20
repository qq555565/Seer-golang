//go:build devtools
// +build devtools

package main

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

func main() {
	urls := []string{
		"http://127.0.0.1:32400/ip.txt",
		"http://127.0.0.1:32400/resource/ui.swf",
		"http://127.0.0.1:32400/resource/map/1.swf",
		"http://127.0.0.1:32400/resource/map/515.swf",
	}

	c := &http.Client{Timeout: 3 * time.Second}
	for _, u := range urls {
		resp, err := c.Get(u)
		if err != nil {
			fmt.Println("GET", u, "ERR:", err)
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		_ = resp.Body.Close()
		fmt.Println("GET", u, "status", resp.StatusCode, "len(first64)", len(b))
	}
}


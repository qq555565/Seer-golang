package gameserver

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestProcessBuffer_FlashPolicyRequest(t *testing.T) {
	c, s := net.Pipe()
	defer c.Close()
	defer s.Close()

	// 这里只测试策略请求识别与响应，不依赖完整初始化
	gs := &GameServer{}

	cd := &ClientData{
		Socket: s,
		Buffer: []byte("<policy-file-request/>\x00"),
	}

	done := make(chan string, 1)
	go func() {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 2048)
		n, err := c.Read(buf)
		if err != nil {
			done <- ""
			return
		}
		done <- string(buf[:n])
	}()

	gs.processBuffer(cd)

	got := <-done
	if got == "" {
		t.Fatalf("未收到策略文件响应")
	}
	if !strings.Contains(got, "<cross-domain-policy>") {
		t.Fatalf("响应不像策略文件: %q", got)
	}
	if !strings.HasSuffix(got, "\x00") {
		t.Fatalf("策略文件应以 \\x00 结尾")
	}
	if len(cd.Buffer) != 0 {
		t.Fatalf("策略请求应被消费，剩余 buffer=%d", len(cd.Buffer))
	}
}


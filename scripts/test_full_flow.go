//go:build devtools
// +build devtools

// 完整流程测试：需先在本机运行 gameserver，再执行:
//   go run ./scripts/test_full_flow.go
// 或: cd golang_version && go run ./scripts/test_full_flow.go
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func main() {
	fmt.Println("===== 赛尔号 Go 服务端完整流程测试 =====")

	// 1) 取 ip.txt，应得到 127.0.0.1:1863
	fmt.Println("\n[1] GET http://127.0.0.1:32400/ip.txt ...")
	resp, err := http.Get("http://127.0.0.1:32400/ip.txt")
	if err != nil {
		fmt.Printf("   失败: %v（请先启动 gameserver）\n", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	addr := strings.TrimSpace(string(body))
	fmt.Printf("   得到: %q\n", addr)
	if addr != "127.0.0.1:1863" {
		fmt.Printf("   预期 127.0.0.1:1863，请检查资源服 ip.txt 配置\n")
		return
	}
	fmt.Println("   ✓ ip.txt 正确，客户端将连此地址发 104/105")

	// 2) 连登录服，发 CMD 105，校验返回体里含 127.0.0.1:5000
	fmt.Println("\n[2] TCP 连接 ", addr, " 发送 CMD 105 ...")
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		fmt.Printf("   连接失败: %v\n", err)
		return
	}
	defer conn.Close()

	header := make([]byte, 17)
	binary.BigEndian.PutUint32(header[0:4], 17)
	header[4] = 0x31
	binary.BigEndian.PutUint32(header[5:9], 105)
	binary.BigEndian.PutUint32(header[9:13], 100000001)
	binary.BigEndian.PutUint32(header[13:17], 0)
	_, _ = conn.Write(header)

	respHead := make([]byte, 17)
	n, err := io.ReadFull(conn, respHead)
	if err != nil || n < 17 {
		fmt.Printf("   读响应头失败: n=%d err=%v\n", n, err)
		return
	}
	respLen := binary.BigEndian.Uint32(respHead[0:4])
	if respLen < 17 {
		fmt.Printf("   响应长度异常: %d\n", respLen)
		return
	}
	left := int(respLen) - 17
	buf := make([]byte, left)
	got, _ := io.ReadFull(conn, buf)
	buf = buf[:got]

	if len(buf) < 12+30 {
		fmt.Printf("   CMD 105 体过短: %d\n", len(buf))
		return
	}
	off := 12
	ipBuf := buf[off+8 : off+24]
	port := binary.BigEndian.Uint16(buf[off+24 : off+26])
	ipStr := string(trimNull(ipBuf))
	fmt.Printf("   服务器列表首条: %s:%d\n", ipStr, port)
	if ipStr != "127.0.0.1" || port != 5000 {
		fmt.Printf("   预期 127.0.0.1:5000（频道服务器）\n")
		return
	}
	fmt.Println("   ✓ CMD 105 返回正确，客户端可连 127.0.0.1:5000 进入频道")

	fmt.Println("\n===== 完整流程测试通过 =====")
}

func trimNull(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}

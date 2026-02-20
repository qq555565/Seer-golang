//go:build devtools
// +build devtools

// 测试 CMD 80001 NIEO_LOGIN（超能NONO登录/状态检查）
// 需先运行 gameserver，再执行: go run ./scripts/test_cmd80001.go
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
	fmt.Println("===== 测试 CMD 80001 NIEO_LOGIN =====")

	// 1) 取 ip.txt
	fmt.Println("\n[1] GET http://127.0.0.1:32400/ip.txt ...")
	resp, err := http.Get("http://127.0.0.1:32400/ip.txt")
	if err != nil {
		fmt.Printf("   ❌ 失败: %v（请先启动 gameserver）\n", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	addr := strings.TrimSpace(string(body))
	fmt.Printf("   ✓ 得到: %q\n", addr)

	// 2) CMD 104 登录
	fmt.Println("\n[2] CMD 104 邮箱登录...")
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		fmt.Printf("   ❌ 连接失败: %v\n", err)
		return
	}
	defer conn.Close()

	email := "test@local.seer"
	passwordMD5 := []byte{0xcc, 0x03, 0xe7, 0x47, 0xa6, 0xaf, 0xbb, 0xcb, 0xf8, 0xbe, 0x76, 0x68, 0xac, 0xfe, 0xbe, 0xe5}
	passwordMD5Str := fmt.Sprintf("%x", passwordMD5)

	body104 := make([]byte, 96)
	copy(body104[0:64], []byte(email))
	copy(body104[64:96], []byte(passwordMD5Str))

	header104 := make([]byte, 17)
	binary.BigEndian.PutUint32(header104[0:4], uint32(17+len(body104)))
	header104[4] = 0x31
	binary.BigEndian.PutUint32(header104[5:9], 104)
	binary.BigEndian.PutUint32(header104[9:13], 0)
	binary.BigEndian.PutUint32(header104[13:17], 0)

	packet104 := append(header104, body104...)
	conn.Write(packet104)

	respHead104 := make([]byte, 17)
	io.ReadFull(conn, respHead104)
	respLen104 := binary.BigEndian.Uint32(respHead104[0:4])
	respUID104 := binary.BigEndian.Uint32(respHead104[9:13])
	respError104 := binary.BigEndian.Uint32(respHead104[13:17])

	if respError104 != 0 {
		fmt.Printf("   ❌ 登录失败，错误码: %d\n", respError104)
		return
	}

	bodyLen104 := int(respLen104) - 17
	respBody104 := make([]byte, bodyLen104)
	io.ReadFull(conn, respBody104)

	session := respBody104[0:16]
	fmt.Printf("   ✓ 登录成功！米米号: %d\n", respUID104)

	// 3) CMD 1001 登录游戏服
	fmt.Println("\n[3] CMD 1001 登录游戏服...")
	conn1001, err := net.DialTimeout("tcp", "127.0.0.1:5000", 3*time.Second)
	if err != nil {
		fmt.Printf("   ❌ 连接失败: %v\n", err)
		return
	}
	defer conn1001.Close()

	body1001 := make([]byte, 16)
	copy(body1001[0:16], session)

	header1001 := make([]byte, 17)
	binary.BigEndian.PutUint32(header1001[0:4], uint32(17+len(body1001)))
	header1001[4] = 0x31
	binary.BigEndian.PutUint32(header1001[5:9], 1001)
	binary.BigEndian.PutUint32(header1001[9:13], uint32(respUID104))
	binary.BigEndian.PutUint32(header1001[13:17], 1)

	packet1001 := append(header1001, body1001...)
	conn1001.Write(packet1001)

	// 读取登录响应和推送包（CMD 1001, 80002, 80001）
	conn1001.SetReadDeadline(time.Now().Add(2 * time.Second))
	for i := 0; i < 5; i++ {
		respHead := make([]byte, 17)
		n, err := io.ReadFull(conn1001, respHead)
		if err != nil || n < 17 {
			break
		}
		respLen := binary.BigEndian.Uint32(respHead[0:4])
		respCmd := binary.BigEndian.Uint32(respHead[5:9])
		bodyLen := int(respLen) - 17
		if bodyLen > 0 {
			buf := make([]byte, bodyLen)
			io.ReadFull(conn1001, buf)
		}
		if respCmd == 1001 || respCmd == 80002 {
			// 已读取登录响应和推送包
		}
	}
	conn1001.SetReadDeadline(time.Time{})
	fmt.Println("   ✓ 游戏服登录成功，已读取推送包")

	// 4) CMD 80001 NIEO_LOGIN（超能NONO登录/状态检查）
	fmt.Println("\n[4] CMD 80001 NIEO_LOGIN（超能NONO状态检查）...")

	header80001 := make([]byte, 17)
	binary.BigEndian.PutUint32(header80001[0:4], 17) // 无包体
	header80001[4] = 0x31
	binary.BigEndian.PutUint32(header80001[5:9], 80001)
	binary.BigEndian.PutUint32(header80001[9:13], uint32(respUID104))
	binary.BigEndian.PutUint32(header80001[13:17], 0)

	conn1001.Write(header80001)
	fmt.Println("   ✓ 已发送 CMD 80001 请求")

	// 读取响应
	respHead80001 := make([]byte, 17)
	io.ReadFull(conn1001, respHead80001)
	respLen80001 := binary.BigEndian.Uint32(respHead80001[0:4])
	respCmd80001 := binary.BigEndian.Uint32(respHead80001[5:9])
	respSeq80001 := binary.BigEndian.Uint32(respHead80001[13:17])

	fmt.Printf("   ✓ 收到响应: CMD=%d SEQ=%d LEN=%d\n", respCmd80001, respSeq80001, respLen80001)

	if respCmd80001 != 80001 {
		fmt.Printf("   ⚠️  响应命令ID不匹配，期望 80001，得到 %d\n", respCmd80001)
		return
	}

	bodyLen80001 := int(respLen80001) - 17
	if bodyLen80001 >= 4 {
		respBody80001 := make([]byte, bodyLen80001)
		io.ReadFull(conn1001, respBody80001)
		status := binary.BigEndian.Uint32(respBody80001[0:4])
		fmt.Printf("   ✓ 超能NONO状态: %d (0=正常/已激活)\n", status)
		if status == 0 {
			fmt.Println("   ✓ NIEO_LOGIN 测试成功！")
		} else {
			fmt.Printf("   ⚠️  状态异常: %d\n", status)
		}
	} else {
		fmt.Printf("   ⚠️  响应体长度异常: %d（期望至少4字节）\n", bodyLen80001)
	}

	fmt.Println("\n===== CMD 80001 测试完成 =====")
}

//go:build devtools
// +build devtools

// 测试 CMD 104 邮箱登录：需先运行 gameserver，再执行:
//   go run ./scripts/test_cmd104.go
package main

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"time"
)

func main() {
	fmt.Println("===== 测试 CMD 104 邮箱登录 =====")

	email := "test@local.seer"
	password := "test123"
	passwordMD5 := md5.Sum([]byte(password))
	passwordMD5Str := hex.EncodeToString(passwordMD5[:])

	fmt.Printf("\n[1] 准备登录: email=%s, passwordMD5=%s\n", email, passwordMD5Str)

	// 连接登录服
	conn, err := net.DialTimeout("tcp", "127.0.0.1:1863", 3*time.Second)
	if err != nil {
		fmt.Printf("   连接失败: %v（请先启动 gameserver）\n", err)
		return
	}
	defer conn.Close()

	// 构建 CMD 104 请求体：email(64字节) + passwordMD5(32字节)
	body := make([]byte, 96)
	emailBytes := []byte(email)
	copy(body[0:64], emailBytes)
	passwordMD5Bytes := []byte(passwordMD5Str)
	copy(body[64:96], passwordMD5Bytes)

	// 构建包头：length(4) + version(1)=0x31 + cmd(4) + userId(4) + result(4)
	header := make([]byte, 17)
	binary.BigEndian.PutUint32(header[0:4], uint32(17+len(body)))
	header[4] = 0x31
	binary.BigEndian.PutUint32(header[5:9], 104)
	binary.BigEndian.PutUint32(header[9:13], 0)
	binary.BigEndian.PutUint32(header[13:17], 0)

	packet := append(header, body...)
	fmt.Printf("[2] 发送 CMD 104 请求（%d 字节）...\n", len(packet))
	_, err = conn.Write(packet)
	if err != nil {
		fmt.Printf("   发送失败: %v\n", err)
		return
	}

	// 读取响应头
	respHead := make([]byte, 17)
	n, err := io.ReadFull(conn, respHead)
	if err != nil || n < 17 {
		fmt.Printf("   读响应头失败: n=%d err=%v\n", n, err)
		return
	}

	respLen := binary.BigEndian.Uint32(respHead[0:4])
	respCmd := binary.BigEndian.Uint32(respHead[5:9])
	respUID := binary.BigEndian.Uint32(respHead[9:13])
	respError := binary.BigEndian.Uint32(respHead[13:17])

	fmt.Printf("[3] 收到响应: CMD=%d UID=%d ERROR=%d LEN=%d\n", respCmd, respUID, respError, respLen)

	if respError != 0 {
		fmt.Printf("   ❌ 登录失败，错误码: %d\n", respError)
		return
	}

	if respLen < 17+20 {
		fmt.Printf("   ⚠️  响应体长度异常: %d（期望至少37字节）\n", respLen)
		return
	}

	// 读取响应体：session(16) + roleCreate(4)
	bodyLen := int(respLen) - 17
	respBody := make([]byte, bodyLen)
	got, err := io.ReadFull(conn, respBody)
	if err != nil {
		fmt.Printf("   读响应体失败: got=%d err=%v\n", got, err)
		return
	}

	if len(respBody) >= 20 {
		session := respBody[0:16]
		roleCreate := binary.BigEndian.Uint32(respBody[16:20])
		sessionHex := hex.EncodeToString(session)
		fmt.Printf("   ✓ 登录成功！\n")
		fmt.Printf("     Session: %s\n", sessionHex)
		fmt.Printf("     角色状态: %s\n", map[uint32]string{0: "未创建", 1: "已创建"}[roleCreate])
		fmt.Printf("     米米号: %d\n", respUID)
	} else {
		fmt.Printf("   ⚠️  响应体格式异常: 长度=%d\n", len(respBody))
	}

	fmt.Println("\n===== CMD 104 测试完成 =====")
}

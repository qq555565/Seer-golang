package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"github.com/seer-game/golang-version/internal/core/packet"
)

// Test client to connect to local gameserver (127.0.0.1:5000),
// send CMD=1001 login request and print all responses (1001/80002/80001/2001/2003/2004).

const (
	serverAddr = "127.0.0.1:5000"
)

// buildLoginRequest builds a CMD=1001 login packet using given userID and 16‑byte session.
func buildLoginRequest(userID uint32, session []byte, seqID uint32) []byte {
	if len(session) != 16 {
		log.Fatalf("session length must be 16 bytes, got %d", len(session))
	}

	// 1001 的请求包体就是 16 字节 session
	body := make([]byte, 16)
	copy(body, session)

	// 手动构造请求包：长度 + 0x31 + cmd + uid + seq + body
	bodyLen := len(body)
	totalLen := 17 + bodyLen

	buf := make([]byte, 0, totalLen)

	// 长度
	tmp := make([]byte, 4)
	putU32 := func(v uint32) {
		tmp[0] = byte(v >> 24)
		tmp[1] = byte(v >> 16)
		tmp[2] = byte(v >> 8)
		tmp[3] = byte(v)
		buf = append(buf, tmp...)
	}

	putU32(uint32(totalLen))
	// 版本
	buf = append(buf, 0x31)
	// CMD=1001
	putU32(1001)
	// UID
	putU32(userID)
	// SEQ
	putU32(seqID)
	// body
	buf = append(buf, body...)

	return buf
}

type packetHeader struct {
	Len     uint32
	Version byte
	Cmd     uint32
	UID     uint32
	Seq     uint32
}

func readPacket(conn net.Conn) (packetHeader, []byte, error) {
	headerBuf := make([]byte, 17)
	if _, err := readFull(conn, headerBuf); err != nil {
		return packetHeader{}, nil, err
	}

	h := packetHeader{
		Len:     packet.ReadUInt32BE(headerBuf, 0),
		Version: headerBuf[4],
		Cmd:     packet.ReadUInt32BE(headerBuf, 5),
		UID:     packet.ReadUInt32BE(headerBuf, 9),
		Seq:     packet.ReadUInt32BE(headerBuf, 13),
	}

	if h.Len < 17 {
		return h, nil, fmt.Errorf("invalid packet length: %d", h.Len)
	}

	bodyLen := int(h.Len) - 17
	var body []byte
	if bodyLen > 0 {
		body = make([]byte, bodyLen)
		if _, err := readFull(conn, body); err != nil {
			return h, nil, err
		}
	}

	return h, body, nil
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func dumpShort(body []byte, max int) string {
	if len(body) == 0 {
		return "<empty>"
	}
	if len(body) > max {
		return hex.EncodeToString(body[:max]) + "..."
	}
	return hex.EncodeToString(body)
}

func main() {
	if len(os.Args) < 3 {
		fmt.Println("用法: go run test_client_map515.go <userID> <sessionHex16bytes>")
		fmt.Println("示例: go run test_client_map515.go 100000002 15a58b1e95037171cba44cd80fc541dd")
		return
	}

	var userID uint32
	_, err := fmt.Sscanf(os.Args[1], "%d", &userID)
	if err != nil {
		log.Fatalf("解析 userID 失败: %v", err)
	}

	sessionHex := os.Args[2]
	if len(sessionHex) != 32 {
		log.Fatalf("sessionHex 必须是 32 个十六进制字符 (16 字节), 当前长度=%d", len(sessionHex))
	}
	session, err := hex.DecodeString(sessionHex)
	if err != nil {
		log.Fatalf("解析 sessionHex 失败: %v", err)
	}

	log.Printf("连接到游戏服务器 %s ...", serverAddr)
	conn, err := net.DialTimeout("tcp", serverAddr, 5*time.Second)
	if err != nil {
		log.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()
	log.Printf("已连接，发送 CMD=1001 登录请求 (UID=%d)...", userID)

	loginPkt := buildLoginRequest(userID, session, 1)
	if _, err := conn.Write(loginPkt); err != nil {
		log.Fatalf("发送登录包失败: %v", err)
	}

	log.Println("开始监听服务器返回的所有数据包，按 Ctrl+C 退出。")

	for {
		h, body, err := readPacket(conn)
		if err != nil {
			log.Printf("读取数据包出错/连接关闭: %v", err)
			return
		}

		log.Printf("RECV CMD=%d UID=%d SEQ=%d LEN=%d BodyLen=%d",
			h.Cmd, h.UID, h.Seq, h.Len, len(body))

		switch h.Cmd {
		case 1001:
			log.Printf("  -> 登录响应 (CMD=1001), Map/状态等在 body 中，前 64 字节: %s", dumpShort(body, 64))
		case 80002:
			log.Printf("  -> 服务器列表 (CMD=80002), 前 64 字节: %s", dumpShort(body, 64))
		case 80001:
			log.Printf("  -> 频道列表 (CMD=80001), 前 64 字节: %s", dumpShort(body, 64))
		case 2001:
			log.Printf("  -> 进入地图响应 (CMD=2001), 前 64 字节: %s", dumpShort(body, 64))
		case 2003:
			log.Printf("  -> 地图玩家列表 (CMD=2003), 前 64 字节: %s", dumpShort(body, 64))
		case 2004:
			log.Printf("  -> 地图怪物列表 (CMD=2004), 前 64 字节: %s", dumpShort(body, 64))
		default:
			// 其他命令也打印一下简要信息
			log.Printf("  -> 其他 CMD=%d, 前 32 字节: %s", h.Cmd, dumpShort(body, 32))
		}
	}
}


package loginserver

import (
	"encoding/binary"
	"net"
	"testing"
)

// TestCMD105_ServerList 验证 CMD 105 返回的服务器列表含 127.0.0.1:5000，客户端据此连「频道服务器」
func TestCMD105_ServerList(t *testing.T) {
	dbPath := t.TempDir() + "/users.json"
	config := Config{
		LoginPort:       0, // 随机端口
		ServerID:        1,
		GameServerPort:  5000,
		LocalServerMode: true,
		UserDBPath:      dbPath,
	}
	ls := New(config)
	if err := ls.Start(); err != nil {
		t.Fatalf("启动登录服务器失败: %v", err)
	}
	defer ls.Stop()

	addr := ls.ListenAddr()
	if addr == nil {
		t.Fatal("ListenAddr 为空")
	}
	host := "127.0.0.1"
	port := addr.String()
	if _, p, err := net.SplitHostPort(port); err == nil {
		port = p
	}
	target := net.JoinHostPort(host, port)

	conn, err := net.Dial("tcp", target)
	if err != nil {
		t.Fatalf("连接登录服失败: %v", err)
	}
	defer conn.Close()

	// 组装 CMD 105 请求（17 字节头，与 Lua 一致：length, version=0x31, cmd, userId, result）
	bodyLen := 0
	pkgLen := uint32(17 + bodyLen)
	header := make([]byte, 17)
	binary.BigEndian.PutUint32(header[0:4], pkgLen)
	header[4] = 0x31
	binary.BigEndian.PutUint32(header[5:9], 105)
	binary.BigEndian.PutUint32(header[9:13], 100000001)
	binary.BigEndian.PutUint32(header[13:17], 0)

	_, err = conn.Write(header)
	if err != nil {
		t.Fatalf("发送 CMD 105 失败: %v", err)
	}

	// 读响应：先读 17 字节头
	respHead := make([]byte, 17)
	n, err := conn.Read(respHead)
	if err != nil || n < 17 {
		t.Fatalf("读响应头失败: n=%d err=%v", n, err)
	}
	respLen := binary.BigEndian.Uint32(respHead[0:4])
	if respLen < 17 {
		t.Fatalf("响应长度异常: %d", respLen)
	}
	bodyLenToRead := int(respLen) - 17
	body := make([]byte, bodyLenToRead)
	got := 0
	for got < bodyLenToRead {
		n, err = conn.Read(body[got:])
		if err != nil {
			break
		}
		got += n
	}
	body = body[:got]

	// CMD 105 体：maxOnlineID(4)+isVIP(4)+onlineCnt(4)+[id(4)+userCnt(4)+ip(16)+port(2)+friends(4)]*n
	if len(body) < 12 {
		t.Fatalf("CMD 105 体过短: %d", len(body))
	}
	onlineCnt := binary.BigEndian.Uint32(body[8:12])
	if onlineCnt < 1 {
		t.Fatalf("在线服务器数为 0")
	}
	// 第一条服务器从偏移 12 开始，30 字节
	if len(body) < 12+30 {
		t.Fatalf("CMD 105 体不足以包含一条 ServerInfo: %d", len(body))
	}
	off := 12
	// id(4) + userCnt(4) + ip(16) + port(2) + friends(4)
	ipBuf := body[off+8 : off+8+16]
	portHost := binary.BigEndian.Uint16(body[off+24 : off+26])
	ipStr := string(trimNull(ipBuf))
	if ipStr != "127.0.0.1" {
		t.Errorf("第一条服务器 IP 应为 127.0.0.1，得到 %q", ipStr)
	}
	if portHost != 5000 {
		t.Errorf("第一条服务器端口应为 5000，得到 %d", portHost)
	}
	t.Logf("CMD 105 通过: 服务器列表含 %s:%d（频道服务器）", ipStr, portHost)
}

func trimNull(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}

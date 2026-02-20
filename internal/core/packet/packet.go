package packet

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// BuildResponse 构建响应数据包
//
// 注意：AS3 客户端 (SV_1) 会把包头最后 4 字节当作 result/errorCode，
// 若非 0 会触发 SocketError 并丢弃该包体，随后导致流错位卡死。
// 因此服务端正常响应必须写 0（除非刻意返回错误码）。
func BuildResponse(cmdID int32, userID uint32, result int32, body []byte) []byte {
	// 计算包长度
	bodyLen := len(body)
	pkgLen := 17 + bodyLen

	// 创建缓冲区
	buf := new(bytes.Buffer)

	// 写入长度
	binary.Write(buf, binary.BigEndian, uint32(pkgLen))

	// 写入版本
	buf.WriteByte(0x31) // 默认版本

	// 写入命令ID
	binary.Write(buf, binary.BigEndian, uint32(cmdID))

	// 写入用户ID
	binary.Write(buf, binary.BigEndian, userID)

	// 写入 result/errorCode（正常必须为 0）
	binary.Write(buf, binary.BigEndian, uint32(result))

	// 写入包体
	buf.Write(body)

	return buf.Bytes()
}

// ReadUInt32BE 读取大端序的uint32
func ReadUInt32BE(data []byte, offset int) uint32 {
	if len(data) < offset+4 {
		return 0
	}
	return binary.BigEndian.Uint32(data[offset:])
}

// ReadUInt16BE 读取大端序的uint16
func ReadUInt16BE(data []byte, offset int) uint16 {
	if len(data) < offset+2 {
		return 0
	}
	return binary.BigEndian.Uint16(data[offset:])
}

// ReadByte 读取字节
func ReadByte(data []byte, offset int) byte {
	if len(data) < offset+1 {
		return 0
	}
	return data[offset]
}

// WriteUInt32BE 写入大端序的uint32
func WriteUInt32BE(buf *bytes.Buffer, value uint32) {
	binary.Write(buf, binary.BigEndian, value)
}

// WriteUInt16BE 写入大端序的uint16
func WriteUInt16BE(buf *bytes.Buffer, value uint16) {
	binary.Write(buf, binary.BigEndian, value)
}

// WriteByte 写入字节
func WriteByte(buf *bytes.Buffer, value byte) {
	buf.WriteByte(value)
}

// WriteString 写入字符串
func WriteString(buf *bytes.Buffer, str string, length int) {
	for i := 0; i < length; i++ {
		if i < len(str) {
			buf.WriteByte(str[i])
		} else {
			buf.WriteByte(0)
		}
	}
}

// HexDump 十六进制转储（格式对齐 Lua 后端）
func HexDump(data []byte, prefix string) string {
	if len(data) == 0 {
		return ""
	}

	var result string
	result += fmt.Sprintf("%s (%d bytes):\n", prefix, len(data))

	for i := 0; i < len(data); i += 16 {
		end := i + 16
		if end > len(data) {
			end = len(data)
		}

		// 地址（格式：0000:）
		result += fmt.Sprintf("   %04X: ", i)

		// 十六进制（每字节2位，空格分隔）
		for j := i; j < end; j++ {
			result += fmt.Sprintf("%02X ", data[j])
		}
		// 补齐空格到16字节
		for j := end; j < i+16; j++ {
			result += "   "
		}

		// ASCII（格式：|....|）
		result += " |"
		for j := i; j < end; j++ {
			b := data[j]
			if b >= 32 && b < 127 {
				result += string(b)
			} else {
				result += "."
			}
		}
		result += "|\n"
	}

	result += "[PACKET] --- 包体结束 ---\n"
	return result
}

// ParsePacket 解析数据包
func ParsePacket(data []byte) (length, version int, cmdID int32, userID uint32, seqID int32, body []byte, err error) {
	if len(data) < 17 {
		err = fmt.Errorf("数据包长度不足")
		return
	}

	length = int(ReadUInt32BE(data, 0))
	version = int(ReadByte(data, 4))
	cmdID = int32(ReadUInt32BE(data, 5))
	userID = ReadUInt32BE(data, 9)
	seqID = int32(ReadUInt32BE(data, 13))

	if len(data) > 17 {
		body = data[17:]
	}

	return
}

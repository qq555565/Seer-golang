package soultransformcache

import (
	"sync"
	"time"
)

const defaultTTL = 60 * time.Second

var (
	mu    sync.RWMutex
	byIP  = make(map[string]entry)
	ttl   = defaultTTL
)

type entry struct {
	PetID int
	At    time.Time
}

// SetTTL 设置 IP 登记的过期时间，默认 60 秒
func SetTTL(d time.Duration) {
	mu.Lock()
	ttl = d
	mu.Unlock()
}

// Register 登记该 IP 刚完成元神赋形领取的精灵 ID，供资源服 /resource/pet/swf/1.swf 按 IP 返回对应 swf
func Register(clientIP string, petID int) {
	if clientIP == "" || petID <= 0 {
		return
	}
	mu.Lock()
	byIP[clientIP] = entry{PetID: petID, At: time.Now()}
	mu.Unlock()
}

// Lookup 按客户端 IP 查找最近登记的赋形奖励精灵 ID，未找到或已过期返回 0
func Lookup(clientIP string) int {
	if clientIP == "" {
		return 0
	}
	mu.RLock()
	e, ok := byIP[clientIP]
	d := ttl
	mu.RUnlock()
	if !ok || e.PetID <= 0 {
		return 0
	}
	if time.Since(e.At) > d {
		mu.Lock()
		delete(byIP, clientIP)
		mu.Unlock()
		return 0
	}
	return e.PetID
}

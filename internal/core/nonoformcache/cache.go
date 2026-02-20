package nonoformcache

import (
	"sync"
	"time"
)

const defaultTTL = 5 * time.Minute

var (
	mu        sync.RWMutex
	byIP      = make(map[string]entry)
	byUserID  = make(map[int64]entry)
	ttl       = defaultTTL
)

type entry struct {
	Level int // 超能等级 SuperLevel 1-12，资源服据此换算形态 1-5
	At    time.Time
}

// SetTTL 设置 IP 形态缓存的过期时间
func SetTTL(d time.Duration) {
	mu.Lock()
	ttl = d
	mu.Unlock()
}

// Register 登记该 IP 对应的超能等级(1-12)，供资源服按 IP 查等级再换算形态
func Register(clientIP string, superLevel int) {
	if clientIP == "" || superLevel <= 0 {
		return
	}
	if superLevel > 12 {
		superLevel = 12
	}
	mu.Lock()
	byIP[clientIP] = entry{Level: superLevel, At: time.Now()}
	mu.Unlock()
}

// Lookup 按客户端 IP 查找超能等级(1-12)，未找到或已过期返回 0
func Lookup(clientIP string) int {
	if clientIP == "" {
		return 0
	}
	mu.RLock()
	e, ok := byIP[clientIP]
	d := ttl
	mu.RUnlock()
	if !ok || e.Level <= 0 {
		return 0
	}
	if time.Since(e.At) > d {
		mu.Lock()
		delete(byIP, clientIP)
		mu.Unlock()
		return 0
	}
	return e.Level
}

// RegisterByUserID 按游戏米米号登记该玩家的超能等级(1-12)，供资源服按等级查形态
func RegisterByUserID(userID int64, superLevel int) {
	if userID <= 0 || superLevel <= 0 {
		return
	}
	if superLevel > 12 {
		superLevel = 12
	}
	mu.Lock()
	byUserID[userID] = entry{Level: superLevel, At: time.Now()}
	mu.Unlock()
}

// LookupByUserID 按米米号查找该玩家的超能等级(1-12)，未找到或已过期返回 0
func LookupByUserID(userID int64) int {
	if userID <= 0 {
		return 0
	}
	mu.RLock()
	e, ok := byUserID[userID]
	d := ttl
	mu.RUnlock()
	if !ok || e.Level <= 0 {
		return 0
	}
	if time.Since(e.At) > d {
		mu.Lock()
		delete(byUserID, userID)
		mu.Unlock()
		return 0
	}
	return e.Level
}

// SuperLevelToForm 按超能等级(1-12)换算形态(1-5)：1-3→1，4-6→2，7-8→3，9-11→4，12→5
func SuperLevelToForm(superLevel int) int {
	if superLevel < 1 {
		return 0
	}
	if superLevel >= 12 {
		return 5
	}
	if superLevel >= 9 {
		return 4
	}
	if superLevel >= 7 {
		return 3
	}
	if superLevel >= 4 {
		return 2
	}
	return 1
}

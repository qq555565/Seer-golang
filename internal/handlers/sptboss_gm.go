package handlers

import (
	"encoding/json"
	"sync"

	"github.com/seer-game/golang-version/internal/core/logger"
	"github.com/seer-game/golang-version/internal/game/sptboss"
)

// SPTBossPersistence SPT BOSS 配置持久化接口（由 main 注入 *userdb.UserDB）
type SPTBossPersistence interface {
	LoadSPTBossConfig() ([]byte, error)
	SaveSPTBossConfig(data []byte) error
}

var (
	sptBossPersistenceMu sync.RWMutex
	sptBossPersistence   SPTBossPersistence
)

// SetSPTBossPersistence 设置 SPT BOSS 配置持久化实现
func SetSPTBossPersistence(p SPTBossPersistence) {
	sptBossPersistenceMu.Lock()
	defer sptBossPersistenceMu.Unlock()
	sptBossPersistence = p
}

// LoadSPTBossConfig 从数据库或本地加载 SPT BOSS 配置并应用到 sptboss 包
func LoadSPTBossConfig() {
	sptBossPersistenceMu.RLock()
	p := sptBossPersistence
	sptBossPersistenceMu.RUnlock()
	if p == nil {
		return
	}
	data, err := p.LoadSPTBossConfig()
	if err != nil {
		logger.Warning("[SPT BOSS] 从数据库加载失败: " + err.Error())
		return
	}
	if len(data) == 0 {
		return
	}
	var cfg sptboss.FullConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		logger.Warning("[SPT BOSS] 配置 JSON 解析失败: " + err.Error())
		return
	}
	sptboss.SetConfig(&cfg)
	logger.Info("[SPT BOSS] 已从数据库加载配置并应用")
}

// GetSPTBossConfig 返回当前 SPT BOSS 配置（供 GM 前端展示）
func GetSPTBossConfig() sptboss.FullConfig {
	return sptboss.GetConfig()
}

// SetSPTBossConfig 更新 SPT BOSS 配置并持久化（GM 保存）
func SetSPTBossConfig(cfg *sptboss.FullConfig) error {
	if cfg == nil {
		sptboss.SetConfig(nil)
		// 恢复内置后也持久化一份“空”或内置快照，便于下次加载时可选
		cfg2 := sptboss.GetConfig()
		return saveSPTBossConfigToPersistence(&cfg2)
	}
	sptboss.SetConfig(cfg)
	return saveSPTBossConfigToPersistence(cfg)
}

func saveSPTBossConfigToPersistence(cfg *sptboss.FullConfig) error {
	sptBossPersistenceMu.RLock()
	p := sptBossPersistence
	sptBossPersistenceMu.RUnlock()
	if p == nil {
		return nil
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return p.SaveSPTBossConfig(data)
}

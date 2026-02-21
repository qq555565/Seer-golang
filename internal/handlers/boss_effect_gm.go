package handlers

import (
	"encoding/json"
	"strconv"
	"sync"

	"github.com/seer-game/golang-version/internal/core/logger"
)

// BossEffectParams 单个 BOSS 效果 ID 的可配置参数（GM 可编辑）
type BossEffectParams struct {
	FixedDamage   int `json:"fixedDamage"`   // 固定伤害（如 976 简/极 的 28）
	PowerPercent  int `json:"powerPercent"`  // 威力百分比（如 1603 的 100）
	Rounds        int `json:"rounds"`        // 持续回合
	SecondParam   int `json:"secondParam"`   // 第二参数（如 1603 的 1、1211 的 300）
	PriorityBonus int `json:"priorityBonus"` // 先制加值
}

// BossEffectConfig 全量 BOSS/特殊多效果配置，key 为效果 ID 字符串（如 "691","976","1470"）
type BossEffectConfig struct {
	Effects map[string]BossEffectParams `json:"effects"`
}

var (
	bossEffectConfigMu sync.RWMutex
	bossEffectConfig   BossEffectConfig
	bossEffectPersistence BossEffectPersistence
)

// BossEffectPersistence 持久化接口（由 main 注入 *userdb.UserDB）
type BossEffectPersistence interface {
	LoadBossEffectConfig() ([]byte, error)
	SaveBossEffectConfig(data []byte) error
}

// SetBossEffectPersistence 设置 BOSS 多效果配置持久化
func SetBossEffectPersistence(p BossEffectPersistence) {
	bossEffectConfigMu.Lock()
	defer bossEffectConfigMu.Unlock()
	bossEffectPersistence = p
}

// LoadBossEffectConfig 从数据库或离线文件加载 BOSS 多效果配置
func LoadBossEffectConfig() {
	bossEffectConfigMu.Lock()
	defer bossEffectConfigMu.Unlock()
	p := bossEffectPersistence
	bossEffectConfigMu.Unlock()
	if p == nil {
		bossEffectConfigMu.Lock()
		return
	}
	data, err := p.LoadBossEffectConfig()
	bossEffectConfigMu.Lock()
	if err != nil {
		logger.Warning("[BOSS多效果] 从数据库加载失败: " + err.Error())
		return
	}
	if len(data) == 0 {
		// 使用空配置，默认无加成
		bossEffectConfig = BossEffectConfig{Effects: map[string]BossEffectParams{}}
		return
	}
	var cfg BossEffectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		logger.Warning("[BOSS多效果] 配置 JSON 解析失败: " + err.Error())
		return
	}
	if cfg.Effects == nil {
		cfg.Effects = map[string]BossEffectParams{}
	}
	bossEffectConfig = cfg
	logger.Info("[BOSS多效果] 已从数据库加载配置")
}

// GetBossEffectConfig 返回当前配置（供 GM 前端展示与保存）
func GetBossEffectConfig() BossEffectConfig {
	bossEffectConfigMu.RLock()
	defer bossEffectConfigMu.RUnlock()
	if bossEffectConfig.Effects == nil {
		return BossEffectConfig{Effects: map[string]BossEffectParams{}}
	}
	// 深拷贝
	out := BossEffectConfig{Effects: make(map[string]BossEffectParams)}
	for k, v := range bossEffectConfig.Effects {
		out.Effects[k] = v
	}
	return out
}

// SetBossEffectConfig 更新配置并持久化
func SetBossEffectConfig(cfg *BossEffectConfig) error {
	bossEffectConfigMu.Lock()
	defer bossEffectConfigMu.Unlock()
	if cfg == nil || cfg.Effects == nil {
		bossEffectConfig = BossEffectConfig{Effects: map[string]BossEffectParams{}}
	} else {
		bossEffectConfig = *cfg
	}
	p := bossEffectPersistence
	if p == nil {
		return nil
	}
	data, err := json.MarshalIndent(&bossEffectConfig, "", "  ")
	if err != nil {
		return err
	}
	return p.SaveBossEffectConfig(data)
}

// GetBossEffectParams 按效果 ID 获取参数（战斗中用）；effectID 为 691/700/976/1470 等
func GetBossEffectParams(effectID int) BossEffectParams {
	bossEffectConfigMu.RLock()
	defer bossEffectConfigMu.RUnlock()
	key := strconv.Itoa(effectID)
	if bossEffectConfig.Effects == nil {
		return BossEffectParams{}
	}
	return bossEffectConfig.Effects[key]
}

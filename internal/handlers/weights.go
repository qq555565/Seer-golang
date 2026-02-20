package handlers

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/seer-game/golang-version/internal/core/logger"
)

const weightsConfigFile = "weights_config.json"

// WeightedPetEntry 权重精灵项（元神珠转化完成时按权重随机给一只）
type WeightedPetEntry struct {
	PetClass int `json:"petClass"`
	Weight   int `json:"weight"`
}

// WeightsConfig 权重配置：胶囊捕捉率、精灵融合成功率、元神珠转化时间
type WeightsConfig struct {
	CapsuleCatchRates        map[string]int            `json:"capsuleCatchRates"`        // 胶囊ID -> 捕捉率(0-100)
	FusionSuccessRate        int                       `json:"fusionSuccessRate"`         // 精灵融合全局默认(0-100)，兼容旧配置
	FusionSuccessRates       map[string]int            `json:"fusionSuccessRates"`       // 元神珠物品 ID -> 融合成功率(0-100)
	SoulPearlTransmuteTm     map[string]int            `json:"soulPearlTransmuteTm"`     // 元神珠 ID -> 普通用户转化时间(秒)，默认 1800
	SoulPearlVipTransmuteTm  map[string]int            `json:"soulPearlVipTransmuteTm"` // 元神珠 ID -> VIP 用户转化时间(秒)，默认 900
	SoulPearlRewardPets      map[string][]WeightedPetEntry `json:"soulPearlRewardPets"`  // 元神珠 ID -> [{ petClass, weight }]，转化完成只给 1 只
}

var (
	weightsConfig       WeightsConfig
	weightsConfigMu     sync.RWMutex
	weightsPersistence  WeightsPersistence // 若为 nil 则使用本地 JSON 文件；由 main 在启用 MySQL 时设置为 UserDB
)

// WeightsPersistence 权重配置持久化（数据库或文件），由 main 在启动时注入
type WeightsPersistence interface {
	LoadWeights() ([]byte, error)
	SaveWeights(data []byte) error
}

// SetWeightsPersistence 设置持久化实现。启用 MySQL 时传入 *userdb.UserDB；否则不调用，将使用 weights_config.json
func SetWeightsPersistence(p WeightsPersistence) {
	weightsPersistence = p
}

func init() {
	loadDefaultWeights()
	// 不再在此处 LoadWeightsConfig，改由 main 在 SetWeightsPersistence 之后调用
}

// defaultCapsuleRates 返回默认胶囊捕捉率映射，供 GM 展示与回填
func defaultCapsuleRates() map[string]int {
	return map[string]int{
		"300001": 50,  // 普通精灵胶囊
		"300002": 65,  // 中级精灵胶囊
		"300003": 80,  // 高级精灵胶囊
		"300004": 95,  // 超级精灵胶囊
		"300005": 90,
		"300006": 100, // 无敌精灵胶囊
		"300009": 100, // 时空精灵胶囊
	}
}

// getDefaultCapsuleRatesForGM 供 GM 接口在配置为空时返回默认胶囊列表，保证前端始终有数据
func getDefaultCapsuleRatesForGM() map[string]int {
	return defaultCapsuleRates()
}

// loadDefaultWeights 默认权重
func loadDefaultWeights() {
	weightsConfigMu.Lock()
	weightsConfig = WeightsConfig{
		CapsuleCatchRates:       defaultCapsuleRates(),
		FusionSuccessRate:       100,
		FusionSuccessRates:      map[string]int{},
		SoulPearlTransmuteTm:    map[string]int{},
		SoulPearlVipTransmuteTm: map[string]int{},
		SoulPearlRewardPets:     map[string][]WeightedPetEntry{},
	}
	weightsConfigMu.Unlock()
}

// LoadWeightsConfig 从数据库或本地 JSON 加载（若已 SetWeightsPersistence 则从 DB，否则从 weights_config.json）
func LoadWeightsConfig() {
	if weightsPersistence != nil {
		data, err := weightsPersistence.LoadWeights()
		if err != nil {
			logger.Warning(fmt.Sprintf("[权重] 从数据库加载失败: %v，使用默认", err))
			return
		}
		if len(data) == 0 {
			logger.Info("[权重] 数据库中无配置，使用默认")
			return
		}
		var cfg WeightsConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			logger.Warning(fmt.Sprintf("[权重] 数据库配置解析失败: %v", err))
			return
		}
		weightsConfigMu.Lock()
		// 仅当加载到的配置非空时才覆盖，避免被空数据清空（如曾误保存空列表）
		if cfg.CapsuleCatchRates != nil && len(cfg.CapsuleCatchRates) > 0 {
			weightsConfig.CapsuleCatchRates = cfg.CapsuleCatchRates
		}
		if cfg.FusionSuccessRate >= 0 && cfg.FusionSuccessRate <= 100 {
			weightsConfig.FusionSuccessRate = cfg.FusionSuccessRate
		}
		if cfg.FusionSuccessRates != nil && len(cfg.FusionSuccessRates) > 0 {
			weightsConfig.FusionSuccessRates = cfg.FusionSuccessRates
		}
		if cfg.SoulPearlTransmuteTm != nil && len(cfg.SoulPearlTransmuteTm) > 0 {
			weightsConfig.SoulPearlTransmuteTm = cfg.SoulPearlTransmuteTm
		}
		if cfg.SoulPearlVipTransmuteTm != nil && len(cfg.SoulPearlVipTransmuteTm) > 0 {
			weightsConfig.SoulPearlVipTransmuteTm = cfg.SoulPearlVipTransmuteTm
		}
		if cfg.SoulPearlRewardPets != nil {
			weightsConfig.SoulPearlRewardPets = cfg.SoulPearlRewardPets
		}
		weightsConfigMu.Unlock()
		logger.Info("[权重] 已从数据库加载配置")
		return
	}
	path := weightsConfigFile
	if dir, err := os.Getwd(); err == nil {
		path = filepath.Join(dir, weightsConfigFile)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		logger.Info(fmt.Sprintf("[权重] 未找到 %s，使用默认", path))
		return
	}
	var cfg WeightsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		logger.Warning(fmt.Sprintf("[权重] 解析失败: %v", err))
		return
	}
	weightsConfigMu.Lock()
	if cfg.CapsuleCatchRates != nil && len(cfg.CapsuleCatchRates) > 0 {
		weightsConfig.CapsuleCatchRates = cfg.CapsuleCatchRates
	}
	if cfg.FusionSuccessRate >= 0 && cfg.FusionSuccessRate <= 100 {
		weightsConfig.FusionSuccessRate = cfg.FusionSuccessRate
	}
	if cfg.FusionSuccessRates != nil && len(cfg.FusionSuccessRates) > 0 {
		weightsConfig.FusionSuccessRates = cfg.FusionSuccessRates
	}
	if cfg.SoulPearlTransmuteTm != nil && len(cfg.SoulPearlTransmuteTm) > 0 {
		weightsConfig.SoulPearlTransmuteTm = cfg.SoulPearlTransmuteTm
	}
	if cfg.SoulPearlVipTransmuteTm != nil && len(cfg.SoulPearlVipTransmuteTm) > 0 {
		weightsConfig.SoulPearlVipTransmuteTm = cfg.SoulPearlVipTransmuteTm
	}
	if cfg.SoulPearlRewardPets != nil {
		weightsConfig.SoulPearlRewardPets = cfg.SoulPearlRewardPets
	}
	weightsConfigMu.Unlock()
	logger.Info("[权重] 已从文件加载配置")
}

// SaveWeightsConfig 保存到数据库或本地 JSON（若已 SetWeightsPersistence 则写 DB，否则写 weights_config.json）
func SaveWeightsConfig() error {
	weightsConfigMu.RLock()
	cfg := weightsConfig
	weightsConfigMu.RUnlock()

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if weightsPersistence != nil {
		return weightsPersistence.SaveWeights(data)
	}
	path := weightsConfigFile
	if dir, err := os.Getwd(); err == nil {
		path = filepath.Join(dir, weightsConfigFile)
	}
	return os.WriteFile(path, data, 0644)
}

// GetCapsuleCatchMod 获取胶囊捕捉修正系数(0~1)，供 handleCatchMonster 使用
func GetCapsuleCatchMod(capsuleID int) float64 {
	weightsConfigMu.RLock()
	defer weightsConfigMu.RUnlock()
	pct, ok := weightsConfig.CapsuleCatchRates[strconv.Itoa(capsuleID)]
	if !ok {
		pct = 50 // 默认 50%
	}
	mod := float64(pct) / 100.0
	if mod < 0 {
		mod = 0
	}
	if mod > 1 {
		mod = 1
	}
	return mod
}

// GetFusionSuccessRate 获取融合成功率(0~1)。按元神珠配置：PetClass 对应元神珠物品 ID，再查该元神珠的配置；未配置用全局默认
func GetFusionSuccessRate(petClass int) float64 {
	weightsConfigMu.RLock()
	rates := make(map[string]int)
	for k, v := range weightsConfig.FusionSuccessRates {
		rates[k] = v
	}
	defaultRate := weightsConfig.FusionSuccessRate
	weightsConfigMu.RUnlock()
	return GetFusionSuccessRateByPetClass(petClass, rates, defaultRate)
}

// GetSoulPearlRewardPetClass 按 GM 配置的赋形奖励精灵权重随机选取一只，返回 PetClass。若未配置或列表为空则 ok=false。
func GetSoulPearlRewardPetClass(soulPearlItemID int) (petClass int, ok bool) {
	if soulPearlItemID <= 0 {
		return 0, false
	}
	weightsConfigMu.RLock()
	list := weightsConfig.SoulPearlRewardPets[strconv.Itoa(soulPearlItemID)]
	weightsConfigMu.RUnlock()
	if len(list) == 0 {
		return 0, false
	}
	var sum int
	for _, e := range list {
		if e.Weight <= 0 {
			continue
		}
		sum += e.Weight
	}
	if sum <= 0 {
		return 0, false
	}
	r := rand.Intn(sum)
	for _, e := range list {
		if e.Weight <= 0 {
			continue
		}
		if r < e.Weight {
			return e.PetClass, true
		}
		r -= e.Weight
	}
	return list[0].PetClass, true
}

// GetSoulPearlTransmuteTime 获取元神珠转化时间(秒)。soulPearlItemID 为元神珠物品 ID(如 1000001)，isVip 为是否 VIP 用户。
// soulPearlItemID<=0 时直接返回默认值，避免用 key "0" 误取 GM 配置导致与后台设置不一致。
func GetSoulPearlTransmuteTime(soulPearlItemID int, isVip bool) int {
	if soulPearlItemID <= 0 {
		if isVip {
			return DefaultVipTransmuteTm
		}
		return DefaultTransmuteTm
	}
	weightsConfigMu.RLock()
	defer weightsConfigMu.RUnlock()
	key := strconv.Itoa(soulPearlItemID)
	if isVip {
		if t, ok := weightsConfig.SoulPearlVipTransmuteTm[key]; ok && t > 0 {
			return t
		}
		return DefaultVipTransmuteTm
	}
	if t, ok := weightsConfig.SoulPearlTransmuteTm[key]; ok && t > 0 {
		return t
	}
	return DefaultTransmuteTm
}

// GetWeightsConfig 供 GM 使用
func GetWeightsConfig() WeightsConfig {
	weightsConfigMu.RLock()
	defer weightsConfigMu.RUnlock()
	cfg := WeightsConfig{
		CapsuleCatchRates:       make(map[string]int),
		FusionSuccessRate:       weightsConfig.FusionSuccessRate,
		FusionSuccessRates:      make(map[string]int),
		SoulPearlTransmuteTm:    make(map[string]int),
		SoulPearlVipTransmuteTm: make(map[string]int),
		SoulPearlRewardPets:     make(map[string][]WeightedPetEntry),
	}
	for k, v := range weightsConfig.CapsuleCatchRates {
		cfg.CapsuleCatchRates[k] = v
	}
	for k, v := range weightsConfig.FusionSuccessRates {
		cfg.FusionSuccessRates[k] = v
	}
	for k, v := range weightsConfig.SoulPearlTransmuteTm {
		cfg.SoulPearlTransmuteTm[k] = v
	}
	for k, v := range weightsConfig.SoulPearlVipTransmuteTm {
		cfg.SoulPearlVipTransmuteTm[k] = v
	}
	for k, v := range weightsConfig.SoulPearlRewardPets {
		cfg.SoulPearlRewardPets[k] = append([]WeightedPetEntry(nil), v...)
	}
	return cfg
}

// SetWeightsConfig 供 GM 使用
func SetWeightsConfig(cfg WeightsConfig) {
	weightsConfigMu.Lock()
	if cfg.CapsuleCatchRates != nil {
		weightsConfig.CapsuleCatchRates = cfg.CapsuleCatchRates
	}
	if cfg.FusionSuccessRate >= 0 && cfg.FusionSuccessRate <= 100 {
		weightsConfig.FusionSuccessRate = cfg.FusionSuccessRate
	}
	if cfg.FusionSuccessRates != nil {
		weightsConfig.FusionSuccessRates = cfg.FusionSuccessRates
	}
	if cfg.SoulPearlTransmuteTm != nil {
		weightsConfig.SoulPearlTransmuteTm = cfg.SoulPearlTransmuteTm
	}
	if cfg.SoulPearlVipTransmuteTm != nil {
		weightsConfig.SoulPearlVipTransmuteTm = cfg.SoulPearlVipTransmuteTm
	}
	if cfg.SoulPearlRewardPets != nil {
		weightsConfig.SoulPearlRewardPets = cfg.SoulPearlRewardPets
	}
	weightsConfigMu.Unlock()
}

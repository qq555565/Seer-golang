package handlers

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/seer-game/golang-version/internal/core/logger"
)

// DarkPortalBossEntry 暗黑武斗场BOSS配置条目
type DarkPortalBossEntry struct {
	DoorIndex  int    `json:"doorIndex"`  // 门索引（0-10）
	SubIndex   int    `json:"subIndex"`   // 子关卡索引（0表示第一关，如3-1）
	BossID     int    `json:"bossId"`     // Boss精灵ID
	EnemyLv    int    `json:"enemyLv"`    // 敌人等级（0=使用默认50级）
	RewardItemID int  `json:"rewardItemId"` // 首次击败奖励物品ID（精元等，0表示无奖励）
	RewardPetID int   `json:"rewardPetId"`  // 首次击败奖励精灵ID（0表示无奖励）
	Remark     string `json:"remark,omitempty"` // 备注
}

// DarkPortalPersistence 暗黑武斗场配置持久化接口（由 main 注入 *userdb.UserDB）
type DarkPortalPersistence interface {
	LoadDarkPortalConfig() ([]byte, error)
	SaveDarkPortalConfig(data []byte) error
}

var (
	darkPortalMu          sync.RWMutex
	darkPortalConfig      []DarkPortalBossEntry
	darkPortalByDoor      map[int][]DarkPortalBossEntry
	darkPortalPersistence DarkPortalPersistence
)

// SetDarkPortalPersistence 设置暗黑武斗场配置持久化实现
func SetDarkPortalPersistence(p DarkPortalPersistence) {
	darkPortalPersistence = p
}

// LoadDarkPortalConfig 从数据库加载暗黑武斗场配置
func LoadDarkPortalConfig() {
	darkPortalMu.Lock()
	defer darkPortalMu.Unlock()

	darkPortalConfig = nil
	darkPortalByDoor = make(map[int][]DarkPortalBossEntry)

	if darkPortalPersistence == nil {
		return
	}
	data, err := darkPortalPersistence.LoadDarkPortalConfig()
	if err != nil || len(data) == 0 {
		if err != nil {
			logger.Warning("[暗黑武斗场] 从数据库加载失败: " + err.Error())
		}
		// 如果数据库中没有配置，暂时不初始化，等待main.go调用InitDarkPortalConfigFromHandlers
		// initDefaultDarkPortalConfig() 会在main.go中调用InitDarkPortalConfigFromHandlers时初始化
		return
	}
	var list []DarkPortalBossEntry
	if err := json.Unmarshal(data, &list); err != nil {
		logger.Warning("[暗黑武斗场] 配置 JSON 解析失败: " + err.Error())
		// JSON解析失败，暂时不初始化，等待main.go调用InitDarkPortalConfigFromHandlers
		return
	}
	// 规范化：按 doorIndex, subIndex 排序，过滤非法行
	for i := range list {
		if list[i].DoorIndex < 0 || list[i].DoorIndex > 10 || list[i].BossID <= 0 {
			continue
		}
		if list[i].SubIndex < 0 {
			list[i].SubIndex = 0
		}
		if list[i].EnemyLv < 0 {
			list[i].EnemyLv = 0
		}
		darkPortalConfig = append(darkPortalConfig, list[i])
	}
	sort.Slice(darkPortalConfig, func(i, j int) bool {
		if darkPortalConfig[i].DoorIndex == darkPortalConfig[j].DoorIndex {
			return darkPortalConfig[i].SubIndex < darkPortalConfig[j].SubIndex
		}
		return darkPortalConfig[i].DoorIndex < darkPortalConfig[j].DoorIndex
	})
	for _, e := range darkPortalConfig {
		darkPortalByDoor[e.DoorIndex] = append(darkPortalByDoor[e.DoorIndex], e)
	}
	logger.Info(fmt.Sprintf("[暗黑武斗场] 已从数据库加载配置, 条目数=%d", len(darkPortalConfig)))
}

// initDefaultDarkPortalConfig 初始化默认配置（已废弃，由InitDarkPortalConfigFromHandlers替代）
func initDefaultDarkPortalConfig() {
	// 已废弃，不再使用
}

// InitDarkPortalConfigFromHandlers 从handlers.go中的配置初始化（由main.go在启动时调用）
func InitDarkPortalConfigFromHandlers() {
	darkPortalMu.Lock()
	defer darkPortalMu.Unlock()
	
	darkPortalConfig = make([]DarkPortalBossEntry, 0)
	darkPortalByDoor = make(map[int][]DarkPortalBossEntry)
	
	// 从handlers.go中的darkPortalDoorBosses和darkPortalBossRewards初始化
	// 由于在同一个包中，可以直接访问handlers.go中的全局变量
	for doorIndex, bosses := range darkPortalDoorBosses {
		for subIndex, bossID := range bosses {
			rewardItemID := darkPortalBossRewards[bossID]
			entry := DarkPortalBossEntry{
				DoorIndex:    int(doorIndex),
				SubIndex:     subIndex,
				BossID:       int(bossID),
				EnemyLv:      50, // 默认等级
				RewardItemID: rewardItemID,
				RewardPetID:  0,
			}
			darkPortalConfig = append(darkPortalConfig, entry)
			darkPortalByDoor[int(doorIndex)] = append(darkPortalByDoor[int(doorIndex)], entry)
		}
	}
	logger.Info(fmt.Sprintf("[暗黑武斗场] 从handlers.go初始化默认配置, 条目数=%d", len(darkPortalConfig)))
}

// GetDarkPortalConfig 返回全部配置（供 GM 前端展示）
func GetDarkPortalConfig() []DarkPortalBossEntry {
	darkPortalMu.RLock()
	defer darkPortalMu.RUnlock()
	out := make([]DarkPortalBossEntry, len(darkPortalConfig))
	copy(out, darkPortalConfig)
	return out
}

// SetDarkPortalConfig 更新配置并持久化（GM 保存）
func SetDarkPortalConfig(list []DarkPortalBossEntry) error {
	// 先在内存中规范化
	tmp := make([]DarkPortalBossEntry, 0, len(list))
	for i := range list {
		if list[i].DoorIndex < 0 || list[i].DoorIndex > 10 || list[i].BossID <= 0 {
			continue
		}
		if list[i].SubIndex < 0 {
			list[i].SubIndex = 0
		}
		if list[i].EnemyLv < 0 {
			list[i].EnemyLv = 0
		}
		tmp = append(tmp, list[i])
	}
	sort.Slice(tmp, func(i, j int) bool {
		if tmp[i].DoorIndex == tmp[j].DoorIndex {
			return tmp[i].SubIndex < tmp[j].SubIndex
		}
		return tmp[i].DoorIndex < tmp[j].DoorIndex
	})

	darkPortalMu.Lock()
	darkPortalConfig = tmp
	darkPortalByDoor = make(map[int][]DarkPortalBossEntry)
	for _, e := range darkPortalConfig {
		darkPortalByDoor[e.DoorIndex] = append(darkPortalByDoor[e.DoorIndex], e)
	}
	darkPortalMu.Unlock()

	// 更新handlers.go中的配置映射
	updateDarkPortalMaps()

	if darkPortalPersistence == nil {
		return nil
	}
	data, err := json.Marshal(tmp)
	if err != nil {
		return err
	}
	return darkPortalPersistence.SaveDarkPortalConfig(data)
}

// updateDarkPortalMaps 更新handlers.go中的darkPortalDoorBosses和darkPortalBossRewards映射
// 注意：这个函数目前不直接修改handlers.go中的全局变量，而是通过GetDarkPortalBosses和GetDarkPortalBossReward函数来获取配置
func updateDarkPortalMaps() {
	// 配置已更新到darkPortalConfig和darkPortalByDoor中
	// handlers.go中的代码会优先使用GetDarkPortalBossEntry和GetDarkPortalBossReward来获取配置
}

// GetDarkPortalBosses 获取指定门的Boss列表（供handlers使用）
func GetDarkPortalBosses(doorIndex uint32) []uint32 {
	darkPortalMu.RLock()
	defer darkPortalMu.RUnlock()
	if bosses, ok := darkPortalByDoor[int(doorIndex)]; ok {
		result := make([]uint32, 0, len(bosses))
		for _, entry := range bosses {
			result = append(result, uint32(entry.BossID))
		}
		return result
	}
	return nil
}

// GetDarkPortalBossReward 获取指定Boss的奖励物品ID（供handlers使用）
func GetDarkPortalBossReward(bossID uint32) (rewardItemID int, rewardPetID int, ok bool) {
	darkPortalMu.RLock()
	defer darkPortalMu.RUnlock()
	for _, entry := range darkPortalConfig {
		if entry.BossID == int(bossID) {
			return entry.RewardItemID, entry.RewardPetID, true
		}
	}
	return 0, 0, false
}

// GetDarkPortalBossEntry 获取指定门和子关卡的配置
func GetDarkPortalBossEntry(doorIndex uint32, subIndex uint32) (DarkPortalBossEntry, bool) {
	darkPortalMu.RLock()
	defer darkPortalMu.RUnlock()
	if bosses, ok := darkPortalByDoor[int(doorIndex)]; ok {
		for _, entry := range bosses {
			if entry.SubIndex == int(subIndex) {
				return entry, true
			}
		}
	}
	return DarkPortalBossEntry{}, false
}

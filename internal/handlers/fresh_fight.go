package handlers

import (
	"encoding/json"
	"sort"
	"sync"

	"github.com/seer-game/golang-version/internal/core/logger"
	gamepets "github.com/seer-game/golang-version/internal/game/pets"
)

// FreshFightLevelEntry 一条试炼之塔配置：某层第 seq 个怪及其属性倍率；通关本层奖励取该层最后一条（Seq 最大）的 Reward 字段
type FreshFightLevelEntry struct {
	Level        int    `json:"level"`        // 第几层（1..N）
	Seq         int    `json:"seq"`          // 该层内顺序（1..）
	BossID      int    `json:"bossId"`       // 精灵 ID
	EnemyLv     int    `json:"enemyLv"`      // 敌人等级（0=按默认规则自动计算）
	HPRatio     int    `json:"hpRatio"`      // HP 百分比（100=原始）
	AtkRatio    int    `json:"atkRatio"`
	DefRatio    int    `json:"defRatio"`
	SARatio     int    `json:"saRatio"`
	SDRatio     int    `json:"sdRatio"`
	SPRatio     int    `json:"spRatio"`
	Remark      string `json:"remark,omitempty"`
	RewardItemID int   `json:"rewardItemId"` // 通关本层奖励精元/道具 ID（仅该层最后一条生效），0=无
	RewardPetID  int   `json:"rewardPetId"`  // 通关本层奖励精灵 ID（仅该层最后一条生效），0=无
}

// FreshFightPersistence 试炼之塔配置持久化接口（由 main 注入 *userdb.UserDB）
type FreshFightPersistence interface {
	LoadFreshFightConfig() ([]byte, error)
	SaveFreshFightConfig(data []byte) error
}

var (
	freshFightMu             sync.RWMutex
	freshFightConfig         []FreshFightLevelEntry
	freshFightByLevel        map[int][]FreshFightLevelEntry
	freshFightRewardByLevel  map[int]struct{ ItemID, PetID int } // 试炼之塔每层通关奖励（取该层最后一条配置），独立管控
	freshFightPersistence    FreshFightPersistence
)

// SetFreshFightPersistence 设置试炼之塔配置持久化实现
func SetFreshFightPersistence(p FreshFightPersistence) {
	freshFightPersistence = p
}

// LoadFreshFightConfig 从数据库加载试炼之塔配置
func LoadFreshFightConfig() {
	freshFightMu.Lock()
	defer freshFightMu.Unlock()

	freshFightConfig = nil
	freshFightByLevel = make(map[int][]FreshFightLevelEntry)
	freshFightRewardByLevel = make(map[int]struct{ ItemID, PetID int })

	if freshFightPersistence == nil {
		return
	}
	data, err := freshFightPersistence.LoadFreshFightConfig()
	if err != nil || len(data) == 0 {
		if err != nil {
			logger.Warning("[试炼之塔] 从数据库加载失败: " + err.Error())
		}
		return
	}
	var list []FreshFightLevelEntry
	if err := json.Unmarshal(data, &list); err != nil {
		logger.Warning("[试炼之塔] 配置 JSON 解析失败: " + err.Error())
		return
	}
	// 规范化：按 level, seq 排序，过滤非法行与缺省倍率
	for i := range list {
		if list[i].Level <= 0 || list[i].BossID <= 0 {
			continue
		}
		if list[i].Seq <= 0 {
			list[i].Seq = 1
		}
		if list[i].EnemyLv < 0 {
			list[i].EnemyLv = 0
		}
		if list[i].HPRatio == 0 {
			list[i].HPRatio = 100
		}
		if list[i].AtkRatio == 0 {
			list[i].AtkRatio = 100
		}
		if list[i].DefRatio == 0 {
			list[i].DefRatio = 100
		}
		if list[i].SARatio == 0 {
			list[i].SARatio = 100
		}
		if list[i].SDRatio == 0 {
			list[i].SDRatio = 100
		}
		if list[i].SPRatio == 0 {
			list[i].SPRatio = 100
		}
		freshFightConfig = append(freshFightConfig, list[i])
	}
	sort.Slice(freshFightConfig, func(i, j int) bool {
		if freshFightConfig[i].Level == freshFightConfig[j].Level {
			return freshFightConfig[i].Seq < freshFightConfig[j].Seq
		}
		return freshFightConfig[i].Level < freshFightConfig[j].Level
	})
	for _, e := range freshFightConfig {
		freshFightByLevel[e.Level] = append(freshFightByLevel[e.Level], e)
	}
	for level, ents := range freshFightByLevel {
		if len(ents) == 0 {
			continue
		}
		last := ents[len(ents)-1]
		if last.RewardItemID > 0 || last.RewardPetID > 0 {
			freshFightRewardByLevel[level] = struct{ ItemID, PetID int }{last.RewardItemID, last.RewardPetID}
		}
	}
	logger.Info("[试炼之塔] 已从数据库加载配置, 条目数=" + itoa(len(freshFightConfig)))
}

// GetFreshFightConfig 返回全部配置（供 GM 前端展示）
func GetFreshFightConfig() []FreshFightLevelEntry {
	freshFightMu.RLock()
	defer freshFightMu.RUnlock()
	out := make([]FreshFightLevelEntry, len(freshFightConfig))
	copy(out, freshFightConfig)
	return out
}

// SetFreshFightConfig 更新配置并持久化（GM 保存）
func SetFreshFightConfig(list []FreshFightLevelEntry) error {
	// 先在内存中规范化
	tmp := make([]FreshFightLevelEntry, 0, len(list))
	for i := range list {
		if list[i].Level <= 0 || list[i].BossID <= 0 {
			continue
		}
		if list[i].Seq <= 0 {
			list[i].Seq = 1
		}
		if list[i].EnemyLv < 0 {
			list[i].EnemyLv = 0
		}
		if list[i].HPRatio == 0 {
			list[i].HPRatio = 100
		}
		if list[i].AtkRatio == 0 {
			list[i].AtkRatio = 100
		}
		if list[i].DefRatio == 0 {
			list[i].DefRatio = 100
		}
		if list[i].SARatio == 0 {
			list[i].SARatio = 100
		}
		if list[i].SDRatio == 0 {
			list[i].SDRatio = 100
		}
		if list[i].SPRatio == 0 {
			list[i].SPRatio = 100
		}
		tmp = append(tmp, list[i])
	}
	sort.Slice(tmp, func(i, j int) bool {
		if tmp[i].Level == tmp[j].Level {
			return tmp[i].Seq < tmp[j].Seq
		}
		return tmp[i].Level < tmp[j].Level
	})

	freshFightMu.Lock()
	freshFightConfig = tmp
	freshFightByLevel = make(map[int][]FreshFightLevelEntry)
	freshFightRewardByLevel = make(map[int]struct{ ItemID, PetID int })
	for _, e := range freshFightConfig {
		freshFightByLevel[e.Level] = append(freshFightByLevel[e.Level], e)
	}
	for level, ents := range freshFightByLevel {
		if len(ents) == 0 {
			continue
		}
		last := ents[len(ents)-1]
		if last.RewardItemID > 0 || last.RewardPetID > 0 {
			freshFightRewardByLevel[level] = struct{ ItemID, PetID int }{last.RewardItemID, last.RewardPetID}
		}
	}
	freshFightMu.Unlock()

	if freshFightPersistence == nil {
		return nil
	}
	data, err := json.Marshal(tmp)
	if err != nil {
		return err
	}
	return freshFightPersistence.SaveFreshFightConfig(data)
}

// GetFreshFightBossIDsForLevel 获取某层的 Boss ID 列表（按顺序），供 2428/2429 使用
func GetFreshFightBossIDsForLevel(level int) []int {
	freshFightMu.RLock()
	defer freshFightMu.RUnlock()
	ents := freshFightByLevel[level]
	if len(ents) == 0 {
		return nil
	}
	res := make([]int, 0, len(ents))
	for _, e := range ents {
		res = append(res, e.BossID)
	}
	return res
}

// GetFreshFightRewardForLevel 返回试炼之塔某层通关奖励（精元/道具 ID、精灵 ID）；0 表示无。仅试炼之塔副本使用，与 SPT/暗黑/勇者之塔独立。
func GetFreshFightRewardForLevel(level int) (itemID, petID int) {
	freshFightMu.RLock()
	r := freshFightRewardByLevel[level]
	freshFightMu.RUnlock()
	return r.ItemID, r.PetID
}

// GetFreshFightEntry 返回某层第 seq 个配置（若不存在则 ok=false）
func GetFreshFightEntry(level, seq int) (FreshFightLevelEntry, bool) {
	freshFightMu.RLock()
	defer freshFightMu.RUnlock()
	ents := freshFightByLevel[level]
	for _, e := range ents {
		if e.Seq == seq {
			return e, true
		}
	}
	return FreshFightLevelEntry{}, false
}

// scaleStatsByEntry 按 GM 配置的倍率缩放敌人属性（百分比）
func scaleStatsByEntry(stats *gamepets.Stats, e FreshFightLevelEntry) {
	if stats == nil {
		return
	}
	clamp := func(v, min, max int) int {
		if v < min {
			return min
		}
		if v > max {
			return max
		}
		return v
	}
	hpRatio := clamp(e.HPRatio, 1, 1000)
	atkRatio := clamp(e.AtkRatio, 1, 1000)
	defRatio := clamp(e.DefRatio, 1, 1000)
	saRatio := clamp(e.SARatio, 1, 1000)
	sdRatio := clamp(e.SDRatio, 1, 1000)
	spRatio := clamp(e.SPRatio, 1, 1000)

	scale := func(base, pct int) int {
		if base <= 0 {
			return 1
		}
		res := base * pct / 100
		if res < 1 {
			res = 1
		}
		return res
	}

	stats.HP = scale(stats.HP, hpRatio)
	stats.MaxHP = scale(stats.MaxHP, hpRatio)
	stats.Attack = scale(stats.Attack, atkRatio)
	stats.Defence = scale(stats.Defence, defRatio)
	stats.SpAtk = scale(stats.SpAtk, saRatio)
	stats.SpDef = scale(stats.SpDef, sdRatio)
	stats.Speed = scale(stats.Speed, spRatio)
}

// itoa 简单的 int 转字符串，避免额外引入 strconv 到整个文件顶部
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := false
	if v < 0 {
		neg = true
		v = -v
	}
	buf := [20]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}


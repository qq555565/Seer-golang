package handlers

import (
	"encoding/json"
	"sort"
	"sync"

	"github.com/seer-game/golang-version/internal/core/logger"
	gamepets "github.com/seer-game/golang-version/internal/game/pets"
)

// FightLevelEntry 勇者之塔一层配置：层数、Boss 列表、敌方等级与属性倍率、通关奖励（GM 独立管控）
type FightLevelEntry struct {
	Level        int   `json:"level"`        // 层数 1~80
	BossIDs      []int `json:"bossIds"`      // 该层 Boss 精灵 ID 列表，至少 1 个
	EnemyLv      int   `json:"enemyLv"`      // 敌方等级：0=自动(10+层数)，>0=固定等级
	HPRatio      int   `json:"hpRatio"`      // HP 倍率百分比，0 或 100=原始
	AtkRatio     int   `json:"atkRatio"`     // 攻击倍率
	DefRatio     int   `json:"defRatio"`     // 防御倍率
	SARatio      int   `json:"saRatio"`      // 特攻倍率
	SDRatio      int   `json:"sdRatio"`      // 特防倍率
	SPRatio      int   `json:"spRatio"`      // 速度倍率
	RewardExp    int   `json:"rewardExp"`    // 通关本层奖励经验（加入经验池），0 表示无
	RewardCoins  int   `json:"rewardCoins"`  // 通关本层奖励赛尔豆，0 表示无
	RewardItemID int   `json:"rewardItemId"` // 通关本层奖励道具/装备 ID，0 表示无
	RewardPetID  int   `json:"rewardPetId"`  // 通关本层奖励精灵 ID，0 表示无
}

// FightLevelPersistence 勇者之塔配置持久化接口（由 main 注入 *userdb.UserDB）
type FightLevelPersistence interface {
	LoadFightLevelConfig() ([]byte, error)
	SaveFightLevelConfig(data []byte) error
}

var (
	fightLevelMu             sync.RWMutex
	fightLevelConfig         []FightLevelEntry
	fightLevelByLevel        map[int][]int
	fightLevelRewardByLevel  map[int]struct{ ItemID, PetID int }
	fightLevelEntryByLevel   map[int]FightLevelEntry // 每层完整配置，供等级/属性倍率查询
	fightLevelPersist        FightLevelPersistence
)

// SetFightLevelPersistence 设置勇者之塔配置持久化实现
func SetFightLevelPersistence(p FightLevelPersistence) {
	fightLevelPersist = p
}

// LoadFightLevelConfig 从数据库加载勇者之塔配置
func LoadFightLevelConfig() {
	fightLevelMu.Lock()
	defer fightLevelMu.Unlock()

	fightLevelConfig = nil
	fightLevelByLevel = make(map[int][]int)
	fightLevelRewardByLevel = make(map[int]struct{ ItemID, PetID int })
	fightLevelEntryByLevel = make(map[int]FightLevelEntry)

	if fightLevelPersist == nil {
		return
	}
	data, err := fightLevelPersist.LoadFightLevelConfig()
	if err != nil || len(data) == 0 {
		if err != nil {
			logger.Warning("[勇者之塔] 从数据库加载失败: " + err.Error())
		}
		return
	}
	var list []FightLevelEntry
	if err := json.Unmarshal(data, &list); err != nil {
		logger.Warning("[勇者之塔] 配置 JSON 解析失败: " + err.Error())
		return
	}
	for i := range list {
		if list[i].Level < 1 || list[i].Level > fightLevelMaxLevel {
			continue
		}
		if len(list[i].BossIDs) == 0 {
			list[i].BossIDs = []int{1}
		}
		normalizeFightLevelRatios(&list[i])
		fightLevelConfig = append(fightLevelConfig, list[i])
		fightLevelByLevel[list[i].Level] = list[i].BossIDs
		if list[i].RewardItemID > 0 || list[i].RewardPetID > 0 {
			fightLevelRewardByLevel[list[i].Level] = struct{ ItemID, PetID int }{list[i].RewardItemID, list[i].RewardPetID}
		}
		fightLevelEntryByLevel[list[i].Level] = list[i]
	}
	sort.Slice(fightLevelConfig, func(i, j int) bool {
		return fightLevelConfig[i].Level < fightLevelConfig[j].Level
	})
	logger.Info("[勇者之塔] 已从数据库加载配置, 层数条目=" + itoa(len(fightLevelByLevel)))
}

// GetFightLevelConfig 返回全部配置（供 GM 前端展示与保存）
func GetFightLevelConfig() []FightLevelEntry {
	fightLevelMu.RLock()
	defer fightLevelMu.RUnlock()
	out := make([]FightLevelEntry, len(fightLevelConfig))
	copy(out, fightLevelConfig)
	return out
}

// SetFightLevelConfig 更新配置并持久化（GM 保存）
func SetFightLevelConfig(list []FightLevelEntry) error {
	tmp := make([]FightLevelEntry, 0, len(list))
	seen := make(map[int]bool)
	for i := range list {
		if list[i].Level < 1 || list[i].Level > fightLevelMaxLevel {
			continue
		}
		if seen[list[i].Level] {
			continue
		}
		seen[list[i].Level] = true
		if len(list[i].BossIDs) == 0 {
			list[i].BossIDs = []int{1}
		}
		normalizeFightLevelRatios(&list[i])
		tmp = append(tmp, list[i])
	}
	sort.Slice(tmp, func(i, j int) bool {
		return tmp[i].Level < tmp[j].Level
	})

	fightLevelMu.Lock()
	fightLevelConfig = tmp
	fightLevelByLevel = make(map[int][]int)
	fightLevelRewardByLevel = make(map[int]struct{ ItemID, PetID int })
	fightLevelEntryByLevel = make(map[int]FightLevelEntry)
	for _, e := range fightLevelConfig {
		fightLevelByLevel[e.Level] = e.BossIDs
		if e.RewardItemID > 0 || e.RewardPetID > 0 {
			fightLevelRewardByLevel[e.Level] = struct{ ItemID, PetID int }{e.RewardItemID, e.RewardPetID}
		}
		fightLevelEntryByLevel[e.Level] = e
	}
	fightLevelMu.Unlock()

	if fightLevelPersist == nil {
		return nil
	}
	data, err := json.Marshal(tmp)
	if err != nil {
		return err
	}
	return fightLevelPersist.SaveFightLevelConfig(data)
}

// GetFightLevelBossIDsForLevel 从配置中获取勇者之塔某层的 Boss 精灵 ID 列表；未配置时返回默认 [1]
func GetFightLevelBossIDsForLevel(level int) []int {
	if level < 1 || level > fightLevelMaxLevel {
		return nil
	}
	fightLevelMu.RLock()
	ids := fightLevelByLevel[level]
	fightLevelMu.RUnlock()
	if len(ids) > 0 {
		return ids
	}
	return []int{1}
}

// GetFightLevelReward 返回勇者之塔某层通关奖励（精元/道具 ID、精灵 ID）；0 表示无。仅勇者之塔副本使用，与 SPT/暗黑独立。
func GetFightLevelReward(level int) (itemID, petID int) {
	if level < 1 || level > fightLevelMaxLevel {
		return 0, 0
	}
	fightLevelMu.RLock()
	r := fightLevelRewardByLevel[level]
	fightLevelMu.RUnlock()
	return r.ItemID, r.PetID
}

// GetFightLevelEntry 返回勇者之塔某层完整配置；未配置时 ok=false，调用方用默认等级与属性。
func GetFightLevelEntry(level int) (FightLevelEntry, bool) {
	if level < 1 || level > fightLevelMaxLevel {
		return FightLevelEntry{}, false
	}
	fightLevelMu.RLock()
	e, ok := fightLevelEntryByLevel[level]
	fightLevelMu.RUnlock()
	return e, ok
}

func normalizeFightLevelRatios(e *FightLevelEntry) {
	if e.HPRatio <= 0 {
		e.HPRatio = 100
	}
	if e.AtkRatio <= 0 {
		e.AtkRatio = 100
	}
	if e.DefRatio <= 0 {
		e.DefRatio = 100
	}
	if e.SARatio <= 0 {
		e.SARatio = 100
	}
	if e.SDRatio <= 0 {
		e.SDRatio = 100
	}
	if e.SPRatio <= 0 {
		e.SPRatio = 100
	}
}

// ScaleFightLevelStats 按勇者之塔该层配置的倍率缩放敌人属性（百分比）；未配置或 100 则不变。
func ScaleFightLevelStats(stats *gamepets.Stats, level int) {
	if stats == nil || level < 1 || level > fightLevelMaxLevel {
		return
	}
	fightLevelMu.RLock()
	e, ok := fightLevelEntryByLevel[level]
	fightLevelMu.RUnlock()
	if !ok || (e.HPRatio == 100 && e.AtkRatio == 100 && e.DefRatio == 100 && e.SARatio == 100 && e.SDRatio == 100 && e.SPRatio == 100) {
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

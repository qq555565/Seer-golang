// Package sptboss GM 可配置结构（地图-BOSS、奖励、特性）
package sptboss

import "sync"

// MapBossItem 地图 BOSS 配置项（GM 用）
type MapBossItem struct {
	MapID     int  `json:"mapId"`
	Param2    uint32 `json:"param2"`
	BossPetID int  `json:"bossPetId"`
	Level     int  `json:"level"`
	HasShield bool `json:"hasShield"`
}

// SPTBossItem SPT BOSS 配置项（首次击败奖励等，GM 用）
type SPTBossItem struct {
	SPTID        int  `json:"sptId"`
	BossPetID    int  `json:"bossPetId"`
	RewardPetID  int  `json:"rewardPetId"`
	RewardItemID int  `json:"rewardItemId"`
	Level        int  `json:"level"`
	HasShield    bool `json:"hasShield"`
}

// DamageMultItem 受到伤害倍数配置（如魔狮迪露×10）
type DamageMultItem struct {
	PetID int `json:"petId"`
	Mult  int `json:"mult"`
}

// TraitConfig BOSS 特性配置（免疫、先制等）
type TraitConfig struct {
	StatusImmune         []int            `json:"statusImmune"`         // 免疫异常状态
	StatDropImmune       []int            `json:"statDropImmune"`       // 免疫能力下降
	SameLifeDeathImmune  []int            `json:"sameLifeDeathImmune"`   // 免疫同生共死
	InfinitePP           []int            `json:"infinitePP"`           // PP 无限
	FirstStrike          []int            `json:"firstStrike"`          // 回合顺序优先
	PriorityBonus        []int            `json:"priorityBonus"`        // 先制+6
	HalfHPOneShot        []int            `json:"halfHPOneShot"`        // 半血后先制+6且秒杀
	DamageTakenMultiplier []DamageMultItem `json:"damageTakenMultiplier"` // 受到伤害倍数
}

// PuniSealLifeConfig 谱尼真身单条命配置
type PuniSealLifeConfig struct {
	LifeIndex int `json:"lifeIndex"` // 命条索引（1-6）
	HP        int `json:"hp"`        // 血量
	AutoHealThreshold int `json:"autoHealThreshold"` // 自动回满阈值（仅第六条命有效，<此值自动回满）
}

// PuniSealConfig 谱尼封印/真身配置
type PuniSealConfig struct {
	DoorIndex      int    `json:"doorIndex"`      // 门索引（1-7为封印，8为真身）
	Name           string `json:"name"`           // 名称（如"虚无"、"元素"等）
	HP             int    `json:"hp"`             // 血量（封印用，真身忽略）
	RewardItemID   int    `json:"rewardItemId"`   // 奖励碎片物品ID
	// 初始能力等级调整
	PlayerAccuracyMod int `json:"playerAccuracyMod"` // 玩家命中等级修正（如第一封印-6）
	EnemyAtkMod       int `json:"enemyAtkMod"`       // 谱尼物攻等级修正
	EnemySpAtkMod     int `json:"enemySpAtkMod"`     // 谱尼特攻等级修正
	// 特殊规则
	ElementOnlyTypes  []int `json:"elementOnlyTypes"`  // 仅允许的属性类型（如元素封印仅光系12/暗影系13）
	EnergyDamageLimit int   `json:"energyDamageLimit"` // 能量封印伤害上限（>此值触发秒杀+回满）
	LifeHealPerTurn   int   `json:"lifeHealPerTurn"`   // 生命封印每回合回复血量
	CycleHPBars       int   `json:"cycleHPBars"`       // 轮回封印血条数（2表示两管血）
	EternalDamageReduction bool `json:"eternalDamageReduction"` // 永恒封印是否对我方伤害减半
	HolyStatDropImmune bool `json:"holyStatDropImmune"`        // 圣洁封印是否免疫能力下降
	// 真身配置（仅 door=8 有效）
	TrueFormLives []PuniSealLifeConfig `json:"trueFormLives"` // 真身各条命配置
}

// FullConfig SPT BOSS 完整配置（GM 读写）
type FullConfig struct {
	MapBosses  []MapBossItem    `json:"mapBosses"`
	SPTBosses  []SPTBossItem    `json:"sptBosses"`
	Traits     TraitConfig      `json:"traits"`
	PuniSeals  []PuniSealConfig `json:"puniSeals"` // 谱尼封印和真身配置
}

var (
	gmMu               sync.RWMutex
	gmMapBossConfig    map[int]map[uint32]MapBossEntry // mapID -> param2 -> entry
	gmSPTBossByPetID   map[int]SPTBossEntry
	gmControlImmune    map[int]bool
	gmStatusImmune     map[int]bool
	gmStatDropImmune   map[int]bool
	gmSameLifeDeathImmune map[int]bool
	gmInfinitePP       map[int]bool
	gmFirstStrike      map[int]bool
	gmPriorityBonus    map[int]bool
	gmHalfHPOneShot    map[int]bool
	gmDamageTakenMult  map[int]int
	gmPuniSeals        map[int]PuniSealConfig // doorIndex -> config
)

func sliceToSet(ids []int) map[int]bool {
	m := make(map[int]bool)
	for _, id := range ids {
		if id > 0 {
			m[id] = true
		}
	}
	return m
}

// SetConfig 应用 GM 配置；nil 表示恢复内置默认
func SetConfig(cfg *FullConfig) {
	gmMu.Lock()
	defer gmMu.Unlock()
	if cfg == nil {
		gmMapBossConfig = nil
		gmSPTBossByPetID = nil
		gmControlImmune = nil
		gmStatusImmune = nil
		gmStatDropImmune = nil
		gmSameLifeDeathImmune = nil
		gmInfinitePP = nil
		gmFirstStrike = nil
		gmPriorityBonus = nil
		gmHalfHPOneShot = nil
		gmDamageTakenMult = nil
		gmPuniSeals = nil
		return
	}
	// 地图-BOSS
	gmMapBossConfig = make(map[int]map[uint32]MapBossEntry)
	for _, it := range cfg.MapBosses {
		if it.BossPetID <= 0 {
			continue
		}
		if gmMapBossConfig[it.MapID] == nil {
			gmMapBossConfig[it.MapID] = make(map[uint32]MapBossEntry)
		}
		gmMapBossConfig[it.MapID][it.Param2] = MapBossEntry{
			BossPetID:  it.BossPetID,
			Level:      it.Level,
			HasShield:  it.HasShield,
		}
	}
	// SPT 奖励
	gmSPTBossByPetID = make(map[int]SPTBossEntry)
	for _, it := range cfg.SPTBosses {
		if it.BossPetID <= 0 {
			continue
		}
		gmSPTBossByPetID[it.BossPetID] = SPTBossEntry{
			SPTID:        it.SPTID,
			BossPetID:    it.BossPetID,
			RewardPetID:  it.RewardPetID,
			RewardItemID: it.RewardItemID,
			Level:        it.Level,
			HasShield:    it.HasShield,
		}
	}
	// 控制免疫 = 所有出现在地图或 SPT 中的 BOSS
	gmControlImmune = make(map[int]bool)
	for _, byParam := range gmMapBossConfig {
		for _, e := range byParam {
			if e.BossPetID > 0 {
				gmControlImmune[e.BossPetID] = true
			}
		}
	}
	for pid := range gmSPTBossByPetID {
		gmControlImmune[pid] = true
	}
	// 特性
	gmStatusImmune = sliceToSet(cfg.Traits.StatusImmune)
	gmStatDropImmune = sliceToSet(cfg.Traits.StatDropImmune)
	gmSameLifeDeathImmune = sliceToSet(cfg.Traits.SameLifeDeathImmune)
	gmInfinitePP = sliceToSet(cfg.Traits.InfinitePP)
	gmFirstStrike = sliceToSet(cfg.Traits.FirstStrike)
	gmPriorityBonus = sliceToSet(cfg.Traits.PriorityBonus)
	gmHalfHPOneShot = sliceToSet(cfg.Traits.HalfHPOneShot)
	gmDamageTakenMult = make(map[int]int)
	for _, it := range cfg.Traits.DamageTakenMultiplier {
		if it.PetID > 0 && it.Mult > 0 {
			gmDamageTakenMult[it.PetID] = it.Mult
		}
	}
	// 谱尼封印和真身
	gmPuniSeals = make(map[int]PuniSealConfig)
	for _, seal := range cfg.PuniSeals {
		if seal.DoorIndex >= 1 && seal.DoorIndex <= 8 {
			gmPuniSeals[seal.DoorIndex] = seal
		}
	}
}

// GetPuniSealConfig 获取谱尼封印/真身配置（doorIndex 1-7为封印，8为真身）
func GetPuniSealConfig(doorIndex int) (PuniSealConfig, bool) {
	gmMu.RLock()
	defer gmMu.RUnlock()
	if gmPuniSeals != nil {
		cfg, ok := gmPuniSeals[doorIndex]
		return cfg, ok
	}
	return PuniSealConfig{}, false
}

// GetConfig 导出当前生效配置（供 GM 读取）；若未设置 GM 配置则导出内置默认
func GetConfig() FullConfig {
	gmMu.RLock()
	useGM := gmMapBossConfig != nil
	gmMu.RUnlock()
	if useGM {
		return exportGMConfig()
	}
	return exportBuiltInConfig()
}

func exportBuiltInConfig() FullConfig {
	var mapBosses []MapBossItem
	for mapID, byParam := range mapBossConfig {
		for param2, e := range byParam {
			if e.BossPetID <= 0 {
				continue
			}
			mapBosses = append(mapBosses, MapBossItem{
				MapID: mapID, Param2: param2,
				BossPetID: e.BossPetID, Level: e.Level, HasShield: e.HasShield,
			})
		}
	}
	var sptBosses []SPTBossItem
	for _, e := range sptBossByPetID {
		sptBosses = append(sptBosses, SPTBossItem{
			SPTID: e.SPTID, BossPetID: e.BossPetID,
			RewardPetID: e.RewardPetID, RewardItemID: e.RewardItemID,
			Level: e.Level, HasShield: e.HasShield,
		})
	}
	traits := TraitConfig{
		StatusImmune:          mapKeys(StatusImmuneBossIDs),
		StatDropImmune:        mapKeys(StatDropImmuneBossIDs),
		SameLifeDeathImmune:   mapKeys(SameLifeDeathImmuneBossIDs),
		InfinitePP:            mapKeys(InfinitePPBossIDs),
		FirstStrike:           mapKeys(FirstStrikeBossIDs),
		PriorityBonus:         mapKeys(PriorityBonusBossIDs),
		HalfHPOneShot:         mapKeys(HalfHPOneShotBossIDs),
		DamageTakenMultiplier: damageMultToList(DamageTakenMultiplierBossIDs),
	}
	puniSeals := exportBuiltInPuniSeals()
	return FullConfig{MapBosses: mapBosses, SPTBosses: sptBosses, Traits: traits, PuniSeals: puniSeals}
}

func exportGMConfig() FullConfig {
	gmMu.RLock()
	defer gmMu.RUnlock()
	var mapBosses []MapBossItem
	for mapID, byParam := range gmMapBossConfig {
		for param2, e := range byParam {
			if e.BossPetID <= 0 {
				continue
			}
			mapBosses = append(mapBosses, MapBossItem{
				MapID: mapID, Param2: param2,
				BossPetID: e.BossPetID, Level: e.Level, HasShield: e.HasShield,
			})
		}
	}
	var sptBosses []SPTBossItem
	for _, e := range gmSPTBossByPetID {
		sptBosses = append(sptBosses, SPTBossItem{
			SPTID: e.SPTID, BossPetID: e.BossPetID,
			RewardPetID: e.RewardPetID, RewardItemID: e.RewardItemID,
			Level: e.Level, HasShield: e.HasShield,
		})
	}
	traits := TraitConfig{
		StatusImmune:          mapKeysFromBool(gmStatusImmune),
		StatDropImmune:        mapKeysFromBool(gmStatDropImmune),
		SameLifeDeathImmune:   mapKeysFromBool(gmSameLifeDeathImmune),
		InfinitePP:            mapKeysFromBool(gmInfinitePP),
		FirstStrike:           mapKeysFromBool(gmFirstStrike),
		PriorityBonus:         mapKeysFromBool(gmPriorityBonus),
		HalfHPOneShot:         mapKeysFromBool(gmHalfHPOneShot),
		DamageTakenMultiplier: damageMultMapToList(gmDamageTakenMult),
	}
	var puniSeals []PuniSealConfig
	if gmPuniSeals != nil {
		for i := 1; i <= 8; i++ {
			if cfg, ok := gmPuniSeals[i]; ok {
				puniSeals = append(puniSeals, cfg)
			}
		}
	} else {
		puniSeals = exportBuiltInPuniSeals()
	}
	return FullConfig{MapBosses: mapBosses, SPTBosses: sptBosses, Traits: traits, PuniSeals: puniSeals}
}

func mapKeys(m map[int]bool) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func mapKeysFromBool(m map[int]bool) []int {
	if m == nil {
		return nil
	}
	return mapKeys(m)
}

func damageMultToList(m map[int]int) []DamageMultItem {
	var out []DamageMultItem
	for pid, mult := range m {
		if mult > 0 {
			out = append(out, DamageMultItem{PetID: pid, Mult: mult})
		}
	}
	return out
}

func damageMultMapToList(m map[int]int) []DamageMultItem {
	if m == nil {
		return nil
	}
	return damageMultToList(m)
}

func exportBuiltInPuniSeals() []PuniSealConfig {
	return []PuniSealConfig{
		{1, "虚无", 7000, 400651, -6, 2, 2, nil, 0, 0, 0, false, false, nil},
		{2, "元素", 8000, 400652, 0, 0, 0, []int{12, 13}, 0, 0, 0, false, false, nil},
		{3, "能量", 9000, 400653, 0, 0, 0, nil, 100, 0, 0, false, false, nil},
		{4, "生命", 10000, 400654, 0, 0, 0, nil, 0, 2000, 0, false, false, nil},
		{5, "轮回", 10000, 400655, 0, 0, 0, nil, 0, 0, 2, false, false, nil},
		{6, "永恒", 12000, 400656, 0, 2, 2, nil, 0, 0, 0, true, false, nil},
		{7, "圣洁", 16000, 400657, 0, 2, 2, nil, 0, 0, 0, false, true, nil},
		{8, "真身", 0, 400658, 0, 0, 0, nil, 0, 0, 0, false, false, []PuniSealLifeConfig{
			{1, 7000, 0},
			{2, 8000, 0},
			{3, 9000, 0},
			{4, 12000, 0},
			{5, 20000, 0},
			{6, 65000, 1000},
		}},
	}
}

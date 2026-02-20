// Package sptboss 赛尔先锋队 SPT BOSS 配置
// 对应前端 PioneerTaskModel + AchieveXMLInfo + PetBook，全地图 BOSS 精灵、首次击败奖励、成就
package sptboss

// SPTBossEntry 单个 SPT BOSS 配置
type SPTBossEntry struct {
	SPTID        int // 先锋队任务 id (1-20)
	BossPetID    int // BOSS 精灵 ID
	RewardPetID  int // 首次击败奖励精灵 ID，0 表示无奖励
	RewardItemID int // 首次击败奖励物品 ID（精元等），0 表示无物品奖励
	Level        int // BOSS 等级
	HasShield    bool // 是否有防护罩（需 2412 破除）
}

// MapBossEntry 地图上的 BOSS 配置，用于 (mapID, param2) 解析
type MapBossEntry struct {
	BossPetID  int
	Level      int
	HasShield  bool
}

// sptBossByPetID petID -> SPT 配置（用于首次击败奖励与成就）
// Level 字段按“地图 BOSS 调整表”同步更新，便于从 SPT 面板直接发起战斗时也使用统一等级。
// 精元奖励为主：仅蘑菇怪奖励1级精灵，其余 BOSS 奖励对应精元（RewardItemID）
var sptBossByPetID = map[int]SPTBossEntry{
	47:   {1, 47, 46, 0, 10, true},       // 蘑菇怪 -> 小蘑菇
	34:   {2, 34, 0, 400051, 25, false},  // 钢牙鲨 -> 黑晶矿
	42:   {3, 42, 0, 400107, 35, false},  // 里奥斯 -> 里奥斯精元
	50:   {4, 50, 0, 400101, 65, false},  // 阿克希亚 -> 阿克希亚精元
	69:   {5, 69, 0, 400102, 50, false},  // 提亚斯 -> 提亚斯精元
	70:   {6, 70, 0, 400103, 70, false},  // 雷伊 -> 雷伊精元
	88:   {7, 88, 0, 400104, 70, false},  // 纳多雷 -> 纳多雷精元
	113:  {8, 113, 0, 400105, 75, false}, // 雷纳多 -> 雷纳多精元
	132:  {9, 132, 0, 400108, 70, false}, // 尤纳斯 -> 尤纳斯精元
	187:  {10, 187, 0, 400114, 50, false}, // 魔狮迪露 -> 魔狮迪露精元
	216:  {11, 216, 0, 400118, 80, false}, // 哈莫雷特 -> 哈莫雷特精元
	264:  {12, 264, 0, 400125, 60, false}, // 奈尼芬多 -> 奈尼芬多精元
	421:  {13, 421, 0, 400139, 80, false}, // 厄尔塞拉 -> 厄尔塞拉精元
	261:  {14, 261, 0, 400126, 70, false}, // 盖亚 -> 盖亚精元（按周几+地图+条件，由 pushGaiyaRewardOrNotice 单独处理）
	274:  {15, 274, 0, 400136, 80, false}, // 塔克林 -> 塔克林精元
	391:  {16, 391, 0, 400137, 70, false}, // 塔西亚 -> 塔西亚精元
	347:  {17, 347, 0, 400127, 70, false}, // 远古鱼龙 -> 远古鱼龙精元
	393:  {18, 393, 0, 0, 75, false},      // 上古炎兽（无奖励）
	300:  {19, 300, 0, 400047, 70, false}, // 谱尼 -> 谱尼精元
	4150: {20, 4150, 0, 0, 80, false},    // 拂晓兔（无奖励）
	413:  {0, 413, 0, 0, 75, false},       // 塞维尔（龙族三巨头，成就用）
	166:  {0, 166, 166, 0, 80, false},     // 克洛斯星 BOSS 闪光波克尔 -> 奖励一只闪光波克尔
}

// bossMapAlias 任务/副本地图 ID -> 正式 BOSS 地图 ID
// 雷伊技能特训等任务会使用与正式地图同名的副本地图（如 912 塞西利亚星 super=40），
// 若不做别名则 2411 用 user.MapID=912 查不到 BOSS，导致无法正常对战阿克西亚/里奥斯
var bossMapAlias = map[int]int{
	912: 40, // 塞西利亚星任务副本 -> 正式塞西利亚星(40)，解析为阿克希亚
}

// mapBossConfig (mapID, param2) -> BOSS 配置
// 客户端 fightWithBoss(name, param2) 发送 param2，配合当前地图解析
var mapBossConfig = map[int]map[uint32]MapBossEntry{
	12:  {0: {47, 10, true}, 1: {83, 5, false}}, // 克洛斯星密林: 0=蘑菇怪 1=依依
	22:  {0: {34, 25, false}},                   // 海洋星海底(seatID22/enterID21): 钢牙鲨
	21:  {0: {34, 25, false}},                   // 海洋星深水区
	17:  {0: {42, 35, false}},                   // 火山星山洞深处: 里奥斯
	40:  {0: {50, 65, false}},                   // 塞西利亚星: 阿克希亚
	27:  {0: {69, 50, false}},                   // 云霄星最高层: 提亚斯（Lv 50）
	32:  {0: {70, 70, false}},                   // 赫尔卡星荒地: 雷伊
	106: {0: {88, 70, false}},                   // 阿尔法星岩地: 纳多雷（Lv 70）
	49:  {0: {113, 75, false}},                  // 贝塔星荒原: 雷纳多（Lv 75）
	314: {0: {132, 70, false}},                  // 拜伦号: 尤纳斯
	53:  {0: {187, 50, false}},                  // 斯诺岩洞: 魔狮迪露
	57:  {0: {216, 80, false}},                  // 尼古尔星: 哈莫雷特（fightWithBoss 默认 param2=0）
	60:  {0: {216, 80, false}},                  // 哈莫雷特
	325: {0: {264, 60, false}},                  // 奈尼芬多（Lv 60）
	61:  {0: {421, 80, false}},                  // 光之迷城: 厄尔塞拉（Lv 80）
	348: {0: {274, 80, false}, 1: {391, 70, false}, 2: {216, 80, false}, 3: {413, 75, false}}, // 塔克林/塔西亚/哈莫雷特/塞维尔（塔克林 Lv 80）
	59:  {0: {347, 70, false}},                    // 远古鱼龙
	16:  {0: {393, 75, false}},                    // 上古炎兽
	10:  {0: {166, 80, false}},                    // 闪光波克尔
	419: {0: {261, 70, false}},                    // 暗影峭壁: 盖亚（Lv 70），PetBook Foundin=暗影峭壁 mapID=419
	// 盖亚三地图（按周几出现，对应不同挑战条件）：火山星15=两回合内击败，露西欧星54=致命一击击败，双子阿尔法星105=十回合后击败
	15:  {0: {261, 70, false}},                    // 火山星: 盖亚（周一、周五）
	54:  {0: {261, 70, false}},                    // 露西欧星: 盖亚（周二、周四、周日）
	105: {0: {261, 70, false}},                   // 双子阿尔法星: 盖亚（周三、周六）
}

// StatusImmuneBossIDs 免疫所有异常状态（烧伤/冻伤/睡眠/麻痹/中毒/畏缩等）的 BOSS 精灵 ID
// 雷伊、哈莫雷特、奈尼芬多、盖亚
var StatusImmuneBossIDs = map[int]bool{
	70:  true,  // 雷伊
	216: true,  // 哈莫雷特
	264: true,  // 奈尼芬多
	261: true,  // 盖亚
}

// ControlImmuneBossIDs 免疫控制类异常（睡眠/麻痹/畏缩等）的 BOSS，包含所有 SPT/地图 BOSS
var ControlImmuneBossIDs = func() map[int]bool {
	m := make(map[int]bool)
	for id := range sptBossByPetID {
		m[id] = true
	}
	for _, byParam := range mapBossConfig {
		for _, e := range byParam {
			if e.BossPetID != 0 {
				m[e.BossPetID] = true
			}
		}
	}
	return m
}()

// IsControlImmune 该精灵 ID 是否为免疫控制类异常的 BOSS（所有 BOSS 均免疫）
func IsControlImmune(petID int) bool {
	gmMu.RLock()
	if gmControlImmune != nil {
		ok := gmControlImmune[petID]
		gmMu.RUnlock()
		return ok
	}
	gmMu.RUnlock()
	return ControlImmuneBossIDs[petID]
}

// IsStatusImmune 该精灵 ID 是否为免疫异常状态的 BOSS
func IsStatusImmune(petID int) bool {
	gmMu.RLock()
	if gmStatusImmune != nil {
		ok := gmStatusImmune[petID]
		gmMu.RUnlock()
		return ok
	}
	gmMu.RUnlock()
	return StatusImmuneBossIDs[petID]
}

// StatDropImmuneBossIDs 免疫能力下降的 BOSS（不会出现负向能力等级，但强化可被清除或降到 0）
// 纳多雷、雷纳多、尤纳斯、哈莫雷特、盖亚、塔克林、塔西亚
var StatDropImmuneBossIDs = map[int]bool{
	88:  true,  // 纳多雷
	113: true,  // 雷纳多
	132: true,  // 尤纳斯
	216: true,  // 哈莫雷特
	261: true,  // 盖亚
	274: true,  // 塔克林
	391: true,  // 塔西亚
}

// IsStatDropImmune 该精灵 ID 是否为免疫能力下降的 BOSS（能力等级不低于 0，强化可被清除）
func IsStatDropImmune(petID int) bool {
	gmMu.RLock()
	if gmStatDropImmune != nil {
		ok := gmStatDropImmune[petID]
		gmMu.RUnlock()
		return ok
	}
	gmMu.RUnlock()
	return StatDropImmuneBossIDs[petID]
}

// SameLifeDeathImmuneBossIDs 免疫技能「同生共死」效果的 BOSS 精灵 ID
// 尤纳斯、哈莫雷特、盖亚、塔克林、塔西亚、雷伊
var SameLifeDeathImmuneBossIDs = map[int]bool{
	132: true, // 尤纳斯
	216: true, // 哈莫雷特
	261: true, // 盖亚
	274: true, // 塔克林
	391: true, // 塔西亚
	70:  true, // 雷伊
}

// IsSameLifeDeathImmune 该精灵 ID 是否为免疫同生共死效果的 BOSS
func IsSameLifeDeathImmune(petID int) bool {
	gmMu.RLock()
	if gmSameLifeDeathImmune != nil {
		ok := gmSameLifeDeathImmune[petID]
		gmMu.RUnlock()
		return ok
	}
	gmMu.RUnlock()
	return SameLifeDeathImmuneBossIDs[petID]
}

// InfinitePPBossIDs 所有技能 PP 无限的 BOSS（雷伊、魔狮迪露、奈尼芬多、盖亚、尤纳斯）
var InfinitePPBossIDs = map[int]bool{
	70:  true,  // 雷伊
	187: true,  // 魔狮迪露
	264: true,  // 奈尼芬多
	261: true,  // 盖亚
	132: true,  // 尤纳斯
}

// IsInfinitePPBoss 该精灵 ID 是否为技能 PP 无限的 BOSS
func IsInfinitePPBoss(petID int) bool {
	gmMu.RLock()
	if gmInfinitePP != nil {
		ok := gmInfinitePP[petID]
		gmMu.RUnlock()
		return ok
	}
	gmMu.RUnlock()
	return InfinitePPBossIDs[petID]
}

// FirstStrikeBossIDs 回合开始我方已 0 HP 时 2505 包顺序仍按“敌方先”的 BOSS（与先制+6 为同一批：盖亚、雷伊）
var FirstStrikeBossIDs = map[int]bool{
	261: true, // 盖亚
	70:  true, // 雷伊
}

// IsFirstStrikeBoss 该精灵 ID 是否在 FirstStrikeBossIDs 中（用于回合初我方已死时的 2505 顺序）
func IsFirstStrikeBoss(petID int) bool {
	gmMu.RLock()
	if gmFirstStrike != nil {
		ok := gmFirstStrike[petID]
		gmMu.RUnlock()
		return ok
	}
	gmMu.RUnlock()
	return FirstStrikeBossIDs[petID]
}

// PriorityBonusBossIDs 所有技能先制+6 的 BOSS（雷伊、盖亚）；先手由速度+技能先制正常比较决定
var PriorityBonusBossIDs = map[int]bool{
	70:  true, // 雷伊
	261: true, // 盖亚
}

// GetPriorityBonus 该 BOSS 技能先制加成（雷伊/盖亚 +6，其余 0）
func GetPriorityBonus(petID int) int {
	gmMu.RLock()
	if gmPriorityBonus != nil {
		if gmPriorityBonus[petID] {
			gmMu.RUnlock()
			return 6
		}
		gmMu.RUnlock()
		return 0
	}
	gmMu.RUnlock()
	if PriorityBonusBossIDs[petID] {
		return 6
	}
	return 0
}

// HalfHPOneShotBossIDs 体力低于一半时：先制+6，且任意技能（属性/攻击）必定秒杀我方当前精灵的 BOSS
var HalfHPOneShotBossIDs = map[int]bool{
	187: true, // 魔狮迪露
}

// IsHalfHPOneShotBoss 该精灵 ID 是否为“半血后先制+6且秒杀”的 BOSS
func IsHalfHPOneShotBoss(petID int) bool {
	gmMu.RLock()
	if gmHalfHPOneShot != nil {
		ok := gmHalfHPOneShot[petID]
		gmMu.RUnlock()
		return ok
	}
	gmMu.RUnlock()
	return HalfHPOneShotBossIDs[petID]
}

// GetPriorityBonusWithHP 考虑当前体力的先制加成：雷伊/盖亚 恒+6；魔狮迪露 体力低于一半时 +6
func GetPriorityBonusWithHP(petID int, currentHP, maxHP uint32) int {
	if GetPriorityBonus(petID) == 6 {
		return 6
	}
	if IsHalfHPOneShotBoss(petID) && maxHP > 0 && currentHP*2 < maxHP {
		return 6
	}
	return 0
}

// DamageTakenMultiplierBossIDs 受到我方攻击伤害（非异常状态伤害）乘 N 倍的 BOSS；N 由 GetDamageTakenMultiplier 返回
var DamageTakenMultiplierBossIDs = map[int]int{
	187: 10, // 魔狮迪露：受到的攻击伤害 ×10
}

// GetDamageTakenMultiplier 该 BOSS 受到攻击伤害的倍数（1=不变，10=十倍）；仅对攻击技能伤害有效，异常状态伤害不乘
func GetDamageTakenMultiplier(petID int) int {
	gmMu.RLock()
	if gmDamageTakenMult != nil {
		if n := gmDamageTakenMult[petID]; n > 0 {
			gmMu.RUnlock()
			return n
		}
		gmMu.RUnlock()
		return 1
	}
	gmMu.RUnlock()
	if n, ok := DamageTakenMultiplierBossIDs[petID]; ok && n > 0 {
		return n
	}
	return 1
}

// 属性类型（与 battle/typeChart、技能 Type 一致）：1=草 2=水 3=火
const (
	TypeGrass = 1
	TypeWater = 2
	TypeFire  = 3
)

// HaMoLeiTeRequiredType 哈莫雷特(216) 顺序破防：必须始终按 水系(2)→火系(3)→草系(1) 循环命中才能受伤，非当前所需属性伤害为 0
func HaMoLeiTeRequiredType(phase int) int {
	switch phase % 3 {
	case 0:
		return TypeWater // 水系
	case 1:
		return TypeFire  // 火系
	case 2:
		return TypeGrass // 草系
	default:
		return TypeWater
	}
}

// IsHaMoLeiTeOrderBoss 该精灵 ID 是否为哈莫雷特顺序破防 BOSS
func IsHaMoLeiTeOrderBoss(petID int) bool {
	return petID == 216
}

// 尤纳斯(132) 规则：贯穿水枪破防 → 保留 1 血 → 仅里奥斯幻影可击杀
const (
	SkillIDPiercingWater = 10323 // 贯穿水枪
	SkillIDPhantom       = 10100 // 幻影（里奥斯）
	PetIDLiAoS           = 42    // 里奥斯
)

// IsYouNaSiBoss 该精灵 ID 是否为尤纳斯（适用贯穿水枪/幻影击杀规则）
func IsYouNaSiBoss(petID int) bool {
	return petID == 132
}

// GetByPetID 根据 BOSS 精灵 ID 获取 SPT 配置
func GetByPetID(bossPetID int) (SPTBossEntry, bool) {
	gmMu.RLock()
	if gmSPTBossByPetID != nil {
		e, ok := gmSPTBossByPetID[bossPetID]
		gmMu.RUnlock()
		return e, ok
	}
	gmMu.RUnlock()
	e, ok := sptBossByPetID[bossPetID]
	return e, ok
}

// GetByMapAndParam 根据地图 ID 和 param2 获取 BOSS 配置
// 若当前地图为任务副本地图（如 912），会先解析为正式 BOSS 地图（40）再查表，避免与雷伊技能特训等任务冲突
func GetByMapAndParam(mapID int, param2 uint32) (MapBossEntry, bool) {
	if canonical, ok := bossMapAlias[mapID]; ok {
		mapID = canonical
	}
	gmMu.RLock()
	if gmMapBossConfig != nil {
		m, ok := gmMapBossConfig[mapID]
		gmMu.RUnlock()
		if !ok {
			return MapBossEntry{}, false
		}
		e, ok := m[param2]
		if !ok || e.BossPetID == 0 {
			return MapBossEntry{}, false
		}
		return e, true
	}
	gmMu.RUnlock()
	m, ok := mapBossConfig[mapID]
	if !ok {
		return MapBossEntry{}, false
	}
	e, ok := m[param2]
	if !ok || e.BossPetID == 0 {
		return MapBossEntry{}, false
	}
	return e, true
}

// GetMapIDsWithBoss 返回有 MAP_BOSS 的地图 ID 列表（用于 buildMapBossList）
func GetMapIDsWithBoss() []int {
	gmMu.RLock()
	if gmMapBossConfig != nil {
		ids := make([]int, 0, len(gmMapBossConfig))
		for id := range gmMapBossConfig {
			ids = append(ids, id)
		}
		gmMu.RUnlock()
		return ids
	}
	gmMu.RUnlock()
	ids := make([]int, 0, len(mapBossConfig))
	for id := range mapBossConfig {
		ids = append(ids, id)
	}
	return ids
}

// HasShield 该地图+region 的 BOSS 是否有防护罩（会先按 bossMapAlias 解析副本地图）
func HasShield(mapID int, region uint32) bool {
	if canonical, ok := bossMapAlias[mapID]; ok {
		mapID = canonical
	}
	gmMu.RLock()
	if gmMapBossConfig != nil {
		m, ok := gmMapBossConfig[mapID]
		gmMu.RUnlock()
		if !ok {
			return false
		}
		e, ok := m[region]
		return ok && e.HasShield
	}
	gmMu.RUnlock()
	m, ok := mapBossConfig[mapID]
	if !ok {
		return false
	}
	e, ok := m[region]
	return ok && e.HasShield
}

// BuildBossAchievement 从 DefeatedSPTBossIds 构建 bossAchievement 200 字节
// 前端 UserInfo.setForLoginInfo/setForMoreInfo 读取 200 个 byte 为 Boolean
// PioneerTaskModel: bossAchievement[id-1] 对应 SPT id 1..20
func BuildBossAchievement(defeatedSPTBossIds []int) []byte {
	out := make([]byte, 200)
	source := sptBossByPetID
	gmMu.RLock()
	if gmSPTBossByPetID != nil {
		source = gmSPTBossByPetID
	}
	gmMu.RUnlock()
	petIDToSPTIndex := make(map[int]int)
	for pid, e := range source {
		if e.SPTID >= 1 && e.SPTID <= 200 {
			petIDToSPTIndex[pid] = e.SPTID - 1
		}
	}
	for _, pid := range defeatedSPTBossIds {
		if idx, ok := petIDToSPTIndex[pid]; ok && idx < 200 {
			out[idx] = 1
		}
	}
	return out
}

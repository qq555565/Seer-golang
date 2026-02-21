package gameserver

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/seer-game/golang-version/internal/core/logger"
	"github.com/seer-game/golang-version/internal/core/packet"
	"github.com/seer-game/golang-version/internal/core/userdb"
	"github.com/seer-game/golang-version/internal/game/mapogres"
)

// PublicIP 当前对外暴露给客户端的服务器 IP（用于服务器列表、资源地址等）。
// 默认 127.0.0.1，可在 main 中通过 SetPublicIP 在启动时修改。
var PublicIP = "127.0.0.1"

// SetPublicIP 在启动时设置对外暴露的服务器 IP。传入空字符串则忽略，沿用默认值。
func SetPublicIP(ip string) {
	if ip == "" {
		return
	}
	PublicIP = ip
}

const flashPolicyRequest = "<policy-file-request/>"

// ClientData 客户端数据
type ClientData struct {
	Socket         net.Conn
	Buffer         []byte
	UserID         int64
	Session        string
	SeqID          int32
	HeartbeatTimer *time.Timer
	LoggedIn       bool
}

// DarkPortalDoorInfo 暗黑武斗场门信息
type DarkPortalDoorInfo struct {
	DoorIndex  uint32 // 门索引（0-10）
	SubIndex   uint32 // 子关卡索引（0表示第一关，如3-1）
}

// BattleState 战斗状态
// 与 Lua fight_handlers 对齐：支持 status(20) 与 battleLv(6) 用于技能附加效果（烧伤、属性升降等）
type BattleState struct {
	EnemyHP     uint32
	EnemyMaxHP  uint32
	PlayerHP    uint32
	PlayerMaxHP uint32
	EnemyID     int
	EnemyLevel  int
	// ActivePetIndex 当前出战精灵在 user.Pets 切片中的下标（0 开始），用于 2405 伤害计算/经验结算等
	// 默认 0；切换精灵(2407) 时更新，但不改变 user.Pets 本身的顺序，这样战斗结束后背包首发仍然与客户端一致。
	ActivePetIndex int
	// TotalPlayerPets 本场战斗开始时玩家背包中可用精灵总数（用于判断“是否还有后备精灵”）
	TotalPlayerPets int
	// DeadPlayerPets 当前已被击败的精灵数量（仅在 PlayerHP 从 >0 变为 0 时累加）
	DeadPlayerPets int
	IsActive       bool
	OpponentUserID int64 // PvP 时对方 UID，0 表示 PvE
	// 技能效果：异常状态 status[0..19]，能力等级 battleLv[0..5]=攻击/防御/特攻/特防/速度/命中（-6~+6）
	PlayerStatus   [20]byte
	EnemyStatus    [20]byte
	PlayerBattleLv [6]int8
	EnemyBattleLv  [6]int8
	// 战斗中的技能与 PP（最多 4 个，与 2503/2504 的技能槽位对应）
	PlayerSkillIDs [4]uint32
	PlayerSkillPP  [4]byte
	EnemySkillIDs  [4]uint32
	EnemySkillPP   [4]byte
	EnemyPPInfinite bool
	// 额外的“回合类固定伤害”效果：由技能配置触发，按回合开始结算
	PlayerFixedDotDamage uint32
	PlayerFixedDotRounds byte
	EnemyFixedDotDamage  uint32
	EnemyFixedDotRounds  byte
	// 暴击强化：下 N 回合内攻击技能必定致命一击（由部分技能效果设置，如 SideEffect=58）
	PlayerCritBuffRounds byte
	EnemyCritBuffRounds  byte
	// 多回合 BUFF/护盾（41 火抗 42 电伤×2 44 特防减半 46 挡n次 47 免疫能力下降 48 免疫异常 49 吸收n点伤害 50 物防减半）
	PlayerFireResistRounds   byte   // 41: 本方受到火系伤害减半
	EnemyFireResistRounds    byte
	PlayerElectricBoostRounds byte  // 42: 本方电系技能伤害×2
	EnemyElectricBoostRounds  byte
	PlayerSpDefHalfRounds    byte   // 44: 本方受到特殊攻击伤害减半
	EnemySpDefHalfRounds     byte
	PlayerBlockCount        byte   // 46: 完全抵挡 n 次攻击（每次抵挡后减 1）
	EnemyBlockCount         byte
	PlayerImmuneStatDropRounds byte // 47: 免疫能力下降
	EnemyImmuneStatDropRounds  byte
	PlayerImmuneStatusRounds byte  // 48: 免疫异常状态
	EnemyImmuneStatusRounds  byte
	PlayerShieldPoints      uint32 // 49: 吸收伤害点数（先扣护盾再扣血）
	EnemyShieldPoints       uint32
	PlayerPhysDefHalfRounds byte   // 50: 本方受到物理攻击伤害减半
	EnemyPhysDefHalfRounds  byte
	// 扩展多回合效果：45 防御同对手 51 攻击同对手 57 每回合回血 65 某属性威力倍率 68 致死留1血
	PlayerCopyDefRounds   byte   // 45: 本方防御力与对手相同
	EnemyCopyDefRounds    byte
	PlayerCopyAtkRounds   byte   // 51: 本方攻击力与对手相同
	EnemyCopyAtkRounds    byte
	PlayerRegenRounds     byte   // 57: 剩余回合数
	PlayerRegenDivisor    byte   // 57: 每回合恢复 maxHP/m
	EnemyRegenRounds      byte
	EnemyRegenDivisor     byte
	// 77 - n 回合内每次使用技能恢复 m 点体力
	PlayerRegenPerUseRounds byte
	PlayerRegenPerUseAmount uint32
	EnemyRegenPerUseRounds  byte
	EnemyRegenPerUseAmount  uint32
	// 78 - n 回合内物理攻击对自身必定 miss
	PlayerPhysMissRounds byte
	EnemyPhysMissRounds  byte
	// 83 - 自身雄性下两回合必定先手；雌性下两回合必定暴击
	PlayerMaleFirstStrikeRounds  byte   // 雄性：先制加成
	PlayerFemaleCritRounds       byte   // 雌性：必定暴击
	EnemyMaleFirstStrikeRounds   byte
	EnemyFemaleCritRounds        byte
	// 84 - n 回合内受到物理攻击时 m% 几率将对手麻痹
	PlayerParalyzeOnPhysHitRounds byte
	PlayerParalyzeOnPhysHitChance byte
	EnemyParalyzeOnPhysHitRounds  byte
	EnemyParalyzeOnPhysHitChance  byte
	// 86/106 - n 回合内属性（特殊）攻击对自身必定 miss
	PlayerSpecialMissRounds byte
	EnemySpecialMissRounds byte
	// 89 - n 回合内每次造成伤害的 1/m 恢复体力
	PlayerLifestealRounds   byte
	PlayerLifestealDivisor  byte
	EnemyLifestealRounds    byte
	EnemyLifestealDivisor   byte
	// 92 - n 回合内受到物理攻击时 m% 几率将对手冻伤
	PlayerFreezeOnPhysHitRounds byte
	PlayerFreezeOnPhysHitChance byte
	EnemyFreezeOnPhysHitRounds  byte
	EnemyFreezeOnPhysHitChance  byte
	// 98 - n 回合内对雄性精灵的伤害为 m 倍
	PlayerMaleDamageMultRounds byte
	PlayerMaleDamageMult       byte
	EnemyMaleDamageMultRounds  byte
	EnemyMaleDamageMult        byte
	// 104 - n 回合内每次直接攻击 m% 几率附带衰弱（随机能力-1）
	PlayerWeaknessOnHitRounds  byte
	PlayerWeaknessOnHitChance  byte
	EnemyWeaknessOnHitRounds   byte
	EnemyWeaknessOnHitChance   byte
	// 108 - n 回合内受到物理攻击时 m% 几率将对手烧伤
	PlayerBurnOnPhysHitRounds  byte
	PlayerBurnOnPhysHitChance  byte
	EnemyBurnOnPhysHitRounds   byte
	EnemyBurnOnPhysHitChance   byte
	// 109 - n 回合内造成伤害时 m% 几率令对手冻伤
	PlayerFreezeOnDealDamageRounds  byte
	PlayerFreezeOnDealDamageChance  byte
	EnemyFreezeOnDealDamageRounds   byte
	EnemyFreezeOnDealDamageChance   byte
	// 463 - n 回合内每回合所受的伤害减少 m 点
	PlayerDamageReducePerRoundRounds  byte
	PlayerDamageReducePerRoundAmount  uint32
	EnemyDamageReducePerRoundRounds   byte
	EnemyDamageReducePerRoundAmount   uint32
	// 402/405 - 后/先出手时额外附加 n 点固定伤害（每回合生效，不需要回合计数）
	// 186 - 后出手时 m% 自身 stat 提升 n 级（每回合生效，不需要回合计数）
	// 148/147/122/173 - 先/后出手时对手能力/状态变化（每回合生效，不需要回合计数）
	// 461 - 若自身体力低于 1/m 则下回合起必定暴击（复用 PlayerCritBuffRounds）
	// 475 - 若伤害不足 m 则下 n 回合必定暴击（复用 PlayerCritBuffRounds）
	// 179 - 属性相同时威力提升 n（每回合生效，不需要回合计数）
	// 193 - 若对手处于 XX 状态则必定暴击（每回合生效，不需要回合计数）
	// 195/494 - 无视对手能力提升（每回合生效，不需要回合计数）
	// 468 - 若自身能力下降则威力翻倍并解除（每回合生效，不需要回合计数）
	PlayerElemPowerRounds byte   // 65: 剩余回合数
	PlayerElemPowerMult   byte   // 65: 威力倍数（实际为倍率，如 10 表示 10 倍）
	PlayerElemPowerType   byte   // 65: 属性类型
	EnemyElemPowerRounds  byte
	EnemyElemPowerMult    byte
	EnemyElemPowerType    byte
	PlayerEndureRounds    byte   // 68: 受到致死攻击时留 1 血
	EnemyEndureRounds      byte
	// 52 先手miss 53 己方伤害m倍 54 对方伤害1/m 55 属性反转 56 属性相同 62 镇魂歌
	PlayerEvasionRounds     byte   // 52: 本方先手时对方技能 miss
	EnemyEvasionRounds      byte
	PlayerDamageMultRounds  byte   // 53: 剩余回合数
	PlayerDamageMult       byte   // 53: 伤害倍数 m
	EnemyDamageMultRounds   byte
	EnemyDamageMult         byte
	PlayerDamageReductRounds byte  // 54: 对方打我方伤害 1/m
	PlayerDamageReduct      byte
	EnemyDamageReductRounds  byte
	EnemyDamageReduct       byte
	PlayerTypeSwapRounds    byte   // 55: 属性反转
	EnemyTypeSwapRounds     byte
	PlayerTypeCopyRounds    byte   // 56: 属性与对方相同
	EnemyTypeCopyRounds     byte
	PlayerDestinyBondRounds byte   // 62: 镇魂歌，n 回合后若己方存活则对方死亡
	EnemyDestinyBondRounds  byte
	// 59 - 牺牲强化下一只：当前精灵被击败时，下一只上场的精灵获得能力强化
	PlayerSacrificeBuffActive bool   // 59: 是否激活牺牲强化（当前精灵被击败时触发）
	PlayerSacrificeBuffStats  [6]int8 // 59: 强化能力等级（攻击/防御/特攻/特防/速度/命中）
	EnemySacrificeBuffActive  bool
	EnemySacrificeBuffStats   [6]int8
	// 66 - 击败回血：当次攻击击败对方时恢复自身最大体力的1/n
	PlayerKillHealDivisor     byte   // 66: 击败回血的除数 n
	EnemyKillHealDivisor      byte
	// 67 - 击败减对方下只最大HP：当次攻击击败对方时减少对方下次出战精灵的最大体力1/n
	PlayerKillReduceMaxHpDivisor byte // 67: 减少最大HP的除数 n
	EnemyKillReduceMaxHpDivisor  byte
	// 69 - 药剂反噬：下n回合对手使用体力药剂时效果变成减少相应的体力
	PlayerPotionReverseRounds byte   // 69: 剩余回合数
	EnemyPotionReverseRounds  byte
	// 71 - 牺牲暴击：自己牺牲(体力降到0), 使下一只出战精灵在前两回合内必定致命一击
	PlayerSacrificeCritActive bool   // 71: 是否激活牺牲暴击
	EnemySacrificeCritActive  bool
	// 72 - Miss死亡：如果此回合miss，则立即死亡
	PlayerMissDeathActive bool   // 72: 是否激活Miss死亡
	EnemyMissDeathActive  bool
	// 73 - 先手反弹：如果先出手，则受攻击时反弹200%的伤害给对手，持续n回合
	PlayerFirstStrikeReflectRounds byte   // 73: 剩余回合数
	PlayerFirstStrikeReflectActive bool   // 73: 是否激活（本回合先手时激活）
	EnemyFirstStrikeReflectRounds  byte
	EnemyFirstStrikeReflectActive  bool
	// 508 - 下回合所受伤害减少 m 点（魂之再生等）；生效一次后清零
	PlayerNextTurnDamageReduce uint32
	EnemyNextTurnDamageReduce  uint32
	// 81 - 下 n 回合自身攻击技能必定命中
	PlayerMustHitRounds byte
	EnemyMustHitRounds  byte
	// 1635 - 誓言之约：k 回合后恢复全部体力；倒计时到 0 时回满
	PlayerDelayedFullHealRounds byte
	EnemyDelayedFullHealRounds  byte
	// 439 - 若自身处于能力下降或异常则对手每回合受到 m 点固定伤害
	PlayerDealFixedDotWhenWeakRounds  byte
	PlayerDealFixedDotWhenWeakDamage  uint32
	EnemyDealFixedDotWhenWeakRounds   byte
	EnemyDealFixedDotWhenWeakDamage   uint32
	// 448 - n 回合内每回合对手全能力降低 stages 级
	EnemyAllStatDropRounds  byte
	EnemyAllStatDropStages  int8
	PlayerAllStatDropRounds byte
	PlayerAllStatDropStages int8
	// 478 - n 回合内对手使用的属性技能(Category=4)无效
	EnemyStatusSkillInvalidRounds  byte
	PlayerStatusSkillInvalidRounds byte
	// 545 - n 回合内若受到伤害高于 m 则对手获得效果 type（如能力下降/异常）
	PlayerReflectStatusWhenHitRounds    byte
	PlayerReflectStatusWhenHitThreshold uint32
	PlayerReflectStatusWhenHitType      byte
	EnemyReflectStatusWhenHitRounds     byte
	EnemyReflectStatusWhenHitThreshold uint32
	EnemyReflectStatusWhenHitType      byte
	// 21 - m~n 回合每回合反弹对手伤害的 1/k
	PlayerReflectDamageRounds  byte
	PlayerReflectDamageDivisor  byte   // k
	EnemyReflectDamageRounds    byte
	EnemyReflectDamageDivisor   byte
	// 32 - n 回合暴击率增加 1/16（与 58 必暴击分开）
	PlayerCritRateBonusRounds byte
	EnemyCritRateBonusRounds  byte
	// 454 - 当自身血量少于 1/n 时先制 +m
	PlayerPriorityBonusWhenLowHPRounds   byte
	PlayerPriorityBonusWhenLowHPDivisor  byte   // n，如 3 表示 1/3 血
	PlayerPriorityBonusWhenLowHPBonus    int    // m
	EnemyPriorityBonusWhenLowHPRounds   byte
	EnemyPriorityBonusWhenLowHPDivisor  byte
	EnemyPriorityBonusWhenLowHPBonus    int
	// 482 - 本回合 m% 几率先制 +n（每回合使用技能时掷骰）
	PlayerPriorityBonusChance  byte   // m%
	PlayerPriorityBonusAmount   int    // n
	EnemyPriorityBonusChance    byte
	EnemyPriorityBonusAmount   int
	// 488 - 对手体力小于 threshold 时伤害增加 percent%（在伤害计算时用，不存回合）
	PlayerDamageBoostWhenEnemyLowThreshold uint32
	PlayerDamageBoostWhenEnemyLowPercent   byte
	// 盖亚挑战条件判定：战斗发生时的地图 ID；回合数（每次 2405 使用技能+1）；最后一击是否致命一击
	BattleMapID    int
	RoundCount     int
	LastHitWasCrit bool
	// 谱尼七封印/真身：记录由 2411 传入的门索引（1~7 为七封印，8 为真身，0 为普通战斗）
	PuniDoorIndex int
	// 谱尼专用扩展状态
	// 元素封印：标记是否已通过异常状态“破解”（异常DOT已经可以正常削血，这里只做标记留待后续扩展）
	PuniElementUnlocked bool
	// 能量封印：本回合玩家对谱尼造成的伤害（用于 >100 触发秒杀+回满）
	PuniEnergyDamageThisTurn uint32
	// 生命封印：用于统计单击伤害是否超过阈值（>2000 一击破封）
	PuniLifeLastHitDamage uint32
	// 轮回封印：当前血条索引（1 或 2），进入战斗时为 1
	PuniCycleHPBar int
	// 永恒封印：占位字段，后续可用于记录自定义防御倍率
	PuniEternalActive bool
	// 圣洁封印：占位字段，后续可用于记录自定义免疫效果开关
	PuniHolyActive bool
	// 真身战：当前命条（1~6），以及第六条命是否已经触发过“低于阈值自动回满”
	PuniTrueFormLifeIndex int
	PuniTrueFormLastLifeHealed bool
	// 哈莫雷特(216) 顺序破防：始终循环 0=需水系(2)，1=需火系(3)，2=需草系(1)，非当前属性伤害为 0
	HaMoLeiTePhase int
	// 尤纳斯(132)：0=未受贯穿水枪，所受攻击伤害为 0；1=已受贯穿水枪，正常受伤但保留 1 血，仅里奥斯幻影可击杀
	YouNaSiPhase int
	// 勇者之塔：当前挑战的层数（1~80），0 表示非勇者之塔战斗；战斗结束时若玩家获胜则更新 CurStage/MaxStage
	TowerLevel int
	// TowerBossIndex 当前层第几只 Boss（0/1/2），用于 3 只顺序上场
	TowerBossIndex int
	// 试炼之塔：当前挑战的层数，0 表示非试炼之塔；多只 Boss 时与 Tower 相同协议（2503 含全部 catchTime=0,1,2...，切换只发 2504+2505）
	FreshLevel int
	// FreshBossIndex 试炼之塔当前层第几只 Boss（0-based）
	FreshBossIndex int
	// IsDarkPortalBattle 本场战斗是否由暗黑武斗场(2425)发起，仅此时发放暗黑精元奖励
	IsDarkPortalBattle bool
	// IsBossChallenge 本场是否为「对应 BOSS 挑战」（2411/2421 发起），仅此时发放该 BOSS 的 SPT 精元/精灵奖励；勇者之塔/试炼之塔/2408 野怪不设
	IsBossChallenge bool
	// 116 - n 回合内每次防御（受到攻击）造成伤害的 1/5 恢复自身体力
	PlayerDefendHealRounds byte
	EnemyDefendHealRounds  byte
	// 117 - n 回合内每次防御（受到攻击）m% 概率使对手疲惫 1~3 回合
	PlayerDefendFatigueRounds byte
	PlayerDefendFatigueChance byte
	EnemyDefendFatigueRounds  byte
	EnemyDefendFatigueChance  byte
	// 125 - n 回合内每回合被攻击时减少受到的伤害上限 m
	PlayerDamageCapRounds byte
	PlayerDamageCap       uint32
	EnemyDamageCapRounds  byte
	EnemyDamageCap        uint32
	// 126 - n 回合内每回合自身攻击和速度 +m 级（回合末结算）
	PlayerSpeedBoostRounds byte
	PlayerSpeedBoostStages int8
	EnemySpeedBoostRounds  byte
	EnemySpeedBoostStages  int8
	// 123 - n 回合内受到任何伤害时自身 XX 提高 m 级
	PlayerHurtStatBoostRounds  byte
	PlayerHurtStatBoostStat    byte
	PlayerHurtStatBoostStages  int8
	EnemyHurtStatBoostRounds   byte
	EnemyHurtStatBoostStat     byte
	EnemyHurtStatBoostStages   int8
	// 128 - n 回合内接受的物理伤害转化为体力恢复
	PlayerPhysDmgToHealRounds byte
	EnemyPhysDmgToHealRounds  byte
	// 185 - 击败处于 XX 状态的对手时，下一只出场的对手也进入 XX 状态（0=未触发）
	PlayerTransferStatusToNextEnemy byte
	// 127 - n% 概率 m 回合内受到伤害减半
	PlayerDamageHalfRounds  byte
	EnemyDamageHalfRounds   byte
	// 144 - 消耗自己所有体力，使下一只出战的精灵 n 回合免疫异常
	PlayerSacrificeImmuneStatusRounds byte
	EnemySacrificeImmuneStatusRounds  byte
	// 146 - n 回合内受到物理攻击时 m% 使对方中毒
	PlayerPoisonOnPhysHitRounds  byte
	PlayerPoisonOnPhysHitChance byte
	EnemyPoisonOnPhysHitRounds  byte
	EnemyPoisonOnPhysHitChance byte
	// 150 - n 回合内对手每回合防御和特防等级 m
	EnemyDefSpDefRounds  byte
	EnemyDefSpDefStages  int8
	PlayerDefSpDefRounds byte
	PlayerDefSpDefStages int8
	// 471 - 先出手时 n 回合内免疫异常状态（复用 PlayerImmuneStatusRounds）
	// 490 - 若造成伤害超过 m 则自身速度 +n 级（每回合生效，不需要回合计数）
	// 429 - 固定伤递增：每次使用增加 increment，最高 max
	PlayerFixedDmgIncrement uint32
	EnemyFixedDmgIncrement  uint32
	// 441 - 每次攻击暴击率 +n%，最高 m%
	PlayerCritRateBonus byte
	EnemyCritRateBonus  byte
	// 110 - n 回合内每次受到攻击时 m% 几率使对手 stat 等级 -1
	PlayerDefendStatDropRounds byte
	PlayerDefendStatDropChance byte
	PlayerDefendStatDropStat   byte
	EnemyDefendStatDropRounds  byte
	EnemyDefendStatDropChance  byte
	EnemyDefendStatDropStat    byte
	// 91 - n 回合内双方状态变化同时影响己方与对手（能力等级与异常状态镜像）
	PlayerStatusMirrorRounds byte
	EnemyStatusMirrorRounds  byte
	// 181 - n% 概率使对手XX，每次使用m%增加，最高k%（累积几率）
	Player181CurrentChance byte // 当前累积几率（百分比）
	Player181StatusIdx     byte
	Player181MaxChance     byte
	Player181Increment     byte
	Enemy181CurrentChance  byte
	Enemy181StatusIdx      byte
	Enemy181MaxChance      byte
	Enemy181Increment      byte
}

// GameServer 游戏服务器
type GameServer struct {
	Port            int
	Clients         []*ClientData
	Sessions        map[string]*ClientData
	Users           map[int64]*userdb.GameData
	ServerList      []map[string]interface{}
	NextSeqID       int32
	UserDB          *userdb.UserDB
	mu              sync.RWMutex
	commandHandlers map[int32]CommandHandler
	BattleStates    map[int64]*BattleState // 用户ID -> 战斗状态
	BattleMu        sync.RWMutex           // 战斗状态锁
	// 地图玩家：mapID -> userID -> *ClientData，用于 2003 列表与广播
	MapUsers   map[int]map[int64]*ClientData
	MapUsersMu sync.RWMutex
	// 客户端断开时回调（由 handlers 注册，用于广播 2003 给同地图其他玩家）；mapID 为该用户断线前所在地图
	OnClientDisconnect func(*ClientData, int)

	// 野生精灵定时刷新（与 Lua 一致）：每玩家每地图槽位 + 进图/战毕时间
	OgreMu             sync.RWMutex
	OgreRefreshState   map[int64]*OgreRefreshState   // userId -> 进图/战毕/上次刷新时间
	OgreSlots          map[int64]map[int][]mapogres.Slot // userId -> mapID -> 当前槽位（长度 4 或 9）
	stopOgreTicker     chan struct{}
	// 暗黑武斗场：用户当前打开的门索引和子关卡索引（userID -> doorIndex），用于 2425 战斗时确定 bossID
	DarkPortalDoors    map[int64]DarkPortalDoorInfo
	DarkPortalDoorsMu  sync.RWMutex
	// 房间精灵展示：userID -> catchTime -> petID，仅内存缓存（房间精灵面板 2323/2324）
	RoomPets   map[int64]map[uint32]uint32
	RoomPetsMu sync.RWMutex
	// 罗威训练/外出精灵：userID -> catchTime -> petID，仅内存缓存（2320/2321/2322）
	RoweiPets   map[int64]map[uint32]uint32
	RoweiPetsMu sync.RWMutex
}

// OgreRefreshState 单玩家的精灵刷新时间状态
type OgreRefreshState struct {
	EnterMapTime     time.Time  // 进入当前地图时间
	LastFightEndTime  *time.Time // 战斗结束时间，用于战毕 3 秒内不刷新
	LastRefreshTime   time.Time  // 上次推送 2004 的刷新时间
}

// CommandHandler 命令处理器接口
type CommandHandler func(ctx *HandlerContext)

// HandlerContext 处理器上下文
type HandlerContext struct {
	UserID     int64
	CmdID      int32
	SeqID      int32
	Body       []byte
	ClientData *ClientData
	GameServer *GameServer
}

// New 创建游戏服务器实例
func New(config userdb.Config) *GameServer {
	gs := &GameServer{
		Port:              5000,
		Clients:           make([]*ClientData, 0),
		Sessions:          make(map[string]*ClientData),
		Users:             make(map[int64]*userdb.GameData),
		ServerList:        make([]map[string]interface{}, 0),
		NextSeqID:         1,
		UserDB:            userdb.New(config),
		commandHandlers:   make(map[int32]CommandHandler),
		BattleStates:      make(map[int64]*BattleState),
		MapUsers:          make(map[int]map[int64]*ClientData),
		OgreRefreshState:  make(map[int64]*OgreRefreshState),
		OgreSlots:         make(map[int64]map[int][]mapogres.Slot),
		stopOgreTicker:    make(chan struct{}),
		DarkPortalDoors:   make(map[int64]DarkPortalDoorInfo),
		RoomPets:          make(map[int64]map[uint32]uint32),
		RoweiPets:         make(map[int64]map[uint32]uint32),
	}

	gs.initServerList()
	return gs
}

// initServerList 初始化服务器列表
func (gs *GameServer) initServerList() {
	// 模拟官服的29个服务器
	for i := 1; i <= 29; i++ {
		gs.ServerList = append(gs.ServerList, map[string]interface{}{
			"id":      i,
			"userCnt": 30, // 模拟用户数
			"ip":      PublicIP,
			"port":    5000 + i,
			"friends": 0,
		})
	}
	logger.Info(fmt.Sprintf("初始化 %d 个服务器", len(gs.ServerList)))
}

// Start 启动服务器
func (gs *GameServer) Start() error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", gs.Port))
	if err != nil {
		return err
	}

	logger.Info(fmt.Sprintf("游戏服务器启动在端口 %d", gs.Port))

	go gs.runOgreRefreshLoop()

	go func() {
		defer ln.Close()

		for {
			conn, err := ln.Accept()
			if err != nil {
				logger.Error(fmt.Sprintf("接受连接失败: %v", err))
				continue
			}

			addr := conn.RemoteAddr()
			logger.Info(fmt.Sprintf("新连接: %s", addr))

			clientData := &ClientData{
				Socket:   conn,
				Buffer:   make([]byte, 0),
				UserID:   0,
				Session:  "",
				SeqID:    0,
				LoggedIn: false,
			}

			gs.mu.Lock()
			gs.Clients = append(gs.Clients, clientData)
			gs.mu.Unlock()

			go gs.handleClient(clientData)
		}
	}()

	return nil
}

// handleClient 处理客户端连接
func (gs *GameServer) handleClient(clientData *ClientData) {
	defer func() {
		gs.removeClient(clientData)
		clientData.Socket.Close()
	}()

	buffer := make([]byte, 4096)

	for {
		n, err := clientData.Socket.Read(buffer)
		if err != nil {
			logger.Info("客户端断开连接")
			return
		}

		clientData.Buffer = append(clientData.Buffer, buffer[:n]...)
		gs.processBuffer(clientData)
	}
}

// processBuffer 处理缓冲区数据
func (gs *GameServer) processBuffer(clientData *ClientData) {
	// Flash 客户端可能先发策略请求（不带 17 字节头），必须优先处理，否则会把 '<pol' 当 length 导致卡死
	for gs.tryConsumeFlashPolicyRequest(clientData) {
		// 连续处理多次（极少见，但防御）
	}

	for len(clientData.Buffer) >= 17 {
		// 读取长度
		length := packet.ReadUInt32BE(clientData.Buffer, 0)

		if len(clientData.Buffer) < int(length) {
			break // 等待更多数据
		}

		// 提取完整数据包
		packetData := clientData.Buffer[:length]
		clientData.Buffer = clientData.Buffer[length:]

		// 解析数据包
		gs.processPacket(clientData, packetData)
	}
}

// tryConsumeFlashPolicyRequest 检测并响应 Flash policy-file-request。
// 返回 true 表示消费了一次请求（buffer 发生变化），调用方可继续循环处理。
func (gs *GameServer) tryConsumeFlashPolicyRequest(clientData *ClientData) bool {
	if len(clientData.Buffer) < len(flashPolicyRequest) {
		return false
	}

	// 可能带 \x00 终止符
	if len(clientData.Buffer) >= len(flashPolicyRequest) && string(clientData.Buffer[:len(flashPolicyRequest)]) == flashPolicyRequest {
		consume := len(flashPolicyRequest)
		if len(clientData.Buffer) > consume && clientData.Buffer[consume] == 0x00 {
			consume++
		}
		clientData.Buffer = clientData.Buffer[consume:]
		gs.sendFlashPolicyFile(clientData)
		return true
	}
	return false
}

func (gs *GameServer) sendFlashPolicyFile(clientData *ClientData) {
	policy := `<?xml version="1.0"?>
<!DOCTYPE cross-domain-policy SYSTEM "/xml/dtds/cross-domain-policy.dtd">
<cross-domain-policy>
	<allow-access-from domain="*" to-ports="*" />
</cross-domain-policy>` + "\x00"

	if clientData.Socket == nil {
		return
	}
	if _, err := clientData.Socket.Write([]byte(policy)); err != nil {
		logger.Error(fmt.Sprintf("发送Flash安全策略文件失败: %v", err))
		return
	}
	logger.Info("已发送Flash安全策略文件（gameserver）")
}

// processPacket 处理数据包
func (gs *GameServer) processPacket(clientData *ClientData, packetData []byte) {
	if len(packetData) < 17 {
		return
	}

	// 读取包头
	length := packet.ReadUInt32BE(packetData, 0)
	// version := packetData[4] // 暂未使用
	cmdId := int32(packet.ReadUInt32BE(packetData, 5))
	userId := packet.ReadUInt32BE(packetData, 9)
	seqId := int32(packet.ReadUInt32BE(packetData, 13))

	body := make([]byte, 0)
	if len(packetData) > 17 {
		body = packetData[17:]
	}

	clientData.UserID = int64(userId)
	clientData.SeqID = seqId

	// 打印包体详情（格式对齐 Lua 后端）
	logger.Info(fmt.Sprintf("收到 CMD=%d UID=%d SEQ=%d LEN=%d", cmdId, userId, seqId, length))
	if len(body) > 0 {
		dump := packet.HexDump(body, fmt.Sprintf("[PACKET] CMD=%d 包体详情", cmdId))
		logger.Info(dump)
	}

	// 处理命令
	gs.handleCommand(clientData, cmdId, int64(userId), seqId, body)
}

// handleCommand 处理命令
func (gs *GameServer) handleCommand(clientData *ClientData, cmdId int32, userId int64, seqId int32, body []byte) {
	// 检查是否有注册的处理器
	handler, exists := gs.commandHandlers[cmdId]
	if exists {
		ctx := &HandlerContext{
			UserID:     userId,
			CmdID:      cmdId,
			SeqID:      seqId,
			Body:       body,
			ClientData: clientData,
			GameServer: gs,
		}
		handler(ctx)
		return
	}

	// 处理默认命令
	switch cmdId {
	case 80008:
		gs.handleHeartbeat(clientData, cmdId, userId, seqId, body)
	default:
		logger.Warning(fmt.Sprintf("未实现的命令: %d (UID=%d SEQ=%d BodyLen=%d)", cmdId, userId, seqId, len(body)))
		// 对于未实现的命令，返回空包但 result=0，避免客户端报错
		gs.SendResponse(clientData, cmdId, userId, seqId, []byte{})
	}
}

// handleHeartbeat 处理心跳包
func (gs *GameServer) handleHeartbeat(clientData *ClientData, cmdId int32, userId int64, seqId int32, body []byte) {
	// 客户端回复的心跳包，不需要再回复
	logger.Debug("收到心跳包响应")
}

// SendResponse 发送响应
func (gs *GameServer) SendResponse(clientData *ClientData, cmdId int32, userId int64, seqId int32, body []byte) {
	// AS3 客户端把第5字段当 result/errorCode，正常必须为 0
	response := packet.BuildResponse(cmdId, uint32(userId), 0, body)

	_, err := clientData.Socket.Write(response)
	if err != nil {
		logger.Error(fmt.Sprintf("发送响应失败: %v", err))
		return
	}

	// 打印包体详情（格式对齐 Lua 后端）
	logger.Info(fmt.Sprintf("发送 CMD=%d SEQ=%d LEN=%d", cmdId, seqId, len(response)))
	if len(body) > 0 {
		dump := packet.HexDump(body, fmt.Sprintf("[PACKET] CMD=%d 包体详情", cmdId))
		logger.Info(dump)
	}
}

// SendErrorResponse 发送错误响应（result 非 0 时客户端会触发 ParseSocketError，如 10017=购买失败）
func (gs *GameServer) SendErrorResponse(clientData *ClientData, cmdId int32, userId int64, seqId int32, errorCode int32) {
	response := packet.BuildResponse(cmdId, uint32(userId), errorCode, []byte{})

	_, err := clientData.Socket.Write(response)
	if err != nil {
		logger.Error(fmt.Sprintf("发送响应失败: %v", err))
		return
	}
	logger.Info(fmt.Sprintf("发送 CMD=%d SEQ=%d LEN=%d [ERROR %d]", cmdId, seqId, len(response), errorCode))
}

// removeClient 移除客户端
func (gs *GameServer) removeClient(clientData *ClientData) {
	// 清理心跳定时器
	if clientData.HeartbeatTimer != nil {
		clientData.HeartbeatTimer.Stop()
	}

	// 设置用户离线状态并持久化到磁盘，避免下线后物品/套装丢失
	if clientData.UserID > 0 && gs.UserDB != nil {
		gs.UserDB.SetUserOffline(clientData.UserID)
		gs.UserDB.SaveToFile()
		logger.Info(fmt.Sprintf("用户 %d 已离线", clientData.UserID))
	}

	// 从地图玩家中移除，并触发广播 2003 给同地图其他玩家（在解锁后由 OnClientDisconnect 执行）
	mapID := 0
	if clientData.UserID > 0 {
		u := gs.GetOrCreateUser(clientData.UserID)
		if u != nil {
			mapID = u.MapID
		}
		gs.RemoveUserFromMap(mapID, clientData.UserID)
	}

	// 从客户端列表移除
	gs.mu.Lock()
	for i, c := range gs.Clients {
		if c == clientData {
			gs.Clients = append(gs.Clients[:i], gs.Clients[i+1:]...)
			break
		}
	}
	// 从会话映射移除
	if clientData.Session != "" {
		delete(gs.Sessions, clientData.Session)
	}
	gs.mu.Unlock()

	if gs.OnClientDisconnect != nil && mapID != 0 {
		gs.OnClientDisconnect(clientData, mapID)
	}
}

// AddUserToMap 将用户加入地图（若曾在其他地图则需先 RemoveUserFromMap 旧地图）
func (gs *GameServer) AddUserToMap(mapID int, userID int64, client *ClientData) {
	if mapID <= 0 {
		return
	}
	gs.MapUsersMu.Lock()
	defer gs.MapUsersMu.Unlock()
	if gs.MapUsers[mapID] == nil {
		gs.MapUsers[mapID] = make(map[int64]*ClientData)
	}
	gs.MapUsers[mapID][userID] = client
}

// RemoveUserFromMap 将用户从地图移除
func (gs *GameServer) RemoveUserFromMap(mapID int, userID int64) {
	if mapID <= 0 {
		return
	}
	gs.MapUsersMu.Lock()
	defer gs.MapUsersMu.Unlock()
	if m, ok := gs.MapUsers[mapID]; ok {
		delete(m, userID)
		if len(m) == 0 {
			delete(gs.MapUsers, mapID)
		}
	}
}

// KickOtherSessionsOfUser 踢掉该用户在其他连接上的会话，保证同一账户只能一个在线
// excludeClient 为当前正在登录的连接，不会被踢；只关闭其他同 UID 的连接
func (gs *GameServer) KickOtherSessionsOfUser(excludeClient *ClientData, userID int64) {
	if userID <= 0 {
		return
	}
	gs.mu.Lock()
	var toClose net.Conn
	for _, c := range gs.Clients {
		if c != excludeClient && c.UserID == userID {
			toClose = c.Socket
			break
		}
	}
	gs.mu.Unlock()
	if toClose != nil {
		toClose.Close()
		logger.Info(fmt.Sprintf("同一账户重复登录: 已踢掉 UID=%d 的旧连接", userID))
	}
}

// GetClientByUserID 根据用户ID获取其连接（用于向指定用户推送如对战邀请等）
func (gs *GameServer) GetClientByUserID(userID int64) *ClientData {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	for _, c := range gs.Clients {
		if c.UserID == userID {
			return c
		}
	}
	return nil
}

// GetOnlineUserIDs 返回当前已登录的用户ID列表（用于 GM 在线列表）
func (gs *GameServer) GetOnlineUserIDs() []int64 {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	var ids []int64
	for _, c := range gs.Clients {
		if c.UserID > 0 && c.LoggedIn {
			ids = append(ids, c.UserID)
		}
	}
	return ids
}

// GetClientsOnMap 返回当前在该地图上的所有客户端（调用方只读，勿长时间持锁）
func (gs *GameServer) GetClientsOnMap(mapID int) []*ClientData {
	gs.MapUsersMu.RLock()
	m := gs.MapUsers[mapID]
	if m == nil || len(m) == 0 {
		gs.MapUsersMu.RUnlock()
		return nil
	}
	list := make([]*ClientData, 0, len(m))
	for _, c := range m {
		list = append(list, c)
	}
	gs.MapUsersMu.RUnlock()
	return list
}

// BroadcastToMap 向同地图除 excludeUserID 外的所有客户端发送一条协议
func (gs *GameServer) BroadcastToMap(mapID int, excludeUserID int64, cmdID int32, body []byte) {
	clients := gs.GetClientsOnMap(mapID)
	for _, c := range clients {
		if c.UserID == excludeUserID {
			continue
		}
		gs.SendResponse(c, cmdID, c.UserID, 0, body)
	}
}

// StartHeartbeat 启动心跳定时器
func (gs *GameServer) StartHeartbeat(clientData *ClientData, userId int64) {
	// 清理现有定时器
	if clientData.HeartbeatTimer != nil {
		clientData.HeartbeatTimer.Stop()
	}

	// 每6秒发送一次心跳包
	clientData.HeartbeatTimer = time.AfterFunc(6*time.Second, func() {
		gs.sendHeartbeat(clientData, userId)
		gs.StartHeartbeat(clientData, userId)
	})
}

// sendHeartbeat 发送心跳包
func (gs *GameServer) sendHeartbeat(clientData *ClientData, userId int64) {
	if clientData.Socket != nil && clientData.LoggedIn {
		response := packet.BuildResponse(80008, uint32(userId), 0, []byte{})
		_, err := clientData.Socket.Write(response)
		if err != nil {
			logger.Error(fmt.Sprintf("发送心跳包失败: %v", err))
		}
	}
}

// GetOrCreateUser 获取或创建用户
func (gs *GameServer) GetOrCreateUser(userId int64) *userdb.GameData {
	gs.mu.RLock()
	user, exists := gs.Users[userId]
	gs.mu.RUnlock()

	if exists {
		return user
	}

	// 从数据库获取
	gameData := gs.UserDB.GetOrCreateGameData(userId)

	gs.mu.Lock()
	gs.Users[userId] = gameData
	gs.mu.Unlock()

	return gameData
}

// RemoveUserCache 从内存缓存中移除用户（GM 删号后调用）
func (gs *GameServer) RemoveUserCache(userID int64) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	delete(gs.Users, userID)
}

// 野生精灵定时刷新常量（与 Lua Config.MapOgres 一致）
const (
	OgreEnterMapDelay   = 5 * time.Second  // 进图后多久开始刷新
	OgreFightEndDelay   = 3 * time.Second // 战斗结束后多久开始刷新（战毕 ≥3 秒可刷新）
	OgreRefreshInterval = 10 * time.Second // 两次刷新间隔
	OgreTickInterval    = 1 * time.Second   // 定时器检查间隔
)

// SetOgreEnterMapTime 记录玩家进入当前地图时间，供定时刷新判断
func (gs *GameServer) SetOgreEnterMapTime(userID int64) {
	gs.OgreMu.Lock()
	defer gs.OgreMu.Unlock()
	if gs.OgreRefreshState[userID] == nil {
		gs.OgreRefreshState[userID] = &OgreRefreshState{}
	}
	gs.OgreRefreshState[userID].EnterMapTime = time.Now()
}

// SetOgreFightEndTime 记录战斗结束时间，战毕 3 秒内不刷新
func (gs *GameServer) SetOgreFightEndTime(userID int64) {
	gs.OgreMu.Lock()
	defer gs.OgreMu.Unlock()
	now := time.Now()
	if gs.OgreRefreshState[userID] == nil {
		gs.OgreRefreshState[userID] = &OgreRefreshState{}
	}
	gs.OgreRefreshState[userID].LastFightEndTime = &now
}

// GetPlayerOgreSlots 获取该玩家在该地图的当前精灵槽位（只读），无则返回 nil
func (gs *GameServer) GetPlayerOgreSlots(userID int64, mapID int) []mapogres.Slot {
	gs.OgreMu.RLock()
	defer gs.OgreMu.RUnlock()
	if gs.OgreSlots[userID] == nil {
		return nil
	}
	return gs.OgreSlots[userID][mapID]
}

// SetPlayerOgreSlots 设置该玩家在该地图的精灵槽位
func (gs *GameServer) SetPlayerOgreSlots(userID int64, mapID int, slots []mapogres.Slot) {
	gs.OgreMu.Lock()
	defer gs.OgreMu.Unlock()
	if gs.OgreSlots[userID] == nil {
		gs.OgreSlots[userID] = make(map[int][]mapogres.Slot)
	}
	gs.OgreSlots[userID][mapID] = slots
}

// ClearPlayerOgreSlots 清空该玩家在该地图的槽位（战斗结束后调用，下次定时刷新会重新生成 3 只）
func (gs *GameServer) ClearPlayerOgreSlots(userID int64, mapID int) {
	gs.OgreMu.Lock()
	defer gs.OgreMu.Unlock()
	if gs.OgreSlots[userID] != nil {
		delete(gs.OgreSlots[userID], mapID)
	}
}

// runOgreRefreshLoop 每秒检查一次，对满足条件的玩家每 10 秒整批刷新该地图野生精灵并推送 2004
func (gs *GameServer) runOgreRefreshLoop() {
	ticker := time.NewTicker(OgreTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-gs.stopOgreTicker:
			return
		case <-ticker.C:
			gs.TickOgreRefresh()
		}
	}
}

// TickOgreRefresh 遍历在线玩家，满足条件则刷新该玩家当前地图精灵并推送 2004
func (gs *GameServer) TickOgreRefresh() {
	gs.mu.RLock()
	clients := make([]*ClientData, 0, len(gs.Clients))
	for _, c := range gs.Clients {
		if c.UserID > 0 && c.LoggedIn {
			clients = append(clients, c)
		}
	}
	gs.mu.RUnlock()

	now := time.Now()
	for _, client := range clients {
		userID := client.UserID
		user := gs.GetOrCreateUser(userID)
		mapID := user.MapID
		if mapID <= 0 {
			continue
		}

		gs.OgreMu.Lock()
		state := gs.OgreRefreshState[userID]
		if state == nil {
			state = &OgreRefreshState{EnterMapTime: now}
			gs.OgreRefreshState[userID] = state
		}
		// 战斗中不刷新
		gs.BattleMu.RLock()
		inFight := gs.BattleStates[userID] != nil && gs.BattleStates[userID].IsActive
		gs.BattleMu.RUnlock()
		if inFight {
			gs.OgreMu.Unlock()
			continue
		}
		// 战毕 3 秒内不刷新
		if state.LastFightEndTime != nil {
			if now.Sub(*state.LastFightEndTime) < OgreFightEndDelay {
				gs.OgreMu.Unlock()
				continue
			}
			state.LastFightEndTime = nil
		}
		// 进图 5 秒内不刷新
		if now.Sub(state.EnterMapTime) < OgreEnterMapDelay {
			gs.OgreMu.Unlock()
			continue
		}
		// 距上次刷新不足 10 秒不刷新
		if !state.LastRefreshTime.IsZero() && now.Sub(state.LastRefreshTime) < OgreRefreshInterval {
			gs.OgreMu.Unlock()
			continue
		}
		state.LastRefreshTime = now

		if gs.OgreSlots[userID] == nil {
			gs.OgreSlots[userID] = make(map[int][]mapogres.Slot)
		}
		gs.OgreMu.Unlock()

		// 每次到点刷新：整批重新生成（不再“一只消失一只出现”）
		newSlots := mapogres.GenerateNewSlotsNoCache(mapID)
		if len(newSlots) > 0 {
			gs.SetPlayerOgreSlots(userID, mapID, newSlots)
			body := gs.BuildMapOgreListFromSlots(newSlots)
			gs.SendResponse(client, 2004, userID, 0, body)
			logger.Info(fmt.Sprintf("[精灵刷新] UID=%d MapID=%d 整批刷新 %d 只", userID, mapID, len(newSlots)))
		}
	}
}

// BuildMapOgreListFromSlots 根据槽位列表构建 2004 包体（9 格，前 4 格用 slots，不足填 0）；slots 为 nil 时全 0
func (gs *GameServer) BuildMapOgreListFromSlots(slots []mapogres.Slot) []byte {
	body := make([]byte, 72)
	index := 0
	for i := 0; i < 9; i++ {
		var petID uint32
		var shiny uint32
		if i < len(slots) && slots[i].PetID > 0 {
			petID = uint32(slots[i].PetID)
			if slots[i].Shiny {
				shiny = 1
			}
		}
		binary.BigEndian.PutUint32(body[index:], petID)
		index += 4
		binary.BigEndian.PutUint32(body[index:], shiny)
		index += 4
	}
	return body
}

// RegisterCommandHandler 注册命令处理器
func (gs *GameServer) RegisterCommandHandler(cmdId int32, handler CommandHandler) {
	gs.commandHandlers[cmdId] = handler
}

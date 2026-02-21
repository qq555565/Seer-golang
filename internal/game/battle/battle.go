package battle

import (
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/seer-game/golang-version/internal/game/skills"
	"github.com/seer-game/golang-version/internal/game/typechart"
)

// 战斗状态常量
const (
	STATUS_PARALYSIS   = 0 // 麻痹
	STATUS_POISON      = 1 // 中毒
	STATUS_BURN        = 2 // 烧伤
	STATUS_DRAIN       = 3 // 吸取对方体力
	STATUS_DRAINED     = 4 // 被对方吸取体力
	STATUS_FREEZE      = 5 // 冻伤
	STATUS_FEAR        = 6 // 害怕
	STATUS_FATIGUE     = 7 // 疲惫
	STATUS_SLEEP       = 8 // 睡眠
	STATUS_PETRIFY     = 9 // 石化
	STATUS_CONFUSION   = 10 // 混乱
	STATUS_WEAKNESS    = 11 // 衰弱
	STATUS_MOUNTAIN_GUARD = 12 // 山神守护
	STATUS_FLAMMABLE   = 13 // 易燃
	STATUS_RAGE        = 14 // 狂暴
	STATUS_ICE_SEAL    = 15 // 冰封
	STATUS_BLEED       = 16 // 流血
	STATUS_IMMUNE_DOWN = 17 // 免疫能力下降
	STATUS_IMMUNE_STATUS = 18 // 免疫异常状态
)

// 能力等级常量
const (
	TRAIT_ATTACK   = 0 // 攻击
	TRAIT_DEFENCE  = 1 // 防御
	TRAIT_SP_ATK   = 2 // 特攻
	TRAIT_SP_DEF   = 3 // 特防
	TRAIT_SPEED    = 4 // 速度
	TRAIT_ACCURACY = 5 // 命中
)

// 战斗结束原因
const (
	REASON_NORMAL     = 0 // 正常
	REASON_EXIT       = 1 // 对方退出
	REASON_TIMEOUT    = 2 // 超时
	REASON_DRAW       = 3 // 平局
	REASON_ERROR      = 4 // 系统错误
	REASON_ESCAPE     = 5 // NPC逃跑
)

// 战斗倒计时
const (
	TURN_TIMEOUT    = 10 // 10秒
	AUTO_FIGHT_DELAY = 2 // 2秒
)

// Pet 战斗精灵
type Pet struct {
	ID       int
	Name     string
	Level    int
	HP       int
	MaxHP    int
	Attack   int
	Defence  int
	SpAtk    int
	SpDef    int
	Speed    int
	Type     int
	Type2    int
	Skills   []int
	SkillPP  []int
	CatchTime int
	BattleLv []int
	Status   map[int]int
	Fatigue  int
	Flinched bool
	Bound    bool
	BoundTurns int
}

// Battle 战斗实例
type Battle struct {
	BattleID       int64
	UserID         int64
	Turn           int
	MaxTurns       int
	IsOver         bool
	Winner         *int64
	Reason         int
	StartTime      time.Time
	LastActionTime time.Time
	Player         *Pet
	Enemy          *Pet
	Log            []BattleLog
	mu             sync.RWMutex
}

// BattleLog 战斗日志
type BattleLog struct {
	Turn               int
	PlayerSkillID      int
	EnemySkillID       int
	FirstAttack        *AttackResult
	SecondAttack       *AttackResult
	IsOver             bool
	Winner             *int64
	Reason             int // 战斗结束原因 (REASON_*)
	PlayerStatusDamage int
	EnemyStatusDamage  int
}

// AttackResult 攻击结果
type AttackResult struct {
	UserID           int64
	SkillID          int
	Damage           int
	IsCrit           bool
	TypeMod          float64
	AttackerRemainHp int
	AttackerMaxHp    int
	TargetRemainHp   int
	TargetMaxHp      int
	CannotAct        bool
	Reason           string
	Blocked          bool
	Missed           bool
	GainHp           int
	RecoilDamage     int
	AtkTimes         int
	Effects          []EffectResult
}

// EffectResult 效果结果
type EffectResult struct {
	Type   string
	Value  int
	Target string
	Stat   int
	Stages int
}

// NewBattle 创建战斗实例
func NewBattle(userID int64, playerPet, enemyPet map[string]interface{}) *Battle {
	battle := &Battle{
		BattleID:       time.Now().UnixNano(),
		UserID:         userID,
		Turn:           0,
		MaxTurns:       50,
		IsOver:         false,
		Reason:         REASON_NORMAL,
		StartTime:      time.Now(),
		LastActionTime: time.Now(),
		Player:         createPetFromData(playerPet),
		Enemy:          createPetFromData(enemyPet),
		Log:            make([]BattleLog, 0),
	}
	
	return battle
}

// createPetFromData 从数据创建精灵
func createPetFromData(data map[string]interface{}) *Pet {
	pet := &Pet{
		ID:       getInt(data, "id", 0),
		Name:     getString(data, "name", ""),
		Level:    getInt(data, "level", 5),
		HP:       getInt(data, "hp", 100),
		MaxHP:    getInt(data, "maxHp", 100),
		Attack:   getInt(data, "attack", 39),
		Defence:  getInt(data, "defence", 35),
		SpAtk:    getInt(data, "spAtk", 78),
		SpDef:    getInt(data, "spDef", 36),
		Speed:    getInt(data, "speed", 39),
		Type:     getInt(data, "type", 8),
		Type2:    getInt(data, "type2", 0),
		Skills:   getIntSlice(data, "skills"),
		SkillPP:  make([]int, 0),
		CatchTime: getInt(data, "catchTime", 0),
		BattleLv: []int{0, 0, 0, 0, 0, 0},
		Status:   make(map[int]int),
	}
	
	// 初始化技能PP
	for range pet.Skills {
		pet.SkillPP = append(pet.SkillPP, 30)
	}
	
	return pet
}

// GetTypeMultiplier 获取属性克制倍率（委托 typechart 包，保持 handlers 兼容）
func GetTypeMultiplier(atkType, defType int) float64 {
	return typechart.GetTypeMultiplier(atkType, defType)
}

// GetTypeMultiplierDual 处理双属性防守：倍率 = 单属性倍率相乘
func GetTypeMultiplierDual(atkType int, defType1 int, defType2 int) float64 {
	return typechart.GetTypeMultiplierDual(atkType, defType1, defType2)
}

// GetStatMultiplier 获取能力等级倍率
func GetStatMultiplier(stage int) float64 {
	stage = max(-6, min(6, stage))
	// 对齐你的规则：除命中外每级 +50% 基础能力值
	// s>=0: 1 + 0.5*s
	// s<0 : 1 / (1 + 0.5*|s|)
	if stage >= 0 {
		return 1.0 + 0.5*float64(stage)
	}
	return 1.0 / (1.0 + 0.5*float64(-stage))
}

// ApplyStatChange 应用能力等级变化
func ApplyStatChange(pet *Pet, stat, change int) bool {
	if pet.BattleLv == nil {
		pet.BattleLv = []int{0, 0, 0, 0, 0, 0}
	}
	
	index := stat
	oldStage := pet.BattleLv[index]
	newStage := max(-6, min(6, oldStage+change))
	pet.BattleLv[index] = newStage
	
	return newStage != oldStage
}

// CalculateDamage 计算伤害
func CalculateDamage(attacker, defender *Pet, skill *skills.Skill, isCrit bool) (int, float64, bool) {
	level := attacker.Level
	power := skill.Power
	
	// DmgBindLv: 伤害等于自身等级
	if skill.DmgBindLv == 1 {
		return level, 1.0, isCrit
	}
	
	// PwrBindDv: 威力=个体值*5
	if skill.PwrBindDv > 0 {
		power = skill.PwrBindDv * 5
	}
	
	// PwrDouble: 对方处于异常状态时威力翻倍
	if skill.PwrDouble == 1 && len(defender.Status) > 0 {
		power *= 2
	}
	
	// 物理/特殊攻击
	var atk, def int
	var atkStage, defStage int
	
	if skill.Category == 1 {
		// 物理
		atk = attacker.Attack
		def = defender.Defence
		atkStage = attacker.BattleLv[TRAIT_ATTACK]
		defStage = defender.BattleLv[TRAIT_DEFENCE]
	} else if skill.Category == 2 {
		// 特殊
		atk = attacker.SpAtk
		def = defender.SpDef
		atkStage = attacker.BattleLv[TRAIT_SP_ATK]
		defStage = defender.BattleLv[TRAIT_SP_DEF]
	} else {
		// 变化技能无伤害
		return 0, 1.0, false
	}
	
	// 应用能力等级
	atk = int(float64(atk) * GetStatMultiplier(atkStage))
	def = int(float64(def) * GetStatMultiplier(defStage))
	def = max(1, def)
	
	// 基础伤害（对齐：[(Lv*0.4+2)*Power*Atk/Def/50+2]）
	// 用浮点做中间值，最终向下取整
	baseDamage := int(((float64(level)*0.4 + 2.0) * float64(power) * float64(atk) / float64(def) / 50.0) + 2.0)
	
	// STAB (同属性加成)
	stab := 1.0
	if skill.Type == attacker.Type || (attacker.Type2 > 0 && skill.Type == attacker.Type2) {
		stab = 1.5
	}
	
	// 属性克制
	typeMod := GetTypeMultiplierDual(skill.Type, defender.Type, defender.Type2)
	
	// 暴击
	critMod := 1.0
	if isCrit {
		critMod = 1.5
	}
	
	// 随机值：217~255
	randomFactor := float64(rand.Intn(255-217+1)+217) / 255.0
	
	damage := int(float64(baseDamage) * stab * typeMod * critMod * randomFactor)
	
	return max(1, damage), typeMod, isCrit
}

// CheckCrit 检查是否暴击
func CheckCrit(attacker, defender *Pet, skill *skills.Skill, isFirst bool) bool {
	// CritAtkFirst: 先出手必暴击
	if skill.CritAtkFirst == 1 && isFirst {
		return true
	}
	
	// CritAtkSecond: 后出手必暴击
	if skill.CritAtkSecond == 1 && !isFirst {
		return true
	}
	
	// CritSelfHalfHp: 自身HP低于一半必暴击
	if skill.CritSelfHalfHp == 1 && attacker.HP < attacker.MaxHP/2 {
		return true
	}
	
	// CritFoeHalfHp: 对方HP低于一半必暴击
	if skill.CritFoeHalfHp == 1 && defender.HP < defender.MaxHP/2 {
		return true
	}
	
	// 基础暴击率
	critRate := skill.CritRate
	if critRate == 0 {
		critRate = 1
	}
	
	// 速度等级加成
	speedStage := attacker.BattleLv[TRAIT_SPEED]
	bonusCrit := max(0, speedStage)
	
	return rand.Intn(16) < (critRate + bonusCrit)
}

// CalcHitChance 根据基础命中和命中/闪避等级计算最终命中率（1~100）
// 命中等级差每级 ±25 个百分点：例如 30% 命中，在命中+1 时变为 55%。
func CalcHitChance(baseAccuracy, accStage, evaStage int) int {
	if baseAccuracy <= 0 {
		baseAccuracy = 100
	}
	stage := max(-6, min(6, accStage-evaStage))

	finalAcc := baseAccuracy + 25*stage
	if finalAcc < 1 {
		finalAcc = 1
	}
	if finalAcc > 100 {
		finalAcc = 100
	}
	return finalAcc
}

// CheckHit 检查是否命中
func CheckHit(attacker, defender *Pet, skill *skills.Skill) bool {
	// 命中等级修正
	accStage := attacker.BattleLv[TRAIT_ACCURACY]
	evaStage := 0 // 闪避等级 (暂未实现)
	finalAcc := CalcHitChance(skill.Accuracy, accStage, evaStage)
	return rand.Intn(100) < finalAcc
}

// ProcessStatusEffects 处理状态效果
func ProcessStatusEffects(pet *Pet) int {
	if pet.Status == nil {
		pet.Status = make(map[int]int)
	}
	
	statusDamage := 0
	
	// 中毒伤害 (每回合损失1/8最大HP)
	if turns, ok := pet.Status[STATUS_POISON]; ok && turns > 0 {
		statusDamage += pet.MaxHP / 8
		pet.Status[STATUS_POISON] = turns - 1
		if pet.Status[STATUS_POISON] <= 0 {
			delete(pet.Status, STATUS_POISON)
		}
	}
	
	// 烧伤伤害 (每回合损失1/16最大HP)
	if turns, ok := pet.Status[STATUS_BURN]; ok && turns > 0 {
		statusDamage += pet.MaxHP / 16
		pet.Status[STATUS_BURN] = turns - 1
		if pet.Status[STATUS_BURN] <= 0 {
			delete(pet.Status, STATUS_BURN)
		}
	}
	
	// 冻伤伤害 (每回合损失1/16最大HP)
	if turns, ok := pet.Status[STATUS_FREEZE]; ok && turns > 0 {
		statusDamage += pet.MaxHP / 16
		pet.Status[STATUS_FREEZE] = turns - 1
		if pet.Status[STATUS_FREEZE] <= 0 {
			delete(pet.Status, STATUS_FREEZE)
		}
	}
	
	// 流血伤害 (每回合损失1/8最大HP)
	if turns, ok := pet.Status[STATUS_BLEED]; ok && turns > 0 {
		statusDamage += pet.MaxHP / 8
		pet.Status[STATUS_BLEED] = turns - 1
		if pet.Status[STATUS_BLEED] <= 0 {
			delete(pet.Status, STATUS_BLEED)
		}
	}
	
	// 紧勒伤害
	if pet.Bound && pet.BoundTurns > 0 {
		statusDamage += pet.MaxHP / 16
		pet.BoundTurns--
		if pet.BoundTurns <= 0 {
			pet.Bound = false
		}
	}
	
	return statusDamage
}

// CanAct 检查是否可以行动
func CanAct(pet *Pet) (bool, string) {
	if pet.Status == nil {
		pet.Status = make(map[int]int)
	}
	
	// 疲惫状态无法行动
	if pet.Fatigue > 0 {
		pet.Fatigue--
		return false, "fatigue"
	}
	
	// 睡眠状态无法行动
	if turns, ok := pet.Status[STATUS_SLEEP]; ok && turns > 0 {
		pet.Status[STATUS_SLEEP] = turns - 1
		if pet.Status[STATUS_SLEEP] <= 0 {
			delete(pet.Status, STATUS_SLEEP)
		}
		return false, "sleep"
	}
	
	// 石化状态无法行动
	if turns, ok := pet.Status[STATUS_PETRIFY]; ok && turns > 0 {
		pet.Status[STATUS_PETRIFY] = turns - 1
		if pet.Status[STATUS_PETRIFY] <= 0 {
			delete(pet.Status, STATUS_PETRIFY)
		}
		return false, "petrify"
	}
	
	// 冰封状态无法行动
	if turns, ok := pet.Status[STATUS_ICE_SEAL]; ok && turns > 0 {
		pet.Status[STATUS_ICE_SEAL] = turns - 1
		if pet.Status[STATUS_ICE_SEAL] <= 0 {
			delete(pet.Status, STATUS_ICE_SEAL)
		}
		return false, "ice_seal"
	}
	
	// 冰冻状态无法行动
	if turns, ok := pet.Status[STATUS_FREEZE]; ok && turns > 0 {
		pet.Status[STATUS_FREEZE] = turns - 1
		if pet.Status[STATUS_FREEZE] <= 0 {
			delete(pet.Status, STATUS_FREEZE)
		}
		return false, "freeze"
	}
	
	// 麻痹有25%几率无法行动
	if turns, ok := pet.Status[STATUS_PARALYSIS]; ok && turns > 0 {
		if rand.Intn(4) == 0 {
			return false, "paralysis"
		}
	}
	
	// 害怕有50%几率无法行动
	if turns, ok := pet.Status[STATUS_FEAR]; ok && turns > 0 {
		pet.Status[STATUS_FEAR] = turns - 1
		if pet.Status[STATUS_FEAR] <= 0 {
			delete(pet.Status, STATUS_FEAR)
		}
		if rand.Intn(2) == 0 {
			return false, "fear"
		}
	}
	
	// 混乱有33%几率攻击自己
	if turns, ok := pet.Status[STATUS_CONFUSION]; ok && turns > 0 {
		pet.Status[STATUS_CONFUSION] = turns - 1
		if pet.Status[STATUS_CONFUSION] <= 0 {
			delete(pet.Status, STATUS_CONFUSION)
		}
		if rand.Intn(3) == 0 {
			return false, "confusion"
		}
	}
	
	// 畏缩状态无法行动 (只持续一回合)
	if pet.Flinched {
		pet.Flinched = false
		return false, "flinch"
	}
	
	return true, ""
}

// AISelectSkill AI选择技能
func AISelectSkill(aiPet, playerPet *Pet, skillIDs []int) int {
	if len(skillIDs) == 0 {
		return 0
	}
	
	bestSkill := 0
	bestScore := -1
	skillManager := skills.GetInstance()
	
	for _, skillID := range skillIDs {
		if skillID > 0 {
			skill := skillManager.Get(skillID)
			if skill != nil {
				score := 0
				
				if skill.Power > 0 {
					// 攻击技能评分
					typeMod := GetTypeMultiplier(skill.Type, playerPet.Type)
					score = skill.Power * int(typeMod*100)
					
					// 考虑命中率
					accuracy := skill.Accuracy
					if accuracy == 0 {
						accuracy = 100
					}
					score = score * accuracy / 100
					
					// 如果对方HP低，优先使用高威力技能
					if playerPet.HP > 0 && playerPet.MaxHP > 0 {
						hpRatio := float64(playerPet.HP) / float64(playerPet.MaxHP)
						if hpRatio < 0.3 {
							score = score * 3 / 2 // 收割加成
						}
					}
				} else {
					// 变化技能评分
					score = 1000
					
					// 如果自己HP低，考虑使用回复技能
					if aiPet.HP > 0 && aiPet.MaxHP > 0 {
						hpRatio := float64(aiPet.HP) / float64(aiPet.MaxHP)
						if hpRatio < 0.5 {
							score = 10000 // 回复技能高优先级
						}
					}
				}
				
				if score > bestScore {
					bestScore = score
					bestSkill = skillID
				}
			}
		}
	}
	
	// 如果没有找到合适技能，使用第一个有效技能
	if bestSkill == 0 {
		for _, skillID := range skillIDs {
			if skillID > 0 {
				bestSkill = skillID
				break
			}
		}
	}
	
	return bestSkill
}

// CompareSpeed 比较速度决定先后攻
func CompareSpeed(pet1, pet2 *Pet, skill1, skill2 *skills.Skill) bool {
	// 先检查技能优先级
	priority1 := 0
	if skill1 != nil {
		priority1 = skill1.Priority
	}
	
	priority2 := 0
	if skill2 != nil {
		priority2 = skill2.Priority
	}
	
	if priority1 != priority2 {
		return priority1 > priority2
	}
	
	// 计算实际速度 (考虑能力等级)
	speed1 := pet1.Speed
	speed2 := pet2.Speed
	
	speedStage1 := pet1.BattleLv[TRAIT_SPEED]
	speedStage2 := pet2.BattleLv[TRAIT_SPEED]
	
	speed1 = int(float64(speed1) * GetStatMultiplier(speedStage1))
	speed2 = int(float64(speed2) * GetStatMultiplier(speedStage2))
	
	// 麻痹状态速度减半
	if turns, ok := pet1.Status[STATUS_PARALYSIS]; ok && turns > 0 {
		speed1 /= 2
	}
	if turns, ok := pet2.Status[STATUS_PARALYSIS]; ok && turns > 0 {
		speed2 /= 2
	}
	
	if speed1 != speed2 {
		return speed1 > speed2
	}
	
	// 速度相同时随机
	return rand.Intn(2) == 0
}

// ExecuteTurn 执行一回合战斗
func (b *Battle) ExecuteTurn(playerSkillID int) *BattleLog {
	b.mu.Lock()
	defer b.mu.Unlock()
	
	b.Turn++
	b.LastActionTime = time.Now()
	
	// 检查回合数限制
	if b.Turn > b.MaxTurns {
		b.IsOver = true
		b.Reason = REASON_DRAW
		return &BattleLog{
			Turn:   b.Turn,
			IsOver: true,
			Reason: REASON_DRAW,
		}
	}
	
	skillManager := skills.GetInstance()
	playerSkill := skillManager.Get(playerSkillID)
	
	// 扣除玩家PP
	if playerSkillID > 0 {
		for i, sid := range b.Player.Skills {
			if sid == playerSkillID {
				b.Player.SkillPP[i] = max(0, b.Player.SkillPP[i]-1)
				break
			}
		}
	}
	
	// AI选择技能
	enemySkillID := AISelectSkill(b.Enemy, b.Player, b.Enemy.Skills)
	enemySkill := skillManager.Get(enemySkillID)
	
	// 扣除敌人PP
	if enemySkillID > 0 {
		for i, sid := range b.Enemy.Skills {
			if sid == enemySkillID {
				b.Enemy.SkillPP[i] = max(0, b.Enemy.SkillPP[i]-1)
				break
			}
		}
	}
	
	// 处理状态效果
	playerStatusDamage := ProcessStatusEffects(b.Player)
	enemyStatusDamage := ProcessStatusEffects(b.Enemy)
	
	// 应用状态伤害
	if playerStatusDamage > 0 {
		b.Player.HP = max(0, b.Player.HP-playerStatusDamage)
	}
	if enemyStatusDamage > 0 {
		b.Enemy.HP = max(0, b.Enemy.HP-enemyStatusDamage)
	}
	
	// 检查状态伤害是否导致死亡
	if b.Enemy.HP <= 0 {
		b.IsOver = true
		winner := b.UserID
		b.Winner = &winner
		return &BattleLog{
			Turn:   b.Turn,
			IsOver: true,
			Winner: &winner,
		}
	}
	
	// 检查是否可以行动
	playerCanAct, playerActReason := CanAct(b.Player)
	enemyCanAct, enemyActReason := CanAct(b.Enemy)
	
	// 当玩家精灵HP为0时，强制玩家无法行动
	if b.Player.HP <= 0 {
		playerCanAct = false
		playerActReason = "fainted"
	}
	
	// 当玩家切换精灵时，强制玩家无法行动
	if playerSkillID == 0 {
		playerCanAct = false
		playerActReason = "switching"
	}
	
	log := &BattleLog{
		Turn:              b.Turn,
		PlayerSkillID:     playerSkillID,
		EnemySkillID:      enemySkillID,
		PlayerStatusDamage: playerStatusDamage,
		EnemyStatusDamage:  enemyStatusDamage,
	}
	
	// 当玩家切换精灵时，直接让敌人行动
	if playerSkillID == 0 {
		// 敌方行动
		if enemyCanAct {
			result := ExecuteAttack(b.Enemy, b.Player, enemySkill, 0, true)
			b.Player.HP = result.TargetRemainHp
			log.FirstAttack = result
		} else {
			log.FirstAttack = &AttackResult{
				UserID:           0,
				SkillID:          0,
				Damage:           0,
				IsCrit:           false,
				TypeMod:          1,
				AttackerRemainHp: b.Enemy.HP,
				AttackerMaxHp:    b.Enemy.MaxHP,
				TargetRemainHp:   b.Player.HP,
				TargetMaxHp:      b.Player.MaxHP,
				CannotAct:        true,
				Reason:           enemyActReason,
			}
		}
	} else {
		// 正常战斗逻辑
		// 决定先后攻
		playerFirst := CompareSpeed(b.Player, b.Enemy, playerSkill, enemySkill)
		
		if playerFirst {
			// 玩家先攻
			if playerCanAct {
				result := ExecuteAttack(b.Player, b.Enemy, playerSkill, b.UserID, true)
				b.Enemy.HP = result.TargetRemainHp
				log.FirstAttack = result
			} else {
				log.FirstAttack = &AttackResult{
					UserID:           b.UserID,
					SkillID:          0,
					Damage:           0,
					IsCrit:           false,
					TypeMod:          1,
					AttackerRemainHp: b.Player.HP,
					AttackerMaxHp:    b.Player.MaxHP,
					TargetRemainHp:   b.Enemy.HP,
					TargetMaxHp:      b.Enemy.MaxHP,
					CannotAct:        true,
					Reason:           playerActReason,
				}
			}
			
			if b.Enemy.HP <= 0 {
				b.IsOver = true
				winner := b.UserID
				b.Winner = &winner
				log.IsOver = true
				log.Winner = &winner
			} else {
				// 敌方反击
				if enemyCanAct {
					result := ExecuteAttack(b.Enemy, b.Player, enemySkill, 0, false)
					b.Player.HP = result.TargetRemainHp
					log.SecondAttack = result
				} else {
					log.SecondAttack = &AttackResult{
						UserID:           0,
						SkillID:          0,
						Damage:           0,
						IsCrit:           false,
						TypeMod:          1,
						AttackerRemainHp: b.Enemy.HP,
						AttackerMaxHp:    b.Enemy.MaxHP,
						TargetRemainHp:   b.Player.HP,
						TargetMaxHp:      b.Player.MaxHP,
						CannotAct:        true,
						Reason:           enemyActReason,
					}
				}
			}
		} else {
			// 敌方先攻
			if enemyCanAct {
				result := ExecuteAttack(b.Enemy, b.Player, enemySkill, 0, true)
				b.Player.HP = result.TargetRemainHp
				log.FirstAttack = result
			} else {
				log.FirstAttack = &AttackResult{
					UserID:           0,
					SkillID:          0,
					Damage:           0,
					IsCrit:           false,
					TypeMod:          1,
					AttackerRemainHp: b.Enemy.HP,
					AttackerMaxHp:    b.Enemy.MaxHP,
					TargetRemainHp:   b.Player.HP,
					TargetMaxHp:      b.Player.MaxHP,
					CannotAct:        true,
					Reason:           enemyActReason,
				}
			}
			
			// 玩家反击
			if playerCanAct {
				result := ExecuteAttack(b.Player, b.Enemy, playerSkill, b.UserID, false)
				b.Enemy.HP = result.TargetRemainHp
				log.SecondAttack = result
			} else {
				log.SecondAttack = &AttackResult{
					UserID:           b.UserID,
					SkillID:          0,
					Damage:           0,
					IsCrit:           false,
					TypeMod:          1,
					AttackerRemainHp: b.Player.HP,
					AttackerMaxHp:    b.Player.MaxHP,
					TargetRemainHp:   b.Enemy.HP,
					TargetMaxHp:      b.Enemy.MaxHP,
					CannotAct:        true,
					Reason:           playerActReason,
				}
			}
			
			if b.Enemy.HP <= 0 {
				b.IsOver = true
				winner := b.UserID
				b.Winner = &winner
				log.IsOver = true
				log.Winner = &winner
			}
		}
	}
	
	b.Log = append(b.Log, *log)
	return log
}

// ExecuteAttack 执行攻击
func ExecuteAttack(attacker, defender *Pet, skill *skills.Skill, attackerUserID int64, isFirst bool) *AttackResult {
	result := &AttackResult{
		UserID:           attackerUserID,
		SkillID:          0,
		AttackerRemainHp: attacker.HP,
		AttackerMaxHp:    attacker.MaxHP,
		TargetRemainHp:   defender.HP,
		TargetMaxHp:      defender.MaxHP,
	}

	// 防御性保护：避免 skill 为空时造成 panic（在 Lua 服中视为“本回合未成功出招”）
	if skill == nil {
		result.CannotAct = true
		result.Reason = "no_skill"
		return result
	}

	// 记录技能 ID（在确认非空之后）
	result.SkillID = skill.ID
	
	// 检查命中
	hit := skill.MustHit == 1 || CheckHit(attacker, defender, skill)
	
	if !hit {
		result.Missed = true
		return result
	}
	
	// 检查暴击
	isCrit := CheckCrit(attacker, defender, skill, isFirst)
	
	// 计算伤害
	damage, typeMod, _ := CalculateDamage(attacker, defender, skill, isCrit)
	
	// 应用伤害
	defender.HP = max(0, defender.HP-damage)
	result.Damage = damage
	result.IsCrit = isCrit
	result.TypeMod = typeMod
	result.TargetRemainHp = defender.HP
	
	return result
}

// CalculateRewards 计算战斗奖励
func CalculateRewards(battle *Battle) map[string]int {
	rewards := map[string]int{
		"exp":   0,
		"coins": 0,
	}
	
	if battle.Winner != nil && *battle.Winner == battle.UserID {
		// 玩家胜利
		enemyLevel := battle.Enemy.Level
		playerLevel := battle.Player.Level
		
		// 经验计算
		baseExp := enemyLevel * 5
		levelDiff := enemyLevel - playerLevel
		expMod := 1.0 + float64(levelDiff)*0.1
		expMod = math.Max(0.5, math.Min(2.0, expMod))
		
		rewards["exp"] = int(float64(baseExp) * expMod)
		rewards["coins"] = enemyLevel * 10
	}
	
	return rewards
}

// CheckTimeout 检查超时
func (b *Battle) CheckTimeout() bool {
	timeSinceLastAction := time.Since(b.LastActionTime)
	return timeSinceLastAction > TURN_TIMEOUT*time.Second
}

// GetInstance 获取战斗管理器实例
func GetInstance() *BattleManager {
	return &BattleManager{
		battles: make(map[int64]*Battle),
	}
}

// BattleManager 战斗管理器
type BattleManager struct {
	battles map[int64]*Battle
	mu      sync.RWMutex
}

// CreateBattle 创建战斗
func (bm *BattleManager) CreateBattle(userID int64, playerPet, enemyPet map[string]interface{}) *Battle {
	battle := NewBattle(userID, playerPet, enemyPet)
	
	bm.mu.Lock()
	bm.battles[battle.BattleID] = battle
	bm.mu.Unlock()
	
	return battle
}

// GetBattle 获取战斗
func (bm *BattleManager) GetBattle(battleID int64) *Battle {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	
	return bm.battles[battleID]
}

// RemoveBattle 移除战斗
func (bm *BattleManager) RemoveBattle(battleID int64) {
	bm.mu.Lock()
	delete(bm.battles, battleID)
	bm.mu.Unlock()
}

// 辅助函数
func getInt(m map[string]interface{}, key string, defaultValue int) int {
	if val, ok := m[key]; ok {
		if intVal, ok := val.(int); ok {
			return intVal
		}
	}
	return defaultValue
}

func getString(m map[string]interface{}, key string, defaultValue string) string {
	if val, ok := m[key]; ok {
		if strVal, ok := val.(string); ok {
			return strVal
		}
	}
	return defaultValue
}

func getIntSlice(m map[string]interface{}, key string) []int {
	if val, ok := m[key]; ok {
		if sliceVal, ok := val.([]int); ok {
			return sliceVal
		}
	}
	return []int{}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

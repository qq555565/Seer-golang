package skills

import (
	"math"
	"math/rand"
	"strconv"
	"strings"

	"github.com/seer-game/golang-version/internal/game/sptboss"
)

// 异常状态常量：客户端 status 数组的索引 = 状态类型，值 = 持续回合数
// 与 PetFightMsgManager.STATUS_ARRAY 对应：["麻痹","中毒","烧伤","吸取","被吸取","冻伤","害怕","疲惫","睡眠",...]
const (
	StatusIndexParalysis = 0 // 麻痹
	StatusIndexPoison    = 1 // 中毒
	StatusIndexBurn      = 2 // 烧伤
	StatusIndexDrain     = 3 // 吸取对方的体力
	StatusIndexDrained   = 4 // 被对方吸取体力
	StatusIndexFreeze    = 5 // 冻伤
	StatusIndexFear      = 6 // 害怕/畏缩（用于畏缩时本回合无法行动）
	StatusIndexFatigue   = 7 // 疲惫（下回合无法行动）
	StatusIndexSleep     = 8 // 睡眠（效果与麻痹一致，但图标不同）
)

// randomStatusRounds 返回 2~3 回合，用于控制类异常（麻痹/害怕/睡眠/疲惫等）
func randomStatusRounds() byte {
	return byte(rand.Intn(2) + 2)
}

// randomDoTRounds 仅用于中毒/烧伤/冻伤：随机 1~2 回合，与控制类异常分开
func randomDoTRounds() byte {
	return byte(rand.Intn(2) + 1)
}

// applyNewStatusDamageOnce 本回合刚挂上中毒/烧伤/冻伤时立即扣一次血（1/8 最大HP），与 ProcessStatusEffects 单次扣量一致
func applyNewStatusDamageOnce(hp *uint32, maxHP uint32) {
	if hp == nil || maxHP == 0 {
		return
	}
	dmg := maxHP / 8
	if dmg > *hp {
		dmg = *hp
	}
	*hp -= dmg
}

// 能力项索引（与 Lua TRAIT、客户端 battleLv 对应）
// 0=攻击 1=防御 2=特攻 3=特防 4=速度 5=命中
const (
	StatAttack   = 0
	StatDefence  = 1
	StatSpAtk    = 2
	StatSpDef    = 3
	StatSpeed    = 4
	StatAccuracy = 5
)

const (
	maxStatStage = 6
	minStatStage = -6
)

// CritBuffState 暴击率强化状态（由某些技能效果设置，例如 SideEffect 58 系列）
// 该状态不直接体现在 battleLv 中，而是独立的“下 N 回合必定暴击/提高暴击率”的标记。
// 为了最小侵入，这里先定义为简单的“下 N 回合攻击技能必定暴击”：
// - N 存在于 SideEffectArg（如 1000058 的 des），由 handlers 在回合开始递减；
// - 具体存储位置在 BattleState 中扩展字段。
// Go 版 BattleState 暂未扩展该字段，因此真正的暴击强化在 handlers 中实现。

// ParseSideEffectArg 解析 SideEffectArg 字符串为整数切片（与 Lua SkillEffects.parseArgs 一致）
// 支持 "1 15 -1"、"5 100 1" 等格式
func ParseSideEffectArg(s string) []int {
	var out []int
	for _, part := range strings.Fields(s) {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

// EffectResult 单次技能效果应用结果，用于日志或扩展
type EffectResult struct {
	GainHP       int32
	RecoilDamage uint32
}

// ProcessStatusEffects 回合开始时结算异常状态伤害（对齐 Lua processStatusEffects）
// 烧伤/中毒/冻伤：每回合扣 1/8 最大血量，并递减对应 status 回合数。
func ProcessStatusEffects(playerHP, enemyHP *uint32, playerMaxHP, enemyMaxHP uint32, playerStatus, enemyStatus *[20]byte) {
	if playerHP != nil && playerStatus != nil && playerMaxHP > 0 {
		// 中毒 status[1]
		if playerStatus[StatusIndexPoison] > 0 {
			dmg := playerMaxHP / 8
			if dmg > *playerHP {
				dmg = *playerHP
			}
			*playerHP -= dmg
			playerStatus[StatusIndexPoison]--
		}
		// 烧伤 status[2]：每回合 1/8 最大HP
		if playerStatus[StatusIndexBurn] > 0 {
			dmg := playerMaxHP / 8
			if dmg > *playerHP {
				dmg = *playerHP
			}
			*playerHP -= dmg
			playerStatus[StatusIndexBurn]--
		}
		// 冻伤 status[5]：效果与烧伤/中毒相同，每回合 1/8 最大HP
		if playerStatus[StatusIndexFreeze] > 0 {
			dmg := playerMaxHP / 8
			if dmg > *playerHP {
				dmg = *playerHP
			}
			*playerHP -= dmg
			playerStatus[StatusIndexFreeze]--
		}
	}
	if enemyHP != nil && enemyStatus != nil && enemyMaxHP > 0 {
		if enemyStatus[StatusIndexPoison] > 0 {
			dmg := enemyMaxHP / 8
			if dmg > *enemyHP {
				dmg = *enemyHP
			}
			*enemyHP -= dmg
			enemyStatus[StatusIndexPoison]--
		}
		if enemyStatus[StatusIndexBurn] > 0 {
			dmg := enemyMaxHP / 8
			if dmg > *enemyHP {
				dmg = *enemyHP
			}
			*enemyHP -= dmg
			enemyStatus[StatusIndexBurn]--
		}
		if enemyStatus[StatusIndexFreeze] > 0 {
			dmg := enemyMaxHP / 8
			if dmg > *enemyHP {
				dmg = *enemyHP
			}
			*enemyHP -= dmg
			enemyStatus[StatusIndexFreeze]--
		}
	}
}

// ApplyEffect 根据技能附加效果修改战斗状态（对齐 Lua seer_battle + seer_skill_effects）
// 修改 playerHP, enemyHP, playerBattleLv, enemyBattleLv, playerStatus, enemyStatus 原地；
// defenderPetID 为被攻击方精灵 ID，用于 BOSS 异常免疫（雷伊/哈莫雷特/奈尼芬多/盖亚免疫所有异常）。
// 返回本回合因效果产生的 gainHP（给攻击方）和 recoilDamage（反伤给攻击方）。
func ApplyEffect(skill *Skill, damage uint32, playerHP, enemyHP *uint32, playerMaxHP, enemyMaxHP uint32,
	playerBattleLv, enemyBattleLv *[6]int8, playerStatus, enemyStatus *[20]byte, defenderPetID int) (gainHP int32, recoilDamage uint32) {
	if skill == nil {
		return 0, 0
	}
	eid := skill.EffectID
	if eid <= 0 {
		return 0, 0
	}
	args := ParseSideEffectArg(skill.SideEffectArg)
	statusImmune := sptboss.IsStatusImmune(defenderPetID)
	statDropImmune := sptboss.IsStatDropImmune(defenderPetID)

	switch eid {
	case 1:
		// 吸血：恢复造成伤害的一定比例
		percent := 50
		if len(args) >= 1 {
			percent = args[0]
		}
		if percent <= 0 {
			percent = 50
		}
		heal := uint32(math.Floor(float64(damage) * float64(percent) / 100))
		if heal > 0 && playerHP != nil {
			sum := *playerHP + heal
			if sum > playerMaxHP {
				sum = playerMaxHP
			}
			*playerHP = sum
			gainHP = int32(heal)
		}

	case 2:
		// 降低对方能力等级（Lua eid=2）SideEffectArg: stat stages；免疫能力下降的 BOSS 不低于 0
		stat, stages := 1, 1
		if len(args) >= 1 {
			stat = args[0]
		}
		if len(args) >= 2 {
			stages = args[1]
		}
		if stat < 0 || stat > 5 {
			stat = 0
		}
		if enemyBattleLv != nil {
			cur := int(enemyBattleLv[stat])
			cur -= stages
			if statDropImmune && cur < 0 {
				cur = 0
			} else if cur < minStatStage {
				cur = minStatStage
			}
			enemyBattleLv[stat] = int8(cur)
		}

	case 3:
		// 提高自身能力等级（同 case 4，Lua eid=3）；SideEffectArg 可多组 stat chance stages，如 "0 100 2 2 100 2"
		for i := 0; i+2 < len(args); i += 3 {
			stat, chance, stages := args[i], 100, 1
			if i+1 < len(args) {
				chance = args[i+1]
			}
			if i+2 < len(args) {
				stages = args[i+2]
			}
			if stat < 0 || stat > 5 {
				stat = 0
			}
			if rand.Intn(100) < chance && playerBattleLv != nil {
				cur := int(playerBattleLv[stat])
				cur += stages
				if cur > maxStatStage {
					cur = maxStatStage
				}
				playerBattleLv[stat] = int8(cur)
			}
		}

	case 4:
		// 提升自己能力等级 SideEffectArg: 可多组 stat chance stages，如红韵 "0 100 1 1 100 1 3 100 1"、觉醒 "0 100 2 2 100 2"
		for i := 0; i+2 < len(args); i += 3 {
			stat, chance, stages := args[i], 100, 1
			if i+1 < len(args) {
				chance = args[i+1]
			}
			if i+2 < len(args) {
				stages = args[i+2]
			}
			if stat < 0 || stat > 5 {
				stat = 0
			}
			if rand.Intn(100) < chance && playerBattleLv != nil {
				cur := int(playerBattleLv[stat])
				cur += stages
				if cur > maxStatStage {
					cur = maxStatStage
				}
				playerBattleLv[stat] = int8(cur)
			}
		}

	case 5:
		// 降低对方或提升自己能力：stages 负数=降对手，正数=升自己；SideEffectArg 可多组 stat chance stages，如电闪光 "4 100 -1 5 100 -1"（速度、命中各-1）
		for i := 0; i+2 < len(args); i += 3 {
			stat, chance, stages := args[i], 100, -1
			if i+1 < len(args) {
				chance = args[i+1]
			}
			if i+2 < len(args) {
				stages = args[i+2]
			}
			if stat < 0 || stat > 5 {
				stat = 0
			}
			if rand.Intn(100) >= chance {
				continue
			}
			if stages < 0 {
				if enemyBattleLv != nil {
					cur := int(enemyBattleLv[stat])
					cur += stages
					if statDropImmune && cur < 0 {
						cur = 0
					} else if cur < minStatStage {
						cur = minStatStage
					}
					enemyBattleLv[stat] = int8(cur)
				}
			} else {
				if playerBattleLv != nil {
					cur := int(playerBattleLv[stat])
					cur += stages
					if cur > maxStatStage {
						cur = maxStatStage
					}
					playerBattleLv[stat] = int8(cur)
				}
			}
		}

	case 6:
		// 反伤：自身受到伤害的一定比例
		divisor := 4
		if len(args) >= 1 {
			divisor = args[0]
		}
		if divisor <= 0 {
			divisor = 4
		}
		recoil := uint32(math.Floor(float64(damage) / float64(divisor)))
		if recoil > 0 && playerHP != nil {
			if *playerHP <= recoil {
				recoil = *playerHP
				*playerHP = 0
			} else {
				*playerHP -= recoil
			}
			recoilDamage = recoil
		}

	case 7:
		// 同生共死（Lua eid=7）
		// 尤纳斯/哈莫雷特/盖亚/塔克林/塔西亚/雷伊 免疫此效果
		if sptboss.IsSameLifeDeathImmune(defenderPetID) {
			break
		}
		// 需求：直接伤害且只在敌方血量高于我方时生效：
		// - 若 enemyHP > playerHP，则伤害 = enemyHP - playerHP，本质效果是“把对方血量压到与自己相同”
		// - 若 enemyHP <= playerHP，则不造成伤害（伤害=0，不会给对方回血）
		if playerHP != nil && enemyHP != nil {
			// 仅当敌方当前 HP 大于我方当前 HP 时生效
			if *enemyHP > *playerHP {
				original := *enemyHP
				target := *playerHP
				// 上限依然不能超过敌方最大 HP；下限为 0
				if target > enemyMaxHP {
					target = enemyMaxHP
				}
				*enemyHP = target
				// 这里的“直接伤害”由 HP 变化体现（damage 由外部流程记录），不在此额外修改 damage 变量
				_ = original // 预留给后续若需要记录实际伤害值时使用
			}
		}

	case 8:
		// 手下留情：对方HP至少保留1（在 handlers 里对伤害做上限处理，此处不修改HP）

	case 9:
		// 愤怒：受到伤害在范围内则攻击+1级（Lua eid=9）SideEffectArg: minDamage maxDamage
		minD, maxD := 20, 80
		if len(args) >= 1 {
			minD = args[0]
		}
		if len(args) >= 2 {
			maxD = args[1]
		}
		if playerBattleLv != nil && damage >= uint32(minD) && damage <= uint32(maxD) {
			cur := int(playerBattleLv[StatAttack])
			cur++
			if cur > maxStatStage {
				cur = maxStatStage
			}
			playerBattleLv[StatAttack] = int8(cur)
		}

	case 10:
		// 麻痹：客户端 status[0]=持续回合数；所有 BOSS 免疫控制类异常
		if sptboss.IsControlImmune(defenderPetID) {
			break
		}
		if !statusImmune {
			chance := 10
			if len(args) >= 1 {
				chance = args[0]
			}
			if rand.Intn(100) < chance && enemyStatus != nil && enemyStatus[StatusIndexParalysis] == 0 {
				enemyStatus[StatusIndexParalysis] = randomStatusRounds()
			}
		}

	case 12:
		// 烧伤：客户端 status[2]=持续回合数（火焰漩涡等）
		if !statusImmune {
			chance := 10
			if len(args) >= 1 {
				chance = args[0]
			}
			if rand.Intn(100) < chance && enemyStatus != nil && enemyStatus[StatusIndexBurn] == 0 {
				enemyStatus[StatusIndexBurn] = randomDoTRounds()
				applyNewStatusDamageOnce(enemyHP, enemyMaxHP)
			}
		}

	case 11, 13:
		// 中毒：客户端 status[1]=持续回合数
		if !statusImmune {
			chance := 10
			if len(args) >= 1 {
				chance = args[0]
			}
			if rand.Intn(100) < chance && enemyStatus != nil && enemyStatus[StatusIndexPoison] == 0 {
				enemyStatus[StatusIndexPoison] = randomDoTRounds()
				applyNewStatusDamageOnce(enemyHP, enemyMaxHP)
			}
		}

	case 14:
		// 冻伤：客户端 status[5]=持续回合数
		if !statusImmune {
			chance := 10
			if len(args) >= 1 {
				chance = args[0]
			}
			if rand.Intn(100) < chance && enemyStatus != nil && enemyStatus[StatusIndexFreeze] == 0 {
				enemyStatus[StatusIndexFreeze] = randomDoTRounds()
				applyNewStatusDamageOnce(enemyHP, enemyMaxHP)
			}
		}

	case 15:
		// 畏缩：对方本回合无法行动（Lua eid=15）客户端 status[6]=害怕；所有 BOSS 免疫控制类异常
		if sptboss.IsControlImmune(defenderPetID) {
			break
		}
		if !statusImmune {
			chance := 10
			if len(args) >= 1 {
				chance = args[0]
			}
			if rand.Intn(100) < chance && enemyStatus != nil {
				enemyStatus[StatusIndexFear] = randomStatusRounds()
			}
		}

	case 16:
		// 睡眠：客户端 status[8]=持续回合数，效果与麻痹类似（本回合无法行动）；所有 BOSS 免疫控制类异常
		if sptboss.IsControlImmune(defenderPetID) {
			break
		}
		if !statusImmune {
			chance := 10
			if len(args) >= 1 {
				chance = args[0]
			}
			if rand.Intn(100) < chance && enemyStatus != nil && enemyStatus[StatusIndexSleep] == 0 {
				enemyStatus[StatusIndexSleep] = randomStatusRounds()
			}
		}

	case 20:
		// 疲惫：使用后下回合无法行动（Lua eid=20）客户端 status[7]=疲惫
		chance := 100
		if len(args) >= 1 {
			chance = args[0]
		}
		if rand.Intn(100) < chance && playerStatus != nil {
			// 异常状态统一 1~2 回合随机
			playerStatus[StatusIndexFatigue] = randomStatusRounds()
		}

	case 29:
		// 额外增加 n 点固定伤害（SideEffectArg 第一个数为固定伤害值）
		fixedDmg := 0
		if len(args) >= 1 {
			fixedDmg = args[0]
		}
		if fixedDmg > 0 && enemyHP != nil {
			d := uint32(fixedDmg)
			if d > *enemyHP {
				d = *enemyHP
			}
			*enemyHP -= d
		}

	case 33:
		// 消除对手能力提升状态（对应 skills.xml 中 Move.SideEffect=33，例如“净化/清除术/燃烧殆尽”等）
		// 规则：仅清除对方“强化”(正向能力等级)，弱化(负向)保持不变
		if enemyBattleLv != nil {
			for i := 0; i < 6; i++ {
				if enemyBattleLv[i] > 0 {
					enemyBattleLv[i] = 0
				}
			}
		}
	}

	return gainHP, recoilDamage
}

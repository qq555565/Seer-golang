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
	StatusIndexPetrify   = 9 // 石化（1000094）
	StatusIndexConfusion = 10 // 混乱（1000099）
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

// ParseSideEffectIds 解析 SideEffect 字符串为效果 ID 列表，如 "4 4 4 438" -> [4,4,4,438]
// 用于多效果技能（如魂之再生 43 508、六道轮回 4 4 4 4 4 438）按顺序分配 SideEffectArg
func ParseSideEffectIds(sideEffect string) []int {
	var ids []int
	for _, part := range strings.Fields(strings.TrimSpace(sideEffect)) {
		id, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// EffectArgCount 返回单次应用该效果时消耗的参数个数（用于多效果时从 SideEffectArg 中切分）
// 供 handlers 等多效果循环使用；4/5: 每组 stat chance stages=3；2: stat stages=2；43/508: 1；438/687/1635: 2；其余默认 1
func EffectArgCount(eid int) int {
	return effectArgCount(eid)
}

func effectArgCount(eid int) int {
	switch eid {
	case 2:
		return 2
	case 3, 4, 5:
		return 3
	case 33:
		return 0 // 消除对手能力提升，无参数
	case 43, 508:
		return 1
	case 66, 67:
		return 1 // 66: 击败回血除数n；67: 减少对方下只最大HP除数n
	case 87:
		return 0 // 恢复自身所有PP，无参数
	case 57, 438, 687, 1635:
		return 2
	case 20:
		return 2 // 疲惫 chance rounds
	case 21:
		return 3 // m n k 回合与反弹除数
	case 22:
		return 2 // n% 令对手疲惫 m 回合
	case 32:
		return 1 // n 回合
	case 439:
		return 2 // n回合 m固定伤害
	case 448:
		return 2 // n回合 每回合降低等级数
	case 478:
		return 1 // n回合
	case 545:
		return 3 // n回合 m伤害阈值 XX类型（能力或异常）
	case 454:
		return 2 // n=血量比例除数 m=先制加值
	case 482:
		return 2 // m% n先制
	case 488:
		return 2 // threshold 体力阈值 percent 伤害增加百分比
	case 41, 42:
		return 2 // 41 火抗 42 电伤×2，常用 n 回合或两参
	case 44, 45, 46, 47, 48, 49, 50, 51, 52, 55, 56:
		return 1 // 44 特防减半 45 防御同对手 46 挡n次 47 免疫能力下降 48 免疫异常 49 吸收n点 50 物防减半 51 攻击同对手 52 先手miss 55 属性反转 56 属性相同
	case 53, 54:
		return 2 // 53 伤害 m 倍 n 回合 54 受伤 1/m n 回合
	case 65:
		return 3 // n 回合 某属性威力 m 倍 elemType
	case 59:
		return 2 // 牺牲强化，至少一对 stat stages，多效果时用第一对
	case 62:
		return 1 // 镇魂歌 n 回合
	case 68:
		return 1 // 致死留 1 血 n 回合
	case 69:
		return 1 // 药剂反噬 n 回合
	case 71, 72:
		return 0 // 71 牺牲暴击 72 Miss 死亡，无参数
	case 73:
		return 1 // 73 先手反弹 n 回合
	case 60:
		return 2 // n 回合 每回合 m 点固定伤害
	case 76:
		return 3 // m% 几率 n 回合 每回合 k 点固定伤害
	case 77:
		return 2 // n 回合 每次使用技能恢复 m 点体力
	case 78:
		return 1 // n 回合物理攻击对自身必定 miss
	case 83:
		return 0 // 无参数，按自身性别
	case 84:
		return 2 // n 回合 m% 几率
	case 86, 106:
		return 1 // n 回合
	case 89, 90:
		return 2 // n 回合 m 除数/倍数
	case 92:
		return 2 // n 回合 m% 几率
	case 98:
		return 2 // n 回合 m 倍数
	case 103:
		return 1 // n% 几率
	case 104:
		return 2 // n 回合 m% 几率
	case 107:
		return 2 // n 伤害阈值 stat 能力索引
	case 108, 109:
		return 2 // n 回合 m% 几率
	// 110~199 新增效果
	case 122, 148, 158, 175, 184, 186:
		return 3 // stat chance stages
	case 129, 131, 133, 135, 141, 167, 172, 179, 193, 451, 461, 464, 472:
		return 1 // 单参数
	case 130, 147, 154, 173, 410, 418, 430, 437, 449, 450, 455, 460, 463, 467, 475:
		return 2 // 双参数
	case 181, 465:
		return 4 // statusIndex/chance increment maxChance 或 chance rounds increment maxChance
	case 182:
		return 4 // statusIndex stat chance stages
	case 473, 474:
		return 3 // threshold stat stages 或 stat chance stages
	// 400~499 新增效果
	case 402, 405, 413, 422, 428, 434, 436, 447, 453, 456, 458, 459, 466, 476, 489:
		return 1 // 单参数
	case 415:
		return 2 // threshold heal
	case 485, 487, 494:
		return 0 // 无参数
	case 468:
		return 0 // 自身能力下降时威力翻倍并解除能力下降，无参数（handlers 伤害计算）
	case 401:
		return 0 // 多效果链中出现（如 33 401），无参数，占位
	case 495:
		return 2 // statusIndex chance
	// 88: n% chance damage m倍; 35/40/61/70/118/139: handlers; 91: n回合
	case 88:
		return 2 // chance mult
	case 91:
		return 1 // n 回合
	case 110:
		return 3 // n回合 m% stat
	case 111, 113, 132:
		return 0 // 无参数（handlers 伤害计算）
	case 112:
		return 0 // 无参数
	case 114:
		return 1 // chance
	case 115:
		return 2 // chance divisor
	case 116:
		return 1 // n 回合
	case 117:
		return 2 // n回合 m%
	case 118:
		return 0 // 无参数（威力随机，handlers 处理）
	case 119:
		return 0 // 无参数（奇偶伤害判断，无需外部参数）
	case 120:
		return 1 // n 除数
	case 121:
		return 1 // chance
	case 124:
		return 2 // chance stages
	case 125:
		return 2 // n回合 m上限
	case 126:
		return 2 // n回合 m级
	case 127:
		return 2 // chance rounds（n% 概率 m 回合伤害减半）
	case 128:
		return 1 // n 回合
	case 134:
		return 2 // threshold ppBonus
	case 136:
		return 1 // divisor
	case 144:
		return 1 // n 回合（下一只免疫异常）
	case 145:
		return 1 // divisor
	case 146:
		return 2 // n 回合 m% 几率
	case 149:
		return 4 // chance1 statusIdx1 chance2 statusIdx2
	case 150:
		return 2 // n 回合 m 等级
	case 151:
		return 2 // hitChance missChance
	case 159:
		return 3 // divisor chance statusIndex
	case 162:
		return 1 // bonus
	case 168:
		return 0 // 无参数（handlers 伤害计算）
	case 178:
		return 2 // divisor sameDivisor
	case 180:
		return 0 // 无参数（handlers 处理）
	case 188:
		return 0 // 无参数
	case 192:
		return 1 // percent
	case 195:
		return 0 // 无参数（无视对手双防提升）
	case 201:
		return 1 // divisor（组队回血除数）
	case 194:
		return 4 // divisor statusIndex statusDivisor (unused4)
	case 196:
		return 6 // chance stat stages extraChance extraStat extraStages
	case 123:
		return 3 // n 回合 stat 能力索引 stages 等级
	case 185:
		return 1 // statusIndex，击败时若对手有该状态则下一只出场也挂该状态
	// 400+ 新增
	case 421:
		return 0 // 无参数
	case 429:
		return 3 // base increment max
	case 431:
		return 0 // 无参数（handlers 伤害计算）
	case 441:
		return 2 // n% m%
	case 444:
		return 0 // 无参数
	case 445:
		return 0 // 无参数
	case 471:
		return 1 // n 回合
	case 484:
		return 3 // times bonus cap
	case 490:
		return 2 // threshold stages
	// BOSS/特殊多效果：参数数量按 skills.xml 多效果技能对齐（详见 docs/SKILL_EFFECTS_STATUS.md）
	case 691:
		return 1 // 五衰末尾
	case 700:
		return 1 // 痴愚
	case 773, 935:
		return 0 // 简/极 无独立参，与 1470 976 共用两参
	case 976:
		return 1 // 简/极 第二参
	case 1083, 1248, 1257:
		return 1
	case 1211:
		return 2 // 希 双参
	case 1470:
		return 1 // 简/极 首参
	case 1603:
		return 2 // 谄诳/红莲等 两参
	case 1605, 1850, 1925:
		return 1
	case 2236, 2237:
		return 1 // 希
	default:
		return 1
	}
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
	playerBattleLv, enemyBattleLv *[6]int8, playerStatus, enemyStatus *[20]byte, defenderPetID int,
	defenderImmuneStatDropRounds, defenderImmuneStatusRounds byte) (gainHP int32, recoilDamage uint32) {
	if skill == nil {
		return 0, 0
	}
	args := ParseSideEffectArg(skill.SideEffectArg)
	eids := ParseSideEffectIds(skill.SideEffect)
	if len(eids) == 0 {
		if skill.EffectID <= 0 {
			return 0, 0
		}
		eids = []int{skill.EffectID}
	}
	statusImmune := sptboss.IsStatusImmune(defenderPetID)
	statDropImmune := sptboss.IsStatDropImmune(defenderPetID)
	if defenderImmuneStatDropRounds > 0 {
		statDropImmune = true
	}
	if defenderImmuneStatusRounds > 0 {
		statusImmune = true
	}
	var totalGainHP int32
	var totalRecoil uint32
	offset := 0
	for _, eid := range eids {
		if eid <= 0 {
			continue
		}
		n := effectArgCount(eid)
		if offset+n > len(args) {
			break
		}
		subArgs := args[offset : offset+n]
		offset += n
		g, r := applyOneEffect(eid, subArgs, damage, playerHP, enemyHP, playerMaxHP, enemyMaxHP,
			playerBattleLv, enemyBattleLv, playerStatus, enemyStatus, defenderPetID,
			statusImmune, statDropImmune)
		totalGainHP += g
		totalRecoil += r
	}
	return totalGainHP, totalRecoil
}

// applyOneEffect 应用单个效果 ID 与对应参数，返回本效果产生的回复与反伤（供 ApplyEffect 多效果循环调用）
func applyOneEffect(eid int, args []int, damage uint32, playerHP, enemyHP *uint32, playerMaxHP, enemyMaxHP uint32,
	playerBattleLv, enemyBattleLv *[6]int8, playerStatus, enemyStatus *[20]byte, defenderPetID int,
	statusImmune, statDropImmune bool) (gainHP int32, recoilDamage uint32) {
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
		// 1000003 解除能力下降（无参数时）/ 1000004 提高自身能力等级（有 stat chance stages 时）
		if len(args) == 0 || (len(args) >= 1 && args[0] == 0 && len(args) < 3) {
			// 解除自身所有能力下降状态（如高速旋转）
			if playerBattleLv != nil {
				for i := 0; i < 6; i++ {
					if playerBattleLv[i] < 0 {
						playerBattleLv[i] = 0
					}
				}
			}
		} else {
			// 提高自身能力等级（同 case 4）；SideEffectArg 可多组 stat chance stages
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

	case 21:
		// 1000021: m~n 回合每回合反弹对手 1/k 伤害；在 handlers 中受击时反弹并回合末递减

	case 22:
		// 1000022: n%令对手疲惫m回合
		if sptboss.IsControlImmune(defenderPetID) {
			break
		}
		if !statusImmune && enemyStatus != nil {
			chance, rounds := 30, 1
			if len(args) >= 1 {
				chance = args[0]
			}
			if len(args) >= 2 {
				rounds = args[1]
			}
			if rounds < 1 {
				rounds = 1
			}
			if rounds > 5 {
				rounds = 5
			}
			if rand.Intn(100) < chance {
				enemyStatus[StatusIndexFatigue] = byte(rounds)
			}
		}

	case 28:
		// 1000028: 降低对方 1/n 的 体力（按对方最大体力比例）
		if enemyHP != nil && enemyMaxHP > 0 {
			divisor := 4
			if len(args) >= 1 && args[0] > 0 {
				divisor = args[0]
			}
			dmg := enemyMaxHP / uint32(divisor)
			if dmg > *enemyHP {
				dmg = *enemyHP
			}
			*enemyHP -= dmg
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

	case 30:
		// 冲撞/土龙闪/水之牙：概率麻痹（与 effect 10 类似）
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

	case 34:
		// 克制：反弹上回合受到伤害的倍数，固定伤害由 handlers 在伤害阶段计算，此处不修改状态

	case 35:
		// 惩罚/烈冲破：对方能力等级越高伤害越高，伤害公式在 handlers 中处理，此处不修改状态

	case 36:
		// 秒杀（如极度冰点）：命中时 n% 概率秒杀，由 handlers 在伤害阶段处理

	case 37:
		// 惊雷切/空掌破：降低对方能力等级，SideEffectArg 如 "2 2" 表示 stat 2（特防）降 2 级
		// 注：1000037 描述为“自身体力小于1/n时威力为m倍”，伤害在 handlers 处理；此处实现“降能力”以匹配技能 惊雷切/空掌破
		stat, stages := 1, 1
		if len(args) >= 1 {
			stat = args[0]
		}
		if len(args) >= 2 {
			stages = args[1]
		}
		if stat < 0 || stat > 5 {
			stat = 1
		}
		if stages > 0 {
			stages = -stages
		}
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

	case 32:
		// 1000032: n 回合暴击率 +1/16；在 handlers 暴击判定与回合末递减
	case 38:
		// 1000038: 降低对方 n 点体力（固定伤害，n 为 SideEffectArg 第一参数）
		if enemyHP != nil && len(args) >= 1 && args[0] > 0 {
			d := uint32(args[0])
			if d > *enemyHP {
				d = *enemyHP
			}
			*enemyHP -= d
		}
	case 31:
		// 1000031: 1回合做m~n次攻击；命中与次数在 handlers 伤害/多段逻辑中处理，此处无状态修改
	case 39:
		// 1000039: 降低对手技能 PP 等；在 handlers 中按几率扣 PP
	case 40:
		// 1000040: 先出手威力 2 倍；在 handlers 伤害计算中处理
	case 41, 42, 44, 45, 46, 47, 48, 49, 50:
		// 1000041~50: 多回合 BUFF（火抗/电伤×2/特防减半/防御同对手/挡n次/免疫能力下降/免疫异常/吸收n点/物防减半）；在 handlers 中设置回合并递减

	case 43:
		// 1000043: 恢复自身最大体力的1/n
		if playerHP != nil && playerMaxHP > 0 {
			divisor := 2
			if len(args) >= 1 && args[0] > 0 {
				divisor = args[0]
			}
			heal := playerMaxHP / uint32(divisor)
			sum := *playerHP + heal
			if sum > playerMaxHP {
				sum = playerMaxHP
			}
			*playerHP = sum
			gainHP = int32(heal)
		}

	case 79:
		// 1000079: 损失1/2的体力，提升自身能力；SideEffectArg 可为多组 stat chance stages
		if playerHP != nil && playerMaxHP > 0 {
			half := playerMaxHP / 2
			if half > *playerHP {
				half = *playerHP
			}
			*playerHP -= half
			recoilDamage = half
		}
		if playerBattleLv != nil {
			for i := 0; i+2 < len(args); i += 3 {
				stat, chance, stages := args[i], 100, 1
				if i+1 < len(args) {
					chance = args[i+1]
				}
				if i+2 < len(args) {
					stages = args[i+2]
				}
				if stat >= 0 && stat <= 5 && rand.Intn(100) < chance {
					cur := int(playerBattleLv[stat])
					cur += stages
					if cur > maxStatStage {
						cur = maxStatStage
					}
					playerBattleLv[stat] = int8(cur)
				}
			}
			if len(args) < 3 {
				// 无参数时默认攻击+1
				cur := int(playerBattleLv[StatAttack])
				if cur < maxStatStage {
					playerBattleLv[StatAttack] = int8(cur + 1)
				}
			}
		}
	case 80:
		// 1000080: 损失1/2的体力，给于对手同等的伤害
		if playerHP != nil && enemyHP != nil && playerMaxHP > 0 {
			half := playerMaxHP / 2
			if half > *playerHP {
				half = *playerHP
			}
			*playerHP -= half
			recoilDamage = half
			dmg := half
			if dmg > *enemyHP {
				dmg = *enemyHP
			}
			*enemyHP -= dmg
		}

	case 93:
		// 1000093: n%几率额外附加m点固定伤害
		if len(args) >= 2 && rand.Intn(100) < args[0] && enemyHP != nil {
			m := uint32(args[1])
			if m > *enemyHP {
				m = *enemyHP
			}
			*enemyHP -= m
		}
	case 94:
		// 1000094: n%令对方石化
		if sptboss.IsControlImmune(defenderPetID) {
			break
		}
		if !statusImmune && enemyStatus != nil {
			chance := 10
			if len(args) >= 1 {
				chance = args[0]
			}
			if rand.Intn(100) < chance {
				enemyStatus[StatusIndexPetrify] = randomStatusRounds()
			}
		}
	case 99:
		// 1000099: n%几率令对手混乱（用畏缩 1 回合表示无法行动）
		if sptboss.IsControlImmune(defenderPetID) {
			break
		}
		if !statusImmune && enemyStatus != nil {
			chance := 10
			if len(args) >= 1 {
				chance = args[0]
			}
			if rand.Intn(100) < chance {
				enemyStatus[StatusIndexConfusion] = 1
			}
		}
	case 101:
		// 1000101: 造成伤害的 n% 恢复自身体力
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
	case 105:
		// 1000105: 给予对象损伤的1/n，会回复自己的体力
		divisor := 2
		if len(args) >= 1 && args[0] > 0 {
			divisor = args[0]
		}
		heal := uint32(math.Floor(float64(damage) / float64(divisor)))
		if heal > 0 && playerHP != nil {
			sum := *playerHP + heal
			if sum > playerMaxHP {
				sum = playerMaxHP
			}
			*playerHP = sum
			gainHP = int32(heal)
		}

	// ----- 51~62: 回合/条件/威力类，多在 handlers 处理 -----
	case 51, 52, 53:
		// 1000051~53: 攻击同对手/先手 miss/伤害 m 倍；在 handlers 中设置回合并递减
	case 54, 55, 56:
		// 1000054~56: 受伤 1/m/属性反转/属性相同；在 handlers 中设置回合并递减
	case 57:
		// 1000057: n回合内每回合使用技能恢复自身最大体力的1/m；当回合先恢复一次
		if playerHP != nil && playerMaxHP > 0 {
			divisor := 4
			if len(args) >= 2 && args[1] > 0 {
				divisor = args[1]
			} else if len(args) >= 1 && args[0] > 0 {
				divisor = args[0]
			}
			heal := playerMaxHP / uint32(divisor)
			sum := *playerHP + heal
			if sum > playerMaxHP {
				sum = playerMaxHP
			}
			*playerHP = sum
			gainHP = int32(heal)
		}
	case 58, 59:
		// 1000058~59: 暴击强化/牺牲强化下一只；在 handlers 中设置状态与回合
	case 60:
		// 1000060: n 回合内每回合附加 m 点固定伤害；在 handlers 中设置并出手后结算
	case 61, 62:
		// 1000061~62: 威力随机/镇魂歌；在 handlers 中处理
	case 63:
		// 1000063: 将能力下降状态反馈给对手（己方负的 battleLv 转为对手的负等级）
		if playerBattleLv != nil && enemyBattleLv != nil && !statDropImmune {
			for i := 0; i < 6; i++ {
				if playerBattleLv[i] < 0 {
					cur := int(enemyBattleLv[i])
					cur += int(playerBattleLv[i]) // 加上负值即降低
					if cur < minStatStage {
						cur = minStatStage
					}
					enemyBattleLv[i] = int8(cur)
				}
			}
		}
	case 64, 65, 66, 67, 68, 69:
		// 1000064~69: 异常时伤害加倍/属性威力倍/击败回血/击败减对方下只最大HP/致死留1血/药剂反噬 等，需伤害阶段或回合
	case 70, 71, 72, 73:
		// 1000070~73: 威力随机/牺牲暴击/ miss死亡/先手反弹 等，需伤害或回合
	case 74:
		// 1000074: 10%中毒、10%烧伤、10%冻伤（剩下70%无效果），随机一种
		if !statusImmune && enemyStatus != nil {
			r := rand.Intn(100)
			if r < 10 {
				enemyStatus[StatusIndexPoison] = randomDoTRounds()
				applyNewStatusDamageOnce(enemyHP, enemyMaxHP)
			} else if r < 20 {
				enemyStatus[StatusIndexBurn] = randomDoTRounds()
				applyNewStatusDamageOnce(enemyHP, enemyMaxHP)
			} else if r < 30 {
				enemyStatus[StatusIndexFreeze] = randomDoTRounds()
				applyNewStatusDamageOnce(enemyHP, enemyMaxHP)
			}
		}
	case 75:
		// 1000075: 10%麻痹、10%睡眠、10%害怕（剩下70%无效果），随机一种；控制类 BOSS 免疫
		if sptboss.IsControlImmune(defenderPetID) {
			break
		}
		if !statusImmune && enemyStatus != nil {
			r := rand.Intn(100)
			if r < 10 {
				if enemyStatus[StatusIndexParalysis] == 0 {
					enemyStatus[StatusIndexParalysis] = randomStatusRounds()
				}
			} else if r < 20 {
				if enemyStatus[StatusIndexSleep] == 0 {
					enemyStatus[StatusIndexSleep] = randomStatusRounds()
				}
			} else if r < 30 {
				enemyStatus[StatusIndexFear] = randomStatusRounds()
			}
		}
	case 76:
		// 1000076: m% 几率 n 回合每回合 k 点固定伤害；在 handlers 中设置并回合末递减
	case 77:
		// 1000077: n 回合内每次使用技能恢复 m 点体力；在 handlers 中设置、使用技能时恢复并回合末递减
	case 78:
		// 1000078: n 回合内物理攻击对自身必定 miss；在 handlers 中设置并回合末递减
	case 81:
		// 1000081: 下 n 回合必中；在 handlers 中设置
	case 82:
		// 1000082: 目标为雄性伤害 200%、雌性 50%；在 handlers 伤害计算中处理
	case 83:
		// 1000083: 自身雄性下两回合必定先手；雌性下两回合必定暴击；在 handlers 中设置
	case 84:
		// 1000084: n 回合内受到物理攻击时 m% 几率将对手麻痹；在 handlers 中设置并受击时判定
	case 86:
		// 1000086: n 回合内属性（特殊）攻击对自身必定 miss；在 handlers 中设置
	case 687:
		// 1000687: 若对手处于异常状态，则造成伤害的 n% 恢复自身体力；SideEffectArg: 通常 n 为百分比（如 100 表示 100%）
		if enemyStatus != nil && playerHP != nil && playerMaxHP > 0 && damage > 0 {
			hasStatus := enemyStatus[StatusIndexParalysis] > 0 || enemyStatus[StatusIndexPoison] > 0 ||
				enemyStatus[StatusIndexBurn] > 0 || enemyStatus[StatusIndexFreeze] > 0 ||
				enemyStatus[StatusIndexFear] > 0 || enemyStatus[StatusIndexFatigue] > 0 ||
				enemyStatus[StatusIndexSleep] > 0 || enemyStatus[StatusIndexPetrify] > 0 ||
				enemyStatus[StatusIndexConfusion] > 0
			if hasStatus {
				percent := 100
				if len(args) >= 1 && args[0] > 0 {
					percent = args[0]
				}
				if len(args) >= 2 && args[1] > 0 {
					percent = args[1]
				}
				heal := uint32(math.Floor(float64(damage) * float64(percent) / 100))
				if heal > 0 {
					sum := *playerHP + heal
					if sum > playerMaxHP {
						sum = playerMaxHP
					}
					*playerHP = sum
					gainHP = int32(heal)
				}
			}
		}
	case 85:
		// 1000085: 使对手的能力提升效果转化到自己身上
		if playerBattleLv != nil && enemyBattleLv != nil {
			for i := 0; i < 6; i++ {
				if enemyBattleLv[i] > 0 {
					cur := int(playerBattleLv[i])
					cur += int(enemyBattleLv[i])
					if cur > maxStatStage {
						cur = maxStatStage
					}
					playerBattleLv[i] = int8(cur)
					enemyBattleLv[i] = 0 // 消除对手该强化
				}
			}
		}
	case 87:
		// 1000087: 恢复自身所有 PP；在 handlers 多效果循环中处理
	case 91:
		// 1000091: n 回合内双方状态变化同时影响自己和对手；在 handlers 中实现
	case 89, 90, 92:
		// 1000089: 吸血 damage/divisor；1000090: 伤害 m 倍（同53）；1000092: 受击 m% 冻伤；在 handlers 中设置并递减
	case 95:
		// 1000095: 对手处于睡眠状态时致命一击率提升 n/16；在 handlers 暴击判定
	case 96:
		// 1000096: 对手处于烧伤状态时威力翻倍；在 handlers 伤害计算
	case 97:
		// 1000097: 对手处于冻伤状态时威力翻倍；在 handlers 伤害计算
	case 98:
		// 1000098: n 回合内对雄性精灵的伤害为 m 倍；在 handlers 伤害计算与递减
	case 100:
		// 1000100: 自身体力越少则威力越大；在 handlers 伤害计算
	case 102:
		// 1000102: 对手处于麻痹状态时威力翻倍；在 handlers 伤害计算
	case 103:
		// 1000103: n% 几率令对手增加一层衰弱（随机能力 -1）
		if enemyBattleLv != nil && !statDropImmune {
			chance := 10
			if len(args) >= 1 {
				chance = args[0]
			}
			if chance > 100 {
				chance = 100
			}
			if rand.Intn(100) < chance {
				stat := rand.Intn(6)
				cur := int(enemyBattleLv[stat])
				cur--
				if cur < minStatStage {
					cur = minStatStage
				}
				enemyBattleLv[stat] = int8(cur)
			}
		}
	case 104:
		// 1000104: n 回合内每次直接攻击 m% 几率附带衰弱；在 handlers 中设置并攻击时判定
	case 106:
		// 1000106: n 回合内属性攻击对自身必定 miss（同 86）；在 handlers 中设置
	case 107:
		// 1000107: 若本次攻击造成的伤害小于 n 则自身 xx 等级+1；在 handlers 伤害应用后判定
	case 108:
		// 1000108: n 回合内受到物理攻击时 m% 几率将对手烧伤；在 handlers 中设置并受击时判定
	case 109:
		// 1000109: n 回合内造成伤害时 m% 几率令对手冻伤；在 handlers 中设置并造成伤害时判定
	case 114:
		// 1000114: n% 概率使对方烧伤
		if !statusImmune && enemyStatus != nil {
			chance := 30
			if len(args) >= 1 && args[0] > 0 {
				chance = args[0]
			}
			if rand.Intn(100) < chance && enemyStatus[StatusIndexBurn] == 0 {
				enemyStatus[StatusIndexBurn] = randomDoTRounds()
				applyNewStatusDamageOnce(enemyHP, enemyMaxHP)
			}
		}
	case 121:
		// 1000121: 命中时同时 n% 概率使对方麻痹
		if sptboss.IsControlImmune(defenderPetID) {
			break
		}
		if !statusImmune && enemyStatus != nil {
			chance := 30
			if len(args) >= 1 && args[0] > 0 {
				chance = args[0]
			}
			if rand.Intn(100) < chance && enemyStatus[StatusIndexParalysis] == 0 {
				enemyStatus[StatusIndexParalysis] = randomStatusRounds()
			}
		}
	case 124:
		// 1000124: n% 概率使对方随机一个能力降低 m 级
		if enemyBattleLv != nil && !statDropImmune {
			chance, stages := 30, 1
			if len(args) >= 1 && args[0] > 0 {
				chance = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				stages = args[1]
			}
			if rand.Intn(100) < chance {
				stat := rand.Intn(6)
				cur := int(enemyBattleLv[stat]) - stages
				if cur < minStatStage {
					cur = minStatStage
				}
				enemyBattleLv[stat] = int8(cur)
			}
		}
	case 120:
		// 1000120: 50% 对方减血 1/n，50% 自己减血 1/n
		divisor := 4
		if len(args) >= 1 && args[0] > 0 {
			divisor = args[0]
		}
		if rand.Intn(2) == 0 {
			if enemyHP != nil && enemyMaxHP > 0 {
				dmg := enemyMaxHP / uint32(divisor)
				if dmg > *enemyHP {
					dmg = *enemyHP
				}
				*enemyHP -= dmg
			}
		} else {
			if playerHP != nil && playerMaxHP > 0 {
				dmg := playerMaxHP / uint32(divisor)
				if dmg > *playerHP {
					dmg = *playerHP
				}
				*playerHP -= dmg
				recoilDamage = dmg
			}
		}
	case 112:
		// 1000112: 造成固定 250~300 伤害；若为致命伤则对手剩余 1 点体力
		if enemyHP != nil && *enemyHP > 0 {
			dmg := uint32(250 + rand.Intn(51))
			if dmg >= *enemyHP && *enemyHP > 1 {
				dmg = *enemyHP - 1
			}
			if dmg > *enemyHP {
				dmg = *enemyHP
			}
			*enemyHP -= dmg
		}
	case 139:
		// 1000139: 50%造成301-350，30%造成101-300，20%造成5-100（固定伤害）
		if enemyHP != nil && *enemyHP > 0 {
			r := rand.Intn(100)
			var dmg uint32
			if r < 50 {
				dmg = uint32(301 + rand.Intn(50))
			} else if r < 80 {
				dmg = uint32(101 + rand.Intn(200))
			} else {
				dmg = uint32(5 + rand.Intn(96))
			}
			if dmg > *enemyHP {
				dmg = *enemyHP
			}
			*enemyHP -= dmg
		}
	case 162:
		// 1000162: 若自身处于异常状态，则附加 n 点固定伤害
		if enemyHP != nil && playerStatus != nil {
			hasStatus := false
			for i := 0; i < 20; i++ {
				if playerStatus[i] > 0 {
					hasStatus = true
					break
				}
			}
			if hasStatus {
				bonus := uint32(50)
				if len(args) >= 1 && args[0] > 0 {
					bonus = uint32(args[0])
				}
				if bonus > *enemyHP {
					bonus = *enemyHP
				}
				*enemyHP -= bonus
			}
		}
	case 145:
		// 1000145: 若对手处于睡眠状态，则每次攻击恢复 1/n 最大体力
		if playerHP != nil && playerMaxHP > 0 && enemyStatus != nil && enemyStatus[StatusIndexSleep] > 0 {
			divisor := 4
			if len(args) >= 1 && args[0] > 0 {
				divisor = args[0]
			}
			heal := playerMaxHP / uint32(divisor)
			if heal > 0 {
				sum := *playerHP + heal
				if sum > playerMaxHP {
					sum = playerMaxHP
				}
				*playerHP = sum
				gainHP = int32(heal)
			}
		}
	case 151:
		// 1000151: 命中时 n% 疲惫 1 回合；未命中时 m% 疲惫 1 回合（命中由 damage>0 判断）
		if sptboss.IsControlImmune(defenderPetID) {
			break
		}
		if !statusImmune && enemyStatus != nil {
			hitChance, missChance := 30, 20
			if len(args) >= 1 && args[0] > 0 {
				hitChance = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				missChance = args[1]
			}
			chance := missChance
			if damage > 0 {
				chance = hitChance
			}
			if rand.Intn(100) < chance && enemyStatus[StatusIndexFatigue] == 0 {
				enemyStatus[StatusIndexFatigue] = 1
			}
		}
	case 159:
		// 1000159: 当对手体力小于最大值的 1/n 时，m% 概率令对手 XX
		if enemyHP != nil && enemyMaxHP > 0 && enemyStatus != nil && !statusImmune {
			divisor, chance, statusIdx := 4, 50, StatusIndexBurn
			if len(args) >= 1 && args[0] > 0 {
				divisor = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				chance = args[1]
			}
			if len(args) >= 3 {
				statusIdx = args[2]
			}
			if *enemyHP < enemyMaxHP/uint32(divisor) && statusIdx >= 0 && statusIdx < 20 {
				if rand.Intn(100) < chance && enemyStatus[statusIdx] == 0 {
					enemyStatus[statusIdx] = randomStatusRounds()
				}
			}
		}
	case 178:
		// 1000178: 造成伤害的 1/n 恢复体力；若属性相同则造成伤害的 1/m 恢复体力（属性判断在 handlers，此处按 1/n 处理）
		if playerHP != nil && playerMaxHP > 0 && damage > 0 {
			divisor := 2
			if len(args) >= 1 && args[0] > 0 {
				divisor = args[0]
			}
			heal := damage / uint32(divisor)
			if heal > 0 {
				sum := *playerHP + heal
				if sum > playerMaxHP {
					sum = playerMaxHP
				}
				*playerHP = sum
				gainHP = int32(heal)
			}
		}
	case 192:
		// 1000192: 附加 n% 当前体力值的伤害
		if enemyHP != nil && *enemyHP > 0 {
			pct := 10
			if len(args) >= 1 && args[0] > 0 {
				pct = args[0]
			}
			bonus := *enemyHP * uint32(pct) / 100
			if bonus > *enemyHP {
				bonus = *enemyHP
			}
			*enemyHP -= bonus
		}
	case 194:
		// 1000194: 造成伤害的 1/n 恢复体力；若对手处于 XX 状态，则造成伤害的 1/m 恢复体力
		if playerHP != nil && playerMaxHP > 0 && damage > 0 && enemyStatus != nil {
			divisor := 2
			if len(args) >= 1 && args[0] > 0 {
				divisor = args[0]
			}
			statusIdx := StatusIndexBurn
			if len(args) >= 2 {
				statusIdx = args[1]
			}
			statusDivisor := divisor
			if len(args) >= 3 && args[2] > 0 {
				statusDivisor = args[2]
			}
			if statusIdx >= 0 && statusIdx < 20 && enemyStatus[statusIdx] > 0 {
				divisor = statusDivisor
			}
			heal := damage / uint32(divisor)
			if heal > 0 {
				sum := *playerHP + heal
				if sum > playerMaxHP {
					sum = playerMaxHP
				}
				*playerHP = sum
				gainHP = int32(heal)
			}
		}
	case 196:
		// 1000196: n% 使对方 XX 等级 -m；若先出手则 j% 使对方 XX 等级 -k（先出手判断在 handlers，此处按基础概率处理）
		// SideEffectArg: chance stat stages extraChance extraStat extraStages
		if enemyBattleLv != nil && !statDropImmune {
			chance, stat, stages := 30, 0, 1
			if len(args) >= 1 && args[0] > 0 {
				chance = args[0]
			}
			if len(args) >= 2 {
				stat = args[1]
			}
			if len(args) >= 3 && args[2] > 0 {
				stages = args[2]
			}
			if stat >= 0 && stat < 6 && rand.Intn(100) < chance {
				cur := int(enemyBattleLv[stat]) - stages
				if cur < minStatStage {
					cur = minStatStage
				}
				enemyBattleLv[stat] = int8(cur)
			}
		}
	case 115:
		// 1000115: n% 概率附加速度的 1/m 点固定伤害（速度值由 handlers 传入，此处用 enemyMaxHP 近似）
		// 实际应由 handlers 传入速度值；此处仅实现概率触发，伤害值由 handlers 处理
	case 88:
		// 1000088: n% 概率伤害为 m 倍（在 handlers 伤害计算阶段处理）
	case 110, 116, 117, 125, 126, 128, 180:
		// 多回合/条件效果，在 handlers 中实现
	case 111, 113, 132, 168:
		// 条件威力/handlers 伤害计算
	case 118:
		// 1000118: 威力随机 40~180（在 handlers 伤害计算阶段随机威力，此处无状态修改）
	case 119:
		// 1000119: 若本回合造成的伤害为奇数，则 30% 令对手疲惫 1 回合；若为偶数，则 30% 自身速度 +1
		if damage > 0 {
			if damage%2 == 1 {
				// 奇数伤害：30% 令对手疲惫
				if !sptboss.IsControlImmune(defenderPetID) && !statusImmune && enemyStatus != nil {
					if rand.Intn(100) < 30 && enemyStatus[StatusIndexFatigue] == 0 {
						enemyStatus[StatusIndexFatigue] = 1
					}
				}
			} else {
				// 偶数伤害：30% 自身速度 +1
				if playerBattleLv != nil && rand.Intn(100) < 30 {
					cur := int(playerBattleLv[StatSpeed]) + 1
					if cur > maxStatStage {
						cur = maxStatStage
					}
					playerBattleLv[StatSpeed] = int8(cur)
				}
			}
		}
	case 123, 127:
		// 1000123: 受伤加能力（handlers 回合状态）；1000127: 伤害减半（handlers）
	case 122, 129, 130, 131, 135:
		// 先手降能力/性别威力/性别附加/性别免疫/伤害下限 — 在 handlers 中实现
	case 188:
		// 1000188: 若对手处于异常状态，则威力翻倍并消除对手相应的防御能力提升效果
		// 威力翻倍在 handlers 伤害计算阶段处理；此处负责消除对手防御/特防强化
		if enemyBattleLv != nil && enemyStatus != nil {
			hasStatus := false
			for i := 0; i < 20; i++ {
				if enemyStatus[i] > 0 {
					hasStatus = true
					break
				}
			}
			if hasStatus {
				// 消除对手防御(1)和特防(3)的强化
				if enemyBattleLv[StatDefence] > 0 {
					enemyBattleLv[StatDefence] = 0
				}
				if enemyBattleLv[StatSpDef] > 0 {
					enemyBattleLv[StatSpDef] = 0
				}
			}
		}
	case 133:
		// 1000133: 对方处于烧伤状态时附加 n 点固定伤害
		if enemyHP != nil && enemyStatus != nil && enemyStatus[StatusIndexBurn] > 0 {
			bonus := uint32(0)
			if len(args) >= 1 && args[0] > 0 {
				bonus = uint32(args[0])
			}
			if bonus > 0 {
				if bonus > *enemyHP {
					bonus = *enemyHP
				}
				*enemyHP -= bonus
			}
		}
	case 141:
		// 1000141: 对方处于冻伤状态时附加 n 点固定伤害
		if enemyHP != nil && enemyStatus != nil && enemyStatus[StatusIndexFreeze] > 0 {
			bonus := uint32(0)
			if len(args) >= 1 && args[0] > 0 {
				bonus = uint32(args[0])
			}
			if bonus > 0 {
				if bonus > *enemyHP {
					bonus = *enemyHP
				}
				*enemyHP -= bonus
			}
		}
	case 167:
		// 1000167: 若对手处于能力下降状态则附加 n 点固定伤害
		if enemyHP != nil && enemyBattleLv != nil {
			hasStatDrop := false
			for i := 0; i < 6; i++ {
				if enemyBattleLv[i] < 0 {
					hasStatDrop = true
					break
				}
			}
			if hasStatDrop {
				bonus := uint32(0)
				if len(args) >= 1 && args[0] > 0 {
					bonus = uint32(args[0])
				}
				if bonus > 0 {
					if bonus > *enemyHP {
						bonus = *enemyHP
					}
					*enemyHP -= bonus
				}
			}
		}
	case 413:
		// 1000413: 若对手处于能力强化状态则附加 n 点固定伤害
		if enemyHP != nil && enemyBattleLv != nil {
			hasBuff := false
			for i := 0; i < 6; i++ {
				if enemyBattleLv[i] > 0 {
					hasBuff = true
					break
				}
			}
			if hasBuff {
				bonus := uint32(0)
				if len(args) >= 1 && args[0] > 0 {
					bonus = uint32(args[0])
				}
				if bonus > 0 {
					if bonus > *enemyHP {
						bonus = *enemyHP
					}
					*enemyHP -= bonus
				}
			}
		}
	case 154:
		// 1000154: 若对手处于 XX 状态，则对对方造成伤害的 1/n 恢复自身体力
		// SideEffectArg: statusIndex divisor
		if playerHP != nil && playerMaxHP > 0 && damage > 0 && enemyStatus != nil {
			statusIdx, divisor := 1, 2
			if len(args) >= 1 {
				statusIdx = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				divisor = args[1]
			}
			if statusIdx >= 0 && statusIdx < 20 && enemyStatus[statusIdx] > 0 {
				heal := damage / uint32(divisor)
				if heal > 0 {
					sum := *playerHP + heal
					if sum > playerMaxHP {
						sum = playerMaxHP
					}
					*playerHP = sum
					gainHP = int32(heal)
				}
			}
		}
	case 182:
		// 1000182: 若对手处于 XX 状态，m% 自身 stat 等级 +k
		// SideEffectArg: statusIndex stat chance stages
		if playerBattleLv != nil && enemyStatus != nil {
			statusIdx, stat, chance, stages := 5, 4, 100, 1
			if len(args) >= 1 {
				statusIdx = args[0]
			}
			if len(args) >= 2 {
				stat = args[1]
			}
			if len(args) >= 3 {
				chance = args[2]
			}
			if len(args) >= 4 {
				stages = args[3]
			}
			if statusIdx >= 0 && statusIdx < 20 && stat >= 0 && stat < 6 && enemyStatus[statusIdx] > 0 {
				if rand.Intn(100) < chance {
					cur := int(playerBattleLv[stat]) + stages
					if cur > maxStatStage {
						cur = maxStatStage
					}
					if cur < minStatStage {
						cur = minStatStage
					}
					playerBattleLv[stat] = int8(cur)
				}
			}
		}
	case 175:
		// 1000175: 若对手处于异常状态，m% 自身 stat 等级 +k
		// SideEffectArg: stat chance stages
		if playerBattleLv != nil && enemyStatus != nil {
			stat, chance, stages := 2, 100, 1
			if len(args) >= 1 {
				stat = args[0]
			}
			if len(args) >= 2 {
				chance = args[1]
			}
			if len(args) >= 3 {
				stages = args[2]
			}
			hasStatus := false
			for i := 0; i < 20; i++ {
				if enemyStatus[i] > 0 {
					hasStatus = true
					break
				}
			}
			if hasStatus && stat >= 0 && stat < 6 {
				if rand.Intn(100) < chance {
					cur := int(playerBattleLv[stat]) + stages
					if cur > maxStatStage {
						cur = maxStatStage
					}
					if cur < minStatStage {
						cur = minStatStage
					}
					playerBattleLv[stat] = int8(cur)
				}
			}
		}
	case 184:
		// 1000184: 若对手处于能力提升状态，m% 自身 stat 等级 +k
		// SideEffectArg: stat chance stages
		if playerBattleLv != nil && enemyBattleLv != nil {
			stat, chance, stages := 2, 100, 1
			if len(args) >= 1 {
				stat = args[0]
			}
			if len(args) >= 2 {
				chance = args[1]
			}
			if len(args) >= 3 {
				stages = args[2]
			}
			hasBuff := false
			for i := 0; i < 6; i++ {
				if enemyBattleLv[i] > 0 {
					hasBuff = true
					break
				}
			}
			if hasBuff && stat >= 0 && stat < 6 {
				if rand.Intn(100) < chance {
					cur := int(playerBattleLv[stat]) + stages
					if cur > maxStatStage {
						cur = maxStatStage
					}
					if cur < minStatStage {
						cur = minStatStage
					}
					playerBattleLv[stat] = int8(cur)
				}
			}
		}
	case 418:
		// 1000418: 若对手处于能力提升状态则对方 stat 等级 +/-n
		// SideEffectArg: stat stages
		if enemyBattleLv != nil && !statDropImmune {
			hasBuff := false
			for i := 0; i < 6; i++ {
				if enemyBattleLv[i] > 0 {
					hasBuff = true
					break
				}
			}
			if hasBuff {
				stat, stages := 0, -1
				if len(args) >= 1 {
					stat = args[0]
				}
				if len(args) >= 2 {
					stages = args[1]
				}
				if stat >= 0 && stat < 6 {
					cur := int(enemyBattleLv[stat]) + stages
					if cur > maxStatStage {
						cur = maxStatStage
					}
					if cur < minStatStage {
						cur = minStatStage
					}
					enemyBattleLv[stat] = int8(cur)
				}
			}
		}
	case 437:
		// 1000437: 若对手处于能力强化状态，则对手 stat 等级 m（同 418）
		// SideEffectArg: stat stages
		if enemyBattleLv != nil && !statDropImmune {
			hasBuff := false
			for i := 0; i < 6; i++ {
				if enemyBattleLv[i] > 0 {
					hasBuff = true
					break
				}
			}
			if hasBuff {
				stat, stages := 0, -1
				if len(args) >= 1 {
					stat = args[0]
				}
				if len(args) >= 2 {
					stages = args[1]
				}
				if stat >= 0 && stat < 6 {
					cur := int(enemyBattleLv[stat]) + stages
					if cur > maxStatStage {
						cur = maxStatStage
					}
					if cur < minStatStage {
						cur = minStatStage
					}
					enemyBattleLv[stat] = int8(cur)
				}
			}
		}
	case 430:
		// 1000430: 消除对手能力强化状态，若消除成功则自身 stat 等级 +m
		// SideEffectArg: stat stages
		cleared := false
		if enemyBattleLv != nil {
			for i := 0; i < 6; i++ {
				if enemyBattleLv[i] > 0 {
					enemyBattleLv[i] = 0
					cleared = true
				}
			}
		}
		if cleared && playerBattleLv != nil {
			stat, stages := 0, 1
			if len(args) >= 1 {
				stat = args[0]
			}
			if len(args) >= 2 {
				stages = args[1]
			}
			if stat >= 0 && stat < 6 {
				cur := int(playerBattleLv[stat]) + stages
				if cur > maxStatStage {
					cur = maxStatStage
				}
				playerBattleLv[stat] = int8(cur)
			}
		}
	case 453:
		// 1000453: 消除对手能力强化状态，若消除成功则对手进入 XX 状态
		// SideEffectArg: statusIndex
		cleared := false
		if enemyBattleLv != nil {
			for i := 0; i < 6; i++ {
				if enemyBattleLv[i] > 0 {
					enemyBattleLv[i] = 0
					cleared = true
				}
			}
		}
		if cleared && enemyStatus != nil && !statusImmune {
			statusIdx := 0
			if len(args) >= 1 {
				statusIdx = args[0]
			}
			if statusIdx >= 0 && statusIdx < 20 && enemyStatus[statusIdx] == 0 {
				enemyStatus[statusIdx] = randomStatusRounds()
			}
		}
	case 434:
		// 1000434: 若自身处于能力强化状态，则 n% 几率令对手 XX
		// SideEffectArg: chance statusIndex
		if playerBattleLv != nil && enemyStatus != nil && !statusImmune {
			hasBuff := false
			for i := 0; i < 6; i++ {
				if playerBattleLv[i] > 0 {
					hasBuff = true
					break
				}
			}
			if hasBuff {
				chance, statusIdx := 50, 6
				if len(args) >= 1 {
					chance = args[0]
				}
				if len(args) >= 2 {
					statusIdx = args[1]
				}
				if statusIdx >= 0 && statusIdx < 20 && rand.Intn(100) < chance {
					if enemyStatus[statusIdx] == 0 {
						enemyStatus[statusIdx] = randomStatusRounds()
					}
				}
			}
		}
	case 415:
		// 1000415: 若造成的伤害大于 m 点，则自身恢复 n 点体力
		// SideEffectArg: threshold heal
		if playerHP != nil && playerMaxHP > 0 && damage > 0 {
			threshold, healAmt := 0, 0
			if len(args) >= 1 {
				threshold = args[0]
			}
			if len(args) >= 2 {
				healAmt = args[1]
			}
			if healAmt > 0 && damage > uint32(threshold) {
				sum := *playerHP + uint32(healAmt)
				if sum > playerMaxHP {
					sum = playerMaxHP
				}
				*playerHP = sum
				gainHP = int32(healAmt)
			}
		}
	case 411:
		// 1000411: 附加对手当前体力值 n% 的百分比伤害（连续使用增加，此处仅实现基础 n%）
		// SideEffectArg: n m k（n=基础%, m=每次增加%, k=最高%）
		if enemyHP != nil && *enemyHP > 0 {
			pct := 8
			if len(args) >= 1 && args[0] > 0 {
				pct = args[0]
			}
			bonus := *enemyHP * uint32(pct) / 100
			if bonus > *enemyHP {
				bonus = *enemyHP
			}
			*enemyHP -= bonus
		}
	case 449:
		// 1000449: 若对手处于能力下降状态则 N% 几率令对手 XX
		// SideEffectArg: chance statusIndex
		if enemyBattleLv != nil && enemyStatus != nil && !statusImmune {
			hasStatDrop := false
			for i := 0; i < 6; i++ {
				if enemyBattleLv[i] < 0 {
					hasStatDrop = true
					break
				}
			}
			if hasStatDrop {
				chance, statusIdx := 100, 6
				if len(args) >= 1 {
					chance = args[0]
				}
				if len(args) >= 2 {
					statusIdx = args[1]
				}
				if statusIdx >= 0 && statusIdx < 20 && rand.Intn(100) < chance {
					if enemyStatus[statusIdx] == 0 {
						enemyStatus[statusIdx] = randomStatusRounds()
					}
				}
			}
		}
	case 450:
		// 1000450: 随机恢复 min 到 max 点体力
		// SideEffectArg: min max
		if playerHP != nil && playerMaxHP > 0 {
			minHeal, maxHeal := 1, 100
			if len(args) >= 1 && args[0] > 0 {
				minHeal = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				maxHeal = args[1]
			}
			if maxHeal < minHeal {
				maxHeal = minHeal
			}
			heal := uint32(minHeal + rand.Intn(maxHeal-minHeal+1))
			sum := *playerHP + heal
			if sum > playerMaxHP {
				sum = playerMaxHP
			}
			*playerHP = sum
			gainHP = int32(heal)
		}
	case 451:
		// 1000451: 命中后 n% 随机令对手进入烧伤、冻伤、中毒中的一种
		// SideEffectArg: chance
		if enemyStatus != nil && !statusImmune {
			chance := 30
			if len(args) >= 1 && args[0] > 0 {
				chance = args[0]
			}
			if rand.Intn(100) < chance {
				r := rand.Intn(3)
				var idx int
				switch r {
				case 0:
					idx = StatusIndexBurn
				case 1:
					idx = StatusIndexFreeze
				default:
					idx = StatusIndexPoison
				}
				if enemyStatus[idx] == 0 {
					enemyStatus[idx] = randomDoTRounds()
					if enemyHP != nil && enemyMaxHP > 0 {
						applyNewStatusDamageOnce(enemyHP, enemyMaxHP)
					}
				}
			}
		}
	case 455:
		// 1000455: 每损失 n 点体力则额外附加 m 点固定伤害
		// SideEffectArg: n m
		if enemyHP != nil && playerMaxHP > 0 {
			n, m := 1, 1
			if len(args) >= 1 && args[0] > 0 {
				n = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				m = args[1]
			}
			lostHP := playerMaxHP - *playerHP
			if playerHP != nil {
				lostHP = playerMaxHP
				if *playerHP < playerMaxHP {
					lostHP = playerMaxHP - *playerHP
				}
			}
			bonus := uint32(int(lostHP)/n) * uint32(m)
			if bonus > 0 {
				if bonus > *enemyHP {
					bonus = *enemyHP
				}
				*enemyHP -= bonus
			}
		}
	case 456:
		// 1000456: 若对手体力不足 n 则直接秒杀
		// SideEffectArg: n
		if enemyHP != nil && *enemyHP > 0 && !sptboss.IsControlImmune(defenderPetID) {
			threshold := 250
			if len(args) >= 1 && args[0] > 0 {
				threshold = args[0]
			}
			if *enemyHP < uint32(threshold) {
				*enemyHP = 0
			}
		}
	case 460:
		// 1000460: m% 几率令对手害怕，若对手处于能力强化状态则额外附加 n% 几率
		// SideEffectArg: chance extraChance
		if enemyStatus != nil && !statusImmune {
			chance, extra := 40, 20
			if len(args) >= 1 {
				chance = args[0]
			}
			if len(args) >= 2 {
				extra = args[1]
			}
			if enemyBattleLv != nil {
				for i := 0; i < 6; i++ {
					if enemyBattleLv[i] > 0 {
						chance += extra
						break
					}
				}
			}
			if chance > 100 {
				chance = 100
			}
			if rand.Intn(100) < chance && enemyStatus[StatusIndexFear] == 0 {
				enemyStatus[StatusIndexFear] = randomStatusRounds()
			}
		}
	case 466:
		// 1000466: 恢复 m 点体力
		// SideEffectArg: m
		if playerHP != nil && playerMaxHP > 0 {
			amount := 50
			if len(args) >= 1 && args[0] > 0 {
				amount = args[0]
			}
			sum := *playerHP + uint32(amount)
			if sum > playerMaxHP {
				sum = playerMaxHP
			}
			*playerHP = sum
			gainHP = int32(uint32(amount))
		}
	case 467:
		// 1000467: 若对手处于 XX 状态则附加 m 点固定伤害
		// SideEffectArg: statusIndex damage
		if enemyHP != nil && enemyStatus != nil {
			statusIdx, dmg := 2, 50
			if len(args) >= 1 {
				statusIdx = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				dmg = args[1]
			}
			if statusIdx >= 0 && statusIdx < 20 && enemyStatus[statusIdx] > 0 {
				bonus := uint32(dmg)
				if bonus > *enemyHP {
					bonus = *enemyHP
				}
				*enemyHP -= bonus
			}
		}
	case 472:
		// 1000472: 若对手处于 XX 状态则每次攻击造成的伤害都将恢复自身体力
		// SideEffectArg: statusIndex
		if playerHP != nil && playerMaxHP > 0 && damage > 0 && enemyStatus != nil {
			statusIdx := 2
			if len(args) >= 1 {
				statusIdx = args[0]
			}
			if statusIdx >= 0 && statusIdx < 20 && enemyStatus[statusIdx] > 0 {
				sum := *playerHP + damage
				if sum > playerMaxHP {
					sum = playerMaxHP
				}
				*playerHP = sum
				gainHP = int32(damage)
			}
		}
	case 473:
		// 1000473: 若造成的伤害不足 m，则自身 stat 等级 +n（类似 107）
		// SideEffectArg: threshold stat stages
		if playerBattleLv != nil && damage > 0 {
			threshold, stat, stages := 300, 0, 2
			if len(args) >= 1 && args[0] > 0 {
				threshold = args[0]
			}
			if len(args) >= 2 {
				stat = args[1]
			}
			if len(args) >= 3 && args[2] > 0 {
				stages = args[2]
			}
			if stat >= 0 && stat < 6 && damage < uint32(threshold) {
				cur := int(playerBattleLv[stat]) + stages
				if cur > maxStatStage {
					cur = maxStatStage
				}
				playerBattleLv[stat] = int8(cur)
			}
		}
	case 485:
		// 1000485: 消除对手能力强化状态，若消除成功则自身恢复所有体力
		if enemyBattleLv != nil && playerHP != nil && playerMaxHP > 0 {
			cleared := false
			for i := 0; i < 6; i++ {
				if enemyBattleLv[i] > 0 {
					enemyBattleLv[i] = 0
					cleared = true
				}
			}
			if cleared {
				*playerHP = playerMaxHP
				gainHP = int32(playerMaxHP)
			}
		}
	case 487:
		// 1000487: 若对手的体力大于 400，则每次使用自身攻击 +1
		if playerBattleLv != nil && enemyHP != nil && *enemyHP > 400 {
			cur := int(playerBattleLv[StatAttack]) + 1
			if cur > maxStatStage {
				cur = maxStatStage
			}
			playerBattleLv[StatAttack] = int8(cur)
		}
	case 489:
		// 1000489: 若自身处于能力提升状态，则每次攻击恢复自身体力的 1/m
		// SideEffectArg: m
		if playerHP != nil && playerMaxHP > 0 && playerBattleLv != nil {
			hasBuff := false
			for i := 0; i < 6; i++ {
				if playerBattleLv[i] > 0 {
					hasBuff = true
					break
				}
			}
			if hasBuff {
				divisor := 4
				if len(args) >= 1 && args[0] > 0 {
					divisor = args[0]
				}
				heal := playerMaxHP / uint32(divisor)
				if heal > 0 {
					sum := *playerHP + heal
					if sum > playerMaxHP {
						sum = playerMaxHP
					}
					*playerHP = sum
					gainHP = int32(heal)
				}
			}
		}
	case 495:
		// 1000495: 若对手处于 XX 状态，则 n% 几率秒杀对手
		// SideEffectArg: statusIndex chance
		if enemyHP != nil && *enemyHP > 0 && enemyStatus != nil && !sptboss.IsControlImmune(defenderPetID) {
			statusIdx, chance := 2, 30
			if len(args) >= 1 {
				statusIdx = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				chance = args[1]
			}
			if statusIdx >= 0 && statusIdx < 20 && enemyStatus[statusIdx] > 0 {
				if rand.Intn(100) < chance {
					*enemyHP = 0
				}
			}
		}
	case 422:
		// 1000422: 附加所造成伤害值 X% 的固定伤害
		// SideEffectArg: percent
		if enemyHP != nil && damage > 0 {
			pct := 10
			if len(args) >= 1 && args[0] > 0 {
				pct = args[0]
			}
			bonus := damage * uint32(pct) / 100
			if bonus > *enemyHP {
				bonus = *enemyHP
			}
			*enemyHP -= bonus
		}
	case 436:
		// 1000436: 附加已损失体力值 m% 的固定伤害
		// SideEffectArg: percent
		if enemyHP != nil && playerMaxHP > 0 && playerHP != nil {
			pct := 30
			if len(args) >= 1 && args[0] > 0 {
				pct = args[0]
			}
			lostHP := uint32(0)
			if *playerHP < playerMaxHP {
				lostHP = playerMaxHP - *playerHP
			}
			bonus := lostHP * uint32(pct) / 100
			if bonus > *enemyHP {
				bonus = *enemyHP
			}
			*enemyHP -= bonus
		}
	case 410:
		// 1000410: n% 回复自身 1/m 体力值（同 438）
		// SideEffectArg: chance divisor
		if playerHP != nil && playerMaxHP > 0 {
			chance, divisor := 15, 4
			if len(args) >= 1 && args[0] > 0 {
				chance = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				divisor = args[1]
			}
			if rand.Intn(100) < chance {
				heal := playerMaxHP / uint32(divisor)
				sum := *playerHP + heal
				if sum > playerMaxHP {
					sum = playerMaxHP
				}
				*playerHP = sum
				gainHP = int32(heal)
			}
		}
	case 465:
		// 1000465: m% 令对手疲惫 n 回合（每次使用几率提升 x%，最高 y%，此处仅实现基础 m%）
		// SideEffectArg: chance rounds increment maxChance
		if enemyStatus != nil && !statusImmune {
			chance := 40
			if len(args) >= 1 && args[0] > 0 {
				chance = args[0]
			}
			if rand.Intn(100) < chance && enemyStatus[StatusIndexFatigue] == 0 {
				rounds := 2
				if len(args) >= 2 && args[1] > 0 {
					rounds = args[1]
				}
				enemyStatus[StatusIndexFatigue] = byte(rounds)
			}
		}
	case 137:
		// 1000137: 损失一半当前体力值，攻击和速度提升2个等级
		if playerHP != nil && playerBattleLv != nil {
			half := *playerHP / 2
			*playerHP -= half
			recoilDamage = half
			for _, stat := range []int{StatAttack, StatSpeed} {
				cur := int(playerBattleLv[stat])
				cur += 2
				if cur > maxStatStage {
					cur = maxStatStage
				}
				playerBattleLv[stat] = int8(cur)
			}
		}
	case 138, 140, 142:
		// 1000138~142: 先手免疫反弹/威力随机/降低体力比例/冻伤附加/损失体力先手 等
	case 143:
		// 1000143: 使对手的能力提升效果反转成能力下降效果
		if enemyBattleLv != nil && !statDropImmune {
			for i := 0; i < 6; i++ {
				if enemyBattleLv[i] > 0 {
					enemyBattleLv[i] = -enemyBattleLv[i]
					if enemyBattleLv[i] < int8(minStatStage) {
						enemyBattleLv[i] = int8(minStatStage)
					}
				}
			}
		}
	case 144:
		// 1000144: 消耗自己所有体力，使下一只出战的精灵 n 回合免疫异常（n 在 handlers 设置；此处仅扣至 0 血）
		if playerHP != nil && *playerHP > 0 {
			recoilDamage = *playerHP
			*playerHP = 0
		}
	case 146, 150:
		// 1000146/150: 受击中毒/对手防特防等级，在 handlers 中实现
	case 149:
		// 1000149: 命中后 n% 令对方 xx，m% 令对方 XX（双异常独立判定）
		if !statusImmune && enemyStatus != nil {
			for i := 0; i+2 <= len(args); i += 2 {
				chance, statusIdx := 30, StatusIndexParalysis
				if i < len(args) && args[i] > 0 {
					chance = args[i]
				}
				if i+1 < len(args) {
					statusIdx = args[i+1]
				}
				if statusIdx < 0 || statusIdx >= 20 {
					statusIdx = StatusIndexParalysis
				}
				if rand.Intn(100) >= chance {
					continue
				}
				if sptboss.IsControlImmune(defenderPetID) && (statusIdx == StatusIndexParalysis || statusIdx == StatusIndexFear || statusIdx == StatusIndexSleep || statusIdx == StatusIndexPetrify || statusIdx == StatusIndexConfusion) {
					continue
				}
				if enemyStatus[statusIdx] == 0 {
					switch statusIdx {
					case StatusIndexPoison, StatusIndexBurn, StatusIndexFreeze:
						enemyStatus[statusIdx] = randomDoTRounds()
						applyNewStatusDamageOnce(enemyHP, enemyMaxHP)
					default:
						enemyStatus[statusIdx] = randomStatusRounds()
					}
				}
			}
		}
	case 147:
		// 1000147: 后出手时，n% 概率使对方 XX（异常状态）
		// 后出手判断在 handlers 中；此处实现状态挂载（handlers 确认后出手后调用）
		// 由于 ApplyEffect 不感知先后手，此处按"无条件 n% 挂状态"处理（handlers 负责先后手过滤）
		if !sptboss.IsControlImmune(defenderPetID) && !statusImmune && enemyStatus != nil {
			chance, statusIdx := 30, StatusIndexParalysis
			if len(args) >= 1 && args[0] > 0 {
				chance = args[0]
			}
			if len(args) >= 2 {
				statusIdx = args[1]
			}
			if statusIdx < 0 || statusIdx >= 20 {
				statusIdx = StatusIndexParalysis
			}
			if rand.Intn(100) < chance && enemyStatus[statusIdx] == 0 {
				switch statusIdx {
				case StatusIndexPoison, StatusIndexBurn, StatusIndexFreeze:
					enemyStatus[statusIdx] = randomDoTRounds()
					applyNewStatusDamageOnce(enemyHP, enemyMaxHP)
				default:
					enemyStatus[statusIdx] = randomStatusRounds()
				}
			}
		}
	case 148:
		// 1000148: 后出手时，m% 使对方 XX 等级降低 n 个等级
		// 后出手判断在 handlers；此处按"无条件 m% 降能力"处理（handlers 负责先后手过滤）
		if enemyBattleLv != nil && !statDropImmune {
			stat, chance, stages := 0, 50, 1
			if len(args) >= 1 {
				stat = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				chance = args[1]
			}
			if len(args) >= 3 && args[2] > 0 {
				stages = args[2]
			}
			if stat >= 0 && stat < 6 && rand.Intn(100) < chance {
				cur := int(enemyBattleLv[stat]) - stages
				if cur < minStatStage {
					cur = minStatStage
				}
				enemyBattleLv[stat] = int8(cur)
			}
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

	// ----- 151~199: 多回合/条件/复合效果 -----
	case 152, 153, 155, 156, 157, 160, 161, 163, 164, 165, 166, 169, 170:
		// 1000152~170: 多回合/条件/击败/异常/能力变化 等，需回合状态或 handlers
	case 171, 174, 176, 177, 183, 187, 189, 190:
		// 1000171~190: 多回合/条件/击败/异常/能力变化 等，需回合状态或 handlers
	case 191, 197, 198, 199:
		// 1000191~199: 多回合/条件/击败/异常/能力变化 等，需回合状态或 handlers
	case 158:
		// 1000158: 当次攻击击败对手，则 m% 自身 XX 等级 +n
		// 击败判断在 handlers；此处实现能力提升（handlers 确认击败后调用）
		// 由于 ApplyEffect 不感知是否击败，此处按"m% 自身 XX 等级 +n"处理（handlers 负责击败过滤）
		if playerBattleLv != nil {
			stat, chance, stages := 0, 100, 1
			if len(args) >= 1 {
				stat = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				chance = args[1]
			}
			if len(args) >= 3 && args[2] > 0 {
				stages = args[2]
			}
			if stat >= 0 && stat < 6 && rand.Intn(100) < chance {
				cur := int(playerBattleLv[stat]) + stages
				if cur > maxStatStage {
					cur = maxStatStage
				}
				playerBattleLv[stat] = int8(cur)
			}
		}
	case 172:
		// 1000172: 若后出手，则给予对方损伤的 1/n 会回复自己的体力
		// 后出手判断在 handlers；此处实现吸血（handlers 确认后出手后调用）
		if playerHP != nil && playerMaxHP > 0 && damage > 0 {
			divisor := 3
			if len(args) >= 1 && args[0] > 0 {
				divisor = args[0]
			}
			heal := damage / uint32(divisor)
			if heal > 0 {
				sum := *playerHP + heal
				if sum > playerMaxHP {
					sum = playerMaxHP
				}
				*playerHP = sum
				gainHP = int32(heal)
			}
		}
	case 173:
		// 1000173: 先出手时，n% 概率令对方 XX（异常状态）
		// 先出手判断在 handlers；此处实现状态挂载（handlers 确认先出手后调用）
		if !sptboss.IsControlImmune(defenderPetID) && !statusImmune && enemyStatus != nil {
			chance, statusIdx := 30, StatusIndexParalysis
			if len(args) >= 1 && args[0] > 0 {
				chance = args[0]
			}
			if len(args) >= 2 {
				statusIdx = args[1]
			}
			if statusIdx < 0 || statusIdx >= 20 {
				statusIdx = StatusIndexParalysis
			}
			if rand.Intn(100) < chance && enemyStatus[statusIdx] == 0 {
				switch statusIdx {
				case StatusIndexPoison, StatusIndexBurn, StatusIndexFreeze:
					enemyStatus[statusIdx] = randomDoTRounds()
					applyNewStatusDamageOnce(enemyHP, enemyMaxHP)
				default:
					enemyStatus[statusIdx] = randomStatusRounds()
				}
			}
		}
	case 179:
		// 1000179: 若属性相同则技能威力提升 n%（威力在 handlers 伤害计算阶段处理，此处无状态修改）
	case 185:
		// 1000185: 若击败处于 XX 状态的对手，则下一个出场的对手也进入 XX 状态
		// 击败判断与下一只出场在 handlers 中处理；此处无状态修改
	case 186:
		// 1000186: 后出手时，m% 使自身 XX 提升 n 个等级
		// 后出手判断在 handlers；此处实现能力提升（handlers 确认后出手后调用）
		if playerBattleLv != nil {
			stat, chance, stages := 0, 50, 1
			if len(args) >= 1 {
				stat = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				chance = args[1]
			}
			if len(args) >= 3 && args[2] > 0 {
				stages = args[2]
			}
			if stat >= 0 && stat < 6 && rand.Intn(100) < chance {
				cur := int(playerBattleLv[stat]) + stages
				if cur > maxStatStage {
					cur = maxStatStage
				}
				playerBattleLv[stat] = int8(cur)
			}
		}
	case 193:
		// 1000193: 若对手处于 XX 状态，则必定致命一击
		// 暴击判断在 handlers 伤害计算阶段处理（检查 enemyStatus[statusIdx]）；此处无状态修改
	case 195:
		// 1000195: 无视对手双防能力提升状态（防御/特防的正向 battleLv 视为 0）
		// 伤害计算在 handlers 中忽略对手防御/特防的正向强化；此处将对手防御/特防强化清零
		if enemyBattleLv != nil {
			if enemyBattleLv[StatDefence] > 0 {
				enemyBattleLv[StatDefence] = 0
			}
			if enemyBattleLv[StatSpDef] > 0 {
				enemyBattleLv[StatSpDef] = 0
			}
		}
	// ----- 200~399: 扩展效果 -----
	case 201:
		// 1000201: 组队时恢复己方 1/n 的体力（单人战斗时按 1/n 恢复，组队逻辑在 handlers 中）
		if playerHP != nil && playerMaxHP > 0 {
			divisor := 2
			if len(args) >= 1 && args[0] > 0 {
				divisor = args[0]
			}
			heal := playerMaxHP / uint32(divisor)
			sum := *playerHP + heal
			if sum > playerMaxHP {
				sum = playerMaxHP
			}
			*playerHP = sum
			gainHP = int32(heal)
		}
	case 202:
		// 1000202: 非指定对象伤害减免（在 handlers 伤害计算阶段处理）
	case 438:
		// 1000438: n%的几率恢复自身体力的1/m；SideEffectArg: n m（如 30 3 表示 30% 几率恢复 1/3 最大体力）
		if playerHP != nil && playerMaxHP > 0 {
			chance, divisor := 100, 2
			if len(args) >= 1 {
				chance = args[0]
			}
			if len(args) >= 2 && args[1] > 0 {
				divisor = args[1]
			}
			if chance <= 0 {
				chance = 100
			}
			if rand.Intn(100) < chance {
				heal := playerMaxHP / uint32(divisor)
				sum := *playerHP + heal
				if sum > playerMaxHP {
					sum = playerMaxHP
				}
				*playerHP = sum
				gainHP = int32(heal)
			}
		}
	case 439:
		// 1000439: n 回合内若自身能力下降或异常则对手每回合受 m 点固定伤害；在 handlers 回合末结算并递减
	case 448:
		// 1000448: n 回合内每回合对手全能力降级；在 handlers 回合末结算并递减
	case 454:
		// 1000454: 自身血量少于 1/n 时先制 +m；在 handlers 先制与回合末递减
	case 482:
		// 1000482: m%几率先制+n，在 handlers 先制计算
	case 488:
		// 1000488: 对手体力小于400时造成的伤害增加10%，在 handlers 伤害计算
	case 508:
		// 1000508: 下回合所受伤害减少 m 点；在 handlers 中受击时扣减并清零
	case 421:
		// 1000421: 击败对手时，将对手的能力强化转移到自身（在 handlers 击败判定中处理）
	case 429:
		// 1000429: 基础固定伤害 base，每次使用增加 increment，最高 max（此处仅实现基础值）
		// SideEffectArg: base increment max
		if enemyHP != nil && *enemyHP > 0 {
			base := 25
			if len(args) >= 1 && args[0] > 0 {
				base = args[0]
			}
			dmg := uint32(base)
			if dmg > *enemyHP {
				dmg = *enemyHP
			}
			*enemyHP -= dmg
		}
	case 402:
		// 1000402: 后出手时，额外附加 n 点固定伤害
		// 后出手判断在 handlers；此处实现固定伤害（handlers 确认后出手后调用）
		if enemyHP != nil && *enemyHP > 0 {
			bonus := uint32(0)
			if len(args) >= 1 && args[0] > 0 {
				bonus = uint32(args[0])
			}
			if bonus > 0 {
				if bonus > *enemyHP {
					bonus = *enemyHP
				}
				*enemyHP -= bonus
			}
		}
	case 405:
		// 1000405: 先出手时，额外附加 n 点固定伤害
		// 先出手判断在 handlers；此处实现固定伤害（handlers 确认先出手后调用）
		if enemyHP != nil && *enemyHP > 0 {
			bonus := uint32(0)
			if len(args) >= 1 && args[0] > 0 {
				bonus = uint32(args[0])
			}
			if bonus > 0 {
				if bonus > *enemyHP {
					bonus = *enemyHP
				}
				*enemyHP -= bonus
			}
		}
	case 428:
		// 1000428: 遇到天敌时附加 m 点固定伤害（仅当 typeMod>1 时在 handlers 中附加，此处不扣血避免重复）
	case 431:
		// 1000431: 若自身处于能力下降状态则威力翻倍（在 handlers 伤害计算中处理）
	case 441:
		// 1000441: 每次攻击暴击率 +n%，最高 m%（在 handlers 暴击判定中处理）
	case 444:
		// 1000444: 将对手所有技能 PP 与自身交换（复杂，暂不实现）
	case 445:
		// 1000445: 战斗结束时奖励赛尔豆（在 handlers 战斗结束时处理）
	case 412:
		// 1000412: 体力低于 1/n 时不消耗 PP（在 handlers PP 扣减时判断）
	case 471:
		// 1000471: 先出手时 n 回合内免疫异常状态（在 handlers 中设置）
	case 484:
		// 1000484: 连击 n 次，每次附加 bonus 点固定伤害，最高 cap 次（在 handlers 伤害计算中处理）
	case 490:
		// 1000490: 若造成伤害超过 m，则自身速度 +n 级（在 handlers 伤害后处理）
	case 447, 458, 459, 461, 463, 464, 468:
		// 1000447: 伤害下限；1000458/459: 先手回血/防御附加伤；1000461: 低血必暴；1000463: 伤害减免；1000464: 天敌烧伤；1000468: 能力下降威力翻倍 — 在 handlers 中实现
	case 474, 475, 476, 478, 494:
		// 1000474/475/476: 先手强化/低伤暴击/后手回血；1000478: 对手属性技能无效；1000494: 无视对手能力提升 — 在 handlers 中实现
		// 1000478: n 回合内对手属性技能无效；在 handlers 中跳过属性效果并递减
	case 545:
		// 1000545: n 回合内受击高于 m 则对手获得效果；在 handlers 受击与回合末递减
	// ----- 1000+ 高编号效果（威力/先制/条件伤害等，多在 handlers）-----
	case 1635:
		// 1001635: 誓言之约 - 立刻恢复自身 n 点体力，k 回合后恢复全部体力；当回合先执行立即回复
		if playerHP != nil && playerMaxHP > 0 && len(args) >= 1 {
			addHP := uint32(args[0])
			if addHP > 0 {
				sum := *playerHP + addHP
				if sum > playerMaxHP {
					sum = playerMaxHP
				}
				*playerHP = sum
				gainHP = int32(addHP)
			}
			// k 回合后恢复全部体力需 handlers 回合状态
		}
	case 1901:
		// 1001901: 潜力越高威力越大，在 handlers 伤害计算
	case 2236:
		// 1002236: 扩展效果（希等）
	// BOSS/特殊多效果：痴愚/谄诳/红莲/皆苦/非天/五衰(MonID 4661)、简/极(4677)、希(4706)，参数由 effectArgCount 切分，具体逻辑在 handlers 多效果循环中占位或按配置扩展
	case 691, 700, 773, 935, 976:
		// 五衰链末尾/痴愚链/简极组合，不在此处改状态
	case 1083, 1211, 1248, 1257, 1470, 1603, 1605, 1850, 1925, 2237:
		// 痴愚系列/希等 BOSS 多效果，不在此处改状态
	default:
		// 未列出的效果 ID：多为回合类/条件类/伤害公式类，需在 handlers 或战斗回合状态中实现
	}

	return gainHP, recoilDamage
}

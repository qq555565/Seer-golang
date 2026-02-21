package handlers

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/seer-game/golang-version/internal/core/logger"
	"github.com/seer-game/golang-version/internal/core/nonoformcache"
	"github.com/seer-game/golang-version/internal/core/packet"
	"github.com/seer-game/golang-version/internal/core/soultransformcache"
	"github.com/seer-game/golang-version/internal/core/userdb"
	gamebattle "github.com/seer-game/golang-version/internal/game/battle"
	gameogres "github.com/seer-game/golang-version/internal/game/mapogres"
	gamepets "github.com/seer-game/golang-version/internal/game/pets"
	gameskills "github.com/seer-game/golang-version/internal/game/skills"
	"github.com/seer-game/golang-version/internal/game/sptboss"
	"github.com/seer-game/golang-version/internal/server/gameserver"
)

// BOSS 防护罩血量缓存：userID -> "mapID_region" -> 当前血量（0 表示满血/未初始化）
var (
	bossHpCache   = make(map[int64]map[string]int)
	bossHpCacheMu sync.RWMutex
)

// resourceBaseURL 资源服根地址（如 http://127.0.0.1:32400），用于在 1001 尾追加 set_user URL，供同机多开时按米米号识别超能 NONO 形态
var resourceBaseURL string

// SetResourceBaseURL 由 main 在启动时设置，用于 1001 扩展块中的 set_user 链接
func SetResourceBaseURL(url string) {
	resourceBaseURL = url
}

// bossSkillOverrides 为特定 BOSS 设置固定技能列表（优先级高于按等级可学技能）
// 顺序即 UI 显示顺序，可按需要扩展
var bossSkillOverrides = map[int][]int{
	// 闪光波克尔（克洛斯星 Boss 及其变体 ID）：红韵、全力一击、魅惑、挥翼飘舞
	// 注：按照需求，将原来的“同生共死”(10036) 替换为 “全力一击”(10033)
	40:  {20210, 10033, 20209, 10486},
	63:  {20210, 10033, 20209, 10486},
	86:  {20210, 10033, 20209, 10486},
	166: {20210, 10033, 20209, 10486},
}

// bossHPOverrides 地图 BOSS 固定血量（仅用于 PVE 敌人，不影响玩家自己拥有的同名精灵）
// key 为精灵 ID，value 为战斗开始时的 MaxHP/HP。
var bossHPOverrides = map[int]int{
	47:  100,     // 蘑菇怪
	34:  200,     // 钢牙鲨
	42:  338,     // 里奥斯
	50:  1000,    // 阿克希亚
	69:  500,     // 提亚斯
	70:  800,     // 雷伊
	88:  1400,    // 纳多雷
	113: 1500,    // 雷纳多
	132: 2800,    // 尤纳斯
	187: 3000000, // 魔狮迪露
	216: 10000,   // 哈莫雷特
	264: 2500,    // 奈尼芬多
	421: 3000,    // 厄尔塞拉
	261: 2000,    // 盖亚
	274: 13000,   // 塔克林
	391: 10000,   // 塔西亚
}

// applyBossHPOverride 若为地图 BOSS，则返回指定的固定血量，否则返回原始值
func applyBossHPOverride(petID int, original int) int {
	if hp, ok := bossHPOverrides[petID]; ok && hp > 0 {
		return hp
	}
	return original
}

// getEnemySkillsForPet 返回敌方精灵的技能列表，若有覆盖则优先覆盖
func getEnemySkillsForPet(petID, level int) []int {
	if skills, ok := bossSkillOverrides[petID]; ok {
		return skills
	}
	petMgr := gamepets.GetInstance()
	return petMgr.GetSkillsForLevel(petID, level)
}

// pickEnemySkill 挑选敌方本回合要用的技能。
// 一般逻辑：
//   - 优先从“有威力或非纯辅助”的技能中随机选择（包括 BOSS 自定义技能列表）
//   - 若全是纯辅助技能，则随机选其中一个
//   - 若仍没有可用技能，则返回 nil,0，表示本回合不出招
//
// 特例：闪光波克尔家族（40 / 63 / 86 / 166）
//   - 按原版规则，四个技能（红韵 / 全力一击 / 魅惑 / 挥翼飘舞）应当等概率随机释放
//   - 因为 20209/20210 是纯属性技（Category=4, Power=0），若走通用逻辑会几乎永远只用攻击技，
//     导致“不会放属性技能”；这里为这几只精灵改为“在可用技能里完全随机”。
func pickEnemySkill(skillMgr *gameskills.Skills, petID, level int) (*gameskills.Skill, uint32) {
	skillsForPet := getEnemySkillsForPet(petID, level)

	// 闪光波克尔及其变体：四个技能全部参与随机
	switch petID {
	case 40, 63, 86, 166:
		var allSkills []*gameskills.Skill
		var allIDs []uint32
		for _, sid := range skillsForPet {
			if sid <= 0 {
				continue
			}
			if sk := skillMgr.Get(sid); sk != nil {
				allSkills = append(allSkills, sk)
				allIDs = append(allIDs, uint32(sid))
			}
		}
		if len(allSkills) == 0 {
			return nil, 0
		}
		idx := rand.Intn(len(allSkills))
		return allSkills[idx], allIDs[idx]
	}

	// 通用逻辑
	var candidates []*gameskills.Skill
	var candidateIDs []uint32
	var fallback *gameskills.Skill
	var fallbackID uint32

	for _, sid := range skillsForPet {
		if sid <= 0 {
			continue
		}
		if sk := skillMgr.Get(sid); sk != nil {
			if sk.Power > 0 || sk.Category != 4 {
				// 具有攻击性的技能：加入候选池，后续随机挑选
				candidates = append(candidates, sk)
				candidateIDs = append(candidateIDs, uint32(sid))
			} else if fallback == nil {
				// 记录一个纯辅助技能作为兜底
				fallback = sk
				fallbackID = uint32(sid)
			}
		}
	}

	// 在候选攻击技能中随机挑选一个
	if len(candidates) > 0 {
		idx := rand.Intn(len(candidates))
		return candidates[idx], candidateIDs[idx]
	}

	// 没有攻击技能时，随机使用第一个找到的辅助技能
	if fallback != nil {
		return fallback, fallbackID
	}

	// 没有任何技能可用：本回合不出招
	return nil, 0
}

// RegisterHandlers 注册所有命令处理器
func RegisterHandlers(gs *gameserver.GameServer) {
	// 注册核心命令处理器
	registerCoreHandlers(gs)

	// 注册游戏逻辑命令处理器
	registerGameHandlers(gs)

	// 注册精灵相关命令处理器
	registerPetHandlers(gs)

	// 注册战斗相关命令处理器
	registerBattleHandlers(gs)

	// 注册未实现命令的空响应（对齐 Lua 服 CMD 列表，避免客户端触发“未实现的命令”）
	registerStubHandlers(gs)

	// 客户端断线时向同地图其他玩家广播更新后的 2003 列表
	gs.OnClientDisconnect = func(cd *gameserver.ClientData, mapID int) {
		body := buildMapPlayerListForMap(gs, mapID)
		gs.BroadcastToMap(mapID, 0, 2003, body)
		logger.Info(fmt.Sprintf("[2003] 用户离开地图后广播: MapID=%d", mapID))
		if mapID == 102 && cd.UserID > 0 {
			OnArenaHostLeave(gs, cd.UserID)
		}
	}
}

// registerCoreHandlers 注册核心命令处理器
func registerCoreHandlers(gs *gameserver.GameServer) {
	gs.RegisterCommandHandler(1001, handleLogin)
	gs.RegisterCommandHandler(1002, handleSystemTime)
	// 登录后初始化阶段常见请求（对齐 Lua 服，避免客户端 Bean/模块卡死）
	gs.RegisterCommandHandler(2150, handleGetRelationList)  // 好友/黑名单
	gs.RegisterCommandHandler(2151, handleFriendAdd)        // 添加好友
	gs.RegisterCommandHandler(2152, handleFriendAnswer)     // 好友请求回复
	gs.RegisterCommandHandler(2153, handleFriendRemove)     // 删除好友
	gs.RegisterCommandHandler(2154, handleBlackAdd)         // 添加黑名单
	gs.RegisterCommandHandler(2155, handleBlackRemove)      // 移除黑名单
	gs.RegisterCommandHandler(2157, handleSeeOnline)        // 查看在线状态
	gs.RegisterCommandHandler(2158, handleRequestOut)       // 发送请求
	gs.RegisterCommandHandler(2159, handleRequestAnswer)    // 请求回复
	gs.RegisterCommandHandler(2751, handleMailGetList)      // 邮件列表
	gs.RegisterCommandHandler(2757, handleMailGetUnread)    // 未读邮件
	gs.RegisterCommandHandler(50004, handleXinCheck)        // 客户端上报
	gs.RegisterCommandHandler(50008, handleXinGetQuadTime)  // 四倍经验时间
	gs.RegisterCommandHandler(70001, handleGetExchangeInfo) // 荣誉/交换
	gs.RegisterCommandHandler(1106, handleGoldOnlineCheckRemain)
	gs.RegisterCommandHandler(1104, handleGoldBuyProduct)   // 金豆购买商品
	gs.RegisterCommandHandler(1101, handleMoneyCheckPsw)    // 米币支付密码检查（返回1则客户端继续购买流程）
	gs.RegisterCommandHandler(1103, handleMoneyCheckRemain) // 米币余额检查
	gs.RegisterCommandHandler(80008, handleHeartbeat)
	gs.RegisterCommandHandler(2701, handleTalkCount)       // 对话计数（物品领取）
	gs.RegisterCommandHandler(2702, handleTalkCate)        // 对话分类（发放领取物品）
	gs.RegisterCommandHandler(10006, handleFitmentUsering) // 正在使用的家具（基地）
	// 系统/支付 完整协议
	gs.RegisterCommandHandler(1005, handleGetImageAddress)
	gs.RegisterCommandHandler(1102, handleMoneyBuyProduct)
	gs.RegisterCommandHandler(1105, handleGoldCheckRemain)
	// 协议层/校验 完整协议
	gs.RegisterCommandHandler(1022, handleCheckFightCode)
	gs.RegisterCommandHandler(8002, handleSystemMessage)
	gs.RegisterCommandHandler(9049, handleOpenBagGet)
	gs.RegisterCommandHandler(11003, handleGetPetInfoAlt)
	gs.RegisterCommandHandler(11007, handleGetPetByCatchTime)
	gs.RegisterCommandHandler(11022, handleGetSecondBag)
	gs.RegisterCommandHandler(41983, handleReconnect)
	gs.RegisterCommandHandler(46046, handleGetMultiForever)
	gs.RegisterCommandHandler(40001, handleGetSuperValue)
	gs.RegisterCommandHandler(40002, handleGetSuperValueByIds)
	gs.RegisterCommandHandler(42023, handleBatchGetBitset)
	gs.RegisterCommandHandler(46057, handleGetMultiForeverByDb)
	gs.RegisterCommandHandler(41080, handleGetForeverValue)
	gs.RegisterCommandHandler(47334, handleFriendListAlt)
	gs.RegisterCommandHandler(47335, handleBlacklistAlt)
	gs.RegisterCommandHandler(10301, handleSystemTimeAlt)
	gs.RegisterCommandHandler(4475, handleItemListAlt)
	gs.RegisterCommandHandler(8001, handleInform)
	gs.RegisterCommandHandler(8004, handleGetBossMonster)
	// 成就/称号 协议（3403 ACHIEVETITLELIST 等，3404 SETTITLE 单独实现）
	gs.RegisterCommandHandler(3404, handleSetTitle)

	// 注册NONO系统命令处理器
	registerNonoHandlers(gs)
}

// registerGameHandlers 注册游戏逻辑命令处理器
func registerGameHandlers(gs *gameserver.GameServer) {
	// 注册地图相关命令处理器
	gs.RegisterCommandHandler(2001, handleEnterMap)
	gs.RegisterCommandHandler(2002, handleLeaveMap)
	gs.RegisterCommandHandler(2003, handleListMapPlayer)
	gs.RegisterCommandHandler(1004, handleMapHot) // 地图热点（宇宙地图热点数据）
	gs.RegisterCommandHandler(2004, handleMapOgreList)
	gs.RegisterCommandHandler(2401, handleInviteToFight)     // 邀请玩家对战（转发 2501 给被邀请方）
	gs.RegisterCommandHandler(2403, handleHandleFightInvite) // 接受/拒绝对战邀请（转发 2502 给邀请方）
	gs.RegisterCommandHandler(2408, handleFightNpcMonster)   // 地图野怪战斗
	gs.RegisterCommandHandler(2412, handleAttackBoss)        // 攻击 SPT BOSS（破除防护罩）
	gs.RegisterCommandHandler(2051, handleGetSimUserInfo)    // 获取简单用户信息
	gs.RegisterCommandHandler(2052, handleGetMoreUserInfo)   // 获取详细用户信息
	gs.RegisterCommandHandler(2101, handlePeopleWalk)        // 人物移动
	gs.RegisterCommandHandler(2102, handleChat)              // 聊天
	gs.RegisterCommandHandler(2104, handleAimat)             // 射击/瞄准（AIMAT）
	gs.RegisterCommandHandler(2107, handleTransformUser)     // 射击命中后变身（TRANSFORM_USER），广播 2108 给同图
	// 地图/玩家 完整协议
	gs.RegisterCommandHandler(2061, handleChangeNickName)
	gs.RegisterCommandHandler(2063, handleChangeColor)
	gs.RegisterCommandHandler(2103, handleDanceAction)
	gs.RegisterCommandHandler(2111, handlePeopleTransform)
	gs.RegisterCommandHandler(2112, handleOnOrOffFlying)

	// 精灵相关（部分，只实现新手流程必需）
	gs.RegisterCommandHandler(2301, handleGetPetInfo)
	// 2302 = MODIFY_PET_NAME（修改精灵名字），由 stub 处理；2308 = PET_DEFAULT（设为首发）
	gs.RegisterCommandHandler(2303, handleGetPetList) // 获取精灵列表（切换精灵时需要）

	// 任务 / 新手奖励相关（2201/2202/2203）、每日任务（2231/2232/2233）、魂珠列表（2354）
	gs.RegisterCommandHandler(2201, handleAcceptTask)
	gs.RegisterCommandHandler(2202, handleCompleteTask)
	gs.RegisterCommandHandler(2203, handleGetTaskBuf)        // 获取任务进度（GET_TASK_BUF），地图装置等依赖此接口
	gs.RegisterCommandHandler(2231, handleAcceptDailyTask)   // 接受每日任务
	gs.RegisterCommandHandler(2232, handleDeleteDailyTask)   // 放弃每日任务
	gs.RegisterCommandHandler(2233, handleCompleteDailyTask) // 完成每日任务（响应格式同 2202 NoviceFinishInfo）
	gs.RegisterCommandHandler(2351, handlePetFusion)   // 精灵融合 PET_FUSION
	gs.RegisterCommandHandler(2354, handleGetSoulBeadList)
	gs.RegisterCommandHandler(2352, handleGetSoulBeadBuf) // 元神赋形：魂珠能量吸收进度（需到对应地区吸取）
	gs.RegisterCommandHandler(2353, handleSetSoulBeadBuf)   // 设置魂珠能量吸收进度（客户端在地区吸取后上报）
	gs.RegisterCommandHandler(2356, handleGetSoulBeadStatus)    // 元神珠赋形状态（剩余孵化时间）
	gs.RegisterCommandHandler(2357, handleTransformSoulBead)    // 元神赋形（放入转化仪）
	gs.RegisterCommandHandler(2358, handleSoulBeadToPet)        // 元神珠孵化完成领取精灵
	gs.RegisterCommandHandler(2315, handlePetHatchPutIn)          // 分子转化仪：放入精元孵化 PET_HATCH
	gs.RegisterCommandHandler(2316, handleNonoMolecularTransform) // 分子转化仪：查询/打开面板 PET_HATCH_GET
	gs.RegisterCommandHandler(2204, handleAddTaskBuf)
	gs.RegisterCommandHandler(2234, handleGetDailyTaskBuf)
	gs.RegisterCommandHandler(2235, handleStub4Zero) // ADD_DAILY_TASK_BUF，与 Lua emptyResponse(4) 一致，暂不持久化每日任务 buf
	gs.RegisterCommandHandler(2065, handleExchangeNewYear)
	gs.RegisterCommandHandler(2251, handleExchangeOre)
	gs.RegisterCommandHandler(2902, handleExchangePetComplete)

	// 背包/物品系统
	registerItemHandlers(gs)

	// 新手战斗触发：2411 -> 回推 2503；2421 盖亚专用（FIGHT_SPECIAL_PET）同逻辑
	gs.RegisterCommandHandler(2411, handleChallengeBoss)
	gs.RegisterCommandHandler(2421, handleChallengeBoss)

	// 试炼之塔：FRESH_CHOICE_FIGHT_LEVEL / FRESH_START_FIGHT_LEVEL
	gs.RegisterCommandHandler(2428, handleFreshChoiceFightLevel)
	gs.RegisterCommandHandler(2429, handleFreshStartFightLevel)

	// 勇者之塔（地图500）：CHOICE_FIGHT_LEVEL / START_FIGHT_LEVEL / LEAVE_FIGHT_LEVEL
	gs.RegisterCommandHandler(2414, handleChoiceFightLevel)
	gs.RegisterCommandHandler(2415, handleStartFightLevel)
	gs.RegisterCommandHandler(2416, handleLeaveFightLevel)

	// 暗黑武斗场：OPEN_DARKPORTAL / FIGHT_DARKPORTAL / LEAVE_DARKPORTAL
	gs.RegisterCommandHandler(2424, handleOpenDarkPortal)
	gs.RegisterCommandHandler(2425, handleFightDarkPortal)
	gs.RegisterCommandHandler(2426, handleLeaveDarkPortal)

	// 挑战擂台（地图102）
	gs.RegisterCommandHandler(2417, handleArenaSetOwner)
	gs.RegisterCommandHandler(2418, handleArenaFightOwner)
	gs.RegisterCommandHandler(2419, handleArenaGetInfo)
	gs.RegisterCommandHandler(2420, handleArenaUpFight)
	gs.RegisterCommandHandler(2422, handleArenaOwnerAcce)
	gs.RegisterCommandHandler(2423, handleArenaOwnerOut)

	// 战斗初始化：2404 READY_TO_FIGHT
	gs.RegisterCommandHandler(2404, handleReadyToFight)
}

// registerPetHandlers 注册精灵相关命令处理器
func registerPetHandlers(gs *gameserver.GameServer) {
	// 基础精灵信息与背包/仓库
	gs.RegisterCommandHandler(2301, handleGetPetInfo) // 获取精灵完整信息
	gs.RegisterCommandHandler(2303, handleGetPetList) // 获取精灵列表（仓库）
	gs.RegisterCommandHandler(2302, handleModifyPetName)
	gs.RegisterCommandHandler(2304, handlePetRelease) // 精灵仓库互转
	gs.RegisterCommandHandler(2305, handlePetShow)    // 展示精灵（跟随面板）
	gs.RegisterCommandHandler(2308, handleSetDefaultPet)

	// 治疗与新手赠宠
	gs.RegisterCommandHandler(2306, handlePetCureAll)   // 全体精灵恢复
	gs.RegisterCommandHandler(2310, handlePetOneCure)   // 单只精灵恢复
	gs.RegisterCommandHandler(2311, handlePetCollect)   // 精灵收集赠宠
	gs.RegisterCommandHandler(2313, handleIsCollect)    // 精灵收集奖励检测
	gs.RegisterCommandHandler(2314, handlePetEvolvtion) // 精灵进化（基础线性进化）

	// 经验池 / 技能
	gs.RegisterCommandHandler(2319, handlePetGetExp) // 获取经验池经验
	gs.RegisterCommandHandler(2318, handlePetSetExp) // 从经验池分配经验给精灵
	gs.RegisterCommandHandler(3007, handleExperienceSharedComplete)     // 发明室经验接收器：领取并平均分配给背包精灵
	gs.RegisterCommandHandler(3009, handleMyExperiencePondComplete)     // 发明室经验接收器：查询教官积累经验值
	gs.RegisterCommandHandler(3011, handleGetMyExperienceComplete)      // 发明室经验接收器：教官查看未领取经验
	gs.RegisterCommandHandler(2325, handlePetRoomInfo)                  // 精灵房间信息（简略面板）
	gs.RegisterCommandHandler(2312, handlePetSkillSwitch)               // 精灵技能切换（技能唤醒仪替换技能）
	gs.RegisterCommandHandler(2336, handleGetPetSkill)                  // 获取精灵技能（技能唤醒仪）
	gs.RegisterCommandHandler(2326, handleUsePetItemOutOfFight)         // 战斗外使用精灵道具（学习力清零）
	gs.RegisterCommandHandler(9278, handleUsePetItemFullAbilityOfStudy) // 学习力注入（单项拉满）

	// 罗威训练 / 房间展示
	gs.RegisterCommandHandler(2320, handleRoweiPetList)   // 罗威训练精灵列表
	gs.RegisterCommandHandler(2321, handleRoweiPetStart)  // 开始罗威训练
	gs.RegisterCommandHandler(2322, handleRoweiPetReturn) // 取回罗威精灵
	gs.RegisterCommandHandler(2323, handleRoomPetShow)    // 设置房间展示精灵
	gs.RegisterCommandHandler(2324, handleRoomPetList)    // 获取房间展示精灵

	// Buff / 自动战斗 / 学习力道具
	gs.RegisterCommandHandler(2327, handleUseSpeedupItem)     // 经验加速道具（2/3倍）
	gs.RegisterCommandHandler(2329, handleUseAutoFightItem)   // 自动战斗道具
	gs.RegisterCommandHandler(2330, handleOnOffAutoFight)     // 开关自动战斗
	gs.RegisterCommandHandler(2331, handleUseEnergyXishou)    // 体力吸收道具
	gs.RegisterCommandHandler(2332, handleUseStudyItem)       // 学习力双倍道具
	gs.RegisterCommandHandler(2343, handlePetResetNature)     // 重置性格
}

// registerBattleHandlers 注册战斗相关命令处理器
func registerBattleHandlers(gs *gameserver.GameServer) {
	gs.RegisterCommandHandler(2405, handleUseSkill)     // 使用技能
	gs.RegisterCommandHandler(2406, handleUsePetItem)   // 使用道具
	gs.RegisterCommandHandler(2407, handleChangePet)    // 切换精灵
	gs.RegisterCommandHandler(2409, handleCatchMonster) // 捕捉精灵
	gs.RegisterCommandHandler(2410, handleEscapeFight)  // 逃跑
}

// DarkPortalDoorBosses 暗黑武斗场各门对应 Boss 配置
// 结构：门索引 -> 子关卡索引 -> Boss精灵ID
// 例如：门3有3-1(巴弗洛177)和3-2(其他boss)
var darkPortalDoorBosses = map[uint32][]uint32{
	0:  {171},                    // 第一门：魔牙鲨
	1:  {174},                    // 第二门：贝鲁基德
	2:  {177, 178, 179},          // 第三门：巴弗洛、3-2、3-3
	3:  {192, 193, 194},          // 第四门：克林卡修、4-2、4-3
	4:  {222, 223, 224},          // 第五门：卡库、5-2、5-3
	5:  {356, 357, 358},          // 第六门：斯加尔卡、6-2、6-3
	6:  {438, 439, 440},          // 第七门：魔花使者、7-2、7-3
	7:  {656, 657, 658},          // 第八门：帕多尼、8-2、8-3
	8:  {779, 780, 781},          // 第九门：迪普利德、9-2、9-3
	9:  {1180, 1181, 1182},       // 第十门：10-1、10-2、10-3
	10: {1183, 1184, 1185},       // 第十一门：11-1、11-2、11-3
}

// darkPortalBossIDs 暗黑武斗场各门对应 Boss 精灵 ID（兼容旧代码，取第一子关卡）
// 参考 PetBookXMLInfo：暗黑第一门魔牙鲨171、第二门贝鲁基德174、第三门巴弗洛177、第四门克林卡修192、第五门卡库222、第六门斯加尔卡356、第七门魔花使者438、第八门帕多尼656、第九门迪普利德779
var darkPortalBossIDs = []uint32{171, 174, 177, 192, 222, 356, 438, 656, 779, 1180, 1183}

// darkPortalBossRewards 暗黑武斗场各门 Boss 首次击败奖励精元物品 ID
// 对应关系：bossID -> 精元物品ID（参考 items.xml）
// 注意：子关卡boss（如3-2、3-3）如果没有配置精元，则使用对应主boss的精元
var darkPortalBossRewards = map[uint32]int{
	// 第一门
	171: 400110, // 魔牙鲨的精元
	// 第二门
	174: 400111, // 贝鲁基德的精元
	// 第三门
	177: 400112, // 巴弗洛的精元
	178: 400112, // 3-2 使用巴弗洛的精元
	179: 400112, // 3-3 使用巴弗洛的精元
	// 第四门
	192: 400116, // 克林卡修的精元
	193: 400116, // 4-2 使用克林卡修的精元
	194: 400116, // 4-3 使用克林卡修的精元
	// 第五门
	222: 400120, // 卡库的精元
	223: 400120, // 5-2 使用卡库的精元
	224: 400120, // 5-3 使用卡库的精元
	// 第六门
	356: 400129, // 斯加尔卡的精元
	357: 400129, // 6-2 使用斯加尔卡的精元
	358: 400129, // 6-3 使用斯加尔卡的精元
	// 第七门
	438: 400142, // 魔花使者的精元
	439: 400142, // 7-2 使用魔花使者的精元
	440: 400142, // 7-3 使用魔花使者的精元
	// 第八门
	656: 400184, // 帕多尼的精元
	657: 400184, // 8-2 使用帕多尼的精元
	658: 400184, // 8-3 使用帕多尼的精元
	// 第九门
	779: 400197, // 迪普利德精元
	780: 400197, // 9-2 使用迪普利德精元
	781: 400197, // 9-3 使用迪普利德精元
	// 第十门
	1180: 0, // 10-1（暂未配置）
	1181: 0, // 10-2（暂未配置）
	1182: 0, // 10-3（暂未配置）
	// 第十一门
	1183: 0, // 11-1（暂未配置）
	1184: 0, // 11-2（暂未配置）
	1185: 0, // 11-3（暂未配置）
}

// puniFragmentItemIDs 谱尼七封印 + 真身对应的碎片道具 ID（参考 items.xml）
// door 1~7 为七道封印，8 为真身
var puniFragmentItemIDs = map[int]int{
	1: 400651, // 谱尼的虚无裂片
	2: 400652, // 谱尼的元素裂片
	3: 400653, // 谱尼的能量裂片
	4: 400654, // 谱尼的生命裂片
	5: 400655, // 谱尼的轮回裂片
	6: 400656, // 谱尼的永恒裂片
	7: 400657, // 谱尼的圣洁裂片
	8: 400658, // 谱尼的真身裂片
}

// puniFragmentItemIDsRev 反向映射：碎片道具 ID -> 是否为谱尼碎片
var puniFragmentItemIDsRev = map[int]int{
	400651: 1,
	400652: 2,
	400653: 3,
	400654: 4,
	400655: 5,
	400656: 6,
	400657: 7,
	400658: 8,
}

// puniSoulItemID 谱尼的精元（items.xml 中 BreedMonID=300 的精元物品）
const puniSoulItemID = 400150

// darkPortalSoulRewardPetIDs 暗黑武斗场精元物品对应的精灵ID（参考 items.xml 的 BreedMonID）
// 对应关系：精元物品ID -> 精灵ID
var darkPortalSoulRewardPetIDs = map[int]int{
	400110: 170, // 魔牙鲨的精元 -> 魔牙鲨(170)
	400111: 172, // 贝鲁基德的精元 -> 贝鲁基德(172)
	400112: 175, // 巴弗洛的精元 -> 巴弗洛(175)
	400116: 190, // 克林卡修的精元 -> 克林卡修(190)
	400120: 221, // 卡库的精元 -> 卡库(221)
	400129: 354, // 斯加尔卡的精元 -> 斯加尔卡(354)
	400142: 436, // 魔花使者的精元 -> 魔花使者(436)
	400184: 655, // 帕多尼的精元 -> 帕多尼(655)
	400197: 777, // 迪普利德精元 -> 迪普利德(777)
}

// handleOpenDarkPortal CMD 2424 OPEN_DARKPORTAL 暗黑武斗场开门
// 请求体：doorIndex(4) + subIndex(4) 门索引 0~10，子关卡索引 0~2（可选，默认0）
// 返回体：bossId(4) 该门该子关卡 Boss 精灵 ID，客户端用于显示及战斗
func handleOpenDarkPortal(ctx *gameserver.HandlerContext) {
	var doorIndex uint32
	var subIndex uint32 = 0
	if len(ctx.Body) >= 4 {
		doorIndex = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	if len(ctx.Body) >= 8 {
		subIndex = binary.BigEndian.Uint32(ctx.Body[4:8])
	}
	
	var bossID uint32 = 171 // 默认第一门魔牙鲨
	// 优先使用数据库配置
	if entry, ok := GetDarkPortalBossEntry(doorIndex, subIndex); ok {
		bossID = uint32(entry.BossID)
	} else if bosses, ok := darkPortalDoorBosses[doorIndex]; ok && len(bosses) > 0 {
		// 兼容旧代码：如果数据库配置不存在，使用代码中的配置
		if int(subIndex) < len(bosses) {
			bossID = bosses[subIndex]
		} else {
			bossID = bosses[0] // 子关卡索引超出范围，使用第一关
		}
	} else if int(doorIndex) < len(darkPortalBossIDs) {
		// 兼容旧代码：如果门配置不存在，使用旧数组
		bossID = darkPortalBossIDs[doorIndex]
	}
	
	// 保存用户当前打开的门索引和子关卡索引，供 2425 战斗时使用
	ctx.GameServer.DarkPortalDoorsMu.Lock()
	ctx.GameServer.DarkPortalDoors[ctx.UserID] = gameserver.DarkPortalDoorInfo{
		DoorIndex: doorIndex,
		SubIndex:  subIndex,
	}
	ctx.GameServer.DarkPortalDoorsMu.Unlock()
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], bossID)
	ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2424] 暗黑武斗场开门: UID=%d door=%d sub=%d bossId=%d", ctx.UserID, doorIndex, subIndex, bossID))
}

// handleFightDarkPortal CMD 2425 FIGHT_DARKPORTAL 暗黑武斗场战斗
// 请求体：空；返回体：空。客户端收到后启动战斗（PetFightModel.mode = MULTI_MODE，走 2404/2405 等战斗流程）
func handleFightDarkPortal(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, []byte{})
		return
	}

	// 先发送 2425 的空响应
	ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, []byte{})

	// 获取用户当前打开的门索引和子关卡索引
	ctx.GameServer.DarkPortalDoorsMu.RLock()
	doorInfo, hasDoor := ctx.GameServer.DarkPortalDoors[ctx.UserID]
	ctx.GameServer.DarkPortalDoorsMu.RUnlock()

	var bossID uint32 = 171 // 默认第一门魔牙鲨
	enemyLevel := 50         // 暗黑武斗场boss默认等级
	if hasDoor {
		// 优先使用数据库配置
		if entry, ok := GetDarkPortalBossEntry(doorInfo.DoorIndex, doorInfo.SubIndex); ok {
			bossID = uint32(entry.BossID)
			if entry.EnemyLv > 0 {
				enemyLevel = entry.EnemyLv
			}
		} else if bosses, ok := darkPortalDoorBosses[doorInfo.DoorIndex]; ok && len(bosses) > 0 {
			// 兼容旧代码：如果数据库配置不存在，使用代码中的配置
			if int(doorInfo.SubIndex) < len(bosses) {
				bossID = bosses[doorInfo.SubIndex]
			} else {
				bossID = bosses[0] // 子关卡索引超出范围，使用第一关
			}
		} else if int(doorInfo.DoorIndex) < len(darkPortalBossIDs) {
			// 兼容旧代码：如果门配置不存在，使用旧数组
			bossID = darkPortalBossIDs[doorInfo.DoorIndex]
		}
	} else {
		logger.Warning(fmt.Sprintf("[2425] 未找到用户门索引，使用默认bossID: UID=%d", ctx.UserID))
	}

	// 写入 BattleStates，供 2503/2504 使用正确的敌方等级
	ctx.GameServer.BattleMu.Lock()
	battle, ok := ctx.GameServer.BattleStates[ctx.UserID]
	if !ok || battle == nil {
		battle = &gameserver.BattleState{}
	}
	battle.EnemyID = int(bossID)
	battle.EnemyLevel = enemyLevel
	battle.IsActive = true
	battle.BattleMapID = user.MapID
	battle.RoundCount = 0
	battle.LastHitWasCrit = false
	battle.IsDarkPortalBattle = true // 仅在本场为暗黑武斗场挑战成功时发放精元/精灵奖励
	ctx.GameServer.BattleStates[ctx.UserID] = battle
	ctx.GameServer.BattleMu.Unlock()

	// 发送 2503 包启动战斗（使用 SEQ=0 作为推送包，不使用请求的 SEQ）
	body := buildNoteReadyToFightInfo(ctx, bossID)
	ctx.GameServer.SendResponse(ctx.ClientData, 2503, ctx.UserID, 0, body)
	logger.Info(fmt.Sprintf("[2425] 暗黑武斗场战斗: UID=%d door=%d sub=%d bossId=%d level=%d", ctx.UserID, doorInfo.DoorIndex, doorInfo.SubIndex, bossID, enemyLevel))
}

// handleLeaveDarkPortal CMD 2426 LEAVE_DARKPORTAL 离开暗黑之门
// 请求体：空；返回体：空。客户端收到后调用 MapManager.changeMap(110) 返回暗黑武斗场主厅
func handleLeaveDarkPortal(ctx *gameserver.HandlerContext) {
	// 清除用户当前打开的门索引
	ctx.GameServer.DarkPortalDoorsMu.Lock()
	delete(ctx.GameServer.DarkPortalDoors, ctx.UserID)
	ctx.GameServer.DarkPortalDoorsMu.Unlock()
	ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, []byte{})
	logger.Info(fmt.Sprintf("[2426] 离开暗黑之门: UID=%d", ctx.UserID))
}

// handleFreshChoiceFightLevel CMD 2428 试炼之塔选层/继续挑战
// 请求体：param(4) — 0=继续当前层；>0=选择某层
// 返回体：curLevel(4) + bossCount(4) + bossId[0..n-1](4 each)
func handleFreshChoiceFightLevel(ctx *gameserver.HandlerContext) {
	var param uint32
	if len(ctx.Body) >= 4 {
		param = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		body := make([]byte, 8)
		ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, body)
		return
	}

	// 计算要进入的层数：0=继续当前，否则尝试跳到 param 对应层（不超过 MaxFreshStage 与配置上限）
	curLevel := calcFreshFightStartLevel(param, user)
	if curLevel <= 0 {
		curLevel = 1
	}

	// 从 GM 配置中获取该层 Boss 列表
	bossIDs := GetFreshFightBossIDsForLevel(curLevel)
	if len(bossIDs) == 0 {
		// 若该层未配置，则返回空列表，前端会提示异常或无法继续
		body := make([]byte, 8)
		binary.BigEndian.PutUint32(body[0:4], uint32(curLevel))
		binary.BigEndian.PutUint32(body[4:8], 0)
		ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, body)
		return
	}

	// 更新玩家进度：CurFreshStage 至少为 curLevel，MaxFreshStage 只增不减
	if user.CurFreshStage < curLevel {
		user.CurFreshStage = curLevel
	}
	if user.MaxFreshStage < curLevel {
		user.MaxFreshStage = curLevel
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	// 记录本次战斗的敌人信息到 BattleState：用第一只 Boss 做战斗入口
	firstBossID := bossIDs[0]
	// 优先使用 GM 中为该层第1只怪配置的 EnemyLv（若 >0）
	entry, hasEntry := GetFreshFightEntry(curLevel, 1)
	defaultLevel := 10 + curLevel
	if defaultLevel < 1 {
		defaultLevel = 1
	}
	enemyLevel := defaultLevel
	if hasEntry && entry.EnemyLv > 0 {
		enemyLevel = entry.EnemyLv
	}

	ctx.GameServer.BattleMu.Lock()
	battle, ok := ctx.GameServer.BattleStates[ctx.UserID]
	if !ok || battle == nil {
		battle = &gameserver.BattleState{}
	}
	battle.EnemyID = firstBossID
	battle.EnemyLevel = enemyLevel
	battle.IsActive = true
	battle.FreshLevel = curLevel
	battle.FreshBossIndex = 0
	ctx.GameServer.BattleStates[ctx.UserID] = battle
	ctx.GameServer.BattleMu.Unlock()

	// 构造返回体：curLevel + count + bossIDs
	n := uint32(len(bossIDs))
	body := make([]byte, 8+4*len(bossIDs))
	binary.BigEndian.PutUint32(body[0:4], uint32(curLevel))
	binary.BigEndian.PutUint32(body[4:8], n)
	off := 8
	for _, id := range bossIDs {
		binary.BigEndian.PutUint32(body[off:off+4], uint32(id))
		off += 4
	}

	logger.Info(fmt.Sprintf("[2428] 试炼之塔选层: userID=%d param=%d curLevel=%d bossIDs=%v EnemyLevel=%d", ctx.UserID, param, curLevel, bossIDs, enemyLevel))
	ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, body)
}

// handleFreshStartFightLevel CMD 2429 试炼之塔开始战斗
// 与 2415 勇者之塔一致：推送 2503（本层全部 Boss catchTime=0,1,2...），切换时只发 2504+2505
func handleFreshStartFightLevel(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		body := make([]byte, 8)
		ctx.GameServer.SendResponse(ctx.ClientData, 2429, ctx.UserID, ctx.SeqID, body)
		return
	}
	curLevel := user.CurFreshStage
	if curLevel <= 0 {
		curLevel = 1
	}
	bossIDs := GetFreshFightBossIDsForLevel(curLevel)
	if len(bossIDs) == 0 {
		body := make([]byte, 8)
		binary.BigEndian.PutUint32(body[0:4], uint32(curLevel))
		binary.BigEndian.PutUint32(body[4:8], 0)
		ctx.GameServer.SendResponse(ctx.ClientData, 2429, ctx.UserID, ctx.SeqID, body)
		return
	}
	idx := 0
	firstBossID := bossIDs[idx]
	enemyLevel := 10 + curLevel
	if enemyLevel < 1 {
		enemyLevel = 1
	}
	if entry, ok := GetFreshFightEntry(curLevel, 1); ok && entry.EnemyLv > 0 {
		enemyLevel = entry.EnemyLv
	}
	ctx.GameServer.BattleMu.Lock()
	battle, ok := ctx.GameServer.BattleStates[ctx.UserID]
	if !ok || battle == nil {
		battle = &gameserver.BattleState{}
	}
	battle.EnemyID = firstBossID
	battle.EnemyLevel = enemyLevel
	battle.IsActive = true
	battle.FreshLevel = curLevel
	battle.FreshBossIndex = idx
	ctx.GameServer.BattleStates[ctx.UserID] = battle
	ctx.GameServer.BattleMu.Unlock()

	n := uint32(len(bossIDs))
	body := make([]byte, 8+4*len(bossIDs))
	binary.BigEndian.PutUint32(body[0:4], uint32(curLevel))
	binary.BigEndian.PutUint32(body[4:8], n)
	for i, id := range bossIDs {
		binary.BigEndian.PutUint32(body[8+i*4:8+(i+1)*4], uint32(id))
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 2429, ctx.UserID, ctx.SeqID, body)
	body2503 := buildNoteReadyToFightInfoTower(ctx, bossIDs)
	ctx.GameServer.SendResponse(ctx.ClientData, 2503, ctx.UserID, 0, body2503)
	logger.Info(fmt.Sprintf("[2429] 试炼之塔开始战斗: userID=%d curLevel=%d EnemyID=%d EnemyLevel=%d 已推2503(含本层%d只Boss)", ctx.UserID, curLevel, firstBossID, enemyLevel, len(bossIDs)))
}

// calcFreshFightStartLevel 计算试炼之塔应进入的层数
func calcFreshFightStartLevel(param uint32, user *userdb.GameData) int {
	cur := user.CurFreshStage
	maxLv := user.MaxFreshStage

	if param == 0 {
		// 继续挑战：优先当前层，否则从 1 层开始
		if cur > 0 {
			return cur
		}
		return 1
	}

	target := int(param)
	if target <= 0 {
		target = 1
	}
	// 不允许跳到超过已解锁层数太多的位置：若 maxLv>0 则限制在 [1, maxLv]
	if maxLv > 0 && target > maxLv {
		target = maxLv
	}
	return target
}

// 勇者之塔最大层数（与客户端 FightLevelModel._maxLevel=80 一致）
const fightLevelMaxLevel = 80

// calcFightLevelStartLevel 计算勇者之塔应进入的层数（param：0=继续当前，>0=选择某层）
func calcFightLevelStartLevel(param uint32, user *userdb.GameData) int {
	cur := user.CurStage
	maxLv := user.MaxStage

	if param == 0 {
		if cur > 0 {
			return cur
		}
		return 1
	}

	target := int(param)
	if target <= 0 {
		target = 1
	}
	if target > fightLevelMaxLevel {
		target = fightLevelMaxLevel
	}
	if maxLv > 0 && target > maxLv {
		target = maxLv
	}
	return target
}

// handleChoiceFightLevel CMD 2414 勇者之塔选层/继续挑战
// 请求体：param(4) — 0=继续当前层；>0=选择某层
// 返回体：curLevel(4) + bossCount(4) + bossId[0..n-1](4 each)，与客户端 ChoiceLevelRequestInfo 一致
func handleChoiceFightLevel(ctx *gameserver.HandlerContext) {
	var param uint32
	if len(ctx.Body) >= 4 {
		param = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		body := make([]byte, 8)
		ctx.GameServer.SendResponse(ctx.ClientData, 2414, ctx.UserID, ctx.SeqID, body)
		return
	}

	curLevel := calcFightLevelStartLevel(param, user)
	if curLevel <= 0 {
		curLevel = 1
	}

	bossIDs := GetFightLevelBossIDsForLevel(curLevel)
	if len(bossIDs) == 0 {
		body := make([]byte, 8)
		binary.BigEndian.PutUint32(body[0:4], uint32(curLevel))
		binary.BigEndian.PutUint32(body[4:8], 0)
		ctx.GameServer.SendResponse(ctx.ClientData, 2414, ctx.UserID, ctx.SeqID, body)
		return
	}

	user.CurStage = curLevel
	user.TowerBossIndex = 0 // 选层时重置当前层 Boss 索引，本层 3 只顺序上场
	if user.MaxStage < curLevel {
		user.MaxStage = curLevel
	}
	ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)

	firstBossID := bossIDs[0]
	enemyLevel := 10 + curLevel
	if enemyLevel < 1 {
		enemyLevel = 1
	}

	ctx.GameServer.BattleMu.Lock()
	battle, ok := ctx.GameServer.BattleStates[ctx.UserID]
	if !ok || battle == nil {
		battle = &gameserver.BattleState{}
	}
	battle.EnemyID = firstBossID
	battle.EnemyLevel = enemyLevel
	battle.IsActive = true
	battle.TowerLevel = curLevel
	battle.TowerBossIndex = 0
	ctx.GameServer.BattleStates[ctx.UserID] = battle
	ctx.GameServer.BattleMu.Unlock()

	n := uint32(len(bossIDs))
	body := make([]byte, 8+4*len(bossIDs))
	binary.BigEndian.PutUint32(body[0:4], uint32(curLevel))
	binary.BigEndian.PutUint32(body[4:8], n)
	for i, id := range bossIDs {
		binary.BigEndian.PutUint32(body[8+i*4:8+(i+1)*4], uint32(id))
	}
	logger.Info(fmt.Sprintf("[2414] 勇者之塔选层: userID=%d param=%d curLevel=%d bossIDs=%v", ctx.UserID, param, curLevel, bossIDs))
	ctx.GameServer.SendResponse(ctx.ClientData, 2414, ctx.UserID, ctx.SeqID, body)
}

// handleStartFightLevel CMD 2415 勇者之塔开始战斗
// 请求体：无（客户端在 map500 点击战斗时发送）
// 返回体：curLevel(4) + bossCount(4) + bossId[](4 each)，与 SuccessFightRequestInfo 一致
func handleStartFightLevel(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		body := make([]byte, 8)
		ctx.GameServer.SendResponse(ctx.ClientData, 2415, ctx.UserID, ctx.SeqID, body)
		return
	}

	curLevel := user.CurStage
	if curLevel <= 0 {
		curLevel = 1
	}
	if curLevel > fightLevelMaxLevel {
		curLevel = fightLevelMaxLevel
	}

	bossIDs := GetFightLevelBossIDsForLevel(curLevel)
	if len(bossIDs) == 0 {
		body := make([]byte, 8)
		binary.BigEndian.PutUint32(body[0:4], uint32(curLevel))
		binary.BigEndian.PutUint32(body[4:8], 0)
		ctx.GameServer.SendResponse(ctx.ClientData, 2415, ctx.UserID, ctx.SeqID, body)
		return
	}

	// 当前层第几只 Boss（0/1/2），打完一只再下一只
	idx := user.TowerBossIndex
	if idx < 0 || idx >= len(bossIDs) {
		idx = 0
		user.TowerBossIndex = 0
	}
	firstBossID := bossIDs[idx]
	enemyLevel := 10 + curLevel
	if enemyLevel < 1 {
		enemyLevel = 1
	}
	if entry, ok := GetFightLevelEntry(curLevel); ok && entry.EnemyLv > 0 {
		enemyLevel = entry.EnemyLv
	}
	ctx.GameServer.BattleMu.Lock()
	battle, ok := ctx.GameServer.BattleStates[ctx.UserID]
	if !ok || battle == nil {
		battle = &gameserver.BattleState{}
	}
	battle.EnemyID = firstBossID
	battle.EnemyLevel = enemyLevel
	battle.IsActive = true
	battle.TowerLevel = curLevel
	battle.TowerBossIndex = idx
	battle.BattleMapID = 500
	ctx.GameServer.BattleStates[ctx.UserID] = battle
	ctx.GameServer.BattleMu.Unlock()

	n := uint32(len(bossIDs))
	body := make([]byte, 8+4*len(bossIDs))
	binary.BigEndian.PutUint32(body[0:4], uint32(curLevel))
	binary.BigEndian.PutUint32(body[4:8], n)
	for i, id := range bossIDs {
		binary.BigEndian.PutUint32(body[8+i*4:8+(i+1)*4], uint32(id))
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 2415, ctx.UserID, ctx.SeqID, body)
	// 客户端依赖 2503 打开对战界面。勇者之塔用本层全部 Boss 一条 2503（catchTime=0,1,2...），切换时不再发 2503，避免重载 DLL 导致 2504 进错上下文
	body2503 := buildNoteReadyToFightInfoTower(ctx, bossIDs)
	ctx.GameServer.SendResponse(ctx.ClientData, 2503, ctx.UserID, 0, body2503)
	logger.Info(fmt.Sprintf("[2415] 勇者之塔开始战斗: userID=%d curLevel=%d EnemyID=%d EnemyLevel=%d 已推2503(含本层%d只Boss)", ctx.UserID, curLevel, firstBossID, enemyLevel, len(bossIDs)))
}

// handleLeaveFightLevel CMD 2416 离开勇者之塔
// 请求体：无；返回 4 字节 0。若当前在 map500，退出后推送 2001 进入空间站侧翼(map 5)，使客户端正确返回侧翼场景。
func handleLeaveFightLevel(ctx *gameserver.HandlerContext) {
	ctx.GameServer.BattleMu.Lock()
	if battle, ok := ctx.GameServer.BattleStates[ctx.UserID]; ok && battle != nil {
		battle.TowerLevel = 0
		battle.TowerBossIndex = 0
	}
	ctx.GameServer.BattleMu.Unlock()
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user != nil {
		user.TowerBossIndex = 0
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
	}
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 2416, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2416] 离开勇者之塔: userID=%d", ctx.UserID))

	// 退出塔后一律返回空间站侧翼：客户端点 2416 时可能在塔场景但服务端 user.MapID 仍为进塔前地图(如 108)，故不判断 MapID，始终推送进入 map 5
	if user != nil {
		oldMapID := user.MapID
		if oldMapID == 0 {
			oldMapID = 1
		}
		user.MapID = mapIDSpaceStationFlank
		if user.PosX == 0 {
			user.PosX = 500
		}
		if user.PosY == 0 {
			user.PosY = 300
		}
		ctx.GameServer.RemoveUserFromMap(oldMapID, ctx.UserID)
		oldListBody := buildMapPlayerListForMap(ctx.GameServer, oldMapID)
		ctx.GameServer.BroadcastToMap(oldMapID, 0, 2003, oldListBody)
		ctx.GameServer.AddUserToMap(mapIDSpaceStationFlank, ctx.UserID, ctx.ClientData)
		ctx.GameServer.SetOgreEnterMapTime(ctx.UserID)
		body2001 := buildPeopleInfo(ctx.UserID, user, time.Now().Unix(), user.PosX, user.PosY, true)
		ctx.GameServer.SendResponse(ctx.ClientData, 2001, ctx.UserID, 0, body2001)
		listBody := buildMapPlayerListForMap(ctx.GameServer, mapIDSpaceStationFlank)
		ctx.GameServer.SendResponse(ctx.ClientData, 2003, ctx.UserID, 0, listBody)
		ctx.GameServer.BroadcastToMap(mapIDSpaceStationFlank, ctx.UserID, 2003, listBody)
		slots := gameogres.GenerateNewSlotsNoCache(mapIDSpaceStationFlank)
		if len(slots) > 0 {
			ctx.GameServer.SetPlayerOgreSlots(ctx.UserID, mapIDSpaceStationFlank, slots)
			ogreBody := ctx.GameServer.BuildMapOgreListFromSlots(slots)
			ctx.GameServer.SendResponse(ctx.ClientData, 2004, ctx.UserID, 0, ogreBody)
		} else {
			ogreBody := buildMapOgreList(mapIDSpaceStationFlank)
			ctx.GameServer.SendResponse(ctx.ClientData, 2004, ctx.UserID, 0, ogreBody)
		}
		logger.Info(fmt.Sprintf("[2416] 已推送进入空间站侧翼 MapID=%d: userID=%d (原 MapID=%d)", mapIDSpaceStationFlank, ctx.UserID, oldMapID))
	}
}

// registerStubHandlers 注册尚未实现完整协议的 CMD：按 Lua 响应格式区分 4 字节 0 / 8 字节 0 / 空包
func registerStubHandlers(gs *gameserver.GameServer) {
	// 扭蛋机：3201 精灵扭蛋机、9757 梦幻扭蛋机 已单独实现
	gs.RegisterCommandHandler(3201, handleGacha)
	gs.RegisterCommandHandler(9757, handleGacha)
	// Lua 返回 writeUInt32BE(0) 的 CMD，响应 4 字节 0
	stub4Zero := []int32{
		5001, 5002, 2442, 2444, 2445, 2446,
		// 用户信息相关 (2051/2052 已实现)：2053 REQUEST_COUNT, 2054 GP_GHAZI_MAX_LEVEL, 2055 USER_PARTY_GET_USER_IMAGE_NAME
		2053, 2054, 2055,
		// 2302, 2306, 2307, 2309, 2310, 2311, 2313, 2314,
		// 2320, 2321, 2322, 2323, 2324, 2327, 2329, 2330, 2331, 2332, 2343
		// 上述精灵相关 CMD 已在 registerPetHandlers 中注册为完整实现，这里不再使用 stub4Zero。
		2328, 2393, // Skill_Sort 与 LEIYI_TRAIN_GET_STATUS 暂仍按 4 字节 0 占位
		// 成就/称号 (ACHIEVELIST/ACHIEVEINFO/ACHIEVETITLELIST/SETTITLE/CONFERACHIEVEMENT/ACHIEVE_AND_TITLE)，3404 单独实现，3405 在 stubEmpty
		3401, 3402, 3403, 3406, 3407,
		// 2411/2421 已在 registerGameHandlers 中注册为 handleChallengeBoss（SPT/盖亚挑战），此处不再 stub
		// 2417-2423 擂台相关由 handleArena* 实现，不在 stub4Zero
		// 2424/2425/2426 暗黑武斗场由 handleDarkPortal* 实现
		// 2428/2429/2430 试炼之塔相关由专用处理器实现，不在 stub4Zero
		// 2414/2415/2416 勇者之塔由 handleChoiceFightLevel / handleStartFightLevel / handleLeaveFightLevel 实现
		2910, 2911, 2912, 2913, 2914, 2917, 2918, 2928, 2929, 2962, 2963,
		3001, 3002, 3003, 3004, 3005, 3006, 3008, 3010,
		4001, 4002, 4003, 4004, 4005, 4006, 4007, 4008, 4009, 4010, 4011, 4012, 4013, 4014,
		4017, 4018, 4019, 4020, 4022, 4023, 4024, 4025, 4101, 4102, 2481,
		10001, 10002, 10003, 10004, 10005, 10007, 10008, 10009,
		// 2151-2159 好友协议已实现完整处理，不再使用 stub4Zero
	}
	for _, cmd := range stub4Zero {
		gs.RegisterCommandHandler(cmd, handleStub4Zero)
	}
	// Lua 返回 ret(4)+count(4) 等 8 字节的 CMD
	gs.RegisterCommandHandler(5052, handleStub8Zero)
	// Lua 返回空或客户端不依赖包体的 CMD（含 gamepktprotocol.lua emptyCmds 中未单独实现的 CMD，9003/2354 已实现故不在此列）
	stubEmpty := []int32{
		5003, // LEAVE_GAME
		1011, 1016, 2289, 2192, 2196, 2361, 3405, 4359, 4364, 4501, 5005,
		9112, 9677, 41006, 41249, 41253, 4148, 4178, 4181, 43706, 45512, 45524,
		45773, 45793, 45798, 45824, 47309, 45071,
		40006, 40007,
	}
	// 注：2313 已在 stub4Zero（精灵 IS_COLLECT），9003/2354 已有完整实现，故不放入 stubEmpty
	for _, cmd := range stubEmpty {
		gs.RegisterCommandHandler(cmd, handleStubEmpty)
	}
}

// handleSystemTime CMD 1002 系统时间，对齐 Lua system_handlers.handleSystemTime
func handleSystemTime(ctx *gameserver.HandlerContext) {
	buf := make([]byte, 8)
	t := uint32(time.Now().Unix())
	binary.BigEndian.PutUint32(buf[0:4], t)
	binary.BigEndian.PutUint32(buf[4:8], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 1002, ctx.UserID, ctx.SeqID, buf)
}

// handleStubEmpty 未实现命令的空响应（返回 result=0 空包，避免客户端报“未实现的命令”）
func handleStubEmpty(ctx *gameserver.HandlerContext) {
	ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, []byte{})
}

// handleStub4Zero 返回 4 字节 0 的协议体（对齐 Lua writeUInt32BE(0)）
func handleStub4Zero(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, body)
}

// handleStub8Zero 返回 8 字节 0（对齐 Lua ret(4)+count(4) 等）
func handleStub8Zero(ctx *gameserver.HandlerContext) {
	body := make([]byte, 8)
	ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, body)
}

// putFixedString 将字符串写入固定长度（不足补0）
func putFixedString(buf []byte, off int, s string, n int) {
	b := []byte(s)
	if len(b) > n {
		b = b[:n]
	}
	copy(buf[off:], b)
	for i := len(b); i < n; i++ {
		buf[off+i] = 0
	}
}

// ==================== 系统/支付 完整协议 ====================

// handleGetImageAddress CMD 1005 GET_IMAGE_ADDRESS
// 响应: host(16) + port(2) + session(16)，对齐 Lua system_handlers.handleGetImageAddress
func handleGetImageAddress(ctx *gameserver.HandlerContext) {
	body := make([]byte, 16+2+16)
	putFixedString(body, 0, gameserver.PublicIP, 16)
	binary.BigEndian.PutUint16(body[16:18], 80)
	putFixedString(body, 18, "", 16)
	ctx.GameServer.SendResponse(ctx.ClientData, 1005, ctx.UserID, ctx.SeqID, body)
}

// handleMoneyBuyProduct CMD 1102 MONEY_BUY_PRODUCT
// 请求: productId(4) + count(2)。响应: unknown(4) + payMoney(4) + remain(4)，对齐 Lua
// 本地服无米币，购买成功时根据商品表发放物品到背包（与 1104 金豆购买一致）
func handleMoneyBuyProduct(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	payMoney := uint32(0)
	remain := uint32(user.Gold * 100)
	productID := uint32(0)
	count := 1
	if len(ctx.Body) >= 6 {
		productID = binary.BigEndian.Uint32(ctx.Body[0:4])
		count = int(uint32(ctx.Body[4])<<8 | uint32(ctx.Body[5]))
		if count <= 0 {
			count = 1
		}
	}
	// 米币商品表：发放物品或金豆
	if entry, ok := moneyProductMap[productID]; ok {
		if entry.AddGold > 0 {
			user.Gold += entry.AddGold * count
		}
		for _, itemID := range entry.ItemIDs {
			itemKey := strconv.Itoa(itemID)
			if it, has := user.Items[itemKey]; has {
				it.Count += count
				user.Items[itemKey] = it
			} else {
				user.Items[itemKey] = userdb.Item{Count: count, ExpireTime: 0x057E40}
			}
			addClothIfNeeded(user, itemID)
		}
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
		remain = uint32(user.Gold * 100)
		logger.Info(fmt.Sprintf("[1102] 米币购买: productID=%d count=%d 已发放到背包", productID, count))
	} else if entry, ok := goldProductMap[productID]; ok {
		// 金豆商品也可通过米币流程购买（本地 0 米币），发放同一物品
		itemKey := strconv.Itoa(entry.ItemID)
		if it, has := user.Items[itemKey]; has {
			it.Count += count
			user.Items[itemKey] = it
		} else {
			user.Items[itemKey] = userdb.Item{Count: count, ExpireTime: 0x057E40}
		}
		addClothIfNeeded(user, entry.ItemID)
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
		logger.Info(fmt.Sprintf("[1102] 米币购买(金豆商品): productID=%d itemID=%d count=%d 已发放到背包", productID, entry.ItemID, count))
	}
	body := make([]byte, 12)
	binary.BigEndian.PutUint32(body[0:4], 0)
	binary.BigEndian.PutUint32(body[4:8], payMoney)
	binary.BigEndian.PutUint32(body[8:12], remain)
	ctx.GameServer.SendResponse(ctx.ClientData, 1102, ctx.UserID, ctx.SeqID, body)
}

// handleGoldCheckRemain CMD 1105 GOLD_CHECK_REMAIN
// 响应: (gold*100)(4)，对齐 Lua
func handleGoldCheckRemain(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, uint32(user.Gold*100))
	ctx.GameServer.SendResponse(ctx.ClientData, 1105, ctx.UserID, ctx.SeqID, body)
}

// ==================== 协议层/校验 完整协议 ====================

// handleCheckFightCode CMD 1022 验证战斗码，响应空包
func handleCheckFightCode(ctx *gameserver.HandlerContext) {
	ctx.GameServer.SendResponse(ctx.ClientData, 1022, ctx.UserID, ctx.SeqID, []byte{})
}

// handleSystemMessage CMD 8002 系统消息（客户端发，服回空）
func handleSystemMessage(ctx *gameserver.HandlerContext) {
	ctx.GameServer.SendResponse(ctx.ClientData, 8002, ctx.UserID, ctx.SeqID, []byte{})
}

// handleOpenBagGet CMD 9049 响应: count(4)=0 + extra(4)=0
func handleOpenBagGet(ctx *gameserver.HandlerContext) {
	body := make([]byte, 8)
	ctx.GameServer.SendResponse(ctx.ClientData, 9049, ctx.UserID, ctx.SeqID, body)
}

// handleGetPetInfoAlt CMD 11003 另一套编号的 GET_PET_INFO，响应: petCount(4)=0
func handleGetPetInfoAlt(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 11003, ctx.UserID, ctx.SeqID, body)
}

// handleGetPetByCatchTime CMD 11007 响应空包
func handleGetPetByCatchTime(ctx *gameserver.HandlerContext) {
	ctx.GameServer.SendResponse(ctx.ClientData, 11007, ctx.UserID, ctx.SeqID, []byte{})
}

// handleGetSecondBag CMD 11022 响应: count(4)=0
func handleGetSecondBag(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 11022, ctx.UserID, ctx.SeqID, body)
}

// handleReconnect CMD 41983 RECONNECT 响应空包
func handleReconnect(ctx *gameserver.HandlerContext) {
	ctx.GameServer.SendResponse(ctx.ClientData, 41983, ctx.UserID, ctx.SeqID, []byte{})
}

// handleGetMultiForever CMD 46046 响应: count(4)=5 + 5*uint32(0)=20 共24字节
func handleGetMultiForever(ctx *gameserver.HandlerContext) {
	body := make([]byte, 24)
	binary.BigEndian.PutUint32(body[0:4], 5)
	ctx.GameServer.SendResponse(ctx.ClientData, 46046, ctx.UserID, ctx.SeqID, body)
}

// handleGetSuperValue CMD 40001 响应: count(4)=0
func handleGetSuperValue(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 40001, ctx.UserID, ctx.SeqID, body)
}

// handleGetSuperValueByIds CMD 40002 响应: count(4)=0
func handleGetSuperValueByIds(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 40002, ctx.UserID, ctx.SeqID, body)
}

// handleBatchGetBitset CMD 42023 响应: count(4)=0
func handleBatchGetBitset(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 42023, ctx.UserID, ctx.SeqID, body)
}

// handleGetMultiForeverByDb CMD 46057 响应: count(4)=0
func handleGetMultiForeverByDb(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 46057, ctx.UserID, ctx.SeqID, body)
}

// handleGetForeverValue CMD 41080 响应: 4 字节 0
func handleGetForeverValue(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 41080, ctx.UserID, ctx.SeqID, body)
}

// handleFriendListAlt CMD 47334 好友列表另一套编号，响应: count(4)=0
func handleFriendListAlt(ctx *gameserver.HandlerContext) {
	var friends []userdb.Friend
	if ctx.GameServer.UserDB != nil {
		friends = ctx.GameServer.UserDB.GetFriends(ctx.UserID)
	} else {
		user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
		friends = user.Friends
	}
	body := make([]byte, 0, 4+len(friends)*8)
	tmp := make([]byte, 4)
	binary.BigEndian.PutUint32(tmp, uint32(len(friends)))
	body = append(body, tmp...)
	for _, f := range friends {
		binary.BigEndian.PutUint32(tmp, uint32(f.UserID))
		body = append(body, tmp...)
		binary.BigEndian.PutUint32(tmp, uint32(f.TimePoke))
		body = append(body, tmp...)
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 47334, ctx.UserID, ctx.SeqID, body)
}

// handleBlacklistAlt CMD 47335 黑名单另一套编号，响应: blackCount(4) + [userId(4)]*n
func handleBlacklistAlt(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	body := make([]byte, 0, 4+len(user.Blacklist)*4)
	tmp := make([]byte, 4)
	binary.BigEndian.PutUint32(tmp, uint32(len(user.Blacklist)))
	body = append(body, tmp...)
	for _, b := range user.Blacklist {
		binary.BigEndian.PutUint32(tmp, uint32(b.UserID))
		body = append(body, tmp...)
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 47335, ctx.UserID, ctx.SeqID, body)
}

// handleSystemTimeAlt CMD 10301 系统时间另一套编号，响应同 1002: time(4)+4
func handleSystemTimeAlt(ctx *gameserver.HandlerContext) {
	body := make([]byte, 8)
	binary.BigEndian.PutUint32(body[0:4], uint32(time.Now().Unix()))
	ctx.GameServer.SendResponse(ctx.ClientData, 10301, ctx.UserID, ctx.SeqID, body)
}

// handleItemListAlt CMD 4475 物品列表另一套编号（储存箱子/收藏/超能NONO栏等用此协议拉列表）
// 请求格式与 2605 相同: itemType1(4)+itemType2(4)+itemType3(4)；响应格式与 2605 相同，否则客户端两栏不显示
func handleItemListAlt(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	body := buildItemList2605Body(user, ctx.Body)
	n := binary.BigEndian.Uint32(body[0:4])
	logger.Info(fmt.Sprintf("[4475] 物品列表(储存箱子/收藏/超能NONO栏): 返回数量=%d BodyLen=%d", n, len(body)))
	ctx.GameServer.SendResponse(ctx.ClientData, 4475, ctx.UserID, ctx.SeqID, body)
}

// ==================== 地图/玩家 完整协议 ====================

// handleChangeNickName CMD 2061 修改昵称
// 请求: newNick(16)。响应: userId(4)+newNick(16)，对齐 Lua map_handlers.handleChangeNickName
func handleChangeNickName(ctx *gameserver.HandlerContext) {
	newNick := ""
	if len(ctx.Body) >= 16 {
		newNick = string(bytes.TrimRight(ctx.Body[0:16], "\x00"))
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	user.Nick = newNick
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	body := make([]byte, 4+16)
	binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID))
	putFixedString(body, 4, newNick, 16)
	ctx.GameServer.SendResponse(ctx.ClientData, 2061, ctx.UserID, ctx.SeqID, body)
}

// handleChangeColor CMD 2063 修改颜色
// 请求: newColor(4)。响应: userId(4)+newColor(4)+cost(4)+remain(4)，对齐 Lua
func handleChangeColor(ctx *gameserver.HandlerContext) {
	newColor := uint32(0)
	if len(ctx.Body) >= 4 {
		newColor = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	user.Color = int(newColor)
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	body := make([]byte, 16)
	binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID))
	binary.BigEndian.PutUint32(body[4:8], newColor)
	binary.BigEndian.PutUint32(body[8:12], 0)
	binary.BigEndian.PutUint32(body[12:16], uint32(user.Coins))
	ctx.GameServer.SendResponse(ctx.ClientData, 2063, ctx.UserID, ctx.SeqID, body)
}

// handleDanceAction CMD 2103 跳舞动作
// 请求: aid(4)+atype(4)。响应: userId(4)+aid(4)+atype(4)，对齐 Lua
func handleDanceAction(ctx *gameserver.HandlerContext) {
	aid, atype := uint32(0), uint32(0)
	if len(ctx.Body) >= 8 {
		aid = binary.BigEndian.Uint32(ctx.Body[0:4])
		atype = binary.BigEndian.Uint32(ctx.Body[4:8])
	}
	body := make([]byte, 12)
	binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID))
	binary.BigEndian.PutUint32(body[4:8], aid)
	binary.BigEndian.PutUint32(body[8:12], atype)
	ctx.GameServer.SendResponse(ctx.ClientData, 2103, ctx.UserID, ctx.SeqID, body)
}

// handlePeopleTransform CMD 2111 变身
// 请求: transId(4)。响应: userId(4)+transId(4)，对齐 Lua；并广播给同地图其他玩家
func handlePeopleTransform(ctx *gameserver.HandlerContext) {
	transId := uint32(0)
	if len(ctx.Body) >= 4 {
		transId = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	body := make([]byte, 8)
	binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID))
	binary.BigEndian.PutUint32(body[4:8], transId)
	ctx.GameServer.SendResponse(ctx.ClientData, 2111, ctx.UserID, ctx.SeqID, body)
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.MapID > 0 {
		ctx.GameServer.BroadcastToMap(user.MapID, ctx.UserID, 2111, body)
	}
}

// handleOnOrOffFlying CMD 2112 飞行开关
// 请求: flyMode(4)。响应/广播 body: userId(4)+flyMode(4)，解包协议与 Flash 端 2112 监听一致
// 先广播 2003（带更新后的 actionType）再广播 2112，便于客户端先更新 UserInfo 再根据 2112 刷新他人飞行显示
func handleOnOrOffFlying(ctx *gameserver.HandlerContext) {
	flyMode := uint32(0)
	if len(ctx.Body) >= 4 {
		flyMode = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	user.FlyMode = int(flyMode) // 持久化到内存，供 2001/2003 使别人可见飞行状态
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	body := make([]byte, 8)
	binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID)) // 包体前 4 字节：谁在飞
	binary.BigEndian.PutUint32(body[4:8], flyMode)             // 包体后 4 字节：0=落地 非0=飞行
	ctx.GameServer.SendResponse(ctx.ClientData, 2112, ctx.UserID, ctx.SeqID, body)
	if user.MapID > 0 {
		// 1. 先广播 2003，让同图玩家先拿到含 actionType 的列表
		listBody := buildMapPlayerListForMap(ctx.GameServer, user.MapID)
		ctx.GameServer.BroadcastToMap(user.MapID, 0, 2003, listBody)
		clients := ctx.GameServer.GetClientsOnMap(user.MapID)
		for _, c := range clients {
			if c.UserID == ctx.UserID {
				continue
			}
			// 2. 2112：包体 飞行者uid(4)+flyMode(4)，包头用飞行者 A 的 userId
			ctx.GameServer.SendResponse(c, 2112, ctx.UserID, 0, body)
			if user.Nono.State == 1 {
				body9019 := make([]byte, 36)
				binary.BigEndian.PutUint32(body9019[0:4], uint32(ctx.UserID))
				binary.BigEndian.PutUint32(body9019[4:8], uint32(user.Nono.SuperNono))
				binary.BigEndian.PutUint32(body9019[8:12], 1)
				nickBytes := []byte(user.Nono.Nick)
				if len(nickBytes) > 16 {
					nickBytes = nickBytes[:16]
				}
				copy(body9019[12:28], nickBytes)
				binary.BigEndian.PutUint32(body9019[28:32], uint32(user.Nono.Color))
				binary.BigEndian.PutUint32(body9019[32:36], 0)
				ctx.GameServer.SendResponse(c, 9019, ctx.UserID, 0, body9019)
			}
		}
		logger.Info(fmt.Sprintf("[2112] 飞行状态广播: UID=%d FlyMode=%d MapID=%d 已发2003+2112", ctx.UserID, flyMode, user.MapID))
	}
}

// ==================== 交换 完整协议 ====================

// handleExchangeNewYear CMD 2065 新年交换，响应: 4 字节 0
func handleExchangeNewYear(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 2065, ctx.UserID, ctx.SeqID, body)
}

// handleExchangeOre CMD 2251 矿石交换，响应: ret(4)=0 + count(4)=0
func handleExchangeOre(ctx *gameserver.HandlerContext) {
	body := make([]byte, 8)
	ctx.GameServer.SendResponse(ctx.ClientData, 2251, ctx.UserID, ctx.SeqID, body)
}

// handleExchangePetComplete CMD 2902 精灵交换完成，响应: 4 字节 0
func handleExchangePetComplete(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 2902, ctx.UserID, ctx.SeqID, body)
}

// ==================== 任务 完整协议 ====================

// handleAddTaskBuf CMD 2204 添加/更新任务缓存（NPC 对话进度等）
// 请求（前端）: taskId(4) + buf(20 字节)，SetTaskBuf/TasksManager 为 writeUnsignedInt(taskId)+writeBytes(ByteArray(20))
// 兼容旧格式: taskId(4) + index(1) + value(4)。响应: 4 字节 0
func handleAddTaskBuf(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) >= 4 {
		taskID := binary.BigEndian.Uint32(ctx.Body[0:4])
		user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
		if user.Tasks == nil {
			user.Tasks = make(map[string]userdb.Task)
		}
		key := strconv.FormatUint(uint64(taskID), 10)
		t := user.Tasks[key]
		if t.Buf == nil {
			t.Buf = make(map[int]int)
		}
		if len(ctx.Body) >= 24 {
			for i := 0; i < 20; i++ {
				t.Buf[i] = int(ctx.Body[4+i])
			}
		} else if len(ctx.Body) >= 5 {
			index := int(ctx.Body[4])
			value := 0
			if len(ctx.Body) >= 9 {
				value = int(binary.BigEndian.Uint32(ctx.Body[5:9]))
			}
			t.Buf[index] = value
		}
		user.Tasks[key] = t
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
	}
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 2204, ctx.UserID, ctx.SeqID, body)
}

// handleGetDailyTaskBuf CMD 2234 获取每日任务缓存，响应: 8 字节 (0,0)，对齐 Lua task_handlers
func handleGetDailyTaskBuf(ctx *gameserver.HandlerContext) {
	body := make([]byte, 8)
	ctx.GameServer.SendResponse(ctx.ClientData, 2234, ctx.UserID, ctx.SeqID, body)
}

// handleInform CMD 8001 通知，响应: type(4)+userID(4)+nick(16)+accept(4)+serverID(4)+mapType(4)+mapID(4)+mapName(64)=104 字节
func handleInform(ctx *gameserver.HandlerContext) {
	body := make([]byte, 104)
	binary.BigEndian.PutUint32(body[0:4], 0)
	binary.BigEndian.PutUint32(body[4:8], uint32(ctx.UserID))
	putFixedString(body, 8, "", 16)
	binary.BigEndian.PutUint32(body[24:28], 0)
	binary.BigEndian.PutUint32(body[28:32], 1)
	binary.BigEndian.PutUint32(body[32:36], 0)
	binary.BigEndian.PutUint32(body[36:40], 301)
	putFixedString(body, 40, "", 64)
	ctx.GameServer.SendResponse(ctx.ClientData, 8001, ctx.UserID, ctx.SeqID, body)
}

// handleGetBossMonster CMD 8004 获取 BOSS 怪物，响应: bonusID(4)+petID(4)+captureTm(4)+itemCount(4)=16 字节
func handleGetBossMonster(ctx *gameserver.HandlerContext) {
	body := make([]byte, 16)
	ctx.GameServer.SendResponse(ctx.ClientData, 8004, ctx.UserID, ctx.SeqID, body)
}

// handleSetTitle CMD 3404 SETTITLE 设置当前称号
// 请求: titleId(4)。响应: 4 字节 0 表示成功。对齐 seer_cmdlist ACHIEVETITLELIST(3403)/SETTITLE(3404)
func handleSetTitle(ctx *gameserver.HandlerContext) {
	titleID := 0
	if len(ctx.Body) >= 4 {
		titleID = int(binary.BigEndian.Uint32(ctx.Body[0:4]))
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	user.CurTitle = titleID
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 3404, ctx.UserID, ctx.SeqID, body)
}

// handleMailGetList CMD 2751 获取邮件列表（空列表）
// 对齐 Lua: mail_handlers.handleMailGetList
func handleMailGetList(ctx *gameserver.HandlerContext) {
	body := make([]byte, 8)
	// total=0, count=0
	ctx.GameServer.SendResponse(ctx.ClientData, 2751, ctx.UserID, ctx.SeqID, body)
}

// handleMailGetUnread CMD 2757 获取未读邮件数（0）
// 对齐 Lua: mail_handlers.handleMailGetUnread
func handleMailGetUnread(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	// unread=0
	ctx.GameServer.SendResponse(ctx.ClientData, 2757, ctx.UserID, ctx.SeqID, body)
}

// handleGetRelationList CMD 2150 获取好友/黑名单列表
// 对齐 Lua: friend_handlers.handleGetRelationList
func handleGetRelationList(ctx *gameserver.HandlerContext) {
	// 从数据库加载最新的好友和黑名单列表（确保数据最新）
	var friends []userdb.Friend
	var black []userdb.BlacklistEntry
	if ctx.GameServer.UserDB != nil {
		friends = ctx.GameServer.UserDB.GetFriends(ctx.UserID)
		black = ctx.GameServer.UserDB.GetBlacklist(ctx.UserID)
		// 更新内存中的用户数据
		user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
		user.Friends = friends
		user.Blacklist = black
	} else {
		user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
		friends = user.Friends
		black = user.Blacklist
	}

	// friendCount(4) + blackCount(4) + [FriendInfo]*n + [BlackInfo]*m
	body := make([]byte, 0, 8+len(friends)*8+len(black)*4)
	tmp := make([]byte, 4)
	binary.BigEndian.PutUint32(tmp, uint32(len(friends)))
	body = append(body, tmp...)
	binary.BigEndian.PutUint32(tmp, uint32(len(black)))
	body = append(body, tmp...)

	for _, f := range friends {
		binary.BigEndian.PutUint32(tmp, uint32(f.UserID))
		body = append(body, tmp...)
		binary.BigEndian.PutUint32(tmp, uint32(f.TimePoke))
		body = append(body, tmp...)
	}
	for _, b := range black {
		binary.BigEndian.PutUint32(tmp, uint32(b.UserID))
		body = append(body, tmp...)
	}

	ctx.GameServer.SendResponse(ctx.ClientData, 2150, ctx.UserID, ctx.SeqID, body)
}

// handleFriendAdd CMD 2151 发送好友请求（需要对方同意）
// 请求: friendID(4)
// 响应: friendID(4) - 返回请求的好友ID
// 同时向对方推送 8001 (INFORM) 通知，type=FRIEND_ADD
func handleFriendAdd(ctx *gameserver.HandlerContext) {
	var friendID int64
	if len(ctx.Body) >= 4 {
		friendID = int64(binary.BigEndian.Uint32(ctx.Body[0:4]))
	}

	if friendID <= 0 {
		// 无效的好友ID
		body := make([]byte, 4)
		ctx.GameServer.SendResponse(ctx.ClientData, 2151, ctx.UserID, ctx.SeqID, body)
		return
	}

	// 不能添加自己为好友
	if friendID == ctx.UserID {
		body := make([]byte, 4)
		ctx.GameServer.SendResponse(ctx.ClientData, 2151, ctx.UserID, ctx.SeqID, body)
		logger.Info(fmt.Sprintf("[2151] 不能添加自己为好友: UID=%d", ctx.UserID))
		return
	}

	// 检查是否已经是好友
	if ctx.GameServer.UserDB != nil {
		if ctx.GameServer.UserDB.IsFriend(ctx.UserID, friendID) {
			body := make([]byte, 4)
			binary.BigEndian.PutUint32(body, uint32(friendID))
			ctx.GameServer.SendResponse(ctx.ClientData, 2151, ctx.UserID, ctx.SeqID, body)
			logger.Info(fmt.Sprintf("[2151] 已经是好友: UID=%d FriendID=%d", ctx.UserID, friendID))
			return
		}
	}

	// 向对方发送好友请求通知（8001 INFORM）
	// InformInfo: type(4) + userID(4) + nick(16) + accept(4) + serverID(4) + mapType(4) + mapID(4) + mapName(64) = 104字节
	requestUser := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	requestNick := requestUser.Nick
	if requestNick == "" {
		requestNick = fmt.Sprintf("Seer%d", ctx.UserID)
	}

	// 构建 InformInfo 包体
	informBody := make([]byte, 104)
	binary.BigEndian.PutUint32(informBody[0:4], 2151) // type = FRIEND_ADD
	binary.BigEndian.PutUint32(informBody[4:8], uint32(ctx.UserID))
	putFixedString := func(s string, n int, offset int) {
		b := []byte(s)
		if len(b) > n {
			b = b[:n]
		}
		copy(informBody[offset:offset+len(b)], b)
		for i := len(b); i < n; i++ {
			informBody[offset+i] = 0
		}
	}
	putFixedString(requestNick, 16, 8)
	binary.BigEndian.PutUint32(informBody[24:28], 0) // accept = 0（请求）
	binary.BigEndian.PutUint32(informBody[28:32], 1) // serverID = 1
	binary.BigEndian.PutUint32(informBody[32:36], 0) // mapType = 0
	mapID := requestUser.MapID
	if mapID <= 0 {
		mapID = 1
	}
	binary.BigEndian.PutUint32(informBody[36:40], uint32(mapID))
	putFixedString("", 64, 40) // mapName

	// 向对方推送通知
	if targetClient := ctx.GameServer.GetClientByUserID(friendID); targetClient != nil && targetClient.LoggedIn {
		ctx.GameServer.SendResponse(targetClient, 8001, friendID, 0, informBody)
		logger.Info(fmt.Sprintf("[2151] 发送好友请求通知: UID=%d -> FriendID=%d", ctx.UserID, friendID))
	} else {
		logger.Info(fmt.Sprintf("[2151] 对方不在线，无法发送好友请求: UID=%d -> FriendID=%d", ctx.UserID, friendID))
	}

	// 响应：返回 friendID（与 Lua 版本一致）
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, uint32(friendID))
	ctx.GameServer.SendResponse(ctx.ClientData, 2151, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2151] 发送好友请求: UID=%d FriendID=%d", ctx.UserID, friendID))
}

// handleFriendAnswer CMD 2152 好友请求回复
// 请求: targetID(4) + accept(4) - accept: 1=接受, 0=拒绝
// 响应: accept(4)
// 如果接受，双方都添加为好友，并向请求方发送 8001 (INFORM) 通知
func handleFriendAnswer(ctx *gameserver.HandlerContext) {
	var targetID int64
	var accept uint32
	if len(ctx.Body) >= 8 {
		targetID = int64(binary.BigEndian.Uint32(ctx.Body[0:4]))
		accept = binary.BigEndian.Uint32(ctx.Body[4:8])
	}

	if accept == 1 && targetID > 0 && ctx.GameServer.UserDB != nil {
		// 接受好友请求：双方都添加为好友
		// 1. 我添加对方为好友
		success1, msg1 := ctx.GameServer.UserDB.AddFriend(ctx.UserID, targetID)
		if success1 {
			logger.Info(fmt.Sprintf("[2152] 接受好友请求: UID=%d 添加 FriendID=%d", ctx.UserID, targetID))
			// 更新内存中的用户数据
			user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
			user.Friends = ctx.GameServer.UserDB.GetFriends(ctx.UserID)
		} else {
			logger.Info(fmt.Sprintf("[2152] 接受好友请求失败: UID=%d FriendID=%d Reason=%s", ctx.UserID, targetID, msg1))
		}

		// 2. 对方也添加我为好友（双向好友关系）
		success2, msg2 := ctx.GameServer.UserDB.AddFriend(targetID, ctx.UserID)
		if success2 {
			logger.Info(fmt.Sprintf("[2152] 双向添加好友: FriendID=%d 添加 UID=%d", targetID, ctx.UserID))
			// 更新对方内存中的用户数据
			if targetUser := ctx.GameServer.GetOrCreateUser(targetID); targetUser != nil {
				targetUser.Friends = ctx.GameServer.UserDB.GetFriends(targetID)
			}
		} else {
			logger.Info(fmt.Sprintf("[2152] 双向添加好友失败: FriendID=%d UID=%d Reason=%s", targetID, ctx.UserID, msg2))
		}

		// 3. 向请求方发送接受通知（8001 INFORM）
		// InformInfo: type(4) + userID(4) + nick(16) + accept(4) + serverID(4) + mapType(4) + mapID(4) + mapName(64) = 104字节
		myUser := ctx.GameServer.GetOrCreateUser(ctx.UserID)
		myNick := myUser.Nick
		if myNick == "" {
			myNick = fmt.Sprintf("Seer%d", ctx.UserID)
		}

		informBody := make([]byte, 104)
		binary.BigEndian.PutUint32(informBody[0:4], 2152) // type = FRIEND_ANSWER
		binary.BigEndian.PutUint32(informBody[4:8], uint32(ctx.UserID))
		putFixedString := func(s string, n int, offset int) {
			b := []byte(s)
			if len(b) > n {
				b = b[:n]
			}
			copy(informBody[offset:offset+len(b)], b)
			for i := len(b); i < n; i++ {
				informBody[offset+i] = 0
			}
		}
		putFixedString(myNick, 16, 8)
		binary.BigEndian.PutUint32(informBody[24:28], 1) // accept = 1（已接受）
		binary.BigEndian.PutUint32(informBody[28:32], 1) // serverID = 1
		binary.BigEndian.PutUint32(informBody[32:36], 0) // mapType = 0
		mapID := myUser.MapID
		if mapID <= 0 {
			mapID = 1
		}
		binary.BigEndian.PutUint32(informBody[36:40], uint32(mapID))
		putFixedString("", 64, 40) // mapName

		// 向请求方推送通知
		if requestClient := ctx.GameServer.GetClientByUserID(targetID); requestClient != nil && requestClient.LoggedIn {
			ctx.GameServer.SendResponse(requestClient, 8001, targetID, 0, informBody)
			logger.Info(fmt.Sprintf("[2152] 发送接受通知: UID=%d -> RequestID=%d", ctx.UserID, targetID))
		}
	} else if accept == 0 && targetID > 0 {
		logger.Info(fmt.Sprintf("[2152] 拒绝好友请求: UID=%d FriendID=%d", ctx.UserID, targetID))
		// 可选：向请求方发送拒绝通知
	}

	// 响应：返回 accept
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, accept)
	ctx.GameServer.SendResponse(ctx.ClientData, 2152, ctx.UserID, ctx.SeqID, body)
}

// handleFriendRemove CMD 2153 删除好友
// 请求: friendID(4)
// 响应: 空
func handleFriendRemove(ctx *gameserver.HandlerContext) {
	var friendID int64
	if len(ctx.Body) >= 4 {
		friendID = int64(binary.BigEndian.Uint32(ctx.Body[0:4]))
	}

	if friendID > 0 && ctx.GameServer.UserDB != nil {
		success := ctx.GameServer.UserDB.RemoveFriend(ctx.UserID, friendID)
		if success {
			logger.Info(fmt.Sprintf("[2153] 删除好友成功: UID=%d FriendID=%d", ctx.UserID, friendID))
			// 更新内存中的用户数据
			user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
			user.Friends = ctx.GameServer.UserDB.GetFriends(ctx.UserID)
		} else {
			logger.Info(fmt.Sprintf("[2153] 删除好友失败（可能不存在）: UID=%d FriendID=%d", ctx.UserID, friendID))
		}
	}

	// 响应：空（与 Lua 版本一致）
	ctx.GameServer.SendResponse(ctx.ClientData, 2153, ctx.UserID, ctx.SeqID, []byte{})
}

// handleBlackAdd CMD 2154 添加黑名单
// 请求: targetID(4)
// 响应: 空
func handleBlackAdd(ctx *gameserver.HandlerContext) {
	var targetID int64
	if len(ctx.Body) >= 4 {
		targetID = int64(binary.BigEndian.Uint32(ctx.Body[0:4]))
	}

	if targetID > 0 && ctx.GameServer.UserDB != nil {
		success, msg := ctx.GameServer.UserDB.AddBlacklist(ctx.UserID, targetID)
		if success {
			logger.Info(fmt.Sprintf("[2154] 添加黑名单成功: UID=%d TargetID=%d", ctx.UserID, targetID))
			// 更新内存中的用户数据
			user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
			user.Blacklist = ctx.GameServer.UserDB.GetBlacklist(ctx.UserID)
		} else {
			logger.Info(fmt.Sprintf("[2154] 添加黑名单失败: UID=%d TargetID=%d Reason=%s", ctx.UserID, targetID, msg))
		}
	}

	// 响应：空（与 Lua 版本一致）
	ctx.GameServer.SendResponse(ctx.ClientData, 2154, ctx.UserID, ctx.SeqID, []byte{})
}

// handleBlackRemove CMD 2155 移除黑名单
// 请求: targetID(4)
// 响应: 空
func handleBlackRemove(ctx *gameserver.HandlerContext) {
	var targetID int64
	if len(ctx.Body) >= 4 {
		targetID = int64(binary.BigEndian.Uint32(ctx.Body[0:4]))
	}

	if targetID > 0 && ctx.GameServer.UserDB != nil {
		success := ctx.GameServer.UserDB.RemoveBlacklist(ctx.UserID, targetID)
		if success {
			logger.Info(fmt.Sprintf("[2155] 移除黑名单成功: UID=%d TargetID=%d", ctx.UserID, targetID))
			// 更新内存中的用户数据
			user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
			user.Blacklist = ctx.GameServer.UserDB.GetBlacklist(ctx.UserID)
		} else {
			logger.Info(fmt.Sprintf("[2155] 移除黑名单失败（可能不存在）: UID=%d TargetID=%d", ctx.UserID, targetID))
		}
	}

	// 响应：空（与 Lua 版本一致）
	ctx.GameServer.SendResponse(ctx.ClientData, 2155, ctx.UserID, ctx.SeqID, []byte{})
}

// handleSeeOnline CMD 2157 查看在线状态
// 请求: count(4) + userIDs[count] (每个4字节)
// 响应: onlineCount(4) + [OnLineInfo]...
// OnLineInfo: userID(4) + serverID(4) + mapType(4) + mapID(4) = 16 bytes
func handleSeeOnline(ctx *gameserver.HandlerContext) {
	var requestCount uint32
	if len(ctx.Body) >= 4 {
		requestCount = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	// 读取所有请求的用户ID
	userIDs := make([]int64, 0, requestCount)
	for i := uint32(0); i < requestCount; i++ {
		offset := 4 + int(i)*4
		if len(ctx.Body) >= offset+4 {
			userID := int64(binary.BigEndian.Uint32(ctx.Body[offset : offset+4]))
			userIDs = append(userIDs, userID)
		}
	}

	// 构建在线用户列表
	onlineUsers := make([]struct {
		userID   int64
		serverID uint32
		mapType  uint32
		mapID    uint32
	}, 0)

	for _, targetID := range userIDs {
		// 检查用户是否在线
		if client := ctx.GameServer.GetClientByUserID(targetID); client != nil && client.LoggedIn {
			user := ctx.GameServer.GetOrCreateUser(targetID)
			mapID := user.MapID
			if mapID == 0 {
				mapID = 1
			}
			mapType := uint32(0)  // 默认地图类型
			serverID := uint32(1) // 当前服务器ID

			onlineUsers = append(onlineUsers, struct {
				userID   int64
				serverID uint32
				mapType  uint32
				mapID    uint32
			}{
				userID:   targetID,
				serverID: serverID,
				mapType:  mapType,
				mapID:    uint32(mapID),
			})
		}
	}

	// 构建响应: count(4) + [OnLineInfo]...
	body := make([]byte, 0, 4+len(onlineUsers)*16)
	tmp := make([]byte, 4)
	binary.BigEndian.PutUint32(tmp, uint32(len(onlineUsers)))
	body = append(body, tmp...)

	for _, info := range onlineUsers {
		binary.BigEndian.PutUint32(tmp, uint32(info.userID))
		body = append(body, tmp...)
		binary.BigEndian.PutUint32(tmp, info.serverID)
		body = append(body, tmp...)
		binary.BigEndian.PutUint32(tmp, info.mapType)
		body = append(body, tmp...)
		binary.BigEndian.PutUint32(tmp, info.mapID)
		body = append(body, tmp...)
	}

	ctx.GameServer.SendResponse(ctx.ClientData, 2157, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2157] 查看在线状态: UID=%d Requested=%d Online=%d", ctx.UserID, requestCount, len(onlineUsers)))
}

// handleRequestOut CMD 2158 发送请求
// 响应: 空
func handleRequestOut(ctx *gameserver.HandlerContext) {
	// 响应：空（与 Lua 版本一致）
	ctx.GameServer.SendResponse(ctx.ClientData, 2158, ctx.UserID, ctx.SeqID, []byte{})
}

// handleRequestAnswer CMD 2159 请求回复
// 响应: 空
func handleRequestAnswer(ctx *gameserver.HandlerContext) {
	// 响应：空（与 Lua 版本一致）
	ctx.GameServer.SendResponse(ctx.ClientData, 2159, ctx.UserID, ctx.SeqID, []byte{})
}

// handleGetExchangeInfo CMD 70001 获取交换/荣誉信息（当前返回 0）
// 对齐 Lua: misc_handlers.handleGetExchangeInfo
func handleGetExchangeInfo(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	// honorValue=0
	ctx.GameServer.SendResponse(ctx.ClientData, 70001, ctx.UserID, ctx.SeqID, body)
}

// handleXinCheck CMD 50004 客户端信息上报（空回包）
func handleXinCheck(ctx *gameserver.HandlerContext) {
	ctx.GameServer.SendResponse(ctx.ClientData, 50004, ctx.UserID, ctx.SeqID, []byte{})
}

// handleXinGetQuadTime CMD 50008 获取四倍经验时间（0）
func handleXinGetQuadTime(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 50008, ctx.UserID, ctx.SeqID, body)
}

// handleGoldOnlineCheckRemain CMD 1106 在线金豆余额（客户端 /100 显示）+ 赛尔豆
func handleGoldOnlineCheckRemain(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	// 客户端读取: 第1个 readUnsignedInt()/100 为金豆数, 第2个 readUnsignedInt() 为赛尔豆
	body := make([]byte, 8)
	binary.BigEndian.PutUint32(body[0:4], uint32(user.Gold*100))
	binary.BigEndian.PutUint32(body[4:8], uint32(user.Coins))
	ctx.GameServer.SendResponse(ctx.ClientData, 1106, ctx.UserID, ctx.SeqID, body)
}

// handleMoneyCheckPsw CMD 1101 米币支付密码检查
// 客户端：若返回 1 则继续发送 MONEY_CHECK_REMAIN(1103)；否则提示设置支付密码
func handleMoneyCheckPsw(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, 1) // 1 = 已设置/跳过，继续购买流程
	ctx.GameServer.SendResponse(ctx.ClientData, 1101, ctx.UserID, ctx.SeqID, body)
}

// handleMoneyCheckRemain CMD 1103 米币余额检查（客户端 /100 显示为米币数）
func handleMoneyCheckRemain(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	// 本地服无米币体系，用金豆*100 返回，使购买流程可走通
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, uint32(user.Gold*100))
	ctx.GameServer.SendResponse(ctx.ClientData, 1103, ctx.UserID, ctx.SeqID, body)
}

// goldProductEntry 金豆商城商品（与 xml/225.xml GoldProduct 一致）
type goldProductEntry struct {
	Price  int
	ItemID int
}

// goldProductMap 金豆商品 productID -> 价格与物品ID（来自 225.xml）
var goldProductMap = map[uint32]goldProductEntry{
	240000: {50, 300006}, 240001: {5, 300024}, 240002: {10, 300025}, 240003: {5, 300026},
	240004: {5, 300027}, 240005: {30, 100266}, 240006: {30, 100267}, 240007: {30, 100268},
	240008: {10, 400054}, 240009: {10, 300028}, 240010: {5, 300029}, 240011: {2, 300030},
	240012: {2, 300031}, 240013: {3, 300032}, 240014: {3, 300033}, 240015: {4, 300034},
	240016: {10, 300035}, 240017: {10, 300036}, 240018: {15, 300037}, 240019: {15, 300038},
	240020: {15, 300039}, 240021: {15, 300040}, 240022: {15, 300041}, 240023: {15, 300042},
	240024: {50, 300043}, 240025: {5, 300044}, 240026: {80, 300009}, 240027: {4, 300045},
	240028: {4, 300046}, 240029: {6, 300047}, 240030: {6, 300048}, 240031: {8, 300049},
	240032: {16, 300050},
}

// moneyProductEntry 米币商品（来自 MoneyProductXMLInfo）：可发放多个物品（套装）或加金豆
type moneyProductEntry struct {
	ItemIDs []int // 物品 ID 列表（套装为多个）
	AddGold int   // 若>0 则加金豆（如 200000/200001/200002 买金豆）
}

// moneyProductMap 米币商品 productID -> 物品/金豆（与 MoneyProductXMLInfo 一致）
var moneyProductMap = map[uint32]moneyProductEntry{
	200000: {nil, 10}, 200001: {nil, 50}, 200002: {nil, 100},
	200051: {[]int{100020}, 0}, 200052: {[]int{100021}, 0}, 200053: {[]int{100022}, 0}, 200054: {[]int{100023}, 0},
	200055: {[]int{100020, 100021, 100022, 100023}, 0},
	200056: {[]int{100036}, 0}, 200057: {[]int{100037}, 0}, 200058: {[]int{100038}, 0}, 200059: {[]int{100039}, 0},
	200060: {[]int{100036, 100037, 100038, 100039}, 0},
	200061: {[]int{100255}, 0}, 200062: {[]int{100256}, 0}, 200063: {[]int{100257}, 0}, 200064: {[]int{100258}, 0}, 200065: {[]int{100259}, 0},
	200066: {[]int{100255, 100256, 100257, 100258, 100259}, 0},
	200067: {[]int{100261}, 0}, 200068: {[]int{100262}, 0}, 200069: {[]int{100263}, 0}, 200070: {[]int{100264}, 0}, 200071: {[]int{100265}, 0},
	200072: {[]int{100261, 100262, 100263, 100264, 100265}, 0},
	200073: {[]int{100162}, 0}, 200074: {[]int{100223}, 0}, 200075: {[]int{100245}, 0}, 200077: {[]int{500522}, 0},
	200078: {[]int{500003}, 0}, 200079: {[]int{500505}, 0}, 200080: {[]int{500506}, 0}, 200081: {[]int{500507}, 0}, 200082: {[]int{500508}, 0},
	200083: {[]int{500509}, 0}, 200084: {[]int{500523}, 0}, 200085: {[]int{500516}, 0}, 200086: {[]int{500806}, 0},
	200087: {[]int{100087}, 0}, 200088: {[]int{100088}, 0}, 200089: {[]int{100089}, 0}, 200090: {[]int{100090}, 0}, 200091: {[]int{100091}, 0},
	200092: {[]int{100087, 100088, 100089, 100090, 100091}, 0},
	200093: {[]int{100033}, 0}, 200094: {[]int{100034}, 0}, 200095: {[]int{100035}, 0}, 200096: {[]int{100033, 100034, 100035}, 0},
	200097: {[]int{100306}, 0}, 200098: {[]int{100307}, 0}, 200099: {[]int{100308}, 0}, 200100: {[]int{100309}, 0}, 200101: {[]int{100310}, 0},
	200102: {[]int{100306, 100307, 100308, 100309, 100310}, 0},
	200103: {[]int{100311}, 0}, 200104: {[]int{100312}, 0}, 200105: {[]int{100313}, 0}, 200106: {[]int{100314}, 0}, 200107: {[]int{100315}, 0},
	200108: {[]int{100311, 100312, 100313, 100314, 100315}, 0},
	200109: {[]int{500002}, 0}, 200110: {[]int{500511}, 0}, 200111: {[]int{500512}, 0}, 200112: {[]int{500513}, 0}, 200113: {[]int{500515}, 0},
	200114: {[]int{500519}, 0}, 200115: {[]int{500520}, 0}, 200116: {[]int{500521}, 0}, 200117: {[]int{500803}, 0},
	200118: {[]int{100329}, 0}, 200119: {[]int{100330}, 0}, 200120: {[]int{100331}, 0}, 200121: {[]int{100332}, 0},
	200122: {[]int{100329, 100330, 100331, 100332}, 0},
	200123: {[]int{100334}, 0}, 200124: {[]int{100335}, 0}, 200125: {[]int{100336}, 0}, 200126: {[]int{100337}, 0},
	200127: {[]int{100334, 100335, 100336, 100337}, 0},
	200128: {[]int{100338}, 0}, 200129: {[]int{100339}, 0}, 200130: {[]int{100340}, 0}, 200131: {[]int{100341}, 0},
	200132: {[]int{100338, 100339, 100340, 100341}, 0},
}

// handleGoldBuyProduct CMD 1104 金豆购买商品
// 请求: productID(4) + count(2 字节 short 大端)
// 响应: result(4) + payGold*100(4) + gold*100(4)，客户端 GoldBuyProductInfo 解析 payGold/gold 为 /100
// 失败时返回 errorCode 10017，客户端 ParseSocketError 显示"购买失败"
const goldBuyErrorCode = 10017 // 客户端显示"购买失败"

func handleGoldBuyProduct(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 6 {
		ctx.GameServer.SendErrorResponse(ctx.ClientData, 1104, ctx.UserID, ctx.SeqID, goldBuyErrorCode)
		return
	}
	productID := binary.BigEndian.Uint32(ctx.Body[0:4])
	count := int(uint32(ctx.Body[4])<<8 | uint32(ctx.Body[5]))
	if count <= 0 {
		count = 1
	}
	entry, ok := goldProductMap[productID]
	if !ok {
		logger.Warning(fmt.Sprintf("[1104] 未知金豆商品 productID=%d", productID))
		ctx.GameServer.SendErrorResponse(ctx.ClientData, 1104, ctx.UserID, ctx.SeqID, goldBuyErrorCode)
		return
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	totalCost := entry.Price * count
	if user.Gold < totalCost {
		logger.Warning(fmt.Sprintf("[1104] 金豆不足: 需要%d 当前%d", totalCost, user.Gold))
		ctx.GameServer.SendErrorResponse(ctx.ClientData, 1104, ctx.UserID, ctx.SeqID, goldBuyErrorCode)
		return
	}
	user.Gold -= totalCost
	itemKey := strconv.Itoa(entry.ItemID)
	if it, has := user.Items[itemKey]; has {
		it.Count += count
		user.Items[itemKey] = it
	} else {
		user.Items[itemKey] = userdb.Item{Count: count, ExpireTime: 0x057E40}
	}
	// 服装/套装（100000-199999）需同时加入 Clothes，否则“我的服装”中不显示
	addClothIfNeeded(user, entry.ItemID)
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	// 响应: result(4) + payGold*100(4) + gold*100(4)
	body := make([]byte, 12)
	binary.BigEndian.PutUint32(body[0:4], 0)
	binary.BigEndian.PutUint32(body[4:8], uint32(totalCost*100))
	binary.BigEndian.PutUint32(body[8:12], uint32(user.Gold*100))
	ctx.GameServer.SendResponse(ctx.ClientData, 1104, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[1104] 金豆购买: productID=%d itemID=%d count=%d 消耗金豆=%d 剩余=%d", productID, entry.ItemID, count, totalCost, user.Gold))
}

// handleNonoInfo CMD 9003 获取 NONO 信息
// 包体前 4 字节为目标 UserID（大端）；不传或为 0 则查自己的。返回该用户的 NONO 信息，避免打开别人 NONO 却显示自己的。
// 对齐 Lua: nono_handlers.handleNonoInfo（body 固定 90 字节）
func handleNonoInfo(ctx *gameserver.HandlerContext) {
	targetID := ctx.UserID
	if len(ctx.Body) >= 4 {
		if id := int64(binary.BigEndian.Uint32(ctx.Body[0:4])); id > 0 {
			targetID = id
		}
	}

	user := ctx.GameServer.GetOrCreateUser(targetID)
	// 确保形态值根据超能等级自动更新（超能等级模型）
	if user.Nono.SuperLevel > 0 {
		updateSuperNonoTypeByLevel(user)
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(targetID, user)
		}
	}
	n := user.Nono

	// 每人最多1只：若在基地且NONO已召唤跟随(State==1)，则 Flag 置 0，避免客户端在基地再画一只（含充电态）
	isBase := user.MapID > 10000 || user.MapID == int(targetID)
	nonoFlag := uint32(n.Flag)
	if isBase && n.State == 1 {
		nonoFlag = 0
	}

	buf := make([]byte, 0, 90)
	writeU32 := func(v uint32) {
		tmp := make([]byte, 4)
		binary.BigEndian.PutUint32(tmp, v)
		buf = append(buf, tmp...)
	}
	writeU16 := func(v uint16) {
		tmp := make([]byte, 2)
		binary.BigEndian.PutUint16(tmp, v)
		buf = append(buf, tmp...)
	}
	writeFixedString := func(s string, nLen int) {
		b := []byte(s)
		if len(b) > nLen {
			b = b[:nLen]
		}
		buf = append(buf, b...)
		if len(b) < nLen {
			buf = append(buf, make([]byte, nLen-len(b))...)
		}
	}

	writeU32(uint32(targetID))  // 目标用户 userId（查谁就返回谁的）
	writeU32(nonoFlag)         // flag（基地且跟随时置0，不显示基地NONO）
	writeU32(uint32(n.State))  // state
	writeFixedString(n.Nick, 16) // nick
	// 返回实际的超能NONO形态值（1-5），而不是布尔值，客户端据此加载对应的SWF文件
	writeU32(uint32(n.SuperNono)) // superNono形态值（1-5）
	logger.Info(fmt.Sprintf("[9003] NONO信息: 请求者=%d 目标=%d SuperLevel=%d SuperNono形态=%d (应加载nono_%d.swf)",
		ctx.UserID, targetID, n.SuperLevel, n.SuperNono, n.SuperNono))
	writeU32(uint32(n.Color))        // color
	writeU32(uint32(n.Power * 1000)) // power (*1000)
	writeU32(uint32(n.Mate * 1000))  // mate (*1000)
	writeU32(uint32(n.IQ))           // iq
	writeU16(uint16(n.AI))           // ai (2)
	if n.Birth > 0 {
		writeU32(uint32(n.Birth))
	} else {
		writeU32(uint32(time.Now().Unix()))
	}
	writeU32(uint32(n.ChargeTime)) // chargeTime
	// func 位图 20 字节，客户端 NonoInfo 按每字节 8 位解析为 func[]，刷新后据此恢复已开启的芯片
	funcBits := make([]byte, 20)
	if len(n.Func) > 0 {
		copy(funcBits, n.Func)
		if len(n.Func) < 20 {
			copy(funcBits[len(n.Func):], make([]byte, 20-len(n.Func)))
		}
	}
	buf = append(buf, funcBits...)
	writeU32(uint32(n.SuperEnergy))
	writeU32(uint32(n.SuperLevel))
	// 客户端用 superStage 拼 nono_N.swf，写形态值 1-5（与 SuperNono 一致）
	superStage := n.SuperNono
	if superStage < 1 {
		superStage = 1
	} else if superStage > 5 {
		superStage = 5
	}
	writeU32(uint32(superStage))

	ctx.GameServer.SendResponse(ctx.ClientData, 9003, ctx.UserID, ctx.SeqID, buf)
}

// ==================== 任务 / 新手奖励 ====================

// 新手任务 ID 对齐 Lua NOVICE_TASK；其他任务 ID 见前端 MapProcess / TaskClass
const (
	taskGetCloth     = 85 // 领取服装
	taskSelectPet    = 86 // 选择精灵
	taskWinBattle    = 87 // 战斗胜利
	taskUseItem      = 88 // 使用道具
	taskBombDisposal = 9  // 赫尔卡星拆弹小游戏（MapProcess_30.as accept(9)），奖励电能锯子
)

// 新手三选一精灵映射，对齐 Lua NOVICE_PET_MAP / MapProcess_102.as
// 1 -> 布布种子 (1)
// 2 -> 小火猴   (7)
// 3 -> 伊优     (4)
var novicePetMap = map[uint32]int{
	1: 1,
	2: 7,
	3: 4,
}

// handleAcceptTask CMD 2201 接受任务
// 对齐 Lua: task_handlers.handleAcceptTask
func handleAcceptTask(ctx *gameserver.HandlerContext) {
	var taskID uint32
	if len(ctx.Body) >= 4 {
		taskID = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.Tasks == nil {
		user.Tasks = make(map[string]userdb.Task)
	}
	key := strconv.FormatUint(uint64(taskID), 10)
	t := user.Tasks[key]
	t.Status = "1" // 1 = 已接受
	user.Tasks[key] = t

	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, taskID)
	ctx.GameServer.SendResponse(ctx.ClientData, 2201, ctx.UserID, ctx.SeqID, buf)
}

// handleGetTaskBuf CMD 2203 获取任务进度（GET_TASK_BUF），地图装置/NPC 对话依赖此接口
// 请求: taskId(4)。响应: taskId(4) + flag(4) + buf(20 字节)，与前端 TaskBufInfo(taskId, flag, buf) 及 buf.position=i; readBoolean() 一致
func handleGetTaskBuf(ctx *gameserver.HandlerContext) {
	var taskID uint32
	if len(ctx.Body) >= 4 {
		taskID = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	flag := uint32(0)
	bufBytes := make([]byte, 20)
	if user.Tasks != nil {
		key := strconv.FormatUint(uint64(taskID), 10)
		if t, ok := user.Tasks[key]; ok && t.Buf != nil {
			for i := 0; i < 20; i++ {
				if v, ok := t.Buf[i]; ok {
					bufBytes[i] = byte(v & 0xff)
				}
			}
			flag = 1
		}
	}
	body := make([]byte, 4+4+20)
	binary.BigEndian.PutUint32(body[0:4], taskID)
	binary.BigEndian.PutUint32(body[4:8], flag)
	copy(body[8:28], bufBytes)
	ctx.GameServer.SendResponse(ctx.ClientData, 2203, ctx.UserID, ctx.SeqID, body)
}

// handleAcceptDailyTask CMD 2231 接受每日任务
// 请求: taskID(4)，响应: taskID(4)，与 2201 逻辑一致
func handleAcceptDailyTask(ctx *gameserver.HandlerContext) {
	var taskID uint32
	if len(ctx.Body) >= 4 {
		taskID = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.Tasks == nil {
		user.Tasks = make(map[string]userdb.Task)
	}
	key := strconv.FormatUint(uint64(taskID), 10)
	t := user.Tasks[key]
	t.Status = "1" // 已接受
	user.Tasks[key] = t
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, taskID)
	ctx.GameServer.SendResponse(ctx.ClientData, 2231, ctx.UserID, ctx.SeqID, buf)
}

// handleDeleteDailyTask CMD 2232 放弃每日任务
// 请求: taskID(4)，响应: taskID(4)
func handleDeleteDailyTask(ctx *gameserver.HandlerContext) {
	var taskID uint32
	if len(ctx.Body) >= 4 {
		taskID = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.Tasks != nil {
		key := strconv.FormatUint(uint64(taskID), 10)
		delete(user.Tasks, key)
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
	}
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, taskID)
	ctx.GameServer.SendResponse(ctx.ClientData, 2232, ctx.UserID, ctx.SeqID, buf)
}

// handleCompleteDailyTask CMD 2233 完成每日任务
// 请求: taskID(4) + param(4)，响应: NoviceFinishInfo（与 2202 相同），客户端据此 setTaskStatus(COMPLETE)
func handleCompleteDailyTask(ctx *gameserver.HandlerContext) {
	var taskID, param uint32
	if len(ctx.Body) >= 4 {
		taskID = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	if len(ctx.Body) >= 8 {
		param = binary.BigEndian.Uint32(ctx.Body[4:8])
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.Tasks == nil {
		user.Tasks = make(map[string]userdb.Task)
	}
	key := strconv.FormatUint(uint64(taskID), 10)
	t := user.Tasks[key]
	t.Status = "3" // 已完成
	user.Tasks[key] = t
	body := buildTaskCompleteResponse(taskID, param, user)
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 2233, ctx.UserID, ctx.SeqID, body)
}

// handleCompleteTask CMD 2202 完成任务（包含新手奖励）
// 对齐 Lua: task_handlers.handleCompleteTask / buildTaskCompleteResponse
func handleCompleteTask(ctx *gameserver.HandlerContext) {
	var taskID, param uint32
	if len(ctx.Body) >= 4 {
		taskID = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	if len(ctx.Body) >= 8 {
		param = binary.BigEndian.Uint32(ctx.Body[4:8])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.Tasks == nil {
		user.Tasks = make(map[string]userdb.Task)
	}
	key := strconv.FormatUint(uint64(taskID), 10)
	t := user.Tasks[key]
	t.Status = "3" // 3 = 已完成
	user.Tasks[key] = t

	// 构建任务完成响应（包含新手三选一精灵和物品奖励等）
	body := buildTaskCompleteResponse(taskID, param, user)

	// 保存数据（任务完成可能添加了精灵或物品）
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	ctx.GameServer.SendResponse(ctx.ClientData, 2202, ctx.UserID, ctx.SeqID, body)
}

// buildTaskCompleteResponse 构建任务完成响应
// NoviceFinishInfo: taskID(4) + petID(4) + captureTm(4) + itemCount(4) + [itemID(4) + itemCnt(4)]...
// 这里只精确实现新手三选一精灵，其他任务返回空奖励但保持结构正确。
func buildTaskCompleteResponse(taskID, param uint32, user *userdb.GameData) []byte {
	writeU32To := func(buf []byte, off int, v uint32) {
		binary.BigEndian.PutUint32(buf[off:off+4], v)
	}

	var petID uint32
	var captureTm uint32

	// 处理新手选宠任务
	if taskID == taskSelectPet {
		if mapped, ok := novicePetMap[param]; ok {
			petID = uint32(mapped)
		} else {
			petID = 1
		}
		captureTm = 0x69686700 + petID

		// 添加到 pets 列表（如果还没有该精灵）
		found := false
		for _, p := range user.Pets {
			if p.ID == int(petID) && p.CatchTime == int(captureTm) {
				found = true
				break
			}
		}
		if !found {
			// 随机性格（0-24）
			rand.Seed(time.Now().UnixNano())
			randomNature := rand.Intn(25)
			// 随机个体值（15-31，新手精灵稍微好一点）
			randomDV := 15 + rand.Intn(17) // 15-31

			newPet := userdb.Pet{
				ID:        int(petID),
				CatchTime: int(captureTm),
				Level:     5,
				DV:        randomDV,
				Nature:    randomNature,
				Exp:       0,
				Name:      "",
			}
			// 新手选宠不自动分配特性；后续可通过“特性开启芯片”等道具获得特性
			user.Pets = append(user.Pets, newPet)
			logger.Info(fmt.Sprintf("[2202] 新手选宠: PetID=%d DV=%d Nature=%d", petID, randomDV, randomNature))
		}
	}

	// 根据任务ID添加物品奖励
	itemCount := uint32(0)
	itemRewards := []struct {
		id    uint32
		count uint32
	}{}

	if user.Items == nil {
		user.Items = make(map[string]userdb.Item)
	}

	// 任务85（领取服装）给新手套装
	if taskID == 85 {
		noviceClothes := []struct {
			id    uint32
			count uint32
		}{
			{100027, 1}, // 新手服装1
			{100028, 1}, // 新手服装2
			{500001, 1}, // 新手家具
			{300650, 3}, // 新手道具1
			{300025, 3}, // 新手道具2
			{300035, 3}, // 新手道具3
			{500502, 1}, // 新手家具2
			{500503, 1}, // 新手家具3
		}

		for _, item := range noviceClothes {
			itemKey := strconv.FormatUint(uint64(item.id), 10)
			if existing, ok := user.Items[itemKey]; ok {
				existing.Count += int(item.count)
				user.Items[itemKey] = existing
			} else {
				user.Items[itemKey] = userdb.Item{Count: int(item.count), ExpireTime: 0x057E40}
			}
			itemRewards = append(itemRewards, item)
			itemCount++
			logger.Info(fmt.Sprintf("[2202] 任务完成奖励: 物品 %d x%d", item.id, item.count))
		}
	}

	// 任务87（战斗胜利）给一些物品奖励
	if taskID == taskWinBattle {
		// 添加治疗药水
		itemKey := "300001"
		if existing, ok := user.Items[itemKey]; ok {
			existing.Count += 5
			user.Items[itemKey] = existing
		} else {
			user.Items[itemKey] = userdb.Item{Count: 5, ExpireTime: 0x057E40}
		}
		itemRewards = append(itemRewards, struct {
			id    uint32
			count uint32
		}{300001, 5})
		itemCount++

		itemKey2 := "300011"
		if existing, ok := user.Items[itemKey2]; ok {
			existing.Count += 3
			user.Items[itemKey2] = existing
		} else {
			user.Items[itemKey2] = userdb.Item{Count: 3, ExpireTime: 0x057E40}
		}
		itemRewards = append(itemRewards, struct {
			id    uint32
			count uint32
		}{300011, 3})
		itemCount++
		logger.Info(fmt.Sprintf("[2202] 任务完成奖励: 物品 300001 x5, 300011 x3"))
	}

	// 任务88（使用道具）给金币奖励
	if taskID == taskUseItem {
		// 添加金币（特殊奖励类型1）
		itemRewards = append(itemRewards, struct {
			id    uint32
			count uint32
		}{1, 50000}) // 类型1=金币
		itemCount++

		// 添加经验（特殊奖励类型3）
		itemRewards = append(itemRewards, struct {
			id    uint32
			count uint32
		}{3, 250000}) // 类型3=经验
		itemCount++

		// 添加其他奖励（特殊奖励类型5）
		itemRewards = append(itemRewards, struct {
			id    uint32
			count uint32
		}{5, 20})
		itemCount++

		// 更新用户金币
		user.Coins += 50000
		logger.Info(fmt.Sprintf("[2202] 任务完成奖励: 金币 +50000, 经验 +250000, 其他 +20"))
	}

	// 任务 9：赫尔卡星拆弹小游戏完成，奖励电能锯子（100059）
	if taskID == taskBombDisposal {
		itemID := uint32(100059) // 电能锯子
		itemKey := strconv.FormatUint(uint64(itemID), 10)
		if existing, ok := user.Items[itemKey]; ok {
			existing.Count++
			user.Items[itemKey] = existing
		} else {
			user.Items[itemKey] = userdb.Item{Count: 1, ExpireTime: 0x057E40}
		}
		itemRewards = append(itemRewards, struct {
			id    uint32
			count uint32
		}{itemID, 1})
		itemCount++
		logger.Info(fmt.Sprintf("[2202] 任务完成奖励: 拆弹小游戏 电能锯子(100059) x1"))
	}

	// 无配置奖励时（如每日任务）给默认 2000 点积累经验，客户端 TaskClass 显示“你获得了 2000 点积累经验！”
	if itemCount == 0 && petID == 0 {
		defaultExp := uint32(2000)
		if user.ExpPool < 0 {
			user.ExpPool = 0
		}
		user.ExpPool += int(defaultExp)
		itemRewards = append(itemRewards, struct {
			id    uint32
			count uint32
		}{3, defaultExp}) // 类型 3 = 积累经验
		itemCount++
		logger.Info(fmt.Sprintf("[2202/2233] 任务完成默认奖励: 积累经验 +%d", defaultExp))
	}

	// 构建响应体
	bodySize := 16 + int(itemCount)*8 // taskID(4) + petID(4) + captureTm(4) + itemCount(4) + [itemID(4) + itemCnt(4)]...
	body := make([]byte, bodySize)
	writeU32To(body, 0, taskID)
	writeU32To(body, 4, petID)
	writeU32To(body, 8, captureTm)
	writeU32To(body, 12, itemCount)

	off := 16
	for _, item := range itemRewards {
		writeU32To(body, off, item.id)
		off += 4
		writeU32To(body, off, item.count)
		off += 4
	}

	return body
}

// handlePetFusion CMD 2351 精灵融合
// 请求: catchTime1(4) + catchTime2(4) + itemID(4)*4（4 个相同融合胶囊物品 ID）
// 响应: PetFusionInfo = obtainTime(4) + soulID(4) + starterCpTm(4) + costItemFlag(4)
func handlePetFusion(ctx *gameserver.HandlerContext) {
	const (
		minBodyLen   = 24 // 2 catchTime + 4 itemID
		requiredItem = 4
	)
	body := ctx.Body
	if len(body) < minBodyLen {
		logger.Warning("[2351] 精灵融合: 包体不足")
		ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, make([]byte, 16))
		return
	}
	ct1 := binary.BigEndian.Uint32(body[0:4])
	ct2 := binary.BigEndian.Uint32(body[4:8])
	itemID := binary.BigEndian.Uint32(body[8:12])
	// 校验 4 个物品 ID 相同
	for i := 1; i < 4; i++ {
		if binary.BigEndian.Uint32(body[8+i*4:12+i*4]) != itemID {
			logger.Warning("[2351] 精灵融合: 物品 ID 不一致")
			ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, make([]byte, 16))
			return
		}
	}
	// 有效融合胶囊：藤结晶 400009、黄晶矿 400001、甲烷 400002
	validItems := map[uint32]bool{400001: true, 400002: true, 400009: true}
	if !validItems[itemID] {
		logger.Warning(fmt.Sprintf("[2351] 精灵融合: 无效物品 ID %d", itemID))
		ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, make([]byte, 16))
		return
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	var p1, p2 *userdb.Pet
	var idx1, idx2 int
	for i, p := range user.Pets {
		if uint32(p.CatchTime) == ct1 {
			p1 = &user.Pets[i]
			idx1 = i
		}
		if uint32(p.CatchTime) == ct2 {
			p2 = &user.Pets[i]
			idx2 = i
		}
	}
	if p1 == nil || p2 == nil {
		logger.Warning("[2351] 精灵融合: 精灵不存在")
		ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, make([]byte, 16))
		return
	}
	if p1.CatchTime == p2.CatchTime {
		logger.Warning("[2351] 精灵融合: 不能选择同一只精灵")
		ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, make([]byte, 16))
		return
	}

	petMgr := gamepets.GetInstance()
	t1 := petMgr.Get(p1.ID)
	t2 := petMgr.Get(p2.ID)
	if t1 == nil || t2 == nil {
		logger.Warning("[2351] 精灵融合: 精灵配置不存在")
		ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, make([]byte, 16))
		return
	}

	// 自定义融合规则：精灵 A + 精灵 B → 指定元神珠，跳过 PetClass/主副校验，100% 成功
	if customSoulPearlID, hasRule := GetFusionRule(p1.ID, p2.ID); hasRule && customSoulPearlID > 0 {
		petClassForBead, hasPC := GetPetClassBySoulPearlItemID(customSoulPearlID)
		if !hasPC || petClassForBead <= 0 {
			logger.Warning(fmt.Sprintf("[2351] 精灵融合: 自定义规则元神珠 %d 无对应 PetClass", customSoulPearlID))
			ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, make([]byte, 16))
			return
		}
		itemKey := strconv.FormatUint(uint64(itemID), 10)
		it, ok := user.Items[itemKey]
		if !ok || it.Count < requiredItem {
			logger.Warning(fmt.Sprintf("[2351] 精灵融合: 物品不足 需要%d", requiredItem))
			ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, make([]byte, 16))
			return
		}
		it.Count -= requiredItem
		if it.Count <= 0 {
			delete(user.Items, itemKey)
		} else {
			user.Items[itemKey] = it
		}
		obtainTime := uint32(time.Now().Unix() & 0xFFFFFFFF)
		if obtainTime == 0 {
			obtainTime = 1
		}
		for _, sb := range user.SoulBeads {
			if sb.ObtainTime == obtainTime {
				obtainTime++
				break
			}
		}
		if idx1 > idx2 {
			user.Pets = append(user.Pets[:idx1], user.Pets[idx1+1:]...)
			user.Pets = append(user.Pets[:idx2], user.Pets[idx2+1:]...)
		} else {
			user.Pets = append(user.Pets[:idx2], user.Pets[idx2+1:]...)
			user.Pets = append(user.Pets[:idx1], user.Pets[idx1+1:]...)
		}
		if user.SoulBeads == nil {
			user.SoulBeads = []userdb.SoulBead{}
		}
		user.SoulBeads = append(user.SoulBeads, userdb.SoulBead{
			ObtainTime: obtainTime,
			ItemID:     uint32(petClassForBead),
		})
		// 发放元神珠物品到背包（与原始融合分支一致，确保 Items 非 nil）
		if user.Items == nil {
			user.Items = make(map[string]userdb.Item)
		}
		spKey := strconv.Itoa(customSoulPearlID)
		if ex, has := user.Items[spKey]; has {
			ex.Count++
			user.Items[spKey] = ex
		} else {
			user.Items[spKey] = userdb.Item{Count: 1, ExpireTime: 0x057E40}
		}
		logger.Info(fmt.Sprintf("[2351] 精灵融合(自定义): 已发放元神珠物品 %d x1", customSoulPearlID))
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		resp := make([]byte, 16)
		binary.BigEndian.PutUint32(resp[0:4], obtainTime)
		binary.BigEndian.PutUint32(resp[4:8], uint32(petClassForBead))
		binary.BigEndian.PutUint32(resp[8:12], ct1)
		binary.BigEndian.PutUint32(resp[12:16], 1)
		ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, resp)
		logger.Info(fmt.Sprintf("[2351] 精灵融合(自定义): PetID=%d+%d → 元神珠%d obtainTime=%d", p1.ID, p2.ID, customSoulPearlID, obtainTime))
		return
	}

	if t1.PetClass != t2.PetClass {
		logger.Warning(fmt.Sprintf("[2351] 精灵融合: PetClass 不同 %d vs %d", t1.PetClass, t2.PetClass))
		ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, make([]byte, 16))
		return
	}
	// 需要一主一副：至少一个 FuseMaster=1，至少一个 FuseSub=1
	masterOK := (t1.FuseMaster == 1) || (t2.FuseMaster == 1)
	subOK := (t1.FuseSub == 1) || (t2.FuseSub == 1)
	if !masterOK || !subOK {
		logger.Warning("[2351] 精灵融合: 精灵未达到融合要求")
		ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, make([]byte, 16))
		return
	}

	itemKey := strconv.FormatUint(uint64(itemID), 10)
	it, ok := user.Items[itemKey]
	if !ok || it.Count < requiredItem {
		logger.Warning(fmt.Sprintf("[2351] 精灵融合: 物品不足 需要%d", requiredItem))
		ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, make([]byte, 16))
		return
	}

	// 无论成功还是失败都扣除 4 个融合材料
	it.Count -= requiredItem
	if it.Count <= 0 {
		delete(user.Items, itemKey)
	} else {
		user.Items[itemKey] = it
	}

	// 融合成功率判定（GM 权重管理可配置，按精元 PetClass 分别配置）
	fusionRate := GetFusionSuccessRate(t1.PetClass)
	if fusionRate < 1.0 && rand.Float64() >= fusionRate {
		// 融合失败：扣除副宠等级 5 级（最低 1 级）
		var subPet *userdb.Pet
		if t1.FuseSub == 1 {
			subPet = p1
		} else {
			subPet = p2
		}
		subPet.Level -= 5
		if subPet.Level < 1 {
			subPet.Level = 1
		}
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		logger.Info(fmt.Sprintf("[2351] 精灵融合: 概率未中 rate=%.2f，副宠等级降5级 当前=%d", fusionRate, subPet.Level))
		ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, make([]byte, 16))
		return
	}

	// 融合成功：移除两只精灵，创建元神珠，并发放对应元神珠物品到背包
	obtainTime := uint32(time.Now().Unix() & 0xFFFFFFFF)
	if obtainTime == 0 {
		obtainTime = 1
	}
	for _, sb := range user.SoulBeads {
		if sb.ObtainTime == obtainTime {
			obtainTime++
			break
		}
	}

	if idx1 > idx2 {
		user.Pets = append(user.Pets[:idx1], user.Pets[idx1+1:]...)
		user.Pets = append(user.Pets[:idx2], user.Pets[idx2+1:]...)
	} else {
		user.Pets = append(user.Pets[:idx2], user.Pets[idx2+1:]...)
		user.Pets = append(user.Pets[:idx1], user.Pets[idx1+1:]...)
	}

	if user.SoulBeads == nil {
		user.SoulBeads = []userdb.SoulBead{}
	}
	user.SoulBeads = append(user.SoulBeads, userdb.SoulBead{
		ObtainTime: obtainTime,
		ItemID:     uint32(t1.PetClass),
	})

	// 融合成功给予对应元神珠物品到背包
	if soulPearlItemID, ok := GetSoulPearlItemIDByPetClass(t1.PetClass); ok && soulPearlItemID > 0 {
		if user.Items == nil {
			user.Items = make(map[string]userdb.Item)
		}
		spKey := strconv.Itoa(soulPearlItemID)
		if ex, has := user.Items[spKey]; has {
			ex.Count++
			user.Items[spKey] = ex
		} else {
			user.Items[spKey] = userdb.Item{Count: 1, ExpireTime: 0x057E40}
		}
		logger.Info(fmt.Sprintf("[2351] 精灵融合: 已发放元神珠物品 %d x1", soulPearlItemID))
	}

	ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	resp := make([]byte, 16)
	binary.BigEndian.PutUint32(resp[0:4], obtainTime)
	binary.BigEndian.PutUint32(resp[4:8], uint32(t1.PetClass))
	binary.BigEndian.PutUint32(resp[8:12], ct1)
	binary.BigEndian.PutUint32(resp[12:16], 1) // costItemFlag=1 已消耗物品
	ctx.GameServer.SendResponse(ctx.ClientData, 2351, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2351] 精灵融合: PetID=%d+%d PetClass=%d 获得元神珠 obtainTime=%d", p1.ID, p2.ID, t1.PetClass, obtainTime))
}

// handleGetSoulBeadList CMD 2354 获取魂珠列表
// 响应: count(4) + [obtainTime(4)+itemID(4)]*count
// 客户端用 itemID 查找 resource/soulBead/icon/{itemID}.swf，元神珠图标对应物品 ID 1000001-1000022，
// 故发送元神珠物品 ID 而非 PetClass，以便精灵元神珠栏正确显示
func handleGetSoulBeadList(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	beads := user.SoulBeads
	if beads == nil {
		beads = []userdb.SoulBead{}
	}
	body := make([]byte, 4+len(beads)*8)
	binary.BigEndian.PutUint32(body[0:4], uint32(len(beads)))
	for i, b := range beads {
		off := 4 + i*8
		binary.BigEndian.PutUint32(body[off:off+4], b.ObtainTime)
		displayID := b.ItemID // 默认用 PetClass
		if soulPearlID, ok := GetSoulPearlItemIDByPetClass(int(b.ItemID)); ok && soulPearlID > 0 {
			displayID = uint32(soulPearlID) // 转为元神珠物品 ID 供客户端正确显示图标
		}
		binary.BigEndian.PutUint32(body[off+4:off+8], displayID)
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 2354, ctx.UserID, ctx.SeqID, body)
}

// handleGetSoulBeadBuf CMD 2352 获取魂珠能量吸收进度（元神赋形用）
// 请求: obtainTime(4)
// 响应: obtainTm(4) + buf(20 字节，每字节 1 表示该步已完成)。客户端据此判断是否可赋形。
// 返回该元神珠已保存的进度；无记录则返回 20 步全 0，需到对应地区吸取能量后方可赋形。
func handleGetSoulBeadBuf(ctx *gameserver.HandlerContext) {
	var obtainTime uint32
	if len(ctx.Body) >= 4 {
		obtainTime = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	body := make([]byte, 4+20)
	binary.BigEndian.PutUint32(body[0:4], obtainTime)
	for i := 0; i < 20; i++ {
		body[4+i] = 0 // 默认未吸收
	}
	if user.SoulBeadBufs != nil {
		for _, b := range user.SoulBeadBufs {
			if b.ObtainTime == obtainTime && len(b.Buf) >= 20 {
				copy(body[4:24], b.Buf[:20])
				break
			}
		}
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 2352, ctx.UserID, ctx.SeqID, body)
}

// handleSetSoulBeadBuf CMD 2353 设置魂珠能量吸收进度（玩家在对应地区吸取后客户端上报）
// 请求: obtainTime(4) + buf(20)
// 响应: 4 字节 0
func handleSetSoulBeadBuf(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 24 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2353, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}
	obtainTime := binary.BigEndian.Uint32(ctx.Body[0:4])
	buf := make([]byte, 20)
	copy(buf, ctx.Body[4:24])
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.SoulBeadBufs == nil {
		user.SoulBeadBufs = []userdb.SoulBeadBuf{}
	}
	found := false
	for i := range user.SoulBeadBufs {
		if user.SoulBeadBufs[i].ObtainTime == obtainTime {
			user.SoulBeadBufs[i].Buf = buf
			found = true
			break
		}
	}
	if !found {
		user.SoulBeadBufs = append(user.SoulBeadBufs, userdb.SoulBeadBuf{ObtainTime: obtainTime, Buf: buf})
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 2353, ctx.UserID, ctx.SeqID, make([]byte, 4))
}

// 元神珠赋形/孵化错误码（与 ParseSocketError 一致）
const (
	soulBeadErrNotExist    = 103547 // 你的元神珠不存在
	soulBeadErrAlreadyTransform = 13034 // 你已经有一个元神珠正在赋形噢
)

// handleGetSoulBeadStatus CMD 2356 获取元神珠赋形状态（剩余孵化时间）
// 请求: 无或 4 字节
// 响应: obtainTm(4) + soulBeadItemID(4) + remainingSec(4)，共 12 字节。无赋中则全 0。remainingSec 为剩余秒数（与 GM 转化时间单位一致），客户端可直接用于显示
func handleGetSoulBeadStatus(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	body := make([]byte, 12)
	if user.SoulBeadTransform != nil {
		remainingSec := int64(0)
		if user.SoulBeadTransform.ExpireTime > time.Now().Unix() {
			remainingSec = user.SoulBeadTransform.ExpireTime - time.Now().Unix()
		}
		binary.BigEndian.PutUint32(body[0:4], user.SoulBeadTransform.ObtainTime)
		binary.BigEndian.PutUint32(body[4:8], user.SoulBeadTransform.ItemID)
		binary.BigEndian.PutUint32(body[8:12], uint32(remainingSec))
		logger.Info(fmt.Sprintf("[2356] 返回赋形状态: obtainTm=%d soulBeadItemID=%d 剩余秒数=%d", user.SoulBeadTransform.ObtainTime, user.SoulBeadTransform.ItemID, remainingSec))
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 2356, ctx.UserID, ctx.SeqID, body)
}

// handlePetHatchPutIn CMD 2315 分子转化仪：放入精元孵化 (PET_HATCH)
// 请求: itemID(4) 精元物品ID 如 400110
// 响应: 4 字节 0 成功
func handlePetHatchPutIn(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 4 {
		resp := make([]byte, 4)
		binary.BigEndian.PutUint32(resp, 1) // 非 0 表示失败
		ctx.GameServer.SendResponse(ctx.ClientData, 2315, ctx.UserID, ctx.SeqID, resp)
		return
	}
	itemID := int(binary.BigEndian.Uint32(ctx.Body[0:4]))
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	resp := make([]byte, 4)
	breedMonID, ok := GetBreedMonIDByItemID(itemID)
	if !ok || breedMonID <= 0 {
		logger.Info(fmt.Sprintf("[2315] 无效精元 itemID=%d", itemID))
		ctx.GameServer.SendResponse(ctx.ClientData, 2315, ctx.UserID, ctx.SeqID, resp)
		return
	}
	if user.SoulBeadTransform != nil {
		logger.Info(fmt.Sprintf("[2315] 已有孵化中 UID=%d", ctx.UserID))
		ctx.GameServer.SendResponse(ctx.ClientData, 2315, ctx.UserID, ctx.SeqID, resp)
		return
	}
	itemKey := strconv.Itoa(itemID)
	if user.Items == nil {
		user.Items = make(map[string]userdb.Item)
	}
	item, has := user.Items[itemKey]
	if !has || item.Count <= 0 {
		logger.Info(fmt.Sprintf("[2315] 背包无精元 itemID=%d UID=%d", itemID, ctx.UserID))
		ctx.GameServer.SendResponse(ctx.ClientData, 2315, ctx.UserID, ctx.SeqID, resp)
		return
	}
	item.Count--
	if item.Count <= 0 {
		delete(user.Items, itemKey)
	} else {
		user.Items[itemKey] = item
	}
	isVip := user.Nono.SuperNono > 0
	incubationSec := GetSoulPearlTransmuteTime(itemID, isVip)
	if incubationSec <= 0 {
		incubationSec = DefaultTransmuteTm
	}
	expireTime := time.Now().Unix() + int64(incubationSec)
	obtainTime := uint32(time.Now().Unix())
	if obtainTime == 0 {
		obtainTime = 1
	}
	user.SoulBeadTransform = &userdb.SoulBeadTransformState{
		ObtainTime:     obtainTime,
		ItemID:         uint32(breedMonID),
		RewardPetClass: uint32(breedMonID),
		ExpireTime:     expireTime,
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	logger.Info(fmt.Sprintf("[2315] 精元放入: UID=%d itemID=%d BreedMonID=%d 孵化秒数=%d", ctx.UserID, itemID, breedMonID, incubationSec))
	ctx.GameServer.SendResponse(ctx.ClientData, 2315, ctx.UserID, ctx.SeqID, resp)
}

// handleNonoMolecularTransform CMD 2316 NONO分子转化仪 (PET_HATCH_GET)
// 前端 App_700002.as 解析: falg=readUnsignedInt(), leftTime=readUnsignedInt(), petID=readUnsignedInt(), captmTime=readUnsignedInt()
// falg=0 打开面板；falg=1 有孵化中。响应必须 16 字节。
func handleNonoMolecularTransform(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	body := make([]byte, 16)
	// 默认 falg=0 表示无孵化，客户端会打开 MoleculePanel
	binary.BigEndian.PutUint32(body[0:4], 0)  // falg
	binary.BigEndian.PutUint32(body[4:8], 0)  // leftTime
	binary.BigEndian.PutUint32(body[8:12], 0) // petID
	binary.BigEndian.PutUint32(body[12:16], 0) // captmTime
	action := uint32(0)
	itemID := 0
	if len(ctx.Body) >= 4 {
		action = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	if len(ctx.Body) >= 8 {
		itemID = int(binary.BigEndian.Uint32(ctx.Body[4:8]))
	}
	logger.Info(fmt.Sprintf("[2316] 请求 bodyLen=%d action=%d itemID=%d", len(ctx.Body), action, itemID))
	// 放入精元：action=1 且 itemID 为有效精元(400xxx)
	if len(ctx.Body) >= 8 && action == 1 {
		breedMonID, ok := GetBreedMonIDByItemID(itemID)
		if !ok || breedMonID <= 0 {
			ctx.GameServer.SendResponse(ctx.ClientData, 2316, ctx.UserID, ctx.SeqID, body)
			return
		}
		if user.SoulBeadTransform != nil {
			ctx.GameServer.SendResponse(ctx.ClientData, 2316, ctx.UserID, ctx.SeqID, body)
			return
		}
		itemKey := strconv.Itoa(itemID)
		if user.Items == nil {
			user.Items = make(map[string]userdb.Item)
		}
		item, has := user.Items[itemKey]
		if !has || item.Count <= 0 {
			ctx.GameServer.SendResponse(ctx.ClientData, 2316, ctx.UserID, ctx.SeqID, body)
			return
		}
		item.Count--
		if item.Count <= 0 {
			delete(user.Items, itemKey)
		} else {
			user.Items[itemKey] = item
		}
		isVip := user.Nono.SuperNono > 0
		incubationSec := GetSoulPearlTransmuteTime(itemID, isVip)
		if incubationSec <= 0 {
			incubationSec = DefaultTransmuteTm
		}
		expireTime := time.Now().Unix() + int64(incubationSec)
		obtainTime := uint32(time.Now().Unix())
		if obtainTime == 0 {
			obtainTime = 1
		}
		user.SoulBeadTransform = &userdb.SoulBeadTransformState{
			ObtainTime:     obtainTime,
			ItemID:         uint32(breedMonID),
			RewardPetClass: uint32(breedMonID),
			ExpireTime:     expireTime,
		}
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
		logger.Info(fmt.Sprintf("[2316] 精元放入: UID=%d itemID=%d BreedMonID=%d 孵化秒数=%d", ctx.UserID, itemID, breedMonID, incubationSec))
	}
	if user.SoulBeadTransform != nil {
		remainingSec := int64(0)
		if user.SoulBeadTransform.ExpireTime > time.Now().Unix() {
			remainingSec = user.SoulBeadTransform.ExpireTime - time.Now().Unix()
		}
		binary.BigEndian.PutUint32(body[0:4], 1)                                    // falg=1 有孵化中
		binary.BigEndian.PutUint32(body[4:8], uint32(remainingSec))                  // leftTime
		binary.BigEndian.PutUint32(body[8:12], user.SoulBeadTransform.ItemID)        // petID
		binary.BigEndian.PutUint32(body[12:16], user.SoulBeadTransform.ObtainTime)   // captmTime
		logger.Info(fmt.Sprintf("[2316] 有孵化: leftTime=%d petID=%d captmTime=%d", remainingSec, user.SoulBeadTransform.ItemID, user.SoulBeadTransform.ObtainTime))
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 2316, ctx.UserID, ctx.SeqID, body)
}

// handleTransformSoulBead CMD 2357 元神赋形（将元神珠放入转化仪）
// 请求: obtainTime(4)
// 响应: errorCode(4)，0 成功；103547 元神珠不存在，13034 已有元神珠正在赋形
func handleTransformSoulBead(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 4 {
		resp := make([]byte, 4)
		binary.BigEndian.PutUint32(resp, soulBeadErrNotExist)
		ctx.GameServer.SendResponse(ctx.ClientData, 2357, ctx.UserID, ctx.SeqID, resp)
		return
	}
	obtainTime := binary.BigEndian.Uint32(ctx.Body[0:4])
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	if user.SoulBeadTransform != nil {
		resp := make([]byte, 4)
		binary.BigEndian.PutUint32(resp, soulBeadErrAlreadyTransform)
		ctx.GameServer.SendResponse(ctx.ClientData, 2357, ctx.UserID, ctx.SeqID, resp)
		return
	}

	var bead *userdb.SoulBead
	for i := range user.SoulBeads {
		if user.SoulBeads[i].ObtainTime == obtainTime {
			bead = &user.SoulBeads[i]
			break
		}
	}
	if bead == nil {
		resp := make([]byte, 4)
		binary.BigEndian.PutUint32(resp, soulBeadErrNotExist)
		ctx.GameServer.SendResponse(ctx.ClientData, 2357, ctx.UserID, ctx.SeqID, resp)
		return
	}

	// 不强制要求能量吸满即可赋形，有该元神珠且无其他赋中即可

	// 从元神珠列表移除，并扣减背包中对应元神珠物品
	newBeads := make([]userdb.SoulBead, 0, len(user.SoulBeads))
	for _, b := range user.SoulBeads {
		if b.ObtainTime != obtainTime {
			newBeads = append(newBeads, b)
		}
	}
	user.SoulBeads = newBeads
	if user.Items != nil {
		if soulPearlItemID, ok := GetSoulPearlItemIDByPetClass(int(bead.ItemID)); ok && soulPearlItemID > 0 {
			spKey := strconv.Itoa(soulPearlItemID)
			if ex, has := user.Items[spKey]; has && ex.Count > 0 {
				ex.Count--
				if ex.Count <= 0 {
					delete(user.Items, spKey)
				} else {
					user.Items[spKey] = ex
				}
			}
		}
	}
	// 使用 GM 权重配置的孵化时间（按元神珠物品 ID，与后台权重管理一致）；未映射到的元神珠用默认 1800/900 秒
	soulPearlItemID, ok := GetSoulPearlItemIDByPetClass(int(bead.ItemID))
	isVip := user.Nono.SuperNono > 0
	var incubationSec int
	if ok && soulPearlItemID > 0 {
		incubationSec = GetSoulPearlTransmuteTime(soulPearlItemID, isVip)
	} else {
		if isVip {
			incubationSec = DefaultVipTransmuteTm
		} else {
			incubationSec = DefaultTransmuteTm
		}
	}
	if incubationSec <= 0 {
		incubationSec = DefaultTransmuteTm // 兜底 1800 秒
	}
	expireTime := time.Now().Unix() + int64(incubationSec)
	rewardPetClass := uint32(0)
	if soulPearlItemID, ok := GetSoulPearlItemIDByPetClass(int(bead.ItemID)); ok && soulPearlItemID > 0 {
		if rp, ok2 := GetSoulPearlRewardPetClass(soulPearlItemID); ok2 && rp > 0 {
			rewardPetClass = uint32(rp)
		}
	}
	if rewardPetClass == 0 {
		rewardPetClass = bead.ItemID
	}
	user.SoulBeadTransform = &userdb.SoulBeadTransformState{
		ObtainTime:     obtainTime,
		ItemID:         bead.ItemID,
		RewardPetClass: rewardPetClass,
		ExpireTime:     expireTime,
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	// 响应 8 字节：status(4) + rewardPetClass(4)。与 2358 发放的精灵一致，客户端用于孵化/完成动画的精灵 swf
	resp := make([]byte, 8)
	binary.BigEndian.PutUint32(resp[0:4], 0)
	binary.BigEndian.PutUint32(resp[4:8], rewardPetClass)
	ctx.GameServer.SendResponse(ctx.ClientData, 2357, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2357] 元神赋形: UID=%d obtainTime=%d PetClass=%d rewardPetClass=%d 孵化秒数=%d 完成时间=%d", ctx.UserID, obtainTime, bead.ItemID, rewardPetClass, incubationSec, expireTime))
}

// handleSoulBeadToPet CMD 2358 元神珠孵化完成，领取精灵
// 请求: obtainTime(4)
// 响应: success(4) + petId(4) + catchTime(4)。success=0 表示失败（未到时间/不存在等）
func handleSoulBeadToPet(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 4 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2358, ctx.UserID, ctx.SeqID, make([]byte, 12))
		return
	}
	obtainTime := binary.BigEndian.Uint32(ctx.Body[0:4])
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	if user.SoulBeadTransform == nil || user.SoulBeadTransform.ObtainTime != obtainTime {
		body := make([]byte, 12) // success=0, petId=0, catchTime=0
		ctx.GameServer.SendResponse(ctx.ClientData, 2358, ctx.UserID, ctx.SeqID, body)
		return
	}
	if time.Now().Unix() < user.SoulBeadTransform.ExpireTime {
		body := make([]byte, 12) // success=0, 未到时间
		ctx.GameServer.SendResponse(ctx.ClientData, 2358, ctx.UserID, ctx.SeqID, body)
		return
	}

	// 2357 时已确定奖励精灵并写入 RewardPetClass，保证与客户端动画一致；未设置时回退到按权重随机
	petClass := int(user.SoulBeadTransform.RewardPetClass)
	if petClass <= 0 {
		petClass = int(user.SoulBeadTransform.ItemID)
		if soulPearlItemID, ok := GetSoulPearlItemIDByPetClass(petClass); ok && soulPearlItemID > 0 {
			if rewardPetClass, ok2 := GetSoulPearlRewardPetClass(soulPearlItemID); ok2 && rewardPetClass > 0 {
				petClass = rewardPetClass
			}
		}
	}
	newCatchTime := int(time.Now().Unix())
	if newCatchTime == 0 {
		newCatchTime = 1
	}
	newPet := userdb.Pet{
		ID:        petClass,
		CatchTime: newCatchTime,
		Level:     1,
		DV:        0,
		Nature:    0,
		Exp:       0,
		Name:      "",
	}
	userdb.AssignFusionTraitIfNeeded(&newPet)
	user.SoulBeadTransform = nil
	if len(user.Pets) >= 6 {
		if user.StoragePets == nil {
			user.StoragePets = []userdb.Pet{}
		}
		user.StoragePets = append(user.StoragePets, newPet)
	} else {
		user.Pets = append(user.Pets, newPet)
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	body := make([]byte, 12)
	binary.BigEndian.PutUint32(body[0:4], 1) // success
	binary.BigEndian.PutUint32(body[4:8], uint32(petClass))
	binary.BigEndian.PutUint32(body[8:12], uint32(newCatchTime))
	ctx.GameServer.SendResponse(ctx.ClientData, 2358, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2358] 元神孵化: UID=%d obtainTime=%d -> PetID=%d CatchTime=%d Trait=%d", ctx.UserID, obtainTime, petClass, newCatchTime, newPet.Trait))
	// 登记客户端 IP -> 奖励精灵 ID，供资源服 /resource/pet/swf/1.swf 按 IP 返回对应赋形动画 swf
	if ctx.ClientData != nil && ctx.ClientData.Socket != nil {
		addr := ctx.ClientData.Socket.RemoteAddr().String()
		clientIP, _, err := net.SplitHostPort(addr)
		if err != nil || clientIP == "" {
			clientIP = addr
		}
		soultransformcache.Register(clientIP, petClass)
	}
	// 推送精灵列表与完整信息，使客户端不重登即可在背包/仓库看到新精灵
	if user.StoragePets == nil {
		user.StoragePets = []userdb.Pet{}
	}
	storageCount := uint32(len(user.StoragePets))
	body2303 := make([]byte, 4+int(storageCount)*12)
	binary.BigEndian.PutUint32(body2303[0:4], storageCount)
	off := 4
	for _, p := range user.StoragePets {
		binary.BigEndian.PutUint32(body2303[off:off+4], uint32(p.ID))
		off += 4
		binary.BigEndian.PutUint32(body2303[off:off+4], uint32(p.CatchTime))
		off += 4
		binary.BigEndian.PutUint32(body2303[off:off+4], 0)
		off += 4
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 2303, ctx.UserID, ctx.SeqID, body2303[:off])
	fullPetBody := buildFullPetInfo(newPet)
	ctx.GameServer.SendResponse(ctx.ClientData, 2301, ctx.UserID, ctx.SeqID, fullPetBody)
	// 再推送一次 2301（seq=0）作为服务端主动推送，便于客户端识别并刷新背包/仓库
	ctx.GameServer.SendResponse(ctx.ClientData, 2301, ctx.UserID, 0, fullPetBody)
	// 使用已有的 8004 奖励提示窗口弹出“获得精灵”信息框：bonusID=0, petID=petClass, captureTm=newCatchTime, 无道具
	body8004 := buildBossMonster8004Body(0, uint32(petClass), uint32(newCatchTime), 0, 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 8004, ctx.UserID, 0, body8004)
}

// ==================== 物品 / 背包 ====================

// buildItemList2605Body 根据 2605/4475 请求体构建物品列表响应体，供 CMD 2605 与 CMD 4475 共用
// 请求 body 前 12 字节: a(4)+b(4)+c(4)；返回 itemCount(4) + [itemId(4)+count(4)+expireTime(4)+flag(4)]...
// 储存箱子分栏：客户端可能用每条第4个uint32(flag)筛选显示，装备=0、收藏=1、超能NONO=2，否则收藏/超能NONO栏不显示
func buildItemList2605Body(user *userdb.GameData, reqBody []byte) []byte {
	var a, b, c uint32
	if len(reqBody) >= 12 {
		a = binary.BigEndian.Uint32(reqBody[0:4])
		b = binary.BigEndian.Uint32(reqBody[4:8])
		c = binary.BigEndian.Uint32(reqBody[8:12])
	}
	if a != 0 && b == 0 && c == 0 {
		a, b, c = 0, 0, 0
	}
	// 根据请求范围决定每条 item 的 flag，供客户端在储存箱子各栏筛选（可能为 1-based 栏位索引：装备=1 收藏=2 超能NONO=3）
	var itemFlag uint32
	if a == 300001 && b == 500000 {
		itemFlag = 2 // 收藏物品栏（第2栏）
	} else if a == 100001 && b == 500000 {
		itemFlag = 3 // 超能NONO栏（第3栏）
	}
	// 其余(装备 100001-101000、1300001-1400000、全量等)保持 0，若客户端用 1 表示装备则此处可改为 1

	type itemRow struct {
		id         uint32
		count      uint32
		expireTime uint32
	}
	rows := make([]itemRow, 0, len(user.Items))
	inRange := func(id uint32) bool {
		if a == 0 && b == 0 && (c == 0 || c < 100000) {
			return true
		}
		if c >= 100000 && id == c {
			return true
		}
		if a <= b && id >= a && id <= b {
			return true
		}
		if a == 1300001 && b == 1400000 {
			if id >= 100001 && id <= 101000 {
				return true
			}
		}
		return false
	}
	for k, it := range user.Items {
		id64, err := strconv.ParseUint(k, 10, 32)
		if err != nil {
			continue
		}
		id := uint32(id64)
		if !inRange(id) {
			continue
		}
		exp := uint32(it.ExpireTime)
		if exp == 0 {
			exp = 0x057E40
		}
		rows = append(rows, itemRow{id: id, count: uint32(it.Count), expireTime: exp})
	}
	body := make([]byte, 4+len(rows)*16)
	binary.BigEndian.PutUint32(body[0:4], uint32(len(rows)))
	off := 4
	for _, r := range rows {
		binary.BigEndian.PutUint32(body[off:off+4], r.id)
		binary.BigEndian.PutUint32(body[off+4:off+8], r.count)
		binary.BigEndian.PutUint32(body[off+8:off+12], r.expireTime)
		binary.BigEndian.PutUint32(body[off+12:off+16], itemFlag)
		off += 16
	}
	return body
}

// handleItemList CMD 2605 物品列表（战斗中/非战斗均可请求）
// 对齐 Lua: item_handlers.handleItemList
// 请求: itemType1(4)+itemType2(4)+itemType3(4)；不足 12 字节或 (a,0,0) 时按“全部”返回
// 响应: itemCount(4) + [itemId(4)+count(4)+expireTime(4)+unknown(4)]...
func handleItemList(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	body := buildItemList2605Body(user, ctx.Body)
	var a, b, c uint32
	if len(ctx.Body) >= 12 {
		a, b, c = binary.BigEndian.Uint32(ctx.Body[0:4]), binary.BigEndian.Uint32(ctx.Body[4:8]), binary.BigEndian.Uint32(ctx.Body[8:12])
	}
	logger.Info(fmt.Sprintf("[2605] 物品列表请求: 范围 %d-%d, %d (用户总物品数: %d)", a, b, c, len(user.Items)))
	logger.Info(fmt.Sprintf("[2605] 返回物品列表: 数量=%d BodyLen=%d", binary.BigEndian.Uint32(body[0:4]), len(body)))
	ctx.GameServer.SendResponse(ctx.ClientData, 2605, ctx.UserID, ctx.SeqID, body)
}

// ==================== 精灵互转 / 展示（2304） ====================

// handlePetRelease CMD 2304 释放/背包仓库互转
// 对齐 Lua: pet_handlers.handlePetRelease（返回 PetTakeOutInfo）
// 请求: catchId(4) + flag(4) - flag=1: 仓库->背包, flag=0: 背包->仓库
// 响应: homeEnergy(4) + firstPetTime(4) + flag(4) + [PetInfo]?
func handlePetRelease(ctx *gameserver.HandlerContext) {
	var catchID uint32
	var reqFlag uint32
	if len(ctx.Body) >= 8 {
		catchID = binary.BigEndian.Uint32(ctx.Body[0:4])
		reqFlag = binary.BigEndian.Uint32(ctx.Body[4:8])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// 确保StoragePets不为nil
	if user.StoragePets == nil {
		user.StoragePets = []userdb.Pet{}
	}

	var picked *userdb.Pet
	var pickedIndex int = -1
	var respFlag uint32 = 0

	if reqFlag == 1 {
		// 仓库 -> 背包：先按 CatchTime 匹配，未命中时兼容客户端传 Pet ID 的情况（仅当仓库中该 ID 唯一时）
		for i := range user.StoragePets {
			if uint32(user.StoragePets[i].CatchTime) == catchID {
				picked = &user.StoragePets[i]
				pickedIndex = i
				break
			}
		}
		if picked == nil {
			var matchByID []int
			for i := range user.StoragePets {
				if user.StoragePets[i].ID == int(catchID) {
					matchByID = append(matchByID, i)
				}
			}
			if len(matchByID) == 1 {
				pickedIndex = matchByID[0]
				picked = &user.StoragePets[pickedIndex]
			}
		}
		// 精元孵化完成领取：无匹配仓库精灵时，检查 SoulBeadTransform（ObtainTime=catchID 且已到期）
		if picked == nil && user.SoulBeadTransform != nil && user.SoulBeadTransform.ObtainTime == catchID &&
			time.Now().Unix() >= user.SoulBeadTransform.ExpireTime {
			petClass := int(user.SoulBeadTransform.RewardPetClass)
			if petClass <= 0 {
				petClass = int(user.SoulBeadTransform.ItemID)
			}
			rand.Seed(time.Now().UnixNano() + int64(catchID))
			newPet := userdb.Pet{
				ID:        petClass,
				CatchTime: int(catchID),
				Level:     1,
				DV:        rand.Intn(32),   // 个体 0-31 随机
				Nature:    rand.Intn(25),   // 性格 0-24 随机
				Exp:       0,
				Name:      "",
				Trait:     0,               // 精元孵化无特性
			}
			user.SoulBeadTransform = nil
			if len(user.Pets) < 6 {
				user.Pets = append(user.Pets, newPet)
				picked = &user.Pets[len(user.Pets)-1]
				respFlag = 1
			} else {
				user.StoragePets = append(user.StoragePets, newPet)
				// 客户端 addStorage 分支不期望 PetInfo，但 2304 响应格式一致，flag=0 表示进仓库
				respFlag = 0
			}
			logger.Info(fmt.Sprintf("[2304] 精元孵化领取: UID=%d PetClass=%d CatchTime=%d", ctx.UserID, petClass, catchID))
		}
		if picked != nil && pickedIndex >= 0 {
			// 先拷贝一份，避免在切片删除后 picked 指向的数据被覆盖，导致“进背包的不是同一只 / 仓库精灵丢失”
			petCopy := *picked
			// 从仓库移除
			user.StoragePets = append(user.StoragePets[:pickedIndex], user.StoragePets[pickedIndex+1:]...)
			// 添加到背包
			user.Pets = append(user.Pets, petCopy)
			// 用拷贝作为响应体
			*picked = petCopy
			respFlag = 1 // 返回PetInfo
		}
	} else {
		// 背包 -> 仓库：先按 CatchTime 匹配，未命中时兼容按 Pet ID（仅当背包中该 ID 唯一时）
		for i := range user.Pets {
			if uint32(user.Pets[i].CatchTime) == catchID {
				picked = &user.Pets[i]
				pickedIndex = i
				break
			}
		}
		if picked == nil {
			var matchByID []int
			for i := range user.Pets {
				if user.Pets[i].ID == int(catchID) {
					matchByID = append(matchByID, i)
				}
			}
			if len(matchByID) == 1 {
				pickedIndex = matchByID[0]
				picked = &user.Pets[pickedIndex]
			}
		}
		if picked != nil {
			// 先拷贝一份，避免后续对切片的修改影响 picked 指针
			petCopy := *picked
			// 从背包移除
			user.Pets = append(user.Pets[:pickedIndex], user.Pets[pickedIndex+1:]...)
			// 添加到仓库
			user.StoragePets = append(user.StoragePets, petCopy)
			respFlag = 0 // 不返回PetInfo
		}
	}

	firstPetTime := uint32(0)
	if len(user.Pets) > 0 {
		firstPetTime = uint32(user.Pets[0].CatchTime)
	}

	// 保存数据
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	petBody := []byte{}
	if picked != nil && respFlag == 1 {
		petBody = buildFullPetInfo(*picked)
	}

	body := make([]byte, 12+len(petBody))
	binary.BigEndian.PutUint32(body[0:4], 0)            // homeEnergy
	binary.BigEndian.PutUint32(body[4:8], firstPetTime) // firstPetTime
	binary.BigEndian.PutUint32(body[8:12], respFlag)    // flag
	if len(petBody) > 0 {
		copy(body[12:], petBody)
	}

	ctx.GameServer.SendResponse(ctx.ClientData, 2304, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2304] 精灵仓库互转: catchID=%d flag=%d respFlag=%d", catchID, reqFlag, respFlag))
}

// handleGetPetInfo CMD 2301 获取精灵完整信息（用于战斗前 PetManager 初始化 / PvP 时查看对方精灵）
// 请求: catchTime(4)；若为 0 则返回当前/第一只精灵。PvP 时客户端会用对方 catchTime 请求，需从对方用户查并返回
func handleGetPetInfo(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	var catch uint32
	if len(ctx.Body) >= 4 {
		catch = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	var picked *userdb.Pet
	if catch != 0 {
		for i := range user.Pets {
			if uint32(user.Pets[i].CatchTime) == catch {
				picked = &user.Pets[i]
				break
			}
		}
		if picked == nil && user.StoragePets != nil {
			for i := range user.StoragePets {
				if uint32(user.StoragePets[i].CatchTime) == catch {
					picked = &user.StoragePets[i]
					break
				}
			}
		}
		// PvP 时：若己方未找到该 catchTime，尝试从对方用户查找（用于显示对方精灵属性/模型）
		if picked == nil {
			ctx.GameServer.BattleMu.Lock()
			battle, ok := ctx.GameServer.BattleStates[ctx.UserID]
			if ok && battle.IsActive && battle.OpponentUserID != 0 {
				oppUser := ctx.GameServer.GetOrCreateUser(battle.OpponentUserID)
				for i := range oppUser.Pets {
					if uint32(oppUser.Pets[i].CatchTime) == catch {
						picked = &oppUser.Pets[i]
						break
					}
				}
				ctx.GameServer.BattleMu.Unlock()
				if picked != nil {
					body := buildFullPetInfo(*picked)
					logger.Info(fmt.Sprintf("[2301] PvP 对方精灵: PetID=%d Level=%d BodyLen=%d", picked.ID, picked.Level, len(body)))
					ctx.GameServer.SendResponse(ctx.ClientData, 2301, ctx.UserID, ctx.SeqID, body)
					return
				}
			} else {
				ctx.GameServer.BattleMu.Unlock()
			}
		}
	}
	if picked == nil && len(user.Pets) > 0 {
		picked = &user.Pets[0]
	}
	if picked == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2301, ctx.UserID, ctx.SeqID, []byte{})
		return
	}

	body := buildFullPetInfo(*picked)
	logger.Info(fmt.Sprintf("[2301] 发送完整宠物信息: PetID=%d Level=%d BodyLen=%d", picked.ID, picked.Level, len(body)))
	ctx.GameServer.SendResponse(ctx.ClientData, 2301, ctx.UserID, ctx.SeqID, body)
}

// handleSetDefaultPet CMD 2308 设置首发精灵（客户端 CommandID.PET_DEFAULT）
// 请求: catchTime(4) - 要设置为首发的精灵的捕获时间
// 响应: 4 字节 0 表示成功（客户端部分实现依赖非空包才触发回调）
func handleSetDefaultPet(ctx *gameserver.HandlerContext) {
	respBody := make([]byte, 4)
	binary.BigEndian.PutUint32(respBody[0:4], 0)
	if len(ctx.Body) < 4 {
		logger.Error("[2308] 请求体长度不足")
		ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, respBody)
		return
	}

	catchTime := binary.BigEndian.Uint32(ctx.Body[0:4])
	logger.Info(fmt.Sprintf("[2308] 收到设置首发精灵请求: UID=%d CatchTime=%d", ctx.UserID, catchTime))

	gameData := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if gameData == nil {
		logger.Error(fmt.Sprintf("[2308] 无法获取用户数据: UID=%d", ctx.UserID))
		ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, respBody)
		return
	}

	var targetIndex = -1
	for i, pet := range gameData.Pets {
		if uint32(pet.CatchTime) == catchTime {
			targetIndex = i
			break
		}
	}

	if targetIndex == -1 {
		logger.Error(fmt.Sprintf("[2308] 未找到指定精灵: UID=%d CatchTime=%d", ctx.UserID, catchTime))
		ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, respBody)
		return
	}

	if targetIndex == 0 {
		logger.Info(fmt.Sprintf("[2308] 精灵已是首发: UID=%d PetID=%d", ctx.UserID, gameData.Pets[0].ID))
		ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, respBody)
		return
	}

	targetPet := gameData.Pets[targetIndex]
	logger.Info(fmt.Sprintf("[2308] 移动精灵到首发位置: UID=%d PetID=%d 从位置%d到位置0", ctx.UserID, targetPet.ID, targetIndex))

	newPets := make([]userdb.Pet, len(gameData.Pets))
	newPets[0] = targetPet
	newIndex := 1
	for i, pet := range gameData.Pets {
		if i != targetIndex {
			newPets[newIndex] = pet
			newIndex++
		}
	}
	gameData.Pets = newPets
	ctx.GameServer.UserDB.SaveGameData(ctx.UserID, gameData)

	logger.Info(fmt.Sprintf("[2308] 设置首发精灵成功: UID=%d PetID=%d CatchTime=%d", ctx.UserID, targetPet.ID, catchTime))
	ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, respBody)
}

// handleGetPetList CMD 2303 获取精灵列表（返回仓库中的精灵）
// 对齐 Lua: pet_handlers.handleGetPetList
// 响应: count(4) + [petId(4) + catchTime(4) + skinID(4)]...
func handleGetPetList(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// 返回仓库中的精灵（不是背包）
	if user.StoragePets == nil {
		user.StoragePets = []userdb.Pet{}
	}
	pets := user.StoragePets
	count := uint32(len(pets))

	body := make([]byte, 4+int(count)*12)
	binary.BigEndian.PutUint32(body[0:4], count)
	off := 4
	for _, p := range pets {
		binary.BigEndian.PutUint32(body[off:off+4], uint32(p.ID))
		off += 4
		binary.BigEndian.PutUint32(body[off:off+4], uint32(p.CatchTime))
		off += 4
		binary.BigEndian.PutUint32(body[off:off+4], 0) // skinID = 0
		off += 4
	}

	ctx.GameServer.SendResponse(ctx.ClientData, 2303, ctx.UserID, ctx.SeqID, body[:off])
}

// handleModifyPetName CMD 2302 修改精灵昵称
// 目前前端未直接使用该协议，这里按 catchTime(4)+newName(16) 的格式兼容写入服务端数据，并返回 4 字节 0 ACK。
func handleModifyPetName(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 20 {
		body := make([]byte, 4)
		ctx.GameServer.SendResponse(ctx.ClientData, 2302, ctx.UserID, ctx.SeqID, body)
		return
	}
	catchTime := binary.BigEndian.Uint32(ctx.Body[0:4])
	newNameRaw := ctx.Body[4:20]
	newName := string(bytes.TrimRight(newNameRaw, "\x00"))

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	var picked *userdb.Pet
	for i := range user.Pets {
		if uint32(user.Pets[i].CatchTime) == catchTime {
			picked = &user.Pets[i]
			break
		}
	}
	if picked == nil && user.StoragePets != nil {
		for i := range user.StoragePets {
			if uint32(user.StoragePets[i].CatchTime) == catchTime {
				picked = &user.StoragePets[i]
				break
			}
		}
	}
	if picked != nil {
		picked.Name = newName
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
		logger.Info(fmt.Sprintf("[2302] 修改精灵昵称: UID=%d CatchTime=%d NewName=%s", ctx.UserID, catchTime, newName))
	}
	resp := make([]byte, 4)
	ctx.GameServer.SendResponse(ctx.ClientData, 2302, ctx.UserID, ctx.SeqID, resp)
}

// handlePetCureAll CMD 2306 全体精灵恢复（客户端主要用于扣费与本地恢复 HP/PP）
// 请求: 空；响应: ret(4)=0。这里按 Lua 习惯扣 50 赛尔豆（不足时扣到 0 不报错）。
func handlePetCureAll(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2306, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}
	const cost = 50
	if user.Coins > 0 {
		if user.Coins >= cost {
			user.Coins -= cost
		} else {
			user.Coins = 0
		}
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
	}
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2306, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2306] 全体精灵恢复: UID=%d Coins=%d", ctx.UserID, user.Coins))
}

// handlePetOneCure CMD 2310 单只精灵恢复
// 请求: catchTime(4)；响应: catchTime(4)。若非超级 NONO，每次扣 20 赛尔豆（不足时扣到 0）。
func handlePetOneCure(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 4 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2310, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}
	catchTime := binary.BigEndian.Uint32(ctx.Body[0:4])
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2310, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}

	var picked *userdb.Pet
	for i := range user.Pets {
		if uint32(user.Pets[i].CatchTime) == catchTime {
			picked = &user.Pets[i]
			break
		}
	}
	if picked == nil && user.StoragePets != nil {
		for i := range user.StoragePets {
			if uint32(user.StoragePets[i].CatchTime) == catchTime {
				picked = &user.StoragePets[i]
				break
			}
		}
	}
	if picked == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2310, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}

	// 超能 NONO 免单，否则扣 20 赛尔豆
	if user.Nono.SuperNono <= 0 && user.Coins > 0 {
		const cost = 20
		if user.Coins >= cost {
			user.Coins -= cost
		} else {
			user.Coins = 0
		}
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	resp := make([]byte, 4)
	binary.BigEndian.PutUint32(resp[0:4], catchTime)
	ctx.GameServer.SendResponse(ctx.ClientData, 2310, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2310] 单只精灵恢复: UID=%d CatchTime=%d Coins=%d", ctx.UserID, catchTime, user.Coins))
}

// handlePetCollect CMD 2311 精灵收集赠宠（例如新船员三选一）
// 请求: activityId(4) + petId(4)
// 响应: petId(4) + catchTime(4)
func handlePetCollect(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 8 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2311, ctx.UserID, ctx.SeqID, make([]byte, 8))
		return
	}
	activityID := binary.BigEndian.Uint32(ctx.Body[0:4])
	requestPetID := binary.BigEndian.Uint32(ctx.Body[4:8])

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2311, ctx.UserID, ctx.SeqID, make([]byte, 8))
		return
	}

	// 从 GM 奖励配置中查找该 activityId 对应的奖励精灵；数据未填则使用解包默认
	rewardCfg := DefaultRewardConfigUnpacked()
	if data, err := ctx.GameServer.UserDB.LoadRewardConfig(); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &rewardCfg)
	}
	finalPetID := requestPetID
	level := 5
	dv := 31
	nature := 0
	for _, r := range rewardCfg.CollectRewards {
		if r.ActivityID == activityID {
			if r.PetID != 0 {
				finalPetID = r.PetID
			}
			if r.Level > 0 {
				level = r.Level
			}
			if r.DV > 0 {
				dv = r.DV
			}
			if r.Nature >= 0 {
				nature = r.Nature
			}
			break
		}
	}

	// 生成唯一 catchTime（参考捕捉与融合逻辑）
	newCatchTime := uint32(time.Now().Unix() & 0xFFFFFFFF)
	if newCatchTime == 0 {
		newCatchTime = 1
	}
	used := func(ct uint32) bool {
		for _, p := range user.Pets {
			if uint32(p.CatchTime) == ct {
				return true
			}
		}
		for _, p := range user.StoragePets {
			if uint32(p.CatchTime) == ct {
				return true
			}
		}
		return false
	}
	for used(newCatchTime) {
		newCatchTime++
	}

	// 默认给 GM 配置的等级 / DV / 性格，经验 0
	newPet := userdb.Pet{
		ID:        int(finalPetID),
		CatchTime: int(newCatchTime),
		Level:     level,
		DV:        dv,
		Nature:    nature,
		Exp:       0,
		Name:      "",
	}

	// 背包最多 6 只，其余进仓库
	if len(user.Pets) < 6 {
		user.Pets = append(user.Pets, newPet)
		logger.Info(fmt.Sprintf("[2311] 赠宠发放到背包: UID=%d Activity=%d PetID=%d CatchTime=%d", ctx.UserID, activityID, finalPetID, newCatchTime))
	} else {
		user.StoragePets = append(user.StoragePets, newPet)
		logger.Info(fmt.Sprintf("[2311] 赠宠发放到仓库: UID=%d Activity=%d PetID=%d CatchTime=%d", ctx.UserID, activityID, finalPetID, newCatchTime))
	}

	// 记录图鉴捕获
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.RecordCatch(ctx.UserID, int(finalPetID))
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	resp := make([]byte, 8)
	binary.BigEndian.PutUint32(resp[0:4], finalPetID)
	binary.BigEndian.PutUint32(resp[4:8], newCatchTime)
	ctx.GameServer.SendResponse(ctx.ClientData, 2311, ctx.UserID, ctx.SeqID, resp)
}

// handleIsCollect CMD 2313 精灵收集奖励检测
// 请求: groupId(4)
// 响应: groupId(4) + hasReward(4)，目前仅精确支持 groupId=301（新船员 starter）
func handleIsCollect(ctx *gameserver.HandlerContext) {
	var groupID uint32
	if len(ctx.Body) >= 4 {
		groupID = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2313, ctx.UserID, ctx.SeqID, make([]byte, 8))
		return
	}

	var hasReward uint32
	switch groupID {
	case 301:
		// 新船员 starter：拥有 1 / 4 / 7 任一精灵视为已领取
		starterIDs := map[int]bool{1: true, 4: true, 7: true}
		check := func(pets []userdb.Pet) bool {
			for _, p := range pets {
				if starterIDs[p.ID] {
					return true
				}
			}
			return false
		}
		if check(user.Pets) || check(user.StoragePets) {
			hasReward = 1
		}
	default:
		// 其他分组暂按“未领取”处理，避免误发奖励；需要时可按 Lua 行为扩展
		hasReward = 0
	}

	resp := make([]byte, 8)
	binary.BigEndian.PutUint32(resp[0:4], groupID)
	binary.BigEndian.PutUint32(resp[4:8], hasReward)
	ctx.GameServer.SendResponse(ctx.ClientData, 2313, ctx.UserID, ctx.SeqID, resp)
}

// handlePetEvolvtion CMD 2314 精灵进化（基础线性进化）
// 请求: catchTime(4) + branchIndex(4)
// 为简化，对应 spt.xml 中 EvolvesTo>0 的线性进化；分支进化与道具校验可后续按 Lua 扩展。
func handlePetEvolvtion(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 4 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2314, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}
	catchTime := binary.BigEndian.Uint32(ctx.Body[0:4])
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2314, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}

	var picked *userdb.Pet
	for i := range user.Pets {
		if uint32(user.Pets[i].CatchTime) == catchTime {
			picked = &user.Pets[i]
			break
		}
	}
	if picked == nil && user.StoragePets != nil {
		for i := range user.StoragePets {
			if uint32(user.StoragePets[i].CatchTime) == catchTime {
				picked = &user.StoragePets[i]
				break
			}
		}
	}
	if picked == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2314, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}

	petMgr := gamepets.GetInstance()

	// 根据当前精灵配置和背包物品真实计算 hasItem，再调用 CanEvolve
	hasItem := false
	petDef := petMgr.Get(picked.ID)
	if petDef != nil && petDef.EvolvItem > 0 {
		// 需要特定进化道具：EvolvItem / EvolvItemCount
		if user.Items != nil {
			itemKey := strconv.Itoa(petDef.EvolvItem)
			if it, ok := user.Items[itemKey]; ok && it.Count >= petDef.EvolvItemCount {
				hasItem = true
			}
		}
	} else {
		// 无进化道具要求时，仅按等级/进化舱限制判断
		hasItem = true
	}

	can, _, evolveTo := petMgr.CanEvolve(picked.ID, picked.Level, hasItem)
	if !can || evolveTo <= 0 {
		// 不可进化时返回成功但不做修改，避免前端卡死
		resp := make([]byte, 4)
		binary.BigEndian.PutUint32(resp[0:4], 0)
		ctx.GameServer.SendResponse(ctx.ClientData, 2314, ctx.UserID, ctx.SeqID, resp)
		logger.Info(fmt.Sprintf("[2314] 精灵无法进化: UID=%d PetID=%d Level=%d", ctx.UserID, picked.ID, picked.Level))
		return
	}

	oldID := picked.ID

	// 若配置要求消耗道具，则在这里真正扣除 EvolvItem/EvolvItemCount
	if petDef != nil && petDef.EvolvItem > 0 && petDef.EvolvItemCount > 0 && user.Items != nil {
		itemKey := strconv.Itoa(petDef.EvolvItem)
		if it, ok := user.Items[itemKey]; ok && it.Count >= petDef.EvolvItemCount {
			it.Count -= petDef.EvolvItemCount
			if it.Count <= 0 {
				delete(user.Items, itemKey)
			} else {
				user.Items[itemKey] = it
			}
		}
	}

	picked.ID = evolveTo
	picked.Exp = 0

	// 计算新属性并推送 NOTE_UPDATE_PROP + 完整宠物信息
	ev := gamepets.ClampAndCapEV(picked.GetEVStats())
	stats := petMgr.GetStats(picked.ID, picked.Level, picked.DV, ev, picked.Nature)
	propBody := buildNoteUpdateProp(uint32(picked.CatchTime), picked.ID, picked.Level, picked.Exp,
		stats.MaxHP, stats.Attack, stats.Defence, stats.SpAtk, stats.SpDef, stats.Speed, ev)
	ctx.GameServer.SendResponse(ctx.ClientData, 2508, ctx.UserID, ctx.SeqID, propBody)

	fullBody := buildFullPetInfo(*picked)
	ctx.GameServer.SendResponse(ctx.ClientData, 2301, ctx.UserID, ctx.SeqID, fullBody)

	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	resp := make([]byte, 4)
	binary.BigEndian.PutUint32(resp[0:4], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2314, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2314] 精灵进化: UID=%d %d -> %d Level=%d", ctx.UserID, oldID, evolveTo, picked.Level))
}

// handlePetBargeList CMD 2309 精灵图鉴/收集统计
// 请求: startId(4) + endId(4)
// 响应: monCount(4) + [monID(4) + enCntCnt(4) + isCatched(4) + isKilled(4)] * monCount
func handlePetBargeList(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 8 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2309, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}
	startID := binary.BigEndian.Uint32(ctx.Body[0:4])
	endID := binary.BigEndian.Uint32(ctx.Body[4:8])
	if endID < startID {
		startID, endID = endID, startID
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil || user.PetBook == nil || len(user.PetBook) == 0 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2309, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}

	body := make([]byte, 4)
	count := uint32(0)
	tmp := make([]byte, 16)
	for id := startID; id <= endID; id++ {
		key := fmt.Sprintf("%d", id)
		entry, ok := user.PetBook[key]
		if !ok {
			continue
		}
		// 仅在有遭遇/击杀/捕获记录时返回该精灵
		if entry.Encountered == 0 && entry.Killed == 0 && entry.Caught == 0 {
			continue
		}
		binary.BigEndian.PutUint32(tmp[0:4], id)
		binary.BigEndian.PutUint32(tmp[4:8], uint32(entry.Encountered))
		binary.BigEndian.PutUint32(tmp[8:12], uint32(entry.Caught))
		isKilled := uint32(0)
		if entry.Killed > 0 {
			isKilled = 1
		}
		binary.BigEndian.PutUint32(tmp[12:16], isKilled)
		body = append(body, tmp...)
		count++
	}

	binary.BigEndian.PutUint32(body[0:4], count)
	ctx.GameServer.SendResponse(ctx.ClientData, 2309, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2309] 精灵收集统计: UID=%d range=%d-%d count=%d", ctx.UserID, startID, endID, count))
}

// handleRoweiPetList CMD 2320 罗威训练精灵列表
// 当前实现为内存态：返回已在 RoweiPets 中登记的精灵列表（id+catchTime+skinID）
func handleRoweiPetList(ctx *gameserver.HandlerContext) {
	ctx.GameServer.RoweiPetsMu.RLock()
	userPets, ok := ctx.GameServer.RoweiPets[ctx.UserID]
	ctx.GameServer.RoweiPetsMu.RUnlock()
	if !ok || len(userPets) == 0 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2320, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}

	count := uint32(len(userPets))
	body := make([]byte, 4+len(userPets)*12)
	binary.BigEndian.PutUint32(body[0:4], count)
	off := 4
	for ct, pid := range userPets {
		binary.BigEndian.PutUint32(body[off:off+4], pid)
		off += 4
		binary.BigEndian.PutUint32(body[off:off+4], ct)
		off += 4
		binary.BigEndian.PutUint32(body[off:off+4], pid) // skinID 简单等于 petID
		off += 4
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 2320, ctx.UserID, ctx.SeqID, body[:off])
}

// handleRoweiPetStart CMD 2321 开始罗威训练
// 请求: petId(4) + catchTime(4)；响应: 4 字节 0
func handleRoweiPetStart(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 8 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2321, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}
	petID := binary.BigEndian.Uint32(ctx.Body[0:4])
	catchTime := binary.BigEndian.Uint32(ctx.Body[4:8])

	ctx.GameServer.RoweiPetsMu.Lock()
	if ctx.GameServer.RoweiPets[ctx.UserID] == nil {
		ctx.GameServer.RoweiPets[ctx.UserID] = make(map[uint32]uint32)
	}
	ctx.GameServer.RoweiPets[ctx.UserID][catchTime] = petID
	ctx.GameServer.RoweiPetsMu.Unlock()

	resp := make([]byte, 4)
	binary.BigEndian.PutUint32(resp[0:4], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2321, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2321] 罗威训练开始: UID=%d PetID=%d CatchTime=%d", ctx.UserID, petID, catchTime))
}

// handleRoweiPetReturn CMD 2322 罗威训练取回
// 请求: catchTime(4)；响应: 4 字节 0
func handleRoweiPetReturn(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 4 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2322, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}
	catchTime := binary.BigEndian.Uint32(ctx.Body[0:4])

	ctx.GameServer.RoweiPetsMu.Lock()
	if m, ok := ctx.GameServer.RoweiPets[ctx.UserID]; ok {
		delete(m, catchTime)
	}
	ctx.GameServer.RoweiPetsMu.Unlock()

	resp := make([]byte, 4)
	binary.BigEndian.PutUint32(resp[0:4], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2322, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2322] 罗威训练取回: UID=%d CatchTime=%d", ctx.UserID, catchTime))
}

// handleRoomPetShow CMD 2323 房间精灵展示设置
// 请求:
//   count(4)=0                  -> 清空展示
//   count(4)>0 + [catchTime(4)+id(4)]*count  -> 设置展示列表
// 响应: count(4) + [id(4)+catchTime(4)+skinID(4)]*count
func handleRoomPetShow(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 4 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2323, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}
	count := binary.BigEndian.Uint32(ctx.Body[0:4])

	ctx.GameServer.RoomPetsMu.Lock()
	if ctx.GameServer.RoomPets[ctx.UserID] == nil {
		ctx.GameServer.RoomPets[ctx.UserID] = make(map[uint32]uint32)
	}
	roomMap := ctx.GameServer.RoomPets[ctx.UserID]
	if count == 0 {
		for k := range roomMap {
			delete(roomMap, k)
		}
	} else {
		expectedLen := 4 + int(count)*8
		if len(ctx.Body) < expectedLen {
			// 包体不足，保持原有配置
			ctx.GameServer.RoomPetsMu.Unlock()
			ctx.GameServer.SendResponse(ctx.ClientData, 2323, ctx.UserID, ctx.SeqID, make([]byte, 4))
			return
		}
		for k := range roomMap {
			delete(roomMap, k)
		}
		off := 4
		for i := uint32(0); i < count; i++ {
			catchTime := binary.BigEndian.Uint32(ctx.Body[off : off+4])
			off += 4
			petID := binary.BigEndian.Uint32(ctx.Body[off : off+4])
			off += 4
			roomMap[catchTime] = petID
		}
	}
	ctx.GameServer.RoomPetsMu.Unlock()

	// 直接复用 RoomPetList 的编码格式作为响应
	ctx.GameServer.RoomPetsMu.RLock()
	roomMap = ctx.GameServer.RoomPets[ctx.UserID]
	ctx.GameServer.RoomPetsMu.RUnlock()

	respCount := uint32(len(roomMap))
	body := make([]byte, 4+len(roomMap)*12)
	binary.BigEndian.PutUint32(body[0:4], respCount)
	off := 4
	for ct, pid := range roomMap {
		binary.BigEndian.PutUint32(body[off:off+4], pid)
		off += 4
		binary.BigEndian.PutUint32(body[off:off+4], ct)
		off += 4
		binary.BigEndian.PutUint32(body[off:off+4], pid) // skinID = petID
		off += 4
	}

	ctx.GameServer.SendResponse(ctx.ClientData, 2323, ctx.UserID, ctx.SeqID, body[:off])
	logger.Info(fmt.Sprintf("[2323] 房间展示更新: UID=%d count=%d", ctx.UserID, respCount))
}

// handleRoomPetList CMD 2324 获取房间展示精灵列表
// 请求: ownerId(4)；响应: count(4) + [id(4)+catchTime(4)+skinID(4)]*count
func handleRoomPetList(ctx *gameserver.HandlerContext) {
	var ownerID uint32
	if len(ctx.Body) >= 4 {
		ownerID = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	if ownerID == 0 {
		ownerID = uint32(ctx.UserID)
	}

	ctx.GameServer.RoomPetsMu.RLock()
	roomMap, ok := ctx.GameServer.RoomPets[int64(ownerID)]
	ctx.GameServer.RoomPetsMu.RUnlock()
	if !ok || len(roomMap) == 0 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2324, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}

	count := uint32(len(roomMap))
	body := make([]byte, 4+len(roomMap)*12)
	binary.BigEndian.PutUint32(body[0:4], count)
	off := 4
	for ct, pid := range roomMap {
		binary.BigEndian.PutUint32(body[off:off+4], pid)
		off += 4
		binary.BigEndian.PutUint32(body[off:off+4], ct)
		off += 4
		binary.BigEndian.PutUint32(body[off:off+4], pid) // skinID = petID
		off += 4
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 2324, ctx.UserID, ctx.SeqID, body[:off])
	logger.Info(fmt.Sprintf("[2324] 房间展示列表: OwnerID=%d count=%d", ownerID, count))
}

// handleUseSpeedupItem CMD 2327 使用经验加速道具（2/3 倍）
// 请求: itemId(4)
// 响应: twoTimes(4) + threeTimes(4)
// 具体哪种道具对应 2 倍或 3 倍可按需要扩展，目前统一视作 twoTimes+1。
func handleUseSpeedupItem(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 4 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2327, ctx.UserID, ctx.SeqID, make([]byte, 8))
		return
	}
	itemID := binary.BigEndian.Uint32(ctx.Body[0:4])
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2327, ctx.UserID, ctx.SeqID, make([]byte, 8))
		return
	}

	if user.Items == nil {
		user.Items = make(map[string]userdb.Item)
	}
	itemKey := strconv.FormatUint(uint64(itemID), 10)
	it, ok := user.Items[itemKey]
	if !ok || it.Count <= 0 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2327, ctx.UserID, ctx.SeqID, make([]byte, 8))
		return
	}
	it.Count--
	if it.Count <= 0 {
		delete(user.Items, itemKey)
	} else {
		user.Items[itemKey] = it
	}

	// 根据 GM 配置计算 twoTimes / threeTimes 增量和上限；数据未填则使用解包默认
	cfg := DefaultBuffItemsConfigUnpacked()
	if data, err := ctx.GameServer.UserDB.LoadBuffItemsConfig(); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &cfg)
	}
	addTwo, addThree := 1, 0
	maxTwo, maxThree := 0, 0
	for _, itCfg := range cfg.SpeedupItems {
		if itCfg.ItemID == itemID {
			if itCfg.TwoTimesAdd != 0 {
				addTwo = itCfg.TwoTimesAdd
			}
			addThree = itCfg.ThreeTimesAdd
			maxTwo = itCfg.MaxTwoTimes
			maxThree = itCfg.MaxThreeTimes
			break
		}
	}
	if addTwo != 0 {
		user.TwoTimes += addTwo
		if maxTwo > 0 && user.TwoTimes > maxTwo {
			user.TwoTimes = maxTwo
		}
	}
	if addThree != 0 {
		user.ThreeTimes += addThree
		if maxThree > 0 && user.ThreeTimes > maxThree {
			user.ThreeTimes = maxThree
		}
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	resp := make([]byte, 8)
	binary.BigEndian.PutUint32(resp[0:4], uint32(user.TwoTimes))
	binary.BigEndian.PutUint32(resp[4:8], uint32(user.ThreeTimes))
	ctx.GameServer.SendResponse(ctx.ClientData, 2327, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2327] 使用经验加速道具: UID=%d itemID=%d TwoTimes=%d ThreeTimes=%d", ctx.UserID, itemID, user.TwoTimes, user.ThreeTimes))
}

// handleUseAutoFightItem CMD 2329 使用自动战斗道具
// 请求: itemId(4)
// 响应: autoFight(4) + autoFightTimes(4)
func handleUseAutoFightItem(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 4 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2329, ctx.UserID, ctx.SeqID, make([]byte, 8))
		return
	}
	itemID := binary.BigEndian.Uint32(ctx.Body[0:4])
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2329, ctx.UserID, ctx.SeqID, make([]byte, 8))
		return
	}

	if user.Items == nil {
		user.Items = make(map[string]userdb.Item)
	}
	itemKey := strconv.FormatUint(uint64(itemID), 10)
	it, ok := user.Items[itemKey]
	if !ok || it.Count <= 0 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2329, ctx.UserID, ctx.SeqID, make([]byte, 8))
		return
	}
	it.Count--
	if it.Count <= 0 {
		delete(user.Items, itemKey)
	} else {
		user.Items[itemKey] = it
	}

	// 读取 GM 配置；数据未填则使用解包默认
	cfg := DefaultBuffItemsConfigUnpacked()
	if data, err := ctx.GameServer.UserDB.LoadBuffItemsConfig(); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &cfg)
	}
	addTimes := 1
	maxTimes := 0
	enable := true
	for _, itCfg := range cfg.AutoFightItems {
		if itCfg.ItemID == itemID {
			if itCfg.TimesAdd != 0 {
				addTimes = itCfg.TimesAdd
			}
			maxTimes = itCfg.MaxTimes
			enable = itCfg.Enable
			break
		}
	}
	if enable {
		user.AutoFight = 1
	}
	user.AutoFightTimes += addTimes
	if maxTimes > 0 && user.AutoFightTimes > maxTimes {
		user.AutoFightTimes = maxTimes
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	resp := make([]byte, 8)
	binary.BigEndian.PutUint32(resp[0:4], uint32(user.AutoFight))
	binary.BigEndian.PutUint32(resp[4:8], uint32(user.AutoFightTimes))
	ctx.GameServer.SendResponse(ctx.ClientData, 2329, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2329] 使用自动战斗道具: UID=%d itemID=%d AutoFight=%d Times=%d", ctx.UserID, itemID, user.AutoFight, user.AutoFightTimes))
}

// handleOnOffAutoFight CMD 2330 开关自动战斗
// 请求: flag(4)；响应: autoFight(4) + autoFightTimes(4)
func handleOnOffAutoFight(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 4 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2330, ctx.UserID, ctx.SeqID, make([]byte, 8))
		return
	}
	flag := binary.BigEndian.Uint32(ctx.Body[0:4])
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2330, ctx.UserID, ctx.SeqID, make([]byte, 8))
		return
	}

	if flag != 0 {
		user.AutoFight = 1
	} else {
		user.AutoFight = 0
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	resp := make([]byte, 8)
	binary.BigEndian.PutUint32(resp[0:4], uint32(user.AutoFight))
	binary.BigEndian.PutUint32(resp[4:8], uint32(user.AutoFightTimes))
	ctx.GameServer.SendResponse(ctx.ClientData, 2330, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2330] 自动战斗开关: UID=%d Flag=%d AutoFight=%d Times=%d", ctx.UserID, flag, user.AutoFight, user.AutoFightTimes))
}

// handleUseEnergyXishou CMD 2331 使用体力吸收/清零道具
// 请求: itemId(4)
// 响应: energyTimes(4)
func handleUseEnergyXishou(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 4 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2331, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}
	itemID := binary.BigEndian.Uint32(ctx.Body[0:4])
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2331, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}

	if user.Items == nil {
		user.Items = make(map[string]userdb.Item)
	}
	itemKey := strconv.FormatUint(uint64(itemID), 10)
	it, ok := user.Items[itemKey]
	if !ok || it.Count <= 0 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2331, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}
	it.Count--
	if it.Count <= 0 {
		delete(user.Items, itemKey)
	} else {
		user.Items[itemKey] = it
	}

	// 数据未填则使用解包默认
	cfg := DefaultBuffItemsConfigUnpacked()
	if data, err := ctx.GameServer.UserDB.LoadBuffItemsConfig(); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &cfg)
	}
	addTimes := 1
	maxTimes := 0
	for _, itCfg := range cfg.EnergyItems {
		if itCfg.ItemID == itemID {
			if itCfg.TimesAdd != 0 {
				addTimes = itCfg.TimesAdd
			}
			maxTimes = itCfg.MaxTimes
			break
		}
	}
	user.EnergyTimes += addTimes
	if maxTimes > 0 && user.EnergyTimes > maxTimes {
		user.EnergyTimes = maxTimes
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	resp := make([]byte, 4)
	binary.BigEndian.PutUint32(resp[0:4], uint32(user.EnergyTimes))
	ctx.GameServer.SendResponse(ctx.ClientData, 2331, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2331] 使用体力吸收道具: UID=%d itemID=%d EnergyTimes=%d", ctx.UserID, itemID, user.EnergyTimes))
}

// handleUseStudyItem CMD 2332 使用学习力双倍道具
// 请求: itemId(4)；响应: learnTimes(4)
func handleUseStudyItem(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 4 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2332, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}
	itemID := binary.BigEndian.Uint32(ctx.Body[0:4])
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2332, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}

	if user.Items == nil {
		user.Items = make(map[string]userdb.Item)
	}
	itemKey := strconv.FormatUint(uint64(itemID), 10)
	it, ok := user.Items[itemKey]
	if !ok || it.Count <= 0 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2332, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}
	it.Count--
	if it.Count <= 0 {
		delete(user.Items, itemKey)
	} else {
		user.Items[itemKey] = it
	}

	// 数据未填则使用解包默认
	cfg := DefaultBuffItemsConfigUnpacked()
	if data, err := ctx.GameServer.UserDB.LoadBuffItemsConfig(); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &cfg)
	}
	addTimes := 1
	maxTimes := 0
	for _, itCfg := range cfg.StudyItems {
		if itCfg.ItemID == itemID {
			if itCfg.TimesAdd != 0 {
				addTimes = itCfg.TimesAdd
			}
			maxTimes = itCfg.MaxTimes
			break
		}
	}
	user.LearnTimes += addTimes
	if maxTimes > 0 && user.LearnTimes > maxTimes {
		user.LearnTimes = maxTimes
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	resp := make([]byte, 4)
	binary.BigEndian.PutUint32(resp[0:4], uint32(user.LearnTimes))
	ctx.GameServer.SendResponse(ctx.ClientData, 2332, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2332] 使用学习力双倍道具: UID=%d itemID=%d LearnTimes=%d", ctx.UserID, itemID, user.LearnTimes))
}

// handlePetResetNature CMD 2343 重置精灵性格
// 请求: catchTime(4) + natureId(4) + flag(4)；响应: ret(4) 0=成功,1=失败
func handlePetResetNature(ctx *gameserver.HandlerContext) {
	if len(ctx.Body) < 8 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2343, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}
	catchTime := binary.BigEndian.Uint32(ctx.Body[0:4])
	natureID := binary.BigEndian.Uint32(ctx.Body[4:8])

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2343, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}

	var picked *userdb.Pet
	for i := range user.Pets {
		if uint32(user.Pets[i].CatchTime) == catchTime {
			picked = &user.Pets[i]
			break
		}
	}
	if picked == nil && user.StoragePets != nil {
		for i := range user.StoragePets {
			if uint32(user.StoragePets[i].CatchTime) == catchTime {
				picked = &user.StoragePets[i]
				break
			}
		}
	}

	resp := make([]byte, 4)
	if picked == nil {
		binary.BigEndian.PutUint32(resp[0:4], 1)
		ctx.GameServer.SendResponse(ctx.ClientData, 2343, ctx.UserID, ctx.SeqID, resp)
		logger.Info(fmt.Sprintf("[2343] 重置性格失败: 未找到精灵 UID=%d CatchTime=%d", ctx.UserID, catchTime))
		return
	}

	picked.Nature = int(natureID)
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	binary.BigEndian.PutUint32(resp[0:4], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2343, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2343] 重置性格成功: UID=%d CatchTime=%d Nature=%d", ctx.UserID, catchTime, natureID))
}

// ==================== 玩家对战邀请（2401/2403 -> 2501/2502） ====================

// handleInviteToFight CMD 2401 邀请玩家对战（INVITE_TO_FIGHT）
// 请求: targetUserID(4) + mode(4)，FightWaitPanel.send 发送被邀请方 userID 与对战模式(1=单挑 2=多精灵)
// 向被邀请方推送 NOTE_INVITE_TO_FIGHT(2501)，body: inviterUserID(4)+inviterNick(16)+mode(4)，供其弹窗选择接受/拒绝
func handleInviteToFight(ctx *gameserver.HandlerContext) {
	targetUserID := int64(0)
	mode := uint32(0)
	if len(ctx.Body) >= 8 {
		targetUserID = int64(binary.BigEndian.Uint32(ctx.Body[0:4]))
		mode = binary.BigEndian.Uint32(ctx.Body[4:8])
	}
	inviter := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	nick := inviter.Nick
	if nick == "" {
		nick = fmt.Sprintf("用户%d", ctx.UserID)
	}
	// 响应邀请者：简单回包（可选，客户端可能不依赖）
	bodyAck := make([]byte, 4)
	binary.BigEndian.PutUint32(bodyAck[0:4], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2401, ctx.UserID, ctx.SeqID, bodyAck)

	targetClient := ctx.GameServer.GetClientByUserID(targetUserID)
	if targetClient == nil {
		return // 被邀请方不在线，不推送
	}
	noteBody := make([]byte, 4+16+4)
	binary.BigEndian.PutUint32(noteBody[0:4], uint32(ctx.UserID))
	putFixedString(noteBody, 4, nick, 16)
	binary.BigEndian.PutUint32(noteBody[20:24], mode)
	ctx.GameServer.SendResponse(targetClient, 2501, ctx.UserID, 0, noteBody)
}

// handleHandleFightInvite CMD 2403 接受/拒绝对战邀请（HANDLE_FIGHT_INVITE）
// 请求: inviterUserID(4) + result(4) + type(4)，result=1 接受、0 拒绝；type 为对战模式
// 向邀请方推送 NOTE_HANDLE_FIGHT_INVITE(2502)，body: responderUserID(4)+responderNick(16)+result(4)
func handleHandleFightInvite(ctx *gameserver.HandlerContext) {
	inviterUserID := int64(0)
	result := uint32(0)
	if len(ctx.Body) >= 12 {
		inviterUserID = int64(binary.BigEndian.Uint32(ctx.Body[0:4]))
		result = binary.BigEndian.Uint32(ctx.Body[4:8])
	}
	responder := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	nick := responder.Nick
	if nick == "" {
		nick = fmt.Sprintf("用户%d", ctx.UserID)
	}
	bodyAck := make([]byte, 4)
	binary.BigEndian.PutUint32(bodyAck[0:4], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2403, ctx.UserID, ctx.SeqID, bodyAck)

	inviterClient := ctx.GameServer.GetClientByUserID(inviterUserID)
	if inviterClient == nil {
		return
	}
	noteBody := make([]byte, 4+16+4)
	binary.BigEndian.PutUint32(noteBody[0:4], uint32(ctx.UserID))
	putFixedString(noteBody, 4, nick, 16)
	binary.BigEndian.PutUint32(noteBody[20:24], result)
	ctx.GameServer.SendResponse(inviterClient, 2502, ctx.UserID, 0, noteBody)

	if result == 0 {
		OnArenaFightInviteRefused(ctx.GameServer, ctx.UserID, inviterUserID)
	}

	// 接受对战后：向双方推送 2503（准备对战）和 2504（开始对战），才能进入对战界面
	// PvP 2503 按接收方分别发送：邀请方收 [邀请方,接受方]，接受方收 [接受方,邀请方]，保证各方 userInfoArray[0]=自己，客户端用其加载“我方/对方”模型，避免双方显示同一精灵模型
	if result == 1 {
		inviterClient := ctx.GameServer.GetClientByUserID(inviterUserID)
		responderClient := ctx.ClientData
		if inviterClient != nil && responderClient != nil {
			// 2503：DLL 用 userInfoArray[0]=我方（左）、[1]=对方（右）。我方在前：邀请方收 [邀请方,接受方]、接受方收 [接受方,邀请方]
			body2503Inviter, body2503Responder := buildNoteReadyToFightInfoPvPPerClient(ctx.GameServer, inviterUserID, ctx.UserID)
			ctx.GameServer.SendResponse(inviterClient, 2503, inviterUserID, 0, body2503Inviter)
			ctx.GameServer.SendResponse(responderClient, 2503, ctx.UserID, 0, body2503Responder)
			// 2504：DLL 按包顺序“第一项→我方位、第二项→对方位”绑定 3D 模型，必须发 [我方,对方]；FightStartInfo 按 userID 区分 myInfo/otherInfo（血条/技能/日志）
			body2504Inviter, body2504Responder := buildNoteStartFightPvP(ctx.GameServer, inviterUserID, ctx.UserID)
			if len(body2504Inviter) > 0 {
				ctx.GameServer.SendResponse(inviterClient, 2504, inviterUserID, 0, body2504Inviter) // 邀请方收 [邀请方,接受方]=[我方,对方]
			}
			if len(body2504Responder) > 0 {
				ctx.GameServer.SendResponse(responderClient, 2504, ctx.UserID, 0, body2504Responder) // 接受方收 [接受方,邀请方]=[我方,对方]
			}
			OnArenaFightInviteAccepted(ctx.GameServer, ctx.UserID, inviterUserID)
			// PvP 初始化双方 BattleState
			setPvPBattleStates(ctx.GameServer, inviterUserID, ctx.UserID)
			inviterPetID, responderPetID := 7, 7
			if u1 := ctx.GameServer.GetOrCreateUser(inviterUserID); len(u1.Pets) > 0 {
				inviterPetID = u1.Pets[0].ID
			}
			if u2 := ctx.GameServer.GetOrCreateUser(ctx.UserID); len(u2.Pets) > 0 {
				responderPetID = u2.Pets[0].ID
			}
			logger.Info(fmt.Sprintf("[2403] PvP 接受: UID=%d(PetID=%d) vs UID=%d(PetID=%d)，2503/2504 包序 [我方,对方]", inviterUserID, inviterPetID, ctx.UserID, responderPetID))
		}
	}
}

// setPvPBattleStates 为 PvP 双方初始化 BattleState，使 2405 有状态、战斗能正常结算并发送 2506
func setPvPBattleStates(gs *gameserver.GameServer, inviterUID, responderUID int64) {
	petMgr := gamepets.GetInstance()
	u1 := gs.GetOrCreateUser(inviterUID)
	u2 := gs.GetOrCreateUser(responderUID)
	petID1, lv1, hp1, maxHp1 := 7, 5, 0, 0
	if len(u1.Pets) > 0 {
		petID1 = u1.Pets[0].ID
		if u1.Pets[0].Level > 0 {
			lv1 = u1.Pets[0].Level
		}
		ev := gamepets.EVStats{}
		ev = u1.Pets[0].GetEVStats()
		st := petMgr.GetStats(petID1, lv1, 31, ev, 0)
		hp1, maxHp1 = st.HP, st.MaxHP
	} else {
		st := petMgr.GetStats(petID1, lv1, 31, gamepets.EVStats{}, 0)
		hp1, maxHp1 = st.HP, st.MaxHP
	}
	petID2, lv2, hp2, maxHp2 := 7, 5, 0, 0
	if len(u2.Pets) > 0 {
		petID2 = u2.Pets[0].ID
		if u2.Pets[0].Level > 0 {
			lv2 = u2.Pets[0].Level
		}
		ev := gamepets.EVStats{}
		ev = u2.Pets[0].GetEVStats()
		st := petMgr.GetStats(petID2, lv2, 31, ev, 0)
		hp2, maxHp2 = st.HP, st.MaxHP
	} else {
		st := petMgr.GetStats(petID2, lv2, 31, gamepets.EVStats{}, 0)
		hp2, maxHp2 = st.HP, st.MaxHP
	}
	if maxHp1 <= 0 {
		maxHp1 = 1
	}
	if maxHp2 <= 0 {
		maxHp2 = 1
	}
	gs.BattleMu.Lock()
	defer gs.BattleMu.Unlock()
	gs.BattleStates[inviterUID] = &gameserver.BattleState{
		PlayerHP:        uint32(hp1),
		PlayerMaxHP:     uint32(maxHp1),
		EnemyHP:         uint32(hp2),
		EnemyMaxHP:      uint32(maxHp2),
		EnemyID:         petID2,
		EnemyLevel:      lv2,
		TotalPlayerPets: len(u1.Pets),
		DeadPlayerPets:  0,
		IsActive:        true,
		OpponentUserID:  responderUID,
	}
	gs.BattleStates[responderUID] = &gameserver.BattleState{
		PlayerHP:        uint32(hp2),
		PlayerMaxHP:     uint32(maxHp2),
		EnemyHP:         uint32(hp1),
		EnemyMaxHP:      uint32(maxHp1),
		EnemyID:         petID1,
		EnemyLevel:      lv1,
		TotalPlayerPets: len(u2.Pets),
		DeadPlayerPets:  0,
		IsActive:        true,
		OpponentUserID:  inviterUID,
	}
}

// buildNoteReadyToFightInfoPvP 构建 PvP 的 2503 包体：userCount(4) + [FighetUserInfo(20) + petCount(4) + SimplePetInfo(72)] * 2，与 NoteReadyToFightInfo 解析一致
func buildNoteReadyToFightInfoPvP(gs *gameserver.GameServer, inviterUID, responderUID int64) []byte {
	petMgr := gamepets.GetInstance()
	skillMgr := gameskills.GetInstance()
	buildFightUserInfo := func(uid uint32, nick string) []byte {
		b := make([]byte, 20)
		binary.BigEndian.PutUint32(b[0:4], uid)
		nb := []byte(nick)
		if len(nb) > 16 {
			nb = nb[:16]
		}
		copy(b[4:20], nb)
		return b
	}
	buildSimplePetInfo := func(petID uint32, level uint32, hp uint32, maxHp uint32, catchTime uint32, skills [][2]uint32) []byte {
		b := make([]byte, 72)
		binary.BigEndian.PutUint32(b[0:4], petID)
		binary.BigEndian.PutUint32(b[4:8], level)
		binary.BigEndian.PutUint32(b[8:12], hp)
		binary.BigEndian.PutUint32(b[12:16], maxHp)
		valid := uint32(0)
		for _, s := range skills {
			if s[0] != 0 {
				valid++
			}
		}
		binary.BigEndian.PutUint32(b[16:20], valid)
		off := 20
		for i := 0; i < 4; i++ {
			var sid, pp uint32
			if i < len(skills) {
				sid, pp = skills[i][0], skills[i][1]
			}
			binary.BigEndian.PutUint32(b[off:off+4], sid)
			binary.BigEndian.PutUint32(b[off+4:off+8], pp)
			off += 8
		}
		binary.BigEndian.PutUint32(b[52:56], catchTime)
		binary.BigEndian.PutUint32(b[56:60], 0)
		binary.BigEndian.PutUint32(b[60:64], 0)
		binary.BigEndian.PutUint32(b[64:68], 0)
		binary.BigEndian.PutUint32(b[68:72], petID) // skinID：客户端用此加载精灵模型/图标，0 会导致蓝格占位
		return b
	}
	getUserFirstPet := func(uid int64) (petID int, level int, catchTime uint32, hp, maxHp int, skills [][2]uint32, nick string) {
		u := gs.GetOrCreateUser(uid)
		nick = u.Nick
		if nick == "" {
			nick = fmt.Sprintf("用户%d", uid)
		}
		petID = 7
		level = 5
		catchTime = 0x69686700 + uint32(7)
		ev := gamepets.EVStats{}
		nature := 0
		if len(u.Pets) > 0 {
			petID = u.Pets[0].ID
			if u.Pets[0].Level > 0 {
				level = u.Pets[0].Level
			}
			catchTime = uint32(u.Pets[0].CatchTime)
			nature = u.Pets[0].Nature
			ev = u.Pets[0].GetEVStats()
			if catchTime == 0 {
				catchTime = 0x69686700 + uint32(petID)
			}
		}
		stats := petMgr.GetStats(petID, level, 31, ev, nature)
		hp, maxHp = stats.HP, stats.MaxHP
		raw := petMgr.GetSkillsForLevel(petID, level)
		for _, sid := range raw {
			if sid <= 0 {
				continue
			}
			pp := uint32(20)
			if sk := skillMgr.Get(sid); sk != nil {
				if sk.PP > 0 {
					pp = uint32(sk.PP)
				} else if sk.MaxPP > 0 {
					pp = uint32(sk.MaxPP)
				}
			}
			skills = append(skills, [2]uint32{uint32(sid), pp})
			if len(skills) >= 4 {
				break
			}
		}
		if len(skills) == 0 {
			skills = append(skills, [2]uint32{10001, 20})
		}
		return
	}
	// User1: 邀请方
	petID1, lv1, ct1, hp1, maxHp1, sk1, nick1 := getUserFirstPet(inviterUID)
	// User2: 接受方
	petID2, lv2, ct2, hp2, maxHp2, sk2, nick2 := getUserFirstPet(responderUID)
	out := make([]byte, 0, 4+96*2)
	tmp4 := make([]byte, 4)
	binary.BigEndian.PutUint32(tmp4, 2)
	out = append(out, tmp4...)
	out = append(out, buildFightUserInfo(uint32(inviterUID), nick1)...)
	binary.BigEndian.PutUint32(tmp4, 1)
	out = append(out, tmp4...)
	out = append(out, buildSimplePetInfo(uint32(petID1), uint32(lv1), uint32(hp1), uint32(maxHp1), ct1, sk1)...)
	out = append(out, buildFightUserInfo(uint32(responderUID), nick2)...)
	binary.BigEndian.PutUint32(tmp4, 1)
	out = append(out, tmp4...)
	out = append(out, buildSimplePetInfo(uint32(petID2), uint32(lv2), uint32(hp2), uint32(maxHp2), ct2, sk2)...)
	return out
}

// buildNoteReadyToFightInfoPvPPerClient 构建 PvP 的 2503 包体，按接收方分别返回：邀请方收 [邀请方,接受方]，接受方收 [接受方,邀请方]
// 保证各方 userInfoArray[0]=自己，客户端用其区分“我方/对方”并加载对应精灵模型（petArray/skinID），避免双方都显示同一模型
func buildNoteReadyToFightInfoPvPPerClient(gs *gameserver.GameServer, inviterUID, responderUID int64) (inviterBody, responderBody []byte) {
	bodyInviterFirst := buildNoteReadyToFightInfoPvP(gs, inviterUID, responderUID) // [邀请方, 接受方]
	// 接受方收 [接受方, 邀请方]：交换两段 FighetUserInfo(20)+petCount(4)+SimplePetInfo(72)
	const blockSize = 20 + 4 + 72 // 96
	if len(bodyInviterFirst) < 4+blockSize*2 {
		return bodyInviterFirst, bodyInviterFirst
	}
	inviterBody = bodyInviterFirst
	responderBody = make([]byte, len(bodyInviterFirst))
	copy(responderBody[0:4], bodyInviterFirst[0:4])                                 // userCount
	copy(responderBody[4:4+blockSize], bodyInviterFirst[4+blockSize:4+blockSize*2]) // 第二块 -> 第一块
	copy(responderBody[4+blockSize:4+blockSize*2], bodyInviterFirst[4:4+blockSize]) // 第一块 -> 第二块
	// 日志：验证 2503 userInfoArray 顺序，确保各方 userInfoArray[0]=自己
	// FighetUserInfo: userID(4) + nickName(16)，userID 在每块的开头
	logger.Info(fmt.Sprintf("[2503-PvP] 邀请方(UID=%d)包: userInfoArray[0] userID=%d, userInfoArray[1] userID=%d",
		inviterUID,
		binary.BigEndian.Uint32(inviterBody[4:8]), binary.BigEndian.Uint32(inviterBody[4+blockSize:4+blockSize+4])))
	logger.Info(fmt.Sprintf("[2503-PvP] 接受方(UID=%d)包: userInfoArray[0] userID=%d, userInfoArray[1] userID=%d",
		responderUID,
		binary.BigEndian.Uint32(responderBody[4:8]), binary.BigEndian.Uint32(responderBody[4+blockSize:4+blockSize+4])))
	return inviterBody, responderBody
}

// buildNoteStartFightPvP 构建 PvP 的 2504 包体：isCanAuto(4) + FightPetInfo(50)*2；首条为当前客户端的“我方”精灵，与 FightStartInfo 解析一致
// 返回 (inviter 的 2504 body, responder 的 2504 body)
func buildNoteStartFightPvP(gs *gameserver.GameServer, inviterUID, responderUID int64) (inviterBody, responderBody []byte) {
	petMgr := gamepets.GetInstance()
	// FightPetInfo 固定 50 字节，与客户端 FightPetInfo.as 解析一致：userID(4)+petID(4)+petName(16)+catchTime(4)+hp(4)+maxHP(4)+lv(4)+catchable(4)+battleLv(6)
	const fightPetInfoSize = 50
	buildFightPetInfo := func(uid uint32, petID int, name string, ct uint32, hp, maxHp, lv int, catchable uint32) []byte {
		if petID <= 0 {
			petID = 7
		}
		if maxHp <= 0 {
			maxHp = 1
		}
		if hp < 0 {
			hp = 0
		}
		if hp > maxHp {
			hp = maxHp
		}
		buf := make([]byte, fightPetInfoSize)
		binary.BigEndian.PutUint32(buf[0:4], uid)
		binary.BigEndian.PutUint32(buf[4:8], uint32(petID))
		nb := []byte(name)
		if len(nb) > 16 {
			nb = nb[:16]
		}
		copy(buf[8:24], nb) // 未写满的字节保持为 0
		binary.BigEndian.PutUint32(buf[24:28], ct)
		binary.BigEndian.PutUint32(buf[28:32], uint32(hp))
		binary.BigEndian.PutUint32(buf[32:36], uint32(maxHp))
		binary.BigEndian.PutUint32(buf[36:40], uint32(lv))
		binary.BigEndian.PutUint32(buf[40:44], catchable)
		for i := 44; i < 50; i++ {
			buf[i] = 0
		}
		return buf
	}
	u1 := gs.GetOrCreateUser(inviterUID)
	u2 := gs.GetOrCreateUser(responderUID)
	petID1, lv1, ct1, hp1, maxHp1 := 7, 5, uint32(0), 0, 0
	if len(u1.Pets) > 0 {
		petID1 = u1.Pets[0].ID
		if u1.Pets[0].Level > 0 {
			lv1 = u1.Pets[0].Level
		}
		ct1 = uint32(u1.Pets[0].CatchTime)
		if ct1 == 0 {
			ct1 = 0x69686700 + uint32(petID1)
		}
	}
	ev1 := gamepets.EVStats{}
	if len(u1.Pets) > 0 {
		ev1 = u1.Pets[0].GetEVStats()
	}
	st1 := petMgr.GetStats(petID1, lv1, 31, ev1, 0)
	hp1, maxHp1 = st1.HP, st1.MaxHP
	name1 := petMgr.GetName(petID1)
	if name1 == "" {
		name1 = "精灵"
	}
	petID2, lv2, ct2, hp2, maxHp2 := 7, 5, uint32(0), 0, 0
	if len(u2.Pets) > 0 {
		petID2 = u2.Pets[0].ID
		if u2.Pets[0].Level > 0 {
			lv2 = u2.Pets[0].Level
		}
		ct2 = uint32(u2.Pets[0].CatchTime)
		if ct2 == 0 {
			ct2 = 0x69686700 + uint32(petID2)
		}
	}
	ev2 := gamepets.EVStats{}
	if len(u2.Pets) > 0 {
		ev2 = u2.Pets[0].GetEVStats()
	}
	st2 := petMgr.GetStats(petID2, lv2, 31, ev2, 0)
	hp2, maxHp2 = st2.HP, st2.MaxHP
	name2 := petMgr.GetName(petID2)
	if name2 == "" {
		name2 = "精灵"
	}
	info1 := buildFightPetInfo(uint32(inviterUID), petID1, name1, ct1, hp1, maxHp1, lv1, 0)
	info2 := buildFightPetInfo(uint32(responderUID), petID2, name2, ct2, hp2, maxHp2, lv2, 0)
	// 客户端 FightStartInfo 按首条 userID==MainManager.actorInfo.userID 区分 myInfo/otherInfo；包内顺序 [我方,对方]
	inviterBody = make([]byte, 4+len(info1)+len(info2))
	binary.BigEndian.PutUint32(inviterBody[0:4], 0)
	copy(inviterBody[4:4+len(info1)], info1)
	copy(inviterBody[4+len(info1):], info2)
	responderBody = make([]byte, 4+len(info1)+len(info2))
	binary.BigEndian.PutUint32(responderBody[0:4], 0)
	copy(responderBody[4:4+len(info2)], info2)
	copy(responderBody[4+len(info2):], info1)
	// 日志：验证两段 FightPetInfo 的 userID/petID 不同，便于排查“两边都显示对方模型”
	// 注意：客户端 FightStartInfo 用 actorInfo.userID 区分 myInfo/otherInfo，FighterModeFactory 用 actorID 区分 PlayerMode/EnemyMode
	// 若两者不一致，可能导致 myInfo 被误判为 EnemyMode，出现"两边都显示对方模型"
	inviterFirstUID := binary.BigEndian.Uint32(inviterBody[4:8])
	inviterFirstPetID := binary.BigEndian.Uint32(inviterBody[8:12])
	inviterSecondUID := binary.BigEndian.Uint32(inviterBody[54:58])
	inviterSecondPetID := binary.BigEndian.Uint32(inviterBody[58:62])
	responderFirstUID := binary.BigEndian.Uint32(responderBody[4:8])
	responderFirstPetID := binary.BigEndian.Uint32(responderBody[8:12])
	responderSecondUID := binary.BigEndian.Uint32(responderBody[54:58])
	responderSecondPetID := binary.BigEndian.Uint32(responderBody[58:62])

	// 验证：第一条 userID 必须等于接收方 userID
	if inviterFirstUID != uint32(inviterUID) {
		logger.Warning(fmt.Sprintf("[2504-PvP] ⚠️ 邀请方包第1段 userID(%d) != 接收方UID(%d)，可能导致客户端识别错误！", inviterFirstUID, inviterUID))
	}
	if responderFirstUID != uint32(responderUID) {
		logger.Warning(fmt.Sprintf("[2504-PvP] ⚠️ 接受方包第1段 userID(%d) != 接收方UID(%d)，可能导致客户端识别错误！", responderFirstUID, responderUID))
	}

	logger.Info(fmt.Sprintf("[2504-PvP] 邀请方(UID=%d)包: 第1段 userID=%d petID=%d(name=%s), 第2段 userID=%d petID=%d(name=%s)",
		inviterUID, inviterFirstUID, inviterFirstPetID, name1, inviterSecondUID, inviterSecondPetID, name2))
	logger.Info(fmt.Sprintf("[2504-PvP] 接受方(UID=%d)包: 第1段 userID=%d petID=%d(name=%s), 第2段 userID=%d petID=%d(name=%s)",
		responderUID, responderFirstUID, responderFirstPetID, name2, responderSecondUID, responderSecondPetID, name1))
	return
}

// buildPvP2504BodyForUser 为 PvP 下 2404 构建当前用户的 2504 包体：我方 FightPetInfo + 对方 FightPetInfo（不修改 BattleState）
func buildPvP2504BodyForUser(gs *gameserver.GameServer, myUID, opponentUID int64, battle *gameserver.BattleState) []byte {
	petMgr := gamepets.GetInstance()
	const fightPetInfoSize = 50
	buildFightPetInfo := func(uid uint32, petID int, name string, ct uint32, hp, maxHp, lv int, catchable uint32) []byte {
		if petID <= 0 {
			petID = 7
		}
		if maxHp <= 0 {
			maxHp = 1
		}
		if hp < 0 {
			hp = 0
		}
		if hp > maxHp {
			hp = maxHp
		}
		buf := make([]byte, fightPetInfoSize)
		binary.BigEndian.PutUint32(buf[0:4], uid)
		binary.BigEndian.PutUint32(buf[4:8], uint32(petID))
		nb := []byte(name)
		if len(nb) > 16 {
			nb = nb[:16]
		}
		copy(buf[8:24], nb)
		binary.BigEndian.PutUint32(buf[24:28], ct)
		binary.BigEndian.PutUint32(buf[28:32], uint32(hp))
		binary.BigEndian.PutUint32(buf[32:36], uint32(maxHp))
		binary.BigEndian.PutUint32(buf[36:40], uint32(lv))
		binary.BigEndian.PutUint32(buf[40:44], catchable)
		for i := 44; i < 50; i++ {
			buf[i] = 0
		}
		return buf
	}
	// 我方：当前用户
	uMe := gs.GetOrCreateUser(myUID)
	petID1, lv1, ct1, hp1, maxHp1 := 7, 5, uint32(0), int(battle.PlayerHP), int(battle.PlayerMaxHP)
	if len(uMe.Pets) > 0 {
		petID1 = uMe.Pets[0].ID
		if uMe.Pets[0].Level > 0 {
			lv1 = uMe.Pets[0].Level
		}
		ct1 = uint32(uMe.Pets[0].CatchTime)
		if ct1 == 0 {
			ct1 = 0x69686700 + uint32(petID1)
		}
	}
	name1 := petMgr.GetName(petID1)
	if name1 == "" {
		name1 = "精灵"
	}
	// 对方：对手
	uOpp := gs.GetOrCreateUser(opponentUID)
	petID2, lv2, ct2 := battle.EnemyID, battle.EnemyLevel, uint32(0)
	hp2, maxHp2 := int(battle.EnemyHP), int(battle.EnemyMaxHP)
	if len(uOpp.Pets) > 0 {
		ct2 = uint32(uOpp.Pets[0].CatchTime)
		if ct2 == 0 {
			ct2 = 0x69686700 + uint32(petID2)
		}
	}
	name2 := petMgr.GetName(petID2)
	if name2 == "" {
		name2 = "精灵"
	}
	info1 := buildFightPetInfo(uint32(myUID), petID1, name1, ct1, hp1, maxHp1, lv1, 0)
	info2 := buildFightPetInfo(uint32(opponentUID), petID2, name2, ct2, hp2, maxHp2, lv2, 0)
	// 与 2403 推送的 2504 一致：包内顺序 [我方,对方]
	out := make([]byte, 4+len(info1)+len(info2))
	binary.BigEndian.PutUint32(out[0:4], 0)
	copy(out[4:4+len(info1)], info1)
	copy(out[4+len(info1):], info2)
	// 确保第1段 userID 始终为接收方(myUID)，第2段为对方(opponentUID)，便于客户端正确识别"我方/对方"
	firstUID := binary.BigEndian.Uint32(out[4:8])
	firstPetID := binary.BigEndian.Uint32(out[8:12])
	secondUID := binary.BigEndian.Uint32(out[54:58])
	secondPetID := binary.BigEndian.Uint32(out[58:62])

	// 验证：第一条 userID 必须等于接收方 userID
	if firstUID != uint32(myUID) {
		logger.Warning(fmt.Sprintf("[2504-2404] ⚠️ 包第1段 userID(%d) != 接收方UID(%d)，可能导致客户端识别错误！", firstUID, myUID))
	}

	logger.Info(fmt.Sprintf("[2504-2404] UID=%d 包体: 第1段 userID=%d petID=%d(name=%s), 第2段 userID=%d petID=%d(name=%s)",
		myUID, firstUID, firstPetID, name1, secondUID, secondPetID, name2))
	return out
}

// ==================== 新手战斗（2411 -> 2503） ====================

// 克洛斯星密林(12) SPT/脚本战斗：客户端 fightWithBoss 第二个参数 0=蘑菇怪(47)、1=依依(83)
const (
	mapIDKlothForest  = 12
	petIDMushroomBoss = 47 // 蘑菇怪 SPTBOSS
	petIDYiyi         = 83 // 依依（密林脚本 NPC）
)

// handleChallengeBoss CMD 2411 挑战BOSS（新手战斗触发 / SPT / 地图脚本 BOSS）
// 客户端 FightInviteManager.fightWithBoss(name, param2)：只发送 param2(4)，不发送名字。
// 全地图 SPT 按 sptboss 配置：(mapID, param2) → bossPetID、等级；
// 特殊地图（如勇者之塔神秘领域 514）按前端脚本自定义解析 param2。
func handleChallengeBoss(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	mapID := user.MapID
	if mapID == 0 {
		mapID = 1
	}

	// 谱尼进度（MaxPuniLv）在协议中约定为 0~8，容错修正异常值
	if user.MaxPuniLv < 0 || user.MaxPuniLv > 8 {
		user.MaxPuniLv = 0
	}

	bossID := uint32(13)
	enemyLevel := 5

	param2 := uint32(0)
	if len(ctx.Body) >= 4 {
		param2 = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	// 谱尼真身：部分客户端点击「挑战谱尼真身」时仍发送 param2=0，若已解锁真身(MaxPuniLv>=7)则视为 door=8
	if (mapID == 514 || mapID == 108) && param2 == 0 && user.MaxPuniLv >= 7 {
		param2 = 8
		logger.Info(fmt.Sprintf("[2411] 谱尼 param2=0 且已解锁真身，视为挑战真身 door=8 (MaxPuniLv=%d)", user.MaxPuniLv))
	}
	// 谱尼挑战：实际游戏中 514.swf 绑定在地图 108 上，这里同时兼容 mapID=514 与 mapID=108，
	// 并且仅当 param2 在 1~8（七封印+真身）范围内时才按谱尼门处理，防止普通 108 地图 BOSS 被误判为谱尼。
	isPuniDoor := (mapID == 514 || mapID == 108) && param2 >= 1 && param2 <= 8
	// 中央常驻战（走 SPT 未配置回退）时，若已解锁真身也按真身写 BattleState，供 2404 用
	var puniCentralAsTrueForm bool

	// 1）勇者之塔神秘领域（MapID=514/108）谱尼七封印 / 真身
	// 前端 MapProcess_514 / FightInviteManager.fightWithBoss：
	// - body 写入的是 doorindex(1~7) 或 8（真身），不是 bossID；
	// - 若未解锁对应封印，应返回 11027，让客户端派发 RobotEvent.ERROR_11027；
	// - 成功时前端只关心是否能进入战斗，不解析 2411 回包体内容。
	if isPuniDoor {
		door := int(param2)
		logger.Info(fmt.Sprintf("[2411] 谱尼挑战请求: UID=%d mapID=%d door=%d maxPuniLv=%d", ctx.UserID, mapID, door, user.MaxPuniLv))

		// 未解锁对应封印：返回 11027
		if door >= 1 && door <= 7 {
			requiredLv := door - 1 // 第一封印要求 MaxPuniLv>=0，第二封印>=1，依此类推
			if user.MaxPuniLv < requiredLv {
				ctx.GameServer.SendErrorResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, 11027)
				return
			}
		}
		// 真身门（8）：需要先解锁前 7 道封印
		if door == 8 && user.MaxPuniLv < 7 {
			ctx.GameServer.SendErrorResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, 11027)
			return
		}

		// 暂不在服务端实现每日封印次数限制（11028），保持简单：由客户端基于 dailyResArr 做一次性限制。

		// 将七封印与真身都视为谱尼本体，根据门编号调整等级，保证难度逐步提升。
		bossID = 300 // 谱尼
		switch door {
		case 1:
			enemyLevel = 110
		case 2:
			enemyLevel = 110
		case 3:
			enemyLevel = 110
		case 4:
			enemyLevel = 110
		case 5:
			enemyLevel = 110
		case 6:
			enemyLevel = 110
		case 7:
			enemyLevel = 110
		case 8:
			enemyLevel = 110
		default:
			// 理论上不会出现 0 或 >8，这里兜底为普通谱尼战
			enemyLevel = 110
		}
	} else {
		// 2）普通 SPT / 其它地图 BOSS：按 sptboss 配置与原有回退逻辑处理
		if ctx.CmdID == 2421 {
			logger.Info(fmt.Sprintf("[2421] 盖亚挑战请求: mapID=%d param2=%d bodyLen=%d", mapID, param2, len(ctx.Body)))
		} else {
			logger.Info(fmt.Sprintf("[2411] 收到挑战BOSS请求: mapID=%d param2=%d bodyLen=%d", mapID, param2, len(ctx.Body)))
		}

		if e, ok := sptboss.GetByMapAndParam(mapID, param2); ok {
			bossID = uint32(e.BossPetID)
			enemyLevel = e.Level
			logger.Info(fmt.Sprintf("[2411] 从SPT配置获取BOSS: mapID=%d param2=%d -> bossID=%d level=%d", mapID, param2, bossID, enemyLevel))
		} else if e, ok := sptboss.GetByMapAndParam(mapID, 0); ok {
			// 该地图在 SPT 中但 param2 不匹配（如客户端误传 param2=1）：按单 BOSS 地图回退到 param2=0，避免把 param2 当 bossID 导致变成布布种子等
			bossID = uint32(e.BossPetID)
			enemyLevel = e.Level
			logger.Info(fmt.Sprintf("[2411] SPT param2 未命中，回退到 mapID=%d param2=0 -> bossID=%d level=%d", mapID, bossID, enemyLevel))
		} else {
			logger.Info(fmt.Sprintf("[2411] SPT配置未找到: mapID=%d param2=%d，尝试从body或盖亚回退", mapID, param2))

			// 勇者之塔 514 地图（实际 MapID=108）中央“挑战谱尼”按钮：客户端仍发送 param2=0，
			// 中央挑战固定视为谱尼真身（door=8），与 MaxPuniLv 无关，便于直接挑战真身。
			if mapID == 108 && param2 == 0 {
				bossID = 300
				enemyLevel = 110
				puniCentralAsTrueForm = true
				logger.Info("[2411] 谱尼中央常驻战(真身): mapID=108 param2=0 -> bossID=300 level=110")
			} else if len(ctx.Body) >= 4 {
				// 兼容：body 直接传 bossID（如从 SPT 面板发起时）
				if v := binary.BigEndian.Uint32(ctx.Body[0:4]); v != 0 {
					bossID = v
					enemyLevel = 50
					logger.Info(fmt.Sprintf("[2411] 从body读取BOSS ID: bossID=%d", bossID))
				}
			}
			// 2421(FIGHT_SPECIAL_PET) 为盖亚挑战：未匹配到地图时仍按盖亚处理，保证点击「我接受对战」能进战
			if ctx.CmdID == 2421 && bossID == 13 {
				bossID = petIDGaiya
				enemyLevel = 70
				logger.Info(fmt.Sprintf("[2421] 盖亚挑战回退: bossID=%d level=%d", bossID, enemyLevel))
			}
		}
	}

	// 写入 BattleStates，供 2503/2504 使用正确的敌方等级
	ctx.GameServer.BattleMu.Lock()
	battle, ok := ctx.GameServer.BattleStates[ctx.UserID]
	if !ok || battle == nil {
		battle = &gameserver.BattleState{}
	}
	// 勇者之塔神秘领域谱尼战：记录本次挑战对应的门索引，便于战斗过程与结束后更新 MaxPuniLv
	if isPuniDoor {
		door := int(param2)
		battle.PuniDoorIndex = door
		// 初始化谱尼扩展状态
		battle.PuniElementUnlocked = false
		battle.PuniEnergyDamageThisTurn = 0
		battle.PuniLifeLastHitDamage = 0
		battle.PuniCycleHPBar = 1
		battle.PuniEternalActive = false
		battle.PuniHolyActive = false
		battle.PuniTrueFormLifeIndex = 0
		battle.PuniTrueFormLastLifeHealed = false
		// door=8 视为真身战，从第一条命开始
		if door == 8 {
			battle.PuniTrueFormLifeIndex = 1
		}
	} else {
		battle.PuniDoorIndex = 0
		// 谱尼中央常驻战（SPT 未配置回退）：固定按真身写，2404 会据此用真身血量
		if puniCentralAsTrueForm && bossID == 300 {
			battle.PuniDoorIndex = 8
			battle.PuniTrueFormLifeIndex = 1
			battle.PuniElementUnlocked = false
			battle.PuniEnergyDamageThisTurn = 0
			battle.PuniLifeLastHitDamage = 0
			battle.PuniCycleHPBar = 1
			battle.PuniEternalActive = false
			battle.PuniHolyActive = false
			battle.PuniTrueFormLastLifeHealed = false
		}
	}
	battle.EnemyID = int(bossID)
	battle.EnemyLevel = enemyLevel
	battle.IsActive = true
	battle.BattleMapID = mapID
	battle.RoundCount = 0
	battle.LastHitWasCrit = false
	battle.IsBossChallenge = true // 仅 2411/2421 发起的 BOSS 挑战才在 2405 发放对应 BOSS 精元/精灵奖励
	ctx.GameServer.BattleStates[ctx.UserID] = battle
	ctx.GameServer.BattleMu.Unlock()

	body := buildNoteReadyToFightInfo(ctx, bossID)
	// 2421：用请求的 SEQ 回 2503，便于客户端把 2503 当作本次请求的响应并进入对战
	ctx.GameServer.SendResponse(ctx.ClientData, 2503, ctx.UserID, ctx.SeqID, body)
}

// buildNoteReadyToFightInfo 构建 2503 回包（最小可用版本）
// body: userCount(4) + [FighetUserInfo(20) + petCount(4) + SimplePetInfo] * 2
// 客户端用 2503 的 petArray（来自 SimplePetInfo 的 petId）预加载对战模型，petID 必须与 2504 一致且非 0
func buildNoteReadyToFightInfo(ctx *gameserver.HandlerContext, enemyID uint32) []byte {
	if enemyID == 0 {
		enemyID = 13
	}
	userID := uint32(ctx.UserID)
	petMgr := gamepets.GetInstance()
	skillMgr := gameskills.GetInstance()

	// 玩家使用实际第一只出战精灵，与 2504 一致，否则客户端预加载错误模型导致对战无模型
	playerPetID := 7
	playerLevel := 5
	playerCatch := uint32(0x69686700 + uint32(playerPetID))
	playerNature := 0
	playerEV := gamepets.EVStats{}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if len(user.Pets) > 0 {
		playerPetID = user.Pets[0].ID
		if user.Pets[0].Level > 0 {
			playerLevel = user.Pets[0].Level
		}
		playerCatch = uint32(user.Pets[0].CatchTime)
		playerNature = user.Pets[0].Nature
		playerEV = user.Pets[0].GetEVStats()
	}
	if playerCatch == 0 {
		playerCatch = 0x69686700 + uint32(playerPetID)
	}
	logger.Info(fmt.Sprintf("[2503] NoteReadyToFight: playerPetID=%d playerLevel=%d enemyID=%d (与2504一致才能正确显示对战)", playerPetID, playerLevel, enemyID))

	playerStats := petMgr.GetStats(playerPetID, playerLevel, 31, playerEV, playerNature)
	playerSkillsRaw := petMgr.GetSkillsForLevel(playerPetID, playerLevel)
	playerSkills := make([][2]uint32, 0, 4)
	for _, sid := range playerSkillsRaw {
		if sid <= 0 {
			continue
		}
		pp := uint32(20)
		if sk := skillMgr.Get(sid); sk != nil {
			if sk.PP > 0 {
				pp = uint32(sk.PP)
			} else if sk.MaxPP > 0 {
				pp = uint32(sk.MaxPP)
			}
		}
		playerSkills = append(playerSkills, [2]uint32{uint32(sid), pp})
		if len(playerSkills) >= 4 {
			break
		}
	}
	if len(playerSkills) == 0 {
		playerSkills = append(playerSkills, [2]uint32{10001, 20})
	}

	// 敌人属性（敌人默认性格0，EV 为 0）；2411 已写入 BattleStates.EnemyLevel（如密林蘑菇怪10级、依依5级）
	enemyLevel := 5
	ctx.GameServer.BattleMu.RLock()
	if battle, ok := ctx.GameServer.BattleStates[ctx.UserID]; ok && battle.IsActive && battle.EnemyLevel > 0 {
		enemyLevel = battle.EnemyLevel
	}
	ctx.GameServer.BattleMu.RUnlock()
	enemyEV := gamepets.EVStats{} // 敌人 EV 默认为 0
	enemyStats := petMgr.GetStats(int(enemyID), enemyLevel, 15, enemyEV, 0)
	// 地图 BOSS 使用固定血量（只影响敌方战斗体力，不改种族值与玩家拥有的同名精灵）
	enemyStats.HP = applyBossHPOverride(int(enemyID), enemyStats.HP)
	enemyStats.MaxHP = applyBossHPOverride(int(enemyID), enemyStats.MaxHP)
	// 试炼之塔倍率：若当前玩家在试炼之塔中（CurFreshStage>0 且有对应配置），按 GM 配置倍率缩放敌人属性
	if user.CurFreshStage > 0 {
		if entry, ok := GetFreshFightEntry(user.CurFreshStage, 1); ok && entry.BossID == int(enemyID) {
			scaleStatsByEntry(&enemyStats, entry)
		}
	}
	// 使用 getEnemySkillsForPet 以支持 BOSS 技能覆盖（如闪光波克尔的"全力一击"）
	enemySkillsRaw := getEnemySkillsForPet(int(enemyID), enemyLevel)
	enemySkills := make([][2]uint32, 0, 4)
	enemyPPInfinite := sptboss.IsInfinitePPBoss(int(enemyID))
	for _, sid := range enemySkillsRaw {
		if sid <= 0 {
			continue
		}
		pp := uint32(20)
		if enemyPPInfinite {
			pp = 99
		} else if sk := skillMgr.Get(sid); sk != nil {
			if sk.PP > 0 {
				pp = uint32(sk.PP)
			} else if sk.MaxPP > 0 {
				pp = uint32(sk.MaxPP)
			}
		}
		enemySkills = append(enemySkills, [2]uint32{uint32(sid), pp})
		if len(enemySkills) >= 4 {
			break
		}
	}
	if len(enemySkills) == 0 {
		defaultPP := uint32(20)
		if enemyPPInfinite {
			defaultPP = 99
		}
		enemySkills = append(enemySkills, [2]uint32{10001, defaultPP})
	}

	buildFightUserInfo := func(uid uint32, nick string) []byte {
		b := make([]byte, 20)
		binary.BigEndian.PutUint32(b[0:4], uid)
		nb := []byte(nick)
		if len(nb) > 16 {
			nb = nb[:16]
		}
		copy(b[4:20], nb)
		return b
	}

	buildSimplePetInfo := func(petID uint32, level uint32, hp uint32, maxHp uint32, catchTime uint32, skills [][2]uint32) []byte {
		// 结构对齐 Lua buildSimplePetInfo:
		// petId(4)+level(4)+hp(4)+maxHp(4)+skillNum(4)+[id(4)+pp(4)]*4+catchTime(4)+catchMap(4)+catchRect(4)+catchLevel(4)+skinID(4)
		b := make([]byte, 72)
		binary.BigEndian.PutUint32(b[0:4], petID)
		binary.BigEndian.PutUint32(b[4:8], level)
		binary.BigEndian.PutUint32(b[8:12], hp)
		binary.BigEndian.PutUint32(b[12:16], maxHp)
		// skillNum = 有效技能数
		valid := uint32(0)
		for _, s := range skills {
			if s[0] != 0 {
				valid++
			}
		}
		binary.BigEndian.PutUint32(b[16:20], valid)
		off := 20
		for i := 0; i < 4; i++ {
			var sid, pp uint32
			if i < len(skills) {
				sid, pp = skills[i][0], skills[i][1]
			}
			binary.BigEndian.PutUint32(b[off:off+4], sid)
			binary.BigEndian.PutUint32(b[off+4:off+8], pp)
			off += 8
		}
		binary.BigEndian.PutUint32(b[52:56], catchTime)
		binary.BigEndian.PutUint32(b[56:60], 0) // catchMap = 0（Lua 注释：官服为0）
		binary.BigEndian.PutUint32(b[60:64], 0) // catchRect
		binary.BigEndian.PutUint32(b[64:68], 0) // catchLevel = 0（官服为0）
		binary.BigEndian.PutUint32(b[68:72], petID) // skinID：客户端用此加载精灵模型/图标，0 会导致敌方切换时蓝格占位、模型不切换
		return b
	}

	// 拼装 body
	// CRITICAL FIX: 发送玩家所有精灵的信息，而不仅仅是第一只
	// 这样 _petInfoMap 中会包含所有精灵，切换时不会出现空指针异常
	// 盖亚(261)对战：仅允许单精灵出战，只发一只
	playerPetCount := len(user.Pets)
	if enemyID == petIDGaiya {
		playerPetCount = 1
	}
	if playerPetCount == 0 {
		playerPetCount = 1 // 至少发送一只默认精灵
	}
	out := make([]byte, 0, 4+20+4+72*playerPetCount+20+4+72)
	tmp4 := make([]byte, 4)
	binary.BigEndian.PutUint32(tmp4, 2)
	out = append(out, tmp4...)

	// Player - 发送所有精灵
	out = append(out, buildFightUserInfo(userID, "Seer")...)
	binary.BigEndian.PutUint32(tmp4, uint32(playerPetCount))
	out = append(out, tmp4...)

	if len(user.Pets) > 0 {
		petsToSend := user.Pets
		if enemyID == petIDGaiya {
			petsToSend = user.Pets[:1] // 盖亚单精灵
		}
		for _, pet := range petsToSend {
			petID := pet.ID
			if petID <= 0 {
				petID = 7
			}
			petLevel := pet.Level
			if petLevel <= 0 {
				petLevel = 5
			}
			petCatch := uint32(pet.CatchTime)
			if petCatch == 0 {
				petCatch = 0x69686700 + uint32(petID)
			}
			petEV := pet.GetEVStats()
			petStats := petMgr.GetStats(petID, petLevel, pet.DV, petEV, pet.Nature)

			// 获取精灵技能
			var petSkillsRaw []int
			if len(pet.Skills) > 0 {
				petSkillsRaw = pet.Skills
			} else {
				petSkillsRaw = petMgr.GetSkillsForLevel(petID, petLevel)
			}
			petSkills := make([][2]uint32, 0, 4)
			for _, sid := range petSkillsRaw {
				if sid <= 0 {
					continue
				}
				pp := uint32(20)
				if sk := skillMgr.Get(sid); sk != nil {
					if sk.PP > 0 {
						pp = uint32(sk.PP)
					} else if sk.MaxPP > 0 {
						pp = uint32(sk.MaxPP)
					}
				}
				petSkills = append(petSkills, [2]uint32{uint32(sid), pp})
				if len(petSkills) >= 4 {
					break
				}
			}
			if len(petSkills) == 0 {
				petSkills = append(petSkills, [2]uint32{10001, 20})
			}

			out = append(out, buildSimplePetInfo(uint32(petID), uint32(petLevel), uint32(petStats.HP), uint32(petStats.MaxHP), petCatch, petSkills)...)
		}
	} else {
		// 没有精灵时发送默认精灵
		out = append(out, buildSimplePetInfo(uint32(playerPetID), uint32(playerLevel), uint32(playerStats.HP), uint32(playerStats.MaxHP), playerCatch, playerSkills)...)
	}

	// Enemy：仅一条，catchTime=0；客户端 _petInfoMap 以 catchTime 为 key，2504/2407 敌方均为 catchTime=0，故此处 skinID=petID 保证 updateEnemyFromOtherInfo/changePet 时模型正确
	out = append(out, buildFightUserInfo(0, "")...)
	binary.BigEndian.PutUint32(tmp4, 1)
	out = append(out, tmp4...)
	out = append(out, buildSimplePetInfo(enemyID, uint32(enemyLevel), uint32(enemyStats.HP), uint32(enemyStats.MaxHP), 0, enemySkills)...)

	logger.Info(fmt.Sprintf("[2503] NoteReadyToFight: 发送玩家精灵数=%d 敌人ID=%d", playerPetCount, enemyID))
	return out
}

// initPlayerSkillPP 在 BattleState 中初始化当前出战精灵的技能与 PP（首次或换宠时使用）
func initPlayerSkillPP(battle *gameserver.BattleState, user *userdb.GameData, activeIdx int, petMgr *gamepets.Pets, skillMgr *gameskills.Skills) {
	// 若 BattleState 中已有技能 ID，则认为本场/本只精灵已初始化
	hasIDs := false
	for i := 0; i < 4; i++ {
		if battle.PlayerSkillIDs[i] != 0 {
			hasIDs = true
			break
		}
	}
	if hasIDs {
		return
	}
	if user == nil || activeIdx < 0 || activeIdx >= len(user.Pets) {
		return
	}
	p := &user.Pets[activeIdx]
	petID := p.ID
	if petID <= 0 {
		petID = 7
	}
	level := p.Level
	if level <= 0 {
		level = 5
	}
	ev := p.GetEVStats()
	stats := petMgr.GetStats(petID, level, p.DV, ev, p.Nature)
	battle.PlayerMaxHP = uint32(stats.MaxHP)
	if battle.PlayerHP == 0 {
		battle.PlayerHP = uint32(stats.HP)
	}

	var rawSkills []int
	if len(p.Skills) > 0 {
		rawSkills = p.Skills
	} else {
		rawSkills = petMgr.GetSkillsForLevel(petID, level)
	}
	for i := 0; i < 4; i++ {
		battle.PlayerSkillIDs[i] = 0
		battle.PlayerSkillPP[i] = 0
	}
	idx := 0
	for _, sid := range rawSkills {
		if sid <= 0 {
			continue
		}
		if idx >= 4 {
			break
		}
		pp := 20
		if sk := skillMgr.Get(sid); sk != nil {
			if sk.PP > 0 {
				pp = sk.PP
			} else if sk.MaxPP > 0 {
				pp = sk.MaxPP
			}
		}
		if pp <= 0 {
			pp = 20
		}
		if pp > 255 {
			pp = 255
		}
		battle.PlayerSkillIDs[idx] = uint32(sid)
		battle.PlayerSkillPP[idx] = byte(pp)
		idx++
	}
}

// initEnemySkillPP 在 BattleState 中初始化敌方技能与 PP（首次使用 2405 时调用）
func initEnemySkillPP(battle *gameserver.BattleState, enemyID, enemyLevel int, skillMgr *gameskills.Skills) {
	hasIDs := false
	for i := 0; i < 4; i++ {
		if battle.EnemySkillIDs[i] != 0 {
			hasIDs = true
			break
		}
	}
	if hasIDs {
		return
	}
	if enemyID <= 0 {
		return
	}
	if enemyLevel <= 0 {
		enemyLevel = 5
	}
	raw := getEnemySkillsForPet(enemyID, enemyLevel)
	infinite := sptboss.IsInfinitePPBoss(enemyID)
	for i := 0; i < 4; i++ {
		battle.EnemySkillIDs[i] = 0
		battle.EnemySkillPP[i] = 0
	}
	idx := 0
	for _, sid := range raw {
		if sid <= 0 {
			continue
		}
		if idx >= 4 {
			break
		}
		pp := 20
		if infinite {
			pp = 99
		} else if sk := skillMgr.Get(sid); sk != nil {
			if sk.PP > 0 {
				pp = sk.PP
			} else if sk.MaxPP > 0 {
				pp = sk.MaxPP
			}
		}
		if pp <= 0 {
			pp = 20
		}
		if pp > 255 {
			pp = 255
		}
		battle.EnemySkillIDs[idx] = uint32(sid)
		battle.EnemySkillPP[idx] = byte(pp)
		idx++
	}
	battle.EnemyPPInfinite = infinite
}

// buildNoteReadyToFightInfoTower 多敌方战斗通用 2503：敌方发送本层全部 Boss（catchTime=0,1,2...），供勇者之塔(2415)与试炼之塔(2429)使用。
// 切换时不再发 2503，避免客户端重载 PetFightDLL 导致 2504 进错上下文；切换时只发 2504+2505，otherInfo.catchTime=当前 Boss 索引，客户端 _petInfoMap.getValue(catchTime) 取正确 skinID。
func buildNoteReadyToFightInfoTower(ctx *gameserver.HandlerContext, bossIDs []int) []byte {
	if len(bossIDs) == 0 {
		return buildNoteReadyToFightInfo(ctx, 13)
	}
	userID := uint32(ctx.UserID)
	petMgr := gamepets.GetInstance()
	skillMgr := gameskills.GetInstance()
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	playerPetID := 7
	playerLevel := 5
	playerCatch := uint32(0x69686700 + 7)
	playerNature := 0
	playerEV := gamepets.EVStats{}
	if len(user.Pets) > 0 {
		playerPetID = user.Pets[0].ID
		if user.Pets[0].Level > 0 {
			playerLevel = user.Pets[0].Level
		}
		playerCatch = uint32(user.Pets[0].CatchTime)
		playerNature = user.Pets[0].Nature
		playerEV = user.Pets[0].GetEVStats()
	}
	if playerCatch == 0 {
		playerCatch = 0x69686700 + uint32(playerPetID)
	}
	enemyLevel := 5
	ctx.GameServer.BattleMu.RLock()
	if battle, ok := ctx.GameServer.BattleStates[ctx.UserID]; ok && battle.IsActive && battle.EnemyLevel > 0 {
		enemyLevel = battle.EnemyLevel
	}
	ctx.GameServer.BattleMu.RUnlock()

	buildFightUserInfo := func(uid uint32, nick string) []byte {
		b := make([]byte, 20)
		binary.BigEndian.PutUint32(b[0:4], uid)
		nb := []byte(nick)
		if len(nb) > 16 {
			nb = nb[:16]
		}
		copy(b[4:20], nb)
		return b
	}
	buildSimplePetInfo := func(petID uint32, level uint32, hp uint32, maxHp uint32, catchTime uint32, skills [][2]uint32) []byte {
		b := make([]byte, 72)
		binary.BigEndian.PutUint32(b[0:4], petID)
		binary.BigEndian.PutUint32(b[4:8], level)
		binary.BigEndian.PutUint32(b[8:12], hp)
		binary.BigEndian.PutUint32(b[12:16], maxHp)
		valid := uint32(0)
		for _, s := range skills {
			if s[0] != 0 {
				valid++
			}
		}
		binary.BigEndian.PutUint32(b[16:20], valid)
		off := 20
		for i := 0; i < 4; i++ {
			var sid, pp uint32
			if i < len(skills) {
				sid, pp = skills[i][0], skills[i][1]
			}
			binary.BigEndian.PutUint32(b[off:off+4], sid)
			binary.BigEndian.PutUint32(b[off+4:off+8], pp)
			off += 8
		}
		binary.BigEndian.PutUint32(b[52:56], catchTime)
		binary.BigEndian.PutUint32(b[56:60], 0)
		binary.BigEndian.PutUint32(b[60:64], 0)
		binary.BigEndian.PutUint32(b[64:68], 0)
		binary.BigEndian.PutUint32(b[68:72], petID)
		return b
	}

	playerStats := petMgr.GetStats(playerPetID, playerLevel, 31, playerEV, playerNature)
	playerSkillsRaw := petMgr.GetSkillsForLevel(playerPetID, playerLevel)
	playerSkills := make([][2]uint32, 0, 4)
	for _, sid := range playerSkillsRaw {
		if sid <= 0 {
			continue
		}
		pp := uint32(20)
		if sk := skillMgr.Get(sid); sk != nil {
			if sk.PP > 0 {
				pp = uint32(sk.PP)
			} else if sk.MaxPP > 0 {
				pp = uint32(sk.MaxPP)
			}
		}
		playerSkills = append(playerSkills, [2]uint32{uint32(sid), pp})
		if len(playerSkills) >= 4 {
			break
		}
	}
	if len(playerSkills) == 0 {
		playerSkills = append(playerSkills, [2]uint32{10001, 20})
	}

	playerPetCount := len(user.Pets)
	if playerPetCount == 0 {
		playerPetCount = 1
	}
	out := make([]byte, 0, 4+20+4+72*playerPetCount+20+4+72*len(bossIDs))
	tmp4 := make([]byte, 4)
	binary.BigEndian.PutUint32(tmp4, 2)
	out = append(out, tmp4...)
	out = append(out, buildFightUserInfo(userID, "Seer")...)
	binary.BigEndian.PutUint32(tmp4, uint32(playerPetCount))
	out = append(out, tmp4...)
	if len(user.Pets) > 0 {
		for _, pet := range user.Pets {
			pid := pet.ID
			if pid <= 0 {
				pid = 7
			}
			petLevel := pet.Level
			if petLevel <= 0 {
				petLevel = 5
			}
			petCatch := uint32(pet.CatchTime)
			if petCatch == 0 {
				petCatch = 0x69686700 + uint32(pid)
			}
			petEV := pet.GetEVStats()
			petStats := petMgr.GetStats(pid, petLevel, pet.DV, petEV, pet.Nature)
			petSkillsRaw := petMgr.GetSkillsForLevel(pid, petLevel)
			if len(pet.Skills) > 0 {
				petSkillsRaw = pet.Skills
			}
			petSkills := make([][2]uint32, 0, 4)
			for _, sid := range petSkillsRaw {
				if sid <= 0 {
					continue
				}
				pp := uint32(20)
				if sk := skillMgr.Get(sid); sk != nil {
					if sk.PP > 0 {
						pp = uint32(sk.PP)
					} else if sk.MaxPP > 0 {
						pp = uint32(sk.MaxPP)
					}
				}
				petSkills = append(petSkills, [2]uint32{uint32(sid), pp})
				if len(petSkills) >= 4 {
					break
				}
			}
			if len(petSkills) == 0 {
				petSkills = append(petSkills, [2]uint32{10001, 20})
			}
			out = append(out, buildSimplePetInfo(uint32(pid), uint32(petLevel), uint32(petStats.HP), uint32(petStats.MaxHP), petCatch, petSkills)...)
		}
	} else {
		out = append(out, buildSimplePetInfo(uint32(playerPetID), uint32(playerLevel), uint32(playerStats.HP), uint32(playerStats.MaxHP), playerCatch, playerSkills)...)
	}

	out = append(out, buildFightUserInfo(0, "")...)
	binary.BigEndian.PutUint32(tmp4, uint32(len(bossIDs)))
	out = append(out, tmp4...)
	enemyEV := gamepets.EVStats{}
	var towerLevelForScale int
	ctx.GameServer.BattleMu.RLock()
	if b, ok := ctx.GameServer.BattleStates[ctx.UserID]; ok && b != nil && b.TowerLevel > 0 {
		towerLevelForScale = b.TowerLevel
	}
	ctx.GameServer.BattleMu.RUnlock()
	for i, eid := range bossIDs {
		if eid <= 0 {
			eid = 13
		}
		estats := petMgr.GetStats(eid, enemyLevel, 15, enemyEV, 0)
		estats.HP = applyBossHPOverride(eid, estats.HP)
		estats.MaxHP = applyBossHPOverride(eid, estats.MaxHP)
		if towerLevelForScale > 0 {
			ScaleFightLevelStats(&estats, towerLevelForScale)
		}
		eskillsRaw := getEnemySkillsForPet(eid, enemyLevel)
		eskills := make([][2]uint32, 0, 4)
		for _, sid := range eskillsRaw {
			if sid <= 0 {
				continue
			}
			pp := uint32(20)
			if sptboss.IsInfinitePPBoss(eid) {
				pp = 99
			} else if sk := skillMgr.Get(sid); sk != nil {
				if sk.PP > 0 {
					pp = uint32(sk.PP)
				} else if sk.MaxPP > 0 {
					pp = uint32(sk.MaxPP)
				}
			}
			eskills = append(eskills, [2]uint32{uint32(sid), pp})
			if len(eskills) >= 4 {
				break
			}
		}
		if len(eskills) == 0 {
			eskills = append(eskills, [2]uint32{10001, 20})
		}
		out = append(out, buildSimplePetInfo(uint32(eid), uint32(enemyLevel), uint32(estats.HP), uint32(estats.MaxHP), uint32(i), eskills)...)
	}
	logger.Info(fmt.Sprintf("[2503] NoteReadyToFightTower: 玩家精灵数=%d 本层Boss数=%d bossIDs=%v", playerPetCount, len(bossIDs), bossIDs))
	return out
}

// ==================== READY_TO_FIGHT (2404) ====================

// handleReadyToFight CMD 2404 战斗初始化
// 对齐 Lua: fight_handlers.handleReadyToFight
// PvP：若已有 BattleState 且 OpponentUserID != 0，仅回 2504（我方/对方顺序）+ 2301，不覆盖状态，否则 2505/2506 无法发给对方。
// PvE：在服务端记录战斗状态，向客户端发送 2504 + 2301。
func handleReadyToFight(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// PvP 已由 2403 推送 2503+2504 并 setPvPBattleStates；客户端仍会发 2404 确认。此时不可覆盖 BattleState，否则 OpponentUserID 丢失，2505/2506 无法发给对方。
	ctx.GameServer.BattleMu.RLock()
	battle, pvpExists := ctx.GameServer.BattleStates[ctx.UserID]
	if pvpExists && battle.IsActive && battle.OpponentUserID != 0 {
		opponentUID := battle.OpponentUserID
		ctx.GameServer.BattleMu.RUnlock()
		// 仅回 2504（我方第一条、对方第二条）+ 2301，不写 BattleState
		body2504 := buildPvP2504BodyForUser(ctx.GameServer, ctx.UserID, opponentUID, battle)
		if len(body2504) > 0 {
			ctx.GameServer.SendResponse(ctx.ClientData, 2504, ctx.UserID, ctx.SeqID, body2504)
		}
		if len(user.Pets) > 0 {
			petInfoBody := buildFullPetInfo(user.Pets[0])
			ctx.GameServer.SendResponse(ctx.ClientData, 2301, ctx.UserID, ctx.SeqID, petInfoBody)
		}
		logger.Info(fmt.Sprintf("[2404] PvP 已就绪: UID=%d 仅回 2504+2301，不覆盖 BattleState", ctx.UserID))
		return
	}
	ctx.GameServer.BattleMu.RUnlock()

	// 选择玩家出战精灵：优先第一只
	playerPetID := 7
	playerLevel := 5
	var catchTime uint32
	if len(user.Pets) > 0 {
		playerPetID = user.Pets[0].ID
		if user.Pets[0].Level > 0 {
			playerLevel = user.Pets[0].Level
		}
		catchTime = uint32(user.Pets[0].CatchTime)
	}
	if catchTime == 0 {
		catchTime = 0x69686700 + uint32(playerPetID)
	}

	// 玩家属性（获取玩家精灵的性格，默认0）
	petMgr := gamepets.GetInstance()
	playerNature := 0
	playerEV := gamepets.EVStats{}
	if len(user.Pets) > 0 {
		playerNature = user.Pets[0].Nature
		playerEV = user.Pets[0].GetEVStats()
	}
	playerStats := petMgr.GetStats(playerPetID, playerLevel, 31, playerEV, playerNature)

	// 敌人 ID/等级：优先取上一次 2408 战斗初始化时记录在 BattleStates 里的 EnemyID，
	// 如果还没有（例如新手 2411 战斗），则回退到默认比比鼠。
	bossID := uint32(13)
	enemyLevel := 5
	enemyEV := gamepets.EVStats{} // 敌人 EV 默认为 0

	ctx.GameServer.BattleMu.RLock()
	if battle, ok := ctx.GameServer.BattleStates[ctx.UserID]; ok && battle.IsActive && battle.EnemyID > 0 {
		bossID = uint32(battle.EnemyID)
		if battle.EnemyLevel > 0 {
			enemyLevel = battle.EnemyLevel
		}
	}
	ctx.GameServer.BattleMu.RUnlock()

	// 兜底：若客户端未先发 2411/2408（或 BattleState 未写入），在“有地图 BOSS 配置”的地图上自动选择该地图默认 BOSS（param2=0）
	// 典型场景：哈莫雷特等 SPT BOSS 地图，客户端直接发 2404，导致落到默认比比鼠(13)。
	mapID := user.MapID
	if mapID == 0 {
		mapID = 1
	}
	if bossID == 13 {
		// 勇者之塔地图 500：按当前层数 + 本层已打到第几只(TowerBossIndex) 取当前只 Boss，保证本层 3 只顺序上场
		if mapID == 500 {
			curLevel := user.CurStage
			if curLevel <= 0 {
				curLevel = 1
			}
			if curLevel > fightLevelMaxLevel {
				curLevel = fightLevelMaxLevel
			}
			towerBossIDs := GetFightLevelBossIDsForLevel(curLevel)
			if len(towerBossIDs) > 0 {
				idx := user.TowerBossIndex
				if idx < 0 || idx >= len(towerBossIDs) {
					idx = 0
				}
				bossID = uint32(towerBossIDs[idx])
				enemyLevel = 10 + curLevel
				if enemyLevel < 1 {
					enemyLevel = 1
				}
				if entry, ok := GetFightLevelEntry(curLevel); ok && entry.EnemyLv > 0 {
					enemyLevel = entry.EnemyLv
				}
				ctx.GameServer.BattleMu.Lock()
				towerBattle, _ := ctx.GameServer.BattleStates[ctx.UserID]
				if towerBattle == nil {
					towerBattle = &gameserver.BattleState{}
				}
				towerBattle.EnemyID = int(bossID)
				towerBattle.EnemyLevel = enemyLevel
				towerBattle.IsActive = true
				towerBattle.TowerLevel = curLevel
				towerBattle.TowerBossIndex = idx
				towerBattle.BattleMapID = 500
				ctx.GameServer.BattleStates[ctx.UserID] = towerBattle
				ctx.GameServer.BattleMu.Unlock()
				logger.Info(fmt.Sprintf("[2404] 勇者之塔兜底: mapID=500 curLevel=%d 第%d只 -> bossID=%d level=%d", curLevel, idx+1, bossID, enemyLevel))
			}
		}
		if bossID == 13 && mapID != 500 {
			if e, ok := sptboss.GetByMapAndParam(mapID, 0); ok && e.BossPetID > 0 {
				bossID = uint32(e.BossPetID)
				enemyLevel = e.Level
				logger.Info(fmt.Sprintf("[2404] 未找到 BattleState 敌人信息，回退到地图BOSS: mapID=%d param2=0 -> bossID=%d level=%d", mapID, bossID, enemyLevel))
			}
		}
	}

	// 确保敌方 petID 非 0，否则客户端无法加载模型（PetAssetsManager 依赖非零 ID）
	if bossID == 0 {
		bossID = 13
	}
	enemyStats := petMgr.GetStats(int(bossID), enemyLevel, 15, enemyEV, 0)

	// 地图 BOSS 固定血量：只影响敌方战斗体力，不改种族值与玩家拥有的同名精灵
	enemyStats.HP = applyBossHPOverride(int(bossID), enemyStats.HP)
	enemyStats.MaxHP = applyBossHPOverride(int(bossID), enemyStats.MaxHP)

	// 特殊：克洛斯星 BOSS 闪光波克尔（mapID=10, petID=166）固定血量 2000（沿用原逻辑）
	if int(bossID) == 166 && user.MapID == 10 {
		enemyStats.HP = 2000
		enemyStats.MaxHP = 2000
	}
	// 勇者之塔：按 GM 配置的该层属性倍率缩放敌方六维
	if mapID == 500 {
		towerLv := user.CurStage
		if towerLv <= 0 {
			towerLv = 1
		}
		if towerLv > fightLevelMaxLevel {
			towerLv = fightLevelMaxLevel
		}
		ScaleFightLevelStats(&enemyStats, towerLv)
	}

	// SPTBOSS 开局自带强化：攻击等级 +2（盖亚与其他 BOSS 一致；盖亚伤害翻倍在 2405 敌方伤害计算处单独乘 2）
	// 这里同时写入 2504 的 battleLv 和 BattleState.EnemyBattleLv，保证客户端显示与服务端计算一致。
	initialEnemyBattleLv := [6]int8{}
	if _, ok := sptboss.GetByPetID(int(bossID)); ok {
		initialEnemyBattleLv[0] = 2
	}

	// FightPetInfo: userID(4)+petID(4)+petName(16)+catchTime(4)+hp(4)+maxHP(4)+lv(4)+catchable(4)+battleLv(6)
	// 客户端用 petID 从 PetAssetsManager.getAssetsByID(petID) 取对战模型，petID 必须与 2503 中一致且非 0
	buildFightPetInfo := func(uid uint32, petID int, name string, ct uint32, hp, maxHP, lv int, catchable uint32, battleLv [6]int8) []byte {
		if petID <= 0 {
			petID = 7
		}
		if maxHP <= 0 {
			maxHP = 1
		}
		if hp < 0 {
			hp = 0
		}
		if hp > maxHP {
			hp = maxHP
		}
		buf := make([]byte, 4+4+16+4+4+4+4+4+6)
		off := 0
		binary.BigEndian.PutUint32(buf[off:off+4], uid)
		off += 4
		binary.BigEndian.PutUint32(buf[off:off+4], uint32(petID))
		off += 4
		nb := []byte(name)
		if len(nb) > 16 {
			nb = nb[:16]
		}
		copy(buf[off:off+16], nb)
		off += 16
		binary.BigEndian.PutUint32(buf[off:off+4], ct)
		off += 4
		binary.BigEndian.PutUint32(buf[off:off+4], uint32(hp))
		off += 4
		binary.BigEndian.PutUint32(buf[off:off+4], uint32(maxHP))
		off += 4
		binary.BigEndian.PutUint32(buf[off:off+4], uint32(lv))
		off += 4
		binary.BigEndian.PutUint32(buf[off:off+4], catchable)
		off += 4
		for i := 0; i < 6; i++ {
			buf[off+i] = byte(uint8(battleLv[i]))
		}
		off += 6
		return buf[:off]
	}

	// 使用 gamepets 中的精灵中文名，便于客户端显示；客户端也会用 petID 加载模型
	playerName := petMgr.GetName(playerPetID)
	if playerName == "" {
		playerName = "精灵"
	}
	enemyName := petMgr.GetName(int(bossID))
	if enemyName == "" {
		enemyName = "野生精灵"
	}

	// SPT BOSS 不可捕捉：敌方为 sptboss 时 catchable=0，客户端不显示捕捉按钮
	enemyCatchable := uint32(1)
	if _, ok := sptboss.GetByPetID(int(bossID)); ok {
		enemyCatchable = 0
	}

	// 谱尼七封印 / 真身：先按门号计算 customEnemyHP，再用于 2504 显示与 BattleState，保证血条不溢出
	customEnemyHP := enemyStats.HP
	customEnemyMaxHP := enemyStats.MaxHP
	var puniDoorForState, puniLifeForState int // 用于写入 BattleState，供 2405 等使用
	if (mapID == 108 || mapID == 514) && bossID == 300 {
		ctx.GameServer.BattleMu.Lock()
		door := 0
		life := 0
		if prev, ok := ctx.GameServer.BattleStates[ctx.UserID]; ok && prev != nil {
			door = prev.PuniDoorIndex
			life = prev.PuniTrueFormLifeIndex
		}
		// 中央挑战（未发 2411 或 param2=0）：固定按真身第一条命处理，与 MaxPuniLv 无关
		if door == 0 {
			door = 8
			life = 1
			logger.Info("[2404] 谱尼中央挑战按真身处理 door=8")
		}
		puniDoorForState = door
		if door == 8 && life <= 0 {
			puniLifeForState = 1
		} else {
			puniLifeForState = life
		}
		if door >= 1 && door <= 8 {
			if cfg, ok := sptboss.GetPuniSealConfig(door); ok {
				if door == 8 {
					// 真身：按当前命条获取血量（使用上方已算好的 life，含中央挑战兜底 life=1）
					lifeIdx := life
					if lifeIdx <= 0 {
						lifeIdx = 1
					}
					for _, lifeCfg := range cfg.TrueFormLives {
						if lifeCfg.LifeIndex == lifeIdx {
							customEnemyHP = lifeCfg.HP
							customEnemyMaxHP = lifeCfg.HP
							break
						}
					}
					// 兜底：如果配置中没有找到对应命条，使用默认值
					if customEnemyHP == enemyStats.HP && len(cfg.TrueFormLives) > 0 {
						lastLife := cfg.TrueFormLives[len(cfg.TrueFormLives)-1]
						customEnemyHP = lastLife.HP
						customEnemyMaxHP = lastLife.HP
					}
				} else {
					// 封印：直接使用配置的血量
					customEnemyHP = cfg.HP
					customEnemyMaxHP = cfg.HP
				}
			} else {
				// 配置不存在时使用内置默认值（向后兼容）
				switch door {
				case 1:
					customEnemyHP = 7000
					customEnemyMaxHP = 7000
				case 2:
					customEnemyHP = 8000
					customEnemyMaxHP = 8000
				case 3:
					customEnemyHP = 9000
					customEnemyMaxHP = 9000
				case 4:
					customEnemyHP = 10000
					customEnemyMaxHP = 10000
				case 5:
					customEnemyHP = 10000
					customEnemyMaxHP = 10000
				case 6:
					customEnemyHP = 12000
					customEnemyMaxHP = 12000
				case 7:
					customEnemyHP = 16000
					customEnemyMaxHP = 16000
				case 8:
					lifeIdx := life
					if lifeIdx <= 0 {
						lifeIdx = 1
					}
					switch lifeIdx {
					case 1:
						customEnemyHP = 7000
						customEnemyMaxHP = 7000
					case 2:
						customEnemyHP = 8000
						customEnemyMaxHP = 8000
					case 3:
						customEnemyHP = 9000
						customEnemyMaxHP = 9000
					case 4:
						customEnemyHP = 12000
						customEnemyMaxHP = 12000
					case 5:
						customEnemyHP = 20000
						customEnemyMaxHP = 20000
					default:
						customEnemyHP = 65000
						customEnemyMaxHP = 65000
					}
				}
			}
		}
		ctx.GameServer.BattleMu.Unlock()
	}

	playerInfo := buildFightPetInfo(uint32(ctx.UserID), playerPetID, playerName, catchTime, playerStats.HP, playerStats.MaxHP, playerLevel, 0, [6]int8{})
	// 谱尼战：2504 使用“伪血量”（puniDisplayHP 压缩到 0~puniDisplayMaxHP），与 BattleState 真实血量比例一致，避免客户端血条超出血槽
	enemyHPFor2504 := customEnemyHP
	enemyMaxHPFor2504 := customEnemyMaxHP
	if (mapID == 108 || mapID == 514) && bossID == 300 {
		dispHP, dispMax := puniDisplayHP(uint32(customEnemyHP), uint32(customEnemyMaxHP))
		enemyHPFor2504 = int(dispHP)
		enemyMaxHPFor2504 = int(dispMax)
	}
	enemyInfo := buildFightPetInfo(0, int(bossID), enemyName, 0, enemyHPFor2504, enemyMaxHPFor2504, enemyLevel, enemyCatchable, initialEnemyBattleLv)

	body := make([]byte, 4+len(playerInfo)+len(enemyInfo))
	binary.BigEndian.PutUint32(body[0:4], 0) // isCanAuto = 0
	copy(body[4:4+len(playerInfo)], playerInfo)
	copy(body[4+len(playerInfo):], enemyInfo)

	ctx.GameServer.SendResponse(ctx.ClientData, 2504, ctx.UserID, ctx.SeqID, body)

	// 初始化战斗状态（盖亚等需记录 BattleMapID 用于战斗结束精元判定；mapID 已在上文设定）
	// 盖亚(261)对战：仅允许单精灵出战
	totalPlayerPets := len(user.Pets)
	if bossID == petIDGaiya {
		totalPlayerPets = 1
	}

	ctx.GameServer.BattleMu.Lock()
	battleState := &gameserver.BattleState{
		EnemyHP:         uint32(customEnemyHP),
		EnemyMaxHP:      uint32(customEnemyMaxHP),
		PlayerHP:        uint32(playerStats.HP),
		PlayerMaxHP:     uint32(playerStats.MaxHP),
		EnemyID:         int(bossID),
		EnemyLevel:      enemyLevel,
		ActivePetIndex:  0,
		TotalPlayerPets: totalPlayerPets,
		DeadPlayerPets:  0,
		IsActive:        true,
		EnemyBattleLv:   initialEnemyBattleLv,
		BattleMapID:     mapID,
		RoundCount:      0,
		LastHitWasCrit:  false,
	}
	// 谱尼：把本次实际使用的门号与命条写回，供 2405/命条切换等使用（含 2404 中央挑战兜底得到的 door=8）
	if (mapID == 108 || mapID == 514) && bossID == 300 && puniDoorForState >= 1 && puniDoorForState <= 8 {
		battleState.PuniDoorIndex = puniDoorForState
		battleState.PuniTrueFormLifeIndex = puniLifeForState
	}

	// 谱尼封印先天状态：在 BattleState 创建后，根据门号设置初始能力等级等（从配置读取）
	if (mapID == 108 || mapID == 514) && bossID == 300 && battleState.PuniDoorIndex >= 1 && battleState.PuniDoorIndex <= 7 {
		door := battleState.PuniDoorIndex
		if cfg, ok := sptboss.GetPuniSealConfig(door); ok {
			if cfg.PlayerAccuracyMod != 0 {
				battleState.PlayerBattleLv[gameskills.StatAccuracy] = int8(cfg.PlayerAccuracyMod)
			}
			if cfg.EnemyAtkMod != 0 {
				battleState.EnemyBattleLv[gameskills.StatAttack] += int8(cfg.EnemyAtkMod)
			}
			if cfg.EnemySpAtkMod != 0 {
				battleState.EnemyBattleLv[gameskills.StatSpAtk] += int8(cfg.EnemySpAtkMod)
			}
		} else {
			// 配置不存在时使用内置默认值（向后兼容）
			switch door {
			case 1:
				battleState.PlayerBattleLv[gameskills.StatAccuracy] = -6
				battleState.EnemyBattleLv[gameskills.StatAttack] += 2
				battleState.EnemyBattleLv[gameskills.StatSpAtk] += 2
			case 6:
				battleState.EnemyBattleLv[gameskills.StatAttack] += 2
				battleState.EnemyBattleLv[gameskills.StatSpAtk] += 2
			case 7:
				battleState.EnemyBattleLv[gameskills.StatAttack] += 2
				battleState.EnemyBattleLv[gameskills.StatSpAtk] += 2
			}
		}
	}

	// 勇者之塔：保留 2415 写入的 TowerLevel/TowerBossIndex 与 BattleMapID=500，否则 2405 战毕时 battle.BattleMapID 为 user.MapID(如 108)，无法跳过 2004，导致当层不刷新
	if prev, ok := ctx.GameServer.BattleStates[ctx.UserID]; ok && prev != nil && (prev.TowerLevel > 0 || mapID == 500) {
		battleState.TowerLevel = prev.TowerLevel
		battleState.TowerBossIndex = prev.TowerBossIndex
		battleState.BattleMapID = 500
	}
	// 试炼之塔：保留 2428/2429 写入的 FreshLevel/FreshBossIndex，多精灵切换协议同勇者之塔
	if prev, ok := ctx.GameServer.BattleStates[ctx.UserID]; ok && prev != nil && prev.FreshLevel > 0 {
		battleState.FreshLevel = prev.FreshLevel
		battleState.FreshBossIndex = prev.FreshBossIndex
	}
	// 保留「对应 BOSS 挑战」标记：2411/2421 已设 IsBossChallenge=true，2404 后 2405 仅在此为 true 时发放 SPT 精元/精灵
	if prev, ok := ctx.GameServer.BattleStates[ctx.UserID]; ok && prev != nil {
		battleState.IsBossChallenge = prev.IsBossChallenge
	}
	ctx.GameServer.BattleStates[ctx.UserID] = battleState
	ctx.GameServer.BattleMu.Unlock()
	logger.Info(fmt.Sprintf("[2404] 战斗状态初始化: PlayerHP=%d/%d EnemyHP=%d/%d EnemyID=%d EnemyLevel=%d",
		battleState.PlayerHP, battleState.PlayerMaxHP, battleState.EnemyHP, battleState.EnemyMaxHP, battleState.EnemyID, battleState.EnemyLevel))

	// 对齐 Lua：确保客户端 PetManager 有当前精灵信息（技能面板依赖）
	// 发送完整宠物信息（2301），这样客户端才能显示技能列表
	if len(user.Pets) > 0 {
		petInfoBody := buildFullPetInfo(user.Pets[0])
		ctx.GameServer.SendResponse(ctx.ClientData, 2301, ctx.UserID, ctx.SeqID, petInfoBody)
	}
}

// ==================== FIGHT_NPC_MONSTER (2408) ====================

// handleFightNpcMonster CMD 2408 地图野怪战斗
// 对齐 Lua: fight_handlers.handleFightNpcMonster
// 请求体: monsterSlot(4) — 槽位 0~8，客户端点哪只怪就发哪一格
// 行为: 根据当前地图与槽位，选出一个怪物ID，然后调用 buildNoteReadyToFightInfo 回推 2503。
func handleFightNpcMonster(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// 解析请求中的槽位索引
	var slotIndex int
	if len(ctx.Body) >= 4 {
		slotIndex = int(binary.BigEndian.Uint32(ctx.Body[0:4]))
	}

	mapID := user.MapID
	if mapID == 0 {
		mapID = 1
	}

	// 与 2004 一致：只用该玩家当前地图的槽位（进图/定时刷新维护），避免用全图缓存导致“点了 A 变成 B”
	slots := ctx.GameServer.GetPlayerOgreSlots(ctx.UserID, mapID)
	if slots == nil || len(slots) == 0 {
		slots = gameogres.GenerateNewSlotsNoCache(mapID)
		if len(slots) > 0 {
			ctx.GameServer.SetPlayerOgreSlots(ctx.UserID, mapID, slots)
		}
	}
	enemyID := 0
	enemyLevel := 5

	// 2004 包体为 9 格，实际只填前 4 格；若客户端发 4~8，按 4 格取模与显示对齐
	if len(slots) > 0 && (slotIndex < 0 || slotIndex >= len(slots)) {
		if slotIndex >= 4 && slotIndex <= 8 {
			slotIndex = slotIndex % 4
		} else if slotIndex < 0 {
			slotIndex = 0
		} else {
			slotIndex = slotIndex % len(slots)
		}
	}
	// 若仍越界或该槽为空，取第一个有效槽
	if len(slots) > 0 && (slotIndex < 0 || slotIndex >= len(slots) || slots[slotIndex].PetID == 0) {
		for i, s := range slots {
			if s.PetID > 0 {
				slotIndex = i
				break
			}
		}
	}

	// 按槽位取怪物ID与等级
	if slotIndex >= 0 && slotIndex < len(slots) && slots[slotIndex].PetID > 0 {
		enemyID = slots[slotIndex].PetID
		if slots[slotIndex].Level > 0 {
			enemyLevel = slots[slotIndex].Level
		}
		logger.Info(fmt.Sprintf("[2408] 使用槽位 %d 的精灵: PetID=%d Level=%d Shiny=%v", slotIndex, enemyID, enemyLevel, slots[slotIndex].Shiny))
	} else {
		// 如果指定槽位无效，找第一个有效的槽位
		for i, s := range slots {
			if s.PetID > 0 {
				enemyID = s.PetID
				if s.Level > 0 {
					enemyLevel = s.Level
				}
				logger.Info(fmt.Sprintf("[2408] 回退到槽位 %d 的精灵: PetID=%d Level=%d", i, enemyID, enemyLevel))
				break
			}
		}
	}

	if enemyID == 0 {
		// 回退到新手 BOSS（比比鼠），避免客户端卡死
		enemyID = 13
		enemyLevel = 5
		logger.Warning(fmt.Sprintf("[2408] 未找到有效精灵，回退到比比鼠: mapID=%d", mapID))
	}

	// 预先在 BattleStates 中记录这次点击对应的敌人ID/等级，
	// 方便后续 2404 READY_TO_FIGHT 使用同一个敌人（对齐 Lua: getOgresForMap → FIGHT_NPC_MONSTER）。
	ctx.GameServer.BattleMu.Lock()
	battle, ok := ctx.GameServer.BattleStates[ctx.UserID]
	if !ok || battle == nil {
		battle = &gameserver.BattleState{}
	}
	battle.EnemyID = enemyID
	battle.EnemyLevel = enemyLevel
	battle.IsActive = true
	ctx.GameServer.BattleStates[ctx.UserID] = battle
	ctx.GameServer.BattleMu.Unlock()

	logger.Info(fmt.Sprintf("[2408] FIGHT_NPC_MONSTER: mapId=%d slot=%d enemyID=%d level=%d", mapID, slotIndex, enemyID, enemyLevel))

	body := buildNoteReadyToFightInfo(ctx, uint32(enemyID))
	ctx.GameServer.SendResponse(ctx.ClientData, 2503, ctx.UserID, ctx.SeqID, body)
}

// ==================== ATTACK_BOSS (2412) ====================

// handleAttackBoss CMD 2412 攻击 SPT BOSS（破除防护罩）
// 客户端 BossModel.aimatState 在玩家用火焰喷射器等击中 BOSS 后发送 region(4)。
// 服务端扣减该 BOSS 防护罩血量并推送 2021 更新显示；响应 2412 返回当前 hp(4)。
// 克洛斯星密林(12) region=0 为蘑菇怪，每次命中扣约 25% 满血，hp 归零后可点击进入战斗。
func handleAttackBoss(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	mapID := user.MapID
	if mapID == 0 {
		mapID = 1
	}
	var region uint32
	if len(ctx.Body) >= 4 {
		region = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	// 仅处理有防护罩的 SPT BOSS（如克洛斯星密林 12 蘑菇怪）
	e, ok := sptboss.GetByMapAndParam(mapID, region)
	if !ok || !e.HasShield {
		ctx.GameServer.SendResponse(ctx.ClientData, 2412, ctx.UserID, ctx.SeqID, make([]byte, 4))
		return
	}

	petMgr := gamepets.GetInstance()
	stats := petMgr.GetStats(e.BossPetID, e.Level, 15, gamepets.EVStats{}, 0)
	maxHP := stats.MaxHP
	currentHP := getBossHp(ctx.UserID, mapID, region)
	if currentHP <= 0 {
		currentHP = maxHP
	}
	// 每次命中扣约 25% 满血，至少扣 1
	damage := maxHP / 4
	if damage <= 0 {
		damage = 1
	}
	newHP := currentHP - damage
	if newHP < 0 {
		newHP = 0
	}
	setBossHp(ctx.UserID, mapID, region, newHP)

	// 响应 2412：返回当前 BOSS 血量(4)，供客户端更新显示
	respBody := make([]byte, 4)
	binary.BigEndian.PutUint32(respBody, uint32(newHP))
	ctx.GameServer.SendResponse(ctx.ClientData, 2412, ctx.UserID, ctx.SeqID, respBody)

	// 推送 2021 更新 BOSS；hp 归零时先移除再重加，客户端才会真正去掉防护罩（FungusBoss 需重新 add 且 show(0) 才不加载 film）
	if newHP == 0 {
		removeBody := buildMapBossRemoveRegion(mapID, region)
		if len(removeBody) > 0 {
			ctx.GameServer.SendResponse(ctx.ClientData, cmdMapBoss, ctx.UserID, 0, removeBody)
		}
	}
	bossBody := buildMapBossList(mapID, uint32(newHP))
	if len(bossBody) > 0 {
		ctx.GameServer.SendResponse(ctx.ClientData, cmdMapBoss, ctx.UserID, 0, bossBody)
		logger.Info(fmt.Sprintf("[2412] ATTACK_BOSS: UID=%d MapID=%d region=%d hp %d->%d", ctx.UserID, mapID, region, currentHP, newHP))
	}
}

// hasCompletedTutorial 检查玩家是否完成新手任务（任务85-88）
// 参考 Lua: hasCompletedTutorial(user) in seer_login_response.lua
func hasCompletedTutorial(gameData *userdb.GameData) bool {
	if gameData == nil || gameData.Tasks == nil {
		return false
	}
	for id := 85; id <= 88; id++ {
		key := strconv.Itoa(id)
		task, ok := gameData.Tasks[key]
		if !ok {
			return false
		}
		status := task.Status
		if status == "" {
			return false
		}
		// 数值 3 或字符串 "completed" 视为已完成
		if status == "completed" || status == "3" {
			continue
		}
		return false
	}
	return true
}

// handleLogin 处理登录命令
func handleLogin(ctx *gameserver.HandlerContext) {
	// 同一账户只允许一个在线会话：若该 UID 已在其他连接登录，踢掉旧连接
	ctx.GameServer.KickOtherSessionsOfUser(ctx.ClientData, ctx.UserID)

	// 获取用户游戏数据
	gameData := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	// 所有用户上线时地图ID固定为1（传送舱）
	gameData.MapID = 1

	// 确保形态值根据超能等级自动更新（超能等级模型）
	if gameData.Nono.SuperLevel > 0 {
		updateSuperNonoTypeByLevel(gameData)
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, gameData)
		}
		// 将当前超能等级登记到资源服缓存中，供 /resource/nono/super/* 使用
		registerSuperNonoToCache(ctx, gameData)
	}

	// 获取账号基础数据（用于 userId / regTime 等字段）
	var account *userdb.User
	if ctx.GameServer.UserDB != nil {
		account = ctx.GameServer.UserDB.FindByUserID(ctx.UserID)
	}

	// 处理登录请求包体
	if len(ctx.Body) > 0 {
		// 这里可以添加对登录请求包体的处理逻辑
		// 例如解密、验证会话等
		logger.Info("处理登录请求包体")
	}

	// 构建登录响应包（严格对齐 Lua 版 SeerLoginResponse.makeLoginResponse）
	body := buildLoginResponse(ctx.UserID, account, gameData)

	// 记录登录响应关键字段与长度，便于和 Lua 抓包对比
	logger.Info(fmt.Sprintf(
		"登录响应数据: UID=%d Nick=%s MapID=%d Pets=%d Clothes=%d Tasks=%d BodyLen=%d",
		ctx.UserID,
		gameData.Nick,
		gameData.MapID,
		len(gameData.Pets),
		len(gameData.Clothes),
		len(gameData.Tasks),
		len(body),
	))

	// 标记用户已登录
	ctx.ClientData.LoggedIn = true

	// 发送响应
	ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, body)

	// 启动心跳
	ctx.GameServer.StartHeartbeat(ctx.ClientData, ctx.UserID)

	// 进入默认地图并推送 2001/2003/2004，使用户加入 MapUsers、能看见同图玩家且被同图玩家看见
	pushInitialMapEnter(ctx)
}

// pushChannelList 推送频道列表
func pushChannelList(ctx *gameserver.HandlerContext) {
	// 构建频道列表响应
	// 这里可以根据实际情况构建频道列表
	channelList := make([]byte, 2048) // 增加容量到2048
	index := 0

	// 频道数量
	binary.BigEndian.PutUint32(channelList[index:], 29) // 29个频道
	index += 4

	// 填充频道数据
	for i := 1; i <= 29; i++ {
		// 频道ID
		binary.BigEndian.PutUint32(channelList[index:], uint32(i))
		index += 4
		// 频道名称
		channelName := fmt.Sprintf("频道%d", i)
		nameBytes := []byte(channelName)
		copy(channelList[index:index+32], nameBytes)
		index += 32
		// 在线人数
		binary.BigEndian.PutUint32(channelList[index:], 100) // 模拟在线人数
		index += 4
	}

	// 发送频道列表响应
	logger.Info(fmt.Sprintf("推送频道列表: 命令ID=80001, 频道数量=29, 数据长度=%d", index))
	ctx.GameServer.SendResponse(ctx.ClientData, 80001, ctx.UserID, 0, channelList[:index])
}

// pushServerList 推送服务器列表
func pushServerList(ctx *gameserver.HandlerContext) {
	// 构建服务器列表响应
	// 这里可以根据实际情况构建服务器列表
	serverList := make([]byte, 2048) // 增加容量到2048
	index := 0

	// 服务器数量
	binary.BigEndian.PutUint32(serverList[index:], 29) // 29个服务器
	index += 4

	// 填充服务器数据
	for i := 1; i <= 29; i++ {
		// 服务器ID
		binary.BigEndian.PutUint32(serverList[index:], uint32(i))
		index += 4
		// 服务器名称
		serverName := fmt.Sprintf("服务器%d", i)
		nameBytes := []byte(serverName)
		copy(serverList[index:index+32], nameBytes)
		index += 32
		// 在线人数
		binary.BigEndian.PutUint32(serverList[index:], 1000) // 模拟在线人数
		index += 4
		// 服务器状态
		binary.BigEndian.PutUint32(serverList[index:], 1) // 1表示正常
		index += 4
	}

	// 发送服务器列表响应
	ctx.GameServer.SendResponse(ctx.ClientData, 80002, ctx.UserID, 0, serverList[:index])
}

// buildLoginResponse 构建登录响应包（对齐 Lua 版 SeerLoginResponse.makeLoginResponse）
func buildLoginResponse(userID int64, account *userdb.User, gameData *userdb.GameData) []byte {
	// 为避免频繁分配，预留一个大概的容量，最终长度由实际写入决定
	buffer := make([]byte, 0, 2048)

	writeU32 := func(v uint32) {
		tmp := make([]byte, 4)
		binary.BigEndian.PutUint32(tmp, v)
		buffer = append(buffer, tmp...)
	}
	writeU8 := func(v byte) {
		buffer = append(buffer, v)
	}
	writeFixedString := func(s string, n int) {
		b := []byte(s)
		if len(b) > n {
			b = b[:n]
		}
		buffer = append(buffer, b...)
		if len(b) < n {
			buffer = append(buffer, make([]byte, n-len(b))...)
		}
	}

	// ---- 1. 账号基本信息 ----
	regTime := time.Now().Unix() - 86400*365
	if account != nil && account.RegisterTime > 0 {
		regTime = account.RegisterTime
	}
	writeU32(uint32(userID))            // userID
	writeU32(uint32(regTime))           // regTime
	writeFixedString(gameData.Nick, 16) // nick

	// ---- 2. VIP Flags ----
	vipFlags := uint32(0)
	if gameData.Nono.SuperNono > 0 {
		vipFlags = 3 // 1 | 2
	}
	writeU32(vipFlags)

	// ---- 3. 基础属性 ----
	writeU32(uint32(gameData.DsFlag))  // dsFlag
	writeU32(uint32(gameData.Color))   // color
	writeU32(uint32(gameData.Texture)) // texture

	energy := gameData.Energy
	if energy == 0 && gameData.Nono.Energy > 0 {
		energy = gameData.Nono.Energy
	}
	if energy == 0 {
		energy = 100
	}
	writeU32(uint32(energy))         // energy
	writeU32(uint32(gameData.Coins)) // coins
	writeU32(uint32(gameData.FightBadge))

	// 地图/出生逻辑：默认进入 ID=1 传送舱（用户要求“玩家登录默认进入地址位置在ID=1的传送舱”）
	gameData.MapID = 1 // 所有用户上线时地图ID固定为1
	if gameData.PosX == 0 {
		gameData.PosX = 300
	}
	if gameData.PosY == 0 {
		gameData.PosY = 270
	}
	writeU32(uint32(gameData.MapID))
	writeU32(uint32(gameData.PosX))
	writeU32(uint32(gameData.PosY))
	writeU32(uint32(gameData.TimeToday))
	if gameData.TimeLimit == 0 {
		gameData.TimeLimit = 86400
	}
	writeU32(uint32(gameData.TimeLimit))

	// ---- 4. Flags （4 个 Byte）----
	writeU8(0) // isClothHalfDay
	writeU8(0) // isRoomHalfDay
	writeU8(0) // iFortressHalfDay
	writeU8(0) // isHQHalfDay

	// ---- 5. 统计信息 ----
	writeU32(uint32(gameData.LoginCnt))
	writeU32(uint32(gameData.Inviter))
	writeU32(uint32(gameData.NewInviteeCnt))
	writeU32(uint32(gameData.Nono.VipLevel))
	writeU32(uint32(gameData.Nono.VipValue))
	writeU32(uint32(gameData.Nono.VipStage))
	writeU32(uint32(gameData.Nono.AutoCharge))

	endTime := uint32(gameData.Nono.VipEndTime)
	isSuper := gameData.Nono.SuperNono > 0
	if isSuper && endTime == 0 {
		endTime = 0x7FFFFFFF
	}
	writeU32(endTime)
	writeU32(uint32(gameData.Nono.FreshManBonus))

	// ---- 6. 固定长度列表 ----
	// nonoChipList: 80 字节，每字节 1=已开启 0=未开启，对应 700001..700080；客户端 UserInfo.setForPeoleInfo 据此恢复
	nonoChipList := make([]byte, 80)
	for i := 0; i < 80 && i < 160; i++ {
		if len(gameData.Nono.Func) > i/8 {
			if (gameData.Nono.Func[i/8] & (1 << uint(i%8))) != 0 {
				nonoChipList[i] = 1
			}
		}
	}
	buffer = append(buffer, nonoChipList...)
	writeFixedString("", 50) // dailyResArr

	// ---- 7. 更多统计字段 ----
	writeU32(uint32(gameData.TeacherID))
	writeU32(uint32(gameData.StudentID))
	writeU32(uint32(gameData.GraduationCount))
	// MaxPuniLv 协议约定范围为 0~8（0=未开启，1~7=封印进度，8=解锁真身），异常值按 0 处理
	maxPuniLv := gameData.MaxPuniLv
	if maxPuniLv < 0 || maxPuniLv > 8 {
		maxPuniLv = 0
	}
	writeU32(uint32(maxPuniLv))
	writeU32(uint32(gameData.PetMaxLev))
	writeU32(uint32(gameData.PetAllNum))
	writeU32(uint32(gameData.MonKingWin))
	writeU32(uint32(gameData.CurStage))
	writeU32(uint32(gameData.MaxStage))
	writeU32(uint32(gameData.CurFreshStage))
	writeU32(uint32(gameData.MaxFreshStage))
	writeU32(uint32(gameData.MaxArenaWins))
	writeU32(uint32(gameData.TwoTimes))
	writeU32(uint32(gameData.ThreeTimes))
	writeU32(uint32(gameData.AutoFight))
	writeU32(uint32(gameData.AutoFightTimes))
	writeU32(uint32(gameData.EnergyTimes))
	writeU32(uint32(gameData.LearnTimes))
	writeU32(uint32(gameData.MonBtlMedal))

	// recordCnt, obtainTm, soulBeadItemID, remainingSec, fuseTimes（元神珠赋形状态，客户端用剩余秒数显示，与 GM 转化时间一致）
	writeU32(0) // recordCnt
	if gameData.SoulBeadTransform != nil {
		writeU32(gameData.SoulBeadTransform.ObtainTime)
		writeU32(gameData.SoulBeadTransform.ItemID) // PetClass，客户端 soulBeadItemID
		remainingSec := int64(0)
		if gameData.SoulBeadTransform.ExpireTime > time.Now().Unix() {
			remainingSec = gameData.SoulBeadTransform.ExpireTime - time.Now().Unix()
		}
		writeU32(uint32(remainingSec))
	} else {
		writeU32(0)
		writeU32(0)
		writeU32(0)
	}
	writeU32(0) // fuseTimes

	// ---- 8. NoNo 详细信息 ----
	hasNono := gameData.Nono.HasNono > 0
	superNono := gameData.Nono.SuperNono

	if hasNono {
		writeU32(1)
	} else {
		writeU32(0)
	}
	// 返回实际的超能NONO形态值（1-5），而不是布尔值，客户端据此加载对应的SWF文件
	writeU32(uint32(superNono)) // superNono形态值（1-5）
	logger.Info(fmt.Sprintf("[登录响应] UserID=%d SuperLevel=%d SuperNono形态=%d (应加载nono_%d.swf)", 
		userID, gameData.Nono.SuperLevel, superNono, superNono))

	// nonoState / nonoColor / nonoNick
	flag := gameData.Nono.Flag
	if flag == 0 {
		flag = -1 // 0xFFFFFFFF
	}
	writeU32(uint32(flag))
	writeU32(uint32(gameData.Nono.Color))
	writeFixedString(gameData.Nono.Nick, 16)

	// ---- 9. TeamInfo (24 bytes) ----
	for i := 0; i < 6; i++ {
		writeU32(0)
	}

	// ---- 10. TeamPKInfo (8 bytes) ----
	writeU32(0)
	writeU32(0)

	// ---- 11. Badge & Reserved ----
	writeU8(0) // padding/flag
	writeU32(uint32(gameData.Badge))
	writeFixedString("", 27)

	// ---- 12. Task List (500 bytes, 每个任务 1 字节状态) ----
	// 塞西利亚星第一层（地图 40）卡加载：init() 里若任务 8/122 为已接受会调 getProStatusList 等异步，
	// 若回调未正确执行或客户端等其才关加载界面会一直停在“快去探索!”。121/122 为雷伊特训，8 为塞西利亚晶体/防寒服等。
	// 登录时不下发这些任务为已接受，避免地图 17/40 卡任务或卡加载；要做任务可在对应 NPC/面板内再次接受。
	const (
		taskIDSeerCecilia  = 8   // 塞西利亚星相关（防寒服/晶体/阿克西亚平静），地图 40 init 依赖 getProStatusList(8)
		taskIDLeiyiLiAosi  = 121 // 雷神极限修行之里奥斯，地图 17
		taskIDLeiyiAkexiya = 122 // 雷伊特训之阿克希亚，地图 40
	)
	taskCount := 0
	if gameData.Tasks != nil {
		for i := 1; i <= 500; i++ {
			key := strconv.Itoa(i)
			statusByte := byte(0)
			if t, ok := gameData.Tasks[key]; ok {
				switch t.Status {
				case "completed", "3":
					statusByte = 3
				case "accepted", "doing", "in_progress", "1":
					statusByte = 1
				default:
					if n, err := strconv.Atoi(t.Status); err == nil {
						if n < 0 {
							n = 0
						} else if n > 255 {
							n = 255
						}
						statusByte = byte(n)
					}
				}
				// 任务 8/121/122：登录时不下发“已接受”，避免地图 40 塞西利亚星第一层卡加载、地图 17/40 卡任务
				if (i == taskIDSeerCecilia || i == taskIDLeiyiLiAosi || i == taskIDLeiyiAkexiya) && statusByte == 1 {
					statusByte = 0
				}
				if statusByte != 0 {
					taskCount++
				}
			}
			writeU8(statusByte)
		}
	} else {
		for i := 0; i < 500; i++ {
			writeU8(0)
		}
	}
	logger.Info(fmt.Sprintf("登录任务状态编码完成: totalTasks=%d", taskCount))

	// ---- 13. Pet List ----
	writeU32(uint32(len(gameData.Pets)))
	if len(gameData.Pets) > 0 {
		for _, pet := range gameData.Pets {
			petBody := buildFullPetInfo(pet)
			buffer = append(buffer, petBody...)
		}
	}

	// ---- 14. Clothes ----
	writeU32(uint32(len(gameData.Clothes)))
	for _, clothID := range gameData.Clothes {
		// Lua: 每件服装写入 (clothId, level)，我们只有 ID，默认等级为 1
		writeU32(uint32(clothID))
		writeU32(1)
	}

	// ---- 15. Title & Achievements ----
	writeU32(uint32(gameData.CurTitle))
	buffer = append(buffer, sptboss.BuildBossAchievement(gameData.DefeatedSPTBossIds)...) // bossAchievement 200 字节

	return buffer
}

// natureToClientID 将后端性格 ID 转为客户端 NatureXMLInfo 的 id
// 后端顺序(0-24): 孤独,勇敢,固执,调皮,胆小,急躁,开朗,天真,大胆,悠闲,顽皮,无虑,保守,稳重,冷静,马虎,沉着,温顺,狂妄,慎重,害羞,浮躁,坦率,实干,认真
// 客户端顺序(0-24): 孤独,固执,调皮,勇敢,大胆,顽皮,无虑,悠闲,保守,稳重,马虎,冷静,沉着,温顺,慎重,狂妄,胆小,急躁,开朗,天真,害羞,实干,坦率,浮躁,认真
var natureToClientIDTable = [25]int{
	0, 3, 1, 2, 16, 17, 18, 19, 4, 7, 5, 6, 8, 9, 11, 10, 12, 13, 15, 14, 20, 23, 22, 21, 24,
}

func natureToClientID(backendNature int) int {
	if backendNature >= 0 && backendNature < 25 {
		return natureToClientIDTable[backendNature]
	}
	return 0
}

// buildFullPetInfo 构建完整版 PetInfo
// 对齐 Lua 版 PetHandlers.buildFullPetInfo / UserInfo.setForLoginInfo 的读取结构
func buildFullPetInfo(p userdb.Pet) []byte {
	buf := make([]byte, 0, 200)

	writeU32 := func(v uint32) {
		tmp := make([]byte, 4)
		binary.BigEndian.PutUint32(tmp, v)
		buf = append(buf, tmp...)
	}
	writeU16 := func(v uint16) {
		tmp := make([]byte, 2)
		binary.BigEndian.PutUint16(tmp, v)
		buf = append(buf, tmp...)
	}
	writeFixedString := func(s string, n int) {
		b := []byte(s)
		if len(b) > n {
			b = b[:n]
		}
		buf = append(buf, b...)
		if len(b) < n {
			buf = append(buf, make([]byte, n-len(b))...)
		}
	}

	petID := p.ID
	if petID <= 0 {
		petID = 1
	}
	level := p.Level
	if level <= 0 {
		level = 5
	}
	dv := p.DV
	if dv <= 0 {
		dv = 31
	}
	nature := p.Nature
	exp := p.Exp
	name := p.Name

	petMgr := gamepets.GetInstance()
	skillMgr := gameskills.GetInstance()

	// 从 Pet 结构体中获取 EV（学习力）
	ev := p.GetEVStats()

	// 基础属性 & 战斗属性（使用精灵的性格）
	stats := petMgr.GetStats(petID, level, dv, ev, nature)
	expInfo := petMgr.GetExpInfo(petID, level, exp)

	// 技能列表：最多 4 个；优先使用技能唤醒仪保存的自定义技能
	var rawSkills []int
	if len(p.Skills) > 0 {
		rawSkills = make([]int, 4)
		for i := 0; i < 4 && i < len(p.Skills); i++ {
			rawSkills[i] = p.Skills[i]
		}
	} else {
		rawSkills = petMgr.GetSkillsForLevel(petID, level)
	}
	skillIDs := make([]int, 4)
	skillPPs := make([]int, 4)
	validCount := 0

	// 确保至少有一个技能（默认技能 10001 = 撞击）
	hasAnySkill := false
	for i := 0; i < len(rawSkills) && i < 4; i++ {
		if rawSkills[i] > 0 {
			hasAnySkill = true
			break
		}
	}
	if !hasAnySkill {
		// 如果没有技能，添加默认技能 10001（撞击）
		rawSkills = []int{10001, 0, 0, 0}
	}

	for i := 0; i < 4; i++ {
		sid := 0
		if i < len(rawSkills) {
			sid = rawSkills[i]
		}
		pp := 0
		if sid > 0 {
			if sk := skillMgr.Get(sid); sk != nil {
				pp = sk.PP
				if pp == 0 {
					pp = sk.MaxPP
				}
			}
			if pp == 0 {
				pp = 20
			}
			validCount++
		}
		skillIDs[i] = sid
		skillPPs[i] = pp
	}
	// 调试日志：输出技能信息
	logger.Info(fmt.Sprintf("[buildFullPetInfo] PetID=%d Level=%d Skills: validCount=%d", petID, level, validCount))
	for i := 0; i < 4; i++ {
		if skillIDs[i] > 0 {
			logger.Info(fmt.Sprintf("  Skill[%d]: ID=%d PP=%d", i, skillIDs[i], skillPPs[i]))
		}
	}

	// 1. 基础信息
	// nature: 后端与客户端 NatureXMLInfo 的性格顺序不同，需查表转换
	// 后端: 0孤独,1勇敢,2固执,3调皮,4胆小... | 客户端: 0孤独,1固执,2调皮,3勇敢,4大胆...
	writeU32(uint32(petID))    // id
	writeFixedString(name, 16) // name
	writeU32(uint32(dv))       // dv
	writeU32(uint32(natureToClientID(nature))) // nature（转客户端 ID 供正确显示）
	writeU32(uint32(level))    // level
	// 对齐前端语义（经验分配器“升级所需经验值”= nextLvExp - exp，须非负）：
	// - exp: 当前等级已获得经验，客户端用 nextLvExp - exp 显示“升级所需经验值”
	// - lvExp: 同上，当前等级经验（与 exp 一致）
	// - nextLvExp: 当前等级升到下一等级所需经验
	writeU32(uint32(expInfo.CurrentLevelExp)) // exp（当前等级经验，避免显示负数）
	writeU32(uint32(expInfo.CurrentLevelExp)) // lvExp（当前等级经验）
	writeU32(uint32(expInfo.NextLevelExp))    // nextLvExp

	// 2. 战斗属性
	writeU32(uint32(stats.HP))      // hp
	writeU32(uint32(stats.MaxHP))   // maxHp
	writeU32(uint32(stats.Attack))  // atk
	writeU32(uint32(stats.Defence)) // def
	writeU32(uint32(stats.SpAtk))   // sa
	writeU32(uint32(stats.SpDef))   // sd
	writeU32(uint32(stats.Speed))   // spd

	// 3. 努力值（当前全部 0）
	writeU32(uint32(ev.HP))
	writeU32(uint32(ev.Atk))
	writeU32(uint32(ev.Def))
	writeU32(uint32(ev.SpAtk))
	writeU32(uint32(ev.SpDef))
	writeU32(uint32(ev.Spd))

	// 4. 技能列表
	writeU32(uint32(validCount))
	for i := 0; i < 4; i++ {
		writeU32(uint32(skillIDs[i]))
		writeU32(uint32(skillPPs[i]))
	}

	// 5. 捕获信息
	writeU32(uint32(p.CatchTime)) // catchTime
	writeU32(uint32(301))         // catchMap，默认 301
	writeU32(0)                   // catchRect
	writeU32(uint32(level))       // catchLevel

	// 6. 特效列表
	if p.Trait > 0 {
		// 目前仅在服务器侧实现“融合精灵特性”这一类永久特性：
		// - itemId: 对应 PetEffectXMLInfo 中的 NewSeIdx.Idx（1006-1045）
		// - status: 2 表示激活/永久效果
		// 其余字段（leftCount/effectID/args）当前 Go 端战斗逻辑未使用，按 0 填充即可让前端正常解析与展示特性文案。
		writeU16(1) // effectCount

		// 对应 com.robot.core.info.pet.PetEffectInfo 的读取顺序：
		// itemId(U32) + status(U8) + leftCount(U8) + effectID(U16) +
		// param1(U8) + reserved(U8) + param2(U8) + paddingUTF(13)
		writeU32(uint32(p.Trait)) // itemId = NewSeIdx.Idx
		buf = append(buf, byte(2)) // status: 2 = 常驻/生效中
		buf = append(buf, byte(0)) // leftCount: 0 = 无次数限制
		writeU16(0)                // effectID: 暂未在 Go 端使用，置 0
		// 简化处理：参数与 13 字节描述区全部填 0，前端仅依赖 itemId/Idx 展示特性名称与说明。
		buf = append(buf, byte(0))           // param1
		buf = append(buf, byte(0))           // reserved
		buf = append(buf, byte(0))           // param2
		buf = append(buf, make([]byte, 13)...) // padding / args 占位
	} else {
		writeU16(0) // effectCount
	}

	// 7. Skin
	writeU32(0) // skinID

	return buf
}

// handleEnterMap 处理进入地图命令
func handleEnterMap(ctx *gameserver.HandlerContext) {
	// 获取用户数据
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// 解析请求参数
	mapId := uint32(0)
	x := uint32(500)
	y := uint32(300)

	if len(ctx.Body) >= 16 {
		// 解析地图类型、地图ID和坐标
		// 暂时忽略mapType，因为GameData结构中没有对应的字段
		// mapType = binary.BigEndian.Uint32(ctx.Body[0:4])
		mapId = binary.BigEndian.Uint32(ctx.Body[4:8])
		x = binary.BigEndian.Uint32(ctx.Body[8:12])
		y = binary.BigEndian.Uint32(ctx.Body[12:16])
	}

	// 兼容修复：暗黑第七门进图初始坐标超出可移动范围的问题
	// 现象：进入暗黑第七门(7-1/7-2/7-3)时，角色出生点偏离可行走区域，被客户端判定为“超出可移动范围”，导致无法移动。
	// 处理：只要当前玩家在暗黑武斗场上下文中，并且 DarkPortalDoors 记录的门索引为 6（第七门），
	// 无论是 7-1 / 7-2 / 7-3，统一将进入该地图的初始坐标修正为一个安全点位 (600,300)。
	// 说明：其它门目前未发现该问题，因此仅对第七门定向修正，避免影响正常地图行为。
	if user != nil {
		ctx.GameServer.DarkPortalDoorsMu.RLock()
		doorInfo, hasDoor := ctx.GameServer.DarkPortalDoors[ctx.UserID]
		ctx.GameServer.DarkPortalDoorsMu.RUnlock()
		if hasDoor && doorInfo.DoorIndex == 6 { // 第七门：索引 6
			oldX, oldY := x, y
			x = 600
			y = 300
			logger.Info(fmt.Sprintf(
				"[2001] 修正暗黑第七门进图坐标: UID=%d MapID=%d OldXY=(%d,%d)->(%d,%d) SubIndex=%d",
				ctx.UserID, mapId, oldX, oldY, x, y, doorInfo.SubIndex,
			))
		}
	}

	logger.Info(fmt.Sprintf(
		"进入地图请求: UID=%d ReqMapID=%d CurMapID=%d X=%d Y=%d BodyLen=%d",
		ctx.UserID,
		mapId,
		user.MapID,
		x,
		y,
		len(ctx.Body),
	))

	// 验证与设置默认地图（默认 ID=1 传送舱，新号也可进入）
	if mapId == 0 {
		mapId = uint32(user.MapID)
		if mapId == 0 {
			mapId = 1
		}
	}

	oldMapID := user.MapID
	// 更新用户状态
	user.MapID = int(mapId)
	user.PosX = int(x)
	user.PosY = int(y)

	// 地图玩家追踪：从旧地图移除、加入新地图，并推送/广播 2003 使同图玩家互相可见
	// 注意：如果 oldMapID == mapId（例如从 handleLeaveMap 的 102→110 切换后，客户端又发送了 2001），
	// 需要避免重复移除/添加，但仍需要发送响应以确保客户端能正确关闭加载界面
	if oldMapID != int(mapId) {
		ctx.GameServer.RemoveUserFromMap(oldMapID, ctx.UserID)
		if oldMapID > 0 {
			oldListBody := buildMapPlayerListForMap(ctx.GameServer, oldMapID)
			ctx.GameServer.BroadcastToMap(oldMapID, 0, 2003, oldListBody)
		}
		ctx.GameServer.AddUserToMap(int(mapId), ctx.UserID, ctx.ClientData)
	} else {
		// 如果用户已经在地图110（例如从handleLeaveMap的102→110切换），
		// 确保用户已正确添加到地图中（可能handleLeaveMap已经添加了，但这里再次确认）
		// 注意：AddUserToMap应该是幂等的，重复调用不应该有问题
		ctx.GameServer.AddUserToMap(int(mapId), ctx.UserID, ctx.ClientData)
	}
	ctx.GameServer.SetOgreEnterMapTime(ctx.UserID)

	// 构建用户信息响应（格式对齐 UserInfo.setForPeoleInfo / Lua map_handlers.buildPeopleInfo）
	body := buildPeopleInfo(ctx.UserID, user, time.Now().Unix(), int(x), int(y), true)

	logger.Info(fmt.Sprintf(
		"进入地图响应2001: UID=%d MapID=%d BodyLen=%d",
		ctx.UserID,
		user.MapID,
		len(body),
	))

	// 发送进入地图响应（对齐 Lua：ENTER_MAP 回包使用 seq=0）
	ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, 0, body)

	// 推送地图玩家列表（同地图所有玩家含自己），使客户端能显示其他玩家（对齐 Lua：seq=0）
	listBody := buildMapPlayerListForMap(ctx.GameServer, int(mapId))
	ctx.GameServer.SendResponse(ctx.ClientData, 2003, ctx.UserID, 0, listBody)
	ctx.GameServer.BroadcastToMap(int(mapId), ctx.UserID, 2003, listBody)
	logger.Info(fmt.Sprintf(
		"推送地图玩家列表2003: UID=%d MapID=%d Count=%d",
		ctx.UserID,
		user.MapID,
		binary.BigEndian.Uint32(listBody[0:4]),
	))
	// 补发 2112/9019 给进图者：部分客户端仅根据 2112、9019 更新他人飞行与 NONO 跟随显示
	pushOtherPlayersFlyAndNonoToClient(ctx.GameServer, ctx.ClientData, int(mapId), ctx.UserID)

	// 推送地图怪物列表（与 Lua 一致：每玩家每地图槽位，进图时生成并记录，供定时刷新与 2408 使用）
	slots := gameogres.GenerateNewSlotsNoCache(int(mapId))
	if len(slots) > 0 {
		ctx.GameServer.SetPlayerOgreSlots(ctx.UserID, int(mapId), slots)
		ogreBody := ctx.GameServer.BuildMapOgreListFromSlots(slots)
		// 对齐 Lua：MAP_OGRE_LIST 主动推送使用 seq=0
		ctx.GameServer.SendResponse(ctx.ClientData, 2004, ctx.UserID, 0, ogreBody)
	} else {
		ogreBody := buildMapOgreList(int(mapId))
		ctx.GameServer.SendResponse(ctx.ClientData, 2004, ctx.UserID, 0, ogreBody)
	}
	logger.Info(fmt.Sprintf(
		"推送地图怪物列表2004: UID=%d MapID=%d",
		ctx.UserID,
		user.MapID,
	))

	// 有防护罩的 SPT 地图：重置 BOSS 防护罩满血并推送 MAP_BOSS(2021)
	// 推送两次 2021：第二次时客户端 BOSS 已存在会调用 show()，BossModel 会执行 _walk.execute 从而启动每 3 秒走动
	if e, ok := sptboss.GetByMapAndParam(int(mapId), 0); ok && e.HasShield {
		petMgr := gamepets.GetInstance()
		stats := petMgr.GetStats(e.BossPetID, e.Level, 15, gamepets.EVStats{}, 0)
		setBossHp(ctx.UserID, int(mapId), 0, stats.MaxHP)
		bossBody := buildMapBossList(int(mapId), uint32(stats.MaxHP))
		if len(bossBody) > 0 {
			// 地图进入时触发的 2021 视为服务器主动推送，统一使用 seq=0
			ctx.GameServer.SendResponse(ctx.ClientData, cmdMapBoss, ctx.UserID, 0, bossBody)
			ctx.GameServer.SendResponse(ctx.ClientData, cmdMapBoss, ctx.UserID, 0, bossBody)
			logger.Info(fmt.Sprintf("[2021] 推送地图 BOSS 列表: UID=%d MapID=%d PetID=%d hp=%d (x2 触发走动)", ctx.UserID, user.MapID, e.BossPetID, stats.MaxHP))
		}
	}
	// 赫尔卡星荒地(32) 雷雨天：推送 2021 含雷伊(id=70)，触发前端 BossCmdListener 派发 LY_OUT，播放雷伊出场动画
	if int(mapId) == gameogres.MapIDHelcarWasteland() && gameogres.IsLeiyiWeather() {
		leiyiBody := buildLeiyiBossBody()
		if len(leiyiBody) > 0 {
			ctx.GameServer.SendResponse(ctx.ClientData, cmdMapBoss, ctx.UserID, 0, leiyiBody)
			ctx.GameServer.SendResponse(ctx.ClientData, cmdMapBoss, ctx.UserID, 0, leiyiBody)
			logger.Info(fmt.Sprintf("[2021] 推送雷伊出场(触发 LY_OUT 动画): UID=%d MapID=%d", ctx.UserID, user.MapID))
		}
	}
	// 盖亚按周几出现在三张地图之一：进入“当日盖亚地图”时推送 2022(SPECIAL_PET_NOTE)，客户端显示可点击盖亚及对话今日条件
	if int(mapId) == getGaiyaMapIDForToday() {
		gaiyaNote := make([]byte, 8)
		binary.BigEndian.PutUint32(gaiyaNote[0:4], 1)   // show=1
		binary.BigEndian.PutUint32(gaiyaNote[4:8], petIDGaiya)
		ctx.GameServer.SendResponse(ctx.ClientData, cmdSpecialPetNote, ctx.UserID, 0, gaiyaNote)
		logger.Info(fmt.Sprintf("[2022] 推送盖亚出场(当日地图): UID=%d MapID=%d", ctx.UserID, user.MapID))
	}

	// 向自己推送 9003，使客户端 NonoManager.info 有完整数据（含 superStage），己方才能绘制跟随的 NoNo（解包：NonoModel 用 _info.superStage 拼 nono_N.swf）
	// 原“家园且跟随时不推”会导致己方在基地完全看不见 NoNo，故改为每次进图都推；若出现基地内多一只再按客户端逻辑收窄
	if user.Nono.SuperLevel > 0 {
		updateSuperNonoTypeByLevel(user)
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
	}
	nonoBody := buildNonoInfo(ctx.UserID, user)
	ctx.GameServer.SendResponse(ctx.ClientData, 9003, ctx.UserID, 0, nonoBody)
}

// pushOtherPlayersFlyAndNonoToClient 向刚进图的客户端补发同图其他玩家的 2112（飞行）、9019（NONO 跟随），
// 因部分客户端仅根据这两条协议更新他人飞行与 NONO 显示，不解析 2003 内的 actionType/State。
func pushOtherPlayersFlyAndNonoToClient(gs *gameserver.GameServer, targetClient *gameserver.ClientData, mapID int, excludeUserID int64) {
	clients := gs.GetClientsOnMap(mapID)
	for _, c := range clients {
		if c.UserID == excludeUserID {
			continue
		}
		other := gs.GetOrCreateUser(c.UserID)
		if other.FlyMode != 0 {
			body2112 := make([]byte, 8)
			binary.BigEndian.PutUint32(body2112[0:4], uint32(c.UserID))
			binary.BigEndian.PutUint32(body2112[4:8], uint32(other.FlyMode))
			// 包头用飞行者 userId，客户端据此识别“谁”在飞并刷新该玩家显示
			gs.SendResponse(targetClient, 2112, c.UserID, 0, body2112)
		}
		if other.Nono.State == 1 {
			body9019 := make([]byte, 36)
			binary.BigEndian.PutUint32(body9019[0:4], uint32(c.UserID))
			binary.BigEndian.PutUint32(body9019[4:8], uint32(other.Nono.SuperNono))
			binary.BigEndian.PutUint32(body9019[8:12], 1)
			nickBytes := []byte(other.Nono.Nick)
			if len(nickBytes) > 16 {
				nickBytes = nickBytes[:16]
			}
			copy(body9019[12:28], nickBytes)
			binary.BigEndian.PutUint32(body9019[28:32], uint32(other.Nono.Color))
			binary.BigEndian.PutUint32(body9019[32:36], 0) // power，可为 0
			gs.SendResponse(targetClient, 9019, c.UserID, 0, body9019)
		}
	}
}

// pushInitialMapEnter 在登录完成后主动推送一次进入默认地图/房间的相关封包
// 相当于模拟客户端发了一次 CMD=2001，然后服务器按同样逻辑回复 2001/2003/2004/9003
func pushInitialMapEnter(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// 使用与 1001 一致的地图：默认 ID=1 传送舱
	mapId := uint32(user.MapID)
	x := uint32(user.PosX)
	y := uint32(user.PosY)
	if mapId == 0 {
		mapId = 1
	}

	if x == 0 {
		x = 500
	}
	if y == 0 {
		y = 300
	}

	logger.Info(fmt.Sprintf(
		"自动进入默认地图: UID=%d MapID=%d X=%d Y=%d",
		ctx.UserID,
		mapId,
		x,
		y,
	))

	// 更新用户状态（与 handleEnterMap 一致）
	user.MapID = int(mapId)
	user.PosX = int(x)
	user.PosY = int(y)
	ctx.GameServer.AddUserToMap(int(mapId), ctx.UserID, ctx.ClientData)
	ctx.GameServer.SetOgreEnterMapTime(ctx.UserID)

	// 构建并发送 2001 / 2003 / 2004 / 9003；2003 含同地图所有人使其他玩家可见
	body := buildPeopleInfo(ctx.UserID, user, time.Now().Unix(), int(x), int(y), true)
	ctx.GameServer.SendResponse(ctx.ClientData, 2001, ctx.UserID, 0, body)
	listBody := buildMapPlayerListForMap(ctx.GameServer, int(mapId))
	ctx.GameServer.SendResponse(ctx.ClientData, 2003, ctx.UserID, 0, listBody)
	ctx.GameServer.BroadcastToMap(int(mapId), ctx.UserID, 2003, listBody)
	pushOtherPlayersFlyAndNonoToClient(ctx.GameServer, ctx.ClientData, int(mapId), ctx.UserID)

	slots := gameogres.GenerateNewSlotsNoCache(int(mapId))
	if len(slots) > 0 {
		ctx.GameServer.SetPlayerOgreSlots(ctx.UserID, int(mapId), slots)
		ogreBody := ctx.GameServer.BuildMapOgreListFromSlots(slots)
		ctx.GameServer.SendResponse(ctx.ClientData, 2004, ctx.UserID, 0, ogreBody)
	} else {
		ogreBody := buildMapOgreList(int(mapId))
		ctx.GameServer.SendResponse(ctx.ClientData, 2004, ctx.UserID, 0, ogreBody)
	}

	// 赫尔卡星荒地(32) 雷雨天：推送 2021 触发雷伊出场动画
	if int(mapId) == gameogres.MapIDHelcarWasteland() && gameogres.IsLeiyiWeather() {
		leiyiBody := buildLeiyiBossBody()
		if len(leiyiBody) > 0 {
			ctx.GameServer.SendResponse(ctx.ClientData, cmdMapBoss, ctx.UserID, 0, leiyiBody)
			ctx.GameServer.SendResponse(ctx.ClientData, cmdMapBoss, ctx.UserID, 0, leiyiBody)
		}
	}
	if int(mapId) == getGaiyaMapIDForToday() {
		gaiyaNote := make([]byte, 8)
		binary.BigEndian.PutUint32(gaiyaNote[0:4], 1)
		binary.BigEndian.PutUint32(gaiyaNote[4:8], petIDGaiya)
		ctx.GameServer.SendResponse(ctx.ClientData, cmdSpecialPetNote, ctx.UserID, 0, gaiyaNote)
	}

	// 向自己推送 9003，使客户端 NonoManager.info 有完整数据（含 superStage），己方才能绘制跟随的 NoNo
	if user.Nono.SuperLevel > 0 {
		updateSuperNonoTypeByLevel(user)
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
	}
	nonoBody := buildNonoInfo(ctx.UserID, user)
	ctx.GameServer.SendResponse(ctx.ClientData, 9003, ctx.UserID, 0, nonoBody)
}

// buildPeopleInfo 构建 2001/2003 用的用户信息体，严格对齐
// 前端 UserInfo.setForPeoleInfo 与 Lua map_handlers.buildPeopleInfo 的读写顺序
// fixedLen=true 用于 2001 单条包体（固定 144 字节）；fixedLen=false 用于 2003 列表项，保留装备数据供同图玩家可见
func buildPeopleInfo(userID int64, user *userdb.GameData, sysTime int64, posX, posY int, fixedLen bool) []byte {
	// 确保形态值根据超能等级更新，客户端用此值加载 nono_N.swf
	if user.Nono.SuperLevel > 0 {
		updateSuperNonoTypeByLevel(user)
	}
	buf := make([]byte, 0, 256)
	writeU32 := func(v uint32) {
		t := make([]byte, 4)
		binary.BigEndian.PutUint32(t, v)
		buf = append(buf, t...)
	}
	writeI32 := func(v int32) {
		writeU32(uint32(v))
	}
	writeU16 := func(v uint16) {
		t := make([]byte, 2)
		binary.BigEndian.PutUint16(t, v)
		buf = append(buf, t...)
	}
	writeFixed := func(s string, n int) {
		b := []byte(s)
		if len(b) > n {
			b = b[:n]
		}
		buf = append(buf, b...)
		for i := len(b); i < n; i++ {
			buf = append(buf, 0)
		}
	}

	if posX == 0 {
		posX = 500
	}
	if posY == 0 {
		posY = 300
	}

	// 1. 基本信息
	writeI32(int32(sysTime))
	writeU32(uint32(userID))
	nick := user.Nick
	if nick == "" {
		nick = fmt.Sprintf("Seer%d", userID)
	}
	writeFixed(nick, 16)
	writeU32(uint32(user.Color))
	if user.Texture == 0 {
		writeU32(0)
	} else {
		writeU32(uint32(user.Texture))
	}

	// 2. VIP (bit0=vip, bit1=viped)
	vipFlags := uint32(0)
	if user.Nono.SuperNono > 0 {
		vipFlags = 3
	}
	writeU32(vipFlags)
	writeU32(uint32(user.Nono.VipStage))

	// 3. 动作与坐标（actionType=flyMode，使别人可见飞行状态）
	actionType := uint32(user.FlyMode)
	writeU32(actionType)
	writeU32(uint32(posX))
	writeU32(uint32(posY))
	writeU32(0) // action
	writeU32(0) // direction
	writeU32(0) // changeShape

	// 4. 精灵（spiritTime=catchTime, spiritID=petId）— 仅使用当前跟随精灵，未设置跟随时不发首发精灵，避免别人看见“未设置跟随却显示首发跟随”
	var petID, catchTime, petDV uint32 = 0, 0, 31
	if user.FollowPetCatchTime > 0 {
		for _, p := range user.Pets {
			if p.CatchTime == user.FollowPetCatchTime {
				petID = uint32(p.ID)
				catchTime = uint32(p.CatchTime)
				if p.DV > 0 {
					petDV = uint32(p.DV)
				}
				break
			}
		}
		if petID == 0 && user.StoragePets != nil {
			for _, p := range user.StoragePets {
				if p.CatchTime == user.FollowPetCatchTime {
					petID = uint32(p.ID)
					catchTime = uint32(p.CatchTime)
					if p.DV > 0 {
						petDV = uint32(p.DV)
					}
					break
				}
			}
		}
	}
	writeU32(catchTime)
	writeU32(petID)
	writeU32(petDV)
	writeU32(0) // petSkin
	writeU32(0) // fightFlag

	// 5. 师徒
	writeU32(uint32(user.TeacherID))
	writeU32(uint32(user.StudentID))

	// 6. NoNo：客户端 setForPeoleInfo 将第一个 U32 按 32 位解析为 nonoState，nonoState[1]=跟随；
	//    后续顺序为 nonoColor(4), superNono(4), playerForm(4), transTime(4)。与 9003 形态一致。
	nonoStateBits := uint32(user.Nono.Flag)
	if user.Nono.State == 1 {
		nonoStateBits |= 2 // bit 1 = 跟随，客户端据此进 showNono 并显示飞船
	}
	writeU32(nonoStateBits)
	writeU32(uint32(user.Nono.Color))
	if user.Nono.SuperNono > 0 {
		writeU32(1)
	} else {
		writeU32(0)
	}
	writeU32(0) // playerForm
	writeU32(0) // transTime

	// 7. TeamInfo：id, coreCount, isShow, logoBg, logoIcon, logoColor, txtColor, logoWord(4)
	writeU32(0)
	writeU32(0)
	writeU32(0)
	writeU16(0)
	writeU16(0)
	writeU16(0)
	writeU16(0)
	writeFixed("", 4)

	// 8. Clothes：count + [id,level]
	writeU32(uint32(len(user.Clothes)))
	for _, cid := range user.Clothes {
		writeU32(uint32(cid))
		writeU32(0) // level，无则 0，与 Lua 一致
	}

	// 9. curTitle
	writeU32(uint32(user.CurTitle))

	const peopleInfoFixedLen = 144
	if fixedLen {
		// 2001 单条包体固定 144 字节，超长会致后续包错位、地图卡 100%
		if len(buf) > peopleInfoFixedLen {
			out := make([]byte, peopleInfoFixedLen)
			copy(out, buf[0:136])
			binary.BigEndian.PutUint32(out[136:140], 0) // clothes count=0
			copy(out[140:144], buf[len(buf)-4:])        // curTitle
			return out
		}
	}
	if len(buf) < peopleInfoFixedLen {
		return append(buf, make([]byte, peopleInfoFixedLen-len(buf))...)
	}
	return buf
}

// buildMapPlayerList 构建地图玩家列表响应（单条 peopleInfo，兼容旧调用）
func buildMapPlayerList(peopleInfo []byte) []byte {
	buffer := make([]byte, 4+len(peopleInfo))
	binary.BigEndian.PutUint32(buffer[0:4], 1)
	copy(buffer[4:], peopleInfo)
	return buffer
}

// buildMapPlayerListForMap 构建当前地图上所有玩家的 2003 列表（含自己），用于同地图其他玩家可见
func buildMapPlayerListForMap(gs *gameserver.GameServer, mapID int) []byte {
	clients := gs.GetClientsOnMap(mapID)
	if len(clients) == 0 {
		return make([]byte, 4) // count=0
	}
	var parts [][]byte
	now := time.Now().Unix()
	for _, c := range clients {
		user := gs.GetOrCreateUser(c.UserID)
		x, y := user.PosX, user.PosY
		if x == 0 {
			x = 500
		}
		if y == 0 {
			y = 300
		}
		parts = append(parts, buildPeopleInfo(c.UserID, user, now, x, y, false))
	}
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	buffer := make([]byte, 4+total)
	binary.BigEndian.PutUint32(buffer[0:4], uint32(len(parts)))
	off := 4
	for _, p := range parts {
		copy(buffer[off:], p)
		off += len(p)
	}
	return buffer
}

// CMD 2021 MAP_BOSS：客户端用此包在地图上显示 SPT BOSS（如蘑菇怪），BossCmdListener 解析后调用 BossController.add
const cmdMapBoss = 2021

// bossHpKey 返回 BOSS 血量缓存的 key
func bossHpKey(mapID int, region uint32) string {
	return fmt.Sprintf("%d_%d", mapID, region)
}

func getBossHp(userID int64, mapID int, region uint32) int {
	bossHpCacheMu.RLock()
	defer bossHpCacheMu.RUnlock()
	if m, ok := bossHpCache[userID]; ok {
		if hp, ok := m[bossHpKey(mapID, region)]; ok {
			return hp
		}
	}
	return 0 // 0 表示使用满血
}

func setBossHp(userID int64, mapID int, region uint32, hp int) {
	bossHpCacheMu.Lock()
	defer bossHpCacheMu.Unlock()
	if bossHpCache[userID] == nil {
		bossHpCache[userID] = make(map[string]int)
	}
	bossHpCache[userID][bossHpKey(mapID, region)] = hp
}

// pos=200 时客户端会移除该 region 的 BOSS（BossCmdListener）
const mapBossPosRemove = 200

// buildMapBossRemoveRegion 构建 2021 包体：移除指定 region 的 BOSS（pos=200）
func buildMapBossRemoveRegion(mapID int, region uint32) []byte {
	e, ok := sptboss.GetByMapAndParam(mapID, region)
	if !ok || !e.HasShield {
		return nil
	}
	body := make([]byte, 4+16)
	binary.BigEndian.PutUint32(body[0:4], 1)
	binary.BigEndian.PutUint32(body[4:8], uint32(e.BossPetID))
	binary.BigEndian.PutUint32(body[8:12], region)
	binary.BigEndian.PutUint32(body[12:16], 0)
	binary.BigEndian.PutUint32(body[16:20], mapBossPosRemove)
	return body
}

// buildLeiyiBossBody 构建雷伊的 MAP_BOSS(2021) 包体，用于触发前端 LY_OUT 出场动画
// 客户端 BossCmdListener 收到 id=70 后会派发 LY_OUT，MapProcess_32 播放 bossMc 动画
func buildLeiyiBossBody() []byte {
	body := make([]byte, 4+16)
	binary.BigEndian.PutUint32(body[0:4], 1)
	binary.BigEndian.PutUint32(body[4:8], 70)  // 雷伊 petID
	binary.BigEndian.PutUint32(body[8:12], 0)  // region=0
	binary.BigEndian.PutUint32(body[12:16], 0) // hp=0 无防护罩
	binary.BigEndian.PutUint32(body[16:20], 0) // pos=0
	return body
}

// buildMapBossList 构建 MAP_BOSS(2021) 响应体，用于在地图上显示 SPT BOSS
// 格式：len(4) + [id(4)+region(4)+hp(4)+pos(4)]*len；pos=200 表示移除该 region
// hp=0 表示无防护罩。仅对 OgreXMLInfo 有 boss 配置的地图推送（如克洛斯星密林 12 蘑菇怪）
func buildMapBossList(mapID int, hp uint32) []byte {
	e, ok := sptboss.GetByMapAndParam(mapID, 0)
	if !ok {
		return nil
	}
	// 仅有防护罩的 BOSS 需要推送 MAP_BOSS；无防护罩的由地图脚本直接触发战斗
	if !e.HasShield {
		return nil
	}
	body := make([]byte, 4+16)
	binary.BigEndian.PutUint32(body[0:4], 1)
	binary.BigEndian.PutUint32(body[4:8], uint32(e.BossPetID))
	binary.BigEndian.PutUint32(body[8:12], 0)   // region=0
	binary.BigEndian.PutUint32(body[12:16], hp) // 当前血量（0=无防护罩）
	binary.BigEndian.PutUint32(body[16:20], 0)  // pos=0
	return body
}

// buildMapOgreList 构建地图怪物列表响应
func buildMapOgreList(mapId int) []byte {
	// 结构：9 个槽位 * [petId(4) + shiny(4)] = 72 字节
	body := make([]byte, 72)
	index := 0

	slots := gameogres.GetSlots(mapId)

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

	logger.Info(fmt.Sprintf("[2004] MapOgreList: mapId=%d slots=%v", mapId, slots))
	return body
}

// buildNonoInfo 构建 9003 NoNo 信息响应，严格对齐客户端 NonoInfo(IDataInput) 的解析顺序：
// userID(4), flag(4), state(4), nick(16), superNono(4), color(4), power(4), mate(4), iq(4), ai(2),
// birth(4), chargeTime(4), func(20), superEnergy(4), superLevel(4), superStage(4)
func buildNonoInfo(userID int64, user *userdb.GameData) []byte {
	n := user.Nono
	buf := make([]byte, 0, 90)
	writeU32 := func(v uint32) {
		t := make([]byte, 4)
		binary.BigEndian.PutUint32(t, v)
		buf = append(buf, t...)
	}
	writeU16 := func(v uint16) {
		t := make([]byte, 2)
		binary.BigEndian.PutUint16(t, v)
		buf = append(buf, t...)
	}
	writeFixed := func(s string, nLen int) {
		b := []byte(s)
		if len(b) > nLen {
			b = b[:nLen]
		}
		buf = append(buf, b...)
		for i := len(b); i < nLen; i++ {
			buf = append(buf, 0)
		}
	}

	writeU32(uint32(userID))
	writeU32(uint32(n.Flag))
	writeU32(uint32(n.State))
	writeFixed(n.Nick, 16)
	writeU32(uint32(n.SuperNono)) // 形态 1-5，客户端加载 nono_N.swf
	writeU32(uint32(n.Color))
	writeU32(uint32(n.Power * 1000)) // 客户端 /1000
	writeU32(uint32(n.Mate * 1000))
	writeU32(uint32(n.IQ))
	writeU16(uint16(n.AI))
	if n.Birth > 0 {
		writeU32(uint32(n.Birth))
	} else {
		writeU32(uint32(time.Now().Unix()))
	}
	writeU32(uint32(n.ChargeTime))
	funcBits := make([]byte, 20)
	if len(n.Func) > 0 {
		copy(funcBits, n.Func)
	}
	buf = append(buf, funcBits...)
	writeU32(uint32(n.SuperEnergy))
	writeU32(uint32(n.SuperLevel))
	// 客户端用 superStage 拼 nono_N.swf，必须写形态值(1-5)；DB 的 SuperStage 可能为 0，故用 SuperNono（由 SuperLevel 换算）
	superStage := n.SuperNono
	if superStage < 1 {
		superStage = 1
	} else if superStage > 5 {
		superStage = 5
	}
	writeU32(uint32(superStage))

	logger.Info(fmt.Sprintf("[buildNonoInfo] UserID=%d SuperLevel=%d SuperNono形态=%d superStage=%d (应加载nono_%d.swf)",
		userID, n.SuperLevel, n.SuperNono, superStage, superStage))
	return buf
}

func handleLeaveMap(ctx *gameserver.HandlerContext) {
	// 1. 按协议回显 userID
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID))
	ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, body)
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	curMapID := user.MapID
	logger.Info(fmt.Sprintf("[2002] 离开地图: UID=%d MapID=%d", ctx.UserID, curMapID))
}

// handleListMapPlayer 处理地图玩家列表命令
func handleListMapPlayer(ctx *gameserver.HandlerContext) {
	// 客户端在地图初始化后会主动请求 LIST_MAP_PLAYER(2003)；返回同地图所有玩家（含自己）
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.MapID == 0 {
		user.MapID = 1
	}
	body := buildMapPlayerListForMap(ctx.GameServer, user.MapID)
	ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, body)
	// 主动请求 2003 时也补发同图他人的 2112/9019，便于客户端刷新后正确显示飞行与 NONO
	pushOtherPlayersFlyAndNonoToClient(ctx.GameServer, ctx.ClientData, user.MapID, ctx.UserID)
}

// handleMapHot CMD 1004 地图热点（宇宙地图热点数据）
// 客户端 MapHotInfo: count(4) + [id(4)+value(4)]*count；空热点则 count=0
func handleMapHot(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, 0) // 热点数量 0，客户端不显示热点
	ctx.GameServer.SendResponse(ctx.ClientData, 1004, ctx.UserID, ctx.SeqID, body)
}

// handleMapOgreList 处理地图怪物列表命令（与 Lua 一致：优先返回该玩家当前地图的槽位）
func handleMapOgreList(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	mapID := user.MapID
	if mapID == 0 {
		mapID = 1
	}
	slots := ctx.GameServer.GetPlayerOgreSlots(ctx.UserID, mapID)
	if slots == nil {
		slots = gameogres.GenerateNewSlotsNoCache(mapID)
		if len(slots) > 0 {
			ctx.GameServer.SetPlayerOgreSlots(ctx.UserID, mapID, slots)
		}
	}
	var body []byte
	if len(slots) > 0 {
		body = ctx.GameServer.BuildMapOgreListFromSlots(slots)
	} else {
		body = buildMapOgreList(mapID) // 无配置地图回退到旧逻辑
	}
	ctx.GameServer.SendResponse(ctx.ClientData, ctx.CmdID, ctx.UserID, ctx.SeqID, body)
}

// handleChat CMD 2102 聊天
// 请求: toID(4) + msgLen(4) + msg(msgLen 字节，UTF-8)
// 响应: ChatInfo = senderID(4) + senderNickName(16) + toID(4) + msgLen(4) + msg(msgLen)
func handleChat(ctx *gameserver.HandlerContext) {
	toID := uint32(0)
	msgLen := uint32(0)
	msgBytes := []byte(nil)
	if len(ctx.Body) >= 8 {
		toID = binary.BigEndian.Uint32(ctx.Body[0:4])
		msgLen = binary.BigEndian.Uint32(ctx.Body[4:8])
		if msgLen > 0 && len(ctx.Body) >= 8+int(msgLen) {
			msgBytes = ctx.Body[8 : 8+msgLen]
		} else if len(ctx.Body) > 8 {
			msgBytes = ctx.Body[8:]
			msgLen = uint32(len(msgBytes))
		}
	}
	// 客户端 ChatAction.as 会在每条消息末尾主动写入字符 "0"，回显时去掉避免显示成 "Bye0"
	if len(msgBytes) > 0 && msgBytes[len(msgBytes)-1] == '0' {
		msgBytes = msgBytes[:len(msgBytes)-1]
		msgLen = uint32(len(msgBytes))
	}
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	nick := user.Nick
	if nick == "" {
		nick = "赛尔"
	}
	nickBytes := []byte(nick)
	if len(nickBytes) > 16 {
		nickBytes = nickBytes[:16]
	}
	// ChatInfo: senderID(4) + senderNickName(16) + toID(4) + msgLen(4) + msg
	body := make([]byte, 0, 4+16+4+4+len(msgBytes))
	buf4 := make([]byte, 4)
	binary.BigEndian.PutUint32(buf4, uint32(ctx.UserID))
	body = append(body, buf4...)
	nickPad := make([]byte, 16)
	copy(nickPad, nickBytes)
	body = append(body, nickPad...)
	binary.BigEndian.PutUint32(buf4, toID)
	body = append(body, buf4...)
	binary.BigEndian.PutUint32(buf4, msgLen)
	body = append(body, buf4...)
	body = append(body, msgBytes...)
	ctx.GameServer.SendResponse(ctx.ClientData, 2102, ctx.UserID, ctx.SeqID, body)
}

// handleAimat CMD 2104 射击/瞄准（AIMAT）
// 对齐 AS3: BasePeoleModel.aimatAction 发送 itemID, type, x, y；AimatCmdListener.onAimat 期望响应 userID(4)+itemID(4)+type(4)+x(4)+y(4)
// 响应并广播给同地图其他玩家，使其他玩家能看到射击动作
func handleAimat(ctx *gameserver.HandlerContext) {
	itemID := uint32(0)
	aimType := uint32(0)
	x := uint32(0)
	y := uint32(0)
	if len(ctx.Body) >= 16 {
		itemID = binary.BigEndian.Uint32(ctx.Body[0:4])
		aimType = binary.BigEndian.Uint32(ctx.Body[4:8])
		x = binary.BigEndian.Uint32(ctx.Body[8:12])
		y = binary.BigEndian.Uint32(ctx.Body[12:16])
	}
	// 响应: userID(4) + itemID(4) + type(4) + x(4) + y(4)，供客户端 dispatchAction 显示射击
	body := make([]byte, 20)
	binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID))
	binary.BigEndian.PutUint32(body[4:8], itemID)
	binary.BigEndian.PutUint32(body[8:12], aimType)
	binary.BigEndian.PutUint32(body[12:16], x)
	binary.BigEndian.PutUint32(body[16:20], y)
	ctx.GameServer.SendResponse(ctx.ClientData, 2104, ctx.UserID, ctx.SeqID, body)
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.MapID > 0 {
		ctx.GameServer.BroadcastToMap(user.MapID, ctx.UserID, 2104, body)
	}
}

// handleTransformUser CMD 2107 射击命中后变身（TRANSFORM_USER）
// 请求: targetUserID(4) + transformId(4) = 8 字节（AimatCmdListener.send(TRANSFORM_USER, _loc2_.info.userID, uint(_loc3_[0]))）
// 响应: 回显相同 8 字节；并向同地图广播 CMD 2108 NOTE_TRANSFORM_USER，body 为 targetUserID(4)+transformId(4)+0(4)，供其他客户端显示变身
func handleTransformUser(ctx *gameserver.HandlerContext) {
	targetUserID := uint32(0)
	transformId := uint32(0)
	if len(ctx.Body) >= 8 {
		targetUserID = binary.BigEndian.Uint32(ctx.Body[0:4])
		transformId = binary.BigEndian.Uint32(ctx.Body[4:8])
	}
	body := make([]byte, 8)
	binary.BigEndian.PutUint32(body[0:4], targetUserID)
	binary.BigEndian.PutUint32(body[4:8], transformId)
	ctx.GameServer.SendResponse(ctx.ClientData, 2107, ctx.UserID, ctx.SeqID, body)
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.MapID > 0 {
		noteBody := make([]byte, 12)
		binary.BigEndian.PutUint32(noteBody[0:4], targetUserID)
		binary.BigEndian.PutUint32(noteBody[4:8], transformId)
		binary.BigEndian.PutUint32(noteBody[8:12], 0)
		ctx.GameServer.BroadcastToMap(user.MapID, 0, 2108, noteBody)
	}
}

// handleHeartbeat 处理心跳包 CMD 80008
func handleHeartbeat(ctx *gameserver.HandlerContext) {
	// 客户端心跳，无需回包；勇者之塔选层刷新需客户端在回到塔界面时主动发 2414
}

// ==================== 战斗命令（最小可用版本）====================

// buildAttackValue 构建攻击值（CMD 2505）
// 对齐客户端 AttackValue：userId(4)+skillId(4)+atkTimes(4)+lostHP(4)+gainHP(4)+remainHp(4)+maxHp(4)+state(4)+
// skillListCount(4)+[PetSkillInfo]*N+isCrit(4)+status(20)+battleLv(6)+maxShield(4)+curShield(4)+petType(4)，N=0 时共 82 字节
// status/battleLv 用于技能附加效果（烧伤、中毒、属性升降等），与 Lua buildAttackValue 一致
func buildAttackValue(userID uint32, skillID uint32, atkTimes uint32, lostHP uint32, gainHP int32, remainHP int32, maxHP uint32, state uint32, isCrit uint32, petType uint32, status [20]byte, battleLv [6]int8) []byte {
	if maxHP == 0 {
		maxHP = 1
	}
	maxHP32 := int32(maxHP)
	if remainHP < 0 {
		remainHP = 0
	}
	if remainHP > maxHP32 {
		remainHP = maxHP32
	}
	// 回血时客户端会 hp += gainHP 再钳位，限制 gainHP 使 (remainHP+gainHP) 不超过 maxHP，血条到顶只显示加血数值
	if gainHP > 0 {
		if remainHP >= maxHP32 {
			gainHP = 0
		} else if int64(remainHP)+int64(gainHP) > int64(maxHP32) {
			gainHP = maxHP32 - remainHP
		}
	}
	const attackValueSize = 82 // 9*4 + skillListCount(4) + isCrit(4) + status(20) + battleLv(6) + maxShield(4) + curShield(4) + petType(4)
	buf := make([]byte, attackValueSize)
	off := 0
	binary.BigEndian.PutUint32(buf[off:off+4], userID)
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], skillID)
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], atkTimes)
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], lostHP)
	off += 4
	// gainHP 和 remainHP 是有符号整数，需要正确转换
	if gainHP < 0 {
		binary.BigEndian.PutUint32(buf[off:off+4], uint32(math.MaxUint32+int64(gainHP)+1))
	} else {
		binary.BigEndian.PutUint32(buf[off:off+4], uint32(gainHP))
	}
	off += 4
	if remainHP < 0 {
		binary.BigEndian.PutUint32(buf[off:off+4], uint32(math.MaxUint32+int64(remainHP)+1))
	} else {
		binary.BigEndian.PutUint32(buf[off:off+4], uint32(remainHP))
	}
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], maxHP)
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], state)
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], 0) // skillListCount = 0
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], isCrit)
	off += 4
	// status 20 bytes（异常状态：0=无 1=中毒 2=烧伤等）
	copy(buf[off:off+20], status[:])
	off += 20
	// battleLv 6 bytes 有符号（攻击/防御/特攻/特防/速度/命中 等级 -6~+6）
	for i := 0; i < 6; i++ {
		buf[off] = byte(uint8(battleLv[i]))
		off++
	}
	binary.BigEndian.PutUint32(buf[off:off+4], 0) // maxShield
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], 0) // curShield
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], petType)
	off += 4
	return buf[:off]
}

// capDisplayHP 将 HP 限制在前端可安全显示的上限内，避免血条超出血槽。
// 服务端 Boss 真实血量可设至 99999999，结算用 BattleState 中的值；仅 2504/2505 下发的显示值按比例压缩到 displayHPMax，客户端用 (dispHP/dispMax) 绘制比例，血条不溢出。
const displayHPMax = 9999

func capDisplayHP(hp, maxHP uint32) (uint32, uint32) {
	if maxHP == 0 {
		return 0, 1
	}
	if hp > maxHP {
		hp = maxHP
	}
	if maxHP <= displayHPMax {
		return hp, maxHP
	}
	// 按比例压缩到 displayHPMax 内，避免客户端血条溢出
	dispMax := uint32(displayHPMax)
	dispHP := hp * dispMax / maxHP
	if dispHP == 0 && hp > 0 {
		dispHP = 1
	}
	return dispHP, dispMax
}

// pushMultiEnemySwitch2504_2505 多敌方战斗（勇者之塔/试炼之塔等）中“当前只胜利、切下一只”时：只发 2407+2505，不发 2503/2504。
// 更新 battle 的 EnemyID/HP/MaxHP 与 TowerBossIndex 或 FreshBossIndex；用 2407(CHANGE_PET,userID=0) 通知敌方切换，避免再发 2504 导致客户端误当作新战斗重复 setup()、addFightUI() 造成我方模型重叠（解包：onStartFight 收到 2504 且 hashMap.length<2 时会 full setup，出现两个我方精灵）。
// useTowerIndex：true=写 battle.TowerBossIndex，false=写 battle.FreshBossIndex（试炼之塔）。
func pushMultiEnemySwitch2504_2505(ctx *gameserver.HandlerContext, battle *gameserver.BattleState, user *userdb.GameData, nextIdx, nextBossID, enemyLevel int, useTowerIndex bool, battleLabel string) {
	// 敌方精灵被击败后延迟 2 秒再切换下一只，便于玩家看清击败结果
	time.Sleep(2 * time.Second)
	petMgr := gamepets.GetInstance()
	enemyEV := gamepets.EVStats{}
	enemyStats := petMgr.GetStats(nextBossID, enemyLevel, 15, enemyEV, 0)
	enemyStats.HP = applyBossHPOverride(nextBossID, enemyStats.HP)
	enemyStats.MaxHP = applyBossHPOverride(nextBossID, enemyStats.MaxHP)
	if useTowerIndex && battle.TowerLevel > 0 {
		ScaleFightLevelStats(&enemyStats, battle.TowerLevel)
	}
	ctx.GameServer.BattleMu.Lock()
	battle.EnemyID = nextBossID
	battle.EnemyLevel = enemyLevel
	battle.EnemyHP = uint32(enemyStats.HP)
	battle.EnemyMaxHP = uint32(enemyStats.MaxHP)
	if useTowerIndex {
		battle.TowerBossIndex = nextIdx
	} else {
		battle.FreshBossIndex = nextIdx
	}
	battle.RoundCount = 0
	battle.EnemyBattleLv = [6]int8{}
	battle.EnemyStatus = [20]byte{}
	// 185 - 若上一只被击败时处于 XX 状态，则本只出场也进入 XX 状态
	if battle.PlayerTransferStatusToNextEnemy > 0 && battle.PlayerTransferStatusToNextEnemy < 20 {
		battle.EnemyStatus[battle.PlayerTransferStatusToNextEnemy] = byte(rand.Intn(2) + 2)
		battle.PlayerTransferStatusToNextEnemy = 0
	}
	battle.EnemyCritBuffRounds = 0
	battle.LastHitWasCrit = false
	ctx.GameServer.BattleStates[ctx.UserID] = battle
	ctx.GameServer.BattleMu.Unlock()

	enemyDispHP, enemyDispMax := capDisplayHP(battle.EnemyHP, battle.EnemyMaxHP)
	enemyName := petMgr.GetName(nextBossID)
	if enemyName == "" {
		enemyName = "野生精灵"
	}
	enemyCatchTime := uint32(nextIdx)
	// 先发 2407（敌方切换）：客户端 userID=0 走 NpcChangePetData.add，nextRound 时 getFighterMode(0).changePet -> setPetMC 只更新敌方视图，不会触发 setup()
	body2407 := buildChangePetInfoBody(0, uint32(nextBossID), enemyName, uint32(enemyLevel), uint32(enemyDispHP), enemyDispMax, enemyCatchTime)
	ctx.GameServer.SendResponse(ctx.ClientData, 2407, ctx.UserID, 0, body2407)
	// 再延迟 400ms 发 2505，确保客户端先处理 2407、nextRound 里完成 changePet（removeAllChild+新模型）后再播本回合攻击
	time.Sleep(400 * time.Millisecond)
	playerAv := buildAttackValue(uint32(ctx.UserID), 0, 0, 0, 0, int32(battle.PlayerHP), battle.PlayerMaxHP, 0, 0, 0, battle.PlayerStatus, battle.PlayerBattleLv)
	enemyAv := buildAttackValue(0, 0, 0, 0, 0, int32(enemyDispHP), enemyDispMax, 0, 0, 0, battle.EnemyStatus, battle.EnemyBattleLv)
	body2505 := make([]byte, 0, 164)
	body2505 = append(body2505, playerAv...)
	body2505 = append(body2505, enemyAv...)
	ctx.GameServer.SendResponse(ctx.ClientData, 2505, ctx.UserID, 0, body2505)
	logger.Info(fmt.Sprintf("[%s] 多精灵战斗: 第 %d 只胜利 -> 切换第 %d 只 EnemyID=%d(%s) Level=%d catchTime=%d 已推2407+2505(不推2503/2504 防我方模型重叠)", battleLabel, nextIdx, nextIdx+1, nextBossID, enemyName, enemyLevel, nextIdx))
}

// puniDisplayHP 将谱尼真实 HP 映射为前端显示用的“伪血量”，保持比例一致且防止血条超出血槽。
// realHP/realMax 为 BattleState 中的真实体力；显示压缩到 0~999，客户端用 (dispHP/dispMax) 绘制比例。
const puniDisplayMaxHP = 999

func puniDisplayHP(realHP, realMax uint32) (uint32, uint32) {
	if realMax == 0 {
		return 0, 1
	}
	if realHP > realMax {
		realHP = realMax
	}
	dispMax := uint32(puniDisplayMaxHP)
	dispHP := realHP * dispMax / realMax
	if dispHP == 0 && realHP > 0 {
		dispHP = 1
	}
	return dispHP, dispMax
}

// 盖亚精元物品 ID；COMPLETE_TASK(2202) / SPRINT_GIFT_NOTICE(8010) / SPECIAL_PET_NOTE(2022) 协议号
const (
	itemIDGaiyaEssence   = 400126
	cmdCompleteTask      = 2202
	cmdSprintGiftNotice  = 8010
	cmdSpecialPetNote    = 2022   // 客户端显示/隐藏盖亚出场，body: show(4)+petID(4)
	taskIDGaiya          = 99
	petIDGaiya           = 261
)

// getGaiyaMapIDForToday 返回当日盖亚出现的地图 ID（15 火山星 / 54 露西欧星 / 105 双子阿尔法星），供进入地图时推送 2022 显示盖亚
func getGaiyaMapIDForToday() int {
	weekday := int(time.Now().Weekday()) // 0=Sun, 1=Mon, ..., 6=Sat
	switch weekday {
	case 0, 2, 4:
		return 54
	case 1, 5:
		return 15
	case 3, 6:
		return 105
	default:
		return 54
	}
}

// pushGaiyaRewardOrNotice 盖亚战斗胜利后：按周几+地图+条件决定推送 COMPLETE_TASK(含精元) 或 SPRINT_GIFT_NOTICE(未按规则)
// 三地图：火山星15=两回合内击败(周一、周五)，露西欧星54=致命一击击败(周二、周四、周日)，双子阿尔法星105=十回合后击败(周三、周六)
func pushGaiyaRewardOrNotice(ctx *gameserver.HandlerContext, battle *gameserver.BattleState, user *userdb.GameData) {
	requiredMap := getGaiyaMapIDForToday()
	if battle.BattleMapID != requiredMap {
		ctx.GameServer.SendResponse(ctx.ClientData, cmdSprintGiftNotice, ctx.UserID, 0, []byte{})
		logger.Info(fmt.Sprintf("[盖亚] 地图不匹配: mapID=%d 今日应为 map=%d，推送 8010", battle.BattleMapID, requiredMap))
		return
	}
	conditionOK := false
	switch requiredMap {
	case 15:
		conditionOK = battle.RoundCount <= 2
	case 54:
		conditionOK = battle.LastHitWasCrit
	case 105:
		conditionOK = battle.RoundCount > 10
	}
	if !conditionOK {
		ctx.GameServer.SendResponse(ctx.ClientData, cmdSprintGiftNotice, ctx.UserID, 0, []byte{})
		logger.Info(fmt.Sprintf("[盖亚] 条件未满足: map=%d Round=%d Crit=%v，推送 8010", requiredMap, battle.RoundCount, battle.LastHitWasCrit))
		return
	}
	// 发放盖亚精元并推送 COMPLETE_TASK(2202) 供前端弹窗
	if user.Items == nil {
		user.Items = make(map[string]userdb.Item)
	}
	itemKey := strconv.FormatUint(uint64(itemIDGaiyaEssence), 10)
	if existing, ok := user.Items[itemKey]; ok {
		existing.Count++
		user.Items[itemKey] = existing
	} else {
		user.Items[itemKey] = userdb.Item{Count: 1, ExpireTime: 0x057E40}
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	body := buildGaiyaCompleteTaskBody()
	ctx.GameServer.SendResponse(ctx.ClientData, cmdCompleteTask, ctx.UserID, 0, body)
	logger.Info(fmt.Sprintf("[盖亚] 条件满足: map=%d 发放精元 400126，推送 2202 taskID=99", requiredMap))
}

// buildGaiyaCompleteTaskBody NoviceFinishInfo: taskID(4)+petID(4)+captureTm(4)+itemCount(4)+[itemID(4)+itemCnt(4)]...
func buildGaiyaCompleteTaskBody() []byte {
	buf := make([]byte, 16+8) // 16 + 1*8
	binary.BigEndian.PutUint32(buf[0:4], taskIDGaiya)
	binary.BigEndian.PutUint32(buf[4:8], 0)
	binary.BigEndian.PutUint32(buf[8:12], 0)
	binary.BigEndian.PutUint32(buf[12:16], 1)
	binary.BigEndian.PutUint32(buf[16:20], itemIDGaiyaEssence)
	binary.BigEndian.PutUint32(buf[20:24], 1)
	return buf
}

// buildBossMonster8004Body 构建 CMD 8004 (GET_BOSS_MONSTER) 响应体，用于首次击败 SPT BOSS 后推送奖励通知
// 客户端 BossCmdListener 收到后弹窗显示"获得精元/精灵"。格式：bonusID(4)+petID(4)+captureTm(4)+itemCount(4)+[itemID(4)+itemCnt(4)]*n
// itemID/itemCnt 为 0 时 itemCount=0（仅精灵奖励）；否则 itemCount=1
func buildBossMonster8004Body(bonusID, petID, captureTm, itemID, itemCnt uint32) []byte {
	itemCount := uint32(0)
	if itemCnt > 0 {
		itemCount = 1
	}
	body := make([]byte, 16+int(itemCount)*8)
	binary.BigEndian.PutUint32(body[0:4], bonusID)
	binary.BigEndian.PutUint32(body[4:8], petID)
	binary.BigEndian.PutUint32(body[8:12], captureTm)
	binary.BigEndian.PutUint32(body[12:16], itemCount)
	if itemCount > 0 {
		binary.BigEndian.PutUint32(body[16:20], itemID)
		binary.BigEndian.PutUint32(body[20:24], itemCnt)
	}
	return body
}

// truncateUTF8ToBytes 将 s 截断为最多 maxBytes 个 UTF-8 字节，不切断多字节字符，保证客户端 readUTFBytes(16) 解析正确
func truncateUTF8ToBytes(s string, maxBytes int) []byte {
	nb := []byte(s)
	if len(nb) <= maxBytes {
		return nb
	}
	nb = nb[:maxBytes]
	for len(nb) > 0 && !utf8.FullRune(nb) {
		nb = nb[:len(nb)-1]
	}
	return nb
}

// buildFightPetInfo50 构建 50 字节 FightPetInfo，供 2504 等使用
// 格式: userID(4)+petID(4)+petName(16)+catchTime(4)+hp(4)+maxHP(4)+lv(4)+catchable(4)+battleLv(6)
func buildFightPetInfo50(uid uint32, petID int, name string, ct uint32, hp, maxHP, lv int, catchable uint32, battleLv [6]int8) []byte {
	if petID <= 0 {
		petID = 7
	}
	if maxHP <= 0 {
		maxHP = 1
	}
	if hp < 0 {
		hp = 0
	}
	if hp > maxHP {
		hp = maxHP
	}
	buf := make([]byte, 4+4+16+4+4+4+4+4+6)
	off := 0
	binary.BigEndian.PutUint32(buf[off:off+4], uid)
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], uint32(petID))
	off += 4
	// 客户端 FightPetInfo 用 readUTFBytes(16) 读名字，必须 16 字节且尾部未写部分为零，否则对战信息“对方【xxx】登场了!”可能显示错名或乱码
	nb := truncateUTF8ToBytes(name, 16)
	copy(buf[off:off+16], nb)
	for i := len(nb); i < 16; i++ {
		buf[off+i] = 0
	}
	off += 16
	binary.BigEndian.PutUint32(buf[off:off+4], ct)
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], uint32(hp))
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], uint32(maxHP))
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], uint32(lv))
	off += 4
	binary.BigEndian.PutUint32(buf[off:off+4], catchable)
	off += 4
	for i := 0; i < 6; i++ {
		buf[off+i] = byte(uint8(battleLv[i]))
	}
	return buf
}

// buildChangePetInfoBody 构建 40 字节 ChangePetInfo，与客户端解析一致：userID(4)+petID(4)+petName(16)+level(4)+hp(4)+maxHp(4)+catchTime(4)
// 用于 2407 响应（玩家切换）及服务端推送 2407（敌方切换，userID=0），客户端 onChangePet 收到 userID=0 会 NpcChangePetData.add，nextRound 时 getFighterMode(0).changePet
func buildChangePetInfoBody(userID, petID uint32, name string, level, hp, maxHP uint32, catchTime uint32) []byte {
	if maxHP == 0 {
		maxHP = 1
	}
	if hp > maxHP {
		hp = maxHP
	}
	buf := make([]byte, 40)
	binary.BigEndian.PutUint32(buf[0:4], userID)
	binary.BigEndian.PutUint32(buf[4:8], petID)
	nb := truncateUTF8ToBytes(name, 16)
	copy(buf[8:24], nb)
	for i := len(nb); i < 16; i++ {
		buf[8+i] = 0
	}
	binary.BigEndian.PutUint32(buf[24:28], level)
	binary.BigEndian.PutUint32(buf[28:32], hp)
	binary.BigEndian.PutUint32(buf[32:36], maxHP)
	binary.BigEndian.PutUint32(buf[36:40], catchTime)
	return buf
}

// buildFightOverInfo 构建战斗结束信息（CMD 2506）
// 对齐 Lua: buildFightOverInfo
// reason(4) + winnerId(4) + twoTimes(4) + threeTimes(4) + autoFightTimes(4) + energyTimes(4) + learnTimes(4) = 28 bytes
func buildFightOverInfo(reason uint32, winnerID uint32) []byte {
	buf := make([]byte, 28)
	binary.BigEndian.PutUint32(buf[0:4], reason)
	binary.BigEndian.PutUint32(buf[4:8], winnerID)
	binary.BigEndian.PutUint32(buf[8:12], 0)  // twoTimes
	binary.BigEndian.PutUint32(buf[12:16], 0) // threeTimes
	binary.BigEndian.PutUint32(buf[16:20], 0) // autoFightTimes
	binary.BigEndian.PutUint32(buf[20:24], 0) // energyTimes
	binary.BigEndian.PutUint32(buf[24:28], 0) // learnTimes
	return buf
}

// mapIDSpaceStationFlank 空间站侧翼地图 ID，退出勇者之塔后返回此地
const mapIDSpaceStationFlank = 5

// pushMapOgreListAfterFightOver 对战/捕捉/逃跑结束后：记录战毕时间、清空该玩家该地图槽位，推送空精灵列表；12 秒后定时器会重新生成 3 只并推送（与 Lua 一致）。
// skip2004ForTower：当本场战斗为勇者之塔(BattleMapID==500)时传 true，不推送空 2004，避免当层被击败后一层不刷新（进塔时 user.MapID 可能仍为进塔前地图如 108）。
func pushMapOgreListAfterFightOver(ctx *gameserver.HandlerContext, skip2004ForTower bool) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	mapID := user.MapID
	if mapID == 0 {
		mapID = 1
	}
	ctx.GameServer.SetOgreFightEndTime(ctx.UserID)
	if skip2004ForTower {
		logger.Info(fmt.Sprintf("[2004] 勇者之塔对战结束，不推送空精灵列表以保持层刷新: UID=%d", ctx.UserID))
		return
	}
	ctx.GameServer.ClearPlayerOgreSlots(ctx.UserID, mapID)
	ogreBody := ctx.GameServer.BuildMapOgreListFromSlots(nil) // nil 或空切片得到 9 格全 0
	ctx.GameServer.SendResponse(ctx.ClientData, 2004, ctx.UserID, 0, ogreBody)
	logger.Info(fmt.Sprintf("[2004] 对战结束推送空精灵列表，12秒后定时刷新: UID=%d MapID=%d", ctx.UserID, mapID))
}

// handleUseSkill CMD 2405 使用技能（带战斗状态管理）
// 对齐 Lua: fight_handlers.handleUseSkill
func handleUseSkill(ctx *gameserver.HandlerContext) {
	var skillID uint32
	if len(ctx.Body) >= 4 {
		skillID = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// ACK
	ctx.GameServer.SendResponse(ctx.ClientData, 2405, ctx.UserID, ctx.SeqID, []byte{})

	// 获取战斗状态
	ctx.GameServer.BattleMu.Lock()
	battle, exists := ctx.GameServer.BattleStates[ctx.UserID]
	if !exists || !battle.IsActive {
		ctx.GameServer.BattleMu.Unlock()
		logger.Warning(fmt.Sprintf("[2405] 战斗状态不存在或已结束，无法使用技能"))
		// 返回错误或结束战斗
		overBody := buildFightOverInfo(0, 0)
		ctx.GameServer.SendResponse(ctx.ClientData, 2506, ctx.UserID, ctx.SeqID, overBody)
		pushMapOgreListAfterFightOver(ctx, false)
		return
	}
	// 注意：保持锁定直到更新完战斗状态

	// 盖亚挑战条件：每使用一次技能计为一回合；同时重置谱尼部分阶段性统计
	battle.RoundCount++
	// 谱尼能量封印：每回合开始时清空本回合累计伤害
	battle.PuniEnergyDamageThisTurn = 0
	// 谱尼生命封印：清空上一击伤害记录
	battle.PuniLifeLastHitDamage = 0
	logger.Info(fmt.Sprintf("[2405] 使用技能前: PlayerHP=%d/%d EnemyHP=%d/%d Round=%d",
		battle.PlayerHP, battle.PlayerMaxHP, battle.EnemyHP, battle.EnemyMaxHP, battle.RoundCount))

	// 回合开始：结算异常状态伤害（烧伤/中毒每回合扣 1/8 最大HP，对齐 Lua processStatusEffects）
	gameskills.ProcessStatusEffects(
		&battle.PlayerHP, &battle.EnemyHP,
		battle.PlayerMaxHP, battle.EnemyMaxHP,
		&battle.PlayerStatus, &battle.EnemyStatus)
	// 谱尼生命封印（第四封印）：每回合开始自动回复体力（从配置读取）
	// 真身第四条命沿用该规则。
	if (battle.BattleMapID == 108 || battle.BattleMapID == 514) && battle.EnemyID == 300 &&
		(battle.PuniDoorIndex == 4 || (battle.PuniDoorIndex == 8 && battle.PuniTrueFormLifeIndex == 4)) &&
		battle.EnemyHP > 0 {
		var healPerTurn uint32 = 2000 // 默认值
		if cfg, ok := sptboss.GetPuniSealConfig(4); ok && cfg.LifeHealPerTurn > 0 {
			healPerTurn = uint32(cfg.LifeHealPerTurn)
		}
		newHP := battle.EnemyHP + healPerTurn
		if newHP > battle.EnemyMaxHP {
			newHP = battle.EnemyMaxHP
		}
		battle.EnemyHP = newHP
	}
	// 尤纳斯(132) phase 1：强制锁 1 血，仅里奥斯幻影可击杀（固定伤害/异常 DoT 不能致死）
	if sptboss.IsYouNaSiBoss(battle.EnemyID) && battle.YouNaSiPhase == 1 && battle.EnemyHP == 0 {
		battle.EnemyHP = 1
	}

	// 我方当前出战精灵 HP 已为 0 时，不允许再出招：只发 2505（双方 0 伤害），然后判定结束或等待 2407 换宠
	// 当前精灵已阵亡（可能在本回合 2405 内被击败，也可能在 2409 捕捉失败后的敌方回合等路径被击败），
	// 若未在“使用技能后”中递增 DeadPlayerPets，这里需把当前这只计入有效阵亡数，否则会出现“背包全部阵亡仍不结束战斗”
	if battle.PlayerHP == 0 {
		totalPets := battle.TotalPlayerPets
		if totalPets <= 0 {
			totalPets = len(user.Pets)
		}
		effectiveDead := battle.DeadPlayerPets + 1 // 当前出战精灵已 0 血，视为已阵亡
		remaining := totalPets - effectiveDead
		enemyUserID := uint32(0)
		if battle.OpponentUserID != 0 {
			enemyUserID = uint32(battle.OpponentUserID)
		}
		enemyRemainForAv := int32(battle.EnemyHP)
		enemyMaxForAv := battle.EnemyMaxHP
		// 谱尼战：对占位 2505 也使用伪血量，保持血条与普通攻击时一致
		if (battle.BattleMapID == 108 || battle.BattleMapID == 514) && battle.EnemyID == 300 {
			dispHP, dispMax := puniDisplayHP(battle.EnemyHP, battle.EnemyMaxHP)
			enemyRemainForAv = int32(dispHP)
			enemyMaxForAv = dispMax
		}
		enemyAv := buildAttackValue(enemyUserID, 0, 0, 0, 0, enemyRemainForAv, enemyMaxForAv, 0, 0, 0, battle.EnemyStatus, battle.EnemyBattleLv)
		playerAv := buildAttackValue(uint32(ctx.UserID), 0, 0, 0, 0, 0, battle.PlayerMaxHP, 0, 0, 0, battle.PlayerStatus, battle.PlayerBattleLv)
		body := make([]byte, 0, 164)
		if sptboss.IsFirstStrikeBoss(battle.EnemyID) {
			body = append(body, enemyAv...)
			body = append(body, playerAv...)
		} else {
			body = append(body, playerAv...)
			body = append(body, enemyAv...)
		}
		ctx.GameServer.BattleMu.Unlock()
		ctx.GameServer.SendResponse(ctx.ClientData, 2505, ctx.UserID, ctx.SeqID, body)
		if remaining <= 0 {
			overBody := buildFightOverInfo(0, 0)
			ctx.GameServer.SendResponse(ctx.ClientData, 2506, ctx.UserID, ctx.SeqID, overBody)
			pushMapOgreListAfterFightOver(ctx, battle.BattleMapID == 500)
			if battle.EnemyID == petIDGaiya {
				gaiyaMap := getGaiyaMapIDForToday()
				if user.MapID != 0 && user.MapID == gaiyaMap {
					gaiyaNote := make([]byte, 8)
					binary.BigEndian.PutUint32(gaiyaNote[0:4], 1)
					binary.BigEndian.PutUint32(gaiyaNote[4:8], petIDGaiya)
					ctx.GameServer.SendResponse(ctx.ClientData, cmdSpecialPetNote, ctx.UserID, 0, gaiyaNote)
				}
			}
			ctx.GameServer.BattleMu.Lock()
			delete(ctx.GameServer.BattleStates, ctx.UserID)
		if battle.OpponentUserID != 0 {
			delete(ctx.GameServer.BattleStates, battle.OpponentUserID)
			ctx.GameServer.BattleMu.Unlock()
			if otherClient := ctx.GameServer.GetClientByUserID(battle.OpponentUserID); otherClient != nil {
				ctx.GameServer.SendResponse(otherClient, 2506, battle.OpponentUserID, 0, overBody)
			}
			} else {
				ctx.GameServer.BattleMu.Unlock()
			}
		} else {
			// 多精灵：当前精灵被击败且还有后备时，自动切换为下一只可用精灵并推送 2407，客户端无需手动换宠
			petMgr := gamepets.GetInstance()
			nextIndex := -1
			for i := 1; i < len(user.Pets); i++ {
				idx := (battle.ActivePetIndex + i) % len(user.Pets)
				p := &user.Pets[idx]
				ev := p.GetEVStats()
				st := petMgr.GetStats(p.ID, p.Level, p.DV, ev, p.Nature)
				if st.HP > 0 {
					nextIndex = idx
					break
				}
			}
			if nextIndex >= 0 {
				ctx.GameServer.BattleMu.Lock()
				battle.DeadPlayerPets++
				battle.ActivePetIndex = nextIndex
				picked := &user.Pets[nextIndex]
				battle.PlayerStatus = [20]byte{}
				battle.PlayerBattleLv = [6]int8{}
				// 59 - 牺牲强化下一只：如果上一只精灵有牺牲强化效果且被击败，给新精灵应用强化
				if battle.PlayerSacrificeBuffActive {
					for i := 0; i < 6; i++ {
						if battle.PlayerSacrificeBuffStats[i] > 0 {
							cur := int(battle.PlayerBattleLv[i])
							cur += int(battle.PlayerSacrificeBuffStats[i])
							if cur > 6 {
								cur = 6
							}
							battle.PlayerBattleLv[i] = int8(cur)
						}
					}
					// 应用后清除牺牲强化标记
					battle.PlayerSacrificeBuffActive = false
					for i := 0; i < 6; i++ {
						battle.PlayerSacrificeBuffStats[i] = 0
					}
				}
				// 71 - 牺牲暴击：自己牺牲(体力降到0), 使下一只出战精灵在前两回合内必定致命一击
				if battle.PlayerSacrificeCritActive {
					battle.PlayerCritBuffRounds = 2
					battle.PlayerSacrificeCritActive = false
				}
				// 67 - 击败减对方下只最大HP：减少新精灵的最大体力1/n
				if battle.PlayerKillReduceMaxHpDivisor > 0 {
					reduceAmount := battle.PlayerMaxHP / uint32(battle.PlayerKillReduceMaxHpDivisor)
					if reduceAmount > 0 && battle.PlayerMaxHP > reduceAmount {
						battle.PlayerMaxHP -= reduceAmount
						if battle.PlayerHP > battle.PlayerMaxHP {
							battle.PlayerHP = battle.PlayerMaxHP
						}
					}
					battle.PlayerKillReduceMaxHpDivisor = 0
				}
				// 144 - 牺牲全部体力使下一只 n 回合免疫异常
				if battle.PlayerSacrificeImmuneStatusRounds > 0 {
					battle.PlayerImmuneStatusRounds = battle.PlayerSacrificeImmuneStatusRounds
					battle.PlayerSacrificeImmuneStatusRounds = 0
				}
				petStats := petMgr.GetStats(picked.ID, picked.Level, picked.DV, picked.GetEVStats(), picked.Nature)
				battle.PlayerHP = uint32(petStats.HP)
				battle.PlayerMaxHP = uint32(petStats.MaxHP)
				ctx.GameServer.BattleStates[ctx.UserID] = battle
				ctx.GameServer.BattleMu.Unlock()
				body2407 := buildChangePetInfoBody(uint32(ctx.UserID), uint32(picked.ID), picked.Name, uint32(picked.Level), uint32(petStats.HP), uint32(petStats.MaxHP), uint32(picked.CatchTime))
				ctx.GameServer.SendResponse(ctx.ClientData, 2407, ctx.UserID, 0, body2407)
				if battle.OpponentUserID != 0 {
					// 延迟 50ms 再发给对方，确保切换方客户端先处理完切换逻辑
					time.Sleep(50 * time.Millisecond)
					if otherClient := ctx.GameServer.GetClientByUserID(battle.OpponentUserID); otherClient != nil {
						ctx.GameServer.SendResponse(otherClient, 2407, battle.OpponentUserID, 0, body2407)
					}
				}
				logger.Info(fmt.Sprintf("[2405] 多精灵自动切换: 已切换至第 %d 只 PetID=%d HP=%d/%d", nextIndex+1, picked.ID, petStats.HP, petStats.MaxHP))
			}
		}
		return
	}

	// 疲惫 / 睡眠 / 麻痹：可以同时存在，图标各自显示，本回合是否能行动由三者“或”决定
	skipPlayerAction := false
	// 疲惫（status[7]）：使用后若持续>0，每回合结算一次“无法行动”，并递减回合数
	if battle.PlayerStatus[gameskills.StatusIndexFatigue] > 0 {
		skipPlayerAction = true
		battle.PlayerStatus[gameskills.StatusIndexFatigue]--
	}
	// 睡眠（status[8]）：效果与麻痹类似，本回合无法行动，并递减回合数
	if battle.PlayerStatus[gameskills.StatusIndexSleep] > 0 {
		skipPlayerAction = true
		battle.PlayerStatus[gameskills.StatusIndexSleep]--
	}
	// 麻痹（status[0]）：本回合无法行动，并递减回合数
	if battle.PlayerStatus[gameskills.StatusIndexParalysis] > 0 {
		skipPlayerAction = true
		battle.PlayerStatus[gameskills.StatusIndexParalysis]--
	}
	// 石化（status[9]）：本回合无法行动，并递减回合数
	if battle.PlayerStatus[gameskills.StatusIndexPetrify] > 0 {
		skipPlayerAction = true
		battle.PlayerStatus[gameskills.StatusIndexPetrify]--
	}
	// 混乱（status[10]）：简化为“本回合无法行动”，并递减回合数
	if battle.PlayerStatus[gameskills.StatusIndexConfusion] > 0 {
		skipPlayerAction = true
		battle.PlayerStatus[gameskills.StatusIndexConfusion]--
	}
	// 石化（status[9]）：本回合无法行动，并递减回合数
	if battle.PlayerStatus[gameskills.StatusIndexPetrify] > 0 {
		skipPlayerAction = true
		battle.PlayerStatus[gameskills.StatusIndexPetrify]--
	}
	// 混乱（status[10]）：简化为“本回合无法行动”，并递减回合数
	if battle.PlayerStatus[gameskills.StatusIndexConfusion] > 0 {
		skipPlayerAction = true
		battle.PlayerStatus[gameskills.StatusIndexConfusion]--
	}

	// 玩家攻击：计算伤害（完整版）
	petMgr := gamepets.GetInstance()
	skillMgr := gameskills.GetInstance()
	// 当前出战精灵索引：优先使用 BattleState.ActivePetIndex，保证切换精灵后仍然使用正确的出战精灵，
	// 同时不改变 user.Pets 的顺序，这样战斗结束后背包首发与客户端一致。
	activeIdx := 0
	if battle.ActivePetIndex > 0 && battle.ActivePetIndex < len(user.Pets) {
		activeIdx = battle.ActivePetIndex
	}
	playerPetID := 7
	playerLevel := 5
	playerDV := 31
	if len(user.Pets) > 0 {
		playerPetID = user.Pets[activeIdx].ID
		if user.Pets[activeIdx].Level > 0 {
			playerLevel = user.Pets[activeIdx].Level
		}
		playerDV = user.Pets[activeIdx].DV
	}
	// 初始化当前出战精灵的技能与 PP（仅首次或换宠后会真正写入）
	initPlayerSkillPP(battle, user, activeIdx, petMgr, skillMgr)
	// 获取玩家精灵的性格、EV 与特性
	playerNature := 0
	playerEV := gamepets.EVStats{}
	playerTrait := 0
	if len(user.Pets) > 0 {
		playerNature = user.Pets[activeIdx].Nature
		playerEV = user.Pets[activeIdx].GetEVStats()
		playerTrait = user.Pets[activeIdx].Trait
	}
	playerStats := petMgr.GetStats(playerPetID, playerLevel, playerDV, playerEV, playerNature)

	// 每当一方使用完技能（造成伤害和附加效果后），结算一次该方此前施加的“多回合固定伤害”
	// 例如“不灭之火”：配置 rounds=5, damage=30，则首次使用那一回合 + 接下来4个回合，
	// 每次自己出手后都会再扣 30 点固定伤害。
	applyFixedDotAfterAttack := func(hp *uint32, dotDamage *uint32, rounds *byte) {
		if hp == nil || dotDamage == nil || rounds == nil {
			return
		}
		if *hp == 0 || *rounds == 0 || *dotDamage == 0 {
			return
		}
		dmg := *dotDamage
		if dmg > *hp {
			dmg = *hp
		}
		*hp -= dmg
		if *rounds > 0 {
			*rounds--
		}
		if *rounds == 0 {
			*dotDamage = 0
		}
	}

	// 获取技能数据
	skill := skillMgr.Get(int(skillID))
	if skill == nil {
		skill = skillMgr.Get(10001) // 默认技能：撞击
	}

	// PP 消耗与耗尽判定：若本回合未被异常直接跳过，则根据技能 ID 查找并减少 PP；
	// 若 PP 已为 0，则本回合无法行动（视为技能释放失败）。
	if !skipPlayerAction && skillID != 0 {
		idxPP := -1
		for i := 0; i < 4; i++ {
			if battle.PlayerSkillIDs[i] == skillID {
				idxPP = i
				break
			}
		}
		if idxPP >= 0 {
			if battle.PlayerSkillPP[idxPP] == 0 {
				skipPlayerAction = true
			} else {
				// 412 - 体力低于 1/n 时不消耗 PP
				noPPCost := false
				if skill.EffectID == 412 && battle.PlayerMaxHP > 0 {
					effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
					divisor := 2
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						divisor = effArgs[0]
					}
					if battle.PlayerHP < battle.PlayerMaxHP/uint32(divisor) {
						noPPCost = true
					}
				}
				if !noPPCost {
					battle.PlayerSkillPP[idxPP]--
				}
			}
		}
	}

	// 计算玩家本回合是否命中（考虑命中等级与必中）
	playerHit := !skipPlayerAction
	if playerHit {
		// 技能本身标记为必中时，直接命中，不受命中等级影响
		if skill.MustHit != 1 {
			baseAcc := skill.Accuracy
			if baseAcc == 0 {
				baseAcc = 100
			}
			// battle.PlayerBattleLv[5] 为命中等级（-6~+6），由技能附加效果维护
			accStage := int(battle.PlayerBattleLv[gameskills.StatAccuracy])
			finalAcc := gamebattle.CalcHitChance(baseAcc, accStage, 0)
			// 特性：精准(1022) —— 命中率 +5%
			if playerTrait == 1022 {
				finalAcc += 5
			}
			if finalAcc > 100 {
				finalAcc = 100
			}
			if finalAcc < 1 {
				finalAcc = 1
			}
			if rand.Intn(100) >= finalAcc {
				playerHit = false
			}
		}
		// 81 - 下 n 回合自身攻击技能必定命中（覆盖本次命中结果）
		if !playerHit && battle.PlayerMustHitRounds > 0 && skill.Category != 4 {
			playerHit = true
		}
	}

	// 获取敌人精灵数据（敌人 EV 默认为 0）
	enemyPet := petMgr.Get(battle.EnemyID)
	enemyEV := gamepets.EVStats{}
	enemyStats := petMgr.GetStats(battle.EnemyID, battle.EnemyLevel, 15, enemyEV, 0)

	// 初始化敌方技能与 PP（仅首次会真正写入）
	initEnemySkillPP(battle, battle.EnemyID, battle.EnemyLevel, skillMgr)

	// 先手判定：按技能先制 + 速度比较（雷伊/盖亚 +6；魔狮迪露 体力低于一半时 +6）
	enemySkillForTurn, enemySkillIDForTurn := pickEnemySkill(skillMgr, battle.EnemyID, battle.EnemyLevel)
	playerPriority := skill.Priority
	// 454 - 当自身血量少于 1/n 时先制 +m
	if battle.PlayerPriorityBonusWhenLowHPRounds > 0 && battle.PlayerMaxHP > 0 && battle.PlayerHP < battle.PlayerMaxHP/uint32(battle.PlayerPriorityBonusWhenLowHPDivisor) {
		playerPriority += battle.PlayerPriorityBonusWhenLowHPBonus
	}
	// 482 - m% 几率先制 +n（本回合掷骰）
	if battle.PlayerPriorityBonusChance > 0 && rand.Intn(100) < int(battle.PlayerPriorityBonusChance) {
		playerPriority += battle.PlayerPriorityBonusAmount
	}
	// 83（雄性）- 下两回合必定先手
	if battle.PlayerMaleFirstStrikeRounds > 0 {
		playerPriority += 10
	}
	enemyPriority := 0
	if enemySkillForTurn != nil {
		enemyPriority = enemySkillForTurn.Priority + sptboss.GetPriorityBonusWithHP(battle.EnemyID, battle.EnemyHP, battle.EnemyMaxHP)
	}
	// 454（敌方）- 当自身血量少于 1/n 时先制 +m
	if battle.EnemyPriorityBonusWhenLowHPRounds > 0 && battle.EnemyMaxHP > 0 && battle.EnemyHP < battle.EnemyMaxHP/uint32(battle.EnemyPriorityBonusWhenLowHPDivisor) {
		enemyPriority += battle.EnemyPriorityBonusWhenLowHPBonus
	}
	// 482（敌方）- m% 几率先制 +n
	if battle.EnemyPriorityBonusChance > 0 && rand.Intn(100) < int(battle.EnemyPriorityBonusChance) {
		enemyPriority += battle.EnemyPriorityBonusAmount
	}
	// 83（敌方雄性）- 下两回合必定先手
	if battle.EnemyMaleFirstStrikeRounds > 0 {
		enemyPriority += 10
	}
	playerSpeed := int(float64(playerStats.Speed) * gamebattle.GetStatMultiplier(int(battle.PlayerBattleLv[gameskills.StatSpeed])))
	if playerSpeed < 1 {
		playerSpeed = 1
	}
	if battle.PlayerStatus[gameskills.StatusIndexParalysis] > 0 {
		playerSpeed /= 2
		if playerSpeed < 1 {
			playerSpeed = 1
		}
	}
	enemySpeed := int(float64(enemyStats.Speed) * gamebattle.GetStatMultiplier(int(battle.EnemyBattleLv[gameskills.StatSpeed])))
	if enemySpeed < 1 {
		enemySpeed = 1
	}
	if battle.EnemyStatus[gameskills.StatusIndexParalysis] > 0 {
		enemySpeed /= 2
		if enemySpeed < 1 {
			enemySpeed = 1
		}
	}
	enemyFirst := (enemyPriority > playerPriority) ||
		(enemyPriority == playerPriority && enemySpeed > playerSpeed) ||
		(enemyPriority == playerPriority && enemySpeed == playerSpeed && rand.Intn(2) == 1)
	isGaiyaFirst := enemyFirst
	// 52：本方先手时对方技能 miss（敌方有 52 且我方先手则我方 miss）
	if playerHit && !enemyFirst && battle.EnemyEvasionRounds > 0 && skill.MustHit != 1 {
		playerHit = false
	}
	// 78（敌方）：n 回合内物理攻击对敌方必定 miss
	if playerHit && skill.Category == 1 && battle.EnemyPhysMissRounds > 0 && skill.MustHit != 1 {
		playerHit = false
	}
	// 86（敌方）：n 回合内属性（特殊）攻击对敌方必定 miss
	if playerHit && skill.Category == 2 && battle.EnemySpecialMissRounds > 0 && skill.MustHit != 1 {
		playerHit = false
	}
	// 72 - Miss死亡：如果此回合miss，则立即死亡
	if !playerHit && battle.PlayerMissDeathActive {
		battle.PlayerHP = 0
		battle.PlayerMissDeathActive = false
	}
	// 136 - 若 Miss，自身恢复 1/n 体力
	if !playerHit && skill != nil && skill.EffectID == 136 && battle.PlayerHP > 0 {
		effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
		divisor := 4
		if len(effArgs) >= 1 && effArgs[0] > 0 {
			divisor = effArgs[0]
		}
		heal := battle.PlayerMaxHP / uint32(divisor)
		if heal > 0 {
			newHP := battle.PlayerHP + heal
			if newHP > battle.PlayerMaxHP {
				newHP = battle.PlayerMaxHP
			}
			battle.PlayerHP = newHP
		}
	}
	// 敌方先手且本回合敌方已将我方击至 0 HP 时，我方未出招，2505 中不显示我方本回合出招
	playerTurnSkippedBecauseDead := false

	// 记录玩家出手前敌人的 HP，用于某些依赖“血量差”的技能（如同生共死）在效果生效后回写真实伤害
	enemyHPBeforeAction := battle.EnemyHP
	// 尤纳斯：本回合是否由里奥斯幻影合法击杀（避免后续锁 1 血误恢复，必须在 goto 前声明）
	var youNaSiKilledByPhantom bool

	// 伤害计算（对齐你的规则）：
	// Category 4 = 变化/状态技能，无威力，不造成伤害；其余 [(Lv*0.4+2)*Power*Atk/Def/50+2] * STAB * TypeMod * Rand
	attackerPet := petMgr.Get(playerPetID)
	power := uint32(skill.Power)
	if power == 0 && skill.Category != 4 {
		power = 40 // 非状态技能缺威力时默认 40
	}
	// 1901：潜力越高威力越大（0~155），按 DV*5
	if skill.EffectID == 1901 && skill.Category != 4 {
		if playerDV < 0 {
			playerDV = 0
		}
		power = uint32(playerDV * 5)
	}
	// 61：威力随机 50~150
	if skill.EffectID == 61 && skill.Category != 4 {
		power = uint32(rand.Intn(150-50+1) + 50)
	}
	// 70：威力随机 140~220
	if skill.EffectID == 70 && skill.Category != 4 {
		power = uint32(rand.Intn(220-140+1) + 140)
	}
	// 40：先出手时威力为 2 倍
	if skill.EffectID == 40 && skill.Category != 4 && !enemyFirst {
		power *= 2
	}
	// 118：威力随机 140~180
	if skill.EffectID == 118 && skill.Category != 4 {
		power = uint32(rand.Intn(180-140+1) + 140)
	}

	// damageCalc 为公式计算得到的理论伤害（不考虑“血量上限”这一物理限制）
	// damage      为实际扣掉的 HP（不会超过目标当前 HP，且考虑“手下留情”等效果）
	damageCalc := uint32(0)
	damage := uint32(0)
	// 玩家本次攻击是否暴击标记（用于 2505 isCrit 字段）
	isCritPlayer := false
	if skill.Category != 4 && playerHit && !skipPlayerAction {
		// 选择物理/特殊攻防，并应用强化弱化倍率（battleLv 跟随精灵）
		atk := float64(playerStats.Attack)
		def := float64(enemyStats.Defence)
		atkStage, defStage := 0, 1 // 物理：攻击/防御
		if skill.Category == 2 {
			atk = float64(playerStats.SpAtk)
			def = float64(enemyStats.SpDef)
			atkStage, defStage = 2, 3 // 特殊：特攻/特防
		}
		// 51: 本方攻击力与对手相同
		if battle.PlayerCopyAtkRounds > 0 {
			if skill.Category == 2 {
				atk = float64(enemyStats.SpAtk)
			} else {
				atk = float64(enemyStats.Attack)
			}
		}
		// 45: 对方防御力与己方相同（即对方防御=己方防御）
		if battle.EnemyCopyDefRounds > 0 {
			if skill.Category == 2 {
				def = float64(playerStats.SpDef)
			} else {
				def = float64(playerStats.Defence)
			}
		}
		atk *= gamebattle.GetStatMultiplier(int(battle.PlayerBattleLv[atkStage]))
		def *= gamebattle.GetStatMultiplier(int(battle.EnemyBattleLv[defStage]))
		if def < 1 {
			def = 1
		}
		powerForCalc := float64(power)
		// 65: n 回合内某属性技能威力 m 倍
		if battle.PlayerElemPowerRounds > 0 && skill.Type == int(battle.PlayerElemPowerType) {
			powerForCalc *= float64(battle.PlayerElemPowerMult)
		}
		// 基础伤害（向下取整）
		baseDamage := math.Floor(((float64(playerLevel)*0.4 + 2.0) * powerForCalc * atk / def / 50.0) + 2.0)

		// STAB（同属性加成，1.5倍）
		stab := 1.0
		if attackerPet != nil && (skill.Type == attackerPet.Type || (attackerPet.Type2 > 0 && skill.Type == attackerPet.Type2)) {
			stab = 1.5
		}

		// 属性克制
		typeMod := 1.0
		if enemyPet != nil {
			if battle.EnemyTypeSwapRounds > 0 {
				// 55：属性反转，用对方类型攻击己方技能类型的克制
				typeMod = gamebattle.GetTypeMultiplierDual(enemyPet.Type, enemyPet.Type2, skill.Type)
			} else if battle.EnemyTypeCopyRounds > 0 {
				// 56：属性相同，克制为 1
				typeMod = 1.0
			} else {
				typeMod = gamebattle.GetTypeMultiplierDual(skill.Type, enemyPet.Type, enemyPet.Type2)
			}
		}

		// 随机（217~255）
		randomMod := float64(rand.Intn(255-217+1)+217) / 255.0

		// 暴击：1/16 基础概率（CritRate 表示几分之十六，默认 1）
		critRate := skill.CritRate
		if critRate == 0 {
			critRate = 1
		}
		// 暴击强化：当存在“下 N 回合攻击技能必定致命一击”效果时，直接视为 100% 暴击率。
		// 属性技能（Category=4）本身不走该分支，因此不会暴击。
		if battle.PlayerCritBuffRounds > 0 {
			critRate = 16
		}
		// 特性：会心(1023) —— 额外 +1/16 暴击率（上限 16）
		if playerTrait == 1023 && critRate < 16 {
			critRate++
		}
		// 32 - n 回合暴击率增加 1/16
		if battle.PlayerCritRateBonusRounds > 0 && critRate < 16 {
			critRate++
		}
		// 441 - 每次攻击暴击率 +n%（累积，最高 m%）；PlayerCritRateBonus 以 1/16 为单位
		if battle.PlayerCritRateBonus > 0 {
			critRate += int(battle.PlayerCritRateBonus)
			if critRate > 16 {
				critRate = 16
			}
		}
		// 83（雌性）- 下两回合必定暴击
		if battle.PlayerFemaleCritRounds > 0 {
			critRate = 16
		}
		// 95 - 对手处于睡眠状态时致命一击率提升 n/16
		if skill.EffectID == 95 && battle.EnemyStatus[gameskills.StatusIndexSleep] > 0 {
			effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
			bonus := 4
			if len(effArgs) >= 1 && effArgs[0] > 0 {
				bonus = effArgs[0]
			}
			critRate += bonus
			if critRate > 16 {
				critRate = 16
			}
		}
		isCritPlayer = rand.Intn(16) < critRate
		// 暴击伤害：对齐“正常攻击伤害的两倍”规则
		critMod := 1.0
		if isCritPlayer {
			critMod = 2.0
			// 暴击后清除对方的防御/特防提升状态（仅清除 >0 的强化，保留减防 debuff）
			if skill.Category == 1 {
				// 物理攻击：清空对方防御提升
				if battle.EnemyBattleLv[gameskills.StatDefence] > 0 {
					battle.EnemyBattleLv[gameskills.StatDefence] = 0
				}
			} else if skill.Category == 2 {
				// 特殊攻击：清空对方特防提升
				if battle.EnemyBattleLv[gameskills.StatSpDef] > 0 {
					battle.EnemyBattleLv[gameskills.StatSpDef] = 0
				}
			}
		}

		// 理论伤害（包含各种加成与随机，但还未考虑“血量上限”等裁剪）
		damageCalc = uint32(baseDamage * stab * typeMod * randomMod * critMod)
		// 53：n 回合己方攻击伤害 m 倍
		if battle.PlayerDamageMultRounds > 0 && battle.PlayerDamageMult > 0 {
			damageCalc *= uint32(battle.PlayerDamageMult)
		}

		// effect 88：n% 几率伤害为 m 倍
		if damageCalc > 0 && skill.EffectID == 88 {
			effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
			chance, mult := 10, 2
			if len(effArgs) >= 1 {
				chance = effArgs[0]
			}
			if len(effArgs) >= 2 {
				mult = effArgs[1]
			}
			if chance < 0 {
				chance = 0
			}
			if mult < 1 {
				mult = 1
			}
			if rand.Intn(100) < chance {
				mul := uint64(damageCalc) * uint64(mult)
				if mul > math.MaxUint32 {
					damageCalc = math.MaxUint32
				} else {
					damageCalc = uint32(mul)
				}
			}
		}

		// 特性：叶绿/流水/炎火/.../圣灵 (1006-1021)
		// 对应 Args: type 5%，当技能属性与特性匹配时，伤害额外 +5%
		if playerTrait >= 1006 && playerTrait <= 1021 && skill.Category != 4 {
			targetType := playerTrait - 1005 // 1006->1, 1007->2, ..., 1021->16
			if skill.Type == targetType {
				mul := uint64(damageCalc) * 105
				mul /= 100
				if mul > math.MaxUint32 {
					damageCalc = math.MaxUint32
				} else {
					damageCalc = uint32(mul)
				}
			}
		}

		// 多段攻击（effect 31）：按参数随机命中次数，直接折算为单次伤害倍数
		// SideEffectArg: minHits maxHits（默认 2~5；示例“连续进行5~8次攻击”）
		if skill.EffectID == 31 {
			effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
			minHits, maxHits := 2, 5
			if len(effArgs) >= 1 {
				minHits = effArgs[0]
			}
			if len(effArgs) >= 2 {
				maxHits = effArgs[1]
			}
			if minHits < 1 {
				minHits = 1
			}
			if maxHits < minHits {
				maxHits = minHits
			}
			hits := rand.Intn(maxHits-minHits+1) + minHits
			// 折算倍数，避免溢出
			if hits > 1 {
				mul := uint64(damageCalc) * uint64(hits)
				if mul > math.MaxUint32 {
					damageCalc = math.MaxUint32
				} else {
					damageCalc = uint32(mul)
				}
			}
		}

		// effect 36 秒杀：命中时 n% 概率秒杀对方
		instantKill := false
		if skill.EffectID == 36 {
			effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
			chance := 5
			if len(effArgs) >= 1 {
				chance = effArgs[0]
			}
			if rand.Intn(100) < chance && battle.EnemyHP > 0 {
				damageCalc = battle.EnemyHP
				instantKill = true
			}
		}

		// 从理论伤害得到实际扣血值
		finalDamage := damageCalc
		if instantKill {
			finalDamage = battle.EnemyHP
		} else {
			// 惩罚（effect 35）：对方能力等级越高伤害越高，附加 sum(正能力等级)*20
			if skill.EffectID == 35 {
				bonus := 0
				for i := 0; i < 6; i++ {
					if battle.EnemyBattleLv[i] > 0 {
						bonus += int(battle.EnemyBattleLv[i]) * 20
					}
				}
				finalDamage += uint32(bonus)
			}
			// effect 64：自身在烧伤/冻伤/中毒状态下造成的伤害加倍（并视为覆盖烧伤减伤）
			hasAilment := battle.PlayerStatus[gameskills.StatusIndexPoison] > 0 ||
				battle.PlayerStatus[gameskills.StatusIndexBurn] > 0 ||
				battle.PlayerStatus[gameskills.StatusIndexFreeze] > 0
			if skill.EffectID == 64 && hasAilment && finalDamage > 0 {
				mul := uint64(finalDamage) * 2
				if mul > math.MaxUint32 {
					finalDamage = math.MaxUint32
				} else {
					finalDamage = uint32(mul)
				}
				damageCalc = finalDamage
			}
			// effect 98：n 回合内对雄性精灵的伤害为 m 倍
			if battle.PlayerMaleDamageMultRounds > 0 && finalDamage > 0 {
				targetPet := petMgr.Get(battle.EnemyID)
				if targetPet != nil && targetPet.Gender == 1 {
					mult := battle.PlayerMaleDamageMult
					if mult < 1 {
						mult = 1
					}
					mul := uint64(finalDamage) * uint64(mult)
					if mul > math.MaxUint32 {
						finalDamage = math.MaxUint32
					} else {
						finalDamage = uint32(mul)
					}
					if finalDamage > battle.EnemyHP {
						finalDamage = battle.EnemyHP
					}
				}
			}
			// effect 82：目标为雄性伤害 200%，雌性 50%（无性别不变）
			if skill.EffectID == 82 && finalDamage > 0 {
				targetPet := petMgr.Get(battle.EnemyID)
				if targetPet != nil {
					if targetPet.Gender == 1 {
						finalDamage *= 2
						if finalDamage > battle.EnemyHP {
							finalDamage = battle.EnemyHP
						}
					} else if targetPet.Gender == 2 {
						finalDamage /= 2
						if finalDamage < 1 {
							finalDamage = 1
						}
					}
				}
			}
			// 96 - 对手处于烧伤状态时威力翻倍
			if skill.EffectID == 96 && battle.EnemyStatus[gameskills.StatusIndexBurn] > 0 && finalDamage > 0 {
				mul := uint64(finalDamage) * 2
				if mul > math.MaxUint32 {
					finalDamage = math.MaxUint32
				} else {
					finalDamage = uint32(mul)
				}
				if finalDamage > battle.EnemyHP {
					finalDamage = battle.EnemyHP
				}
			}
			// 97 - 对手处于冻伤状态时威力翻倍
			if skill.EffectID == 97 && battle.EnemyStatus[gameskills.StatusIndexFreeze] > 0 && finalDamage > 0 {
				mul := uint64(finalDamage) * 2
				if mul > math.MaxUint32 {
					finalDamage = math.MaxUint32
				} else {
					finalDamage = uint32(mul)
				}
				if finalDamage > battle.EnemyHP {
					finalDamage = battle.EnemyHP
				}
			}
			// 102 - 对手处于麻痹状态时威力翻倍
			if skill.EffectID == 102 && battle.EnemyStatus[gameskills.StatusIndexParalysis] > 0 && finalDamage > 0 {
				mul := uint64(finalDamage) * 2
				if mul > math.MaxUint32 {
					finalDamage = math.MaxUint32
				} else {
					finalDamage = uint32(mul)
				}
				if finalDamage > battle.EnemyHP {
					finalDamage = battle.EnemyHP
				}
			}
			// 188 - 若对手处于异常状态则威力翻倍（消除对手防/特防强化在 ApplyEffect 中已做）
			if skill.EffectID == 188 && finalDamage > 0 {
				hasEnemyStatus := false
				for i := 0; i < 20; i++ {
					if battle.EnemyStatus[i] > 0 {
						hasEnemyStatus = true
						break
					}
				}
				if hasEnemyStatus {
					mul := uint64(finalDamage) * 2
					if mul > math.MaxUint32 {
						finalDamage = math.MaxUint32
					} else {
						finalDamage = uint32(mul)
					}
					if finalDamage > battle.EnemyHP {
						finalDamage = battle.EnemyHP
					}
				}
			}
			// 100 - 自身体力越少则威力越大（伤害 *= 2 - HP/maxHP，满血1倍、空血2倍）
			if skill.EffectID == 100 && finalDamage > 0 && battle.PlayerMaxHP > 0 {
				ratio := 2.0 - float64(battle.PlayerHP)/float64(battle.PlayerMaxHP)
				if ratio < 1.0 {
					ratio = 1.0
				}
				if ratio > 2.0 {
					ratio = 2.0
				}
				mul := uint64(float64(finalDamage) * ratio)
				if mul > math.MaxUint32 {
					finalDamage = math.MaxUint32
				} else {
					finalDamage = uint32(mul)
				}
				if finalDamage > battle.EnemyHP {
					finalDamage = battle.EnemyHP
				}
			}
			if !(skill.EffectID == 64 && hasAilment) && battle.PlayerStatus[gameskills.StatusIndexBurn] > 0 {
				// 烧伤效果：被烧伤方造成的伤害减半
				finalDamage = finalDamage / 2
				if finalDamage < 1 {
					finalDamage = 1
				}
			}
			// 195/494 - 无视对手能力提升状态（在伤害计算阶段已通过不应用对手防御提升实现，此处无需额外处理）
			// 179 - 若属性相同则技能威力提升 n（在伤害计算基础上附加百分比）
			if skill.EffectID == 179 && finalDamage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				boost := 20
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					boost = effArgs[0]
				}
				if attackerPet != nil {
					targetPet := petMgr.Get(battle.EnemyID)
					if targetPet != nil && attackerPet.Type == targetPet.Type {
						finalDamage = finalDamage * uint32(100+boost) / 100
					}
				}
			}
			// 129 - 对方为 X 性则技能威力翻倍
			if skill.EffectID == 129 && finalDamage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				gender := 2
				if len(effArgs) >= 1 {
					gender = effArgs[0]
				}
				targetPet := petMgr.Get(battle.EnemyID)
				if targetPet != nil && targetPet.Gender == gender {
					mul := uint64(finalDamage) * 2
					if mul > math.MaxUint32 {
						finalDamage = math.MaxUint32
					} else {
						finalDamage = uint32(mul)
					}
				}
			}
			// 130 - 对方为 X 性则附加 n 点伤害
			if skill.EffectID == 130 && finalDamage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				gender, bonus := 1, 100
				if len(effArgs) >= 1 {
					gender = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					bonus = effArgs[1]
				}
				targetPet := petMgr.Get(battle.EnemyID)
				if targetPet != nil && targetPet.Gender == gender {
					finalDamage += uint32(bonus)
				}
			}
			// 131 - 对方为 X 性则免疫当前回合伤害
			if skill.EffectID == 131 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				gender := 1
				if len(effArgs) >= 1 {
					gender = effArgs[0]
				}
				targetPet := petMgr.Get(battle.EnemyID)
				if targetPet != nil && targetPet.Gender == gender {
					finalDamage = 0
					damageCalc = 0
				}
			}
			// 135/447 - 造成的伤害不会低于 n
			if (skill.EffectID == 135 || skill.EffectID == 447) && finalDamage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				floor := 80
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					floor = effArgs[0]
				}
				if finalDamage < uint32(floor) {
					finalDamage = uint32(floor)
				}
			}
			// 193 - 若对手处于 XX 状态则必定致命一击（若本次未暴击，则强制应用暴击倍率）
			if skill.EffectID == 193 && finalDamage > 0 && !isCritPlayer {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				statusIdx := 5
				if len(effArgs) >= 1 {
					statusIdx = effArgs[0]
				}
				if statusIdx >= 0 && statusIdx < 20 && battle.EnemyStatus[statusIdx] > 0 {
					isCritPlayer = true
					mul := uint64(finalDamage) * 2
					if mul > math.MaxUint32 {
						finalDamage = math.MaxUint32
					} else {
						finalDamage = uint32(mul)
					}
				}
			}
			// 468 - 若自身处于能力下降状态则威力翻倍，同时解除能力下降状态
			if skill.EffectID == 468 && finalDamage > 0 {
				hasStatDrop := false
				for i := 0; i < 6; i++ {
					if battle.PlayerBattleLv[i] < 0 {
						hasStatDrop = true
						break
					}
				}
				if hasStatDrop {
					mul := uint64(finalDamage) * 2
					if mul > math.MaxUint32 {
						finalDamage = math.MaxUint32
					} else {
						finalDamage = uint32(mul)
					}
					for i := 0; i < 6; i++ {
						if battle.PlayerBattleLv[i] < 0 {
							battle.PlayerBattleLv[i] = 0
						}
					}
				}
			}
			// 195 - 若对手处于异常状态则攻击力双倍
			if skill.EffectID == 195 && finalDamage > 0 {
				hasStatus := false
				for i := 0; i < 20; i++ {
					if battle.EnemyStatus[i] > 0 {
						hasStatus = true
						break
					}
				}
				if hasStatus {
					mul := uint64(finalDamage) * 2
					if mul > math.MaxUint32 {
						finalDamage = math.MaxUint32
					} else {
						finalDamage = uint32(mul)
					}
				}
			}
			// 180 - 只在第一回合有效果（若非第一回合，威力减半）
			if skill.EffectID == 180 && finalDamage > 0 && battle.RoundCount > 1 {
				finalDamage /= 2
				if finalDamage < 1 {
					finalDamage = 1
				}
			}
			// 88 - n% 概率伤害为 m 倍
			if skill.EffectID == 88 && finalDamage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				chance, mult := 10, 2
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					chance = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					mult = effArgs[1]
				}
				if rand.Intn(100) < chance {
					mul := uint64(finalDamage) * uint64(mult)
					if mul > math.MaxUint32 {
						finalDamage = math.MaxUint32
					} else {
						finalDamage = uint32(mul)
					}
				}
			}
			// 35 - 对方能力等级越高伤害越大（每有一个正向等级加成 5%）
			if skill.EffectID == 35 && finalDamage > 0 {
				totalBoost := 0
				for i := 0; i < 6; i++ {
					if battle.EnemyBattleLv[i] > 0 {
						totalBoost += int(battle.EnemyBattleLv[i])
					}
				}
				if totalBoost > 0 {
					mul := uint64(finalDamage) * uint64(100+totalBoost*5) / 100
					if mul > math.MaxUint32 {
						finalDamage = math.MaxUint32
					} else {
						finalDamage = uint32(mul)
					}
				}
			}
			// 111 - 攻击力越高附加伤害越大（攻击力每高出对手 10 点附加 1 点伤害）
			if skill.EffectID == 111 && finalDamage > 0 {
				playerPet := petMgr.Get(playerPetID)
				enemyPet := petMgr.Get(battle.EnemyID)
				if playerPet != nil && enemyPet != nil && playerPet.Atk > enemyPet.Atk {
					bonus := uint32(playerPet.Atk-enemyPet.Atk) / 10
					finalDamage += bonus
				}
			}
			// 113 - 速度越高威力越大（速度每高出对手 10 点附加 1 点伤害）
			if skill.EffectID == 113 && finalDamage > 0 {
				playerPet := petMgr.Get(playerPetID)
				enemyPet := petMgr.Get(battle.EnemyID)
				if playerPet != nil && enemyPet != nil && playerPet.Spd > enemyPet.Spd {
					bonus := uint32(playerPet.Spd-enemyPet.Spd) / 10
					finalDamage += bonus
				}
			}
			// 132 - 当前体力在对方体力以上时威力翻倍
			if skill.EffectID == 132 && finalDamage > 0 && battle.PlayerHP >= battle.EnemyHP {
				mul := uint64(finalDamage) * 2
				if mul > math.MaxUint32 {
					finalDamage = math.MaxUint32
				} else {
					finalDamage = uint32(mul)
				}
			}
			// 168 - 若自身处于睡眠状态则威力翻倍
			if skill.EffectID == 168 && finalDamage > 0 && battle.PlayerStatus[gameskills.StatusIndexSleep] > 0 {
				mul := uint64(finalDamage) * 2
				if mul > math.MaxUint32 {
					finalDamage = math.MaxUint32
				} else {
					finalDamage = uint32(mul)
				}
			}
			// 429 - 固定伤递增：基础 base 点，每次使用增加 increment，最高 max
			if skill.EffectID == 429 && finalDamage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				base, increment, maxDmg := 25, 25, 100
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					base = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					increment = effArgs[1]
				}
				if len(effArgs) >= 3 && effArgs[2] > 0 {
					maxDmg = effArgs[2]
				}
				current := uint32(base) + battle.PlayerFixedDmgIncrement
				if current > uint32(maxDmg) {
					current = uint32(maxDmg)
				}
				if current > battle.EnemyHP {
					current = battle.EnemyHP
				}
				finalDamage += current
				battle.PlayerFixedDmgIncrement += uint32(increment)
				if battle.PlayerFixedDmgIncrement > uint32(maxDmg-base) {
					battle.PlayerFixedDmgIncrement = uint32(maxDmg - base)
				}
			}
			// 431 - 若自身处于能力下降状态则威力翻倍
			if skill.EffectID == 431 && finalDamage > 0 {
				hasStatDrop := false
				for i := 0; i < 6; i++ {
					if battle.PlayerBattleLv[i] < 0 {
						hasStatDrop = true
						break
					}
				}
				if hasStatDrop {
					mul := uint64(finalDamage) * 2
					if mul > math.MaxUint32 {
						finalDamage = math.MaxUint32
					} else {
						finalDamage = uint32(mul)
					}
				}
			}
			// 特性：坚硬(1024) —— 受到的伤害减少5%（仅作用于敌人对我方造成的伤害；此处为我方进攻，不处理）
			if finalDamage < 1 {
				finalDamage = 1
			}
			// 实际扣血不能超过目标当前 HP
			if finalDamage > battle.EnemyHP {
				finalDamage = battle.EnemyHP
			}
			// 同生共死（effect 7）：仅当对方血量高于我方时生效，否则造成 0 伤害（不把对方打死）
			if skill.EffectID == 7 && battle.EnemyHP <= battle.PlayerHP {
				finalDamage = 0
				damageCalc = 0
			}
			// 手下留情（effect 8）：伤害正常判定，但若伤害≥对方剩余体力则改为剩余体力-1（留 1 滴血）
			// 同时把 damageCalc 改为实际伤害，否则 2505 里用 damageCalc 作 lostHP 会显示成公式伤害（如 1000+）
			if skill.EffectID == 8 && finalDamage >= battle.EnemyHP && battle.EnemyHP > 0 {
				finalDamage = battle.EnemyHP - 1
				damageCalc = finalDamage
			}
		}
		// 42：本方电系技能伤害×2
		if playerHit && battle.PlayerElectricBoostRounds > 0 && skill.Type == 5 {
			finalDamage *= 2
			if finalDamage > battle.EnemyHP {
				finalDamage = battle.EnemyHP
			}
		}
		// 敌方防御侧：46 挡一次 41 火抗 44 特防减半 50 物防减半 49 护盾 127 伤害减半
		if playerHit && finalDamage > 0 {
			if battle.EnemyBlockCount > 0 {
				battle.EnemyBlockCount--
				finalDamage = 0
			} else {
				// 127 - 敌方 n 回合内受到伤害减半
				if battle.EnemyDamageHalfRounds > 0 {
					finalDamage /= 2
					if finalDamage < 1 {
						finalDamage = 1
					}
				}
				// 54：对方打我方伤害 1/m（此处我方攻击敌方，敌方有 54 则我方打敌方伤害 1/m）
				if battle.EnemyDamageReductRounds > 0 && battle.EnemyDamageReduct > 0 {
					finalDamage /= uint32(battle.EnemyDamageReduct)
					if finalDamage < 1 {
						finalDamage = 1
					}
				}
				if battle.EnemyFireResistRounds > 0 && skill.Type == 3 {
					finalDamage /= 2
					if finalDamage < 1 {
						finalDamage = 1
					}
				}
				if battle.EnemySpDefHalfRounds > 0 && skill.Category == 2 {
					finalDamage /= 2
					if finalDamage < 1 {
						finalDamage = 1
					}
				}
				if battle.EnemyPhysDefHalfRounds > 0 && skill.Category == 1 {
					finalDamage /= 2
					if finalDamage < 1 {
						finalDamage = 1
					}
				}
				if battle.EnemyShieldPoints > 0 {
					if finalDamage <= battle.EnemyShieldPoints {
						battle.EnemyShieldPoints -= finalDamage
						finalDamage = 0
					} else {
						finalDamage -= battle.EnemyShieldPoints
						battle.EnemyShieldPoints = 0
					}
				}
				if finalDamage > battle.EnemyHP {
					finalDamage = battle.EnemyHP
				}
			}
		}
	// 68: 致死留 1 血（仅一次）
	if playerHit && finalDamage > 0 && battle.EnemyEndureRounds > 0 && finalDamage >= battle.EnemyHP && battle.EnemyHP > 0 {
		finalDamage = battle.EnemyHP - 1
		battle.EnemyEndureRounds--
	}
	// 402 - 后出手时额外附加 n 点固定伤害
	if playerHit && skill.EffectID == 402 && enemyFirst && finalDamage > 0 {
		effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
		bonus := uint32(50)
		if len(effArgs) >= 1 && effArgs[0] > 0 {
			bonus = uint32(effArgs[0])
		}
		finalDamage += bonus
		if finalDamage > battle.EnemyHP {
			finalDamage = battle.EnemyHP
		}
	}
	// 405 - 先出手时额外附加 n 点固定伤害
	if playerHit && skill.EffectID == 405 && !enemyFirst && finalDamage > 0 {
		effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
		bonus := uint32(50)
		if len(effArgs) >= 1 && effArgs[0] > 0 {
			bonus = uint32(effArgs[0])
		}
		finalDamage += bonus
		if finalDamage > battle.EnemyHP {
			finalDamage = battle.EnemyHP
		}
	}
	// BOSS/特殊多效果：GM 配置的固定伤害等（如 976 简/极）
	if playerHit && finalDamage > 0 {
		eids := gameskills.ParseSideEffectIds(skill.SideEffect)
		for _, eid := range eids {
			p := GetBossEffectParams(eid)
			if p.FixedDamage > 0 {
				finalDamage += uint32(p.FixedDamage)
			}
		}
		if finalDamage > battle.EnemyHP {
			finalDamage = battle.EnemyHP
		}
	}
	// 敌方 146：我方以物理攻击命中敌方时，敌方 n 回合内 m% 使对方（我方）中毒
	if playerHit && finalDamage > 0 && skill.Category == 1 && battle.EnemyPoisonOnPhysHitRounds > 0 &&
		battle.PlayerStatus[gameskills.StatusIndexPoison] == 0 {
		if rand.Intn(100) < int(battle.EnemyPoisonOnPhysHitChance) {
			battle.PlayerStatus[gameskills.StatusIndexPoison] = byte(rand.Intn(2) + 1)
		}
	}
	damage = finalDamage
}
	// 疲惫或未命中时本回合不造成直接伤害
	if skipPlayerAction || !playerHit {
		damageCalc = 0
		damage = 0
	}

	// 敌方本回合变量（盖亚先手时在“我方出招”前结算，非盖亚时在“我方出招”后结算）
	enemyDamage := uint32(0)
	enemyDamageCalc := uint32(0)
	enemySkillID := uint32(0)
	enemyAttempted := false
	enemyHitFor2505 := false
	playerHPBeforeEnemy := battle.PlayerHP
	effectGainHP := int32(0)

	// 盖亚先手：先执行敌方回合，再执行我方（若仍存活）；非盖亚则先执行我方再执行敌方
	fromGaiyaFirst := false
	var doEnemyTurn func() // 定义在 runEnemyTurnFirst，此处仅声明避免 goto 跳过声明
	if isGaiyaFirst {
		goto runEnemyTurnFirst
	}
doPlayerTurn:
	// 非疲惫时才扣血并应用技能效果；盖亚战时仅当我方仍存活才结算我方出招
	if !skipPlayerAction && (!isGaiyaFirst || battle.PlayerHP > 0) {
		if playerHit {
			// 谱尼七封印/真身特殊规则：仅在 108/514 地图对战谱尼(300)时生效（从配置读取）
			isPuniBattle := ((battle.BattleMapID == 108 || battle.BattleMapID == 514) && battle.EnemyID == 300 && battle.PuniDoorIndex >= 1 && battle.PuniDoorIndex <= 8)
			door := battle.PuniDoorIndex
			life := battle.PuniTrueFormLifeIndex
			if isPuniBattle {
				// 1）元素封印：仅允许的属性类型攻击有效
				checkElementOnly := false
				var allowedTypes []int
				if door == 8 {
					// 真身第二、第三条命沿用元素封印规则
					if life == 2 || life == 3 {
						if cfg, ok := sptboss.GetPuniSealConfig(2); ok && len(cfg.ElementOnlyTypes) > 0 {
							checkElementOnly = true
							allowedTypes = cfg.ElementOnlyTypes
						}
					}
				} else {
					if cfg, ok := sptboss.GetPuniSealConfig(door); ok && len(cfg.ElementOnlyTypes) > 0 {
						checkElementOnly = true
						allowedTypes = cfg.ElementOnlyTypes
					}
				}
				if checkElementOnly && skill.Category != 4 {
					allowed := false
					for _, t := range allowedTypes {
						if skill.Type == t {
							allowed = true
							break
						}
					}
					if !allowed {
						damage = 0
						damageCalc = 0
					}
				} else if door == 2 || (door == 8 && (life == 2 || life == 3)) {
					// 配置不存在时使用内置默认值（向后兼容）
					if skill.Type != 12 && skill.Type != 13 {
						damage = 0
						damageCalc = 0
					}
				}
				// 2）能量封印：单次伤害超过上限时，本回合稍后会触发“玩家当前精灵阵亡 + 谱尼回满血”
				if door == 3 || (door == 8 && life == 3) {
					if damage > 0 {
						battle.PuniEnergyDamageThisTurn += damage
					}
				}
			}
			// 3）生命封印（door=4）：当前实现仅用于配合每回合 +2000 回复（在回合开始阶段处理）。
			// 哈莫雷特(216)：必须始终按 水系(2)→火系(3)→草系(1) 循环命中才能受伤，否则伤害为 0（每击都校验顺序）
			if sptboss.IsHaMoLeiTeOrderBoss(battle.EnemyID) && skill.Category != 4 {
				required := sptboss.HaMoLeiTeRequiredType(battle.HaMoLeiTePhase)
				if skill.Type != required {
					damage = 0
					damageCalc = 0
				} else {
					battle.HaMoLeiTePhase = (battle.HaMoLeiTePhase + 1) % 3
				}
			}
			// 尤纳斯(132)：未受贯穿水枪前所受攻击伤害为 0；受贯穿水枪后正常受伤但保留 1 血，仅里奥斯(42)的幻影(10100)可击杀
			if sptboss.IsYouNaSiBoss(battle.EnemyID) && skill.Category != 4 {
				if battle.YouNaSiPhase == 0 {
					if skill.ID != sptboss.SkillIDPiercingWater {
						damage = 0
						damageCalc = 0
					} else {
						battle.YouNaSiPhase = 1
					}
				} else if battle.YouNaSiPhase == 1 && damage >= battle.EnemyHP {
					// 本击会致死：仅里奥斯幻影允许击杀
					if skill.ID != sptboss.SkillIDPhantom || playerPetID != sptboss.PetIDLiAoS {
						damage = battle.EnemyHP - 1
						if damage > battle.EnemyHP {
							damage = 0
						}
						damageCalc = damage
					} else {
						youNaSiKilledByPhantom = true // 本回合幻影击杀，后续不再执行锁 1 血
					}
				}
			}
			// 魔狮迪露(187) 等：受到的攻击伤害（非异常）乘 N 倍
			mult := sptboss.GetDamageTakenMultiplier(battle.EnemyID)
			if mult > 1 && damage > 0 {
				damage = damage * uint32(mult)
				if damage > battle.EnemyHP {
					damage = battle.EnemyHP
				}
				damageCalc = damage
			}
			// 488 - 对手体力小于 threshold 时伤害增加 percent%（生效一次后清零）
			if battle.PlayerDamageBoostWhenEnemyLowThreshold > 0 && battle.EnemyHP < battle.PlayerDamageBoostWhenEnemyLowThreshold && damage > 0 {
				damage = damage * (100 + uint32(battle.PlayerDamageBoostWhenEnemyLowPercent)) / 100
				if damage > battle.EnemyHP {
					damage = battle.EnemyHP
				}
				battle.PlayerDamageBoostWhenEnemyLowThreshold = 0
				battle.PlayerDamageBoostWhenEnemyLowPercent = 0
			}
			// 84（敌方）- 受到物理攻击时 m% 几率将对手麻痹
			if battle.EnemyParalyzeOnPhysHitRounds > 0 && skill.Category == 1 && damage > 0 &&
				!sptboss.IsControlImmune(playerPetID) && battle.PlayerStatus[gameskills.StatusIndexParalysis] == 0 {
				if rand.Intn(100) < int(battle.EnemyParalyzeOnPhysHitChance) {
					battle.PlayerStatus[gameskills.StatusIndexParalysis] = byte(rand.Intn(2) + 2)
				}
			}
			// 92（敌方）- 受到物理攻击时 m% 几率将对手冻伤
			if battle.EnemyFreezeOnPhysHitRounds > 0 && skill.Category == 1 && damage > 0 &&
				!sptboss.IsControlImmune(playerPetID) && battle.PlayerStatus[gameskills.StatusIndexFreeze] == 0 {
				if rand.Intn(100) < int(battle.EnemyFreezeOnPhysHitChance) {
					battle.PlayerStatus[gameskills.StatusIndexFreeze] = byte(rand.Intn(2) + 1)
					dmg := battle.PlayerMaxHP / 8
					if dmg > battle.PlayerHP {
						dmg = battle.PlayerHP
					}
					battle.PlayerHP -= dmg
				}
			}
			// 108（敌方）- 受到物理攻击时 m% 几率将对手烧伤
			if battle.EnemyBurnOnPhysHitRounds > 0 && skill.Category == 1 && damage > 0 &&
				!sptboss.IsStatusImmune(playerPetID) && battle.PlayerStatus[gameskills.StatusIndexBurn] == 0 {
				if rand.Intn(100) < int(battle.EnemyBurnOnPhysHitChance) {
					battle.PlayerStatus[gameskills.StatusIndexBurn] = byte(rand.Intn(2) + 1)
					dmg := battle.PlayerMaxHP / 8
					if dmg > battle.PlayerHP {
						dmg = battle.PlayerHP
					}
					battle.PlayerHP -= dmg
				}
			}
			// 89 - 每次造成伤害的 1/m 恢复体力
			if battle.PlayerLifestealRounds > 0 && battle.PlayerLifestealDivisor > 0 && damage > 0 {
				heal := damage / uint32(battle.PlayerLifestealDivisor)
				if heal > 0 {
					newHP := battle.PlayerHP + heal
					if newHP > battle.PlayerMaxHP {
						newHP = battle.PlayerMaxHP
					}
					battle.PlayerHP = newHP
					effectGainHP += int32(heal)
				}
			}
			// 104 - 每次直接攻击 m% 几率附带衰弱（随机能力 -1）
			if battle.PlayerWeaknessOnHitRounds > 0 && skill.Category != 4 && damage > 0 &&
				!sptboss.IsStatDropImmune(battle.EnemyID) {
				if rand.Intn(100) < int(battle.PlayerWeaknessOnHitChance) {
					stat := rand.Intn(6)
					cur := int(battle.EnemyBattleLv[stat])
					cur--
					if cur < -6 {
						cur = -6
					}
					battle.EnemyBattleLv[stat] = int8(cur)
				}
			}
			// 109 - 造成伤害时 m% 几率令对手冻伤
			if battle.PlayerFreezeOnDealDamageRounds > 0 && skill.Category != 4 && damage > 0 &&
				!sptboss.IsControlImmune(battle.EnemyID) && battle.EnemyStatus[gameskills.StatusIndexFreeze] == 0 {
				if rand.Intn(100) < int(battle.PlayerFreezeOnDealDamageChance) {
					battle.EnemyStatus[gameskills.StatusIndexFreeze] = byte(rand.Intn(2) + 1)
					dmg := battle.EnemyMaxHP / 8
					if dmg > battle.EnemyHP {
						dmg = battle.EnemyHP
					}
					battle.EnemyHP -= dmg
				}
			}
			// 21（敌方） - 反弹伤害：敌方受到攻击时对玩家造成伤害的 1/divisor
			if battle.EnemyReflectDamageRounds > 0 && battle.EnemyReflectDamageDivisor > 0 && damage > 0 {
				reflectDmg := damage / uint32(battle.EnemyReflectDamageDivisor)
				if reflectDmg > battle.PlayerHP {
					reflectDmg = battle.PlayerHP
				}
				battle.PlayerHP -= reflectDmg
			}
			// 463（敌方）- n 回合内每回合所受的伤害减少 m 点
			if battle.EnemyDamageReducePerRoundRounds > 0 && battle.EnemyDamageReducePerRoundAmount > 0 && damage > 0 {
				if damage > battle.EnemyDamageReducePerRoundAmount {
					damage -= battle.EnemyDamageReducePerRoundAmount
				} else {
					damage = 0
				}
			}
			// 125（敌方）- n 回合内被攻击时减少受到的伤害上限 m
			if battle.EnemyDamageCapRounds > 0 && battle.EnemyDamageCap > 0 && damage > battle.EnemyDamageCap {
				damage = battle.EnemyDamageCap
			}
			// 123（敌方）- n 回合内受到任何伤害时自身 XX 提高 m 级（在 128 前判定，以「受到伤害」为准）
			if battle.EnemyHurtStatBoostRounds > 0 && damage > 0 {
				stat := int(battle.EnemyHurtStatBoostStat)
				if stat >= 0 && stat < 6 {
					cur := int(battle.EnemyBattleLv[stat]) + int(battle.EnemyHurtStatBoostStages)
					if cur > 6 {
						cur = 6
					}
					if cur < -6 {
						cur = -6
					}
					battle.EnemyBattleLv[stat] = int8(cur)
				}
			}
			// 128（敌方）- n 回合内接受的物理伤害转化为体力恢复
			if battle.EnemyPhysDmgToHealRounds > 0 && skill.Category == 1 && damage > 0 {
				heal := damage
				newHP := battle.EnemyHP + heal
				if newHP > battle.EnemyMaxHP {
					newHP = battle.EnemyMaxHP
				}
				battle.EnemyHP = newHP
				damage = 0
			}
			battle.EnemyHP -= damage
			if battle.EnemyHP > battle.EnemyMaxHP {
				battle.EnemyHP = 0 // uint 下溢时变为极大值，统一置 0
			}
			if battle.EnemyHP == 0 {
				battle.LastHitWasCrit = isCritPlayer
			}
			// 110（敌方）- n 回合内每次受到攻击时 m% 几率使对手 stat 等级 -1
			if battle.EnemyDefendStatDropRounds > 0 && damage > 0 &&
				!sptboss.IsStatDropImmune(playerPetID) && battle.PlayerImmuneStatDropRounds == 0 {
				if rand.Intn(100) < int(battle.EnemyDefendStatDropChance) {
					stat := int(battle.EnemyDefendStatDropStat)
					cur := int(battle.PlayerBattleLv[stat]) - 1
					if cur < -6 {
						cur = -6
					}
					battle.PlayerBattleLv[stat] = int8(cur)
				}
			}
			// 116（敌方）- n 回合内每次受到攻击造成伤害的 1/5 恢复自身体力
			if battle.EnemyDefendHealRounds > 0 && damage > 0 {
				heal := damage / 5
				if heal > 0 {
					newHP := battle.EnemyHP + heal
					if newHP > battle.EnemyMaxHP {
						newHP = battle.EnemyMaxHP
					}
					battle.EnemyHP = newHP
				}
			}
			// 117（敌方）- n 回合内每次受到攻击 m% 概率使对手疲惫 1~3 回合
			if battle.EnemyDefendFatigueRounds > 0 && damage > 0 &&
				!sptboss.IsControlImmune(playerPetID) && battle.PlayerStatus[gameskills.StatusIndexFatigue] == 0 {
				if rand.Intn(100) < int(battle.EnemyDefendFatigueChance) {
					battle.PlayerStatus[gameskills.StatusIndexFatigue] = byte(rand.Intn(3) + 1)
				}
			}
			// 107 - 若本次攻击造成的伤害小于 n 则自身 xx 等级提升 1
			if playerHit && skill.EffectID == 107 && skill.Category != 4 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				if len(effArgs) >= 2 {
					thresh, statIdx := effArgs[0], effArgs[1]
					if statIdx >= 0 && statIdx < 6 && damage < uint32(thresh) {
						cur := int(battle.PlayerBattleLv[statIdx])
						cur++
						if cur > 6 {
							cur = 6
						}
						battle.PlayerBattleLv[statIdx] = int8(cur)
					}
				}
			}
			// 172 - 若后出手，则造成伤害的 1/n 恢复自身体力
			if playerHit && skill.EffectID == 172 && enemyFirst && damage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				divisor := 3
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					divisor = effArgs[0]
				}
				heal := damage / uint32(divisor)
				if heal > 0 {
					newHP := battle.PlayerHP + heal
					if newHP > battle.PlayerMaxHP {
						newHP = battle.PlayerMaxHP
					}
					battle.PlayerHP = newHP
					effectGainHP += int32(heal)
				}
			}
			// 458 - 若先出手则造成攻击伤害的 n% 恢复自身体力
			if playerHit && skill.EffectID == 458 && !enemyFirst && damage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				pct := 50
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					pct = effArgs[0]
				}
				heal := damage * uint32(pct) / 100
				if heal > 0 {
					newHP := battle.PlayerHP + heal
					if newHP > battle.PlayerMaxHP {
						newHP = battle.PlayerMaxHP
					}
					battle.PlayerHP = newHP
					effectGainHP += int32(heal)
				}
			}
			// 459 - 附加对手防御值 m% 的固定伤害
			if playerHit && skill.EffectID == 459 && damage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				pct := 20
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					pct = effArgs[0]
				}
				defVal := uint32(enemyStats.Defence)
				if skill.Category == 2 {
					defVal = uint32(enemyStats.SpDef)
				}
				bonus := defVal * uint32(pct) / 100
				if bonus > 0 && bonus <= battle.EnemyHP {
					battle.EnemyHP -= bonus
				} else if bonus > battle.EnemyHP {
					battle.EnemyHP = 0
				}
			}
			// 461 - 使用后若自身体力低于 1/m 则从下回合开始必定致命一击（复用 PlayerCritBuffRounds）
			if playerHit && skill.EffectID == 461 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				divisor := 3
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					divisor = effArgs[0]
				}
				if battle.PlayerMaxHP > 0 && battle.PlayerHP < battle.PlayerMaxHP/uint32(divisor) {
					battle.PlayerCritBuffRounds = 3
				}
			}
			// 463 - n 回合内每回合所受的伤害减少 m 点（设置回合状态）
			if playerHit && skill.EffectID == 463 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				rounds, amount := 2, 150
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					amount = effArgs[1]
				}
				if rounds > 10 {
					rounds = 10
				}
				battle.PlayerDamageReducePerRoundRounds = byte(rounds)
				battle.PlayerDamageReducePerRoundAmount = uint32(amount)
			}
			// 474 - 先出手时 m% 自身 stat 等级 +n
			if playerHit && skill.EffectID == 474 && !enemyFirst {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				stat, chance, stages := 2, 100, 1
				if len(effArgs) >= 1 {
					stat = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if len(effArgs) >= 3 && effArgs[2] > 0 {
					stages = effArgs[2]
				}
				if stat >= 0 && stat < 6 && rand.Intn(100) < chance {
					cur := int(battle.PlayerBattleLv[stat]) + stages
					if cur > 6 {
						cur = 6
					}
					if cur < -6 {
						cur = -6
					}
					battle.PlayerBattleLv[stat] = int8(cur)
				}
			}
			// 475 - 若造成的伤害不足 m，则下 n 回合的攻击必定致命一击
			if playerHit && skill.EffectID == 475 && damage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				threshold, rounds := 300, 2
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					threshold = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					rounds = effArgs[1]
				}
				if damage < uint32(threshold) {
					battle.PlayerCritBuffRounds = byte(rounds + 1)
				}
			}
			// 476 - 后出手时恢复 m 点体力
			if playerHit && skill.EffectID == 476 && enemyFirst {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				amount := 100
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					amount = effArgs[0]
				}
				newHP := battle.PlayerHP + uint32(amount)
				if newHP > battle.PlayerMaxHP {
					newHP = battle.PlayerMaxHP
				}
				battle.PlayerHP = newHP
				effectGainHP += int32(amount)
			}
			// 186 - 后出手时 m% 自身 stat 等级 +n
			if playerHit && skill.EffectID == 186 && enemyFirst {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				stat, chance, stages := 0, 100, 1
				if len(effArgs) >= 1 {
					stat = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if len(effArgs) >= 3 && effArgs[2] > 0 {
					stages = effArgs[2]
				}
				if stat >= 0 && stat < 6 && rand.Intn(100) < chance {
					cur := int(battle.PlayerBattleLv[stat]) + stages
					if cur > 6 {
						cur = 6
					}
					battle.PlayerBattleLv[stat] = int8(cur)
				}
			}
			// 122 - 先出手时 m% 对方 stat 等级降低 n
			if playerHit && skill.EffectID == 122 && !enemyFirst && !sptboss.IsStatDropImmune(battle.EnemyID) {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				stat, chance, stages := 0, 50, -1
				if len(effArgs) >= 1 {
					stat = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if len(effArgs) >= 3 {
					stages = effArgs[2]
				}
				if stat >= 0 && stat < 6 && rand.Intn(100) < chance {
					cur := int(battle.EnemyBattleLv[stat]) + stages
					if cur < -6 {
						cur = -6
					}
					if cur > 6 {
						cur = 6
					}
					battle.EnemyBattleLv[stat] = int8(cur)
				}
			}
			// 148 - 后出手时 m% 对方 stat 等级降低 n
			if playerHit && skill.EffectID == 148 && enemyFirst && !sptboss.IsStatDropImmune(battle.EnemyID) {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				stat, chance, stages := 0, 50, -1
				if len(effArgs) >= 1 {
					stat = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if len(effArgs) >= 3 {
					stages = effArgs[2]
				}
				if stat >= 0 && stat < 6 && rand.Intn(100) < chance {
					cur := int(battle.EnemyBattleLv[stat]) + stages
					if cur < -6 {
						cur = -6
					}
					if cur > 6 {
						cur = 6
					}
					battle.EnemyBattleLv[stat] = int8(cur)
				}
			}
			// 147 - 后出手时 n% 概率使对方 XX
			if playerHit && skill.EffectID == 147 && enemyFirst && !sptboss.IsStatusImmune(battle.EnemyID) {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				chance, statusIdx := 30, 6
				if len(effArgs) >= 1 {
					chance = effArgs[0]
				}
				if len(effArgs) >= 2 {
					statusIdx = effArgs[1]
				}
				if statusIdx >= 0 && statusIdx < 20 && rand.Intn(100) < chance {
					if battle.EnemyStatus[statusIdx] == 0 {
						battle.EnemyStatus[statusIdx] = byte(rand.Intn(2) + 2)
					}
				}
			}
			// 173 - 先出手时 n% 概率使对方 XX
			if playerHit && skill.EffectID == 173 && !enemyFirst && !sptboss.IsStatusImmune(battle.EnemyID) {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				chance, statusIdx := 30, 6
				if len(effArgs) >= 1 {
					chance = effArgs[0]
				}
				if len(effArgs) >= 2 {
					statusIdx = effArgs[1]
				}
				if statusIdx >= 0 && statusIdx < 20 && rand.Intn(100) < chance {
					if battle.EnemyStatus[statusIdx] == 0 {
						battle.EnemyStatus[statusIdx] = byte(rand.Intn(2) + 2)
					}
				}
			}

			// 484 - 连击 n 次，每次附加 bonus 点固定伤害（简化：附加 n*bonus 点固定伤害）
			if playerHit && skill.EffectID == 484 && damage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				times, bonus := 5, 1
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					times = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					bonus = effArgs[1]
				}
				totalBonus := uint32(times * bonus)
				if totalBonus > 0 && battle.EnemyHP > 0 {
					if totalBonus > battle.EnemyHP {
						totalBonus = battle.EnemyHP
					}
					battle.EnemyHP -= totalBonus
				}
			}
			// 428 - 遇到天敌时附加 m 点固定伤害（仅当属性克制 typeMod > 1 时生效）
			if playerHit && skill.EffectID == 428 && damage > 0 {
				enemyPet := petMgr.Get(battle.EnemyID)
				typeMod := 1.0
				if enemyPet != nil {
					typeMod = gamebattle.GetTypeMultiplierDual(skill.Type, enemyPet.Type, enemyPet.Type2)
				}
				if typeMod > 1.0 && battle.EnemyHP > 0 {
					effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
					bonus := uint32(50)
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						bonus = uint32(effArgs[0])
					}
					if bonus > 0 {
						if bonus > battle.EnemyHP {
							bonus = battle.EnemyHP
						}
						battle.EnemyHP -= bonus
					}
				}
			}
			// 464 - 命中时 m% 概率使对方烧伤
			if playerHit && skill.EffectID == 464 && damage > 0 && !sptboss.IsStatusImmune(battle.EnemyID) && battle.EnemyImmuneStatusRounds == 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				chance := 30
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					chance = effArgs[0]
				}
				if rand.Intn(100) < chance && battle.EnemyStatus[gameskills.StatusIndexBurn] == 0 {
					battle.EnemyStatus[gameskills.StatusIndexBurn] = byte(rand.Intn(2) + 1)
					dmg := battle.EnemyMaxHP / 8
					if dmg > battle.EnemyHP {
						dmg = battle.EnemyHP
					}
					battle.EnemyHP -= dmg
				}
			}
			// 115 - n% 概率附加速度的 1/m 点固定伤害
			if playerHit && skill.EffectID == 115 && damage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				chance, divisor := 30, 2
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					chance = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					divisor = effArgs[1]
				}
				if rand.Intn(100) < chance {
					bonus := uint32(playerStats.Speed) / uint32(divisor)
					if bonus > 0 && battle.EnemyHP > 0 {
						if bonus > battle.EnemyHP {
							bonus = battle.EnemyHP
						}
						battle.EnemyHP -= bonus
					}
				}
			}
			// 119 - 伤害为偶数时 30% 疲惫 +1 回合；奇数时 30% 速度 +1
			if playerHit && skill.EffectID == 119 && damage > 0 {
				if damage%2 == 0 {
					if !sptboss.IsControlImmune(battle.EnemyID) && rand.Intn(100) < 30 {
						if battle.EnemyStatus[gameskills.StatusIndexFatigue] == 0 {
							battle.EnemyStatus[gameskills.StatusIndexFatigue] = 1
						}
					}
				} else {
					if rand.Intn(100) < 30 {
						cur := int(battle.PlayerBattleLv[gameskills.StatSpeed]) + 1
						if cur > 6 {
							cur = 6
						}
						battle.PlayerBattleLv[gameskills.StatSpeed] = int8(cur)
					}
				}
			}
			// 134 - 造成的伤害低于 n，则自身所有技能的 PP +m
			if playerHit && skill.EffectID == 134 && damage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				threshold, ppBonus := 100, 1
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					threshold = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					ppBonus = effArgs[1]
				}
				if damage < uint32(threshold) {
					for i := 0; i < 4; i++ {
						if battle.PlayerSkillPP[i] > 0 {
							newPP := int(battle.PlayerSkillPP[i]) + ppBonus
							if newPP > 255 {
								newPP = 255
							}
							battle.PlayerSkillPP[i] = byte(newPP)
						}
					}
				}
			}
			// 188 - 若自身处于异常状态，则附加对应的反击效果（烧伤→烧伤对手，冻伤→冻伤对手，中毒→中毒对手）
			if playerHit && skill.EffectID == 188 && damage > 0 && !sptboss.IsStatusImmune(battle.EnemyID) {
				if battle.PlayerStatus[gameskills.StatusIndexBurn] > 0 && battle.EnemyStatus[gameskills.StatusIndexBurn] == 0 {
					battle.EnemyStatus[gameskills.StatusIndexBurn] = byte(rand.Intn(2) + 1)
					dmg := battle.EnemyMaxHP / 8
					if dmg > battle.EnemyHP {
						dmg = battle.EnemyHP
					}
					battle.EnemyHP -= dmg
				} else if battle.PlayerStatus[gameskills.StatusIndexFreeze] > 0 && battle.EnemyStatus[gameskills.StatusIndexFreeze] == 0 {
					battle.EnemyStatus[gameskills.StatusIndexFreeze] = byte(rand.Intn(2) + 1)
					dmg := battle.EnemyMaxHP / 8
					if dmg > battle.EnemyHP {
						dmg = battle.EnemyHP
					}
					battle.EnemyHP -= dmg
				} else if battle.PlayerStatus[gameskills.StatusIndexPoison] > 0 && battle.EnemyStatus[gameskills.StatusIndexPoison] == 0 {
					battle.EnemyStatus[gameskills.StatusIndexPoison] = byte(rand.Intn(2) + 1)
					dmg := battle.EnemyMaxHP / 8
					if dmg > battle.EnemyHP {
						dmg = battle.EnemyHP
					}
					battle.EnemyHP -= dmg
				}
			}
			// 181 - n% 概率使对手XX，每次使用m%增加，最高k%（累积几率）
			// SideEffectArg: statusIndex chance increment maxChance
			if playerHit && skill.EffectID == 181 && !sptboss.IsStatusImmune(battle.EnemyID) && battle.EnemyImmuneStatusRounds == 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				statusIdx, baseChance, increment, maxChance := gameskills.StatusIndexBurn, 30, 10, 100
				if len(effArgs) >= 1 {
					statusIdx = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					baseChance = effArgs[1]
				}
				if len(effArgs) >= 3 && effArgs[2] > 0 {
					increment = effArgs[2]
				}
				if len(effArgs) >= 4 && effArgs[3] > 0 {
					maxChance = effArgs[3]
				}
				// 若是第一次使用，初始化
				if battle.Player181CurrentChance == 0 {
					battle.Player181CurrentChance = byte(baseChance)
					battle.Player181StatusIdx = byte(statusIdx)
					battle.Player181MaxChance = byte(maxChance)
					battle.Player181Increment = byte(increment)
				}
				currentChance := int(battle.Player181CurrentChance)
				if statusIdx >= 0 && statusIdx < 20 && rand.Intn(100) < currentChance {
					if battle.EnemyStatus[statusIdx] == 0 {
						battle.EnemyStatus[statusIdx] = byte(rand.Intn(2) + 2)
					}
				}
				// 增加几率
				newChance := currentChance + increment
				if newChance > maxChance {
					newChance = maxChance
				}
				battle.Player181CurrentChance = byte(newChance)
			}
			// 441 - 每次攻击暴击率 +n%，最高 m%（以 1/16 为单位累积）
			if playerHit && skill.EffectID == 441 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				increment, maxBonus := 1, 8
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					increment = effArgs[0] / 6
					if increment < 1 {
						increment = 1
					}
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					maxBonus = effArgs[1] / 6
					if maxBonus < 1 {
						maxBonus = 1
					}
				}
				newBonus := int(battle.PlayerCritRateBonus) + increment
				if newBonus > maxBonus {
					newBonus = maxBonus
				}
				if newBonus > 16 {
					newBonus = 16
				}
				battle.PlayerCritRateBonus = byte(newBonus)
			}
			// 490 - 若造成伤害超过 m，则自身速度 +n 级
			if playerHit && skill.EffectID == 490 && damage > 0 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				threshold, stages := 200, 1
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					threshold = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					stages = effArgs[1]
				}
				if damage > uint32(threshold) {
					cur := int(battle.PlayerBattleLv[gameskills.StatSpeed]) + stages
					if cur > 6 {
						cur = 6
					}
					battle.PlayerBattleLv[gameskills.StatSpeed] = int8(cur)
				}
			}
			// 66/67/158 - 当次攻击击败对方时
			if enemyHPBeforeAction > 0 && battle.EnemyHP == 0 && playerHit {
				eids := gameskills.ParseSideEffectIds(skill.SideEffect)
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				offset := 0
				for _, eid := range eids {
					n := gameskills.EffectArgCount(eid)
					if offset+n > len(effArgs) {
						break
					}
					if eid == 66 {
						div := 2
						if n > 0 && effArgs[offset] > 0 {
							div = effArgs[offset]
						}
						heal := battle.PlayerMaxHP / uint32(div)
						if heal > 0 {
							battle.PlayerHP += heal
							if battle.PlayerHP > battle.PlayerMaxHP {
								battle.PlayerHP = battle.PlayerMaxHP
							}
						}
					}
					if eid == 67 {
						if n > 0 && effArgs[offset] > 0 {
							battle.EnemyKillReduceMaxHpDivisor = byte(effArgs[offset])
						}
					}
					// 158 - 当次攻击击败对手，则 m% 自身 stat 等级 +n
					if eid == 158 && n >= 3 {
						stat, chance, stages := effArgs[offset], effArgs[offset+1], effArgs[offset+2]
						if stat >= 0 && stat < 6 && rand.Intn(100) < chance {
							cur := int(battle.PlayerBattleLv[stat]) + stages
							if cur > 6 {
								cur = 6
							}
							if cur < -6 {
								cur = -6
							}
							battle.PlayerBattleLv[stat] = int8(cur)
						}
					}
					// 421 - 击败对手时，将对手的能力强化转移到自身
					if eid == 421 {
						for i := 0; i < 6; i++ {
							if battle.EnemyBattleLv[i] > 0 {
								cur := int(battle.PlayerBattleLv[i]) + int(battle.EnemyBattleLv[i])
								if cur > 6 {
									cur = 6
								}
								battle.PlayerBattleLv[i] = int8(cur)
								battle.EnemyBattleLv[i] = 0
							}
						}
					}
					// 185 - 若击败处于 XX 状态的对手，则下一只出场的对手也进入 XX 状态
					if eid == 185 && n >= 1 {
						statusIdx := effArgs[offset]
						if statusIdx >= 0 && statusIdx < 20 && battle.EnemyStatus[statusIdx] > 0 {
							battle.PlayerTransferStatusToNextEnemy = byte(statusIdx)
						}
					}
					offset += n
				}
			}

			// 特性：汲取(1039) —— 攻击命中后吸取命中伤害的8%体力
			if playerTrait == 1039 && damage > 0 {
				heal := uint32(math.Floor(float64(damage) * 0.08))
				if heal > 0 {
					newHP := battle.PlayerHP + heal
					if newHP > battle.PlayerMaxHP {
						newHP = battle.PlayerMaxHP
					}
					battle.PlayerHP = newHP
					effectGainHP += int32(heal)
				}
			}

			// effect 60：n回合内,每回合附加m点固定伤害
			// SideEffectArg: rounds damagePerTurn（例如 "5 30" 表示当前回合 + 之后4回合，每次自己出手后都额外扣30点固定伤害）
			if skill.EffectID == 60 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				rounds, perTurn := 0, 0
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					perTurn = effArgs[1]
				}
				if rounds < 0 {
					rounds = 0
				}
				if perTurn < 0 {
					perTurn = 0
				}
				if rounds > 0 && perTurn > 0 {
					battle.EnemyFixedDotRounds = byte(rounds)
					battle.EnemyFixedDotDamage = uint32(perTurn)
				}
			}
			// effect 76：m% 几率在 n 回合内每回合造成 k 点固定伤害
			if skill.EffectID == 76 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				chance, rounds, perTurn := 100, 0, 0
				if len(effArgs) >= 1 {
					chance = effArgs[0]
				}
				if len(effArgs) >= 2 {
					rounds = effArgs[1]
				}
				if len(effArgs) >= 3 {
					perTurn = effArgs[2]
				}
				if chance < 0 {
					chance = 0
				}
				if chance > 100 {
					chance = 100
				}
				if rounds > 0 && perTurn > 0 && rand.Intn(100) < chance {
					battle.EnemyFixedDotRounds = byte(rounds)
					battle.EnemyFixedDotDamage = uint32(perTurn)
				}
			}
			// effect 77：n 回合内每次使用技能恢复 m 点体力（含当回合使用）
			if skill.EffectID == 77 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				rounds, amount := 0, 0
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					amount = effArgs[1]
				}
				if rounds > 0 && amount > 0 {
					battle.PlayerRegenPerUseRounds = byte(rounds)
					battle.PlayerRegenPerUseAmount = uint32(amount)
				}
			}
			// effect 78：n 回合内物理攻击对自身必定 miss
			if skill.EffectID == 78 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				rounds := 0
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if rounds > 0 {
					battle.PlayerPhysMissRounds = byte(rounds)
				}
			}
			// effect 83：自身雄性下两回合必定先手；雌性下两回合必定暴击
			if skill.EffectID == 83 && attackerPet != nil {
				if attackerPet.Gender == 1 {
					battle.PlayerMaleFirstStrikeRounds = 2
				} else if attackerPet.Gender == 2 {
					battle.PlayerFemaleCritRounds = 2
				}
			}
			// effect 84：n 回合内受到物理攻击时 m% 几率将对手麻痹
			if skill.EffectID == 84 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				rounds, chance := 5, 50
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if rounds > 0 {
					battle.PlayerParalyzeOnPhysHitRounds = byte(rounds)
					if chance > 100 {
						chance = 100
					}
					battle.PlayerParalyzeOnPhysHitChance = byte(chance)
				}
			}
			// effect 86/106：n 回合内属性（特殊）攻击对自身必定 miss
			if skill.EffectID == 86 || skill.EffectID == 106 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				rounds := 0
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if rounds > 0 {
					battle.PlayerSpecialMissRounds = byte(rounds)
				}
			}
			// effect 89：n 回合内每次造成伤害的 1/m 恢复体力
			if skill.EffectID == 89 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				rounds, div := 0, 4
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					div = effArgs[1]
				}
				if rounds > 0 {
					battle.PlayerLifestealRounds = byte(rounds)
					battle.PlayerLifestealDivisor = byte(div)
				}
			}
			// effect 90：n 回合内自身造成的伤害为 m 倍（同 53）
			if skill.EffectID == 90 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				rounds, mult := 0, 2
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					mult = effArgs[1]
				}
				if rounds > 0 {
					battle.PlayerDamageMultRounds = byte(rounds)
					battle.PlayerDamageMult = byte(mult)
				}
			}
			// effect 92：n 回合内受到物理攻击时 m% 几率将对手冻伤
			if skill.EffectID == 92 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				rounds, chance := 5, 50
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if rounds > 0 {
					battle.PlayerFreezeOnPhysHitRounds = byte(rounds)
					if chance > 100 {
						chance = 100
					}
					battle.PlayerFreezeOnPhysHitChance = byte(chance)
				}
			}
			// effect 98：n 回合内对雄性精灵的伤害为 m 倍
			if skill.EffectID == 98 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				rounds, mult := 0, 2
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					mult = effArgs[1]
				}
				if rounds > 0 {
					battle.PlayerMaleDamageMultRounds = byte(rounds)
					battle.PlayerMaleDamageMult = byte(mult)
				}
			}
			// effect 104：n 回合内每次直接攻击 m% 几率附带衰弱
			if skill.EffectID == 104 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				rounds, chance := 0, 50
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if rounds > 0 {
					battle.PlayerWeaknessOnHitRounds = byte(rounds)
					if chance > 100 {
						chance = 100
					}
					battle.PlayerWeaknessOnHitChance = byte(chance)
				}
			}
			// effect 108：n 回合内受到物理攻击时 m% 几率将对手烧伤
			if skill.EffectID == 108 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				rounds, chance := 5, 50
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if rounds > 0 {
					battle.PlayerBurnOnPhysHitRounds = byte(rounds)
					if chance > 100 {
						chance = 100
					}
					battle.PlayerBurnOnPhysHitChance = byte(chance)
				}
			}
			// effect 109：n 回合内造成伤害时 m% 几率令对手冻伤
			if skill.EffectID == 109 {
				effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
				rounds, chance := 5, 50
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if rounds > 0 {
					battle.PlayerFreezeOnDealDamageRounds = byte(rounds)
					if chance > 100 {
						chance = 100
					}
					battle.PlayerFreezeOnDealDamageChance = byte(chance)
				}
			}
			// 77（已有状态）：每次使用技能恢复 m 点体力（非 77 技能时也生效）
			if playerHit && battle.PlayerRegenPerUseRounds > 0 && battle.PlayerRegenPerUseAmount > 0 {
				heal := battle.PlayerRegenPerUseAmount
				if heal > battle.PlayerMaxHP-battle.PlayerHP {
					heal = battle.PlayerMaxHP - battle.PlayerHP
				}
				if heal > 0 {
					battle.PlayerHP += heal
					effectGainHP += int32(heal)
				}
			}

		var effectRecoil uint32
		// 91 - 镜像前保存旧值，ApplyEffect 后若镜像回合>0 则双方能力/状态变化同步
		var oldPlayerLv [6]int8
		var oldEnemyLv [6]int8
		var oldPlayerStatus, oldEnemyStatus [20]byte
		if battle.PlayerStatusMirrorRounds > 0 {
			oldPlayerLv = battle.PlayerBattleLv
			oldEnemyLv = battle.EnemyBattleLv
			oldPlayerStatus = battle.PlayerStatus
			oldEnemyStatus = battle.EnemyStatus
		}
		effectGainHP, effectRecoil = gameskills.ApplyEffect(skill, damage,
			&battle.PlayerHP, &battle.EnemyHP,
			battle.PlayerMaxHP, battle.EnemyMaxHP,
			&battle.PlayerBattleLv, &battle.EnemyBattleLv,
			&battle.PlayerStatus, &battle.EnemyStatus, battle.EnemyID,
			battle.EnemyImmuneStatDropRounds, battle.EnemyImmuneStatusRounds)
		_ = effectRecoil
		if battle.PlayerStatusMirrorRounds > 0 {
			for i := 0; i < 6; i++ {
				deltaE := int(battle.EnemyBattleLv[i]) - int(oldEnemyLv[i])
				deltaP := int(battle.PlayerBattleLv[i]) - int(oldPlayerLv[i])
				v := int(oldPlayerLv[i]) + deltaE + deltaP
				if v > 6 {
					v = 6
				}
				if v < -6 {
					v = -6
				}
				battle.PlayerBattleLv[i] = int8(v)
				v = int(oldEnemyLv[i]) + deltaE + deltaP
				if v > 6 {
					v = 6
				}
				if v < -6 {
					v = -6
				}
				battle.EnemyBattleLv[i] = int8(v)
			}
			for i := 0; i < 20; i++ {
				deltaE := int(battle.EnemyStatus[i]) - int(oldEnemyStatus[i])
				deltaP := int(battle.PlayerStatus[i]) - int(oldPlayerStatus[i])
				v := int(oldPlayerStatus[i]) + deltaE + deltaP
				if v < 0 {
					v = 0
				}
				if v > 10 {
					v = 10
				}
				battle.PlayerStatus[i] = byte(v)
				v = int(oldEnemyStatus[i]) + deltaE + deltaP
				if v < 0 {
					v = 0
				}
				if v > 10 {
					v = 10
				}
				battle.EnemyStatus[i] = byte(v)
			}
		}

		// 暴击率提升效果（SideEffect 58 系列）：SideEffectArg[0] = 持续回合数
		// 表现：下 n 回合内，自身使用“攻击技能”时必定打出致命一击。
		if skill.EffectID == 58 {
			effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
			if len(effArgs) >= 1 {
				rounds := effArgs[0]
				if rounds < 0 {
					rounds = 0
				}
				if rounds > 0 {
					// +1 是为了配合“每个战斗回合结束时自动减 1”的机制，
					// 确保玩家在之后能获得 param1[0] 个完整回合的必定暴击效果。
					battle.PlayerCritBuffRounds = byte(rounds + 1)
				}
			}
		}

		// 多效果技能（如 43 508、1635）：按 SideEffect 列表依次消耗参数并设置回合状态
		if playerHit {
			eids := gameskills.ParseSideEffectIds(skill.SideEffect)
			effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
			offset := 0
			for _, eid := range eids {
				n := gameskills.EffectArgCount(eid)
				if offset+n > len(effArgs) {
					break
				}
				subArgs := effArgs[offset : offset+n]
				offset += n
				switch eid {
				case 58: // 下 n 回合攻击技能必定暴击（多效果时也生效）
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						rounds := subArgs[0]
						if rounds > 10 {
							rounds = 10
						}
						battle.PlayerCritBuffRounds = byte(rounds + 1)
					}
				case 508: // 减少 m 点下回合所受的伤害（魂之再生等）
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerNextTurnDamageReduce = uint32(subArgs[0])
					}
				case 81: // 下 n 回合自身攻击技能必定命中
					rounds := 3
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						rounds = subArgs[0]
					}
					if rounds > 10 {
						rounds = 10
					}
					battle.PlayerMustHitRounds = byte(rounds + 1) // +1 使本回合也生效
				case 60: // n 回合内每回合附加 m 点固定伤害
					if len(subArgs) >= 2 && subArgs[0] > 0 && subArgs[1] > 0 {
						battle.EnemyFixedDotRounds = byte(subArgs[0])
						battle.EnemyFixedDotDamage = uint32(subArgs[1])
					}
				case 76: // m% 几率 n 回合每回合 k 点固定伤害
					if len(subArgs) >= 3 && subArgs[1] > 0 && subArgs[2] > 0 {
						chance := 100
						if subArgs[0] > 0 {
							chance = subArgs[0]
						}
						if rand.Intn(100) < chance {
							battle.EnemyFixedDotRounds = byte(subArgs[1])
							battle.EnemyFixedDotDamage = uint32(subArgs[2])
						}
					}
				case 77: // n 回合内每次使用技能恢复 m 点体力
					if len(subArgs) >= 2 && subArgs[0] > 0 && subArgs[1] > 0 {
						battle.PlayerRegenPerUseRounds = byte(subArgs[0])
						battle.PlayerRegenPerUseAmount = uint32(subArgs[1])
					}
				case 78: // n 回合内物理攻击对自身必定 miss
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerPhysMissRounds = byte(subArgs[0])
					}
				case 83: // 自身雄性下两回合必定先手；雌性下两回合必定暴击
					if attackerPet != nil {
						if attackerPet.Gender == 1 {
							battle.PlayerMaleFirstStrikeRounds = 2
						} else if attackerPet.Gender == 2 {
							battle.PlayerFemaleCritRounds = 2
						}
					}
				case 84: // n 回合内受到物理攻击时 m% 几率将对手麻痹
					if len(subArgs) >= 2 && subArgs[0] > 0 {
						battle.PlayerParalyzeOnPhysHitRounds = byte(subArgs[0])
						chance := subArgs[1]
						if chance > 100 {
							chance = 100
						}
						battle.PlayerParalyzeOnPhysHitChance = byte(chance)
					}
				case 86: // n 回合内属性（特殊）攻击对自身必定 miss
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerSpecialMissRounds = byte(subArgs[0])
					}
				case 91: // n 回合内双方状态变化同时影响己方与对手
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerStatusMirrorRounds = byte(subArgs[0])
					}
				case 89: // n 回合内造成伤害时吸血 damage/divisor
					n, div := 0, 2
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						div = subArgs[1]
					}
					if n > 0 {
						battle.PlayerLifestealRounds = byte(n)
						battle.PlayerLifestealDivisor = byte(div)
					}
				case 90: // n 回合己方攻击伤害 m 倍（同 53）
					n, mult := 0, 2
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						mult = subArgs[1]
					}
					if n > 0 {
						battle.PlayerDamageMultRounds = byte(n)
						battle.PlayerDamageMult = byte(mult)
					}
				case 92: // n 回合内受到物理攻击时 m% 几率将对手冻伤
					n, chance := 0, 50
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						chance = subArgs[1]
					}
					if n > 0 {
						battle.PlayerFreezeOnPhysHitRounds = byte(n)
						if chance > 100 {
							chance = 100
						}
						battle.PlayerFreezeOnPhysHitChance = byte(chance)
					}
				case 127: // n% 概率 m 回合内受到伤害减半
					chance, rounds := 50, 3
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						chance = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						rounds = subArgs[1]
					}
					if chance > 100 {
						chance = 100
					}
					if rand.Intn(100) < chance && rounds > 0 {
						battle.PlayerDamageHalfRounds = byte(rounds)
					}
				case 144: // 消耗全部体力，下一只 n 回合免疫异常（HP 已在 ApplyEffect 归零）
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerSacrificeImmuneStatusRounds = byte(subArgs[0])
					}
				case 146: // n 回合内受物理攻击时 m% 使对方中毒
					n, m := 0, 50
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						m = subArgs[1]
					}
					if m > 100 {
						m = 100
					}
					if n > 0 {
						battle.PlayerPoisonOnPhysHitRounds = byte(n)
						battle.PlayerPoisonOnPhysHitChance = byte(m)
					}
				case 150: // n 回合内对手每回合防、特防等级 m
					n, m := 0, 1
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if len(subArgs) >= 2 {
						m = subArgs[1]
					}
					if n > 0 {
						battle.EnemyDefSpDefRounds = byte(n)
						if m < -6 {
							m = -6
						}
						if m > 6 {
							m = 6
						}
						battle.EnemyDefSpDefStages = int8(m)
					}
				case 98: // n 回合内对雄性精灵的伤害为 m 倍
					n, mult := 0, 2
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						mult = subArgs[1]
					}
					if n > 0 {
						battle.PlayerMaleDamageMultRounds = byte(n)
						battle.PlayerMaleDamageMult = byte(mult)
					}
				case 104: // n 回合内每次直接攻击 m% 几率附带衰弱
					n, chance := 0, 50
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						chance = subArgs[1]
					}
					if n > 0 {
						battle.PlayerWeaknessOnHitRounds = byte(n)
						if chance > 100 {
							chance = 100
						}
						battle.PlayerWeaknessOnHitChance = byte(chance)
					}
				case 106: // n 回合内属性（特殊）攻击对自身必定 miss（同 86）
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerSpecialMissRounds = byte(subArgs[0])
					}
				case 107: // 伤害小于 n 则自身 stat 等级+1（每击判定，无回合状态）
					// 参数在伤害应用时从技能读取，此处仅占位保证多效果解析不越界
				case 108: // n 回合内受到物理攻击时 m% 几率将对手烧伤
					n, chance := 0, 50
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						chance = subArgs[1]
					}
					if n > 0 {
						battle.PlayerBurnOnPhysHitRounds = byte(n)
						if chance > 100 {
							chance = 100
						}
						battle.PlayerBurnOnPhysHitChance = byte(chance)
					}
				case 109: // n 回合内造成伤害时 m% 几率令对手冻伤
					n, chance := 0, 50
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						chance = subArgs[1]
					}
					if n > 0 {
						battle.PlayerFreezeOnDealDamageRounds = byte(n)
						if chance > 100 {
							chance = 100
						}
						battle.PlayerFreezeOnDealDamageChance = byte(chance)
					}
				case 1635: // 誓言之约：k 回合后恢复全部体力
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						battle.PlayerDelayedFullHealRounds = byte(subArgs[1])
					}
				// BOSS/特殊多效果：仅消费参数，不修改战斗状态；后续可按配置扩展
				case 691, 700, 1083, 1248, 1257, 1605, 1850, 1925, 2236, 2237:
					// 单参或已由 effectArgCount 切分的 subArgs，此处仅占位
				case 773, 935:
					// 简/极 无独立参数（0 参）
				case 976:
					// 简/极 第二参（如 28），可扩展为当回合固定伤害等
				case 1211:
					// 希 双参（如 100 300）
				case 1470:
					// 简/极 首参（如 1）
				case 1603:
					// 谄诳/红莲等 两参（如 100 1）
				case 439: // 若自身处于能力下降或异常则对手每回合受到 m 点固定伤害
					nR, dmg := 5, 200
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						nR = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						dmg = subArgs[1]
					}
					if nR > 10 {
						nR = 10
					}
					battle.PlayerDealFixedDotWhenWeakRounds = byte(nR)
					battle.PlayerDealFixedDotWhenWeakDamage = uint32(dmg)
				case 448: // n 回合内每回合对手全能力降低 stages 级
					nR, stages := 2, -1
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						nR = subArgs[0]
					}
					if len(subArgs) >= 2 {
						stages = subArgs[1]
						if stages == 0 {
							stages = -1
						}
					}
					if stages > 0 {
						stages = -stages
					}
					if stages < -6 {
						stages = -6
					}
					if nR > 10 {
						nR = 10
					}
					battle.EnemyAllStatDropRounds = byte(nR)
					battle.EnemyAllStatDropStages = int8(stages)
				case 478: // n 回合内令对手使用的属性技能无效
					nR := 2
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						nR = subArgs[0]
					}
					if nR > 10 {
						nR = 10
					}
					battle.EnemyStatusSkillInvalidRounds = byte(nR)
				case 545: // n 回合内若受到伤害高于 m 则对手获得效果 type
					nR, thresh, typ := 3, 200, 1
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						nR = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						thresh = subArgs[1]
					}
					if len(subArgs) >= 3 {
						typ = subArgs[2]
					}
					if nR > 10 {
						nR = 10
					}
					battle.PlayerReflectStatusWhenHitRounds = byte(nR)
					battle.PlayerReflectStatusWhenHitThreshold = uint32(thresh)
					battle.PlayerReflectStatusWhenHitType = byte(typ)
				case 87: // 恢复自身所有技能 PP 值
					for i := 0; i < 4; i++ {
						if battle.PlayerSkillIDs[i] != 0 {
							if sk := skillMgr.Get(int(battle.PlayerSkillIDs[i])); sk != nil && sk.MaxPP > 0 {
								battle.PlayerSkillPP[i] = byte(sk.MaxPP)
							} else {
								battle.PlayerSkillPP[i] = 35
							}
						}
					}
				case 21: // m~n 回合每回合反弹对手伤害的 1/k（多效果时用第2、3参为回合数、除数）
					nR, k := 3, 4
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						nR = subArgs[1]
					}
					if len(subArgs) >= 3 && subArgs[2] > 0 {
						k = subArgs[2]
					}
					if nR > 10 {
						nR = 10
					}
					if k > 10 {
						k = 10
					}
					battle.PlayerReflectDamageRounds = byte(nR)
					battle.PlayerReflectDamageDivisor = byte(k)
				case 32: // n 回合暴击率增加 1/16
					nR := 3
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						nR = subArgs[0]
					}
					if nR > 10 {
						nR = 10
					}
					battle.PlayerCritRateBonusRounds = byte(nR + 1)
				case 454: // 当自身血量少于 1/n 时先制 +m
					nDiv, mBonus := 3, 1
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						nDiv = subArgs[0]
					}
					if len(subArgs) >= 2 {
						mBonus = subArgs[1]
					}
					if nDiv > 10 {
						nDiv = 10
					}
					battle.PlayerPriorityBonusWhenLowHPRounds = 1
					battle.PlayerPriorityBonusWhenLowHPDivisor = byte(nDiv)
					battle.PlayerPriorityBonusWhenLowHPBonus = mBonus
				case 482: // m% 几率先制 +n（本回合掷骰）
					mChance, nBonus := 30, 1
					if len(subArgs) >= 1 {
						mChance = subArgs[0]
					}
					if len(subArgs) >= 2 {
						nBonus = subArgs[1]
					}
					if mChance > 100 {
						mChance = 100
					}
					battle.PlayerPriorityBonusChance = byte(mChance)
					battle.PlayerPriorityBonusAmount = nBonus
				case 488: // 对手体力小于 threshold 时伤害增加 percent%
					thresh, percent := 400, byte(10)
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						thresh = subArgs[0]
					}
					if len(subArgs) >= 2 {
						percent = byte(subArgs[1])
					}
					if percent > 100 {
						percent = 100
					}
					battle.PlayerDamageBoostWhenEnemyLowThreshold = uint32(thresh)
					battle.PlayerDamageBoostWhenEnemyLowPercent = percent
				// 多效果时第二、第三效果也设置状态（41–50, 51–56, 57, 59, 62, 65, 68, 69, 71–73）
				case 41:
					n := 0
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						n = subArgs[1]
					} else if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if n > 0 {
						battle.PlayerFireResistRounds = byte(n)
					}
				case 42:
					n := 0
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						n = subArgs[1]
					} else if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if n > 0 {
						battle.PlayerElectricBoostRounds = byte(n)
					}
				case 44:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerSpDefHalfRounds = byte(subArgs[0])
					}
				case 45:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerCopyDefRounds = byte(subArgs[0])
					}
				case 46:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerBlockCount = byte(subArgs[0])
					}
				case 47:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerImmuneStatDropRounds = byte(subArgs[0])
					}
				case 48:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerImmuneStatusRounds = byte(subArgs[0])
					}
				case 49:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerShieldPoints = uint32(subArgs[0])
					}
				case 50:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerPhysDefHalfRounds = byte(subArgs[0])
					}
				case 51:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerCopyAtkRounds = byte(subArgs[0])
					}
				case 52:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerEvasionRounds = byte(subArgs[0])
					}
				case 53:
					n, mult := 0, 2
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						mult = subArgs[1]
					}
					if n > 0 {
						battle.PlayerDamageMultRounds = byte(n)
						battle.PlayerDamageMult = byte(mult)
					}
				case 54:
					n, mult := 0, 2
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						mult = subArgs[1]
					}
					if n > 0 {
						battle.PlayerDamageReductRounds = byte(n)
						battle.PlayerDamageReduct = byte(mult)
					}
				case 55:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerTypeSwapRounds = byte(subArgs[0])
					}
				case 56:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerTypeCopyRounds = byte(subArgs[0])
					}
				case 57:
					n, div := 0, 5
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if len(subArgs) >= 2 && subArgs[1] > 0 {
						div = subArgs[1]
					}
					if n > 0 {
						battle.PlayerRegenRounds = byte(n)
						battle.PlayerRegenDivisor = byte(div)
					}
				case 59:
					battle.PlayerSacrificeBuffActive = true
					for i := 0; i < 6; i++ {
						battle.PlayerSacrificeBuffStats[i] = 0
					}
					for i := 0; i+1 < len(subArgs); i += 2 {
						stat, stages := subArgs[i], subArgs[i+1]
						if stat >= 0 && stat < 6 && stages > 0 {
							battle.PlayerSacrificeBuffStats[stat] = int8(stages)
						}
					}
					if len(subArgs) == 0 {
						for i := 0; i < 6; i++ {
							battle.PlayerSacrificeBuffStats[i] = 1
						}
					}
				case 62:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerDestinyBondRounds = byte(subArgs[0])
					}
				case 65:
					n, mult, elemType := 0, 2, 0
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						n = subArgs[0]
					}
					if len(subArgs) >= 2 {
						mult = subArgs[1]
					}
					if len(subArgs) >= 3 {
						elemType = subArgs[2]
					}
					if n > 0 && mult > 0 {
						battle.PlayerElemPowerRounds = byte(n)
						battle.PlayerElemPowerMult = byte(mult)
						battle.PlayerElemPowerType = byte(elemType)
					}
				case 68:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerEndureRounds = byte(subArgs[0])
					}
				case 69:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerPotionReverseRounds = byte(subArgs[0])
					}
				case 71:
					battle.PlayerSacrificeCritActive = true
				case 72:
					battle.PlayerMissDeathActive = true
				case 73:
					if len(subArgs) >= 1 && subArgs[0] > 0 {
						battle.PlayerFirstStrikeReflectRounds = byte(subArgs[0])
						if !enemyFirst {
							battle.PlayerFirstStrikeReflectActive = true
						}
					}
				}
			}
		}

		// Effect ID 39：n% 降低对手所有技能 m 点 PP 值
		// SideEffectArg: n m（例如 "20 1" 表示 20% 几率每个技能 -1 PP）
		if skill.EffectID == 39 && playerHit {
			effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
			chance, ppReduction := 100, 1
			if len(effArgs) >= 1 {
				chance = effArgs[0]
			}
			if len(effArgs) >= 2 {
				ppReduction = effArgs[1]
			}
			if chance < 0 {
				chance = 0
			}
			if chance > 100 {
				chance = 100
			}
			if ppReduction < 0 {
				ppReduction = 0
			}
			if rand.Intn(100) < chance && ppReduction > 0 && !battle.EnemyPPInfinite {
				for i := 0; i < 4; i++ {
					if battle.EnemySkillPP[i] > 0 {
						if int(battle.EnemySkillPP[i]) >= ppReduction {
							battle.EnemySkillPP[i] -= byte(ppReduction)
						} else {
							battle.EnemySkillPP[i] = 0
						}
					}
				}
			}
		}

			// 对于“同生共死”一类基于血量差的技能，附加效果可能在普通伤害结算后再次大幅修改 enemyHP。
			// 为了让客户端看到的 lostHP/日志中的 Damage 与真实扣血一致，
			// 在效果应用后，用“出手前 HP - 当前 HP”重写 damage 与 damageCalc。
			if skill.EffectID == 7 {
				if enemyHPBeforeAction > battle.EnemyHP {
					actualLoss := enemyHPBeforeAction - battle.EnemyHP
					damage = actualLoss
					damageCalc = actualLoss
				} else {
					// 未造成额外伤害时，视为 0（例如敌方 HP <= 我方 HP）
					damage = 0
					damageCalc = 0
				}
			}
		}

		// 多回合 BUFF/护盾（41 火抗 42 电伤×2 44 特防减半 46 挡n次 47 免疫能力下降 48 免疫异常 49 吸收n点 50 物防减半）
		if playerHit {
			effArgs := gameskills.ParseSideEffectArg(skill.SideEffectArg)
			n := 0
			if len(effArgs) >= 1 {
				n = effArgs[0]
			}
			if n < 0 {
				n = 0
			}
			switch skill.EffectID {
			case 41:
				if len(effArgs) >= 2 {
					n = effArgs[1]
				}
				if n > 0 {
					battle.PlayerFireResistRounds = byte(n)
				}
			case 42:
				if len(effArgs) >= 2 {
					n = effArgs[1]
				}
				if n > 0 {
					battle.PlayerElectricBoostRounds = byte(n)
				}
			case 44:
				if n > 0 {
					battle.PlayerSpDefHalfRounds = byte(n)
				}
			case 46:
				if n > 0 {
					battle.PlayerBlockCount = byte(n)
				}
			case 47:
				if n > 0 {
					battle.PlayerImmuneStatDropRounds = byte(n)
				}
			case 48:
				if n > 0 {
					battle.PlayerImmuneStatusRounds = byte(n)
				}
			case 49:
				if n > 0 {
					battle.PlayerShieldPoints = uint32(n)
				}
			case 50:
				if n > 0 {
					battle.PlayerPhysDefHalfRounds = byte(n)
				}
			case 32: // n 回合暴击率增加 1/16（与多效果 case 32 一致）
				if n > 0 {
					if n > 10 {
						n = 10
					}
					battle.PlayerCritRateBonusRounds = byte(n + 1)
				}
			case 45: // 防御同对手
				if n > 0 {
					battle.PlayerCopyDefRounds = byte(n)
				}
			case 51: // 攻击同对手
				if n > 0 {
					battle.PlayerCopyAtkRounds = byte(n)
				}
			case 57: // n 回合每回合恢复 maxHP/m
				div := 5
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					div = effArgs[1]
				}
				if n > 0 {
					battle.PlayerRegenRounds = byte(n)
					battle.PlayerRegenDivisor = byte(div)
				}
			case 65: // n 回合内某属性技能威力 m 倍，SideEffectArg: n m elemType
				mult, elemType := 2, 0
				if len(effArgs) >= 2 {
					mult = effArgs[1]
				}
				if len(effArgs) >= 3 {
					elemType = effArgs[2]
				}
				if n > 0 && mult > 0 {
					battle.PlayerElemPowerRounds = byte(n)
					battle.PlayerElemPowerMult = byte(mult)
					battle.PlayerElemPowerType = byte(elemType)
				}
			case 68: // 1 回合内致死留 1 血
				if n > 0 {
					battle.PlayerEndureRounds = byte(n)
				}
			case 52: // n 回合本方先手时对方技能 miss
				if n > 0 {
					battle.PlayerEvasionRounds = byte(n)
				}
			case 53: // n 回合己方攻击伤害 m 倍，Arg: n m
				mult := 2
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					mult = effArgs[1]
				}
				if n > 0 {
					battle.PlayerDamageMultRounds = byte(n)
					battle.PlayerDamageMult = byte(mult)
				}
			case 54: // n 回合对方打我方伤害 1/m
				mult := 2
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					mult = effArgs[1]
				}
				if n > 0 {
					battle.PlayerDamageReductRounds = byte(n)
					battle.PlayerDamageReduct = byte(mult)
				}
			case 55: // n 回合属性反转
				if n > 0 {
					battle.PlayerTypeSwapRounds = byte(n)
				}
			case 56: // n 回合属性与对方相同
				if n > 0 {
					battle.PlayerTypeCopyRounds = byte(n)
				}
			case 91: // n 回合内双方状态变化同时影响己方与对手（能力/异常镜像）
				if n > 0 {
					battle.PlayerStatusMirrorRounds = byte(n)
				}
			case 62: // n 回合后若己方存活则对方死亡（镇魂歌）
				if n > 0 {
					battle.PlayerDestinyBondRounds = byte(n)
				}
			case 59: // 牺牲强化下一只：当前精灵被击败时，下一只上场的精灵获得能力强化
				// SideEffectArg: stat1 stages1 stat2 stages2 ... (例如 "0 2 2 1" 表示攻击+2级、特攻+1级)
				// 如果没有参数，默认全能力+1级
				battle.PlayerSacrificeBuffActive = true
				// 清空之前的强化记录
				for i := 0; i < 6; i++ {
					battle.PlayerSacrificeBuffStats[i] = 0
				}
				if len(effArgs) >= 2 {
					// 解析参数：stat stages 对
					for i := 0; i+1 < len(effArgs); i += 2 {
						stat := effArgs[i]
						stages := effArgs[i+1]
						if stat >= 0 && stat < 6 && stages > 0 {
							battle.PlayerSacrificeBuffStats[stat] = int8(stages)
						}
					}
				} else {
					// 默认全能力+1级
					for i := 0; i < 6; i++ {
						battle.PlayerSacrificeBuffStats[i] = 1
					}
				}
			case 69: // 药剂反噬：下n回合对手使用体力药剂时效果变成减少相应的体力
				if n > 0 {
					battle.PlayerPotionReverseRounds = byte(n)
				}
			case 71: // 牺牲暴击：自己牺牲(体力降到0), 使下一只出战精灵在前两回合内必定致命一击
				battle.PlayerSacrificeCritActive = true
			case 72: // Miss死亡：如果此回合miss，则立即死亡
				battle.PlayerMissDeathActive = true
			case 73: // 先手反弹：如果先出手，则受攻击时反弹200%的伤害给对手，持续n回合
				if n > 0 {
					battle.PlayerFirstStrikeReflectRounds = byte(n)
					// 检查本回合是否先手：如果玩家先手（!enemyFirst），则激活
					if !enemyFirst {
						battle.PlayerFirstStrikeReflectActive = true
					}
				}
			case 116: // n 回合内每次受到攻击造成伤害的 1/5 恢复自身体力
				if n > 0 {
					battle.PlayerDefendHealRounds = byte(n)
				}
			case 117: // n 回合内每次受到攻击 m% 概率使对手疲惫 1~3 回合
				chance := 30
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					chance = effArgs[1]
				}
				if n > 0 {
					battle.PlayerDefendFatigueRounds = byte(n)
					if chance > 100 {
						chance = 100
					}
					battle.PlayerDefendFatigueChance = byte(chance)
				}
			case 123: // n 回合内受到任何伤害时自身 XX 提高 m 级；SideEffectArg: n stat stages
				rounds, stat, stages := 0, 0, 1
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					stat = effArgs[1]
				}
				if len(effArgs) >= 3 && effArgs[2] > 0 {
					stages = effArgs[2]
				}
				if rounds > 0 && stat >= 0 && stat < 6 {
					battle.PlayerHurtStatBoostRounds = byte(rounds)
					battle.PlayerHurtStatBoostStat = byte(stat)
					battle.PlayerHurtStatBoostStages = int8(stages)
				}
			case 125: // n 回合内被攻击时减少受到的伤害上限 m
				cap := 0
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					cap = effArgs[1]
				}
				if n > 0 && cap > 0 {
					battle.PlayerDamageCapRounds = byte(n)
					battle.PlayerDamageCap = uint32(cap)
				}
			case 126: // n 回合内每回合自身攻击和速度 +m 级
				stages := 1
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					stages = effArgs[1]
				}
				if n > 0 {
					battle.PlayerSpeedBoostRounds = byte(n)
					battle.PlayerSpeedBoostStages = int8(stages)
				}
			case 128: // n 回合内接受的物理伤害转化为体力恢复
				if n > 0 {
					battle.PlayerPhysDmgToHealRounds = byte(n)
				}
			case 471: // 先出手时 n 回合内免疫异常状态
				if n > 0 && !enemyFirst {
					battle.PlayerImmuneStatusRounds = byte(n)
				}
			case 110: // n 回合内每次受到攻击时 m% 几率使对手 stat 等级 -1
				chance, stat := 50, 0
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					chance = effArgs[1]
				}
				if len(effArgs) >= 3 {
					stat = effArgs[2]
				}
				if n > 0 {
					battle.PlayerDefendStatDropRounds = byte(n)
					if chance > 100 {
						chance = 100
					}
					battle.PlayerDefendStatDropChance = byte(chance)
					if stat < 0 || stat > 5 {
						stat = 0
					}
					battle.PlayerDefendStatDropStat = byte(stat)
				}
			case 127: // n% 概率 m 回合内受到伤害减半
				chance127, rounds127 := 50, 3
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					chance127 = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					rounds127 = effArgs[1]
				}
				if chance127 > 100 {
					chance127 = 100
				}
				if rand.Intn(100) < chance127 && rounds127 > 0 {
					battle.PlayerDamageHalfRounds = byte(rounds127)
				}
			case 144: // 消耗全部体力，下一只 n 回合免疫异常（HP 已在 ApplyEffect 归零）
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					battle.PlayerSacrificeImmuneStatusRounds = byte(effArgs[0])
				}
			case 146: // n 回合内受物理攻击时 m% 使对方中毒
				m146 := 50
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					m146 = effArgs[1]
				}
				if m146 > 100 {
					m146 = 100
				}
				if n > 0 {
					battle.PlayerPoisonOnPhysHitRounds = byte(n)
					battle.PlayerPoisonOnPhysHitChance = byte(m146)
				}
			case 150: // n 回合内对手每回合防、特防等级 m
				m150 := 1
				if len(effArgs) >= 2 {
					m150 = effArgs[1]
				}
				if n > 0 {
					battle.EnemyDefSpDefRounds = byte(n)
					if m150 < -6 {
						m150 = -6
					}
					if m150 > 6 {
						m150 = 6
					}
					battle.EnemyDefSpDefStages = int8(m150)
				}
			}
		}

		// 当前回合（包括施放带多回合固伤效果的这一回合），在对方普通伤害结算完之后，
		// 立即结算一跳固定伤害（若之前已有剩余回合，则同样在本次出手后结算）。
		applyFixedDotAfterAttack(&battle.EnemyHP, &battle.EnemyFixedDotDamage, &battle.EnemyFixedDotRounds)
		// 尤纳斯(132) phase 1：固定伤害不能致死，强制锁 1 血；若本回合已由里奥斯幻影击杀则不再恢复 1 血
		if sptboss.IsYouNaSiBoss(battle.EnemyID) && battle.YouNaSiPhase == 1 && battle.EnemyHP == 0 && !youNaSiKilledByPhantom {
			battle.EnemyHP = 1
		}

	// ===== 谱尼七封印 / 真身收尾逻辑 =====
	isPuniBattle := ((battle.BattleMapID == 108 || battle.BattleMapID == 514) && battle.EnemyID == 300 && battle.PuniDoorIndex >= 1 && battle.PuniDoorIndex <= 8)
		door := battle.PuniDoorIndex
		life := battle.PuniTrueFormLifeIndex

		// 能量封印：若本回合累计对谱尼造成伤害超过上限，则当前出战精灵被秒杀，谱尼体力回满（从配置读取）
		// 真身第三条命沿用该规则。
		checkEnergyLimit := false
		var energyLimit uint32 = 100 // 默认值
		if isPuniBattle {
			if door == 8 && life == 3 {
				if cfg, ok := sptboss.GetPuniSealConfig(3); ok && cfg.EnergyDamageLimit > 0 {
					checkEnergyLimit = true
					energyLimit = uint32(cfg.EnergyDamageLimit)
				} else {
					checkEnergyLimit = true // 默认启用
				}
			} else if door == 3 {
				if cfg, ok := sptboss.GetPuniSealConfig(3); ok && cfg.EnergyDamageLimit > 0 {
					checkEnergyLimit = true
					energyLimit = uint32(cfg.EnergyDamageLimit)
				} else {
					checkEnergyLimit = true // 默认启用
				}
			}
		}
		if checkEnergyLimit && battle.PuniEnergyDamageThisTurn > energyLimit {
			// 谱尼回满
			battle.EnemyHP = battle.EnemyMaxHP
			// 当前精灵从 >0 变为 0，计入阵亡
			if battle.PlayerHP > 0 {
				battle.PlayerHP = 0
				battle.DeadPlayerPets++
			}
		}

		// 轮回封印（第五封印）：多管血条——第一管耗尽后自动进入第二管（从配置读取）
		if isPuniBattle && door == 5 && battle.EnemyHP == 0 && battle.PuniCycleHPBar == 1 {
			if cfg, ok := sptboss.GetPuniSealConfig(5); ok && cfg.CycleHPBars >= 2 {
				battle.PuniCycleHPBar = 2
				battle.EnemyHP = battle.EnemyMaxHP
			} else {
				// 配置不存在时使用内置默认值（向后兼容）：2管血
				battle.PuniCycleHPBar = 2
				battle.EnemyHP = battle.EnemyMaxHP
			}
		}

		// 永恒封印（第六封印）：谱尼高防御——在上述所有结算完后，对最终伤害再衰减一次（从配置读取）
		// 真身第五条命沿用该规则。
		checkEternalReduction := false
		if isPuniBattle {
			if door == 8 && life == 5 {
				if cfg, ok := sptboss.GetPuniSealConfig(6); ok {
					checkEternalReduction = cfg.EternalDamageReduction
				} else {
					checkEternalReduction = true // 默认启用
				}
			} else if door == 6 {
				if cfg, ok := sptboss.GetPuniSealConfig(6); ok {
					checkEternalReduction = cfg.EternalDamageReduction
				} else {
					checkEternalReduction = true // 默认启用
				}
			}
		}
		if checkEternalReduction && damage > 0 {
			// 再减半一次，至少保留 1 点伤害
			if damage > 1 {
				damage = damage / 2
				if damage < 1 {
					damage = 1
				}
			}
			if damage > battle.EnemyHP {
				damage = battle.EnemyHP
			}
			battle.EnemyHP -= damage
			if battle.EnemyHP > battle.EnemyMaxHP {
				battle.EnemyHP = 0
			}
			damageCalc = damage
		}

		// 圣洁封印（第七封印）：谱尼免疫能力下降——回滚所有对 EnemyBattleLv 的负向修改（从配置读取）
		// 真身第四、第五条命沿用该规则。
		checkHolyImmune := false
		if isPuniBattle {
			if door == 8 && (life == 4 || life == 5) {
				if cfg, ok := sptboss.GetPuniSealConfig(7); ok {
					checkHolyImmune = cfg.HolyStatDropImmune
				} else {
					checkHolyImmune = true // 默认启用
				}
			} else if door == 7 {
				if cfg, ok := sptboss.GetPuniSealConfig(7); ok {
					checkHolyImmune = cfg.HolyStatDropImmune
				} else {
					checkHolyImmune = true // 默认启用
				}
			}
		}
		if checkHolyImmune {
			for i := 0; i < len(battle.EnemyBattleLv); i++ {
				if battle.EnemyBattleLv[i] < 0 {
					battle.EnemyBattleLv[i] = 0
				}
			}
		}

		// 真身战：六条命 + 每条命对应固定血量；第六条命自动回满（从配置读取）
		if isPuniBattle && door == 8 {
			cfg, hasCfg := sptboss.GetPuniSealConfig(8)
			if battle.PuniTrueFormLifeIndex <= 0 {
				battle.PuniTrueFormLifeIndex = 1
				var firstHP uint32 = 7000
				if hasCfg && len(cfg.TrueFormLives) > 0 {
					firstHP = uint32(cfg.TrueFormLives[0].HP)
				}
				battle.EnemyMaxHP = firstHP
				battle.EnemyHP = firstHP
			}
			// 六条命：前 5 条命归零时自动切换到下一条命并回满，且根据命数调整血量上限。
			if battle.PuniTrueFormLifeIndex < 6 && battle.EnemyHP == 0 {
				battle.PuniTrueFormLifeIndex++
				var nextHP uint32 = 8000
				if hasCfg {
					for _, lifeCfg := range cfg.TrueFormLives {
						if lifeCfg.LifeIndex == battle.PuniTrueFormLifeIndex {
							nextHP = uint32(lifeCfg.HP)
							break
						}
					}
				} else {
					// 配置不存在时使用内置默认值（向后兼容）
					switch battle.PuniTrueFormLifeIndex {
					case 2:
						nextHP = 8000
					case 3:
						nextHP = 9000
					case 4:
						nextHP = 12000
					case 5:
						nextHP = 20000
					case 6:
						nextHP = 65000
					}
				}
				battle.EnemyMaxHP = nextHP
				battle.EnemyHP = nextHP
			} else if battle.PuniTrueFormLifeIndex == 6 && !battle.PuniTrueFormLastLifeHealed && battle.EnemyHP > 0 {
				// 第六条命：当血量低于阈值时自动回满一次（从配置读取）
				var threshold uint32 = 1000
				if hasCfg {
					for _, lifeCfg := range cfg.TrueFormLives {
						if lifeCfg.LifeIndex == 6 && lifeCfg.AutoHealThreshold > 0 {
							threshold = uint32(lifeCfg.AutoHealThreshold)
							break
						}
					}
				}
				if battle.EnemyHP <= threshold {
					battle.EnemyHP = battle.EnemyMaxHP
					battle.PuniTrueFormLastLifeHealed = true
				}
			}
		}
	}
	if fromGaiyaFirst {
		fromGaiyaFirst = false
		goto afterEnemyTurn
	}

runEnemyTurnFirst:
	// 敌人反击（如果敌人还活着）；畏缩/睡眠/麻痹 时本回合无法行动
	// 封装为闭包以便盖亚先手时在“我方出招”前调用一次，非盖亚时在“我方出招”后调用一次
	doEnemyTurn = func() {
		playerHPBeforeEnemy = battle.PlayerHP
		enemyFlinched := false
		// 害怕（status[6]）：本回合无法行动，并递减回合数
		if battle.EnemyStatus[gameskills.StatusIndexFear] > 0 {
			enemyFlinched = true
			battle.EnemyStatus[gameskills.StatusIndexFear]--
		}
		// 敌方睡眠（status[8]）：效果与麻痹一致，本回合无法行动，并递减回合数
		if battle.EnemyStatus[gameskills.StatusIndexSleep] > 0 {
			enemyFlinched = true
			battle.EnemyStatus[gameskills.StatusIndexSleep]--
		}
		// 敌方麻痹（status[0]）：本回合无法行动，并递减回合数
		enemyParalyzed := false
		if battle.EnemyStatus[gameskills.StatusIndexParalysis] > 0 {
			enemyParalyzed = true
			battle.EnemyStatus[gameskills.StatusIndexParalysis]--
		}
		// 敌方石化（status[9]）：本回合无法行动，并递减回合数
		enemyPetrified := false
		if battle.EnemyStatus[gameskills.StatusIndexPetrify] > 0 {
			enemyPetrified = true
			battle.EnemyStatus[gameskills.StatusIndexPetrify]--
		}
		// 敌方混乱（status[10]）：简化为“本回合无法行动”，并递减回合数
		enemyConfused := false
		if battle.EnemyStatus[gameskills.StatusIndexConfusion] > 0 {
			enemyConfused = true
			battle.EnemyStatus[gameskills.StatusIndexConfusion]--
		}

		// 敌人反击（如果敌人还活着且未畏缩、未因麻痹无法行动）
		enemyDamage = 0
		enemyDamageCalc = 0
		enemySkillID = 0
		enemyAttempted = false
		enemyHitFor2505 = false
		if battle.EnemyHP > 0 && !enemyFlinched && !enemyParalyzed && !enemyPetrified && !enemyConfused {
		if enemySkillForTurn != nil && enemySkillIDForTurn != 0 {
			enemySkillID = enemySkillIDForTurn
			enemyAttempted = true

			// 敌方 PP 消耗与耗尽判定（支持 Effect 39 导致本回合无法行动）
			if !battle.EnemyPPInfinite {
				idxPP := -1
				for i := 0; i < 4; i++ {
					if battle.EnemySkillIDs[i] == enemySkillIDForTurn {
						idxPP = i
						break
					}
				}
				if idxPP >= 0 {
					if battle.EnemySkillPP[idxPP] == 0 {
						// 敌方当前选择的技能 PP 已为 0：本回合无法出招
						enemySkillID = 0
						enemyAttempted = false
						enemyHitFor2505 = false
						return
					}
					battle.EnemySkillPP[idxPP]--
				}
			}

			// 魔狮迪露(187) 体力低于一半时：任意技能（属性/攻击）必定秒杀我方当前精灵
			moShiDiLuOneShot := sptboss.IsHalfHPOneShotBoss(battle.EnemyID) && battle.EnemyMaxHP > 0 && battle.EnemyHP*2 < battle.EnemyMaxHP
			if moShiDiLuOneShot {
				enemyDamageCalc = battle.PlayerHP
				battle.PlayerHP = 0
				enemyHitFor2505 = true
			} else {
			// 计算敌人本回合是否命中（考虑命中等级与必中）
			enemyHit := true
			if enemySkillForTurn.MustHit != 1 {
				baseAcc := enemySkillForTurn.Accuracy
				if baseAcc == 0 {
					baseAcc = 100
				}
				accStage := int(battle.EnemyBattleLv[gameskills.StatAccuracy])
				finalAcc := gamebattle.CalcHitChance(baseAcc, accStage, 0)
				// 特性：回避(1025) —— 被技能命中的几率减少5%
				if playerTrait == 1025 {
					finalAcc -= 5
				}
				if finalAcc > 100 {
					finalAcc = 100
				}
				if finalAcc < 1 {
					finalAcc = 1
				}
				if rand.Intn(100) >= finalAcc {
					enemyHit = false
				}
			}
			// 52：本方先手时对方技能 miss（我方有 52 且敌方先手则敌方 miss）
			if enemyHit && enemyFirst && battle.PlayerEvasionRounds > 0 && enemySkillForTurn.MustHit != 1 {
				enemyHit = false
			}
			// 78（我方）：n 回合内物理攻击对自身必定 miss
			if enemyHit && enemySkillForTurn.Category == 1 && battle.PlayerPhysMissRounds > 0 && enemySkillForTurn.MustHit != 1 {
				enemyHit = false
			}
			// 86（我方）：n 回合内属性（特殊）攻击对自身必定 miss
			if enemyHit && enemySkillForTurn.Category == 2 && battle.PlayerSpecialMissRounds > 0 && enemySkillForTurn.MustHit != 1 {
				enemyHit = false
			}
			// 72 - Miss死亡：如果此回合miss，则立即死亡
			if !enemyHit && battle.EnemyMissDeathActive {
				battle.EnemyHP = 0
				battle.EnemyMissDeathActive = false
			}
			enemyHitFor2505 = enemyHit

			// 敌人伤害计算（仅在命中且为攻击技能时）
			enemyPower := uint32(enemySkillForTurn.Power)
			// Category=4 为纯变化/属性技能（红韵、魅惑等），不应造成直接伤害
			if enemySkillForTurn.Category != 4 && enemyPower == 0 {
				enemyPower = 40
			}
			// 1901：潜力越高威力越大（按 DV*5）。PvE 敌方 DV 统一按 15 计算（与 enemyStats 取值一致）。
			if enemySkillForTurn.EffectID == 1901 && enemySkillForTurn.Category != 4 {
				enemyPower = 15 * 5
			}
			// 61：威力随机 50~150
			if enemySkillForTurn.EffectID == 61 && enemySkillForTurn.Category != 4 {
				enemyPower = uint32(rand.Intn(150-50+1) + 50)
			}
			// 70：威力随机 140~220
			if enemySkillForTurn.EffectID == 70 && enemySkillForTurn.Category != 4 {
				enemyPower = uint32(rand.Intn(220-140+1) + 140)
			}
			// 40：先出手时威力为 2 倍（仅当敌方本回合先手）
			if enemySkillForTurn.EffectID == 40 && enemySkillForTurn.Category != 4 && enemyFirst {
				enemyPower *= 2
			}
			// 118：威力随机 140~180
			if enemySkillForTurn.EffectID == 118 && enemySkillForTurn.Category != 4 {
				enemyPower = uint32(rand.Intn(180-140+1) + 140)
			}
			// 65：敌方某属性技能威力 m 倍
			if battle.EnemyElemPowerRounds > 0 && enemySkillForTurn.Type == int(battle.EnemyElemPowerType) {
				enemyPower *= uint32(battle.EnemyElemPowerMult)
			}
			// 敌方攻防（按技能类别），并应用强化弱化倍率
			enemyAtk := float64(enemyStats.Attack)
			enemyDef := float64(playerStats.Defence)
			enemyAtkStage, enemyDefStage := 0, 1 // 物理：攻击/防御
			if enemySkillForTurn.Category == 2 {
				enemyAtk = float64(enemyStats.SpAtk)
				enemyDef = float64(playerStats.SpDef)
				enemyAtkStage, enemyDefStage = 2, 3 // 特殊：特攻/特防
			}
			// 51：敌方攻击力与己方相同
			if battle.EnemyCopyAtkRounds > 0 {
				if enemySkillForTurn.Category == 2 {
					enemyAtk = float64(playerStats.SpAtk)
				} else {
					enemyAtk = float64(playerStats.Attack)
				}
			}
			// 45：我方防御力与对手相同（即我方防御=敌方防御）
			if battle.PlayerCopyDefRounds > 0 {
				if enemySkillForTurn.Category == 2 {
					enemyDef = float64(enemyStats.SpDef)
				} else {
					enemyDef = float64(enemyStats.Defence)
				}
			}
			enemyAtk *= gamebattle.GetStatMultiplier(int(battle.EnemyBattleLv[enemyAtkStage]))
			enemyDef *= gamebattle.GetStatMultiplier(int(battle.PlayerBattleLv[enemyDefStage]))
			if enemyDef < 1 {
				enemyDef = 1
			}
			enemyBaseDamage := 0.0
			if enemySkillForTurn.Category != 4 && enemyPower > 0 {
				enemyBaseDamage = math.Floor(((float64(battle.EnemyLevel)*0.4 + 2.0) * float64(enemyPower) * enemyAtk / enemyDef / 50.0) + 2.0)
			}
			enemyFinalDamage := uint32(0)
			isCritEnemy := false
			if enemyHit && enemySkillForTurn.Category != 4 && enemyBaseDamage > 0 {
				enemyStab := 1.0
				if enemyPet != nil && (enemySkillForTurn.Type == enemyPet.Type || (enemyPet.Type2 > 0 && enemySkillForTurn.Type == enemyPet.Type2)) {
					enemyStab = 1.5
				}
				enemyTypeMod := 1.0
				if attackerPet != nil {
					if battle.PlayerTypeSwapRounds > 0 {
						// 55：属性反转（我方有 55，敌方攻击我方时）
						enemyTypeMod = gamebattle.GetTypeMultiplierDual(attackerPet.Type, attackerPet.Type2, enemySkillForTurn.Type)
					} else if battle.PlayerTypeCopyRounds > 0 {
						// 56：属性相同
						enemyTypeMod = 1.0
					} else {
						enemyTypeMod = gamebattle.GetTypeMultiplierDual(enemySkillForTurn.Type, attackerPet.Type, attackerPet.Type2)
					}
				}
				enemyRandomMod := float64(rand.Intn(255-217+1)+217) / 255.0
				// 敌人暴击：1/16 基础概率
				enemyCritRate := enemySkillForTurn.CritRate
				if enemyCritRate == 0 {
					enemyCritRate = 1
				}
				// 若敌方处于“下 N 回合必定暴击”效果中，则直接视为 100% 暴击率
				if battle.EnemyCritBuffRounds > 0 {
					enemyCritRate = 16
				}
				// 32（敌方）- n 回合暴击率增加 1/16
				if battle.EnemyCritRateBonusRounds > 0 && enemyCritRate < 16 {
					enemyCritRate++
				}
				// 441（敌方）- 每次攻击暴击率 +n%（累积，最高 m%）
				if battle.EnemyCritRateBonus > 0 {
					enemyCritRate += int(battle.EnemyCritRateBonus)
					if enemyCritRate > 16 {
						enemyCritRate = 16
					}
				}
				// 83（敌方雌性）- 下两回合必定暴击
				if battle.EnemyFemaleCritRounds > 0 {
					enemyCritRate = 16
				}
				// 95（敌方）- 对手处于睡眠状态时致命一击率提升 n/16
				if enemySkillForTurn.EffectID == 95 && battle.PlayerStatus[gameskills.StatusIndexSleep] > 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					bonus := 4
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						bonus = effArgs[0]
					}
					enemyCritRate += bonus
					if enemyCritRate > 16 {
						enemyCritRate = 16
					}
				}
				enemyCritMod := 1.0
				if rand.Intn(16) < enemyCritRate {
					// 暴击：伤害为正常攻击伤害的两倍
					isCritEnemy = true
					enemyCritMod = 2.0
					// 敌方暴击时，同样清除我方防御/特防的强化状态
					if enemySkillForTurn.Category == 1 {
						// 物理暴击：清空我方防御提升
						if battle.PlayerBattleLv[gameskills.StatDefence] > 0 {
							battle.PlayerBattleLv[gameskills.StatDefence] = 0
						}
					} else if enemySkillForTurn.Category == 2 {
						// 特殊暴击：清空我方特防提升
						if battle.PlayerBattleLv[gameskills.StatSpDef] > 0 {
							battle.PlayerBattleLv[gameskills.StatSpDef] = 0
						}
					}
				}
				enemyFinalDamage = uint32(enemyBaseDamage * enemyStab * enemyTypeMod * enemyRandomMod * enemyCritMod)
				// 53：敌方攻击伤害 m 倍
				if battle.EnemyDamageMultRounds > 0 && battle.EnemyDamageMult > 0 {
					enemyFinalDamage *= uint32(battle.EnemyDamageMult)
				}
				// 盖亚(261) 伤害翻倍，能力提升仍与其他 BOSS 一致（仅攻击+2）
				if battle.EnemyID == petIDGaiya && enemyFinalDamage > 0 {
					enemyFinalDamage *= 2
				}
			}
			// effect 88：n% 几率伤害为 m 倍
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 88 {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				chance, mult := 10, 2
				if len(effArgs) >= 1 {
					chance = effArgs[0]
				}
				if len(effArgs) >= 2 {
					mult = effArgs[1]
				}
				if chance < 0 {
					chance = 0
				}
				if mult < 1 {
					mult = 1
				}
				if rand.Intn(100) < chance {
					mul := uint64(enemyFinalDamage) * uint64(mult)
					if mul > math.MaxUint32 {
						enemyFinalDamage = math.MaxUint32
					} else {
						enemyFinalDamage = uint32(mul)
					}
				}
			}
			// 敌人多段攻击（effect 31），同样折算为单次伤害倍数
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 31 {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				minHits, maxHits := 2, 5
				if len(effArgs) >= 1 {
					minHits = effArgs[0]
				}
				if len(effArgs) >= 2 {
					maxHits = effArgs[1]
				}
				if minHits < 1 {
					minHits = 1
				}
				if maxHits < minHits {
					maxHits = minHits
				}
				hits := rand.Intn(maxHits-minHits+1) + minHits
				if hits > 1 {
					mul := uint64(enemyFinalDamage) * uint64(hits)
					if mul > math.MaxUint32 {
						enemyFinalDamage = math.MaxUint32
					} else {
						enemyFinalDamage = uint32(mul)
					}
				}
			}
			// effect 64：自身在烧伤/冻伤/中毒状态下造成的伤害加倍（并视为覆盖烧伤减伤）
			enemyHasAilment := battle.EnemyStatus[gameskills.StatusIndexPoison] > 0 ||
				battle.EnemyStatus[gameskills.StatusIndexBurn] > 0 ||
				battle.EnemyStatus[gameskills.StatusIndexFreeze] > 0
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 64 && enemyHasAilment {
				mul := uint64(enemyFinalDamage) * 2
				if mul > math.MaxUint32 {
					enemyFinalDamage = math.MaxUint32
				} else {
					enemyFinalDamage = uint32(mul)
				}
			} else if enemyFinalDamage > 0 && battle.EnemyStatus[gameskills.StatusIndexBurn] > 0 {
				// 烧伤效果：被烧伤方（敌人）造成的伤害减半
				enemyFinalDamage = enemyFinalDamage / 2
				if enemyFinalDamage < 1 {
					enemyFinalDamage = 1
				}
			}
			// effect 82（敌方）：目标为雄性伤害 200%，雌性 50%
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 82 {
				targetPet := petMgr.Get(playerPetID)
				if targetPet != nil {
					if targetPet.Gender == 1 {
						enemyFinalDamage *= 2
						if enemyFinalDamage > battle.PlayerHP {
							enemyFinalDamage = battle.PlayerHP
						}
					} else if targetPet.Gender == 2 {
						enemyFinalDamage /= 2
						if enemyFinalDamage < 1 {
							enemyFinalDamage = 1
						}
					}
				}
			}
			// 96（敌方）- 对手处于烧伤状态时威力翻倍
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 96 && battle.PlayerStatus[gameskills.StatusIndexBurn] > 0 {
				mul := uint64(enemyFinalDamage) * 2
				if mul > math.MaxUint32 {
					enemyFinalDamage = math.MaxUint32
				} else {
					enemyFinalDamage = uint32(mul)
				}
				if enemyFinalDamage > battle.PlayerHP {
					enemyFinalDamage = battle.PlayerHP
				}
			}
			// 97（敌方）- 对手处于冻伤状态时威力翻倍
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 97 && battle.PlayerStatus[gameskills.StatusIndexFreeze] > 0 {
				mul := uint64(enemyFinalDamage) * 2
				if mul > math.MaxUint32 {
					enemyFinalDamage = math.MaxUint32
				} else {
					enemyFinalDamage = uint32(mul)
				}
				if enemyFinalDamage > battle.PlayerHP {
					enemyFinalDamage = battle.PlayerHP
				}
			}
			// 102（敌方）- 对手处于麻痹状态时威力翻倍
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 102 && battle.PlayerStatus[gameskills.StatusIndexParalysis] > 0 {
				mul := uint64(enemyFinalDamage) * 2
				if mul > math.MaxUint32 {
					enemyFinalDamage = math.MaxUint32
				} else {
					enemyFinalDamage = uint32(mul)
				}
				if enemyFinalDamage > battle.PlayerHP {
					enemyFinalDamage = battle.PlayerHP
				}
			}
			// 98（敌方）- n 回合内对雄性精灵的伤害为 m 倍
			if enemyFinalDamage > 0 && battle.EnemyMaleDamageMultRounds > 0 {
				targetPet := petMgr.Get(playerPetID)
				if targetPet != nil && targetPet.Gender == 1 {
					mult := battle.EnemyMaleDamageMult
					if mult < 1 {
						mult = 1
					}
					mul := uint64(enemyFinalDamage) * uint64(mult)
					if mul > math.MaxUint32 {
						enemyFinalDamage = math.MaxUint32
					} else {
						enemyFinalDamage = uint32(mul)
					}
					if enemyFinalDamage > battle.PlayerHP {
						enemyFinalDamage = battle.PlayerHP
					}
				}
			}
			// 100（敌方）- 自身体力越少则威力越大
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 100 && battle.EnemyMaxHP > 0 {
				ratio := 2.0 - float64(battle.EnemyHP)/float64(battle.EnemyMaxHP)
				if ratio < 1.0 {
					ratio = 1.0
				}
				if ratio > 2.0 {
					ratio = 2.0
				}
				mul := uint64(float64(enemyFinalDamage) * ratio)
				if mul > math.MaxUint32 {
					enemyFinalDamage = math.MaxUint32
				} else {
					enemyFinalDamage = uint32(mul)
				}
				if enemyFinalDamage > battle.PlayerHP {
					enemyFinalDamage = battle.PlayerHP
				}
			}
			if enemyFinalDamage < 1 {
				enemyFinalDamage = 0
			}
			// 179（敌方）- 若属性相同则技能威力提升 n
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 179 {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				boost := 20
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					boost = effArgs[0]
				}
				enemyPet := petMgr.Get(battle.EnemyID)
				targetPet := petMgr.Get(playerPetID)
				if enemyPet != nil && targetPet != nil && enemyPet.Type == targetPet.Type {
					enemyFinalDamage = enemyFinalDamage * uint32(100+boost) / 100
				}
			}
			// 129（敌方）- 对方为 X 性则技能威力翻倍
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 129 {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				gender := 2
				if len(effArgs) >= 1 {
					gender = effArgs[0]
				}
				targetPet := petMgr.Get(playerPetID)
				if targetPet != nil && targetPet.Gender == gender {
					mul := uint64(enemyFinalDamage) * 2
					if mul > math.MaxUint32 {
						enemyFinalDamage = math.MaxUint32
					} else {
						enemyFinalDamage = uint32(mul)
					}
				}
			}
			// 130（敌方）- 对方为 X 性则附加 n 点伤害
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 130 {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				gender, bonus := 1, 100
				if len(effArgs) >= 1 {
					gender = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					bonus = effArgs[1]
				}
				targetPet := petMgr.Get(playerPetID)
				if targetPet != nil && targetPet.Gender == gender {
					enemyFinalDamage += uint32(bonus)
				}
			}
			// 131（敌方）- 对方为 X 性则免疫当前回合伤害
			if enemySkillForTurn.EffectID == 131 {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				gender := 1
				if len(effArgs) >= 1 {
					gender = effArgs[0]
				}
				targetPet := petMgr.Get(playerPetID)
				if targetPet != nil && targetPet.Gender == gender {
					enemyFinalDamage = 0
					enemyDamageCalc = 0
				}
			}
			// 135/447（敌方）- 造成的伤害不会低于 n
			if (enemySkillForTurn.EffectID == 135 || enemySkillForTurn.EffectID == 447) && enemyFinalDamage > 0 {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				floor := 80
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					floor = effArgs[0]
				}
				if enemyFinalDamage < uint32(floor) {
					enemyFinalDamage = uint32(floor)
				}
			}
			// 193（敌方）- 若对手处于 XX 状态则必定致命一击
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 193 && !isCritEnemy {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				statusIdx := 5
				if len(effArgs) >= 1 {
					statusIdx = effArgs[0]
				}
				if statusIdx >= 0 && statusIdx < 20 && battle.PlayerStatus[statusIdx] > 0 {
					isCritEnemy = true
					mul := uint64(enemyFinalDamage) * 2
					if mul > math.MaxUint32 {
						enemyFinalDamage = math.MaxUint32
					} else {
						enemyFinalDamage = uint32(mul)
					}
				}
			}
			// 468（敌方）- 若自身处于能力下降状态则威力翻倍，同时解除能力下降状态
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 468 {
				hasStatDrop := false
				for i := 0; i < 6; i++ {
					if battle.EnemyBattleLv[i] < 0 {
						hasStatDrop = true
						break
					}
				}
				if hasStatDrop {
					mul := uint64(enemyFinalDamage) * 2
					if mul > math.MaxUint32 {
						enemyFinalDamage = math.MaxUint32
					} else {
						enemyFinalDamage = uint32(mul)
					}
					for i := 0; i < 6; i++ {
						if battle.EnemyBattleLv[i] < 0 {
							battle.EnemyBattleLv[i] = 0
						}
					}
				}
			}
			// 195（敌方）- 若对手处于异常状态则攻击力双倍
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 195 {
				hasStatus := false
				for i := 0; i < 20; i++ {
					if battle.PlayerStatus[i] > 0 {
						hasStatus = true
						break
					}
				}
				if hasStatus {
					mul := uint64(enemyFinalDamage) * 2
					if mul > math.MaxUint32 {
						enemyFinalDamage = math.MaxUint32
					} else {
						enemyFinalDamage = uint32(mul)
					}
				}
			}
			// 180（敌方）- 只在第一回合有效果
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 180 && battle.RoundCount > 1 {
				enemyFinalDamage /= 2
				if enemyFinalDamage < 1 {
					enemyFinalDamage = 1
				}
			}
			// 88（敌方）- n% 概率伤害为 m 倍
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 88 {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				chance, mult := 10, 2
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					chance = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					mult = effArgs[1]
				}
				if rand.Intn(100) < chance {
					mul := uint64(enemyFinalDamage) * uint64(mult)
					if mul > math.MaxUint32 {
						enemyFinalDamage = math.MaxUint32
					} else {
						enemyFinalDamage = uint32(mul)
					}
				}
			}
			// 35（敌方）- 对方能力等级越高伤害越大
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 35 {
				totalBoost := 0
				for i := 0; i < 6; i++ {
					if battle.PlayerBattleLv[i] > 0 {
						totalBoost += int(battle.PlayerBattleLv[i])
					}
				}
				if totalBoost > 0 {
					mul := uint64(enemyFinalDamage) * uint64(100+totalBoost*5) / 100
					if mul > math.MaxUint32 {
						enemyFinalDamage = math.MaxUint32
					} else {
						enemyFinalDamage = uint32(mul)
					}
				}
			}
			// 111（敌方）- 攻击力越高附加伤害越大
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 111 {
				enemyPet := petMgr.Get(battle.EnemyID)
				targetPet := petMgr.Get(playerPetID)
				if enemyPet != nil && targetPet != nil && enemyPet.Atk > targetPet.Atk {
					bonus := uint32(enemyPet.Atk-targetPet.Atk) / 10
					enemyFinalDamage += bonus
				}
			}
			// 113（敌方）- 速度越高威力越大
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 113 {
				enemyPet := petMgr.Get(battle.EnemyID)
				targetPet := petMgr.Get(playerPetID)
				if enemyPet != nil && targetPet != nil && enemyPet.Spd > targetPet.Spd {
					bonus := uint32(enemyPet.Spd-targetPet.Spd) / 10
					enemyFinalDamage += bonus
				}
			}
			// 132（敌方）- 当前体力在对方体力以上时威力翻倍
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 132 && battle.EnemyHP >= battle.PlayerHP {
				mul := uint64(enemyFinalDamage) * 2
				if mul > math.MaxUint32 {
					enemyFinalDamage = math.MaxUint32
				} else {
					enemyFinalDamage = uint32(mul)
				}
			}
			// 168（敌方）- 若自身处于睡眠状态则威力翻倍
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 168 && battle.EnemyStatus[gameskills.StatusIndexSleep] > 0 {
				mul := uint64(enemyFinalDamage) * 2
				if mul > math.MaxUint32 {
					enemyFinalDamage = math.MaxUint32
				} else {
					enemyFinalDamage = uint32(mul)
				}
			}
			// 429（敌方）- 固定伤递增
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 429 {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				base, increment, maxDmg := 25, 25, 100
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					base = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					increment = effArgs[1]
				}
				if len(effArgs) >= 3 && effArgs[2] > 0 {
					maxDmg = effArgs[2]
				}
				current := uint32(base) + battle.EnemyFixedDmgIncrement
				if current > uint32(maxDmg) {
					current = uint32(maxDmg)
				}
				if current > battle.PlayerHP {
					current = battle.PlayerHP
				}
				enemyFinalDamage += current
				battle.EnemyFixedDmgIncrement += uint32(increment)
				if battle.EnemyFixedDmgIncrement > uint32(maxDmg-base) {
					battle.EnemyFixedDmgIncrement = uint32(maxDmg - base)
				}
			}
			// 431（敌方）- 若自身处于能力下降状态则威力翻倍
			if enemyFinalDamage > 0 && enemySkillForTurn.EffectID == 431 {
				hasStatDrop := false
				for i := 0; i < 6; i++ {
					if battle.EnemyBattleLv[i] < 0 {
						hasStatDrop = true
						break
					}
				}
				if hasStatDrop {
					mul := uint64(enemyFinalDamage) * 2
					if mul > math.MaxUint32 {
						enemyFinalDamage = math.MaxUint32
					} else {
						enemyFinalDamage = uint32(mul)
					}
				}
			}
			// 402（敌方）- 后出手时额外附加 n 点固定伤害
			if enemyHit && enemySkillForTurn.EffectID == 402 && !enemyFirst && enemyFinalDamage > 0 {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				bonus := uint32(50)
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					bonus = uint32(effArgs[0])
				}
				enemyFinalDamage += bonus
				if enemyFinalDamage > battle.PlayerHP {
					enemyFinalDamage = battle.PlayerHP
				}
			}
			// 405（敌方）- 先出手时额外附加 n 点固定伤害
			if enemyHit && enemySkillForTurn.EffectID == 405 && enemyFirst && enemyFinalDamage > 0 {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				bonus := uint32(50)
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					bonus = uint32(effArgs[0])
				}
				enemyFinalDamage += bonus
				if enemyFinalDamage > battle.PlayerHP {
					enemyFinalDamage = battle.PlayerHP
				}
			}
			// 理论伤害用于客户端显示（对方打我方时显示实际受到的伤害数字，而非仅剩余血量）
			enemyDamageCalc = enemyFinalDamage
			// 42：敌方电系技能伤害×2
			if enemyHit && battle.EnemyElectricBoostRounds > 0 && enemySkillForTurn.Type == 5 && enemyFinalDamage > 0 {
				enemyFinalDamage *= 2
				if enemyFinalDamage > battle.PlayerHP {
					enemyFinalDamage = battle.PlayerHP
				}
			}
			if enemyHit && enemyFinalDamage > 0 {
				// 我方防御侧：46 挡一次 54 伤害1/m 41 火抗 44 特防减半 50 物防减半 49 护盾
				if battle.PlayerBlockCount > 0 {
					battle.PlayerBlockCount--
					enemyFinalDamage = 0
				} else {
					// 54：对方打我方伤害 1/m
					if battle.PlayerDamageReductRounds > 0 && battle.PlayerDamageReduct > 0 {
						enemyFinalDamage /= uint32(battle.PlayerDamageReduct)
						if enemyFinalDamage < 1 {
							enemyFinalDamage = 1
						}
					}
					if battle.PlayerFireResistRounds > 0 && enemySkillForTurn.Type == 3 {
						enemyFinalDamage /= 2
						if enemyFinalDamage < 1 {
							enemyFinalDamage = 1
						}
					}
					if battle.PlayerSpDefHalfRounds > 0 && enemySkillForTurn.Category == 2 {
						enemyFinalDamage /= 2
						if enemyFinalDamage < 1 {
							enemyFinalDamage = 1
						}
					}
					if battle.PlayerPhysDefHalfRounds > 0 && enemySkillForTurn.Category == 1 {
						enemyFinalDamage /= 2
						if enemyFinalDamage < 1 {
							enemyFinalDamage = 1
						}
					}
					if battle.PlayerShieldPoints > 0 {
						if enemyFinalDamage <= battle.PlayerShieldPoints {
							battle.PlayerShieldPoints -= enemyFinalDamage
							enemyFinalDamage = 0
						} else {
							enemyFinalDamage -= battle.PlayerShieldPoints
							battle.PlayerShieldPoints = 0
						}
					}
					if enemyFinalDamage > battle.PlayerHP {
						enemyFinalDamage = battle.PlayerHP
					}
				}
			}
			// 68：致死留 1 血（仅一次）
			if enemyHit && enemyFinalDamage > 0 && battle.PlayerEndureRounds > 0 && enemyFinalDamage >= battle.PlayerHP && battle.PlayerHP > 0 {
				enemyFinalDamage = battle.PlayerHP - 1
				battle.PlayerEndureRounds--
			}
			if enemyHit && enemyFinalDamage > 0 {
				// 特性：坚硬(1024) —— 受到的伤害减少5%
				if playerTrait == 1024 {
					reduced := uint32(math.Floor(float64(enemyFinalDamage) * 0.95))
					if reduced < 1 {
						reduced = 1
					}
					enemyFinalDamage = reduced
				}
				// 顽强(1026)：受到致死攻击时有3%几率余下1点体力
				if playerTrait == 1026 && playerHPBeforeEnemy > 1 && enemyFinalDamage >= playerHPBeforeEnemy {
					if rand.Intn(100) < 3 {
						enemyFinalDamage = playerHPBeforeEnemy - 1
					}
				}
				if enemyFinalDamage > battle.PlayerHP {
					enemyFinalDamage = battle.PlayerHP
				}
				enemyDamage = enemyFinalDamage
				// 73 - 先手反弹：如果先出手，则受攻击时反弹200%的伤害给对手
				if battle.PlayerFirstStrikeReflectActive && battle.PlayerFirstStrikeReflectRounds > 0 && enemyDamage > 0 {
					reflectDamage := enemyDamage * 2
					if reflectDamage > battle.EnemyHP {
						reflectDamage = battle.EnemyHP
					}
					battle.EnemyHP -= reflectDamage
				}
			playerHPBeforeDamage := battle.PlayerHP
			// 127 - n 回合内受到伤害减半
			if battle.PlayerDamageHalfRounds > 0 && enemyDamage > 0 {
				enemyDamage /= 2
			}
			// 508 - 下回合所受伤害减少 m 点（生效一次后清零）
			if battle.PlayerNextTurnDamageReduce > 0 {
				if enemyDamage > battle.PlayerNextTurnDamageReduce {
					enemyDamage -= battle.PlayerNextTurnDamageReduce
				} else {
					enemyDamage = 0
				}
				battle.PlayerNextTurnDamageReduce = 0
			}
			// 463 - n 回合内每回合所受的伤害减少 m 点
			if battle.PlayerDamageReducePerRoundRounds > 0 && battle.PlayerDamageReducePerRoundAmount > 0 && enemyDamage > 0 {
				if enemyDamage > battle.PlayerDamageReducePerRoundAmount {
					enemyDamage -= battle.PlayerDamageReducePerRoundAmount
				} else {
					enemyDamage = 0
				}
			}
			// 125 - n 回合内被攻击时减少受到的伤害上限 m
			if battle.PlayerDamageCapRounds > 0 && battle.PlayerDamageCap > 0 && enemyDamage > battle.PlayerDamageCap {
				enemyDamage = battle.PlayerDamageCap
			}
			// 128 - n 回合内接受的物理伤害转化为体力恢复
			if battle.PlayerPhysDmgToHealRounds > 0 && enemySkillForTurn != nil && enemySkillForTurn.Category == 1 && enemyDamage > 0 {
				heal := enemyDamage
				newHP := battle.PlayerHP + heal
				if newHP > battle.PlayerMaxHP {
					newHP = battle.PlayerMaxHP
				}
				battle.PlayerHP = newHP
				enemyDamage = 0
			}
			// 123 - n 回合内受到任何伤害时自身 XX 提高 m 级
			if battle.PlayerHurtStatBoostRounds > 0 && enemyDamage > 0 {
				stat := int(battle.PlayerHurtStatBoostStat)
				if stat >= 0 && stat < 6 {
					cur := int(battle.PlayerBattleLv[stat]) + int(battle.PlayerHurtStatBoostStages)
					if cur > 6 {
						cur = 6
					}
					if cur < -6 {
						cur = -6
					}
					battle.PlayerBattleLv[stat] = int8(cur)
				}
			}
				// 84 - 受到物理攻击时 m% 几率将对手麻痹
				if battle.PlayerParalyzeOnPhysHitRounds > 0 && enemySkillForTurn.Category == 1 && enemyDamage > 0 &&
					!sptboss.IsControlImmune(battle.EnemyID) && battle.EnemyStatus[gameskills.StatusIndexParalysis] == 0 {
					if rand.Intn(100) < int(battle.PlayerParalyzeOnPhysHitChance) {
						battle.EnemyStatus[gameskills.StatusIndexParalysis] = byte(rand.Intn(2) + 2)
					}
				}
				// 92 - 受到物理攻击时 m% 几率将对手冻伤
				if battle.PlayerFreezeOnPhysHitRounds > 0 && enemySkillForTurn.Category == 1 && enemyDamage > 0 &&
					!sptboss.IsControlImmune(battle.EnemyID) && battle.EnemyStatus[gameskills.StatusIndexFreeze] == 0 {
					if rand.Intn(100) < int(battle.PlayerFreezeOnPhysHitChance) {
						battle.EnemyStatus[gameskills.StatusIndexFreeze] = byte(rand.Intn(2) + 1)
						dmg := battle.EnemyMaxHP / 8
						if dmg > battle.EnemyHP {
							dmg = battle.EnemyHP
						}
						battle.EnemyHP -= dmg
					}
				}
				// 108 - 受到物理攻击时 m% 几率将对手烧伤
				if battle.PlayerBurnOnPhysHitRounds > 0 && enemySkillForTurn.Category == 1 && enemyDamage > 0 &&
					!sptboss.IsStatusImmune(battle.EnemyID) && battle.EnemyStatus[gameskills.StatusIndexBurn] == 0 {
					if rand.Intn(100) < int(battle.PlayerBurnOnPhysHitChance) {
						battle.EnemyStatus[gameskills.StatusIndexBurn] = byte(rand.Intn(2) + 1)
						dmg := battle.EnemyMaxHP / 8
						if dmg > battle.EnemyHP {
							dmg = battle.EnemyHP
						}
						battle.EnemyHP -= dmg
					}
				}
				// 146 - n 回合内受物理攻击时 m% 使对方中毒
				if battle.PlayerPoisonOnPhysHitRounds > 0 && enemySkillForTurn.Category == 1 && enemyDamage > 0 &&
					!sptboss.IsStatusImmune(battle.EnemyID) && battle.EnemyStatus[gameskills.StatusIndexPoison] == 0 {
					if rand.Intn(100) < int(battle.PlayerPoisonOnPhysHitChance) {
						battle.EnemyStatus[gameskills.StatusIndexPoison] = byte(rand.Intn(2) + 1)
					}
				}
				// 21 - 反弹伤害 1/k：受到攻击时对对手造成本次受到伤害的 1/divisor
				if battle.PlayerReflectDamageRounds > 0 && battle.PlayerReflectDamageDivisor > 0 && enemyDamage > 0 {
					reflectDmg := enemyDamage / uint32(battle.PlayerReflectDamageDivisor)
					if reflectDmg > battle.EnemyHP {
						reflectDmg = battle.EnemyHP
					}
					battle.EnemyHP -= reflectDmg
				}
				// 545 - 若受到伤害高于 m 则对手获得效果 type（如刺刃甲壳：type 1 = 防御-1）
				if battle.PlayerReflectStatusWhenHitRounds > 0 && enemyDamage >= battle.PlayerReflectStatusWhenHitThreshold && enemyDamage > 0 && !sptboss.IsStatDropImmune(battle.EnemyID) {
					typ := battle.PlayerReflectStatusWhenHitType
					if typ <= 5 {
						// type 0~5：对手对应能力等级 -1（0攻 1防 2特攻 3特防 4速 5命中）
						cur := int(battle.EnemyBattleLv[typ])
						cur--
						if cur < -6 {
							cur = -6
						}
						battle.EnemyBattleLv[typ] = int8(cur)
					}
					// type >= 10 可扩展为异常状态，此处仅实现能力下降
				}
				battle.PlayerHP -= enemyDamage
				// 116 - n 回合内每次受到攻击造成伤害的 1/5 恢复自身体力
				if battle.PlayerDefendHealRounds > 0 && enemyDamage > 0 {
					heal := enemyDamage / 5
					if heal > 0 {
						newHP := battle.PlayerHP + heal
						if newHP > battle.PlayerMaxHP {
							newHP = battle.PlayerMaxHP
						}
						battle.PlayerHP = newHP
					}
				}
				// 117 - n 回合内每次受到攻击 m% 概率使对手疲惫 1~3 回合
				if battle.PlayerDefendFatigueRounds > 0 && enemyDamage > 0 &&
					!sptboss.IsControlImmune(battle.EnemyID) && battle.EnemyStatus[gameskills.StatusIndexFatigue] == 0 {
					if rand.Intn(100) < int(battle.PlayerDefendFatigueChance) {
						battle.EnemyStatus[gameskills.StatusIndexFatigue] = byte(rand.Intn(3) + 1)
					}
				}
				// 110 - n 回合内每次受到攻击时 m% 几率使对手 stat 等级 -1
				if battle.PlayerDefendStatDropRounds > 0 && enemyDamage > 0 &&
					!sptboss.IsStatDropImmune(battle.EnemyID) && battle.EnemyImmuneStatDropRounds == 0 {
					if rand.Intn(100) < int(battle.PlayerDefendStatDropChance) {
						stat := int(battle.PlayerDefendStatDropStat)
						cur := int(battle.EnemyBattleLv[stat]) - 1
						if cur < -6 {
							cur = -6
						}
						battle.EnemyBattleLv[stat] = int8(cur)
					}
				}
				// 172（敌方）- 若自身后出手则造成伤害的 1/n 回复体力
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 172 && !enemyFirst && enemyDamage > 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					div := 2
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						div = effArgs[0]
					}
					heal := enemyDamage / uint32(div)
					if heal > 0 {
						newHP := battle.EnemyHP + heal
						if newHP > battle.EnemyMaxHP {
							newHP = battle.EnemyMaxHP
						}
						battle.EnemyHP = newHP
					}
				}
				// 458（敌方）- 若自身先出手则造成伤害的 n% 回复体力
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 458 && enemyFirst && enemyDamage > 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					pct := 50
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						pct = effArgs[0]
					}
					heal := enemyDamage * uint32(pct) / 100
					if heal > 0 {
						newHP := battle.EnemyHP + heal
						if newHP > battle.EnemyMaxHP {
							newHP = battle.EnemyMaxHP
						}
						battle.EnemyHP = newHP
					}
				}
				// 459（敌方）- 附加对手防御/特防 n% 的固定伤害
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 459 && enemyDamage > 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					pct := 50
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						pct = effArgs[0]
					}
					var defVal uint32
					if enemySkillForTurn.Category == 1 {
						defVal = uint32(playerStats.Defence)
					} else {
						defVal = uint32(playerStats.SpDef)
					}
					bonus := defVal * uint32(pct) / 100
					if bonus > 0 && battle.PlayerHP > 0 {
						if bonus > battle.PlayerHP {
							bonus = battle.PlayerHP
						}
						battle.PlayerHP -= bonus
					}
				}
				// 461（敌方）- 若自身体力低于 1/m 则下回合起必定暴击
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 461 && enemyDamage > 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					div := 4
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						div = effArgs[0]
					}
					if battle.EnemyMaxHP > 0 && battle.EnemyHP > 0 && battle.EnemyHP < battle.EnemyMaxHP/uint32(div) {
						battle.EnemyCritBuffRounds = 3
					}
				}
				// 474（敌方）- 若自身先出手则 n% 几率自身 stat 提升 m 级
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 474 && enemyFirst && enemyDamage > 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					chance, statIdx, stages := 50, 0, 1
					if len(effArgs) >= 1 {
						chance = effArgs[0]
					}
					if len(effArgs) >= 2 {
						statIdx = effArgs[1]
					}
					if len(effArgs) >= 3 && effArgs[2] > 0 {
						stages = effArgs[2]
					}
					if statIdx >= 0 && statIdx < 6 && rand.Intn(100) < chance {
						cur := int(battle.EnemyBattleLv[statIdx])
						cur += stages
						if cur > 6 {
							cur = 6
						}
						battle.EnemyBattleLv[statIdx] = int8(cur)
					}
				}
				// 475（敌方）- 若伤害不足 m 则下 n 回合必定暴击
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 475 && enemyDamage > 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					threshold, rounds := 100, 2
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						threshold = effArgs[0]
					}
					if len(effArgs) >= 2 && effArgs[1] > 0 {
						rounds = effArgs[1]
					}
					if enemyDamage < uint32(threshold) {
						battle.EnemyCritBuffRounds = byte(rounds + 1)
					}
				}
				// 476（敌方）- 若自身后出手则回复 n 点体力
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 476 && !enemyFirst && enemyDamage > 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					amount := uint32(100)
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						amount = uint32(effArgs[0])
					}
					newHP := battle.EnemyHP + amount
					if newHP > battle.EnemyMaxHP {
						newHP = battle.EnemyMaxHP
					}
					battle.EnemyHP = newHP
				}
				// 186（敌方）- 若自身后出手则 n% 几率自身 stat 提升 m 级
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 186 && !enemyFirst && enemyDamage > 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					chance, statIdx, stages := 50, 0, 1
					if len(effArgs) >= 1 {
						chance = effArgs[0]
					}
					if len(effArgs) >= 2 {
						statIdx = effArgs[1]
					}
					if len(effArgs) >= 3 && effArgs[2] > 0 {
						stages = effArgs[2]
					}
					if statIdx >= 0 && statIdx < 6 && rand.Intn(100) < chance {
						cur := int(battle.EnemyBattleLv[statIdx])
						cur += stages
						if cur > 6 {
							cur = 6
						}
						battle.EnemyBattleLv[statIdx] = int8(cur)
					}
				}
				// 122（敌方）- 若自身先出手则 n% 几率对手 stat 降低 m 级
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 122 && enemyFirst && enemyDamage > 0 &&
					!sptboss.IsStatDropImmune(playerPetID) && battle.PlayerImmuneStatDropRounds == 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					chance, statIdx, stages := 50, 0, 1
					if len(effArgs) >= 1 {
						chance = effArgs[0]
					}
					if len(effArgs) >= 2 {
						statIdx = effArgs[1]
					}
					if len(effArgs) >= 3 && effArgs[2] > 0 {
						stages = effArgs[2]
					}
					if statIdx >= 0 && statIdx < 6 && rand.Intn(100) < chance {
						cur := int(battle.PlayerBattleLv[statIdx])
						cur -= stages
						if cur < -6 {
							cur = -6
						}
						battle.PlayerBattleLv[statIdx] = int8(cur)
					}
				}
				// 148（敌方）- 若自身后出手则 n% 几率对手 stat 降低 m 级
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 148 && !enemyFirst && enemyDamage > 0 &&
					!sptboss.IsStatDropImmune(playerPetID) && battle.PlayerImmuneStatDropRounds == 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					chance, statIdx, stages := 50, 0, 1
					if len(effArgs) >= 1 {
						chance = effArgs[0]
					}
					if len(effArgs) >= 2 {
						statIdx = effArgs[1]
					}
					if len(effArgs) >= 3 && effArgs[2] > 0 {
						stages = effArgs[2]
					}
					if statIdx >= 0 && statIdx < 6 && rand.Intn(100) < chance {
						cur := int(battle.PlayerBattleLv[statIdx])
						cur -= stages
						if cur < -6 {
							cur = -6
						}
						battle.PlayerBattleLv[statIdx] = int8(cur)
					}
				}
				// 147（敌方）- 若自身后出手则 n% 几率令对手陷入异常状态
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 147 && !enemyFirst && enemyDamage > 0 &&
					!sptboss.IsStatusImmune(playerPetID) && battle.PlayerImmuneStatusRounds == 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					chance, statusIdx := 50, gameskills.StatusIndexBurn
					if len(effArgs) >= 1 {
						chance = effArgs[0]
					}
					if len(effArgs) >= 2 {
						statusIdx = effArgs[1]
					}
					if statusIdx >= 0 && statusIdx < 20 && battle.PlayerStatus[statusIdx] == 0 && rand.Intn(100) < chance {
						battle.PlayerStatus[statusIdx] = 2
					}
				}
				// 173（敌方）- 若自身先出手则 n% 几率令对手陷入异常状态
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 173 && enemyFirst && enemyDamage > 0 &&
					!sptboss.IsStatusImmune(playerPetID) && battle.PlayerImmuneStatusRounds == 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					chance, statusIdx := 50, gameskills.StatusIndexBurn
					if len(effArgs) >= 1 {
						chance = effArgs[0]
					}
					if len(effArgs) >= 2 {
						statusIdx = effArgs[1]
					}
					if statusIdx >= 0 && statusIdx < 20 && battle.PlayerStatus[statusIdx] == 0 && rand.Intn(100) < chance {
						battle.PlayerStatus[statusIdx] = 2
					}
				}
				// 115（敌方）- n% 概率附加速度的 1/m 点固定伤害
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 115 && enemyDamage > 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					chance, divisor := 30, 2
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						chance = effArgs[0]
					}
					if len(effArgs) >= 2 && effArgs[1] > 0 {
						divisor = effArgs[1]
					}
					if rand.Intn(100) < chance {
						bonus := uint32(enemyStats.Speed) / uint32(divisor)
						if bonus > 0 && battle.PlayerHP > 0 {
							if bonus > battle.PlayerHP {
								bonus = battle.PlayerHP
							}
							battle.PlayerHP -= bonus
						}
					}
				}
				// 119（敌方）- 伤害为偶数时 30% 疲惫对手 +1 回合；奇数时 30% 自身速度 +1
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 119 && enemyDamage > 0 {
					if enemyDamage%2 == 0 {
						if !sptboss.IsControlImmune(playerPetID) && battle.PlayerImmuneStatusRounds == 0 && rand.Intn(100) < 30 {
							if battle.PlayerStatus[gameskills.StatusIndexFatigue] == 0 {
								battle.PlayerStatus[gameskills.StatusIndexFatigue] = 1
							}
						}
					} else {
						if rand.Intn(100) < 30 {
							cur := int(battle.EnemyBattleLv[gameskills.StatSpeed]) + 1
							if cur > 6 {
								cur = 6
							}
							battle.EnemyBattleLv[gameskills.StatSpeed] = int8(cur)
						}
					}
				}
				// 134（敌方）- 造成的伤害低于 n，则自身所有技能的 PP +m
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 134 && enemyDamage > 0 && !battle.EnemyPPInfinite {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					threshold, ppBonus := 100, 1
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						threshold = effArgs[0]
					}
					if len(effArgs) >= 2 && effArgs[1] > 0 {
						ppBonus = effArgs[1]
					}
					if enemyDamage < uint32(threshold) {
						for i := 0; i < 4; i++ {
							if battle.EnemySkillPP[i] > 0 {
								newPP := int(battle.EnemySkillPP[i]) + ppBonus
								if newPP > 255 {
									newPP = 255
								}
								battle.EnemySkillPP[i] = byte(newPP)
							}
						}
					}
				}
				// 188（敌方）- 若自身处于异常状态，则附加对应的反击效果
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 188 && enemyDamage > 0 && !sptboss.IsStatusImmune(playerPetID) && battle.PlayerImmuneStatusRounds == 0 {
					if battle.EnemyStatus[gameskills.StatusIndexBurn] > 0 && battle.PlayerStatus[gameskills.StatusIndexBurn] == 0 {
						battle.PlayerStatus[gameskills.StatusIndexBurn] = byte(rand.Intn(2) + 1)
						dmg := battle.PlayerMaxHP / 8
						if dmg > battle.PlayerHP {
							dmg = battle.PlayerHP
						}
						battle.PlayerHP -= dmg
					} else if battle.EnemyStatus[gameskills.StatusIndexFreeze] > 0 && battle.PlayerStatus[gameskills.StatusIndexFreeze] == 0 {
						battle.PlayerStatus[gameskills.StatusIndexFreeze] = byte(rand.Intn(2) + 1)
						dmg := battle.PlayerMaxHP / 8
						if dmg > battle.PlayerHP {
							dmg = battle.PlayerHP
						}
						battle.PlayerHP -= dmg
					} else if battle.EnemyStatus[gameskills.StatusIndexPoison] > 0 && battle.PlayerStatus[gameskills.StatusIndexPoison] == 0 {
						battle.PlayerStatus[gameskills.StatusIndexPoison] = byte(rand.Intn(2) + 1)
						dmg := battle.PlayerMaxHP / 8
						if dmg > battle.PlayerHP {
							dmg = battle.PlayerHP
						}
						battle.PlayerHP -= dmg
					}
				}
				// 428（敌方）- 命中时附加 m 点固定伤害
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 428 && enemyDamage > 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					bonus := uint32(50)
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						bonus = uint32(effArgs[0])
					}
					if bonus > 0 && battle.PlayerHP > 0 {
						if bonus > battle.PlayerHP {
							bonus = battle.PlayerHP
						}
						battle.PlayerHP -= bonus
					}
				}
				// 464（敌方）- 命中时 m% 概率使对方烧伤
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 464 && enemyDamage > 0 &&
					!sptboss.IsStatusImmune(playerPetID) && battle.PlayerImmuneStatusRounds == 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					chance := 30
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						chance = effArgs[0]
					}
					if rand.Intn(100) < chance && battle.PlayerStatus[gameskills.StatusIndexBurn] == 0 {
						battle.PlayerStatus[gameskills.StatusIndexBurn] = byte(rand.Intn(2) + 1)
						dmg := battle.PlayerMaxHP / 8
						if dmg > battle.PlayerHP {
							dmg = battle.PlayerHP
						}
						battle.PlayerHP -= dmg
					}
				}
				// 181（敌方）- n% 概率使对手XX，每次使用m%增加，最高k%
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 181 && enemyHit &&
					!sptboss.IsStatusImmune(playerPetID) && battle.PlayerImmuneStatusRounds == 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					statusIdx, baseChance, increment, maxChance := gameskills.StatusIndexBurn, 30, 10, 100
					if len(effArgs) >= 1 {
						statusIdx = effArgs[0]
					}
					if len(effArgs) >= 2 && effArgs[1] > 0 {
						baseChance = effArgs[1]
					}
					if len(effArgs) >= 3 && effArgs[2] > 0 {
						increment = effArgs[2]
					}
					if len(effArgs) >= 4 && effArgs[3] > 0 {
						maxChance = effArgs[3]
					}
					if battle.Enemy181CurrentChance == 0 {
						battle.Enemy181CurrentChance = byte(baseChance)
						battle.Enemy181StatusIdx = byte(statusIdx)
						battle.Enemy181MaxChance = byte(maxChance)
						battle.Enemy181Increment = byte(increment)
					}
					currentChance := int(battle.Enemy181CurrentChance)
					if statusIdx >= 0 && statusIdx < 20 && rand.Intn(100) < currentChance {
						if battle.PlayerStatus[statusIdx] == 0 {
							battle.PlayerStatus[statusIdx] = byte(rand.Intn(2) + 2)
						}
					}
					newChance := currentChance + increment
					if newChance > maxChance {
						newChance = maxChance
					}
					battle.Enemy181CurrentChance = byte(newChance)
				}
				// 441（敌方）- 每次攻击暴击率 +n%，最高 m%
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 441 && enemyHit {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					increment, maxBonus := 1, 8
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						increment = effArgs[0] / 6
						if increment < 1 {
							increment = 1
						}
					}
					if len(effArgs) >= 2 && effArgs[1] > 0 {
						maxBonus = effArgs[1] / 6
						if maxBonus < 1 {
							maxBonus = 1
						}
					}
					newBonus := int(battle.EnemyCritRateBonus) + increment
					if newBonus > maxBonus {
						newBonus = maxBonus
					}
					if newBonus > 16 {
						newBonus = 16
					}
					battle.EnemyCritRateBonus = byte(newBonus)
				}
				// 490（敌方）- 若造成伤害超过 m，则自身速度 +n 级
				if enemySkillForTurn != nil && enemySkillForTurn.EffectID == 490 && enemyDamage > 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					threshold, stages := 200, 1
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						threshold = effArgs[0]
					}
					if len(effArgs) >= 2 && effArgs[1] > 0 {
						stages = effArgs[1]
					}
					if enemyDamage > uint32(threshold) {
						cur := int(battle.EnemyBattleLv[gameskills.StatSpeed]) + stages
						if cur > 6 {
							cur = 6
						}
						battle.EnemyBattleLv[gameskills.StatSpeed] = int8(cur)
					}
				}
				// 89（敌方）- 吸血：造成伤害时恢复自身 damage/divisor 体力
				if battle.EnemyLifestealRounds > 0 && enemyDamage > 0 && battle.EnemyLifestealDivisor > 0 {
					heal := enemyDamage / uint32(battle.EnemyLifestealDivisor)
					if heal > 0 {
						newHP := battle.EnemyHP + heal
						if newHP > battle.EnemyMaxHP {
							newHP = battle.EnemyMaxHP
						}
						battle.EnemyHP = newHP
					}
				}
				// 104（敌方）- n 回合内每次直接攻击 m% 几率附带衰弱（随机能力-1）
				if battle.EnemyWeaknessOnHitRounds > 0 && enemySkillForTurn.Category != 4 && enemyDamage > 0 &&
					!sptboss.IsStatDropImmune(playerPetID) && battle.PlayerImmuneStatDropRounds == 0 {
					if rand.Intn(100) < int(battle.EnemyWeaknessOnHitChance) {
						stat := rand.Intn(6)
						cur := int(battle.PlayerBattleLv[stat])
						cur--
						if cur < -6 {
							cur = -6
						}
						battle.PlayerBattleLv[stat] = int8(cur)
					}
				}
				// 109（敌方）- 造成伤害时 m% 几率令对手冻伤
				if battle.EnemyFreezeOnDealDamageRounds > 0 && enemySkillForTurn.Category != 4 && enemyDamage > 0 &&
					!sptboss.IsControlImmune(playerPetID) && battle.PlayerStatus[gameskills.StatusIndexFreeze] == 0 {
					if rand.Intn(100) < int(battle.EnemyFreezeOnDealDamageChance) {
						battle.PlayerStatus[gameskills.StatusIndexFreeze] = byte(rand.Intn(2) + 1)
						dmg := battle.PlayerMaxHP / 8
						if dmg > battle.PlayerHP {
							dmg = battle.PlayerHP
						}
						battle.PlayerHP -= dmg
					}
				}
				// 107（敌方）- 若本次攻击造成的伤害小于 n 则自身 xx 等级提升 1
				if enemyHit && enemySkillForTurn != nil && enemySkillForTurn.EffectID == 107 && enemySkillForTurn.Category != 4 && enemyDamage > 0 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					if len(effArgs) >= 2 {
						thresh, statIdx := effArgs[0], effArgs[1]
						if statIdx >= 0 && statIdx < 6 && enemyDamage < uint32(thresh) {
							cur := int(battle.EnemyBattleLv[statIdx])
							cur++
							if cur > 6 {
								cur = 6
							}
							battle.EnemyBattleLv[statIdx] = int8(cur)
						}
					}
				}
				// 66 - 击败回血（敌方版本）：当次攻击击败我方时恢复敌方最大体力的1/n
				if playerHPBeforeDamage > 0 && battle.PlayerHP == 0 && enemySkillForTurn != nil && enemySkillForTurn.EffectID == 66 {
					div := 2
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						div = effArgs[0]
					}
					heal := battle.EnemyMaxHP / uint32(div)
					if heal > 0 {
						newHP := battle.EnemyHP + heal
						if newHP > battle.EnemyMaxHP {
							newHP = battle.EnemyMaxHP
						}
						battle.EnemyHP = newHP
					}
				}
				// 67 - 击败减对方下只最大HP（敌方版本）：当次攻击击败我方时减少我方下次出战精灵的最大体力1/n
				if playerHPBeforeDamage > 0 && battle.PlayerHP == 0 && enemySkillForTurn != nil && enemySkillForTurn.EffectID == 67 {
					div := 2
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						div = effArgs[0]
					}
					battle.EnemyKillReduceMaxHpDivisor = byte(div)
				}
				// 158（敌方）- 击败对手后 n% 几率自身 stat 提升 m 级
				if playerHPBeforeDamage > 0 && battle.PlayerHP == 0 && enemySkillForTurn != nil && enemySkillForTurn.EffectID == 158 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					chance, statIdx, stages := 100, 0, 1
					if len(effArgs) >= 1 {
						chance = effArgs[0]
					}
					if len(effArgs) >= 2 {
						statIdx = effArgs[1]
					}
					if len(effArgs) >= 3 && effArgs[2] > 0 {
						stages = effArgs[2]
					}
					if statIdx >= 0 && statIdx < 6 && rand.Intn(100) < chance {
						cur := int(battle.EnemyBattleLv[statIdx])
						cur += stages
						if cur > 6 {
							cur = 6
						}
						battle.EnemyBattleLv[statIdx] = int8(cur)
					}
				}
				// 73 - 先手反弹（敌方版本）：如果敌方先出手，则受攻击时反弹200%的伤害给我方
				if battle.EnemyFirstStrikeReflectActive && battle.EnemyFirstStrikeReflectRounds > 0 && enemyDamage > 0 {
					reflectDamage := enemyDamage * 2
					if reflectDamage > battle.PlayerHP {
						reflectDamage = battle.PlayerHP
					}
					battle.PlayerHP -= reflectDamage
				}
				if battle.PlayerHP > battle.PlayerMaxHP {
					battle.PlayerHP = 0 // uint 下溢时置 0
				}

				// 回神(1028)：精灵体力降至 1/8 以下时，有3%几率体力回满
				if playerTrait == 1028 && playerHPBeforeEnemy > battle.PlayerMaxHP/8 &&
					battle.PlayerHP > 0 && battle.PlayerHP <= battle.PlayerMaxHP/8 {
					if rand.Intn(100) < 3 {
						battle.PlayerHP = battle.PlayerMaxHP
					}
				}
				// 受到普通攻击时有3%几率使对方进入异常状态（1029-1034）；雷伊/哈莫雷特/奈尼芬多/盖亚免疫
				if enemySkillForTurn.Category != 4 && playerTrait >= 1029 && playerTrait <= 1034 && !sptboss.IsStatusImmune(battle.EnemyID) {
					if rand.Intn(100) < 3 {
						statusIndex := -1
						switch playerTrait {
						case 1029:
							statusIndex = gameskills.StatusIndexParalysis
						case 1030:
							statusIndex = gameskills.StatusIndexPoison
						case 1031:
							statusIndex = gameskills.StatusIndexBurn
						case 1032:
							statusIndex = gameskills.StatusIndexFreeze
						case 1033:
							statusIndex = gameskills.StatusIndexFear
						case 1034:
							statusIndex = gameskills.StatusIndexSleep
						}
						if statusIndex >= 0 && statusIndex < len(battle.EnemyStatus) {
							if battle.EnemyStatus[statusIndex] == 0 {
								battle.EnemyStatus[statusIndex] = 2
							}
						}
					}
				}
				// 受到特殊攻击时 5% 几率降低对方能力等级（1035-1038,1040）
				if enemySkillForTurn.Category == 2 {
					statIndex := -1
					switch playerTrait {
					case 1035:
						statIndex = gameskills.StatAttack
					case 1036:
						statIndex = gameskills.StatDefence
					case 1037:
						statIndex = gameskills.StatSpAtk
					case 1038:
						statIndex = gameskills.StatSpDef
					case 1040:
						statIndex = gameskills.StatSpeed
					}
					if statIndex >= 0 && statIndex < len(battle.EnemyBattleLv) {
						if rand.Intn(100) < 5 {
							cur := int(battle.EnemyBattleLv[statIndex])
							cur--
							if sptboss.IsStatDropImmune(battle.EnemyID) && cur < 0 {
								cur = 0
							} else if cur < -6 {
								cur = -6
							}
							battle.EnemyBattleLv[statIndex] = int8(cur)
						}
					}
				}
				// 受到任何攻击时 5% 几率提升自身能力等级（1041-1045）
				boostStat := -1
				switch playerTrait {
				case 1041:
					boostStat = gameskills.StatAttack
				case 1042:
					boostStat = gameskills.StatDefence
				case 1043:
					boostStat = gameskills.StatSpAtk
				case 1044:
					boostStat = gameskills.StatSpDef
				case 1045:
					boostStat = gameskills.StatSpeed
				}
				if boostStat >= 0 && boostStat < len(battle.PlayerBattleLv) && enemySkillForTurn.Category != 4 {
					if rand.Intn(100) < 5 {
						cur := int(battle.PlayerBattleLv[boostStat])
						cur++
						if cur > 6 {
							cur = 6
						}
						battle.PlayerBattleLv[boostStat] = int8(cur)
					}
				}
			}

			// 敌方技能附加效果（红韵/魅惑等）：对敌方自身和我方的能力等级、异常状态等进行修改
			// 这里沿用 ApplyEffect 语义：
			//   - 第一个 HP/status/battleLv 参数 = 出手方（此处为敌人）
			//   - 第二个 HP/status/battleLv 参数 = 被攻击方（此处为玩家）
			// 仅当本次命中或为自身必中强化类技能时才应用效果；后者由 MustHit 标记保证。
			// 478 - 我方施加的“对手属性技能无效”：敌方使用 Category=4 时跳过效果
			enemyStatusSkillInvalid := battle.EnemyStatusSkillInvalidRounds > 0 && enemySkillForTurn != nil && enemySkillForTurn.Category == 4
			if enemyHit && enemySkillForTurn != nil && !enemyStatusSkillInvalid {
				var enemyEffectRecoil uint32
				defenderPetID := 0
				if battle.ActivePetIndex >= 0 && battle.ActivePetIndex < len(user.Pets) {
					defenderPetID = user.Pets[battle.ActivePetIndex].ID
				}
				var oldPlayerLvE [6]int8
				var oldEnemyLvE [6]int8
				var oldPlayerStatusE, oldEnemyStatusE [20]byte
				if battle.EnemyStatusMirrorRounds > 0 {
					oldPlayerLvE = battle.PlayerBattleLv
					oldEnemyLvE = battle.EnemyBattleLv
					oldPlayerStatusE = battle.PlayerStatus
					oldEnemyStatusE = battle.EnemyStatus
				}
				_, enemyEffectRecoil = gameskills.ApplyEffect(enemySkillForTurn, enemyDamage,
					&battle.EnemyHP, &battle.PlayerHP,
					battle.EnemyMaxHP, battle.PlayerMaxHP,
					&battle.EnemyBattleLv, &battle.PlayerBattleLv,
					&battle.EnemyStatus, &battle.PlayerStatus, defenderPetID,
					battle.PlayerImmuneStatDropRounds, battle.PlayerImmuneStatusRounds)
				_ = enemyEffectRecoil
				if battle.EnemyStatusMirrorRounds > 0 {
					for i := 0; i < 6; i++ {
						deltaE := int(battle.EnemyBattleLv[i]) - int(oldEnemyLvE[i])
						deltaP := int(battle.PlayerBattleLv[i]) - int(oldPlayerLvE[i])
						v := int(oldPlayerLvE[i]) + deltaE + deltaP
						if v > 6 {
							v = 6
						}
						if v < -6 {
							v = -6
						}
						battle.PlayerBattleLv[i] = int8(v)
						v = int(oldEnemyLvE[i]) + deltaE + deltaP
						if v > 6 {
							v = 6
						}
						if v < -6 {
							v = -6
						}
						battle.EnemyBattleLv[i] = int8(v)
					}
					for i := 0; i < 20; i++ {
						deltaE := int(battle.EnemyStatus[i]) - int(oldEnemyStatusE[i])
						deltaP := int(battle.PlayerStatus[i]) - int(oldPlayerStatusE[i])
						v := int(oldPlayerStatusE[i]) + deltaE + deltaP
						if v < 0 {
							v = 0
						}
						if v > 10 {
							v = 10
						}
						battle.PlayerStatus[i] = byte(v)
						v = int(oldEnemyStatusE[i]) + deltaE + deltaP
						if v < 0 {
							v = 0
						}
						if v > 10 {
							v = 10
						}
						battle.EnemyStatus[i] = byte(v)
					}
				}

				// 敌方暴击率提升效果（SideEffect 58 系列）
				if enemySkillForTurn.EffectID == 58 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					if len(effArgs) >= 1 {
						rounds := effArgs[0]
						if rounds < 0 {
							rounds = 0
						}
						if rounds > 0 {
							// +1 同理，保证敌方也能获得 param1[0] 个完整回合的必定暴击效果
							battle.EnemyCritBuffRounds = byte(rounds + 1)
						}
					}
				}
				// 敌方 32：n 回合暴击率增加 1/16
				if enemySkillForTurn.EffectID == 32 {
					effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
					rounds := 3
					if len(effArgs) >= 1 && effArgs[0] > 0 {
						rounds = effArgs[0]
					}
					if rounds > 10 {
						rounds = 10
					}
					battle.EnemyCritRateBonusRounds = byte(rounds + 1)
				}
			}

			// 敌方若也配置了“不灭之火”效果，同样能在若干回合内令我方每回合受到固定伤害（478 时属性技能无效，不设置）
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 60 {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, perTurn := 0, 0
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					perTurn = effArgs[1]
				}
				if rounds > 0 && perTurn > 0 {
					battle.PlayerFixedDotRounds = byte(rounds)
					battle.PlayerFixedDotDamage = uint32(perTurn)
				}
			}
			// 敌方 76：m% 几率在 n 回合内每回合造成 k 点固定伤害
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 76 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				chance, rounds, perTurn := 100, 0, 0
				if len(effArgs) >= 1 {
					chance = effArgs[0]
				}
				if len(effArgs) >= 2 {
					rounds = effArgs[1]
				}
				if len(effArgs) >= 3 {
					perTurn = effArgs[2]
				}
				if rounds > 0 && perTurn > 0 && rand.Intn(100) < chance {
					battle.PlayerFixedDotRounds = byte(rounds)
					battle.PlayerFixedDotDamage = uint32(perTurn)
				}
			}
			// 敌方 77：n 回合内每次使用技能恢复 m 点体力
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 77 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, amount := 0, 0
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					amount = effArgs[1]
				}
				if rounds > 0 && amount > 0 {
					battle.EnemyRegenPerUseRounds = byte(rounds)
					battle.EnemyRegenPerUseAmount = uint32(amount)
				}
			}
			// 敌方 78：n 回合内物理攻击对敌方必定 miss
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 78 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds := 0
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if rounds > 0 {
					battle.EnemyPhysMissRounds = byte(rounds)
				}
			}
			// 敌方 83：自身雄性下两回合必定先手；雌性下两回合必定暴击
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 83 && enemyHit {
				enemyPet := petMgr.Get(battle.EnemyID)
				if enemyPet != nil {
					if enemyPet.Gender == 1 {
						battle.EnemyMaleFirstStrikeRounds = 2
					} else if enemyPet.Gender == 2 {
						battle.EnemyFemaleCritRounds = 2
					}
				}
			}
			// 敌方 84：n 回合内受到物理攻击时 m% 几率将对手麻痹
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 84 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, chance := 5, 50
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if rounds > 0 {
					battle.EnemyParalyzeOnPhysHitRounds = byte(rounds)
					if chance > 100 {
						chance = 100
					}
					battle.EnemyParalyzeOnPhysHitChance = byte(chance)
				}
			}
			// 敌方 86/106：n 回合内属性（特殊）攻击对自身必定 miss
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && (enemySkillForTurn.EffectID == 86 || enemySkillForTurn.EffectID == 106) && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds := 0
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if rounds > 0 {
					battle.EnemySpecialMissRounds = byte(rounds)
				}
			}
			// 敌方 89：n 回合内每次造成伤害的 1/m 恢复体力
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 89 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, div := 0, 4
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					div = effArgs[1]
				}
				if rounds > 0 {
					battle.EnemyLifestealRounds = byte(rounds)
					battle.EnemyLifestealDivisor = byte(div)
				}
			}
			// 敌方 90：n 回合内自身造成的伤害为 m 倍
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 90 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, mult := 0, 2
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					mult = effArgs[1]
				}
				if rounds > 0 {
					battle.EnemyDamageMultRounds = byte(rounds)
					battle.EnemyDamageMult = byte(mult)
				}
			}
			// 敌方 92：n 回合内受到物理攻击时 m% 几率将对手冻伤
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 92 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, chance := 5, 50
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if rounds > 0 {
					battle.EnemyFreezeOnPhysHitRounds = byte(rounds)
					if chance > 100 {
						chance = 100
					}
					battle.EnemyFreezeOnPhysHitChance = byte(chance)
				}
			}
			// 敌方 98：n 回合内对雄性精灵的伤害为 m 倍
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 98 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, mult := 0, 2
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					mult = effArgs[1]
				}
				if rounds > 0 {
					battle.EnemyMaleDamageMultRounds = byte(rounds)
					battle.EnemyMaleDamageMult = byte(mult)
				}
			}
			// 敌方 104：n 回合内每次直接攻击 m% 几率附带衰弱
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 104 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, chance := 0, 50
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if rounds > 0 {
					battle.EnemyWeaknessOnHitRounds = byte(rounds)
					if chance > 100 {
						chance = 100
					}
					battle.EnemyWeaknessOnHitChance = byte(chance)
				}
			}
			// 敌方 91：n 回合内双方状态变化同时影响己方与对手
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 91 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds := 0
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if rounds > 0 {
					battle.EnemyStatusMirrorRounds = byte(rounds)
				}
			}
			// 敌方 127：n% 概率 m 回合内受到伤害减半
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 127 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				chance, rounds := 50, 3
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					chance = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					rounds = effArgs[1]
				}
				if chance > 100 {
					chance = 100
				}
				if rand.Intn(100) < chance && rounds > 0 {
					battle.EnemyDamageHalfRounds = byte(rounds)
				}
			}
			// 敌方 146：n 回合内受物理攻击时 m% 使对方中毒
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 146 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, m := 5, 50
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					m = effArgs[1]
				}
				if m > 100 {
					m = 100
				}
				if rounds > 0 {
					battle.EnemyPoisonOnPhysHitRounds = byte(rounds)
					battle.EnemyPoisonOnPhysHitChance = byte(m)
				}
			}
			// 敌方 150：n 回合内对手（我方）每回合防、特防等级 m
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 150 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, m := 0, 1
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					m = effArgs[1]
				}
				if rounds > 0 {
					battle.PlayerDefSpDefRounds = byte(rounds)
					if m < -6 {
						m = -6
					}
					if m > 6 {
						m = 6
					}
					battle.PlayerDefSpDefStages = int8(m)
				}
			}
			// 敌方 108：n 回合内受到物理攻击时 m% 几率将对手烧伤
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 108 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, chance := 5, 50
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if rounds > 0 {
					battle.EnemyBurnOnPhysHitRounds = byte(rounds)
					if chance > 100 {
						chance = 100
					}
					battle.EnemyBurnOnPhysHitChance = byte(chance)
				}
			}
			// 敌方 109：n 回合内造成伤害时 m% 几率令对手冻伤
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 109 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, chance := 5, 50
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					chance = effArgs[1]
				}
				if rounds > 0 {
					battle.EnemyFreezeOnDealDamageRounds = byte(rounds)
					if chance > 100 {
						chance = 100
					}
					battle.EnemyFreezeOnDealDamageChance = byte(chance)
				}
			}
			// 敌方 77（已有状态）：每次使用技能恢复 m 点体力
			if enemyHit && battle.EnemyRegenPerUseRounds > 0 && battle.EnemyRegenPerUseAmount > 0 {
				heal := battle.EnemyRegenPerUseAmount
				if heal > battle.EnemyMaxHP-battle.EnemyHP {
					heal = battle.EnemyMaxHP - battle.EnemyHP
				}
				if heal > 0 {
					battle.EnemyHP += heal
				}
			}

			// 敌方 Effect ID 39：n%降低对手所有技能m点PP值（478 时属性技能无效，不执行）
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 39 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				chance, ppReduction := 100, 1
				if len(effArgs) >= 1 {
					chance = effArgs[0]
				}
				if len(effArgs) >= 2 {
					ppReduction = effArgs[1]
				}
				if chance < 0 {
					chance = 0
				}
				if chance > 100 {
					chance = 100
				}
				if ppReduction < 0 {
					ppReduction = 0
				}
				// 随机判定是否触发
				if rand.Intn(100) < chance && ppReduction > 0 {
					// 降低玩家所有技能的 PP（但不能低于 0）
					for i := 0; i < 4; i++ {
						if battle.PlayerSkillPP[i] > 0 {
							if int(battle.PlayerSkillPP[i]) >= ppReduction {
								battle.PlayerSkillPP[i] -= byte(ppReduction)
							} else {
								battle.PlayerSkillPP[i] = 0
							}
						}
					}
					// 检查玩家当前选择的技能（skillID）的 PP 是否为 0
					// 如果玩家选择的技能 PP 为 0，则玩家下回合使用该技能时会因为 PP 为 0 而无法行动
					// 这个效果会在下回合的 PP 检查中自然生效（已在第8431-8448行实现）
				}
			}

			// 敌方多回合 BUFF/护盾（41-50）施加于自身
			if enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				n := 0
				if len(effArgs) >= 1 {
					n = effArgs[0]
				}
				if n < 0 {
					n = 0
				}
				switch enemySkillForTurn.EffectID {
				case 41:
					if len(effArgs) >= 2 {
						n = effArgs[1]
					}
					if n > 0 {
						battle.EnemyFireResistRounds = byte(n)
					}
				case 42:
					if len(effArgs) >= 2 {
						n = effArgs[1]
					}
					if n > 0 {
						battle.EnemyElectricBoostRounds = byte(n)
					}
				case 44:
					if n > 0 {
						battle.EnemySpDefHalfRounds = byte(n)
					}
				case 46:
					if n > 0 {
						battle.EnemyBlockCount = byte(n)
					}
				case 47:
					if n > 0 {
						battle.EnemyImmuneStatDropRounds = byte(n)
					}
				case 48:
					if n > 0 {
						battle.EnemyImmuneStatusRounds = byte(n)
					}
				case 49:
					if n > 0 {
						battle.EnemyShieldPoints = uint32(n)
					}
				case 50:
					if n > 0 {
						battle.EnemyPhysDefHalfRounds = byte(n)
					}
				case 45:
					if n > 0 {
						battle.EnemyCopyDefRounds = byte(n)
					}
				case 51:
					if n > 0 {
						battle.EnemyCopyAtkRounds = byte(n)
					}
				case 57:
					div := 5
					if len(effArgs) >= 2 && effArgs[1] > 0 {
						div = effArgs[1]
					}
					if n > 0 {
						battle.EnemyRegenRounds = byte(n)
						battle.EnemyRegenDivisor = byte(div)
					}
				case 65:
					mult, elemType := 2, 0
					if len(effArgs) >= 2 {
						mult = effArgs[1]
					}
					if len(effArgs) >= 3 {
						elemType = effArgs[2]
					}
					if n > 0 && mult > 0 {
						battle.EnemyElemPowerRounds = byte(n)
						battle.EnemyElemPowerMult = byte(mult)
						battle.EnemyElemPowerType = byte(elemType)
					}
				case 68:
					if n > 0 {
						battle.EnemyEndureRounds = byte(n)
					}
				case 52:
					if n > 0 {
						battle.EnemyEvasionRounds = byte(n)
					}
				case 53:
					mult := 2
					if len(effArgs) >= 2 && effArgs[1] > 0 {
						mult = effArgs[1]
					}
					if n > 0 {
						battle.EnemyDamageMultRounds = byte(n)
						battle.EnemyDamageMult = byte(mult)
					}
				case 54:
					mult := 2
					if len(effArgs) >= 2 && effArgs[1] > 0 {
						mult = effArgs[1]
					}
					if n > 0 {
						battle.EnemyDamageReductRounds = byte(n)
						battle.EnemyDamageReduct = byte(mult)
					}
				case 55:
					if n > 0 {
						battle.EnemyTypeSwapRounds = byte(n)
					}
				case 56:
					if n > 0 {
						battle.EnemyTypeCopyRounds = byte(n)
					}
				case 62:
					if n > 0 {
						battle.EnemyDestinyBondRounds = byte(n)
					}
				case 59: // 牺牲强化下一只：当前精灵被击败时，下一只上场的精灵获得能力强化
					// SideEffectArg: stat1 stages1 stat2 stages2 ... (例如 "0 2 2 1" 表示攻击+2级、特攻+1级)
					// 如果没有参数，默认全能力+1级
					battle.EnemySacrificeBuffActive = true
					// 清空之前的强化记录
					for i := 0; i < 6; i++ {
						battle.EnemySacrificeBuffStats[i] = 0
					}
					if len(effArgs) >= 2 {
						// 解析参数：stat stages 对
						for i := 0; i+1 < len(effArgs); i += 2 {
							stat := effArgs[i]
							stages := effArgs[i+1]
							if stat >= 0 && stat < 6 && stages > 0 {
								battle.EnemySacrificeBuffStats[stat] = int8(stages)
							}
						}
					} else {
						// 默认全能力+1级
						for i := 0; i < 6; i++ {
							battle.EnemySacrificeBuffStats[i] = 1
						}
					}
				case 69: // 药剂反噬：下n回合对手使用体力药剂时效果变成减少相应的体力
					if n > 0 {
						battle.EnemyPotionReverseRounds = byte(n)
					}
				case 71: // 牺牲暴击：自己牺牲(体力降到0), 使下一只出战精灵在前两回合内必定致命一击
					battle.EnemySacrificeCritActive = true
				case 72: // Miss死亡：如果此回合miss，则立即死亡
					battle.EnemyMissDeathActive = true
				case 73: // 先手反弹：如果先出手，则受攻击时反弹200%的伤害给对手，持续n回合
					if n > 0 {
						battle.EnemyFirstStrikeReflectRounds = byte(n)
						// 检查本回合是否先手：如果敌方先手（enemyFirst），则激活
						if enemyFirst {
							battle.EnemyFirstStrikeReflectActive = true
						}
					}
				}
			}
			// 463（敌方）- n 回合内每回合所受的伤害减少 m 点
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 463 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, amount := 0, 0
				if len(effArgs) >= 1 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					amount = effArgs[1]
				}
				if rounds > 0 && amount > 0 {
					battle.EnemyDamageReducePerRoundRounds = byte(rounds)
					battle.EnemyDamageReducePerRoundAmount = uint32(amount)
				}
			}
			// 110（敌方）- n 回合内每次受到攻击时 m% 几率使对手 stat 等级 -1
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 110 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, chance, stat := 0, 50, 0
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					chance = effArgs[1]
				}
				if len(effArgs) >= 3 {
					stat = effArgs[2]
				}
				if rounds > 0 {
					battle.EnemyDefendStatDropRounds = byte(rounds)
					if chance > 100 {
						chance = 100
					}
					battle.EnemyDefendStatDropChance = byte(chance)
					if stat < 0 || stat > 5 {
						stat = 0
					}
					battle.EnemyDefendStatDropStat = byte(stat)
				}
			}
			// 116（敌方）- n 回合内每次受到攻击造成伤害的 1/5 恢复自身体力
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 116 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds := 0
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					rounds = effArgs[0]
				}
				if rounds > 0 {
					battle.EnemyDefendHealRounds = byte(rounds)
				}
			}
			// 117（敌方）- n 回合内每次受到攻击 m% 概率使对手疲惫 1~3 回合
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 117 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, chance := 0, 30
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					chance = effArgs[1]
				}
				if rounds > 0 {
					battle.EnemyDefendFatigueRounds = byte(rounds)
					if chance > 100 {
						chance = 100
					}
					battle.EnemyDefendFatigueChance = byte(chance)
				}
			}
			// 123（敌方）- n 回合内受到任何伤害时自身 XX 提高 m 级
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 123 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, stat, stages := 0, 0, 1
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 {
					stat = effArgs[1]
				}
				if len(effArgs) >= 3 && effArgs[2] > 0 {
					stages = effArgs[2]
				}
				if rounds > 0 && stat >= 0 && stat < 6 {
					battle.EnemyHurtStatBoostRounds = byte(rounds)
					battle.EnemyHurtStatBoostStat = byte(stat)
					battle.EnemyHurtStatBoostStages = int8(stages)
				}
			}
			// 125（敌方）- n 回合内被攻击时减少受到的伤害上限 m
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 125 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, cap := 0, 0
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					cap = effArgs[1]
				}
				if rounds > 0 && cap > 0 {
					battle.EnemyDamageCapRounds = byte(rounds)
					battle.EnemyDamageCap = uint32(cap)
				}
			}
			// 126（敌方）- n 回合内每回合自身攻击和速度 +m 级
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 126 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds, stages := 0, 1
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					rounds = effArgs[0]
				}
				if len(effArgs) >= 2 && effArgs[1] > 0 {
					stages = effArgs[1]
				}
				if rounds > 0 {
					battle.EnemySpeedBoostRounds = byte(rounds)
					battle.EnemySpeedBoostStages = int8(stages)
				}
			}
			// 128（敌方）- n 回合内接受的物理伤害转化为体力恢复
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 128 && enemyHit {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds := 0
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					rounds = effArgs[0]
				}
				if rounds > 0 {
					battle.EnemyPhysDmgToHealRounds = byte(rounds)
				}
			}
			// 471（敌方）- 先出手时 n 回合内免疫异常状态
			if enemySkillForTurn != nil && !enemyStatusSkillInvalid && enemySkillForTurn.EffectID == 471 && enemyHit && enemyFirst {
				effArgs := gameskills.ParseSideEffectArg(enemySkillForTurn.SideEffectArg)
				rounds := 0
				if len(effArgs) >= 1 && effArgs[0] > 0 {
					rounds = effArgs[0]
				}
				if rounds > 0 {
					battle.EnemyImmuneStatusRounds = byte(rounds)
				}
			}

			// 敌方出手结束后，同样结算一次多回合固定伤害（若存在）
			applyFixedDotAfterAttack(&battle.PlayerHP, &battle.PlayerFixedDotDamage, &battle.PlayerFixedDotRounds)
			}
		}
	}
	}
	// 盖亚/雷伊先手：先执行敌方回合，若我方仍存活再执行我方出招；否则我方本回合未出招
	if isGaiyaFirst {
		doEnemyTurn()
		if battle.PlayerHP > 0 && !skipPlayerAction {
			fromGaiyaFirst = true
			goto doPlayerTurn
		}
		playerTurnSkippedBecauseDead = true
		goto afterEnemyTurn
	}
	if !isGaiyaFirst {
		doEnemyTurn()
	}
afterEnemyTurn:

	// 一整个战斗回合结束：暴击强化回合数（58 类效果）自动减 1
	if battle.PlayerCritBuffRounds > 0 {
		battle.PlayerCritBuffRounds--
	}
	if battle.EnemyCritBuffRounds > 0 {
		battle.EnemyCritBuffRounds--
	}
	// 81 - 必中回合数递减
	if battle.PlayerMustHitRounds > 0 {
		battle.PlayerMustHitRounds--
	}
	if battle.EnemyMustHitRounds > 0 {
		battle.EnemyMustHitRounds--
	}
	// 1635 - 誓言之约：倒计时到 0 时回满体力
	if battle.PlayerDelayedFullHealRounds > 0 {
		battle.PlayerDelayedFullHealRounds--
		if battle.PlayerDelayedFullHealRounds == 0 && battle.PlayerHP > 0 {
			battle.PlayerHP = battle.PlayerMaxHP
		}
	}
	if battle.EnemyDelayedFullHealRounds > 0 {
		battle.EnemyDelayedFullHealRounds--
		if battle.EnemyDelayedFullHealRounds == 0 && battle.EnemyHP > 0 {
			battle.EnemyHP = battle.EnemyMaxHP
		}
	}
	// 439 - 若自身处于能力下降或异常则对手本回合受到 m 点固定伤害
	if battle.PlayerDealFixedDotWhenWeakRounds > 0 {
		playerWeak := false
		for i := 0; i < 6; i++ {
			if battle.PlayerBattleLv[i] < 0 {
				playerWeak = true
				break
			}
		}
		if !playerWeak {
			for i := 0; i < 20; i++ {
				if battle.PlayerStatus[i] > 0 {
					playerWeak = true
					break
				}
			}
		}
		if playerWeak && battle.PlayerDealFixedDotWhenWeakDamage > 0 && battle.EnemyHP > 0 {
			d := battle.PlayerDealFixedDotWhenWeakDamage
			if d > battle.EnemyHP {
				d = battle.EnemyHP
			}
			battle.EnemyHP -= d
		}
		battle.PlayerDealFixedDotWhenWeakRounds--
	}
	if battle.EnemyDealFixedDotWhenWeakRounds > 0 {
		enemyWeak := false
		for i := 0; i < 6; i++ {
			if battle.EnemyBattleLv[i] < 0 {
				enemyWeak = true
				break
			}
		}
		if !enemyWeak {
			for i := 0; i < 20; i++ {
				if battle.EnemyStatus[i] > 0 {
					enemyWeak = true
					break
				}
			}
		}
		if enemyWeak && battle.EnemyDealFixedDotWhenWeakDamage > 0 && battle.PlayerHP > 0 {
			d := battle.EnemyDealFixedDotWhenWeakDamage
			if d > battle.PlayerHP {
				d = battle.PlayerHP
			}
			battle.PlayerHP -= d
		}
		battle.EnemyDealFixedDotWhenWeakRounds--
	}
	// 448 - 每回合对手全能力降低 stages 级
	if battle.EnemyAllStatDropRounds > 0 && !sptboss.IsStatDropImmune(battle.EnemyID) {
		for i := 0; i < 6; i++ {
			cur := int(battle.EnemyBattleLv[i])
			cur += int(battle.EnemyAllStatDropStages)
			if cur < -6 {
				cur = -6
			}
			battle.EnemyBattleLv[i] = int8(cur)
		}
		battle.EnemyAllStatDropRounds--
	}
	if battle.PlayerAllStatDropRounds > 0 {
		for i := 0; i < 6; i++ {
			cur := int(battle.PlayerBattleLv[i])
			cur += int(battle.PlayerAllStatDropStages)
			if cur < -6 {
				cur = -6
			}
			battle.PlayerBattleLv[i] = int8(cur)
		}
		battle.PlayerAllStatDropRounds--
	}
	// 478 - 对手属性技能无效回合数递减
	if battle.EnemyStatusSkillInvalidRounds > 0 {
		battle.EnemyStatusSkillInvalidRounds--
	}
	if battle.PlayerStatusSkillInvalidRounds > 0 {
		battle.PlayerStatusSkillInvalidRounds--
	}
	// 21 - 反弹伤害回合数递减
	if battle.PlayerReflectDamageRounds > 0 {
		battle.PlayerReflectDamageRounds--
	}
	if battle.EnemyReflectDamageRounds > 0 {
		battle.EnemyReflectDamageRounds--
	}
	// 32 - 暴击率增加回合数递减
	if battle.PlayerCritRateBonusRounds > 0 {
		battle.PlayerCritRateBonusRounds--
	}
	if battle.EnemyCritRateBonusRounds > 0 {
		battle.EnemyCritRateBonusRounds--
	}
	// 454 - 低血量先制加成回合数递减
	if battle.PlayerPriorityBonusWhenLowHPRounds > 0 {
		battle.PlayerPriorityBonusWhenLowHPRounds--
	}
	if battle.EnemyPriorityBonusWhenLowHPRounds > 0 {
		battle.EnemyPriorityBonusWhenLowHPRounds--
	}
	// 482 - 先制几率/数值为当回合生效，回合结束清除
	battle.PlayerPriorityBonusChance = 0
	battle.PlayerPriorityBonusAmount = 0
	battle.EnemyPriorityBonusChance = 0
	battle.EnemyPriorityBonusAmount = 0
	// 77 - 每次使用技能恢复体力，回合数递减
	if battle.PlayerRegenPerUseRounds > 0 {
		battle.PlayerRegenPerUseRounds--
	}
	if battle.EnemyRegenPerUseRounds > 0 {
		battle.EnemyRegenPerUseRounds--
	}
	// 78 - 物理攻击 miss，回合数递减
	if battle.PlayerPhysMissRounds > 0 {
		battle.PlayerPhysMissRounds--
	}
	if battle.EnemyPhysMissRounds > 0 {
		battle.EnemyPhysMissRounds--
	}
	// 83 - 雄性先手/雌性暴击，回合数递减
	if battle.PlayerMaleFirstStrikeRounds > 0 {
		battle.PlayerMaleFirstStrikeRounds--
	}
	if battle.PlayerFemaleCritRounds > 0 {
		battle.PlayerFemaleCritRounds--
	}
	if battle.EnemyMaleFirstStrikeRounds > 0 {
		battle.EnemyMaleFirstStrikeRounds--
	}
	if battle.EnemyFemaleCritRounds > 0 {
		battle.EnemyFemaleCritRounds--
	}
	// 91 - 状态镜像，回合数递减
	if battle.PlayerStatusMirrorRounds > 0 {
		battle.PlayerStatusMirrorRounds--
	}
	if battle.EnemyStatusMirrorRounds > 0 {
		battle.EnemyStatusMirrorRounds--
	}
	// 127 - 伤害减半，回合数递减
	if battle.PlayerDamageHalfRounds > 0 {
		battle.PlayerDamageHalfRounds--
	}
	if battle.EnemyDamageHalfRounds > 0 {
		battle.EnemyDamageHalfRounds--
	}
	// 84 - 受击麻痹，回合数递减
	if battle.PlayerParalyzeOnPhysHitRounds > 0 {
		battle.PlayerParalyzeOnPhysHitRounds--
	}
	if battle.EnemyParalyzeOnPhysHitRounds > 0 {
		battle.EnemyParalyzeOnPhysHitRounds--
	}
	// 146 - 受物理攻击时中毒，回合数递减
	if battle.PlayerPoisonOnPhysHitRounds > 0 {
		battle.PlayerPoisonOnPhysHitRounds--
	}
	if battle.EnemyPoisonOnPhysHitRounds > 0 {
		battle.EnemyPoisonOnPhysHitRounds--
	}
	// 150 - 对手每回合防/特防等级 m：回合末设定对手等级并递减
	if battle.EnemyDefSpDefRounds > 0 {
		battle.EnemyBattleLv[1] = battle.EnemyDefSpDefStages
		battle.EnemyBattleLv[3] = battle.EnemyDefSpDefStages
		battle.EnemyDefSpDefRounds--
	}
	if battle.PlayerDefSpDefRounds > 0 {
		battle.PlayerBattleLv[1] = battle.PlayerDefSpDefStages
		battle.PlayerBattleLv[3] = battle.PlayerDefSpDefStages
		battle.PlayerDefSpDefRounds--
	}
	// 86 - 属性攻击 miss，回合数递减
	if battle.PlayerSpecialMissRounds > 0 {
		battle.PlayerSpecialMissRounds--
	}
	if battle.EnemySpecialMissRounds > 0 {
		battle.EnemySpecialMissRounds--
	}
	// 89 - 吸血，回合数递减
	if battle.PlayerLifestealRounds > 0 {
		battle.PlayerLifestealRounds--
	}
	if battle.EnemyLifestealRounds > 0 {
		battle.EnemyLifestealRounds--
	}
	// 92 - 受击冻伤，回合数递减
	if battle.PlayerFreezeOnPhysHitRounds > 0 {
		battle.PlayerFreezeOnPhysHitRounds--
	}
	if battle.EnemyFreezeOnPhysHitRounds > 0 {
		battle.EnemyFreezeOnPhysHitRounds--
	}
	// 98 - 对雄性伤害倍率，回合数递减
	if battle.PlayerMaleDamageMultRounds > 0 {
		battle.PlayerMaleDamageMultRounds--
	}
	if battle.EnemyMaleDamageMultRounds > 0 {
		battle.EnemyMaleDamageMultRounds--
	}
	// 104 - 攻击附带衰弱，回合数递减
	if battle.PlayerWeaknessOnHitRounds > 0 {
		battle.PlayerWeaknessOnHitRounds--
	}
	if battle.EnemyWeaknessOnHitRounds > 0 {
		battle.EnemyWeaknessOnHitRounds--
	}
	// 108 - 受击烧伤，回合数递减
	if battle.PlayerBurnOnPhysHitRounds > 0 {
		battle.PlayerBurnOnPhysHitRounds--
	}
	if battle.EnemyBurnOnPhysHitRounds > 0 {
		battle.EnemyBurnOnPhysHitRounds--
	}
	// 109 - 造成伤害附带冻伤，回合数递减
	if battle.PlayerFreezeOnDealDamageRounds > 0 {
		battle.PlayerFreezeOnDealDamageRounds--
	}
	if battle.EnemyFreezeOnDealDamageRounds > 0 {
		battle.EnemyFreezeOnDealDamageRounds--
	}
	// 463 - 每回合所受伤害减少 m 点，回合数递减
	if battle.PlayerDamageReducePerRoundRounds > 0 {
		battle.PlayerDamageReducePerRoundRounds--
	}
	if battle.EnemyDamageReducePerRoundRounds > 0 {
		battle.EnemyDamageReducePerRoundRounds--
	}
	// 110 - 受击降能力，回合数递减
	if battle.PlayerDefendStatDropRounds > 0 {
		battle.PlayerDefendStatDropRounds--
	}
	if battle.EnemyDefendStatDropRounds > 0 {
		battle.EnemyDefendStatDropRounds--
	}
	// 116 - 受击回血，回合数递减
	if battle.PlayerDefendHealRounds > 0 {
		battle.PlayerDefendHealRounds--
	}
	if battle.EnemyDefendHealRounds > 0 {
		battle.EnemyDefendHealRounds--
	}
	// 117 - 受击疲惫，回合数递减
	if battle.PlayerDefendFatigueRounds > 0 {
		battle.PlayerDefendFatigueRounds--
	}
	if battle.EnemyDefendFatigueRounds > 0 {
		battle.EnemyDefendFatigueRounds--
	}
	// 125 - 伤害上限，回合数递减
	if battle.PlayerDamageCapRounds > 0 {
		battle.PlayerDamageCapRounds--
	}
	if battle.EnemyDamageCapRounds > 0 {
		battle.EnemyDamageCapRounds--
	}
	// 126 - n 回合内每回合自身攻击和速度 +m 级，回合末生效并递减
	if battle.PlayerSpeedBoostRounds > 0 && battle.PlayerSpeedBoostStages != 0 {
		for _, statIdx := range []int{gameskills.StatAttack, gameskills.StatSpeed} {
			cur := int(battle.PlayerBattleLv[statIdx]) + int(battle.PlayerSpeedBoostStages)
			if cur > 6 {
				cur = 6
			}
			if cur < -6 {
				cur = -6
			}
			battle.PlayerBattleLv[statIdx] = int8(cur)
		}
		battle.PlayerSpeedBoostRounds--
	}
	if battle.EnemySpeedBoostRounds > 0 && battle.EnemySpeedBoostStages != 0 {
		for _, statIdx := range []int{gameskills.StatAttack, gameskills.StatSpeed} {
			cur := int(battle.EnemyBattleLv[statIdx]) + int(battle.EnemySpeedBoostStages)
			if cur > 6 {
				cur = 6
			}
			if cur < -6 {
				cur = -6
			}
			battle.EnemyBattleLv[statIdx] = int8(cur)
		}
		battle.EnemySpeedBoostRounds--
	}
	// 123 - 受伤加能力，回合数递减
	if battle.PlayerHurtStatBoostRounds > 0 {
		battle.PlayerHurtStatBoostRounds--
	}
	if battle.EnemyHurtStatBoostRounds > 0 {
		battle.EnemyHurtStatBoostRounds--
	}
	// 128 - 物理伤害转回血，回合数递减
	if battle.PlayerPhysDmgToHealRounds > 0 {
		battle.PlayerPhysDmgToHealRounds--
	}
	if battle.EnemyPhysDmgToHealRounds > 0 {
		battle.EnemyPhysDmgToHealRounds--
	}
	// 545 - 受到伤害高于 m 则对手获得效果，回合数递减
	if battle.PlayerReflectStatusWhenHitRounds > 0 {
		battle.PlayerReflectStatusWhenHitRounds--
	}
	if battle.EnemyReflectStatusWhenHitRounds > 0 {
		battle.EnemyReflectStatusWhenHitRounds--
	}
	// 多回合 BUFF 回合数（41 42 44 47 48 50）每回合减 1
	if battle.PlayerFireResistRounds > 0 {
		battle.PlayerFireResistRounds--
	}
	if battle.PlayerElectricBoostRounds > 0 {
		battle.PlayerElectricBoostRounds--
	}
	if battle.PlayerSpDefHalfRounds > 0 {
		battle.PlayerSpDefHalfRounds--
	}
	if battle.PlayerImmuneStatDropRounds > 0 {
		battle.PlayerImmuneStatDropRounds--
	}
	if battle.PlayerImmuneStatusRounds > 0 {
		battle.PlayerImmuneStatusRounds--
	}
	if battle.PlayerPhysDefHalfRounds > 0 {
		battle.PlayerPhysDefHalfRounds--
	}
	if battle.EnemyFireResistRounds > 0 {
		battle.EnemyFireResistRounds--
	}
	if battle.EnemyElectricBoostRounds > 0 {
		battle.EnemyElectricBoostRounds--
	}
	if battle.EnemySpDefHalfRounds > 0 {
		battle.EnemySpDefHalfRounds--
	}
	if battle.EnemyImmuneStatDropRounds > 0 {
		battle.EnemyImmuneStatDropRounds--
	}
	if battle.EnemyImmuneStatusRounds > 0 {
		battle.EnemyImmuneStatusRounds--
	}
	if battle.EnemyPhysDefHalfRounds > 0 {
		battle.EnemyPhysDefHalfRounds--
	}
	// 45 51 65 68 回合数递减
	if battle.PlayerCopyDefRounds > 0 {
		battle.PlayerCopyDefRounds--
	}
	if battle.PlayerCopyAtkRounds > 0 {
		battle.PlayerCopyAtkRounds--
	}
	if battle.PlayerElemPowerRounds > 0 {
		battle.PlayerElemPowerRounds--
	}
	if battle.EnemyCopyDefRounds > 0 {
		battle.EnemyCopyDefRounds--
	}
	if battle.EnemyCopyAtkRounds > 0 {
		battle.EnemyCopyAtkRounds--
	}
	if battle.EnemyElemPowerRounds > 0 {
		battle.EnemyElemPowerRounds--
	}
	// 57：每回合回血 maxHP/divisor
	if battle.PlayerRegenRounds > 0 && battle.PlayerRegenDivisor > 0 && battle.PlayerHP > 0 && battle.PlayerHP < battle.PlayerMaxHP {
		heal := battle.PlayerMaxHP / uint32(battle.PlayerRegenDivisor)
		if heal > 0 {
			battle.PlayerHP += heal
			if battle.PlayerHP > battle.PlayerMaxHP {
				battle.PlayerHP = battle.PlayerMaxHP
			}
		}
		battle.PlayerRegenRounds--
	}
	if battle.EnemyRegenRounds > 0 && battle.EnemyRegenDivisor > 0 && battle.EnemyHP > 0 && battle.EnemyHP < battle.EnemyMaxHP {
		heal := battle.EnemyMaxHP / uint32(battle.EnemyRegenDivisor)
		if heal > 0 {
			battle.EnemyHP += heal
			if battle.EnemyHP > battle.EnemyMaxHP {
				battle.EnemyHP = battle.EnemyMaxHP
			}
		}
		battle.EnemyRegenRounds--
	}
	// 52 53 54 55 56 69 73 回合数递减
	if battle.PlayerEvasionRounds > 0 {
		battle.PlayerEvasionRounds--
	}
	if battle.PlayerDamageMultRounds > 0 {
		battle.PlayerDamageMultRounds--
	}
	if battle.PlayerDamageReductRounds > 0 {
		battle.PlayerDamageReductRounds--
	}
	if battle.PlayerTypeSwapRounds > 0 {
		battle.PlayerTypeSwapRounds--
	}
	if battle.PlayerTypeCopyRounds > 0 {
		battle.PlayerTypeCopyRounds--
	}
	if battle.PlayerPotionReverseRounds > 0 {
		battle.PlayerPotionReverseRounds--
	}
	if battle.PlayerFirstStrikeReflectRounds > 0 {
		battle.PlayerFirstStrikeReflectRounds--
		if battle.PlayerFirstStrikeReflectRounds == 0 {
			battle.PlayerFirstStrikeReflectActive = false
		}
	}
	if battle.EnemyEvasionRounds > 0 {
		battle.EnemyEvasionRounds--
	}
	if battle.EnemyDamageMultRounds > 0 {
		battle.EnemyDamageMultRounds--
	}
	if battle.EnemyDamageReductRounds > 0 {
		battle.EnemyDamageReductRounds--
	}
	if battle.EnemyTypeSwapRounds > 0 {
		battle.EnemyTypeSwapRounds--
	}
	if battle.EnemyTypeCopyRounds > 0 {
		battle.EnemyTypeCopyRounds--
	}
	if battle.EnemyPotionReverseRounds > 0 {
		battle.EnemyPotionReverseRounds--
	}
	if battle.EnemyFirstStrikeReflectRounds > 0 {
		battle.EnemyFirstStrikeReflectRounds--
		if battle.EnemyFirstStrikeReflectRounds == 0 {
			battle.EnemyFirstStrikeReflectActive = false
		}
	}
	// 62：镇魂歌，n 回合后若己方存活则对方死亡
	if battle.PlayerDestinyBondRounds > 0 {
		battle.PlayerDestinyBondRounds--
		if battle.PlayerDestinyBondRounds == 0 && battle.PlayerHP > 0 {
			battle.EnemyHP = 0
		}
	}
	if battle.EnemyDestinyBondRounds > 0 {
		battle.EnemyDestinyBondRounds--
		if battle.EnemyDestinyBondRounds == 0 && battle.EnemyHP > 0 {
			battle.PlayerHP = 0
		}
	}

	// 在构建 2505 之前统一钳位双方 HP，确保不会出现 HP > MaxHP 导致前端血条溢出。
	if battle.PlayerHP > battle.PlayerMaxHP {
		battle.PlayerHP = battle.PlayerMaxHP
	}
	if battle.EnemyHP > battle.EnemyMaxHP {
		battle.EnemyHP = battle.EnemyMaxHP
	}

	// 保存敌人反击后的玩家HP（用于 2505 的 remainHP，buildAttackValue 内会再钳位到 [0,maxHP]）
	playerHPAfterCounter := battle.PlayerHP

	// PvP 时需用对方 userID 填第二个 AttackValue，客户端才能正确匹配"对方"血条与图标
	opponentUID := battle.OpponentUserID
	ctx.GameServer.BattleMu.Unlock()

	// 构建 2505 响应：两个 AttackValue（出手顺序：盖亚261 时敌人先手，否则玩家先手）
	// 对齐 Lua / 2407 的约定：
	// - AttackValue.userID 表示“出手方”
	// - lostHP 表示“被攻击方本次损失的血量”
	// - remainHP/maxHP 表示“被攻击方当前血量/最大血量”（用于客户端更新血条）
	enemyUserID := uint32(0)
	if opponentUID != 0 {
		enemyUserID = uint32(opponentUID)
	}
	isCritU32 := uint32(0)
	if isCritPlayer {
		isCritU32 = 1
	}
	playerAtkTimesU32 := uint32(1)
	if !playerHit || playerTurnSkippedBecauseDead {
		playerAtkTimesU32 = 0
		damageCalc = 0
		isCritU32 = 0
		if playerTurnSkippedBecauseDead {
			effectGainHP = 0
		}
	}
	enemyAtkTimesU32 := uint32(1)
	if !enemyAttempted || !enemyHitFor2505 {
		enemyAtkTimesU32 = 0
		enemyDamageCalc = 0
	}
	playerSkillIDFor2505 := skillID
	if playerTurnSkippedBecauseDead {
		playerSkillIDFor2505 = 0
	}

	// 对 2505 中暴露给前端的 HP 进行“伪血量”缩放：谱尼使用 puniDisplayHP 压缩到 0~999，与 2504 一致，避免血条超出血槽
	playerRemainForAv := int32(playerHPAfterCounter)
	playerMaxForAv := battle.PlayerMaxHP
	enemyRemainForAv := int32(battle.EnemyHP)
	enemyMaxForAv := battle.EnemyMaxHP
	if (battle.BattleMapID == 108 || battle.BattleMapID == 514) && battle.EnemyID == 300 && battle.EnemyMaxHP > puniDisplayMaxHP {
		dispHP, dispMax := puniDisplayHP(battle.EnemyHP, battle.EnemyMaxHP)
		enemyRemainForAv = int32(dispHP)
		enemyMaxForAv = dispMax
	}

	playerAv := buildAttackValue(
		uint32(ctx.UserID), playerSkillIDFor2505, playerAtkTimesU32,
		damageCalc, effectGainHP,
		playerRemainForAv, playerMaxForAv,
		0, isCritU32, 0,
		battle.PlayerStatus, battle.PlayerBattleLv,
	)
	enemyAv := buildAttackValue(
		enemyUserID, enemySkillID, enemyAtkTimesU32,
		enemyDamageCalc, 0,
		enemyRemainForAv, enemyMaxForAv,
		0, 0, 0,
		battle.EnemyStatus, battle.EnemyBattleLv,
	)
	body := make([]byte, 0, 160)
	// 先手由速度+先制比较决定（雷伊/盖亚 技能先制+6）：2505 按实际出手顺序
	if isGaiyaFirst {
		body = append(body, enemyAv...)
		body = append(body, playerAv...)
	} else {
		body = append(body, playerAv...)
		body = append(body, enemyAv...)
	}

	ctx.GameServer.SendResponse(ctx.ClientData, 2505, ctx.UserID, ctx.SeqID, body)
	// PvP：向对方也发送 2505（同一 body：first=攻击方，second=对方），对方客户端用 userID 匹配更新我方/对方血条
	if opponentUID != 0 {
		if otherClient := ctx.GameServer.GetClientByUserID(opponentUID); otherClient != nil {
			ctx.GameServer.SendResponse(otherClient, 2505, opponentUID, 0, body)
		}
	}

	logger.Info(fmt.Sprintf("[2405] 使用技能后: PlayerHP=%d/%d EnemyHP=%d/%d Damage=%d EnemyDamage=%d",
		battle.PlayerHP, battle.PlayerMaxHP, battle.EnemyHP, battle.EnemyMaxHP, damage, enemyDamage))

	// 检查战斗是否结束
	isOver := false
	winnerID := uint32(0)

	// 敌方 HP 为 0：直接判定玩家获胜
	if battle.EnemyHP == 0 {
		isOver = true
		winnerID = uint32(ctx.UserID)
		logger.Info(fmt.Sprintf("[2405] 战斗结束: 玩家获胜"))
	} else if battle.PlayerHP == 0 {
		// 我方当前出战精灵 HP 为 0：
		// 依据 BattleState 中记录的“本场战斗开始时可用精灵总数”和“已被击败的精灵数量”来判断：
		// - 若所有精灵都已被击败，则直接判定战斗失败；
		// - 否则仍有后备精灵，交给前端弹出“换宠”面板。
		if battle.TotalPlayerPets <= 0 {
			// 兼容旧数据：若未初始化总数，则退回到 len(user.Pets) 判定
			battle.TotalPlayerPets = len(user.Pets)
		}
		// 当前这只精灵刚刚被击败，将其计入 DeadPlayerPets
		battle.DeadPlayerPets++
		remaining := battle.TotalPlayerPets - battle.DeadPlayerPets
		if remaining <= 0 {
			isOver = true
			winnerID = 0 // 敌人获胜
			logger.Info(fmt.Sprintf("[2405] 战斗结束: 敌人获胜（无可用后备精灵）"))
		} else {
			logger.Info(fmt.Sprintf("[2405] 当前精灵被击败，但玩家还有其它精灵可用，等待 2407 切换精灵 (Total=%d Dead=%d Remaining=%d)",
				battle.TotalPlayerPets, battle.DeadPlayerPets, remaining))
		}
	}

	if isOver {
		// 如果玩家获胜，给予经验奖励（勇者之塔、普通战斗均给；使用当前出战精灵 ActivePetIndex）
		if winnerID == uint32(ctx.UserID) && len(user.Pets) > 0 {
			petMgr := gamepets.GetInstance()
			enemyPet := petMgr.Get(battle.EnemyID)
			expGain := 50
			if enemyPet != nil && enemyPet.YieldingExp > 0 {
				expGain = enemyPet.YieldingExp
			}
			activeIdx := 0
			if battle.ActivePetIndex >= 0 && battle.ActivePetIndex < len(user.Pets) {
				activeIdx = battle.ActivePetIndex
			}
			active := &user.Pets[activeIdx]

			// Pet.Exp 语义：当前等级已获得经验（不是总经验）
			// 这里复用 2318（经验池分配）里的自动升级逻辑，避免经验始终为 0 的问题
			if active.Level <= 0 {
				active.Level = 1
			}
			if active.Level > 100 {
				active.Level = 100
				active.Exp = 0
			}

			oldLevel := active.Level
			oldExp := active.Exp

			remain := expGain
			for remain > 0 && active.Level < 100 {
				expInfo := petMgr.GetExpInfo(active.ID, active.Level, active.Exp)

				// 防御：如果 nextLevelExp 计算异常为 0，避免死循环，直接把剩余经验累加到当前等级
				if expInfo.NextLevelExp <= 0 {
					active.Exp += remain
					remain = 0
					break
				}

				need := expInfo.NextLevelExp - active.Exp
				if need > remain {
					// 本级别升不了级，只增加当前等级经验
					active.Exp += remain
					remain = 0
				} else {
					// 升级：扣除当前等级所需经验，等级+1，当前等级经验清零
					active.Exp = 0
					active.Level++
					remain -= need
					if active.Level >= 100 {
						active.Level = 100
						active.Exp = 0
						break
					}
				}
			}

			// 检查是否可以进化（只处理“直接进化”场景：配置里有 EvolvesTo，且不需要道具/进化舱）
			canEvolve, _, evolveTo := petMgr.CanEvolve(active.ID, active.Level, false)
			if canEvolve && evolveTo > 0 {
				logger.Info(fmt.Sprintf("[2405] 精灵进化触发: PetID %d -> %d (Level=%d)", active.ID, evolveTo, active.Level))
				active.ID = evolveTo
				// 进化后当前等级经验清零，重新开始积累
				active.Exp = 0
			}

			logger.Info(fmt.Sprintf(
				"[2405] 战斗胜利: 获得经验 %d (Level %d->%d, Exp %d->%d, PetID=%d)",
				expGain, oldLevel, active.Level, oldExp, active.Exp, active.ID,
			))

			// 副本奖励独立管控：勇者之塔(TowerLevel>0)、试炼之塔(FreshLevel>0) 已在本段上方按各自配置发放；
			// 以下仅处理 SPT BOSS 挑战、暗黑武斗场、谱尼、盖亚，互不交叉。
			// 仅当本场为「对应 BOSS 挑战」（2411/2421 发起，IsBossChallenge=true）时才发放该 BOSS 的 SPT 精元/精灵奖励
			if battle.IsBossChallenge {
				// 首次击败 SPT BOSS 奖励：按 sptboss 配置发放对应精灵（如蘑菇怪→小蘑菇）或精元（如里奥斯→里奥斯精元）
				if entry, ok := sptboss.GetByPetID(battle.EnemyID); ok && entry.RewardPetID > 0 {
					alreadyDefeated := false
					for _, id := range user.DefeatedSPTBossIds {
						if id == battle.EnemyID {
							alreadyDefeated = true
							break
						}
					}
					if !alreadyDefeated {
						user.DefeatedSPTBossIds = append(user.DefeatedSPTBossIds, battle.EnemyID)
						newCatchTime := int(time.Now().Unix())
						rand.Seed(time.Now().UnixNano() + int64(newCatchTime))
						newPet := userdb.Pet{
							ID:        entry.RewardPetID,
							CatchTime: newCatchTime,
							// 闪光波克尔奖励：80 级满个体
							Level: func() int {
								if entry.RewardPetID == 166 {
									return 80
								}
								return 1
							}(),
							DV: func() int {
								if entry.RewardPetID == 166 {
									return 31
								}
								return rand.Intn(32)
							}(),
							Nature: rand.Intn(25),
							Exp:    0,
							Name:   "",
						}
						// SPT 奖励精灵：先统一放入仓库，由前端 BossCmdListener 通过 2304 (PET_RELEASE)
						// 按 PetManager.setIn 捕获时间将其转入背包；这样与前端解包协议保持一致，
						// 避免直接写入背包导致 2304 查不到仓库记录、奖励精灵无法实时出现在精灵背包。
						if user.StoragePets == nil {
							user.StoragePets = []userdb.Pet{}
						}
						user.StoragePets = append(user.StoragePets, newPet)
						if ctx.GameServer.UserDB != nil {
							ctx.GameServer.UserDB.RecordCatch(ctx.UserID, entry.RewardPetID)
						}
						logger.Info(fmt.Sprintf("[2405] 首次击败 SPT BOSS PetID=%d，奖励精灵 PetID=%d CatchTime=%d", battle.EnemyID, entry.RewardPetID, newCatchTime))
						// 推送 CMD 8004 通知客户端弹窗显示获得精灵
						body8004 := buildBossMonster8004Body(0, uint32(entry.RewardPetID), uint32(newCatchTime), 0, 0)
						ctx.GameServer.SendResponse(ctx.ClientData, 8004, ctx.UserID, 0, body8004)
					}
				} else if entry, ok := sptboss.GetByPetID(battle.EnemyID); ok && entry.RewardItemID > 0 && battle.EnemyID != petIDGaiya {
					// 精元奖励（雷伊、纳多雷等）；盖亚(261)由 pushGaiyaRewardOrNotice 单独处理
					alreadyDefeated := false
					for _, id := range user.DefeatedSPTBossIds {
						if id == battle.EnemyID {
							alreadyDefeated = true
							break
						}
					}
					if !alreadyDefeated {
						user.DefeatedSPTBossIds = append(user.DefeatedSPTBossIds, battle.EnemyID)
						if user.Items == nil {
							user.Items = make(map[string]userdb.Item)
						}
						itemKey := strconv.Itoa(entry.RewardItemID)
						if it, has := user.Items[itemKey]; has {
							it.Count++
							user.Items[itemKey] = it
						} else {
							user.Items[itemKey] = userdb.Item{Count: 1, ExpireTime: 0x057E40}
						}
						logger.Info(fmt.Sprintf("[2405] 首次击败 SPT BOSS PetID=%d，奖励精元 ItemID=%d", battle.EnemyID, entry.RewardItemID))
						// 推送 CMD 8004 通知客户端弹窗显示获得精元
						body8004 := buildBossMonster8004Body(0, 0, 0, uint32(entry.RewardItemID), 1)
						ctx.GameServer.SendResponse(ctx.ClientData, 8004, ctx.UserID, 0, body8004)
					}
				} else if _, ok := sptboss.GetByPetID(battle.EnemyID); ok {
					// SPT BOSS 无奖励精灵/精元，仅记录击败成就
					alreadyDefeated := false
					for _, id := range user.DefeatedSPTBossIds {
						if id == battle.EnemyID {
							alreadyDefeated = true
							break
						}
					}
					if !alreadyDefeated {
						user.DefeatedSPTBossIds = append(user.DefeatedSPTBossIds, battle.EnemyID)
					}
				}
			} else if battle.IsDarkPortalBattle {
				// 仅当本场战斗由暗黑武斗场(2425)发起时才发放精元/精灵，避免在其他地图击败同 ID 精灵误发奖励
				if rewardItemID, rewardPetID, isDarkPortalBoss := GetDarkPortalBossReward(uint32(battle.EnemyID)); isDarkPortalBoss {
					// 暗黑武斗场 BOSS 奖励（优先使用数据库配置）
					alreadyDefeated := false
					for _, id := range user.DefeatedSPTBossIds {
						if id == battle.EnemyID {
							alreadyDefeated = true
							break
						}
					}
					if !alreadyDefeated {
						// 奖励精灵
						if rewardPetID > 0 {
							user.DefeatedSPTBossIds = append(user.DefeatedSPTBossIds, battle.EnemyID)
							newCatchTime := int(time.Now().Unix())
							rand.Seed(time.Now().UnixNano() + int64(newCatchTime))
							newPet := userdb.Pet{
								ID:        rewardPetID,
								CatchTime: newCatchTime,
								Level:     1,
								DV:        rand.Intn(32),
								Nature:    rand.Intn(25),
								Exp:       0,
								Name:      "",
							}
							// 暗黑武斗场奖励精灵也统一先进入仓库，前端通过 2304 (PET_RELEASE)
							// 根据精灵捕捉时间决定是否放入背包或保留在仓库，保证与原版前端解包/交互一致。
							if user.StoragePets == nil {
								user.StoragePets = []userdb.Pet{}
							}
							user.StoragePets = append(user.StoragePets, newPet)
							if ctx.GameServer.UserDB != nil {
								ctx.GameServer.UserDB.RecordCatch(ctx.UserID, rewardPetID)
							}
							logger.Info(fmt.Sprintf("[2405] 首次击败暗黑武斗场 BOSS PetID=%d，奖励精灵 PetID=%d CatchTime=%d", battle.EnemyID, rewardPetID, newCatchTime))
							body8004 := buildBossMonster8004Body(0, uint32(rewardPetID), uint32(newCatchTime), 0, 0)
							ctx.GameServer.SendResponse(ctx.ClientData, 8004, ctx.UserID, 0, body8004)
						} else if rewardItemID > 0 {
							// 奖励精元
							if user.Items == nil {
								user.Items = make(map[string]userdb.Item)
							}
							itemKey := strconv.Itoa(rewardItemID)
							hasSoul := false
							hasPet := false
							
							// 检查背包中是否已有该精元物品
							if _, has := user.Items[itemKey]; has {
								hasSoul = true
							}
							
							// 检查背包中是否已有对应的精灵
							if soulRewardPetID, ok := darkPortalSoulRewardPetIDs[rewardItemID]; ok {
								// 检查背包中的精灵
								for _, pet := range user.Pets {
									if pet.ID == soulRewardPetID {
										hasPet = true
										break
									}
								}
								// 检查仓库中的精灵
								if !hasPet && user.StoragePets != nil {
									for _, pet := range user.StoragePets {
										if pet.ID == soulRewardPetID {
											hasPet = true
											break
										}
									}
								}
							}
							
							// 如果背包中已有精元或对应的精灵，则不给予奖励，但仍记录击败成就
							if !hasSoul && !hasPet {
								user.DefeatedSPTBossIds = append(user.DefeatedSPTBossIds, battle.EnemyID)
								user.Items[itemKey] = userdb.Item{Count: 1, ExpireTime: 0x057E40}
								logger.Info(fmt.Sprintf("[2405] 首次击败暗黑武斗场 BOSS PetID=%d，奖励精元 ItemID=%d", battle.EnemyID, rewardItemID))
								// 推送 CMD 8004 通知客户端弹窗显示获得精元
								body8004 := buildBossMonster8004Body(0, 0, 0, uint32(rewardItemID), 1)
								ctx.GameServer.SendResponse(ctx.ClientData, 8004, ctx.UserID, 0, body8004)
							} else {
								// 背包中已有精元或对应的精灵，记录击败成就但不给予奖励
								user.DefeatedSPTBossIds = append(user.DefeatedSPTBossIds, battle.EnemyID)
								reason := ""
								if hasSoul {
									reason = "已有精元"
								} else if hasPet {
									reason = "已有对应精灵"
								}
								logger.Info(fmt.Sprintf("[2405] 击败暗黑武斗场 BOSS PetID=%d，但%s，不给予精元奖励 ItemID=%d", battle.EnemyID, reason, rewardItemID))
							}
						} else {
							// 无奖励，仅记录击败成就
							user.DefeatedSPTBossIds = append(user.DefeatedSPTBossIds, battle.EnemyID)
							logger.Info(fmt.Sprintf("[2405] 击败暗黑武斗场 BOSS PetID=%d，无奖励", battle.EnemyID))
						}
					}
				} else if _, ok := darkPortalBossRewards[uint32(battle.EnemyID)]; ok {
					// 兼容旧代码：使用代码中的配置
					// 暗黑武斗场 BOSS 无奖励（如第十门、第十一门），仅记录击败成就
					alreadyDefeated := false
					for _, id := range user.DefeatedSPTBossIds {
						if id == battle.EnemyID {
							alreadyDefeated = true
							break
						}
					}
					if !alreadyDefeated {
						user.DefeatedSPTBossIds = append(user.DefeatedSPTBossIds, battle.EnemyID)
					}
				}
			}

			// 谱尼七封印/真身：战斗胜利后在服务端更新 MaxPuniLv 进度，使其与前端解包协议与解锁逻辑一致，
			// 并发放对应门的“谱尼裂片”道具；集齐 1~7 号裂片后自动合成「谱尼的精元」(400150)。
			if (battle.BattleMapID == 108 || battle.BattleMapID == 514) && battle.EnemyID == 300 && winnerID == uint32(ctx.UserID) {
				door := battle.PuniDoorIndex
				if door <= 0 {
					// 未记录门索引时（如早期数据），按第一封印处理
					door = 1
				}

				// 1）更新进度 MaxPuniLv（0~8）
				targetLv := user.MaxPuniLv
				if door >= 1 && door <= 7 {
					if targetLv < door {
						targetLv = door
					}
				} else if door >= 8 {
					// 真身视为第 8 阶段进度，需在解锁前七封印后挑战
					if targetLv < 8 {
						targetLv = 8
					}
				}
				// 协议允许范围 0~8，其余视为 0
				if targetLv < 0 {
					targetLv = 0
				}
				if targetLv > 8 {
					targetLv = 8
				}
				if targetLv != user.MaxPuniLv {
					logger.Info(fmt.Sprintf("[2405] 谱尼战胜利: 更新 MaxPuniLv %d -> %d (door=%d)", user.MaxPuniLv, targetLv, door))
					user.MaxPuniLv = targetLv
				}

				// 2）发放对应门的谱尼裂片（1~8 每门一片，背包最多各 1 个，从配置读取）
				if user.Items == nil {
					user.Items = make(map[string]userdb.Item)
				}
				var fragItemID int
				if cfg, ok := sptboss.GetPuniSealConfig(door); ok && cfg.RewardItemID > 0 {
					fragItemID = cfg.RewardItemID
				} else if defaultFrag, ok := puniFragmentItemIDs[door]; ok {
					fragItemID = defaultFrag // 配置不存在时使用内置默认值（向后兼容）
				}
				if fragItemID > 0 {
					fragKey := strconv.Itoa(fragItemID)
					if it, has := user.Items[fragKey]; has {
						// items.xml 中碎片 Max=1：若已有则保持 1，不再累加
						if it.Count <= 0 {
							it.Count = 1
							user.Items[fragKey] = it
						}
					} else {
						user.Items[fragKey] = userdb.Item{Count: 1, ExpireTime: 0x057E40}
					}
					logger.Info(fmt.Sprintf("[2405] 谱尼战胜利: 发放碎片 ItemID=%d (door=%d)", fragItemID, door))
				}

			}

			// 保存数据
			if ctx.GameServer.UserDB != nil {
				ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
			}

			// 发送 NOTE_UPDATE_PROP(2508)，驱动“战斗结束属性结算面板”（与经验一致使用出战精灵）
			activePet := &user.Pets[activeIdx]
			ev := gamepets.ClampAndCapEV(activePet.GetEVStats())
			stats := petMgr.GetStats(activePet.ID, activePet.Level, activePet.DV, ev, activePet.Nature)
			propBody := buildNoteUpdateProp(uint32(activePet.CatchTime), activePet.ID, activePet.Level, activePet.Exp,
				stats.MaxHP, stats.Attack, stats.Defence, stats.SpAtk, stats.SpDef, stats.Speed, ev)
			ctx.GameServer.SendResponse(ctx.ClientData, 2508, ctx.UserID, ctx.SeqID, propBody)
		}

		// 勇者之塔多精灵战斗：本层还有下一只时不发 2506（不弹胜利），直接切换下一只并推 2503，客户端在同一场战斗内连续打多只
		if battle.TowerLevel > 0 && winnerID == uint32(ctx.UserID) {
			bossIDs := GetFightLevelBossIDsForLevel(battle.TowerLevel)
			if battle.TowerBossIndex+1 < len(bossIDs) {
				user.TowerBossIndex = battle.TowerBossIndex + 1
				if ctx.GameServer.UserDB != nil {
					ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
				}
				nextIdx := battle.TowerBossIndex + 1
				nextBossID := bossIDs[nextIdx]
				enemyLevel := 10 + battle.TowerLevel
				if enemyLevel < 1 {
					enemyLevel = 1
				}
				if entry, ok := GetFightLevelEntry(battle.TowerLevel); ok && entry.EnemyLv > 0 {
					enemyLevel = entry.EnemyLv
				}
				// 多精灵敌方切换：只发 2407+2505（不发 2503/2504），与试炼之塔共用 pushMultiEnemySwitch2504_2505
				pushMultiEnemySwitch2504_2505(ctx, battle, user, nextIdx, nextBossID, enemyLevel, true, "勇者之塔")
				return
			}
			// 本层最后一只已击败，进阶层数
			user.CurStage = battle.TowerLevel + 1
			user.TowerBossIndex = 0
			if user.MaxStage < user.CurStage {
				user.MaxStage = user.CurStage
			}
			if ctx.GameServer.UserDB != nil {
				ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
			}
			logger.Info(fmt.Sprintf("[勇者之塔] 战斗胜利: userID=%d 层=%d -> CurStage=%d MaxStage=%d", ctx.UserID, battle.TowerLevel, user.CurStage, user.MaxStage))
			// 勇者之塔副本奖励独立管控：仅按本层配置发放经验、赛尔豆、道具/装备、精灵，与 SPT/暗黑互斥
			if entry, ok := GetFightLevelEntry(battle.TowerLevel); ok && (entry.RewardExp > 0 || entry.RewardCoins > 0 || entry.RewardPetID > 0 || entry.RewardItemID > 0) {
				if entry.RewardExp > 0 {
					if user.ExpPool < 0 {
						user.ExpPool = 0
					}
					user.ExpPool += entry.RewardExp
					logger.Info(fmt.Sprintf("[2405] 勇者之塔 层=%d 奖励经验 +%d 经验池=%d", battle.TowerLevel, entry.RewardExp, user.ExpPool))
				}
				if entry.RewardCoins > 0 {
					user.Coins += entry.RewardCoins
					logger.Info(fmt.Sprintf("[2405] 勇者之塔 层=%d 奖励赛尔豆 +%d", battle.TowerLevel, entry.RewardCoins))
				}
				if entry.RewardPetID > 0 {
					newCatchTime := int(time.Now().Unix())
					rand.Seed(time.Now().UnixNano() + int64(newCatchTime))
					newPet := userdb.Pet{
						ID: entry.RewardPetID, CatchTime: newCatchTime, Level: 1, DV: rand.Intn(32), Nature: rand.Intn(25), Exp: 0, Name: "",
					}
					if user.StoragePets == nil {
						user.StoragePets = []userdb.Pet{}
					}
					user.StoragePets = append(user.StoragePets, newPet)
					if ctx.GameServer.UserDB != nil {
						ctx.GameServer.UserDB.RecordCatch(ctx.UserID, entry.RewardPetID)
					}
					body8004 := buildBossMonster8004Body(0, uint32(entry.RewardPetID), uint32(newCatchTime), 0, 0)
					ctx.GameServer.SendResponse(ctx.ClientData, 8004, ctx.UserID, 0, body8004)
					logger.Info(fmt.Sprintf("[2405] 勇者之塔 层=%d 奖励精灵 PetID=%d", battle.TowerLevel, entry.RewardPetID))
				}
				if entry.RewardItemID > 0 {
					if user.Items == nil {
						user.Items = make(map[string]userdb.Item)
					}
					itemKey := strconv.Itoa(entry.RewardItemID)
					if it, has := user.Items[itemKey]; has {
						it.Count++
						user.Items[itemKey] = it
					} else {
						user.Items[itemKey] = userdb.Item{Count: 1, ExpireTime: 0x057E40}
					}
					body8004 := buildBossMonster8004Body(0, 0, 0, uint32(entry.RewardItemID), 1)
					ctx.GameServer.SendResponse(ctx.ClientData, 8004, ctx.UserID, 0, body8004)
					logger.Info(fmt.Sprintf("[2405] 勇者之塔 层=%d 奖励道具/装备 ItemID=%d", battle.TowerLevel, entry.RewardItemID))
				}
				if ctx.GameServer.UserDB != nil {
					ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
				}
			}
		} else if battle.FreshLevel > 0 && winnerID == uint32(ctx.UserID) {
			// 试炼之塔多精灵：与勇者之塔相同协议（开局 2503 含本层全部 Boss catchTime=0,1,2...，切换只发 2407+2505）
			bossIDs := GetFreshFightBossIDsForLevel(battle.FreshLevel)
			if battle.FreshBossIndex+1 < len(bossIDs) {
				user.CurFreshStage = battle.FreshLevel // 确保进度
				if ctx.GameServer.UserDB != nil {
					ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
				}
				nextIdx := battle.FreshBossIndex + 1
				nextBossID := bossIDs[nextIdx]
				enemyLevel := 10 + battle.FreshLevel
				if enemyLevel < 1 {
					enemyLevel = 1
				}
				pushMultiEnemySwitch2504_2505(ctx, battle, user, nextIdx, nextBossID, enemyLevel, false, "试炼之塔")
				return
			}
			// 试炼之塔本层最后一只已击败
			if ctx.GameServer.UserDB != nil {
				ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
			}
			logger.Info(fmt.Sprintf("[试炼之塔] 战斗胜利: userID=%d 层=%d", ctx.UserID, battle.FreshLevel))
			// 试炼之塔副本奖励独立管控：仅按本层配置发放，与 SPT/暗黑/勇者之塔互斥
			if rewardItemID, rewardPetID := GetFreshFightRewardForLevel(battle.FreshLevel); rewardPetID > 0 || rewardItemID > 0 {
				if rewardPetID > 0 {
					newCatchTime := int(time.Now().Unix())
					rand.Seed(time.Now().UnixNano() + int64(newCatchTime))
					newPet := userdb.Pet{
						ID: rewardPetID, CatchTime: newCatchTime, Level: 1, DV: rand.Intn(32), Nature: rand.Intn(25), Exp: 0, Name: "",
					}
					if user.StoragePets == nil {
						user.StoragePets = []userdb.Pet{}
					}
					user.StoragePets = append(user.StoragePets, newPet)
					if ctx.GameServer.UserDB != nil {
						ctx.GameServer.UserDB.RecordCatch(ctx.UserID, rewardPetID)
					}
					body8004 := buildBossMonster8004Body(0, uint32(rewardPetID), uint32(newCatchTime), 0, 0)
					ctx.GameServer.SendResponse(ctx.ClientData, 8004, ctx.UserID, 0, body8004)
					logger.Info(fmt.Sprintf("[2405] 试炼之塔 层=%d 奖励精灵 PetID=%d", battle.FreshLevel, rewardPetID))
				}
				if rewardItemID > 0 {
					if user.Items == nil {
						user.Items = make(map[string]userdb.Item)
					}
					itemKey := strconv.Itoa(rewardItemID)
					if it, has := user.Items[itemKey]; has {
						it.Count++
						user.Items[itemKey] = it
					} else {
						user.Items[itemKey] = userdb.Item{Count: 1, ExpireTime: 0x057E40}
					}
					body8004 := buildBossMonster8004Body(0, 0, 0, uint32(rewardItemID), 1)
					ctx.GameServer.SendResponse(ctx.ClientData, 8004, ctx.UserID, 0, body8004)
					logger.Info(fmt.Sprintf("[2405] 试炼之塔 层=%d 奖励精元 ItemID=%d", battle.FreshLevel, rewardItemID))
				}
				if ctx.GameServer.UserDB != nil {
					ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
				}
			}
		}

		// 勇者之塔本层胜利后：先推 2414(下一层) 再推 2506，使塔界面显示本层精灵而非刚击败的精灵
		// 2414 使用与 2506 相同的 ctx.SeqID，便于客户端在战斗结束流程中应用选层更新；再补发一条 seq=0 的 2414 兜底
		if battle.TowerLevel > 0 && battle.BattleMapID == 500 {
			nextLevel := user.CurStage
			if nextLevel <= 0 {
				nextLevel = 1
			}
			if nextLevel > fightLevelMaxLevel {
				nextLevel = fightLevelMaxLevel
			}
			nextBossIDs := GetFightLevelBossIDsForLevel(nextLevel)
			if len(nextBossIDs) > 0 {
				body2414 := make([]byte, 8+4*len(nextBossIDs))
				binary.BigEndian.PutUint32(body2414[0:4], uint32(nextLevel))
				binary.BigEndian.PutUint32(body2414[4:8], uint32(len(nextBossIDs)))
				for i, id := range nextBossIDs {
					binary.BigEndian.PutUint32(body2414[8+i*4:8+(i+1)*4], uint32(id))
				}
				ctx.GameServer.SendResponse(ctx.ClientData, 2414, ctx.UserID, ctx.SeqID, body2414)
				ctx.GameServer.SendResponse(ctx.ClientData, 2414, ctx.UserID, 0, body2414)
				logger.Info(fmt.Sprintf("[2414] 勇者之塔战后推送下一层(先于2506): curLevel=%d bossIDs=%v seq=%d+广播", nextLevel, nextBossIDs, ctx.SeqID))
			}
		}
		overBody := buildFightOverInfo(0, winnerID)
		ctx.GameServer.SendResponse(ctx.ClientData, 2506, ctx.UserID, ctx.SeqID, overBody)
		pushMapOgreListAfterFightOver(ctx, battle.BattleMapID == 500)
		// 盖亚（261）在三地图按周几+条件才给精元（400126）；否则推送 SPRINT_GIFT_NOTICE(8010) 提示未按规则
		if battle.EnemyID == 261 && winnerID == uint32(ctx.UserID) {
			pushGaiyaRewardOrNotice(ctx, battle, user)
		}
		// 盖亚战结束后：若当前在当日盖亚地图，重发 2022 使盖亚仍显示在地图中，无需重新进图
		if battle.EnemyID == petIDGaiya {
			gaiyaMap := getGaiyaMapIDForToday()
			if user.MapID != 0 && user.MapID == gaiyaMap {
				gaiyaNote := make([]byte, 8)
				binary.BigEndian.PutUint32(gaiyaNote[0:4], 1)
				binary.BigEndian.PutUint32(gaiyaNote[4:8], petIDGaiya)
				ctx.GameServer.SendResponse(ctx.ClientData, cmdSpecialPetNote, ctx.UserID, 0, gaiyaNote)
				logger.Info(fmt.Sprintf("[2022] 盖亚战后重推盖亚出场: UID=%d MapID=%d", ctx.UserID, user.MapID))
			}
		}
		// PvP：向对方也发送 2506 并清理状态，否则对方对战不退出
		opponentUID := battle.OpponentUserID
		ctx.GameServer.BattleMu.Lock()
		delete(ctx.GameServer.BattleStates, ctx.UserID)
		if opponentUID != 0 {
			delete(ctx.GameServer.BattleStates, opponentUID)
		}
		ctx.GameServer.BattleMu.Unlock()
		if opponentUID != 0 {
			if otherClient := ctx.GameServer.GetClientByUserID(opponentUID); otherClient != nil {
				ctx.GameServer.SendResponse(otherClient, 2506, opponentUID, 0, overBody)
				userOpp := ctx.GameServer.GetOrCreateUser(opponentUID)
				mapID := userOpp.MapID
				if mapID == 0 {
					mapID = 1
				}
				ctx.GameServer.SetOgreFightEndTime(opponentUID)
				ctx.GameServer.ClearPlayerOgreSlots(opponentUID, mapID)
				ogreBody := ctx.GameServer.BuildMapOgreListFromSlots(nil)
				ctx.GameServer.SendResponse(otherClient, 2004, opponentUID, 0, ogreBody)
			}
		}
	}
}

// handleUsePetItem CMD 2406 使用道具（最小可用版本）
// 对齐 Lua: fight_handlers.handleUsePetItem
func handleUsePetItem(ctx *gameserver.HandlerContext) {
	var itemID uint32
	if len(ctx.Body) >= 8 {
		_ = binary.BigEndian.Uint32(ctx.Body[0:4]) // catchTime，目前逻辑中未使用
		itemID = binary.BigEndian.Uint32(ctx.Body[4:8])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// 检查道具是否存在
	itemKey := strconv.FormatUint(uint64(itemID), 10)
	item, hasItem := user.Items[itemKey]
	if !hasItem || item.Count <= 0 {
		// 错误响应：errorCode = 10301（道具不足）
		ctx.GameServer.SendResponse(ctx.ClientData, 2406, ctx.UserID, ctx.SeqID, []byte{})
		return
	}

	// 扣除道具
	item.Count--
	if item.Count <= 0 {
		delete(user.Items, itemKey)
	} else {
		user.Items[itemKey] = item
	}

	// 简化：统一按 30 点固定体力药处理（可按 itemID 精细化）
	healHP := int32(30)

	// 在战斗中优先使用 BattleState 的 HP；否则退回到宠物当前 HP
	currentHP := int32(0)
	maxHP := uint32(0)

	ctx.GameServer.BattleMu.Lock()
	battle, inBattle := ctx.GameServer.BattleStates[ctx.UserID]
	if inBattle && battle.IsActive {
		currentHP = int32(battle.PlayerHP)
		maxHP = battle.PlayerMaxHP
	} else if len(user.Pets) > 0 {
		petMgr := gamepets.GetInstance()
		// 默认使用首发精灵（与战斗外逻辑一致）
		petNature := user.Pets[0].Nature
		petEV := user.Pets[0].GetEVStats()
		petStats := petMgr.GetStats(user.Pets[0].ID, user.Pets[0].Level, user.Pets[0].DV, petEV, petNature)
		currentHP = int32(petStats.HP)
		maxHP = uint32(petStats.MaxHP)
	}

	if maxHP > 0 {
		if currentHP+healHP > int32(maxHP) {
			healHP = int32(maxHP) - currentHP
			currentHP = int32(maxHP)
		} else {
			currentHP += healHP
		}
	}

	// 回写战斗中的 HP
	if inBattle && battle.IsActive {
		if currentHP < 0 {
			currentHP = 0
		}
		battle.PlayerHP = uint32(currentHP)
		battle.PlayerMaxHP = maxHP
	}
	ctx.GameServer.BattleMu.Unlock()

	// 响应：userId(4) + itemId(4) + hp(4) + changeHp(4)
	body := make([]byte, 16)
	binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID))
	binary.BigEndian.PutUint32(body[4:8], itemID)
	binary.BigEndian.PutUint32(body[8:12], uint32(currentHP))
	// changeHp 是有符号整数，需要转换
	if healHP < 0 {
		binary.BigEndian.PutUint32(body[12:16], uint32(math.MaxUint32+int64(healHP)+1))
	} else {
		binary.BigEndian.PutUint32(body[12:16], uint32(healHP))
	}

	ctx.GameServer.SendResponse(ctx.ClientData, 2406, ctx.UserID, ctx.SeqID, body)

	// 保存数据
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	// 若在战斗中，使用 HP 药也视为本回合行动，立刻结算敌方一次攻击（与 2407 切换精灵一致）
	if inBattle && battle.IsActive {
		petMgr := gamepets.GetInstance()

		ctx.GameServer.BattleMu.Lock()
		curBattle, ok := ctx.GameServer.BattleStates[ctx.UserID]
		if !ok || !curBattle.IsActive {
			ctx.GameServer.BattleMu.Unlock()
			return
		}

		skillMgr := gameskills.GetInstance()
		enemySkill, enemySkillID := pickEnemySkill(skillMgr, curBattle.EnemyID, curBattle.EnemyLevel)
		if enemySkill == nil || enemySkillID == 0 {
			// 敌人本回合不出招
			ctx.GameServer.BattleMu.Unlock()
			return
		}

		// 控制类异常：畏缩/睡眠/麻痹 时敌方本回合无法行动（与 2405 一致）
		enemyFlinched := false
		if curBattle.EnemyStatus[gameskills.StatusIndexFear] > 0 {
			enemyFlinched = true
			curBattle.EnemyStatus[gameskills.StatusIndexFear]--
		}
		if curBattle.EnemyStatus[gameskills.StatusIndexSleep] > 0 {
			enemyFlinched = true
			curBattle.EnemyStatus[gameskills.StatusIndexSleep]--
		}
		enemyParalyzed := false
		if curBattle.EnemyStatus[gameskills.StatusIndexParalysis] > 0 {
			enemyParalyzed = true
			curBattle.EnemyStatus[gameskills.StatusIndexParalysis]--
		}
		if enemyFlinched || enemyParalyzed {
			enemySkillID = 0
		}

		enemyDamageCalc := uint32(0)
		enemyDamage := uint32(0)
		if !enemyFlinched && !enemyParalyzed {
			// 敌人属性
			enemyEV := gamepets.EVStats{}
			enemyStats := petMgr.GetStats(curBattle.EnemyID, curBattle.EnemyLevel, 15, enemyEV, 0)

			// 我方当前出战精灵完整属性（用于防御端与属性克制），与 2405 一致
			activeIdx := 0
			if curBattle.ActivePetIndex > 0 && curBattle.ActivePetIndex < len(user.Pets) {
				activeIdx = curBattle.ActivePetIndex
			}
			playerPetID := 7
			playerLevel := 5
			playerDV := 31
			playerNature := 0
			playerEVForDef := gamepets.EVStats{}
			if len(user.Pets) > 0 {
				playerPetID = user.Pets[activeIdx].ID
				if user.Pets[activeIdx].Level > 0 {
					playerLevel = user.Pets[activeIdx].Level
				}
				playerDV = user.Pets[activeIdx].DV
				playerNature = user.Pets[activeIdx].Nature
				playerEVForDef = user.Pets[activeIdx].GetEVStats()
			}
			playerStatsForDef := petMgr.GetStats(playerPetID, playerLevel, playerDV, playerEVForDef, playerNature)

			enemyPower := uint32(enemySkill.Power)
			if enemyPower == 0 {
				enemyPower = 40
			}

			// 敌方攻防（按技能类别），并应用强化弱化倍率
			enemyAtk := float64(enemyStats.Attack)
			enemyDef := float64(playerStatsForDef.Defence)
			enemyAtkStage, enemyDefStage := 0, 1
			if enemySkill.Category == 2 {
				enemyAtk = float64(enemyStats.SpAtk)
				enemyDef = float64(playerStatsForDef.SpDef)
				enemyAtkStage, enemyDefStage = 2, 3
			}
			enemyAtk *= gamebattle.GetStatMultiplier(int(curBattle.EnemyBattleLv[enemyAtkStage]))
			enemyDef *= gamebattle.GetStatMultiplier(int(curBattle.PlayerBattleLv[enemyDefStage]))
			if enemyDef < 1 {
				enemyDef = 1
			}

			enemyBaseDamage := math.Floor(((float64(curBattle.EnemyLevel)*0.4 + 2.0) * float64(enemyPower) * enemyAtk / enemyDef / 50.0) + 2.0)

			enemyStab := 1.0
			if enemyPetDef := petMgr.Get(curBattle.EnemyID); enemyPetDef != nil && (enemySkill.Type == enemyPetDef.Type || (enemyPetDef.Type2 > 0 && enemySkill.Type == enemyPetDef.Type2)) {
				enemyStab = 1.5
			}

			enemyTypeMod := 1.0
			if len(user.Pets) > 0 {
				if attackerPetDef := petMgr.Get(user.Pets[activeIdx].ID); attackerPetDef != nil {
					enemyTypeMod = gamebattle.GetTypeMultiplierDual(enemySkill.Type, attackerPetDef.Type, attackerPetDef.Type2)
				}
			}

			enemyRandomMod := float64(rand.Intn(255-217+1)+217) / 255.0
			enemyFinalDamage := uint32(enemyBaseDamage * enemyStab * enemyTypeMod * enemyRandomMod)
			if curBattle.EnemyStatus[gameskills.StatusIndexBurn] > 0 {
				enemyFinalDamage = enemyFinalDamage / 2
				if enemyFinalDamage < 1 {
					enemyFinalDamage = 1
				}
			}
			if enemyFinalDamage < 1 {
				enemyFinalDamage = 1
			}
			enemyDamageCalc = enemyFinalDamage // 理论伤害，用于客户端显示
			if enemyFinalDamage > curBattle.PlayerHP {
				enemyFinalDamage = curBattle.PlayerHP
			}

			enemyDamage = enemyFinalDamage

			// 更新玩家 HP
			curBattle.PlayerHP -= enemyDamage
			if curBattle.PlayerHP > curBattle.PlayerMaxHP {
				curBattle.PlayerHP = 0
			}
		}

		opponentUID := curBattle.OpponentUserID
		enemyStatusFor2505 := curBattle.EnemyStatus // 敌方异常状态和强化弱化跟随精灵，需正确下发
		enemyBattleLvFor2505 := curBattle.EnemyBattleLv
		ctx.GameServer.BattleMu.Unlock()

		// 构造 2505，通知前端敌人攻击一次（status/battleLv 用敌方实际值）
		body2505 := make([]byte, 0, 80)
		enemyUserID := uint32(0)
		if opponentUID != 0 {
			enemyUserID = uint32(opponentUID)
		}

		body2505 = append(body2505, buildAttackValue(
			enemyUserID, enemySkillID, 1,
			enemyDamageCalc, 0,
			int32(curBattle.EnemyHP), curBattle.EnemyMaxHP,
			0, 0, 0,
			enemyStatusFor2505, enemyBattleLvFor2505,
		)...)

		ctx.GameServer.SendResponse(ctx.ClientData, 2505, ctx.UserID, ctx.SeqID, body2505)
		if opponentUID != 0 {
			if otherClient := ctx.GameServer.GetClientByUserID(opponentUID); otherClient != nil {
				ctx.GameServer.SendResponse(otherClient, 2505, opponentUID, 0, body2505)
			}
		}
	}
}

// buildNoteUpdateProp 构建 CMD 2508 NOTE_UPDATE_PROP
// 对齐前端解析：
// PetUpdatePropInfo:
//
//	addition(4) + count(4) + UpdatePropInfo * count
//
// UpdatePropInfo:
//
//	catchTime(4) + id(4) + level(4) +
//	exp(4) + currentLvExp(4) + nextLvExp(4) +   // exp=总经验, currentLvExp=当前等级经验
//	maxHp(4) + atk(4) + def(4) + sa(4) + sd(4) + sp(4) +
//	ev_hp(4) + ev_a(4) + ev_d(4) + ev_sa(4) + ev_sd(4) + ev_sp(4)
func buildNoteUpdateProp(catchTime uint32, petID int, level int, currentLevelExp int, maxHP int, attack, defence, spAtk, spDef, speed int, ev gamepets.EVStats) []byte {
	petMgr := gamepets.GetInstance()
	expInfo := petMgr.GetExpInfo(petID, level, currentLevelExp)

	addition := uint32(0) // /100 为加成倍率，先按 0（无额外加成）
	count := uint32(1)

	body := make([]byte, 0, 8+72)
	putU32 := func(v uint32) {
		tmp := make([]byte, 4)
		binary.BigEndian.PutUint32(tmp, v)
		body = append(body, tmp...)
	}

	// PetUpdatePropInfo header
	putU32(addition)
	putU32(count)

	// UpdatePropInfo
	putU32(catchTime)
	putU32(uint32(petID))
	putU32(uint32(level))
	// 与 2301 保持语义一致：
	// - exp: 当前等级已获得经验（与 2301 一致，避免经验分配器“升级所需经验值”为负）
	// - currentLvExp: 当前等级已获得经验
	// - nextLvExp: 升级到下一等级所需经验
	putU32(uint32(expInfo.CurrentLevelExp)) // exp（当前等级经验）
	putU32(uint32(expInfo.CurrentLevelExp)) // currentLvExp（当前等级经验）
	putU32(uint32(expInfo.NextLevelExp))    // nextLvExp
	putU32(uint32(maxHP))
	putU32(uint32(attack))
	putU32(uint32(defence))
	putU32(uint32(spAtk))
	putU32(uint32(spDef))
	putU32(uint32(speed))
	putU32(uint32(ev.HP))
	putU32(uint32(ev.Atk))
	putU32(uint32(ev.Def))
	putU32(uint32(ev.SpAtk))
	putU32(uint32(ev.SpDef))
	putU32(uint32(ev.Spd))

	return body
}

// buildNoteUpdateSkill 构建 CMD 2507 NOTE_UPDATE_SKILL
// 对齐前端 PetUpdateSkillInfo + UpdateSkillInfo 解析：
//
//	PetUpdateSkillInfo: count(4) + [UpdateSkillInfo]*count
//	UpdateSkillInfo: catchTime(4) + activeCount(4) + unactiveCount(4) + [activeId(4)]*activeCount + [unactiveId(4)]*unactiveCount
//
// activeSkills=当前技能(可被替换)，unactiveSkills=新学会的技能
func buildNoteUpdateSkill(catchTime uint32, petID int, oldLevel int, newSkillIDs []int) []byte {
	petMgr := gamepets.GetInstance()
	currentSkills := petMgr.GetSkillsForLevel(petID, oldLevel)
	var activeIDs []int
	for _, sid := range currentSkills {
		if sid > 0 {
			activeIDs = append(activeIDs, sid)
		}
	}
	body := make([]byte, 0, 20+len(activeIDs)*4+len(newSkillIDs)*4)
	putU32 := func(v uint32) {
		tmp := make([]byte, 4)
		binary.BigEndian.PutUint32(tmp, v)
		body = append(body, tmp...)
	}
	putU32(1) // count=1，一个 UpdateSkillInfo
	putU32(catchTime)
	putU32(uint32(len(activeIDs)))
	putU32(uint32(len(newSkillIDs)))
	for _, id := range activeIDs {
		putU32(uint32(id))
	}
	for _, id := range newSkillIDs {
		putU32(uint32(id))
	}
	return body
}

// handleChangePet CMD 2407 切换精灵
// 对齐 Lua: fight_handlers.handleChangePet
func handleChangePet(ctx *gameserver.HandlerContext) {
	var reqCatchTime uint32
	if len(ctx.Body) >= 4 {
		reqCatchTime = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	logger.Info(fmt.Sprintf("[2407] 收到切换精灵请求: reqCatchTime=%d", reqCatchTime))

	// 盖亚(261)对战：仅允许单精灵，禁止切换
	ctx.GameServer.BattleMu.RLock()
	if battle, ok := ctx.GameServer.BattleStates[ctx.UserID]; ok && battle.IsActive && battle.EnemyID == petIDGaiya {
		ctx.GameServer.BattleMu.RUnlock()
		logger.Warning("[2407] 盖亚对战不允许切换精灵")
		errorBody := make([]byte, 8)
		binary.BigEndian.PutUint32(errorBody[0:4], uint32(ctx.UserID))
		binary.BigEndian.PutUint32(errorBody[4:8], 2) // 错误码：盖亚单精灵不可切换
		ctx.GameServer.SendResponse(ctx.ClientData, 2407, ctx.UserID, ctx.SeqID, errorBody)
		return
	}
	ctx.GameServer.BattleMu.RUnlock()

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// 打印所有精灵信息
	logger.Info(fmt.Sprintf("[2407] 玩家精灵列表: 共%d只", len(user.Pets)))
	for i, pet := range user.Pets {
		logger.Info(fmt.Sprintf("[2407]   [%d] PetID=%d CatchTime=%d Level=%d", i, pet.ID, pet.CatchTime, pet.Level))
	}

	// 查找精灵
	var picked *userdb.Pet
	var pickedIndex int = -1
	if reqCatchTime != 0 {
		for i := range user.Pets {
			if uint32(user.Pets[i].CatchTime) == reqCatchTime {
				picked = &user.Pets[i]
				pickedIndex = i
				logger.Info(fmt.Sprintf("[2407] 找到匹配的精灵: index=%d PetID=%d", i, user.Pets[i].ID))
				break
			}
		}
	}
	if picked == nil && len(user.Pets) > 0 {
		picked = &user.Pets[0]
		pickedIndex = 0
		logger.Info(fmt.Sprintf("[2407] 未找到匹配精灵，使用第一只: PetID=%d", user.Pets[0].ID))
	}

	if picked == nil {
		// 错误响应（使用空包）
		logger.Warning("[2407] 没有可用的精灵")
		ctx.GameServer.SendResponse(ctx.ClientData, 2407, ctx.UserID, ctx.SeqID, []byte{})
		return
	}

	petMgr := gamepets.GetInstance()
	petEV := picked.GetEVStats()
	petStats := petMgr.GetStats(picked.ID, picked.Level, picked.DV, petEV, picked.Nature)

	logger.Info(fmt.Sprintf("[2407] 选中精灵: PetID=%d HP=%d/%d", picked.ID, petStats.HP, petStats.MaxHP))

	// 检查精灵是否还有HP
	if petStats.HP <= 0 {
		// 错误响应：精灵已死亡
		logger.Warning(fmt.Sprintf("[2407] 精灵HP为0，无法切换: PetID=%d", picked.ID))
		errorBody := make([]byte, 8)
		binary.BigEndian.PutUint32(errorBody[0:4], uint32(ctx.UserID))
		binary.BigEndian.PutUint32(errorBody[4:8], 1) // 错误码：精灵已死亡
		ctx.GameServer.SendResponse(ctx.ClientData, 2407, ctx.UserID, ctx.SeqID, errorBody)
		return
	}

	// 响应 2407：使用 buildChangePetInfoBody 保证 16 字节 UTF-8 安全截断，与 2504 名字一致
	body := buildChangePetInfoBody(uint32(ctx.UserID), uint32(picked.ID), picked.Name, uint32(picked.Level), uint32(petStats.HP), uint32(petStats.MaxHP), uint32(picked.CatchTime))

	// 调试：打印 2407 包体的字段值与十六进制，方便对照前端 ChangePetInfo
	logger.Info(fmt.Sprintf("[2407] RESP body fields: userID=%d petID=%d level=%d hp=%d maxHp=%d catchTime=%d",
		ctx.UserID, picked.ID, picked.Level, petStats.HP, petStats.MaxHP, picked.CatchTime))
	hexStr := ""
	for i, b := range body {
		if i > 0 && i%4 == 0 {
			hexStr += " "
		}
		hexStr += fmt.Sprintf("%02X", b)
	}
	logger.Info(fmt.Sprintf("[2407] RESP raw hex: %s", hexStr))

	// 保存选中的精灵ID和等级（目前仅用于日志调试）
	selectedPetID := picked.ID
	selectedPetLevel := picked.Level

	// 更新战斗状态中的当前出战精灵下标，而不改变 user.Pets 的顺序，
	// 这样战斗结束后背包首发仍然与客户端显示一致。
	playerWasDead := false  // 上一只精灵是否被击败（强制切换 vs 主动切换）
	var opponentUID int64   // PvP 时对方 UID，用于推送 2407 使对方客户端更新“对方换宠”显示
	ctx.GameServer.BattleMu.Lock()
	if battle, exists := ctx.GameServer.BattleStates[ctx.UserID]; exists && battle != nil && battle.IsActive {
		playerWasDead = (battle.PlayerHP == 0) // 在更新前保存：被击败后切换视为新战斗开始
		opponentUID = battle.OpponentUserID
		battle.ActivePetIndex = pickedIndex
		// 切换精灵时重置我方异常状态和强化弱化（新精灵上场为干净状态）；敌方/野生精灵的 status 和 battleLv 不重置
		battle.PlayerStatus = [20]byte{}
		battle.PlayerBattleLv = [6]int8{}
		// 59 - 牺牲强化下一只：如果上一只精灵有牺牲强化效果且被击败，给新精灵应用强化
		if playerWasDead && battle.PlayerSacrificeBuffActive {
			for i := 0; i < 6; i++ {
				if battle.PlayerSacrificeBuffStats[i] > 0 {
					cur := int(battle.PlayerBattleLv[i])
					cur += int(battle.PlayerSacrificeBuffStats[i])
					if cur > 6 {
						cur = 6
					}
					battle.PlayerBattleLv[i] = int8(cur)
				}
			}
			// 应用后清除牺牲强化标记
			battle.PlayerSacrificeBuffActive = false
			for i := 0; i < 6; i++ {
				battle.PlayerSacrificeBuffStats[i] = 0
			}
		}
		// 71 - 牺牲暴击：自己牺牲(体力降到0), 使下一只出战精灵在前两回合内必定致命一击
		if playerWasDead && battle.PlayerSacrificeCritActive {
			battle.PlayerCritBuffRounds = 2
			battle.PlayerSacrificeCritActive = false
		}
		// 67 - 击败减对方下只最大HP：减少新精灵的最大体力1/n
		if battle.PlayerKillReduceMaxHpDivisor > 0 {
			reduceAmount := battle.PlayerMaxHP / uint32(battle.PlayerKillReduceMaxHpDivisor)
			if reduceAmount > 0 && battle.PlayerMaxHP > reduceAmount {
				battle.PlayerMaxHP -= reduceAmount
				if battle.PlayerHP > battle.PlayerMaxHP {
					battle.PlayerHP = battle.PlayerMaxHP
				}
			}
			battle.PlayerKillReduceMaxHpDivisor = 0
		}
		// 144 - 牺牲全部体力使下一只 n 回合免疫异常
		if battle.PlayerSacrificeImmuneStatusRounds > 0 {
			battle.PlayerImmuneStatusRounds = battle.PlayerSacrificeImmuneStatusRounds
			battle.PlayerSacrificeImmuneStatusRounds = 0
		}
		// 同时更新当前血量与最大血量，保证后续 2405 使用正确的 HP
		hp := petStats.HP
		if hp < 0 {
			hp = 0
		}
		if hp > petStats.MaxHP {
			hp = petStats.MaxHP
		}
		battle.PlayerHP = uint32(hp)
		battle.PlayerMaxHP = uint32(petStats.MaxHP)
	}
	ctx.GameServer.BattleMu.Unlock()

	// 发送 2407 响应
	// 注意：不再发送 2301、2508、2504，因为：
	// 1. _petInfoMap 在战斗开始时已经包含了所有精灵信息（通过 buildNoteReadyToFightInfo）
	// 2. 2504 会导致前端重新创建精灵模型，造成精灵重叠
	// 3. 2508 属性更新在切换时不需要
	ctx.GameServer.SendResponse(ctx.ClientData, 2407, ctx.UserID, ctx.SeqID, body)
	// PvP：向对方推送同一条 2407（userID=切换方、petID=新精灵），对方客户端据此显示“对方【xxx】登场了!”并更新右侧模型
	if opponentUID != 0 {
		// 延迟 50ms 再发给对方，确保切换方客户端先处理完切换逻辑
		time.Sleep(50 * time.Millisecond)
		if otherClient := ctx.GameServer.GetClientByUserID(opponentUID); otherClient != nil {
			ctx.GameServer.SendResponse(otherClient, 2407, opponentUID, 0, body)
		}
	}

	// 更新战斗状态，并保存敌方当前 HP 供后续 2505 第二条使用（避免误发“满血”导致客户端显示虚假回血 +N）
	var enemyHPFor2505, enemyMaxHPFor2505 uint32
	ctx.GameServer.BattleMu.Lock()
	battle, exists := ctx.GameServer.BattleStates[ctx.UserID]
	if exists && battle.IsActive {
		// 修复：谱尼封印/真身在切换精灵(2407)时，敌方最大血量应保持为当前门/命对应的固定值，
		// 不应因为客户端血条同步/回包字段而被错误“压缩”为当前血量（例如出现 400/400）。
		if (battle.BattleMapID == 108 || battle.BattleMapID == 514) && battle.EnemyID == 300 && battle.PuniDoorIndex >= 1 && battle.PuniDoorIndex <= 8 {
			expectedMax := uint32(0)
			switch battle.PuniDoorIndex {
			case 1: // 虚无
				expectedMax = 7000
			case 2: // 元素
				expectedMax = 8000
			case 3: // 能量
				expectedMax = 9000
			case 4: // 生命
				expectedMax = 10000
			case 5: // 轮回（两条命，上限一致）
				expectedMax = 10000
			case 6: // 永恒
				expectedMax = 12000
			case 7: // 圣洁
				expectedMax = 16000
			case 8: // 真身：按 1~6 命上限
				life := battle.PuniTrueFormLifeIndex
				if life <= 0 {
					life = 1
				}
				switch life {
				case 1:
					expectedMax = 7000
				case 2:
					expectedMax = 8000
				case 3:
					expectedMax = 9000
				case 4:
					expectedMax = 12000
				case 5:
					expectedMax = 20000
				default:
					expectedMax = 65000
				}
			}
			if expectedMax > 0 {
				battle.EnemyMaxHP = expectedMax
				if battle.EnemyHP > expectedMax {
					battle.EnemyHP = expectedMax
				}
			}
		}
		enemyHPFor2505 = battle.EnemyHP
		enemyMaxHPFor2505 = battle.EnemyMaxHP
		// 更新玩家HP，钳位到 [0, MaxHP] 保证血条范围有效
		hp := petStats.HP
		if hp < 0 {
			hp = 0
		}
		if hp > petStats.MaxHP {
			hp = petStats.MaxHP
		}
		battle.PlayerHP = uint32(hp)
		battle.PlayerMaxHP = uint32(petStats.MaxHP)
		logger.Info(fmt.Sprintf("[2407] 更新战斗状态: PlayerHP=%d/%d PetID=%d 敌方HP=%d/%d(供2505)", battle.PlayerHP, battle.PlayerMaxHP, selectedPetID, enemyHPFor2505, enemyMaxHPFor2505))
		// PvP 模式：同时更新对方 BattleState 中的 EnemyID（我方切换后的 petID）
		// 谁切换则谁的新精灵为干净状态；对方视角的 Enemy 就是切换方的新精灵，需重置 EnemyStatus/EnemyBattleLv
		if battle.OpponentUserID != 0 {
			if opponentBattle, opponentExists := ctx.GameServer.BattleStates[battle.OpponentUserID]; opponentExists && opponentBattle.IsActive {
				opponentBattle.EnemyID = selectedPetID
				opponentBattle.EnemyLevel = selectedPetLevel
				opponentBattle.EnemyHP = uint32(hp)
				opponentBattle.EnemyMaxHP = uint32(petStats.MaxHP)
				opponentBattle.EnemyStatus = [20]byte{}  // 切换方新精灵上场，重置其异常状态
				opponentBattle.EnemyBattleLv = [6]int8{} // 重置其强化弱化
				logger.Info(fmt.Sprintf("[2407] 更新对方战斗状态: OpponentUID=%d EnemyID=%d EnemyHP=%d/%d", battle.OpponentUserID, selectedPetID, petStats.HP, petStats.MaxHP))
			}
		}
	}
	ctx.GameServer.BattleMu.Unlock()

	// 如果战斗中，且为主动切换（上一只精灵未死）：敌方立刻行动一回合（换宠消耗回合）
	// 若为强制切换（上一只精灵被击败）：视为新战斗开始，敌方本回合已出招击杀，不再攻击新上场的精灵。
	if playerWasDead && exists && battle.IsActive {
		logger.Info("[2407] 强制切换（上一只精灵被击败），视为新战斗开始，敌方本回合不再攻击")
	}
	// 注意：不再发送 2508 和 2504，因为：
	// 1. 2504 会导致前端重新创建精灵模型，造成精灵重叠
	// 2. 2508 属性更新在切换时不需要
	// 只发送 2505（敌方攻击）
	if exists && battle.IsActive && !playerWasDead {
		petMgr := gamepets.GetInstance()

		// ==================== 敌方立刻行动一回合（换宠消耗回合，Lua 版 2407 也会触发一次 executeTurn）====================
		// 这里只实现 PvE 简化逻辑：敌人使用默认技能 10001 攻击玩家一次，构造 2505。

		// 重新加锁，使用最新的战斗状态并写回本回合结果
		ctx.GameServer.BattleMu.Lock()
		curBattle, ok := ctx.GameServer.BattleStates[ctx.UserID]
		if !ok || !curBattle.IsActive {
			ctx.GameServer.BattleMu.Unlock()
			return
		}

		skillMgr := gameskills.GetInstance()
		enemySkill, enemySkillID := pickEnemySkill(skillMgr, curBattle.EnemyID, curBattle.EnemyLevel)
		if enemySkill == nil || enemySkillID == 0 {
			// 敌人本回合不出招：不做任何反击，直接返回
			ctx.GameServer.BattleMu.Unlock()
			return
		}

		// 控制类异常：畏缩/睡眠/麻痹 时敌方本回合无法行动（与 2405 一致）
		enemyFlinched := false
		if curBattle.EnemyStatus[gameskills.StatusIndexFear] > 0 {
			enemyFlinched = true
			curBattle.EnemyStatus[gameskills.StatusIndexFear]--
		}
		if curBattle.EnemyStatus[gameskills.StatusIndexSleep] > 0 {
			enemyFlinched = true
			curBattle.EnemyStatus[gameskills.StatusIndexSleep]--
		}
		enemyParalyzed := false
		if curBattle.EnemyStatus[gameskills.StatusIndexParalysis] > 0 {
			enemyParalyzed = true
			curBattle.EnemyStatus[gameskills.StatusIndexParalysis]--
		}
		if enemyFlinched || enemyParalyzed {
			enemySkillID = 0 // 受控制未出招，不发送技能 ID
		}

		// 敌人属性（与 2405 中一致）
		enemyEV := gamepets.EVStats{}
		enemyStats := petMgr.GetStats(curBattle.EnemyID, curBattle.EnemyLevel, 15, enemyEV, 0)

		// 玩家属性：使用当前切换后的精灵战斗属性
		playerStatsAfterSwitch := petStats

		enemyDamage := uint32(0)
		enemyDamageCalc := uint32(0)
		if !enemyFlinched && !enemyParalyzed {
			enemyPower := uint32(enemySkill.Power)
			if enemyPower == 0 {
				enemyPower = 40
			}

			// 敌方攻防（按技能类别），并应用强化弱化倍率（切换后我方 battleLv 已重置为 0）
			enemyAtk := float64(enemyStats.Attack)
			enemyDef := float64(playerStatsAfterSwitch.Defence)
			enemyAtkStage, enemyDefStage := 0, 1
			if enemySkill.Category == 2 {
				enemyAtk = float64(enemyStats.SpAtk)
				enemyDef = float64(playerStatsAfterSwitch.SpDef)
				enemyAtkStage, enemyDefStage = 2, 3
			}
			enemyAtk *= gamebattle.GetStatMultiplier(int(curBattle.EnemyBattleLv[enemyAtkStage]))
			enemyDef *= gamebattle.GetStatMultiplier(int(curBattle.PlayerBattleLv[enemyDefStage]))
			if enemyDef < 1 {
				enemyDef = 1
			}

			enemyBaseDamage := math.Floor(((float64(curBattle.EnemyLevel)*0.4 + 2.0) * float64(enemyPower) * enemyAtk / enemyDef / 50.0) + 2.0)

			enemyStab := 1.0
			if enemyPetDef := petMgr.Get(curBattle.EnemyID); enemyPetDef != nil && (enemySkill.Type == enemyPetDef.Type || (enemyPetDef.Type2 > 0 && enemySkill.Type == enemyPetDef.Type2)) {
				enemyStab = 1.5
			}

			enemyTypeMod := 1.0
			if attackerPetDef := petMgr.Get(selectedPetID); attackerPetDef != nil {
				enemyTypeMod = gamebattle.GetTypeMultiplierDual(enemySkill.Type, attackerPetDef.Type, attackerPetDef.Type2)
			}

			enemyRandomMod := float64(rand.Intn(255-217+1)+217) / 255.0
			enemyFinalDamage := uint32(enemyBaseDamage * enemyStab * enemyTypeMod * enemyRandomMod)
			if curBattle.EnemyStatus[gameskills.StatusIndexBurn] > 0 {
				enemyFinalDamage = enemyFinalDamage / 2
				if enemyFinalDamage < 1 {
					enemyFinalDamage = 1
				}
			}
			if enemyFinalDamage < 1 {
				enemyFinalDamage = 1
			}
			enemyDamageCalc = enemyFinalDamage // 理论伤害，用于客户端显示
			if enemyFinalDamage > curBattle.PlayerHP {
				enemyFinalDamage = curBattle.PlayerHP
			}

			enemyDamage = enemyFinalDamage

			// 更新玩家 HP
			curBattle.PlayerHP -= enemyDamage
			if curBattle.PlayerHP > curBattle.PlayerMaxHP {
				curBattle.PlayerHP = 0
			}
		}

		// 记录更新后的 HP、敌方 status/battleLv（用于构造 2505），然后解锁
		opponentUID := curBattle.OpponentUserID
		enemyStatusFor2505 := curBattle.EnemyStatus // 敌方异常状态和强化弱化跟随精灵，不重置，需正确下发
		enemyBattleLvFor2505 := curBattle.EnemyBattleLv
		ctx.GameServer.BattleMu.Unlock()

		// 构造 2505：第一条为敌人攻击玩家，第二条为占位（使用切换前保存的敌方 HP，避免客户端误显示“+N”回血）
		body2505 := make([]byte, 0, 160)

		// 敌人 userID：PvE 为 0，PvP 为对方 UID
		enemyUserID := uint32(0)
		if opponentUID != 0 {
			enemyUserID = uint32(opponentUID)
		}

		// 第一条：敌人攻击玩家（status/battleLv 用敌方实际值，客户端才能正确显示对方异常与强化弱化）
		enemyRemainForAv1 := int32(curBattle.EnemyHP)
		enemyMaxForAv1 := curBattle.EnemyMaxHP
		// 谱尼战：占位 2505 也使用伪血量，保持血条与普通攻击时一致
		if (curBattle.BattleMapID == 108 || curBattle.BattleMapID == 514) && curBattle.EnemyID == 300 {
			dispHP, dispMax := puniDisplayHP(curBattle.EnemyHP, curBattle.EnemyMaxHP)
			enemyRemainForAv1 = int32(dispHP)
			enemyMaxForAv1 = dispMax
		}
		body2505 = append(body2505, buildAttackValue(enemyUserID, enemySkillID, 1, enemyDamageCalc, 0, enemyRemainForAv1, enemyMaxForAv1, 0, 0, 0, enemyStatusFor2505, enemyBattleLvFor2505)...)

		// 第二条：占位，同步敌方血条。必须用 enemyUserID（敌方），因前端按 attackValue.userID 更新对应方血条；status/battleLv 同样下发敌方实际值
		if enemyMaxHPFor2505 == 0 {
			enemyMaxHPFor2505 = 1
		}
		enemyRemainForAv2 := int32(enemyHPFor2505)
		enemyMaxForAv2 := enemyMaxHPFor2505
		if (curBattle.BattleMapID == 108 || curBattle.BattleMapID == 514) && curBattle.EnemyID == 300 {
			dispHP2, dispMax2 := puniDisplayHP(uint32(enemyHPFor2505), enemyMaxHPFor2505)
			enemyRemainForAv2 = int32(dispHP2)
			enemyMaxForAv2 = dispMax2
		}
		body2505 = append(body2505, buildAttackValue(enemyUserID, 0, 0, 0, 0, enemyRemainForAv2, enemyMaxForAv2, 0, 0, 0, enemyStatusFor2505, enemyBattleLvFor2505)...)

		ctx.GameServer.SendResponse(ctx.ClientData, 2505, ctx.UserID, ctx.SeqID, body2505)
		if opponentUID != 0 {
			if otherClient := ctx.GameServer.GetClientByUserID(opponentUID); otherClient != nil {
				ctx.GameServer.SendResponse(otherClient, 2505, opponentUID, 0, body2505)
			}
		}
	}
}

// handleCatchMonster CMD 2409 捕捉精灵
// 请求: itemID(4) 胶囊物品 ID，客户端 CatchPetItemCategory 发送
// 响应成功: catchTime(4)+petId(4)；失败: 空 body
func handleCatchMonster(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// 解析胶囊 ID
	capsuleID := 300001
	if len(ctx.Body) >= 4 {
		capsuleID = int(binary.BigEndian.Uint32(ctx.Body[0:4]))
	}
	if capsuleID <= 0 {
		capsuleID = 300001
	}

	// 校验胶囊：GM 可在权重管理中设置各胶囊捕捉率，未知胶囊默认 50%
	capsuleMod := GetCapsuleCatchMod(capsuleID)
	if user.Items == nil {
		user.Items = make(map[string]userdb.Item)
	}
	itemKey := strconv.Itoa(capsuleID)
	it, hasCapsule := user.Items[itemKey]
	if !hasCapsule || it.Count < 1 {
		ctx.GameServer.SendResponse(ctx.ClientData, 2409, ctx.UserID, ctx.SeqID, []byte{})
		logger.Info(fmt.Sprintf("[2409] 胶囊不足: UID=%d itemID=%d", ctx.UserID, capsuleID))
		return
	}

	ctx.GameServer.BattleMu.Lock()
	battle, exists := ctx.GameServer.BattleStates[ctx.UserID]
	if exists && battle.IsActive && battle.OpponentUserID != 0 {
		ctx.GameServer.BattleMu.Unlock()
		// PvP 模式下不可捕捉对方精灵：返回空/失败，客户端会显示“赛尔间对战无法捕捉”等
		ctx.GameServer.SendResponse(ctx.ClientData, 2409, ctx.UserID, ctx.SeqID, []byte{})
		logger.Info(fmt.Sprintf("[2409] PvP 模式拒绝捕捉: UID=%d", ctx.UserID))
		return
	}
	bossID := 13
	bossLevel := 5
	enemyHP, enemyMaxHP := uint32(0), uint32(1)
	if exists && battle.IsActive {
		bossID = battle.EnemyID
		bossLevel = battle.EnemyLevel
		enemyHP, enemyMaxHP = battle.EnemyHP, battle.EnemyMaxHP
		if enemyMaxHP == 0 {
			enemyMaxHP = 1
		}
		logger.Info(fmt.Sprintf("[2409] 从战斗状态获取: EnemyID=%d EnemyLevel=%d HP=%d/%d", bossID, bossLevel, enemyHP, enemyMaxHP))
	} else {
		logger.Warning(fmt.Sprintf("[2409] 未找到战斗状态，使用默认值: EnemyID=%d EnemyLevel=%d", bossID, bossLevel))
	}
	ctx.GameServer.BattleMu.Unlock()

	if _, ok := sptboss.GetByPetID(bossID); ok {
		ctx.GameServer.SendResponse(ctx.ClientData, 2409, ctx.UserID, ctx.SeqID, []byte{})
		logger.Info(fmt.Sprintf("[2409] SPT BOSS 不可捕捉，拒绝: UID=%d PetID=%d", ctx.UserID, bossID))
		return
	}

	// 消耗胶囊（无论成功失败，使用即消耗）
	it.Count--
	if it.Count <= 0 {
		delete(user.Items, itemKey)
	} else {
		user.Items[itemKey] = it
	}

	// 计算捕捉概率：基础率(petCatchRate/255) * HP因子(血量越低越易捉) * 胶囊修正
	petMgr := gamepets.GetInstance()
	petDef := petMgr.Get(bossID)
	petCatchRate := 255
	if petDef != nil && petDef.CatchRate > 0 {
		petCatchRate = petDef.CatchRate
	}
	hpFactor := 0.3 + 0.7*(1.0-float64(enemyHP)/float64(enemyMaxHP))
	baseRate := float64(petCatchRate) / 255.0
	catchProb := baseRate * hpFactor * capsuleMod
	if catchProb > 0.99 && capsuleMod < 1.0 {
		catchProb = 0.99
	}
	if capsuleMod >= 1.0 {
		catchProb = 1.0
	}
	roll := rand.Float64()
	caught := roll < catchProb

	logger.Info(fmt.Sprintf("[2409] 捕捉判定: itemID=%d petCatchRate=%d hpFactor=%.2f mod=%.2f prob=%.2f roll=%.2f -> %v",
		capsuleID, petCatchRate, hpFactor, capsuleMod, catchProb, roll, caught))

	if !caught {
		ctx.GameServer.SendResponse(ctx.ClientData, 2409, ctx.UserID, ctx.SeqID, []byte{})
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
		logger.Info(fmt.Sprintf("[2409] 捕捉失败: UID=%d PetID=%d (胶囊已消耗)，推送敌方回合", ctx.UserID, bossID))

		// 捕捉失败后推送敌方回合（CMD 2505），避免客户端卡在“等待对方”
		petMgr := gamepets.GetInstance()
		skillMgr := gameskills.GetInstance()
		ctx.GameServer.BattleMu.Lock()
		curBattle, ok := ctx.GameServer.BattleStates[ctx.UserID]
		if !ok || !curBattle.IsActive {
			ctx.GameServer.BattleMu.Unlock()
			return
		}
		enemySkill, enemySkillID := pickEnemySkill(skillMgr, curBattle.EnemyID, curBattle.EnemyLevel)
		enemyFlinched := false
		if curBattle.EnemyStatus[gameskills.StatusIndexFear] > 0 {
			enemyFlinched = true
			curBattle.EnemyStatus[gameskills.StatusIndexFear]--
		}
		if curBattle.EnemyStatus[gameskills.StatusIndexSleep] > 0 {
			enemyFlinched = true
			curBattle.EnemyStatus[gameskills.StatusIndexSleep]--
		}
		enemyParalyzed := curBattle.EnemyStatus[gameskills.StatusIndexParalysis] > 0
		if enemyParalyzed {
			curBattle.EnemyStatus[gameskills.StatusIndexParalysis]--
		}
		if enemyFlinched || enemyParalyzed {
			enemySkillID = 0
		}
		enemyDamageCalc := uint32(0)
		enemyDamage := uint32(0)
		if enemySkill != nil && enemySkillID != 0 && !enemyFlinched && !enemyParalyzed {
			enemyEV := gamepets.EVStats{}
			enemyStats := petMgr.GetStats(curBattle.EnemyID, curBattle.EnemyLevel, 15, enemyEV, 0)
			// 使用当前出战精灵的完整属性计算防御，否则 Defence/SpDef 为 0 会导致伤害异常偏高
			activeIdx := 0
			if curBattle.ActivePetIndex > 0 && curBattle.ActivePetIndex < len(user.Pets) {
				activeIdx = curBattle.ActivePetIndex
			}
			playerPetID := 7
			playerLevel := 5
			playerDV := 31
			playerNature := 0
			playerEV := gamepets.EVStats{}
			if len(user.Pets) > 0 {
				playerPetID = user.Pets[activeIdx].ID
				if user.Pets[activeIdx].Level > 0 {
					playerLevel = user.Pets[activeIdx].Level
				}
				playerDV = user.Pets[activeIdx].DV
				playerNature = user.Pets[activeIdx].Nature
				playerEV = user.Pets[activeIdx].GetEVStats()
			}
			playerStatsForDef := petMgr.GetStats(playerPetID, playerLevel, playerDV, playerEV, playerNature)
			enemyPower := uint32(enemySkill.Power)
			if enemyPower == 0 {
				enemyPower = 40
			}
			enemyAtk := float64(enemyStats.Attack)
			enemyDef := float64(playerStatsForDef.Defence)
			enemyAtkStage, enemyDefStage := 0, 1
			if enemySkill.Category == 2 {
				enemyAtk = float64(enemyStats.SpAtk)
				enemyDef = float64(playerStatsForDef.SpDef)
				enemyAtkStage, enemyDefStage = 2, 3
			}
			enemyAtk *= gamebattle.GetStatMultiplier(int(curBattle.EnemyBattleLv[enemyAtkStage]))
			enemyDef *= gamebattle.GetStatMultiplier(int(curBattle.PlayerBattleLv[enemyDefStage]))
			if enemyDef < 1 {
				enemyDef = 1
			}
			enemyBaseDamage := math.Floor(((float64(curBattle.EnemyLevel)*0.4+2.0)*float64(enemyPower)*enemyAtk/enemyDef/50.0) + 2.0)
			enemyStab := 1.0
			if enemyPetDef := petMgr.Get(curBattle.EnemyID); enemyPetDef != nil && (enemySkill.Type == enemyPetDef.Type || (enemyPetDef.Type2 > 0 && enemySkill.Type == enemyPetDef.Type2)) {
				enemyStab = 1.5
			}
			enemyTypeMod := 1.0
			if len(user.Pets) > 0 {
				if attackerPetDef := petMgr.Get(user.Pets[0].ID); attackerPetDef != nil {
					enemyTypeMod = gamebattle.GetTypeMultiplierDual(enemySkill.Type, attackerPetDef.Type, attackerPetDef.Type2)
				}
			}
			enemyRandomMod := float64(rand.Intn(255-217+1)+217) / 255.0
			enemyFinalDamage := uint32(enemyBaseDamage * enemyStab * enemyTypeMod * enemyRandomMod)
			if curBattle.EnemyStatus[gameskills.StatusIndexBurn] > 0 {
				enemyFinalDamage = enemyFinalDamage / 2
				if enemyFinalDamage < 1 {
					enemyFinalDamage = 1
				}
			}
			if enemyFinalDamage < 1 {
				enemyFinalDamage = 1
			}
			enemyDamageCalc = enemyFinalDamage
			if enemyFinalDamage > curBattle.PlayerHP {
				enemyFinalDamage = curBattle.PlayerHP
			}
			enemyDamage = enemyFinalDamage
			curBattle.PlayerHP -= enemyDamage
			if curBattle.PlayerHP > curBattle.PlayerMaxHP {
				curBattle.PlayerHP = 0
			}
		}
		opponentUID := curBattle.OpponentUserID
		enemyStatusFor2505 := curBattle.EnemyStatus
		enemyBattleLvFor2505 := curBattle.EnemyBattleLv
		playerHPAfter := curBattle.PlayerHP
		playerMaxHP := curBattle.PlayerMaxHP
		playerStatusFor2505 := curBattle.PlayerStatus
		playerBattleLvFor2505 := curBattle.PlayerBattleLv
		ctx.GameServer.BattleMu.Unlock()

		// 与 2405 一致：2505 发两条 AttackValue（我方行动 + 对方行动），否则客户端可能卡在“等待对方”
		playerAv := buildAttackValue(
			uint32(ctx.UserID), 0, 1, // 我方本回合“捕捉”，skillID=0 表示无攻击技能
			0, 0, int32(playerHPAfter), uint32(playerMaxHP),
			0, 0, 0,
			playerStatusFor2505, playerBattleLvFor2505,
		)
		enemyUserID := uint32(0)
		if opponentUID != 0 {
			enemyUserID = uint32(opponentUID)
		}
		enemyAv := buildAttackValue(
			enemyUserID, enemySkillID, 1,
			enemyDamageCalc, 0,
			int32(curBattle.EnemyHP), curBattle.EnemyMaxHP,
			0, 0, 0,
			enemyStatusFor2505, enemyBattleLvFor2505,
		)
		body2505 := make([]byte, 0, 164)
		body2505 = append(body2505, playerAv...)
		body2505 = append(body2505, enemyAv...)
		ctx.GameServer.SendResponse(ctx.ClientData, 2505, ctx.UserID, ctx.SeqID, body2505)
		if opponentUID != 0 {
			if otherClient := ctx.GameServer.GetClientByUserID(opponentUID); otherClient != nil {
				ctx.GameServer.SendResponse(otherClient, 2505, opponentUID, 0, body2505)
			}
		}
		return
	}

	newCatchTime := uint32(time.Now().Unix())
	rand.Seed(time.Now().UnixNano() + int64(newCatchTime))
	catchLevel := bossLevel
	randomDV := rand.Intn(32)
	randomNature := rand.Intn(25)

	body := make([]byte, 8)
	binary.BigEndian.PutUint32(body[0:4], newCatchTime)
	binary.BigEndian.PutUint32(body[4:8], uint32(bossID))
	ctx.GameServer.SendResponse(ctx.ClientData, 2409, ctx.UserID, ctx.SeqID, body)

	newPet := userdb.Pet{
		ID:        bossID,
		CatchTime: int(newCatchTime),
		Level:     catchLevel, // 等级与对战中的敌人等级一致
		DV:        randomDV,
		Nature:    randomNature,
		Exp:       0,
		Name:      "",
	}
	// 野外/副本捕捉的精灵默认不带特性；后续需通过“特性开启芯片”等道具获得特性
	// 背包已满 6 只则放入仓库，否则放入背包（与 SPT 奖励精灵一致）
	if len(user.Pets) >= 6 {
		if user.StoragePets == nil {
			user.StoragePets = []userdb.Pet{}
		}
		user.StoragePets = append(user.StoragePets, newPet)
		logger.Info(fmt.Sprintf("[2409] 捕捉成功: PetID=%d Level=%d Capsule=%d CatchTime=%d -> 仓库(背包已满)", bossID, catchLevel, capsuleID, newCatchTime))
	} else {
		user.Pets = append(user.Pets, newPet)
		logger.Info(fmt.Sprintf("[2409] 捕捉成功: PetID=%d Level=%d Capsule=%d CatchTime=%d -> 背包", bossID, catchLevel, capsuleID, newCatchTime))
	}

	// 清理战斗状态
	ctx.GameServer.BattleMu.Lock()
	delete(ctx.GameServer.BattleStates, ctx.UserID)
	ctx.GameServer.BattleMu.Unlock()

	// 结束战斗
	overBody := buildFightOverInfo(0, uint32(ctx.UserID))
	ctx.GameServer.SendResponse(ctx.ClientData, 2506, ctx.UserID, ctx.SeqID, overBody)
	pushMapOgreListAfterFightOver(ctx, false)

	// 保存数据
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
}

// ==================== NONO系统命令处理器 ====================

// calculateSuperNonoTypeByLevel 根据超能等级计算对应的形态（超能等级模型）
// 超能等级最高为12级，超能形态分别是1、4、7、9、12，共五个形态
// 等级1-3：形态1，等级4-6：形态2，等级7-8：形态3，等级9-11：形态4，等级12：形态5
func calculateSuperNonoTypeByLevel(level int) int {
	if level < 1 {
		return 0
	}
	if level >= 12 {
		return 5 // 形态5：等级12
	}
	if level >= 9 {
		return 4 // 形态4：等级9-11
	}
	if level >= 7 {
		return 3 // 形态3：等级7-8
	}
	if level >= 4 {
		return 2 // 形态2：等级4-6
	}
	return 1 // 形态1：等级1-3
}

// updateSuperNonoTypeByLevel 根据超能等级更新形态
func updateSuperNonoTypeByLevel(user *userdb.GameData) {
	calculatedType := calculateSuperNonoTypeByLevel(user.Nono.SuperLevel)
	user.Nono.SuperNono = calculatedType
}

// registerSuperNonoToCache 将当前连接的超能等级登记到资源服缓存中，供 /resource/nono/super/* 按等级换算形态
func registerSuperNonoToCache(ctx *gameserver.HandlerContext, user *userdb.GameData) {
	if user == nil || user.Nono.SuperLevel <= 0 {
		return
	}
	// 按米米号登记，供 /api/set_user?uid=... 使用
	nonoformcache.RegisterByUserID(ctx.UserID, user.Nono.SuperLevel)

	// 按客户端 IP 登记，供未通过 set_user 时的回退逻辑使用
	if ctx.ClientData != nil && ctx.ClientData.Socket != nil {
		addr := ctx.ClientData.Socket.RemoteAddr().String()
		clientIP, _, err := net.SplitHostPort(addr)
		if err != nil || clientIP == "" {
			clientIP = addr
		}
		nonoformcache.Register(clientIP, user.Nono.SuperLevel)
	}
}

// registerNonoHandlers 注册NONO系统命令处理器
func registerNonoHandlers(gs *gameserver.GameServer) {
	gs.RegisterCommandHandler(9001, handleNonoOpen)          // 开启NONO
	gs.RegisterCommandHandler(9002, handleNonoChangeName)    // 修改NONO名称
	gs.RegisterCommandHandler(9003, handleNonoInfo)          // NONO信息
	gs.RegisterCommandHandler(9004, handleNonoChipMixture)   // NONO芯片合成
	gs.RegisterCommandHandler(9007, handleNonoCure)          // NONO治疗
	gs.RegisterCommandHandler(9008, handleNonoExpadm)        // NONO经验管理
	gs.RegisterCommandHandler(9010, handleNonoImplementTool) // 使用NONO道具/芯片
	gs.RegisterCommandHandler(9012, handleNonoChangeColor)   // 修改NONO颜色
	gs.RegisterCommandHandler(9013, handleNonoPlay)          // NONO玩耍
	gs.RegisterCommandHandler(9014, handleNonoCloseOpen)     // NONO开关
	gs.RegisterCommandHandler(9015, handleNonoExeList)       // NONO执行列表
	gs.RegisterCommandHandler(9016, handleNonoCharge)        // NONO充电
	gs.RegisterCommandHandler(9017, handleNonoStartExe)      // 开始执行
	gs.RegisterCommandHandler(9018, handleNonoEndExe)        // 结束执行
	gs.RegisterCommandHandler(9019, handleNonoFollowOrHoom)  // 跟随或回家
	gs.RegisterCommandHandler(9020, handleNonoOpenSuper)     // 开启超级NONO
	gs.RegisterCommandHandler(9021, handleNonoHelpExp)       // NONO帮助经验
	gs.RegisterCommandHandler(9022, handleNonoMateChange)    // NONO心情变化
	gs.RegisterCommandHandler(9023, handleNonoGetChip)       // 获取芯片
	gs.RegisterCommandHandler(9024, handleNonoAddEnergyMate) // 增加能量心情
	gs.RegisterCommandHandler(9025, handleGetDiamond)        // 获取钻石
	gs.RegisterCommandHandler(9026, handleNonoAddExp)        // 增加NONO经验
	gs.RegisterCommandHandler(9027, handleNonoIsInfo)        // NONO是否有信息
	gs.RegisterCommandHandler(80001, handleNieoLogin)        // 超能NONO登录
}

// handleNonoOpen CMD 9001 开启NONO
func handleNonoOpen(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	user.Nono.HasNono = 1
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 9001, ctx.UserID, ctx.SeqID, []byte{})
}

// handleNonoChangeName CMD 9002 修改NONO名称
func handleNonoChangeName(ctx *gameserver.HandlerContext) {
	var name string
	if len(ctx.Body) >= 16 {
		name = string(ctx.Body[:16])
		// 移除末尾的null字符
		for i := len(name) - 1; i >= 0 && name[i] == 0; i-- {
			name = name[:i]
		}
	}
	if name == "" {
		name = "NoNo"
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	user.Nono.Nick = name
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	body := make([]byte, 16)
	copy(body, []byte(name))
	ctx.GameServer.SendResponse(ctx.ClientData, 9002, ctx.UserID, ctx.SeqID, body)
}

// handleNonoChipMixture CMD 9004 NONO芯片合成
func handleNonoChipMixture(ctx *gameserver.HandlerContext) {
	ctx.GameServer.SendResponse(ctx.ClientData, 9004, ctx.UserID, ctx.SeqID, []byte{})
}

// handleNonoCure CMD 9007 NONO治疗
func handleNonoCure(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	// 恢复所有精灵的HP
	// 注意：当前GameData结构中没有存储HP，所以只发送响应
	// 实际的HP恢复应该在战斗状态中处理
	_ = gamepets.GetInstance() // 确保技能管理器已加载
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 9007, ctx.UserID, ctx.SeqID, []byte{})
}

// handleNonoExpadm CMD 9008 NONO经验管理
func handleNonoExpadm(ctx *gameserver.HandlerContext) {
	ctx.GameServer.SendResponse(ctx.ClientData, 9008, ctx.UserID, ctx.SeqID, []byte{})
}

// handleNonoImplementTool CMD 9010 使用NONO道具/芯片
func handleNonoImplementTool(ctx *gameserver.HandlerContext) {
	var itemID uint32
	if len(ctx.Body) >= 4 {
		itemID = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// 消耗背包中的道具
	itemKey := strconv.FormatUint(uint64(itemID), 10)
	if item, hasItem := user.Items[itemKey]; hasItem {
		item.Count--
		if item.Count <= 0 {
			delete(user.Items, itemKey)
		} else {
			user.Items[itemKey] = item
		}
	}

	// 芯片（700001-700060）使用后永久解锁，持久化到 Nono.Func 位图
	if itemID >= 700001 && itemID <= 700060 {
		idx := int(itemID - 700001)
		for len(user.Nono.Func) < 20 {
			user.Nono.Func = append(user.Nono.Func, 0)
		}
		if idx < 160 {
			byteIdx := idx / 8
			bitIdx := uint(idx % 8)
			user.Nono.Func[byteIdx] |= 1 << bitIdx
		}
	}

	// 从 items.xml 解析的道具效果中读取芯片对 NONO 的数值增益与颜色（UseAI / UsePower / Color）
	// 仅在 NONO 道具/芯片使用时应用，避免影响其他系统
	loadItemNamesOnce()
	if eff, ok := itemEffects[int(itemID)]; ok {
		// 能量（Power）累加，参考其他逻辑上限 100000
		if eff.UsePower != 0 {
			user.Nono.Power += eff.UsePower
			if user.Nono.Power > 100000 {
				user.Nono.Power = 100000
			}
			if user.Nono.Power < 0 {
				user.Nono.Power = 0
			}
		}
		// AI 直接累加（存储为 uint16，UseAI 数值较小，无需额外上限）
		if eff.UseAI != 0 {
			user.Nono.AI += eff.UseAI
			if user.Nono.AI < 0 {
				user.Nono.AI = 0
			}
		}
		// 变色芯片（700301-700312）：使用后直接修改 NONO 颜色，Color 取自 items.xml
		if eff.Color != 0 && itemID >= 700301 && itemID <= 700312 {
			user.Nono.Color = eff.Color
			// 若当前 NONO 处于跟随状态，主动推送一次 9019，驱动客户端立刻刷新超能NONO外观颜色
			pushNonoFollowState(ctx, user)
		}
	}

	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	// 响应: userId(4) + itemId(4) + power(4, *1000) + ai(2) + mate(4, *1000) + iq(4)
	body := make([]byte, 22)
	binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID))
	binary.BigEndian.PutUint32(body[4:8], itemID)
	binary.BigEndian.PutUint32(body[8:12], uint32(user.Nono.Power*1000))
	binary.BigEndian.PutUint16(body[12:14], uint16(user.Nono.AI))
	binary.BigEndian.PutUint32(body[14:18], uint32(user.Nono.Mate*1000))
	binary.BigEndian.PutUint32(body[18:22], uint32(user.Nono.IQ))
	ctx.GameServer.SendResponse(ctx.ClientData, 9010, ctx.UserID, ctx.SeqID, body)
}

// handleNonoChangeColor CMD 9012 修改NONO颜色
func handleNonoChangeColor(ctx *gameserver.HandlerContext) {
	var color uint32
	if len(ctx.Body) >= 4 {
		color = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	user.Nono.Color = int(color)
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 9012, ctx.UserID, ctx.SeqID, []byte{})
}

// handleNonoPlay CMD 9013 NONO玩耍
func handleNonoPlay(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	// 玩耍增加心情
	if user.Nono.Mate+5000 > 100000 {
		user.Nono.Mate = 100000
	} else {
		user.Nono.Mate += 5000
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	// 响应: result(4) + itemId(4) + power(4) + ai(2) + mate(4) + iq(4)
	body := make([]byte, 22)
	binary.BigEndian.PutUint32(body[0:4], 0)
	binary.BigEndian.PutUint32(body[4:8], 0)
	binary.BigEndian.PutUint32(body[8:12], uint32(user.Nono.Power))
	binary.BigEndian.PutUint16(body[12:14], uint16(user.Nono.AI))
	binary.BigEndian.PutUint32(body[14:18], uint32(user.Nono.Mate))
	binary.BigEndian.PutUint32(body[18:22], uint32(user.Nono.IQ))
	ctx.GameServer.SendResponse(ctx.ClientData, 9013, ctx.UserID, ctx.SeqID, body)
}

// handleNonoCloseOpen CMD 9014 NONO开关
func handleNonoCloseOpen(ctx *gameserver.HandlerContext) {
	var action uint32
	if len(ctx.Body) >= 4 {
		action = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	user.Nono.State = int(action)
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 9014, ctx.UserID, ctx.SeqID, []byte{})
	if user.MapID > 0 {
		listBody := buildMapPlayerListForMap(ctx.GameServer, user.MapID)
		ctx.GameServer.BroadcastToMap(user.MapID, 0, 2003, listBody)
	}
}

// handleNonoExeList CMD 9015 NONO执行列表
func handleNonoExeList(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], 0) // count = 0
	ctx.GameServer.SendResponse(ctx.ClientData, 9015, ctx.UserID, ctx.SeqID, body)
}

// handleNonoCharge CMD 9016 NONO充电
func handleNonoCharge(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.Nono.SuperEnergy+1000 > 99999 {
		user.Nono.SuperEnergy = 99999
	} else {
		user.Nono.SuperEnergy += 1000
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 9016, ctx.UserID, ctx.SeqID, []byte{})
}

// handleNonoStartExe CMD 9017 开始执行
func handleNonoStartExe(ctx *gameserver.HandlerContext) {
	ctx.GameServer.SendResponse(ctx.ClientData, 9017, ctx.UserID, ctx.SeqID, []byte{})
}

// handleNonoEndExe CMD 9018 结束执行
func handleNonoEndExe(ctx *gameserver.HandlerContext) {
	ctx.GameServer.SendResponse(ctx.ClientData, 9018, ctx.UserID, ctx.SeqID, []byte{})
}

// pushNonoFollowState 主动推送一次 9019，通知客户端当前 NONO 的跟随状态与外观（颜色/形态）
// 用于在服务端状态改变但客户端未重新发送 9019 时（例如使用变色芯片后），实现「实时生效」刷新
func pushNonoFollowState(ctx *gameserver.HandlerContext, user *userdb.GameData) {
	// 仅在 NONO 处于跟随状态时才需要推送
	if user.Nono.State != 1 {
		return
	}

	// 与前端 FollowCmdListener 解析顺序一致：userId(4), superStage(4), state(4), nick(16), color(4), power(4)
	body := make([]byte, 36)
	binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID))
	binary.BigEndian.PutUint32(body[4:8], uint32(user.Nono.SuperNono)) // superStage 1-5
	binary.BigEndian.PutUint32(body[8:12], 1)                         // state=1 跟随状态
	nickBytes := []byte(user.Nono.Nick)
	if len(nickBytes) > 16 {
		nickBytes = nickBytes[:16]
	}
	copy(body[12:28], nickBytes)
	binary.BigEndian.PutUint32(body[28:32], uint32(user.Nono.Color))
	binary.BigEndian.PutUint32(body[32:36], 0) // power
	logger.Info(fmt.Sprintf("[9019][push] NONO跟随实时刷新: UserID=%d SuperLevel=%d SuperNono形态=%d (应加载nono_%d.swf) Color=%d",
		ctx.UserID, user.Nono.SuperLevel, user.Nono.SuperNono, user.Nono.SuperNono, user.Nono.Color))

	ctx.GameServer.SendResponse(ctx.ClientData, 9019, ctx.UserID, ctx.SeqID, body)
}

// handleNonoFollowOrHoom CMD 9019 跟随或回家
// 请求包体（36 字节时）：[0:4] userID, [4:8] 保留, [8:12] state(0=回家 1=跟随), [12:28] nick, [28:32] color, [32:36] 超能等级(1-12)
// 根据封包中的超能等级更新 SuperLevel，并据此计算形态(1-5)下发给客户端，客户端加载 nono_N.swf
func handleNonoFollowOrHoom(ctx *gameserver.HandlerContext) {
	var action uint32
	if len(ctx.Body) >= 12 {
		// 完整包体时 state 在 [8:12]
		action = binary.BigEndian.Uint32(ctx.Body[8:12])
	} else if len(ctx.Body) >= 4 {
		action = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// 从封包解析超能等级(1-12)，更新后按等级换算形态(1-5)下发给客户端，并登记到资源服缓存
	if len(ctx.Body) >= 36 {
		superLevelFromPacket := binary.BigEndian.Uint32(ctx.Body[32:36])
		if superLevelFromPacket >= 1 && superLevelFromPacket <= 12 {
			user.Nono.SuperLevel = int(superLevelFromPacket)
			updateSuperNonoTypeByLevel(user)
			registerSuperNonoToCache(ctx, user)
		}
	}

	// 持久化 NONO 状态：1=跟随，0=在家。每人最多1只，召唤后基地不再显示另一只
	user.Nono.State = int(action)
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	var body []byte
	if action == 1 {
		// 形态已由上方的封包超能等级更新；若无封包等级则沿用库里的 SuperLevel 再算一次形态
		if user.Nono.SuperLevel > 0 {
			updateSuperNonoTypeByLevel(user)
		}
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
		// 跟随: 返回 36 字节，与前端 FollowCmdListener 一致：userId(4), superStage(4), state(4), nick(16), color(4), power(4)
		body = make([]byte, 36)
		binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID))
		binary.BigEndian.PutUint32(body[4:8], uint32(user.Nono.SuperNono)) // superStage 1-5
		binary.BigEndian.PutUint32(body[8:12], 1)                         // state=1 跟随状态
		nickBytes := []byte(user.Nono.Nick)
		if len(nickBytes) > 16 {
			nickBytes = nickBytes[:16]
		}
		copy(body[12:28], nickBytes)
		binary.BigEndian.PutUint32(body[28:32], uint32(user.Nono.Color))
		binary.BigEndian.PutUint32(body[32:36], 0) // power
		logger.Info(fmt.Sprintf("[9019] NONO跟随: UserID=%d SuperLevel=%d SuperNono形态=%d (应加载nono_%d.swf) body[4:8]=%d",
			ctx.UserID, user.Nono.SuperLevel, user.Nono.SuperNono, user.Nono.SuperNono, user.Nono.SuperNono))
	} else {
		// 回家: 只返回12 bytes
		body = make([]byte, 12)
		binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID))
		binary.BigEndian.PutUint32(body[4:8], 0)  // flag=0
		binary.BigEndian.PutUint32(body[8:12], 0) // state=0 已回家
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 9019, ctx.UserID, ctx.SeqID, body)
	// 广播更新后的 2003，使同图玩家即时看到 NONO 跟随/回家状态与形态
	if user.MapID > 0 {
		listBody := buildMapPlayerListForMap(ctx.GameServer, user.MapID)
		ctx.GameServer.BroadcastToMap(user.MapID, 0, 2003, listBody)
		if action == 1 && len(body) >= 36 {
			bodyForOthers := make([]byte, 36)
			copy(bodyForOthers, body)
			binary.BigEndian.PutUint32(bodyForOthers[4:8], uint32(user.Nono.SuperNono))
			ctx.GameServer.BroadcastToMap(user.MapID, ctx.UserID, 9019, bodyForOthers)
		} else if action == 0 {
			// 回家时也向同图其他玩家广播 9019（12 字节 state=0），客户端收到后触发 NONO_HOOM 并 hideNono
			ctx.GameServer.BroadcastToMap(user.MapID, ctx.UserID, 9019, body)
		}
	}
}

// handleNonoOpenSuper CMD 9020 开启超级NONO
func handleNonoOpenSuper(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.Nono.SuperLevel < 1 {
		user.Nono.SuperLevel = 1
	}
	if user.Nono.SuperStage < 1 {
		user.Nono.SuperStage = 1
	}
	// 根据超能等级自动更新形态（超能等级模型），而不是硬编码为1
	updateSuperNonoTypeByLevel(user)
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	// 更新资源服缓存中的超能等级，便于 /resource/nono/super/* 按等级换算形态
	registerSuperNonoToCache(ctx, user)
	ctx.GameServer.SendResponse(ctx.ClientData, 9020, ctx.UserID, ctx.SeqID, []byte{})
}

// handleNonoHelpExp CMD 9021 NONO帮助经验（NoNo 照顾精灵积累的经验，可在发明室经验接收器领取）
func handleNonoHelpExp(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.ExpPool < 0 {
		user.ExpPool = 0
	}
	const addPerCall = 3000
	user.ExpPool += addPerCall
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 9021, ctx.UserID, ctx.SeqID, []byte{})
	logger.Info(fmt.Sprintf("[9021] NONO帮助经验: 经验池 +%d 当前=%d", addPerCall, user.ExpPool))
}

// handleNonoMateChange CMD 9022 NONO心情变化
func handleNonoMateChange(ctx *gameserver.HandlerContext) {
	ctx.GameServer.SendResponse(ctx.ClientData, 9022, ctx.UserID, ctx.SeqID, []byte{})
}

// superNonoChipList 发明室「超能NoNo芯片领取」面板顺序：每页 3 个，共 4 页，按 (page-1)*3+slot 取
// 第 1 页：时空穿梭(700019)、经验加成(700007)、超能雷达(700009)，其余按常见芯片顺序
var superNonoChipList = []uint32{
	700019, 700007, 700009, // 第1页
	700001, 700002, 700003, // 第2页
	700004, 700005, 700006, // 第3页
	700008, 700010, 700011, // 第4页
}

// handleNonoGetChip CMD 9023 获取芯片（发明室超能NoNo芯片领取 / 任务送芯片）
// 请求: 4 字节时 body[0:4]=chipType（或 itemId-700000）；20 字节时 body[12:16]=页(1-based)，body[16:20]=槽位(0-based) 或全局序号
// 响应: 0(4)*3 + len(4) + [itemId(4)+count(4)]... 客户端 onGetChip 按此解析
func handleNonoGetChip(ctx *gameserver.HandlerContext) {
	var chipItemID uint32
	if len(ctx.Body) >= 20 {
		page := binary.BigEndian.Uint32(ctx.Body[12:16])
		slotOrIndex := binary.BigEndian.Uint32(ctx.Body[16:20])
		// 发明室面板：每页 3 个，全局序号 = (page-1)*3 + slot（点击第几页第几个）
		globalIndex := (page-1)*3 + slotOrIndex
		if globalIndex < uint32(len(superNonoChipList)) {
			chipItemID = superNonoChipList[globalIndex]
		} else if slotOrIndex >= 1 && slotOrIndex <= 60 {
			chipItemID = 700000 + slotOrIndex
		} else {
			chipItemID = superNonoChipList[0]
		}
	} else if len(ctx.Body) >= 4 {
		chipType := binary.BigEndian.Uint32(ctx.Body[0:4])
		if chipType >= 1 && chipType <= 60 {
			chipItemID = 700000 + chipType
		} else {
			chipItemID = 700005 // 任务送跟随模式芯片等默认
		}
	} else {
		chipItemID = 700005
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	// 添加到背包
	itemKey := strconv.FormatUint(uint64(chipItemID), 10)
	if item, hasItem := user.Items[itemKey]; hasItem {
		item.Count++
		user.Items[itemKey] = item
	} else {
		user.Items[itemKey] = userdb.Item{
			Count:      1,
			ExpireTime: 0x057E40,
		}
	}

	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	// 响应: 0(4)*3 + len(4) + [itemId(4) + count(4)] 与客户端 onGetChip 一致（itemId 为 70000x）
	body := make([]byte, 24)
	binary.BigEndian.PutUint32(body[0:4], 0)
	binary.BigEndian.PutUint32(body[4:8], 0)
	binary.BigEndian.PutUint32(body[8:12], 0)
	binary.BigEndian.PutUint32(body[12:16], 1)
	binary.BigEndian.PutUint32(body[16:20], uint32(chipItemID))
	binary.BigEndian.PutUint32(body[20:24], 1)
	ctx.GameServer.SendResponse(ctx.ClientData, 9023, ctx.UserID, ctx.SeqID, body)
}

// handleNonoAddEnergyMate CMD 9024 增加能量心情
func handleNonoAddEnergyMate(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.Nono.Power+10000 > 100000 {
		user.Nono.Power = 100000
	} else {
		user.Nono.Power += 10000
	}
	if user.Nono.Mate+10000 > 100000 {
		user.Nono.Mate = 100000
	} else {
		user.Nono.Mate += 10000
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 9024, ctx.UserID, ctx.SeqID, []byte{})
}

// handleGetDiamond CMD 9025 获取钻石
func handleGetDiamond(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], 9999) // 钻石数量
	ctx.GameServer.SendResponse(ctx.ClientData, 9025, ctx.UserID, ctx.SeqID, body)
}

// handleNonoAddExp CMD 9026 增加NONO经验
func handleNonoAddExp(ctx *gameserver.HandlerContext) {
	ctx.GameServer.SendResponse(ctx.ClientData, 9026, ctx.UserID, ctx.SeqID, []byte{})
}

// handleNonoIsInfo CMD 9027 NONO是否有信息
func handleNonoIsInfo(ctx *gameserver.HandlerContext) {
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], 1) // 有NONO
	ctx.GameServer.SendResponse(ctx.ClientData, 9027, ctx.UserID, ctx.SeqID, body)
}

// handleNieoLogin CMD 80001 超能NONO登录/状态检查
func handleNieoLogin(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	currentTime := uint32(time.Now().Unix())

	// 检查是否需要激活超能NONO
	needActivate := false
	if user.Nono.SuperNono == 0 {
		needActivate = true
	} else if user.Nono.VipEndTime > 0 && user.Nono.VipEndTime < int64(currentTime) {
		needActivate = true
	}

	if needActivate {
		// 激活超能NONO（默认30天）
		durationDays := 30
		endTime := currentTime + uint32(durationDays*24*60*60)
		user.Nono.VipEndTime = int64(endTime)
		if user.Nono.SuperLevel < 1 {
			user.Nono.SuperLevel = 1
		}
		if user.Nono.SuperStage < 1 {
			user.Nono.SuperStage = 1
		}
		// 根据超能等级自动更新形态（超能等级模型），而不是硬编码为1
		updateSuperNonoTypeByLevel(user)
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
		// 同步更新资源服缓存中的超能等级
		registerSuperNonoToCache(ctx, user)

		// 发送80002激活成功通知
		message := fmt.Sprintf("成功激活超能NONO！\n到期时间:%s", time.Unix(int64(endTime), 0).Format("2006-01-02"))
		msgBytes := []byte(message)
		notifyBody := make([]byte, 4+len(msgBytes))
		binary.BigEndian.PutUint32(notifyBody[0:4], uint32(len(msgBytes)))
		copy(notifyBody[4:], msgBytes)
		ctx.GameServer.SendResponse(ctx.ClientData, 80002, ctx.UserID, 0, notifyBody)

		// 发送VIP_CO (8006) 更新
		vipBody := make([]byte, 16)
		binary.BigEndian.PutUint32(vipBody[0:4], uint32(ctx.UserID))
		binary.BigEndian.PutUint32(vipBody[4:8], 2) // vipFlag=2 (激活超能NONO)
		binary.BigEndian.PutUint32(vipBody[8:12], uint32(user.Nono.AutoCharge))
		binary.BigEndian.PutUint32(vipBody[12:16], endTime)
		ctx.GameServer.SendResponse(ctx.ClientData, 8006, ctx.UserID, 0, vipBody)
	}

	// 发送80001状态响应
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], 0) // status=0 正常/已激活
	ctx.GameServer.SendResponse(ctx.ClientData, 80001, ctx.UserID, ctx.SeqID, body)
}

// ==================== 物品/背包系统命令处理器 ====================

// registerItemHandlers 注册物品/背包系统命令处理器
func registerItemHandlers(gs *gameserver.GameServer) {
	gs.RegisterCommandHandler(2601, handleItemBuy)               // 购买物品
	gs.RegisterCommandHandler(2602, handleItemSale)              // 出售物品
	gs.RegisterCommandHandler(2604, handleChangeCloth)           // 更换服装
	gs.RegisterCommandHandler(2605, handleItemList)              // 物品列表
	gs.RegisterCommandHandler(2606, handleMultiItemBuy)          // 批量购买
	gs.RegisterCommandHandler(2607, handleItemExpend)            // 消耗物品
	gs.RegisterCommandHandler(2609, handleEquipUpdate)           // 装备升级
	gs.RegisterCommandHandler(2901, handleExchangeClothComplete) // 兑换服装完成
}

// addClothIfNeeded 若 itemID 为服装/套装（100000-199999），则加入 user.Clothes，避免“我的服装”中不显示
func addClothIfNeeded(user *userdb.GameData, itemID int) {
	if itemID < 100000 || itemID > 199999 {
		return
	}
	for _, cid := range user.Clothes {
		if cid == itemID {
			return
		}
	}
	user.Clothes = append(user.Clothes, itemID)
}

// handleItemBuy CMD 2601 购买物品
func handleItemBuy(ctx *gameserver.HandlerContext) {
	var itemID, count uint32
	if len(ctx.Body) >= 8 {
		itemID = binary.BigEndian.Uint32(ctx.Body[0:4])
		count = binary.BigEndian.Uint32(ctx.Body[4:8])
	}
	if count == 0 {
		count = 1
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	itemKey := strconv.FormatUint(uint64(itemID), 10)

	// 检查唯一性（简化：如果已拥有唯一物品，返回错误）
	// 这里应该检查items.xml中的唯一性配置，暂时简化处理

	// 价格检查（简化：默认价格100）
	unitPrice := 100
	totalCost := unitPrice * int(count)

	if user.Coins < totalCost {
		// 返回错误码 103107 (赛尔豆余额不足)
		ctx.GameServer.SendResponse(ctx.ClientData, 2601, ctx.UserID, ctx.SeqID, []byte{})
		return
	}

	// 扣钱
	user.Coins -= totalCost

	// 添加物品
	if item, hasItem := user.Items[itemKey]; hasItem {
		item.Count += int(count)
		user.Items[itemKey] = item
	} else {
		user.Items[itemKey] = userdb.Item{
			Count:      int(count),
			ExpireTime: 0x057E40,
		}
	}
	// 服装/套装（100000-199999）需同时加入 Clothes，否则“我的服装”中不显示
	addClothIfNeeded(user, int(itemID))
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	// 响应: cash(4) + itemID(4) + itemNum(4) + itemLevel(4)
	body := make([]byte, 16)
	binary.BigEndian.PutUint32(body[0:4], uint32(user.Coins))
	binary.BigEndian.PutUint32(body[4:8], itemID)
	binary.BigEndian.PutUint32(body[8:12], count)
	binary.BigEndian.PutUint32(body[12:16], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2601, ctx.UserID, ctx.SeqID, body)
}

// handleItemSale CMD 2602 出售物品
func handleItemSale(ctx *gameserver.HandlerContext) {
	var itemID, count uint32
	if len(ctx.Body) >= 8 {
		itemID = binary.BigEndian.Uint32(ctx.Body[0:4])
		count = binary.BigEndian.Uint32(ctx.Body[4:8])
	}
	if count == 0 {
		count = 1
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	itemKey := strconv.FormatUint(uint64(itemID), 10)

	if item, hasItem := user.Items[itemKey]; hasItem {
		item.Count -= int(count)
		if item.Count <= 0 {
			delete(user.Items, itemKey)
		} else {
			user.Items[itemKey] = item
		}
		// 给予金币（简化：默认价格50）
		user.Coins += int(count) * 50
	}

	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	ctx.GameServer.SendResponse(ctx.ClientData, 2602, ctx.UserID, ctx.SeqID, []byte{})
}

// handleChangeCloth CMD 2604 更换服装
func handleChangeCloth(ctx *gameserver.HandlerContext) {
	var clothCount uint32
	if len(ctx.Body) >= 4 {
		clothCount = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	clothIDs := make([]uint32, 0, clothCount)
	for i := uint32(0); i < clothCount && len(ctx.Body) >= int(4+4*(i+1)); i++ {
		clothID := binary.BigEndian.Uint32(ctx.Body[4+i*4 : 8+i*4])
		clothIDs = append(clothIDs, clothID)
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	user.Clothes = make([]int, len(clothIDs))
	for i, id := range clothIDs {
		user.Clothes[i] = int(id)
	}

	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	// 响应: userID(4) + clothCount(4) + [clothId(4) + clothType(4)]...
	body := make([]byte, 8+len(clothIDs)*8)
	binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID))
	binary.BigEndian.PutUint32(body[4:8], uint32(len(clothIDs)))
	off := 8
	for _, clothID := range clothIDs {
		binary.BigEndian.PutUint32(body[off:off+4], clothID)
		binary.BigEndian.PutUint32(body[off+4:off+8], 0) // clothType
		off += 8
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 2604, ctx.UserID, ctx.SeqID, body)
}

// handleMultiItemBuy CMD 2606 批量购买
func handleMultiItemBuy(ctx *gameserver.HandlerContext) {
	var itemCount uint32
	if len(ctx.Body) >= 4 {
		itemCount = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	itemIDs := make([]uint32, 0, itemCount)
	for i := uint32(0); i < itemCount && len(ctx.Body) >= int(4+4*(i+1)); i++ {
		itemID := binary.BigEndian.Uint32(ctx.Body[4+i*4 : 8+i*4])
		itemIDs = append(itemIDs, itemID)
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// 计算总价
	totalCost := 0
	validItems := make([]uint32, 0)
	for _, itemID := range itemIDs {
		itemKey := strconv.FormatUint(uint64(itemID), 10)
		// 唯一性检查（简化）
		if _, hasItem := user.Items[itemKey]; !hasItem {
			totalCost += 100 // 默认价格
			validItems = append(validItems, itemID)
		}
	}

	if user.Coins < totalCost {
		// 返回错误码
		body := make([]byte, 8)
		binary.BigEndian.PutUint32(body[0:4], 10016) // 错误码
		binary.BigEndian.PutUint32(body[4:8], uint32(user.Coins))
		ctx.GameServer.SendResponse(ctx.ClientData, 2606, ctx.UserID, ctx.SeqID, body)
		return
	}

	// 扣钱
	user.Coins -= totalCost

	// 添加物品
	for _, itemID := range validItems {
		itemKey := strconv.FormatUint(uint64(itemID), 10)
		if item, hasItem := user.Items[itemKey]; hasItem {
			item.Count++
			user.Items[itemKey] = item
		} else {
			user.Items[itemKey] = userdb.Item{
				Count:      1,
				ExpireTime: 0x057E40,
			}
		}
		// 服装/套装（100000-199999）需同时加入 Clothes
		addClothIfNeeded(user, int(itemID))
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	// 响应: result(4) + remainCoins(4)
	body := make([]byte, 8)
	binary.BigEndian.PutUint32(body[0:4], 0) // result=0 成功
	binary.BigEndian.PutUint32(body[4:8], uint32(user.Coins))
	ctx.GameServer.SendResponse(ctx.ClientData, 2606, ctx.UserID, ctx.SeqID, body)
}

// handleItemExpend CMD 2607 消耗物品
func handleItemExpend(ctx *gameserver.HandlerContext) {
	var itemID, count uint32
	if len(ctx.Body) >= 8 {
		itemID = binary.BigEndian.Uint32(ctx.Body[0:4])
		count = binary.BigEndian.Uint32(ctx.Body[4:8])
	}
	if count == 0 {
		count = 1
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	itemKey := strconv.FormatUint(uint64(itemID), 10)

	// 专用：谱尼碎片合成精元逻辑
	// 当背包中集齐 8 个「谱尼的XX裂片」(400651~400658) 时，在背包中点击任意一个碎片（CMD 2607），
	// 不消耗该碎片本身，而是一次性消耗 8 个碎片各 1 个，合成「谱尼的精元」(400150)。
	if _, isPuniFrag := puniFragmentItemIDsRev[int(itemID)]; isPuniFrag {
		if user.Items == nil {
			user.Items = make(map[string]userdb.Item)
		}
		// 检查是否已拥有谱尼精元，若已有则不再合成，按普通消耗逻辑处理
		soulKey := strconv.Itoa(puniSoulItemID)
		hasSoul := false
		if it, ok := user.Items[soulKey]; ok && it.Count > 0 {
			hasSoul = true
		}
		if !hasSoul {
			// 检查 1~8 号碎片是否全部至少 1 个
			allFragmentsOwned := true
			for d := 1; d <= 8; d++ {
				fragID, ok := puniFragmentItemIDs[d]
				if !ok {
					allFragmentsOwned = false
					break
				}
				key := strconv.Itoa(fragID)
				if it, ok := user.Items[key]; !ok || it.Count <= 0 {
					allFragmentsOwned = false
					break
				}
			}
			if allFragmentsOwned {
				// 消耗 8 个碎片各 1 个
				for d := 1; d <= 8; d++ {
					fragID := puniFragmentItemIDs[d]
					key := strconv.Itoa(fragID)
					if it, ok := user.Items[key]; ok {
						it.Count--
						if it.Count <= 0 {
							delete(user.Items, key)
						} else {
							user.Items[key] = it
						}
					}
				}
				// 发放 1 个谱尼的精元
				if it, ok := user.Items[soulKey]; ok {
					it.Count++
					user.Items[soulKey] = it
				} else {
					user.Items[soulKey] = userdb.Item{Count: 1, ExpireTime: 0x057E40}
				}
				if ctx.GameServer.UserDB != nil {
					ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
				}
				logger.Info(fmt.Sprintf("[2607] 使用谱尼碎片触发合成: 消耗 8 碎片，获得『谱尼的精元』(ItemID=%d)", puniSoulItemID))
				ctx.GameServer.SendResponse(ctx.ClientData, 2607, ctx.UserID, ctx.SeqID, []byte{})
				return
			}
		}
		// 未集齐 8 碎片 或 已有精元：不做任何实际消耗，直接返回成功（与客户端“点击使用但无效果”一致）
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
		ctx.GameServer.SendResponse(ctx.ClientData, 2607, ctx.UserID, ctx.SeqID, []byte{})
		return
	}

	// 默认：普通消耗逻辑
	if item, hasItem := user.Items[itemKey]; hasItem {
		item.Count -= int(count)
		if item.Count <= 0 {
			delete(user.Items, itemKey)
		} else {
			user.Items[itemKey] = item
		}
	}

	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	ctx.GameServer.SendResponse(ctx.ClientData, 2607, ctx.UserID, ctx.SeqID, []byte{})
}

// handleEquipUpdate CMD 2609 装备升级
func handleEquipUpdate(ctx *gameserver.HandlerContext) {
	ctx.GameServer.SendResponse(ctx.ClientData, 2609, ctx.UserID, ctx.SeqID, []byte{})
}

// handleExchangeClothComplete CMD 2901 兑换服装完成
func handleExchangeClothComplete(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	// 解析 exchangeID(4) + itemId(4)，将兑换得到的服装加入 Clothes
	itemID := 0
	if len(ctx.Body) >= 4 {
		_ = binary.BigEndian.Uint32(ctx.Body[0:4]) // exchangeID
	}
	if len(ctx.Body) >= 8 {
		itemID = int(binary.BigEndian.Uint32(ctx.Body[4:8]))
	}
	if itemID > 0 {
		addClothIfNeeded(user, itemID)
		// 若为服装则也记入 Items 数量（部分客户端按物品显示）
		if itemID >= 100000 && itemID <= 199999 {
			itemKey := strconv.Itoa(itemID)
			if it, has := user.Items[itemKey]; has {
				it.Count++
				user.Items[itemKey] = it
			} else {
				user.Items[itemKey] = userdb.Item{Count: 1, ExpireTime: 0x057E40}
			}
		}
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
	}
	// 响应: ret(4) + itemId(4) + count(4)
	body := make([]byte, 12)
	binary.BigEndian.PutUint32(body[0:4], 0)
	binary.BigEndian.PutUint32(body[4:8], uint32(itemID))
	binary.BigEndian.PutUint32(body[8:12], 1)
	ctx.GameServer.SendResponse(ctx.ClientData, 2901, ctx.UserID, ctx.SeqID, body)
}

// handleEscapeFight CMD 2410 逃跑（最小可用版本）
// 对齐 Lua: fight_handlers.handleEscapeFight
func handleEscapeFight(ctx *gameserver.HandlerContext) {
	// 响应 2410: result(4) = 1（成功）
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], 1)

	ctx.GameServer.SendResponse(ctx.ClientData, 2410, ctx.UserID, ctx.SeqID, body)

	// 如果当前没有有效战斗状态，就不要再发 2506/刷新地图，避免重复“退出战斗 → 重进地图”的流程
	ctx.GameServer.BattleMu.RLock()
	battle, exists := ctx.GameServer.BattleStates[ctx.UserID]
	active := exists && battle != nil && battle.IsActive
	ctx.GameServer.BattleMu.RUnlock()
	if !active {
		return
	}

	// 结束战斗
	overBody := buildFightOverInfo(0, 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2506, ctx.UserID, ctx.SeqID, overBody)
	pushMapOgreListAfterFightOver(ctx, false)

	// 清理战斗状态（如果有的话）
	ctx.GameServer.BattleMu.Lock()
	delete(ctx.GameServer.BattleStates, ctx.UserID)
	ctx.GameServer.BattleMu.Unlock()

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
}

// handlePeopleWalk CMD 2101 人物移动
// 对齐 Lua: map_handlers.handlePeopleWalk
// 请求: walkType(4) + x(4) + y(4) + amfLen(4) + amfData
// 响应: walkType(4) + userID(4) + x(4) + y(4) + amfLen(4) + amfData
func handlePeopleWalk(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	var walkType, x, y, amfLen uint32
	var amfData []byte

	if len(ctx.Body) >= 16 {
		walkType = binary.BigEndian.Uint32(ctx.Body[0:4])
		x = binary.BigEndian.Uint32(ctx.Body[4:8])
		y = binary.BigEndian.Uint32(ctx.Body[8:12])
		amfLen = binary.BigEndian.Uint32(ctx.Body[12:16])

		if len(ctx.Body) >= 16+int(amfLen) {
			amfData = ctx.Body[16 : 16+int(amfLen)]
		}
	}

	// 更新用户位置
	user.PosX = int(x)
	user.PosY = int(y)
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	// 构建响应
	body := make([]byte, 20+len(amfData))
	binary.BigEndian.PutUint32(body[0:4], walkType)
	binary.BigEndian.PutUint32(body[4:8], uint32(ctx.UserID))
	binary.BigEndian.PutUint32(body[8:12], x)
	binary.BigEndian.PutUint32(body[12:16], y)
	binary.BigEndian.PutUint32(body[16:20], amfLen)
	if len(amfData) > 0 {
		copy(body[20:], amfData)
	}

	// 发送响应给请求者，并广播给同地图其他玩家
	ctx.GameServer.SendResponse(ctx.ClientData, 2101, ctx.UserID, ctx.SeqID, body)
	if user.MapID > 0 {
		ctx.GameServer.BroadcastToMap(user.MapID, ctx.UserID, 2101, body)
	}
	logger.Info(fmt.Sprintf("[2101] 人物移动: UID=%d X=%d Y=%d", ctx.UserID, x, y))
}

// handlePetShow CMD 2305 展示精灵（跟随面板）
// 对齐 Lua: pet_handlers.handlePetShow
// 请求: catchTime(4) + flag(4)
// 响应: userID(4) + catchTime(4) + petID(4) + flag(4) + dv(4) + skinID(4)
func handlePetShow(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	var reqCatchTime, reqFlag uint32
	if len(ctx.Body) >= 8 {
		reqCatchTime = binary.BigEndian.Uint32(ctx.Body[0:4])
		reqFlag = binary.BigEndian.Uint32(ctx.Body[4:8])
	}

	// 确定要展示的精灵
	var petID, catchTime, petDV uint32 = 7, 0, 31

	// 如果指定了 catchTime，查找对应的精灵
	if reqCatchTime > 0 {
		catchTime = reqCatchTime
		// 先从背包查找
		for _, pet := range user.Pets {
			if uint32(pet.CatchTime) == reqCatchTime {
				petID = uint32(pet.ID)
				petDV = uint32(pet.DV)
				if petDV == 0 {
					petDV = 31
				}
				break
			}
		}
		// 如果背包没找到，从仓库查找
		if petID == 7 && user.StoragePets != nil {
			for _, pet := range user.StoragePets {
				if uint32(pet.CatchTime) == reqCatchTime {
					petID = uint32(pet.ID)
					petDV = uint32(pet.DV)
					if petDV == 0 {
						petDV = 31
					}
					break
				}
			}
		}
	} else {
		// 使用已保存的跟随精灵，否则第一只
		if user.FollowPetCatchTime > 0 {
			for _, pet := range user.Pets {
				if pet.CatchTime == user.FollowPetCatchTime {
					petID = uint32(pet.ID)
					catchTime = uint32(pet.CatchTime)
					petDV = uint32(pet.DV)
					if petDV == 0 {
						petDV = 31
					}
					break
				}
			}
			if petID == 7 && user.StoragePets != nil {
				for _, pet := range user.StoragePets {
					if pet.CatchTime == user.FollowPetCatchTime {
						petID = uint32(pet.ID)
						catchTime = uint32(pet.CatchTime)
						petDV = uint32(pet.DV)
						if petDV == 0 {
							petDV = 31
						}
						break
					}
				}
			}
		}
		if catchTime == 0 && len(user.Pets) > 0 {
			petID = uint32(user.Pets[0].ID)
			catchTime = uint32(user.Pets[0].CatchTime)
			petDV = uint32(user.Pets[0].DV)
			if petDV == 0 {
				petDV = 31
			}
		}
		if catchTime == 0 {
			catchTime = 0x69686700 + petID
		}
	}

	// 保存当前跟随精灵，供 buildPeopleInfo 与 2003 同步给其他玩家
	if reqCatchTime > 0 {
		if reqFlag == 1 {
			user.FollowPetCatchTime = int(reqCatchTime)
		} else {
			user.FollowPetCatchTime = 0
		}
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
	}

	// 构建响应
	body := make([]byte, 24)
	binary.BigEndian.PutUint32(body[0:4], uint32(ctx.UserID))
	binary.BigEndian.PutUint32(body[4:8], catchTime)
	binary.BigEndian.PutUint32(body[8:12], petID)
	binary.BigEndian.PutUint32(body[12:16], reqFlag)
	binary.BigEndian.PutUint32(body[16:20], petDV)
	binary.BigEndian.PutUint32(body[20:24], 0) // skinID = 0

	ctx.GameServer.SendResponse(ctx.ClientData, 2305, ctx.UserID, ctx.SeqID, body)
	if user.MapID > 0 {
		ctx.GameServer.BroadcastToMap(user.MapID, ctx.UserID, 2305, body)
	}
	logger.Info(fmt.Sprintf("[2305] 展示精灵: PetID=%d CatchTime=%d DV=%d (广播同地图)", petID, catchTime, petDV))
}

// handlePetGetExp CMD 2319 获取经验池经验
// 对齐 Lua: pet_advanced_handlers.handlePetGetExp
// 响应: expPool(4)
func handlePetGetExp(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	expPool := user.ExpPool
	if expPool < 0 {
		expPool = 0
	}

	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], uint32(expPool))

	ctx.GameServer.SendResponse(ctx.ClientData, 2319, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2319] 获取经验池经验: ExpPool=%d", expPool))
}

// handleGetMyExperienceComplete CMD 3011 发明室经验接收器 - 教官查看未领取经验（GetExperienceInfo.getExp）
// 响应: getExp(4) 未领取的经验值
func handleGetMyExperienceComplete(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	expPool := user.ExpPool
	if expPool < 0 {
		expPool = 0
	}
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], uint32(expPool))
	ctx.GameServer.SendResponse(ctx.ClientData, 3011, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[3011] 教官查看未领取经验: ExpPool=%d", expPool))
}

// handleMyExperiencePondComplete CMD 3009 发明室经验接收器 - 学员查询教官积累经验值（MyExperiencePondInfo.getMyExp）
// 响应: getMyExp(4) 教官积累的经验值（简化：当前用户经验池；若为 0 则首次赠送测试经验以便能领取）
func handleMyExperiencePondComplete(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	expPool := user.ExpPool
	if expPool < 0 {
		expPool = 0
	}
	// 经验池为 0 时赠送一次可领取经验，避免玩家点经验接收器永远领不到（NoNo 积累 / 任务奖励未触发时）
	const welcomeExp = 5000
	if expPool == 0 {
		user.ExpPool = welcomeExp
		expPool = welcomeExp
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
		logger.Info(fmt.Sprintf("[3009] 经验池为空，赠送首次可领取经验: %d", welcomeExp))
	}
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], uint32(expPool))
	ctx.GameServer.SendResponse(ctx.ClientData, 3009, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[3009] 查询经验池可领取: ExpPool=%d", expPool))
}

// handleExperienceSharedComplete CMD 3007 发明室经验接收器 - 领取经验并平均分配给背包所有精灵（ExperienceSharedInfo.getFraction）
// 响应: getFraction(4) 本次共分配给精灵的总经验
func handleExperienceSharedComplete(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	totalPool := user.ExpPool
	if totalPool < 0 {
		totalPool = 0
	}
	n := len(user.Pets)
	if n == 0 {
		user.ExpPool = 0
		if ctx.GameServer.UserDB != nil {
			ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		}
		body := make([]byte, 4)
		binary.BigEndian.PutUint32(body[0:4], 0)
		ctx.GameServer.SendResponse(ctx.ClientData, 3007, ctx.UserID, ctx.SeqID, body)
		logger.Info("[3007] 领取经验但背包无精灵，已清空经验池")
		return
	}
	perPet := totalPool / n
	user.ExpPool = 0
	petMgr := gamepets.GetInstance()
	for idx := range user.Pets {
		if perPet <= 0 {
			continue
		}
		p := &user.Pets[idx]
		p.Exp += perPet
		if p.Level <= 0 {
			p.Level = 1
		}
		if p.Level > 100 {
			p.Level = 100
			p.Exp = 0
			continue
		}
		oldLevel := p.Level
		for {
			expInfo := petMgr.GetExpInfo(p.ID, p.Level, p.Exp)
			if p.Level >= 100 {
				p.Level = 100
				p.Exp = 0
				break
			}
			if p.Exp < expInfo.NextLevelExp {
				break
			}
			p.Exp -= expInfo.NextLevelExp
			p.Level++
		}
		canEvolve, _, evolveTo := petMgr.CanEvolve(p.ID, p.Level, false)
		if canEvolve && evolveTo > 0 {
			p.ID = evolveTo
			p.Exp = 0
		}
		if p.Level > oldLevel {
			var newSkillIDs []int
			seen := make(map[int]bool)
			for lv := oldLevel + 1; lv <= p.Level; lv++ {
				for _, sid := range petMgr.GetSkillsLearnedAtLevel(p.ID, lv) {
					if sid > 0 && !seen[sid] {
						seen[sid] = true
						newSkillIDs = append(newSkillIDs, sid)
					}
				}
			}
			if len(newSkillIDs) > 0 {
				skillBody := buildNoteUpdateSkill(uint32(p.CatchTime), p.ID, oldLevel, newSkillIDs)
				ctx.GameServer.SendResponse(ctx.ClientData, 2507, ctx.UserID, ctx.SeqID, skillBody)
			}
		}
		ev := gamepets.ClampAndCapEV(p.GetEVStats())
		stats := petMgr.GetStats(p.ID, p.Level, p.DV, ev, p.Nature)
		propBody := buildNoteUpdateProp(uint32(p.CatchTime), p.ID, p.Level, p.Exp,
			stats.MaxHP, stats.Attack, stats.Defence, stats.SpAtk, stats.SpDef, stats.Speed, ev)
		ctx.GameServer.SendResponse(ctx.ClientData, 2508, ctx.UserID, ctx.SeqID, propBody)
		fullBody := buildFullPetInfo(*p)
		ctx.GameServer.SendResponse(ctx.ClientData, 2301, ctx.UserID, ctx.SeqID, fullBody)
	}
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}
	totalGiven := perPet * n
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], uint32(totalGiven))
	ctx.GameServer.SendResponse(ctx.ClientData, 3007, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[3007] 经验接收器领取: 总分配=%d 每只=%d 精灵数=%d", totalGiven, perPet, n))
}

// handleTalkCount CMD 2701 对话计数（矿物挖掘/气体收集今日次数；发明室经验接收器 CateId=2002）
// 请求: cateId(4)，前端 DayOreCount.sendToServer(_type) 发明室经验接收器发 2002
// 响应: MiningCountInfo 仅 miningCount(4)，0=今日未领 1=今日已领
func handleTalkCount(ctx *gameserver.HandlerContext) {
	var cateId uint32
	if len(ctx.Body) >= 4 {
		cateId = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	count := uint32(0)
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	today := int64(time.Now().Unix() / 86400)
	if user.MiningDate == today && user.MiningCount != nil {
		if c, ok := user.MiningCount[int(cateId)]; ok {
			count = uint32(c)
		}
	}

	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], count)
	ctx.GameServer.SendResponse(ctx.ClientData, 2701, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2701] 对话计数: CateId=%d Count=%d", cateId, count))
}

// 矿物/气体 cateId 对应物品 ID。bMiningStr "1"=黄晶矿(黄金矿)：map10/21/15/325 对应 1/2/3/17 一律发黄晶矿 400001；bMiningStr "2"=甲烷：4/5/6/18 发甲烷 400002
var miningCateToItemID = map[uint32]uint32{
	1: 400001, 2: 400001, 3: 400001, 4: 400002, 5: 400002, 6: 400002, // 1 2 3 17 黄晶矿(黄金矿) 4 5 6 18 甲烷
	7: 400009, 8: 400009, 9: 400009, 10: 400009, 11: 400009, 12: 400009,
	13: 400001, 14: 400009, 15: 400009, 16: 400009, 17: 400001, 18: 400002,
	19: 400001, 20: 400001, 21: 400001, 22: 400001, // 海洋星二层等黄金矿(黄晶矿 400001)，cateId 19-22 发黄金矿
}

// handleTalkCate CMD 2702 对话分类（发放领取物品 / 矿物挖掘、气体收集结果）
// 请求: cateId(4)。响应: DayTalkInfo = cateCount(4) + cateCount×CateInfo(id,count) + outCount(4) + outCount×CateInfo(id,count)
func handleTalkCate(ctx *gameserver.HandlerContext) {
	var cateId uint32
	if len(ctx.Body) >= 4 {
		cateId = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.Items == nil {
		user.Items = map[string]userdb.Item{}
	}

	var outItemID, outCount uint32

	// 发明室(107) 经验接收器 CateId=2002：每日一次 10 万经验放入 NoNo 经验存储器(ExpPool)，客户端提示“恭喜你获得了 X 积累经验”
	// 船长室 超能 NONO 赛尔豆领取 CateId=2001：每日一次 10 万赛尔豆
	const captainRoomCoinsCateId = 2001
	const captainRoomCoinsPerDay = 100000
	const inventionRoomExpCateId = 2002
	const inventionRoomExpPerDay = 100000
	if cateId == captainRoomCoinsCateId {
		today := int64(time.Now().Unix() / 86400)
		if user.MiningDate != today {
			user.MiningCount = map[int]int{}
			user.MiningDate = today
		}
		if user.MiningCount == nil {
			user.MiningCount = map[int]int{}
		}
		if user.MiningCount[captainRoomCoinsCateId] >= 1 {
			outItemID = 0
			outCount = 0
			logger.Info("[2702] 船长室每日赛尔豆: 今日已领过，不再发放")
		} else {
			user.MiningCount[captainRoomCoinsCateId] = 1
			user.Coins += captainRoomCoinsPerDay
			// 客户端用 outList[0].id/count 显示；id=1 作为“赛尔豆/金币”类型，count 为数值用于提示
			outItemID = 1
			outCount = uint32(captainRoomCoinsPerDay)
			logger.Info(fmt.Sprintf("[2702] 船长室每日赛尔豆: 发放 %d 当前 Coins=%d", captainRoomCoinsPerDay, user.Coins))
		}
	} else if cateId == inventionRoomExpCateId {
		today := int64(time.Now().Unix() / 86400)
		if user.MiningDate != today {
			user.MiningCount = map[int]int{}
			user.MiningDate = today
		}
		if user.MiningCount == nil {
			user.MiningCount = map[int]int{}
		}
		if user.MiningCount[inventionRoomExpCateId] >= 1 {
			outItemID = 0
			outCount = 0
			logger.Info("[2702] 发明室经验接收器: 今日已领过，不再发放")
		} else {
			user.MiningCount[inventionRoomExpCateId] = 1
			if user.ExpPool < 0 {
				user.ExpPool = 0
			}
			user.ExpPool += inventionRoomExpPerDay
			// 客户端用 outList[0].id/count 显示；id=3 表示“积累经验”，count 为数值用于提示
			outItemID = 3
			outCount = uint32(inventionRoomExpPerDay)
			logger.Info(fmt.Sprintf("[2702] 发明室经验接收器: 发放 %d 经验到 NoNo 经验存储器，当前 ExpPool=%d", inventionRoomExpPerDay, user.ExpPool))
		}
	} else if cateId >= 1 && cateId <= 22 {
		today := int64(time.Now().Unix() / 86400)
		if user.MiningDate != today {
			user.MiningCount = map[int]int{}
			user.MiningDate = today
		}
		if user.MiningCount == nil {
			user.MiningCount = map[int]int{}
		}
		user.MiningCount[int(cateId)]++
		itemID := miningCateToItemID[cateId]
		if itemID == 0 {
			itemID = 400001
		}
		// 每次采集数量随机 2-5
		n := uint32(rand.Intn(4) + 2) // [2,5]
		itemKey := strconv.FormatUint(uint64(itemID), 10)
		if it, ok := user.Items[itemKey]; ok {
			it.Count += int(n)
			user.Items[itemKey] = it
		} else {
			user.Items[itemKey] = userdb.Item{Count: int(n), ExpireTime: 0}
		}
		outItemID = itemID
		outCount = n
		logger.Info(fmt.Sprintf("[2702] 矿物/气体: CateId=%d 今日次数+1 发放 itemId=%d x%d", cateId, itemID, n))
	} else if cateId == 2051 {
		// Map 103: 扭蛋牌 x2
		itemID := uint32(400501)
		itemKey := strconv.FormatUint(uint64(itemID), 10)
		if it, ok := user.Items[itemKey]; ok {
			it.Count += 2
			user.Items[itemKey] = it
		} else {
			user.Items[itemKey] = userdb.Item{Count: 2, ExpireTime: 0x057E40}
		}
		outItemID = itemID
		outCount = 2
		logger.Info("[2702] TALK_CATE 发放物品: itemId=400501 x2")
	}

	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	// DayTalkInfo: cateCount(4) + cateCount×CateInfo(8) + outCount(4) + outCount×CateInfo(8)
	// 客户端 outCount=条目数，每条 CateInfo(id,count) 表示一种物品及数量；采集只发 1 条，count 为 2-5
	body := make([]byte, 0, 24)
	body = append(body, 0, 0, 0, 0) // cateCount = 0
	tmp := make([]byte, 4)
	if outCount > 0 {
		binary.BigEndian.PutUint32(tmp, 1) // outCount=1 条 CateInfo，客户端据此显示完成提示
		body = append(body, tmp...)
		binary.BigEndian.PutUint32(tmp, outItemID)
		body = append(body, tmp...)
		binary.BigEndian.PutUint32(tmp, outCount) // 该条数量 2-5
		body = append(body, tmp...)
	} else {
		binary.BigEndian.PutUint32(tmp, 0)
		body = append(body, tmp...)
	}
	ctx.GameServer.SendResponse(ctx.ClientData, 2702, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2702] 对话分类: CateId=%d", cateId))
}

// handlePetSetExp CMD 2318 从经验池分配经验给精灵
// 对齐 Lua: pet_advanced_handlers.handlePetSetExp
// 请求: catchTime(4) + expAmount(4)
// 响应: newPoolExp(4)
func handlePetSetExp(ctx *gameserver.HandlerContext) {
	var catchTime, expAmount uint32
	if len(ctx.Body) >= 8 {
		catchTime = binary.BigEndian.Uint32(ctx.Body[0:4])
		expAmount = binary.BigEndian.Uint32(ctx.Body[4:8])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// 经验池扣减
	currentPool := user.ExpPool
	if currentPool < 0 {
		currentPool = 0
	}
	use := int(expAmount)
	if use < 0 {
		use = 0
	}
	if use > currentPool {
		use = currentPool
	}
	user.ExpPool = currentPool - use

	// 给对应精灵加经验并处理升级 & 进化（Pet.Exp 表示“当前等级已获得经验”，不是总经验）
	if catchTime != 0 && use > 0 {
		petMgr := gamepets.GetInstance()
		for idx := range user.Pets {
			if uint32(user.Pets[idx].CatchTime) != catchTime {
				continue
			}

			p := &user.Pets[idx]
			p.Exp += use
			if p.Level <= 0 {
				p.Level = 1
			}
			if p.Level > 100 {
				p.Level = 100
				p.Exp = 0
				break
			}

			oldLevel := p.Level
			// 自动升级：当当前等级经验 >= nextLevelExp 时升级
			for {
				expInfo := petMgr.GetExpInfo(p.ID, p.Level, p.Exp)
				if p.Level >= 100 {
					p.Level = 100
					p.Exp = 0
					break
				}
				if p.Exp < expInfo.NextLevelExp {
					break
				}
				p.Exp -= expInfo.NextLevelExp
				p.Level++
			}

			// 检查是否可以进化（直接进化型）
			canEvolve, _, evolveTo := petMgr.CanEvolve(p.ID, p.Level, false)
			if canEvolve && evolveTo > 0 {
				logger.Info(fmt.Sprintf("[2318] 精灵进化触发: PetID %d -> %d (Level=%d)", p.ID, evolveTo, p.Level))
				p.ID = evolveTo
				// 进化后当前等级经验清零
				p.Exp = 0
			}

			// 若升级了且精灵有新可学技能，发送 NOTE_UPDATE_SKILL(2507)，驱动更换技能窗口
			if p.Level > oldLevel {
				var newSkillIDs []int
				seen := make(map[int]bool)
				for lv := oldLevel + 1; lv <= p.Level; lv++ {
					for _, sid := range petMgr.GetSkillsLearnedAtLevel(p.ID, lv) {
						if sid > 0 && !seen[sid] {
							seen[sid] = true
							newSkillIDs = append(newSkillIDs, sid)
						}
					}
				}
				if len(newSkillIDs) > 0 {
					skillBody := buildNoteUpdateSkill(uint32(p.CatchTime), p.ID, oldLevel, newSkillIDs)
					ctx.GameServer.SendResponse(ctx.ClientData, 2507, ctx.UserID, ctx.SeqID, skillBody)
					logger.Info(fmt.Sprintf("[2318] 升级触发技能学习: PetID=%d Level=%d->%d 新技能=%v", p.ID, oldLevel, p.Level, newSkillIDs))
				}
			}

			// 发送 NOTE_UPDATE_PROP(2508)，驱动升级/属性变化面板
			ev := gamepets.ClampAndCapEV(p.GetEVStats())
			stats := petMgr.GetStats(p.ID, p.Level, p.DV, ev, p.Nature)
			propBody := buildNoteUpdateProp(uint32(p.CatchTime), p.ID, p.Level, p.Exp,
				stats.MaxHP, stats.Attack, stats.Defence, stats.SpAtk, stats.SpDef, stats.Speed, ev)
			ctx.GameServer.SendResponse(ctx.ClientData, 2508, ctx.UserID, ctx.SeqID, propBody)

			// 同步一次完整精灵信息（2301），方便背包面板立即刷新
			fullBody := buildFullPetInfo(*p)
			ctx.GameServer.SendResponse(ctx.ClientData, 2301, ctx.UserID, ctx.SeqID, fullBody)

			break
		}
	}

	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	resp := make([]byte, 4)
	binary.BigEndian.PutUint32(resp[0:4], uint32(user.ExpPool))
	ctx.GameServer.SendResponse(ctx.ClientData, 2318, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2318] 分配经验: catchTime=%d use=%d newPool=%d", catchTime, use, user.ExpPool))
}

// handlePetSkillSwitch CMD 2312 精灵技能切换（技能唤醒仪替换技能）
// 请求体常见格式：
//
//	A) catchTime(4) + count(4) + [slotIndex(4), skillId(4)]*count — 只替换指定槽位，一技能只能带一个
//	B) catchTime(4) + [skillId(4)]*4 — 四槽依次填，会去重（同技能只保留第一次出现）
//
// 响应: ret(4)，0 表示成功，1 表示未找到精灵
func handlePetSkillSwitch(ctx *gameserver.HandlerContext) {
	body := ctx.Body
	bodyLen := len(body)
	logger.Info(fmt.Sprintf("[2312] 收到技能切换请求 bodyLen=%d", bodyLen))
	if bodyLen > 0 {
		dump := packet.HexDump(body, fmt.Sprintf("[PACKET] CMD=2312 包体详情"))
		logger.Info(dump)
	}

	var catchTime uint32
	if bodyLen >= 4 {
		catchTime = binary.BigEndian.Uint32(body[0:4])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	var picked *userdb.Pet
	if catchTime != 0 {
		for i := range user.Pets {
			if uint32(user.Pets[i].CatchTime) == catchTime {
				picked = &user.Pets[i]
				break
			}
		}
		if picked == nil && user.StoragePets != nil {
			for i := range user.StoragePets {
				if uint32(user.StoragePets[i].CatchTime) == catchTime {
					picked = &user.StoragePets[i]
					break
				}
			}
		}
	}
	if picked == nil {
		logger.Info(fmt.Sprintf("[2312] 未找到精灵 catchTime=%d pets=%d", catchTime, len(user.Pets)))
		resp := make([]byte, 4)
		binary.BigEndian.PutUint32(resp[0:4], 1)
		ctx.GameServer.SendResponse(ctx.ClientData, 2312, ctx.UserID, ctx.SeqID, resp)
		return
	}

	petMgr := gamepets.GetInstance()
	skillMgr := gameskills.GetInstance()
	currentSkills := make([]int, 4)
	if len(picked.Skills) > 0 {
		for i := 0; i < 4 && i < len(picked.Skills); i++ {
			currentSkills[i] = picked.Skills[i]
		}
	}
	if currentSkills[0] == 0 && currentSkills[1] == 0 && currentSkills[2] == 0 && currentSkills[3] == 0 {
		defaults := petMgr.GetSkillsForLevel(picked.ID, picked.Level)
		for i := 0; i < 4 && i < len(defaults); i++ {
			currentSkills[i] = defaults[i]
		}
	}

	// 格式 A：catchTime(4) + count(4) + [slotIndex(4), skillId(4)]*count，只替换指定槽
	finalSkills := make([]int, 4)
	for i := 0; i < 4; i++ {
		finalSkills[i] = currentSkills[i]
	}
	if bodyLen >= 12 {
		count := binary.BigEndian.Uint32(body[4:8])
		if count >= 1 && count <= 4 && bodyLen >= 8+int(count)*8 {
			type slotPair struct {
				slot int
				sid  int
			}
			var pairs []slotPair
			for i := 0; i < int(count); i++ {
				slotIdx := int(binary.BigEndian.Uint32(body[8+8*i : 8+8*i+4]))
				sid := int(binary.BigEndian.Uint32(body[8+8*i+4 : 8+8*(i+1)]))
				oldSid := sid
				targetSlot := slotIdx
				// 兼容前端 20 字节格式：单槽时 body[16:20] 为右侧选中的新技能 ID，若有效则用其替换（避免前端误传左侧槽技能 ID）
				if count == 1 && bodyLen >= 20 && i == 0 {
					newSkillId := int(binary.BigEndian.Uint32(body[16:20]))
					if newSkillId > 1 && skillMgr.Exists(newSkillId) {
						logger.Info(fmt.Sprintf("[2312] 格式A 收到槽位/技能对(20字节): slotIndex=%d 原skillId=%d 使用newSkillId=%d", slotIdx, sid, newSkillId))
						sid = newSkillId
					} else {
						logger.Info(fmt.Sprintf("[2312] 格式A 收到槽位/技能对: slotIndex=%d skillId=%d", slotIdx, sid))
					}

					// 进一步兼容：20 字节格式里 body[12:16] 常为“被替换的旧技能ID”。
					// 若 slotIndex 与旧技能所在槽不一致，则优先用旧技能ID定位槽位，避免前端 slotIndex 传错导致换错槽。
					if oldSid > 1 {
						found := -1
						for si := 0; si < 4; si++ {
							if currentSkills[si] == oldSid {
								found = si
								break
							}
						}
						if found >= 0 && found != slotIdx {
							logger.Info(fmt.Sprintf("[2312] 20字节槽位纠正: slotIndex=%d oldSkillId=%d 实际槽=%d", slotIdx, oldSid, found))
							targetSlot = found
						}
					}
				} else {
					logger.Info(fmt.Sprintf("[2312] 格式A 收到槽位/技能对: slotIndex=%d skillId=%d", slotIdx, sid))
				}
				// 客户端传 0-based 槽位(0~3)，直接使用
				if targetSlot >= 0 && targetSlot < 4 && sid > 1 && skillMgr.Exists(sid) {
					pairs = append(pairs, slotPair{targetSlot, sid})
				}
			}
			// 格式 A 解析后若无有效槽位对，直接返回成功且不修改技能，避免误入格式 B 破坏数据
			if len(pairs) == 0 {
				resp := make([]byte, 4)
				binary.BigEndian.PutUint32(resp[0:4], 0)
				ctx.GameServer.SendResponse(ctx.ClientData, 2312, ctx.UserID, ctx.SeqID, resp)
				logger.Info(fmt.Sprintf("[2312] 格式A无有效槽位/技能对，保持原技能: catchTime=%d petID=%d skills=%v", catchTime, picked.ID, picked.Skills))
				return
			}
			// 单槽替换且该槽已是该技能时，不做修改（客户端可能误传了左侧槽技能ID而非右侧选中的新技能ID）
			if len(pairs) == 1 && currentSkills[pairs[0].slot] == pairs[0].sid {
				resp := make([]byte, 4)
				binary.BigEndian.PutUint32(resp[0:4], 0)
				ctx.GameServer.SendResponse(ctx.ClientData, 2312, ctx.UserID, ctx.SeqID, resp)
				logger.Info(fmt.Sprintf("[2312] 槽位已是该技能，无变更(请确认客户端发送的是右侧选中的新技能ID): slot=%d skillId=%d", pairs[0].slot, pairs[0].sid))
				return
			}
			usedSid := make(map[int]bool)
			setByUser := make(map[int]bool) // 记录被用户指定替换的槽位，去重时保留这些槽的新技能
			for _, p := range pairs {
				if usedSid[p.sid] {
					continue
				}
				usedSid[p.sid] = true
				setByUser[p.slot] = true
				finalSkills[p.slot] = p.sid
			}
			// 同一技能只能带一个：若新技能与已有槽重复，重复槽放“被替换槽的原技能”（交换），避免技能丢失
			for i := 0; i < 4; i++ {
				sid := finalSkills[i]
				if sid <= 0 {
					continue
				}
				for j := i + 1; j < 4; j++ {
					if finalSkills[j] != sid {
						continue
					}
					if setByUser[i] {
						finalSkills[j] = currentSkills[i]
					} else if setByUser[j] {
						finalSkills[i] = currentSkills[j]
					} else {
						finalSkills[j] = currentSkills[j]
						if finalSkills[j] == sid {
							finalSkills[j] = 0
						}
					}
				}
			}
			picked.Skills = finalSkills
			if ctx.GameServer.UserDB != nil {
				ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
				ctx.GameServer.UserDB.SaveToFile() // 立即持久化到磁盘，确保重新进入游戏后技能不丢失
			}
			resp := make([]byte, 4)
			binary.BigEndian.PutUint32(resp[0:4], 0)
			ctx.GameServer.SendResponse(ctx.ClientData, 2312, ctx.UserID, ctx.SeqID, resp)
			logger.Info(fmt.Sprintf("[2312] 精灵技能切换成功(按槽): catchTime=%d petID=%d skills=%v", catchTime, picked.ID, picked.Skills))
			return
		}
	}

	// 格式 B：catchTime(4) + [skillId(4)]*4，四槽依次填，去重（一技能只能带一个）
	skills := make([]int, 0, 4)
	for i := 0; i < 4 && bodyLen >= 4+4*(i+1); i++ {
		sid := int(binary.BigEndian.Uint32(body[4+4*i : 4+4*(i+1)]))
		skills = append(skills, sid)
	}
	logger.Info(fmt.Sprintf("[2312] 解析 catchTime=%d skills=%v", catchTime, skills))

	for i := 0; i < 4; i++ {
		sid := 0
		if i < len(skills) {
			sid = skills[i]
		}
		if sid > 1 && skillMgr.Exists(sid) {
			finalSkills[i] = sid
		} else {
			finalSkills[i] = currentSkills[i]
		}
	}
	// 去重：同一技能只保留第一次出现的槽位，后面重复的槽位恢复为当前技能
	seen := make(map[int]int)
	for i := 0; i < 4; i++ {
		sid := finalSkills[i]
		if sid <= 0 {
			continue
		}
		if _, ok := seen[sid]; ok {
			finalSkills[i] = currentSkills[i]
			if finalSkills[i] == sid {
				finalSkills[i] = 0
			}
		} else {
			seen[sid] = i
		}
	}
	picked.Skills = finalSkills
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
		ctx.GameServer.UserDB.SaveToFile() // 立即持久化到磁盘，确保重新进入游戏后技能不丢失
	}
	resp := make([]byte, 4)
	binary.BigEndian.PutUint32(resp[0:4], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2312, ctx.UserID, ctx.SeqID, resp)
	logger.Info(fmt.Sprintf("[2312] 精灵技能切换成功: catchTime=%d petID=%d skills=%v (已去重)", catchTime, picked.ID, picked.Skills))
}

// handleGetPetSkill CMD 2336 获取精灵技能（技能唤醒仪）
// 请求: catchTime(4)
// 响应: count(4) + [skillId(4)]*4，客户端用于 getCanStudySkill 回调
func handleGetPetSkill(ctx *gameserver.HandlerContext) {
	var catchTime uint32
	if len(ctx.Body) >= 4 {
		catchTime = binary.BigEndian.Uint32(ctx.Body[0:4])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	var picked *userdb.Pet
	if catchTime != 0 {
		for i := range user.Pets {
			if uint32(user.Pets[i].CatchTime) == catchTime {
				picked = &user.Pets[i]
				break
			}
		}
		if picked == nil && user.StoragePets != nil {
			for i := range user.StoragePets {
				if uint32(user.StoragePets[i].CatchTime) == catchTime {
					picked = &user.StoragePets[i]
					break
				}
			}
		}
	}
	if picked == nil && len(user.Pets) > 0 {
		picked = &user.Pets[0]
	}

	petID := 7
	level := 5
	if picked != nil {
		petID = picked.ID
		if picked.Level > 0 {
			level = picked.Level
		}
	}

	petMgr := gamepets.GetInstance()
	var rawSkills []int
	if picked != nil && len(picked.Skills) > 0 {
		rawSkills = make([]int, 4)
		for i := 0; i < 4 && i < len(picked.Skills); i++ {
			rawSkills[i] = picked.Skills[i]
		}
	} else {
		rawSkills = petMgr.GetSkillsForLevel(petID, level)
	}

	body := make([]byte, 0, 4+16)
	tmp := make([]byte, 4)
	binary.BigEndian.PutUint32(tmp, 4) // count=4 技能槽
	body = append(body, tmp...)
	for i := 0; i < 4; i++ {
		sid := 0
		if i < len(rawSkills) {
			sid = rawSkills[i]
		}
		binary.BigEndian.PutUint32(tmp, uint32(sid))
		body = append(body, tmp...)
	}

	ctx.GameServer.SendResponse(ctx.ClientData, 2336, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2336] 精灵技能: catchTime=%d petID=%d lv=%d skills=%v", catchTime, petID, level, rawSkills))
}

// handlePetRoomInfo CMD 2325 精灵房间信息（精灵简略信息面板）
// 对齐 Lua: pet_advanced_handlers.handlePetRoomInfo
// 请求: ownerId(4) + catchTime(4)
// 响应结构（与 Lua 一致）：
// ownerId(4) catchTime(4) petId(4) nature(4) level(4)
// hp(4) atk(4) def(4) spAtk(4) spDef(4) speed(4)
// skillCount(4) + [skillId(4) pp(4)]*N
// ev_hp(4) ev_atk(4) ev_def(4) ev_sa(4) ev_sd(4) ev_sp(4)
// effNum(2)
func handlePetRoomInfo(ctx *gameserver.HandlerContext) {
	var ownerID uint32
	var catchTime uint32
	if len(ctx.Body) >= 4 {
		ownerID = binary.BigEndian.Uint32(ctx.Body[0:4])
	}
	if len(ctx.Body) >= 8 {
		catchTime = binary.BigEndian.Uint32(ctx.Body[4:8])
	}
	if ownerID == 0 {
		ownerID = uint32(ctx.UserID)
	}

	// 当前 Go 服只支持查询自己（ownerID!=自己时先兜底返回自己数据，避免前端面板卡死）
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	source := user

	if catchTime == 0 && len(source.Pets) > 0 {
		catchTime = uint32(source.Pets[0].CatchTime)
	}

	var picked *userdb.Pet
	if catchTime != 0 {
		for i := range source.Pets {
			if uint32(source.Pets[i].CatchTime) == catchTime {
				picked = &source.Pets[i]
				break
			}
		}
		if picked == nil && source.StoragePets != nil {
			for i := range source.StoragePets {
				if uint32(source.StoragePets[i].CatchTime) == catchTime {
					picked = &source.StoragePets[i]
					break
				}
			}
		}
	}
	if picked == nil && len(source.Pets) > 0 {
		picked = &source.Pets[0]
	}

	// 默认值
	petID := 7
	level := 5
	dv := 31
	nature := 0
	if picked != nil {
		petID = picked.ID
		if picked.Level > 0 {
			level = picked.Level
		}
		if picked.DV > 0 {
			dv = picked.DV
		}
		nature = picked.Nature
	}

	petMgr := gamepets.GetInstance()
	// 获取 EV（如果 Pet 存在则使用其 EV，否则为 0）
	ev := gamepets.EVStats{}
	if picked != nil {
		ev = picked.GetEVStats()
	}
	stats := petMgr.GetStats(petID, level, dv, ev, nature)

	// 技能（最多4个），优先使用技能唤醒仪自定义技能；PP 先用 20 兜底
	var rawSkills []int
	if picked != nil && len(picked.Skills) > 0 {
		rawSkills = make([]int, 4)
		for i := 0; i < 4 && i < len(picked.Skills); i++ {
			rawSkills[i] = picked.Skills[i]
		}
	} else {
		rawSkills = petMgr.GetSkillsForLevel(petID, level)
	}
	type skillEntry struct{ id, pp int }
	entries := make([]skillEntry, 0, 4)
	for i := 0; i < len(rawSkills) && len(entries) < 4; i++ {
		if rawSkills[i] > 0 {
			entries = append(entries, skillEntry{id: rawSkills[i], pp: 20})
		}
	}

	// 组包
	body := make([]byte, 0, 128)
	putU32 := func(v uint32) {
		t := make([]byte, 4)
		binary.BigEndian.PutUint32(t, v)
		body = append(body, t...)
	}
	putU16 := func(v uint16) {
		t := make([]byte, 2)
		binary.BigEndian.PutUint16(t, v)
		body = append(body, t...)
	}

	// nature: 后端与客户端 NatureXMLInfo 性格顺序不同，需查表转换
	putU32(ownerID)
	putU32(catchTime)
	putU32(uint32(petID))
	putU32(uint32(natureToClientID(nature)))
	putU32(uint32(level))
	putU32(uint32(stats.HP))
	putU32(uint32(stats.Attack))
	putU32(uint32(stats.Defence))
	putU32(uint32(stats.SpAtk))
	putU32(uint32(stats.SpDef))
	putU32(uint32(stats.Speed))
	putU32(uint32(len(entries)))
	for _, e := range entries {
		putU32(uint32(e.id))
		putU32(uint32(e.pp))
	}
	// EVs（学习力）
	ev = gamepets.ClampAndCapEV(ev)
	putU32(uint32(ev.HP))
	putU32(uint32(ev.Atk))
	putU32(uint32(ev.Def))
	putU32(uint32(ev.SpAtk))
	putU32(uint32(ev.SpDef))
	putU32(uint32(ev.Spd))
	// effNum
	putU16(0)

	ctx.GameServer.SendResponse(ctx.ClientData, 2325, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2325] 房间精灵信息: owner=%d catch=%d petID=%d lv=%d", ownerID, catchTime, petID, level))
}

// handleUsePetItemOutOfFight CMD 2326 战斗外使用精灵道具
// 由前端 PetPropsPanel / PetPropClass_* 触发：
// 请求：catchTime(4) + itemId(4)
//
// 目前补齐“学习力清零道具”：
// 300037 atk清零, 300038 def清零, 300039 sa清零, 300040 sd清零, 300041 sp清零, 300042 hp清零
// 响应：uint32(0)（客户端不解析，存在即可）
// 并推送：2508 更新属性 + 2301 完整宠物信息（便于立即刷新面板）
func handleUsePetItemOutOfFight(ctx *gameserver.HandlerContext) {
	var catchTime uint32
	var itemID uint32
	if len(ctx.Body) >= 8 {
		catchTime = binary.BigEndian.Uint32(ctx.Body[0:4])
		itemID = binary.BigEndian.Uint32(ctx.Body[4:8])
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2326, ctx.UserID, ctx.SeqID, []byte{})
		return
	}

	// 找精灵（队伍优先，其次仓库）
	var picked *userdb.Pet
	if catchTime != 0 {
		for i := range user.Pets {
			if uint32(user.Pets[i].CatchTime) == catchTime {
				picked = &user.Pets[i]
				break
			}
		}
		if picked == nil && user.StoragePets != nil {
			for i := range user.StoragePets {
				if uint32(user.StoragePets[i].CatchTime) == catchTime {
					picked = &user.StoragePets[i]
					break
				}
			}
		}
	}
	if picked == nil && len(user.Pets) > 0 {
		picked = &user.Pets[0]
		catchTime = uint32(picked.CatchTime)
	}
	if picked == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 2326, ctx.UserID, ctx.SeqID, []byte{})
		return
	}

	// 扣道具（items map key 是字符串）
	if user.Items == nil {
		user.Items = make(map[string]userdb.Item)
	}
	itemKey := strconv.FormatUint(uint64(itemID), 10)
	it, ok := user.Items[itemKey]
	if !ok || it.Count <= 0 {
		// 道具不足：返回空包即可（前端通常只关心是否触发刷新）
		ctx.GameServer.SendResponse(ctx.ClientData, 2326, ctx.UserID, ctx.SeqID, []byte{})
		return
	}
	it.Count--
	if it.Count <= 0 {
		delete(user.Items, itemKey)
	} else {
		user.Items[itemKey] = it
	}

	// 学习力清零
	switch itemID {
	case 300037: // atk -> 0
		picked.EVAttack = 0
	case 300038: // def -> 0
		picked.EVDefence = 0
	case 300039: // sa -> 0
		picked.EVSpAtk = 0
	case 300040: // sd -> 0
		picked.EVSpDef = 0
	case 300041: // sp -> 0
		picked.EVSpeed = 0
	case 300042: // hp -> 0
		picked.EVHP = 0
	default:
		// 其他道具暂不处理（但仍扣道具并成功返回）
	}

	// 统一裁剪一次，避免出现历史脏数据（总510/单项255）
	ev := picked.GetEVStats()
	ev = gamepets.ClampAndCapEV(ev)
	picked.EVHP = ev.HP
	picked.EVAttack = ev.Atk
	picked.EVDefence = ev.Def
	picked.EVSpAtk = ev.SpAtk
	picked.EVSpDef = ev.SpDef
	picked.EVSpeed = ev.Spd

	// 保存
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	// ACK（前端不读内容，但需要回包）
	ctx.GameServer.SendResponse(ctx.ClientData, 2326, ctx.UserID, ctx.SeqID, make([]byte, 4))

	// 推送属性刷新
	petMgr := gamepets.GetInstance()
	petStats := petMgr.GetStats(picked.ID, picked.Level, picked.DV, ev, picked.Nature)
	propBody := buildNoteUpdateProp(uint32(picked.CatchTime), picked.ID, picked.Level, picked.Exp,
		petStats.MaxHP, petStats.Attack, petStats.Defence, petStats.SpAtk, petStats.SpDef, petStats.Speed, ev)
	ctx.GameServer.SendResponse(ctx.ClientData, 2508, ctx.UserID, ctx.SeqID, propBody)
	ctx.GameServer.SendResponse(ctx.ClientData, 2301, ctx.UserID, ctx.SeqID, buildFullPetInfo(*picked))
}

// handleUsePetItemFullAbilityOfStudy CMD 9278 使用“满学习力/注入”道具
// 前端请求：catchTime(4) + statIndex(4) + itemId(4) + flag(4)
// statIndex 约定（与前端一致）：
// 0=HP, 1=ATK, 2=DEF, 3=SA, 4=SD, 5=SP
// flag 通常为 0（固定注入）或 1（弹窗选择注入项）；服务端统一按“把该项 EV 设为 255”处理。
//
// 响应：uint32(0)（客户端不解析）
// 并推送：2508 更新属性 + 2301 完整宠物信息
func handleUsePetItemFullAbilityOfStudy(ctx *gameserver.HandlerContext) {
	var catchTime uint32
	var statIndex uint32
	var itemID uint32
	if len(ctx.Body) >= 16 {
		catchTime = binary.BigEndian.Uint32(ctx.Body[0:4])
		statIndex = binary.BigEndian.Uint32(ctx.Body[4:8])
		itemID = binary.BigEndian.Uint32(ctx.Body[8:12])
		// flag := binary.BigEndian.Uint32(ctx.Body[12:16]) // 目前无需使用
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 9278, ctx.UserID, ctx.SeqID, []byte{})
		return
	}

	// 找精灵（队伍优先，其次仓库）
	var picked *userdb.Pet
	if catchTime != 0 {
		for i := range user.Pets {
			if uint32(user.Pets[i].CatchTime) == catchTime {
				picked = &user.Pets[i]
				break
			}
		}
		if picked == nil && user.StoragePets != nil {
			for i := range user.StoragePets {
				if uint32(user.StoragePets[i].CatchTime) == catchTime {
					picked = &user.StoragePets[i]
					break
				}
			}
		}
	}
	if picked == nil && len(user.Pets) > 0 {
		picked = &user.Pets[0]
		catchTime = uint32(picked.CatchTime)
	}
	if picked == nil {
		ctx.GameServer.SendResponse(ctx.ClientData, 9278, ctx.UserID, ctx.SeqID, []byte{})
		return
	}

	// 扣道具
	if user.Items == nil {
		user.Items = make(map[string]userdb.Item)
	}
	itemKey := strconv.FormatUint(uint64(itemID), 10)
	it, ok := user.Items[itemKey]
	if !ok || it.Count <= 0 {
		ctx.GameServer.SendResponse(ctx.ClientData, 9278, ctx.UserID, ctx.SeqID, []byte{})
		return
	}
	it.Count--
	if it.Count <= 0 {
		delete(user.Items, itemKey)
	} else {
		user.Items[itemKey] = it
	}

	// 设置单项 EV = 255（总和/单项裁剪在后面统一处理）
	switch statIndex {
	case 0:
		picked.EVHP = 255
	case 1:
		picked.EVAttack = 255
	case 2:
		picked.EVDefence = 255
	case 3:
		picked.EVSpAtk = 255
	case 4:
		picked.EVSpDef = 255
	case 5:
		picked.EVSpeed = 255
	default:
		// 非法 statIndex：不改数据，但仍回包（避免客户端卡死）
	}

	// 统一裁剪（总510/单项255）
	ev := picked.GetEVStats()
	ev = gamepets.ClampAndCapEV(ev)
	picked.EVHP = ev.HP
	picked.EVAttack = ev.Atk
	picked.EVDefence = ev.Def
	picked.EVSpAtk = ev.SpAtk
	picked.EVSpDef = ev.SpDef
	picked.EVSpeed = ev.Spd

	// 保存
	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	// ACK
	ctx.GameServer.SendResponse(ctx.ClientData, 9278, ctx.UserID, ctx.SeqID, make([]byte, 4))

	// 推送属性刷新
	petMgr := gamepets.GetInstance()
	petStats := petMgr.GetStats(picked.ID, picked.Level, picked.DV, ev, picked.Nature)
	propBody := buildNoteUpdateProp(uint32(picked.CatchTime), picked.ID, picked.Level, picked.Exp,
		petStats.MaxHP, petStats.Attack, petStats.Defence, petStats.SpAtk, petStats.SpDef, petStats.Speed, ev)
	ctx.GameServer.SendResponse(ctx.ClientData, 2508, ctx.UserID, ctx.SeqID, propBody)
	ctx.GameServer.SendResponse(ctx.ClientData, 2301, ctx.UserID, ctx.SeqID, buildFullPetInfo(*picked))
}

// handleGetSimUserInfo CMD 2051 获取简单用户信息
// 对齐 Lua: map_handlers.handleGetSimUserInfo
// 请求: targetId(4) (可选，默认使用当前用户)
// 响应: targetId(4) + nick(16) + color(4) + texture(4) + vip(4) + status(4) + mapType(4) + mapId(4) +
//
//	isCanBeTeacher(4) + teacherID(4) + studentID(4) + graduationCount(4) + vipLevel(4) +
//	teamId(4) + teamIsShow(4) + clothCount(4) + [clothId(4) + level(4)]...
func handleGetSimUserInfo(ctx *gameserver.HandlerContext) {
	targetID := ctx.UserID
	if len(ctx.Body) >= 4 {
		targetID = int64(binary.BigEndian.Uint32(ctx.Body[0:4]))
	}

	user := ctx.GameServer.GetOrCreateUser(targetID)
	if user == nil {
		user = ctx.GameServer.GetOrCreateUser(ctx.UserID)
		targetID = ctx.UserID
	}

	nick := user.Nick
	if nick == "" {
		nick = fmt.Sprintf("Seer%d", targetID)
	}

	// 构建响应
	body := make([]byte, 0, 128)
	putU32 := func(v uint32) {
		t := make([]byte, 4)
		binary.BigEndian.PutUint32(t, v)
		body = append(body, t...)
	}
	putFixedString := func(s string, n int) {
		b := []byte(s)
		if len(b) > n {
			b = b[:n]
		}
		body = append(body, b...)
		for i := len(b); i < n; i++ {
			body = append(body, 0)
		}
	}

	putU32(uint32(targetID))
	putFixedString(nick, 16)
	putU32(uint32(user.Color))
	// texture（涂鸦/头像）：客户端用此请求 doodle/prev/{id}.swf，0 表示默认
	putU32(uint32(user.Texture))

	// VIP 标志（根据 SuperNono 判断）
	vipFlag := uint32(0)
	if user.Nono.SuperNono > 0 {
		vipFlag = 1
	}
	putU32(vipFlag)
	putU32(0) // status
	putU32(0) // mapType
	mapID := user.MapID
	if mapID <= 0 {
		mapID = 1 // 客户端 mapID=0 可能异常，回 1
	}
	putU32(uint32(mapID))

	// 师徒相关
	isCanBeTeacher := uint32(0) // 简化：默认不可当老师
	putU32(isCanBeTeacher)
	putU32(uint32(user.TeacherID))
	putU32(uint32(user.StudentID))
	putU32(uint32(user.GraduationCount))
	putU32(uint32(user.Nono.VipLevel))

	// 战队相关（当前未实现）
	putU32(0) // teamId
	putU32(0) // teamIsShow

	// 服装列表
	putU32(uint32(len(user.Clothes)))
	for _, clothID := range user.Clothes {
		putU32(uint32(clothID))
		putU32(0) // level
	}

	ctx.GameServer.SendResponse(ctx.ClientData, 2051, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2051] 获取简单用户信息: targetID=%d", targetID))
}

// handleGetMoreUserInfo CMD 2052 获取详细用户信息
// 对齐 Lua: map_handlers.handleGetMoreUserInfo
// 请求: targetId(4) (可选，默认使用当前用户)
// 响应: targetId(4) + nick(16) + regTime(4) + petAllNum(4) + petMaxLev(4) + bossAchievement(200) +
//
//	graduationCount(4) + monKingWin(4) + messWin(4) + maxStage(4) + maxArenaWins(4) + curTitle(4)
func handleGetMoreUserInfo(ctx *gameserver.HandlerContext) {
	targetID := ctx.UserID
	if len(ctx.Body) >= 4 {
		targetID = int64(binary.BigEndian.Uint32(ctx.Body[0:4]))
	}

	user := ctx.GameServer.GetOrCreateUser(targetID)
	if user == nil {
		user = ctx.GameServer.GetOrCreateUser(ctx.UserID)
		targetID = ctx.UserID
	}

	nick := user.Nick
	if nick == "" {
		nick = fmt.Sprintf("Seer%d", targetID)
	}

	// 获取注册时间（从 UserDB 或使用默认值）
	regTime := uint32(time.Now().Unix() - 86400*365)
	if ctx.GameServer.UserDB != nil {
		account := ctx.GameServer.UserDB.FindByUserID(targetID)
		if account != nil && account.RegisterTime > 0 {
			regTime = uint32(account.RegisterTime)
		}
	}

	// 构建响应
	body := make([]byte, 0, 256)
	putU32 := func(v uint32) {
		t := make([]byte, 4)
		binary.BigEndian.PutUint32(t, v)
		body = append(body, t...)
	}
	putFixedString := func(s string, n int) {
		b := []byte(s)
		if len(b) > n {
			b = b[:n]
		}
		body = append(body, b...)
		for i := len(b); i < n; i++ {
			body = append(body, 0)
		}
	}

	// petAllNum：无则用队伍+仓库精灵数；petMaxLev：无则用 100，对齐 Lua user.petAllNum or 0 / user.petMaxLev or 100
	petAllNum := user.PetAllNum
	if petAllNum <= 0 {
		petAllNum = len(user.Pets) + len(user.StoragePets)
	}
	petMaxLev := user.PetMaxLev
	if petMaxLev <= 0 {
		petMaxLev = 100
	}

	putU32(uint32(targetID))
	putFixedString(nick, 16)
	putU32(regTime)
	putU32(uint32(petAllNum))
	putU32(uint32(petMaxLev))
	body = append(body, sptboss.BuildBossAchievement(user.DefeatedSPTBossIds)...) // bossAchievement 200 字节
	putU32(uint32(user.GraduationCount))
	putU32(uint32(user.MonKingWin))
	putU32(uint32(user.MessWin))
	putU32(uint32(user.MaxStage))
	putU32(uint32(user.MaxArenaWins))
	putU32(uint32(user.CurTitle))

	ctx.GameServer.SendResponse(ctx.ClientData, 2052, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2052] 获取详细用户信息: targetID=%d", targetID))
}

// handleFitmentUsering CMD 10006 正在使用的家具（基地）
// 对齐 Lua: room_handlers.handleFitmentUsering
// 请求: targetUserId(4) (可选，默认使用当前用户)
// 响应: userID(4) + roomID(4) + count(4) + [id(4) + x(4) + y(4) + dir(4) + status(4)]...
func handleFitmentUsering(ctx *gameserver.HandlerContext) {
	targetUserID := ctx.UserID
	if len(ctx.Body) >= 4 {
		targetUserID = int64(binary.BigEndian.Uint32(ctx.Body[0:4]))
	}

	user := ctx.GameServer.GetOrCreateUser(targetUserID)
	if user == nil {
		user = ctx.GameServer.GetOrCreateUser(ctx.UserID)
		targetUserID = ctx.UserID
	}

	// 获取家具列表
	fitments := user.Fitments
	if fitments == nil {
		fitments = []userdb.Fitment{}
	}

	roomID := targetUserID // 房间ID默认使用用户ID

	// 构建响应
	body := make([]byte, 0, 12+len(fitments)*20)
	putU32 := func(v uint32) {
		t := make([]byte, 4)
		binary.BigEndian.PutUint32(t, v)
		body = append(body, t...)
	}

	putU32(uint32(targetUserID))  // userID (房主)
	putU32(uint32(roomID))        // roomID
	putU32(uint32(len(fitments))) // count

	// 添加家具列表
	for _, fitment := range fitments {
		putU32(uint32(fitment.ID))
		putU32(uint32(fitment.X))
		putU32(uint32(fitment.Y))
		putU32(uint32(fitment.Dir))
		putU32(uint32(fitment.Status))
	}

	ctx.GameServer.SendResponse(ctx.ClientData, 10006, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[10006] 正在使用的家具: owner=%d visitor=%d count=%d", targetUserID, ctx.UserID, len(fitments)))
}

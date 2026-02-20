package handlers

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/seer-game/golang-version/internal/core/logger"
	"github.com/seer-game/golang-version/internal/core/userdb"
	"github.com/seer-game/golang-version/internal/server/gameserver"
)

const (
	gachaTicketID = 400501 // 神奇扭蛋牌
	gachaConfig   = "gacha_rewards.json"
)

// GachaReward 扭蛋机奖励项
type GachaReward struct {
	ItemID  int    `json:"itemID"`
	Weight  int    `json:"weight"`  // 权重，越大概率越高
	Name    string `json:"name"`    // 中文名称（可选，用于 GM 显示）
	IsGold  bool   `json:"isGold"`  // 是否金豆商城道具（金豆道具概率应低于赛尔豆）
}

var (
	gachaRewards   []GachaReward
	gachaRewardsMu sync.RWMutex
	gachaTotalW    int
	gachaPersistence GachaPersistence // 若为 nil 则使用本地 JSON 文件；由 main 在启用 MySQL 时设置为 UserDB
	gachaPersistenceMu sync.RWMutex
)

// GachaPersistence 扭蛋机配置持久化（数据库或文件），由 main 在启动时注入
type GachaPersistence interface {
	LoadGachaConfig() ([]byte, error)
	SaveGachaConfig(data []byte) error
}

// SetGachaPersistence 设置持久化实现。启用 MySQL 时传入 *userdb.UserDB；否则不调用，将使用 gacha_rewards.json
func SetGachaPersistence(p GachaPersistence) {
	gachaPersistenceMu.Lock()
	defer gachaPersistenceMu.Unlock()
	gachaPersistence = p
}

func init() {
	loadDefaultGachaRewards()
	// 不再在此处 LoadGachaRewards，改由 main 在 SetGachaPersistence 之后调用
}

// loadDefaultGachaRewards 加载默认奖励：
// - 稀有奖励：上古炎兽精元 / 始祖灵兽精元 / 宝贝鲤精元（权重 1）
// - 普通奖励：体力/活力药剂、胶囊等常见精灵道具（权重 5）
func loadDefaultGachaRewards() {
	// 稀有奖励（权重 1）
	rareItems := []GachaReward{
		{490001, 1, "上古炎兽精元", false},
		{490002, 1, "始祖灵兽精元", false},
		{490003, 1, "宝贝鲤精元", false},
	}

	// 普通奖励（权重 5），对齐精灵道具商店常见道具
	commonItems := []GachaReward{
		// 精灵胶囊
		{300001, 5, "普通精灵胶囊", false},
		{300002, 5, "中级精灵胶囊", false},
		{300003, 5, "高级精灵胶囊", false},
		{300004, 5, "超级精灵胶囊", false},
		// 体力药剂
		{300011, 5, "初级体力药剂", false},
		{300012, 5, "中级体力药剂", false},
		{300013, 5, "高级体力药剂", false},
		{300014, 5, "超级体力药剂", false},
		{300015, 5, "特级体力药剂", false},
		// 活力药剂
		{300016, 5, "初级活力药剂", false},
		{300017, 5, "中级活力药剂", false},
		{300018, 5, "高级活力药剂", false},
		{300019, 5, "超级活力药剂", false},
		// 形态胶囊
		{300152, 5, "形态固定胶囊", false},
		{300153, 5, "形态还原胶囊", false},
		// 全能学习力注入剂
		{300651, 5, "全能学习力注入剂", false},
	}

	gachaRewards = append(rareItems, commonItems...)
	recalcGachaTotalWeight()
}

func recalcGachaTotalWeight() {
	total := 0
	for _, r := range gachaRewards {
		if r.Weight > 0 {
			total += r.Weight
		}
	}
	gachaTotalW = total
}

// LoadGachaRewards 从数据库或本地 JSON 加载奖励配置（若已 SetGachaPersistence 则从 DB，否则从 gacha_rewards.json）
func LoadGachaRewards() {
	gachaPersistenceMu.RLock()
	persistence := gachaPersistence
	gachaPersistenceMu.RUnlock()

	var data []byte
	var err error
	if persistence != nil {
		// 从数据库加载
		data, err = persistence.LoadGachaConfig()
		if err != nil {
			logger.Warning(fmt.Sprintf("[扭蛋机] 从数据库加载配置失败: %v", err))
			return
		}
		if len(data) == 0 {
			return
		}
		logger.Info("[扭蛋机] 已从数据库加载配置")
	} else {
		// 从文件加载
		path := gachaConfig
		if dir, err := os.Getwd(); err == nil {
			path = filepath.Join(dir, gachaConfig)
		}
		data, err = os.ReadFile(path)
		if err != nil {
			logger.Info(fmt.Sprintf("[扭蛋机] 未找到 %s，使用默认奖励", path))
			return
		}
		logger.Info(fmt.Sprintf("[扭蛋机] 已从 %s 加载配置", path))
	}

	var list []GachaReward
	if err := json.Unmarshal(data, &list); err != nil {
		logger.Warning(fmt.Sprintf("[扭蛋机] 解析配置失败: %v", err))
		return
	}
	if len(list) == 0 {
		logger.Info("[扭蛋机] 配置为空，保留默认奖励")
		return
	}
	gachaRewardsMu.Lock()
	gachaRewards = list
	recalcGachaTotalWeight()
	gachaRewardsMu.Unlock()
	logger.Info(fmt.Sprintf("[扭蛋机] 已加载 %d 项奖励", len(gachaRewards)))
}

// SaveGachaRewards 保存奖励配置到数据库或本地 JSON（若已 SetGachaPersistence 则写 DB，否则写 gacha_rewards.json）
func SaveGachaRewards() error {
	gachaRewardsMu.RLock()
	list := make([]GachaReward, len(gachaRewards))
	copy(list, gachaRewards)
	gachaRewardsMu.RUnlock()

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}

	gachaPersistenceMu.RLock()
	persistence := gachaPersistence
	gachaPersistenceMu.RUnlock()

	if persistence != nil {
		// 使用数据库持久化
		if err := persistence.SaveGachaConfig(data); err != nil {
			return fmt.Errorf("保存到数据库失败: %w", err)
		}
		logger.Info("[扭蛋机] 配置已保存到数据库")
		return nil
	}

	// 使用文件持久化
	path := gachaConfig
	if dir, err := os.Getwd(); err == nil {
		path = filepath.Join(dir, gachaConfig)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}
	logger.Info(fmt.Sprintf("[扭蛋机] 配置已保存到 %s", path))
	return nil
}

// GetGachaRewards 获取当前奖励列表（供 GM 使用）
func GetGachaRewards() []GachaReward {
	gachaRewardsMu.RLock()
	defer gachaRewardsMu.RUnlock()
	list := make([]GachaReward, len(gachaRewards))
	copy(list, gachaRewards)
	return list
}

// SetGachaRewards 设置奖励列表（供 GM 使用）
func SetGachaRewards(list []GachaReward) {
	gachaRewardsMu.Lock()
	gachaRewards = list
	recalcGachaTotalWeight()
	gachaRewardsMu.Unlock()
}

// AddGachaReward 添加奖励
func AddGachaReward(r GachaReward) {
	if r.Weight <= 0 {
		r.Weight = 1
	}
	if r.Name == "" {
		r.Name = GetItemName(r.ItemID)
	}
	gachaRewardsMu.Lock()
	gachaRewards = append(gachaRewards, r)
	recalcGachaTotalWeight()
	gachaRewardsMu.Unlock()
}

// RemoveGachaReward 按索引删除奖励
func RemoveGachaReward(index int) bool {
	gachaRewardsMu.Lock()
	defer gachaRewardsMu.Unlock()
	if index < 0 || index >= len(gachaRewards) {
		return false
	}
	gachaRewards = append(gachaRewards[:index], gachaRewards[index+1:]...)
	recalcGachaTotalWeight()
	return true
}

// UpdateGachaReward 按索引修改奖励
func UpdateGachaReward(index int, r GachaReward) bool {
	gachaRewardsMu.Lock()
	defer gachaRewardsMu.Unlock()
	if index < 0 || index >= len(gachaRewards) {
		return false
	}
	if r.Weight <= 0 {
		r.Weight = 1
	}
	if r.Name == "" {
		r.Name = GetItemName(r.ItemID)
	}
	gachaRewards[index] = r
	recalcGachaTotalWeight()
	return true
}

// RollGacha 抽一次奖，返回 (itemID, count)。若奖池为空返回 (0, 0)
func RollGacha() (itemID int, count int) {
	gachaRewardsMu.RLock()
	list := gachaRewards
	total := gachaTotalW
	gachaRewardsMu.RUnlock()

	if total <= 0 || len(list) == 0 {
		return 0, 0
	}
	rnd := rand.Intn(total)
	for _, r := range list {
		if r.Weight <= 0 {
			continue
		}
		if rnd < r.Weight {
			return r.ItemID, 1
		}
		rnd -= r.Weight
	}
	return list[0].ItemID, 1
}

// GiveGachaReward 给用户发放扭蛋奖励并返回用于 2601 提示的 body
func GiveGachaReward(user *userdb.GameData, itemID int, count int) []byte {
	if itemID <= 0 || count <= 0 {
		return nil
	}
	itemKey := strconv.Itoa(itemID)
	if it, has := user.Items[itemKey]; has {
		it.Count += count
		user.Items[itemKey] = it
	} else {
		user.Items[itemKey] = userdb.Item{Count: count, ExpireTime: 0x057E40}
	}
	addClothIfNeeded(user, itemID)
	// 2601 响应格式: cash(4) + itemID(4) + itemNum(4) + itemLevel(4)
	body := make([]byte, 16)
	coins := user.Coins
	if coins < 0 {
		coins = 0
	}
	binary.BigEndian.PutUint32(body[0:4], uint32(coins))
	binary.BigEndian.PutUint32(body[4:8], uint32(itemID))
	binary.BigEndian.PutUint32(body[8:12], uint32(count))
	binary.BigEndian.PutUint32(body[12:16], 0)
	return body
}

// handleGacha CMD 3201 精灵扭蛋机 / 9757 梦幻扭蛋机：
// - 消耗对应数量的扭蛋牌
// - 根据抽奖次数（1 / 5 / 10 连抽）多次 RollGacha 并发放奖励
// - 回包当前 CMD（用于扭蛋机界面刷新）以及 2601（复用“获得道具/精灵”提示窗口），
//   其中两个包的 body 都使用「最后一次」抽中的奖励。
func handleGacha(ctx *gameserver.HandlerContext) {
	cmd := int32(3201)
	if ctx.CmdID == 9757 {
		cmd = 9757
	}
	ticketID := gachaTicketID // 3201 用神奇扭蛋牌 400501
	if ctx.CmdID == 9757 {
		ticketID = 400505 // 梦幻扭蛋牌
	}

	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)

	// 读取抽奖次数：客户端在包体前 4 字节写入「按钮编号」：
	// 1=单抽，2=5连抽，3=10连抽。
	// 这里在日志中打印原始包体，便于分析与调试。
	logger.Info(fmt.Sprintf("[扭蛋机] 原始包体: len=%d hex=% X", len(ctx.Body), ctx.Body))

	times := 1
	if len(ctx.Body) >= 4 {
		btn := int(binary.BigEndian.Uint32(ctx.Body[0:4]))
		switch btn {
		case 1:
			times = 1
		case 2:
			times = 5
		case 3:
			times = 10
		default:
			times = 1
		}
	}
	if times <= 0 {
		times = 1
	}

	ticketKey := strconv.Itoa(ticketID)
	it, has := user.Items[ticketKey]
	if !has || it.Count < times {
		logger.Info(fmt.Sprintf("[扭蛋机] CMD=%d UID=%d 扭蛋牌不足(need=%d, have=%d)", cmd, ctx.UserID, times, it.Count))
		ctx.GameServer.SendResponse(ctx.ClientData, cmd, ctx.UserID, ctx.SeqID, []byte{})
		return
	}

	// 扣除对应数量的扭蛋牌
	it.Count -= times
	if it.Count <= 0 {
		delete(user.Items, ticketKey)
	} else {
		user.Items[ticketKey] = it
	}

	// 连抽：循环 RollGacha，多次发放奖励，并在本地累加成 itemID -> count 的映射，
	// 以便构造 EggMechineGame 期望的 3201 包体格式：
	// cash(4) + petID(4) + catchTime(4) + itemCount(4) + [itemID(4) + itemNum(4)] * itemCount
	rewards := make(map[int]int)
	var lastItemID, lastCount int
	for i := 0; i < times; i++ {
		itemID, count := RollGacha()
		if itemID == 0 || count <= 0 {
			logger.Warning("[扭蛋机] 奖池为空，未发放奖励")
			continue
		}
		body := GiveGachaReward(user, itemID, count)
		if body == nil {
			continue
		}
		rewards[itemID] += count
		lastItemID, lastCount = itemID, count
	}

	// 若本次连抽完全没有抽出有效奖励（极端情况），返回空包避免前端卡死
	if len(rewards) == 0 {
		ctx.GameServer.SendResponse(ctx.ClientData, cmd, ctx.UserID, ctx.SeqID, []byte{})
		return
	}

	if ctx.GameServer.UserDB != nil {
		ctx.GameServer.UserDB.SaveGameData(ctx.UserID, user)
	}

	// 根据 EggMechineGame 的解析逻辑构造 3201 包体：
	// coins(4) + petID(4) + catchTime(4) + itemCount(4) + [itemID(4) + itemNum(4)] * itemCount
	coins := user.Coins
	if coins < 0 {
		coins = 0
	}
	itemCount := len(rewards)
	body := make([]byte, 0, 16+itemCount*8)
	tmp := make([]byte, 4)

	// coins
	binary.BigEndian.PutUint32(tmp, uint32(coins))
	body = append(body, tmp...)
	// petID（当前扭蛋机只发道具，不直接发精灵，因此置 0，避免触发“获得精灵”窗口）
	binary.BigEndian.PutUint32(tmp, 0)
	body = append(body, tmp...)
	// catchTime（保留字段，置 0）
	binary.BigEndian.PutUint32(tmp, 0)
	body = append(body, tmp...)
	// itemCount
	binary.BigEndian.PutUint32(tmp, uint32(itemCount))
	body = append(body, tmp...)

	// items
	for id, c := range rewards {
		binary.BigEndian.PutUint32(tmp, uint32(id))
		body = append(body, tmp...)
		binary.BigEndian.PutUint32(tmp, uint32(c))
		body = append(body, tmp...)
	}

	// 仅回应当前 CMD（扭蛋机主流程），EggMechineGame 会根据 3201 包体自行弹出
	// “你获得了X个XXX”的道具提示；只有真正下发精灵时才需要在 body 中填充 petID。
	ctx.GameServer.SendResponse(ctx.ClientData, cmd, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[扭蛋机] CMD=%d UID=%d 连抽=%d 次，总奖励种类=%d，最后一次获得: itemID=%d count=%d 名称=%s", cmd, ctx.UserID, times, itemCount, lastItemID, lastCount, GetItemName(lastItemID)))
}

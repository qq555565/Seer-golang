package handlers

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	gamepets "github.com/seer-game/golang-version/internal/game/pets"
)

// SoulPearl 元神珠（精灵融合产物，对应物品表中的元神珠代码）
type SoulPearl struct {
	ItemID       int    `json:"itemID"`       // 元神珠物品 ID，如 1000001
	Name         string `json:"name"`         // 显示名，如 绿色元神珠
	TransmuteMon int    `json:"transmuteMon"` // 孵化出的精灵 ID（用于解析 PetClass）
}

// 元神珠静态列表（来自 items 配置：Item ID 1000001–1000022 等）
var soulPearlList = []SoulPearl{
	{1000001, "绿色元神珠", 301},
	{1000002, "蓝色元神珠", 304},
	{1000003, "红色元神珠", 307},
	{1000004, "咖啡色元神珠", 316},
	{1000005, "天青色元神珠", 322},
	{1000006, "橙色元神珠", 319},
	{1000007, "米色元神珠", 325}, // TransmuteMon 可为 "325 328 330"，取首个
	{1000008, "紫色元神珠", 310},
	{1000009, "粉色元神珠", 313},
	{1000010, "灰色元神珠", 338},
	{1000011, "暗紫色元神珠", 341},
	{1000012, "黄色元神珠", 401},
	{1000013, "深红色元神珠", 404},
	{1000014, "橙紫色元神珠", 427},
	{1000015, "暗绿色元神珠", 571},
	{1000016, "暗棕色元神珠", 578},
	{1000017, "红绿色元神珠", 629},
	{1000018, "棕黑色元神珠", 623},
	{1000019, "橙黑色元神珠", 625},
	{1000020, "蓝紫色元神珠", 1072},
	{1000021, "青紫色元神珠", 1487},
	{1000022, "蓝白元神珠", 1487},
}

var (
	petClassToSoulPearlID   map[int]int // PetClass -> 元神珠 ItemID（融合时用）
	soulPearlIDToPetClass   map[int]int // 元神珠 ItemID -> PetClass
	soulPearlMapsOnce       sync.Once
)

func initSoulPearlMaps() {
	petClassToSoulPearlID = make(map[int]int)
	soulPearlIDToPetClass = make(map[int]int)
	petMgr := gamepets.GetInstance()
	_ = petMgr.Load()
	for _, sp := range soulPearlList {
		pet := petMgr.Get(sp.TransmuteMon)
		if pet == nil {
			continue
		}
		pc := pet.PetClass
		if pc <= 0 {
			continue
		}
		soulPearlIDToPetClass[sp.ItemID] = pc
		if _, exists := petClassToSoulPearlID[pc]; !exists {
			petClassToSoulPearlID[pc] = sp.ItemID
		}
	}
}

// 元神珠转化时间默认值（秒）：普通 1800(30 分钟)，VIP 900(15 分钟)，与 items 配置一致
const (
	DefaultTransmuteTm    = 1800
	DefaultVipTransmuteTm = 900
)

// breedItemXML 用于解析 items.xml 中的精元（BreedMonID 存在的道具）
type breedItemXML struct {
	ID            int    `xml:"ID,attr"`
	Name          string `xml:"Name,attr"`
	BreedMonID    int    `xml:"BreedMonID,attr"`
	BreedTime     int    `xml:"BreedTime,attr"`
	VipBreedTime  int    `xml:"VipBreedTime,attr"`
}

type breedItemsRoot struct {
	XMLName struct{} `xml:"Items"`
	Cats    []struct {
		Items []breedItemXML `xml:"Item"`
	} `xml:"Cat"`
}

var (
	breedItemsList   []breedItemXML // 从 items.xml 解析的精元（400xxx 等）
	breedItemsOnce   sync.Once
)

func loadBreedItemsOnce() {
	breedItemsOnce.Do(func() {
		var data []byte
		var err error
		if itemContentProvider != nil {
			data, err = itemContentProvider()
		}
		if len(data) == 0 || err != nil {
			candidates := []string{
				filepath.Join("data", "items.xml"),
				filepath.Join("..", "data", "items.xml"),
			}
			if exePath, e := os.Executable(); e == nil {
				exeDir := filepath.Dir(exePath)
				candidates = append([]string{
					filepath.Join(exeDir, "..", "data", "items.xml"),
					filepath.Join(exeDir, "..", "..", "luvit_version", "data", "items.xml"),
					filepath.Join(exeDir, "..", "luvit_version", "data", "items.xml"),
				}, candidates...)
			}
			for _, p := range candidates {
				data, err = os.ReadFile(p)
				if err == nil && len(data) > 0 {
					break
				}
			}
		}
		if len(data) == 0 {
			return
		}
		var root breedItemsRoot
		if xml.Unmarshal(data, &root) != nil {
			return
		}
		seen := make(map[int]bool)
		for _, cat := range root.Cats {
			for _, it := range cat.Items {
				if it.ID <= 0 || it.BreedMonID <= 0 {
					continue
				}
				if seen[it.ID] {
					continue
				}
				seen[it.ID] = true
				if it.BreedTime <= 0 {
					it.BreedTime = DefaultTransmuteTm
				}
				if it.VipBreedTime <= 0 {
					it.VipBreedTime = DefaultVipTransmuteTm
				}
				breedItemsList = append(breedItemsList, it)
			}
		}
	})
}

// GetSoulPearlListForGM 返回元神珠+精元列表（含融合成功率、普通/VIP 转化时间、转化完成奖励精灵权重），供 GM 权重管理展示与保存
func GetSoulPearlListForGM(rateBySoulPearlID, transmuteTmByID, vipTransmuteTmByID map[string]int, defaultRate int, rewardPetsByID map[string][]WeightedPetEntry) []SoulPearlWithRate {
	soulPearlMapsOnce.Do(initSoulPearlMaps)
	loadBreedItemsOnce()
	seenID := make(map[int]bool)
	out := make([]SoulPearlWithRate, 0, len(soulPearlList)+len(breedItemsList))
	for _, sp := range soulPearlList {
		seenID[sp.ItemID] = true
		rate := defaultRate
		if r, ok := rateBySoulPearlID[strconv.Itoa(sp.ItemID)]; ok {
			rate = r
		}
		if rate < 0 {
			rate = 0
		}
		if rate > 100 {
			rate = 100
		}
		tm := DefaultTransmuteTm
		if t, ok := transmuteTmByID[strconv.Itoa(sp.ItemID)]; ok && t > 0 {
			tm = t
		}
		vipTm := DefaultVipTransmuteTm
		if t, ok := vipTransmuteTmByID[strconv.Itoa(sp.ItemID)]; ok && t > 0 {
			vipTm = t
		}
		var rewardPets []WeightedPetEntry
		if rewardPetsByID != nil {
			rewardPets = rewardPetsByID[strconv.Itoa(sp.ItemID)]
		}
		name := GetItemName(sp.ItemID)
		if name == "" || name == "未知道具" {
			name = sp.Name
		}
		out = append(out, SoulPearlWithRate{
			ItemID:           sp.ItemID,
			Name:             name,
			Rate:             rate,
			TransmuteTm:      tm,
			VipTransmuteTm:   vipTm,
			RewardPets:       rewardPets,
		})
	}
	// 追加精元物品（400xxx 等，来自 items.xml BreedMonID）
	for _, b := range breedItemsList {
		if seenID[b.ID] {
			continue
		}
		seenID[b.ID] = true
		tm := b.BreedTime
		if t, ok := transmuteTmByID[strconv.Itoa(b.ID)]; ok && t > 0 {
			tm = t
		}
		vipTm := b.VipBreedTime
		if t, ok := vipTransmuteTmByID[strconv.Itoa(b.ID)]; ok && t > 0 {
			vipTm = t
		}
		name := GetItemName(b.ID)
		if name == "" || name == "未知道具" {
			name = b.Name
		}
		// 精元无融合成功率，固定 100；奖励精灵为 BreedMonID 对应精灵
		out = append(out, SoulPearlWithRate{
			ItemID:           b.ID,
			Name:             name,
			Rate:             100,
			TransmuteTm:      tm,
			VipTransmuteTm:   vipTm,
			RewardPets:       rewardPetsByID[strconv.Itoa(b.ID)],
		})
	}
	return out
}

// SoulPearlWithRate 元神珠 + 融合成功率 + 转化时间 + 奖励精灵权重，供 GM 接口返回
type SoulPearlWithRate struct {
	ItemID          int                 `json:"itemID"`
	Name           string              `json:"name"`
	Rate           int                 `json:"rate"`
	TransmuteTm    int                 `json:"transmuteTm"`    // 普通用户转化时间(秒)
	VipTransmuteTm int                 `json:"vipTransmuteTm"` // VIP 用户转化时间(秒)
	RewardPets     []WeightedPetEntry  `json:"rewardPets"`     // 转化完成按权重随机给一只
}

// GetFusionSuccessRateByPetClass 根据融合得到的 PetClass 查对应元神珠的融合成功率(0~1)
// 配置按元神珠物品 ID 存储，此处通过 PetClass -> 元神珠 ID 再查配置
func GetFusionSuccessRateByPetClass(petClass int, rateBySoulPearlID map[string]int, defaultRate int) float64 {
	soulPearlMapsOnce.Do(initSoulPearlMaps)
	soulPearlID, ok := petClassToSoulPearlID[petClass]
	if !ok {
		if defaultRate < 0 {
			defaultRate = 0
		}
		if defaultRate > 100 {
			defaultRate = 100
		}
		return float64(defaultRate) / 100.0
	}
	rate, ok := rateBySoulPearlID[strconv.Itoa(soulPearlID)]
	if !ok {
		if defaultRate < 0 {
			defaultRate = 0
		}
		if defaultRate > 100 {
			defaultRate = 100
		}
		return float64(defaultRate) / 100.0
	}
	if rate < 0 {
		rate = 0
	}
	if rate > 100 {
		rate = 100
	}
	return float64(rate) / 100.0
}

// GetSoulPearlItemIDByPetClass 根据 PetClass 返回对应的元神珠物品 ID（如 1000001），供融合成功时发放到背包
func GetSoulPearlItemIDByPetClass(petClass int) (itemID int, ok bool) {
	soulPearlMapsOnce.Do(initSoulPearlMaps)
	itemID, ok = petClassToSoulPearlID[petClass]
	return itemID, ok
}

// GetPetClassBySoulPearlItemID 根据元神珠物品 ID 返回 PetClass，供自定义融合规则写入 SoulBead
func GetPetClassBySoulPearlItemID(itemID int) (petClass int, ok bool) {
	soulPearlMapsOnce.Do(initSoulPearlMaps)
	petClass, ok = soulPearlIDToPetClass[itemID]
	return petClass, ok
}

// GetBreedMonIDByItemID 根据精元物品 ID（400xxx）返回 BreedMonID（孵化出的精灵 ID）
func GetBreedMonIDByItemID(itemID int) (breedMonID int, ok bool) {
	loadBreedItemsOnce()
	for _, b := range breedItemsList {
		if b.ID == itemID && b.BreedMonID > 0 {
			return b.BreedMonID, true
		}
	}
	return 0, false
}

// IsBreedItem 判断是否为可孵化的精元物品（400xxx 等）
func IsBreedItem(itemID int) bool {
	_, ok := GetBreedMonIDByItemID(itemID)
	return ok
}

// SoulPearlOption 元神珠选项，供 GM 融合管理下拉
type SoulPearlOption struct {
	ItemID int    `json:"itemID"`
	Name   string `json:"name"`
}

// GetSoulPearlOptionsForGM 返回全部元神珠的 itemID+name 列表
func GetSoulPearlOptionsForGM() []SoulPearlOption {
	out := make([]SoulPearlOption, 0, len(soulPearlList))
	for _, sp := range soulPearlList {
		name := GetItemName(sp.ItemID)
		if name == "" || name == "未知道具" {
			name = sp.Name
		}
		out = append(out, SoulPearlOption{ItemID: sp.ItemID, Name: name})
	}
	return out
}

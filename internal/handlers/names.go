package handlers

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/seer-game/golang-version/internal/core/logger"
	"github.com/seer-game/golang-version/internal/game/pets"
	gameskills "github.com/seer-game/golang-version/internal/game/skills"
)

// 精灵名称映射（会在启动时从 pets.xml 自动填充）
var PetNames = map[int]string{
	// 可以在这里放一些手动覆盖/修正的别名
	4150: "拂晓兔",
}

var (
	petNamesOnce  sync.Once
	itemNamesOnce sync.Once
)

// monsterXML 用于解析 pets.xml 中我们需要的最小字段
type monsterXML struct {
	ID      int    `xml:"ID,attr"`
	DefName string `xml:"DefName,attr"`
}

type monstersRoot struct {
	// pets.xml 顶层就是 <Monsters>，其子节点为 <Monster>，这里直接取所有 Monster
	Monsters []monsterXML `xml:"Monster"`
}

// loadPetNamesOnce 从 spt（数据库或 data/spt.xml）或 pets.xml 加载精灵 ID 与中文名，填充到 PetNames 中
func loadPetNamesOnce() {
	petNamesOnce.Do(func() {
		// 优先从 spt（精灵表，可能来自数据库）取 DefName，避免依赖 pets.xml
		defNames := pets.GetInstance().GetAllDefNames()
		if len(defNames) > 0 {
			count := 0
			for id, name := range defNames {
				if _, exists := PetNames[id]; !exists {
					PetNames[id] = name
					count++
				}
			}
			logger.Info(fmt.Sprintf("[GM] 已从 spt 加载 %d 个精灵名称", count))
			return
		}

		// 回退：从 pets.xml 文件加载
		readAndFill := func(path string) bool {
			data, err := os.ReadFile(path)
			if err != nil {
				return false
			}
			var root monstersRoot
			if err := xml.Unmarshal(data, &root); err != nil {
				logger.Error(fmt.Sprintf("[GM] 解析 pets.xml 失败: %v", err))
				return false
			}
			count := 0
			for _, m := range root.Monsters {
				if m.ID <= 0 || m.DefName == "" {
					continue
				}
				if _, exists := PetNames[m.ID]; !exists {
					PetNames[m.ID] = m.DefName
					count++
				}
			}
			logger.Info(fmt.Sprintf("[GM] 已从 pets.xml 加载 %d 个精灵名称", count))
			return true
		}

		// 1) 以可执行文件目录为基准：先试 data/spt.xml（与 spt 同结构），再试 pets.xml
		if exePath, err := os.Executable(); err == nil {
			exeDir := filepath.Dir(exePath)
			candidates := []string{
				filepath.Join(exeDir, "..", "data", "spt.xml"), // 与 data/skills.xml 同目录
				filepath.Join(exeDir, "..", "..", "luvit_version", "data", "pets.xml"),
				filepath.Join(exeDir, "..", "luvit_version", "data", "pets.xml"),
				filepath.Join(exeDir, "luvit_version", "data", "pets.xml"),
			}
			for _, c := range candidates {
				if readAndFill(c) {
					return
				}
			}
		}

		// 2) 以当前工作目录为基准
		candidates := []string{
			filepath.Join("data", "spt.xml"),
			filepath.Join("luvit_version", "data", "pets.xml"),
			filepath.Join("..", "luvit_version", "data", "pets.xml"),
		}
		for _, c := range candidates {
			if readAndFill(c) {
				return
			}
		}

		logger.Warning("[GM] 未能找到 pets.xml，精灵中文名将仅使用内置少量映射")
	})
}

// 道具名称映射（会在启动时从 items.xml 或数据库自动填充）
var ItemNames = map[int]string{
	// 这里可以放少量覆盖/别名；绝大多数由 items.xml 自动加载
}

// 若设置则 loadItemNamesOnce 优先从该提供者读取（如数据库）
var itemContentProvider func() ([]byte, error)

// SetItemContentProvider 设置道具 XML 内容提供者；Load 时优先使用，失败或为空则回退到文件
func SetItemContentProvider(f func() ([]byte, error)) {
	itemContentProvider = f
}

// itemXML / itemsRoot 用于解析 items.xml
type itemXML struct {
	ID       int    `xml:"ID,attr"`
	Name     string `xml:"Name,attr"`
	UseAI    int    `xml:"UseAI,attr"`
	UsePower int    `xml:"UsePower,attr"`
	Color    int    `xml:"Color,attr"`
}

type itemsRoot struct {
	Cats []struct {
		Items []itemXML `xml:"Item"`
	} `xml:"Cat"`
}

// ItemEffect 描述道具在 NONO 系统中的简单数值效果（目前用于芯片）
type ItemEffect struct {
	UseAI    int
	UsePower int
	Color    int
}

// itemEffects 从 items.xml 解析出的道具效果表：key 为 ItemID
var itemEffects = map[int]ItemEffect{}

// GetPetName 获取精灵中文名称（优先从 pets.xml 动态加载）
func GetPetName(id int) string {
	if id <= 0 {
		return "未知精灵"
	}
	loadPetNamesOnce()
	if name, exists := PetNames[id]; exists {
		return name
	}
	return "未知精灵"
}

// GMPetInfo 提供给 GM 前端使用的精灵信息
type GMPetInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// GetAllPetsForGM 返回按 ID 升序排序的精灵列表（ID + 中文名），供 GM 下拉/搜索用
func GetAllPetsForGM() []GMPetInfo {
	loadPetNamesOnce()
	list := make([]GMPetInfo, 0, len(PetNames))
	for id, name := range PetNames {
		list = append(list, GMPetInfo{
			ID:   id,
			Name: name,
		})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})
	return list
}

// loadItemNamesOnce 从数据库或 items.xml 文件加载道具 ID 与中文名，填充到 ItemNames 中
func loadItemNamesOnce() {
	itemNamesOnce.Do(func() {
		parseAndFillItems := func(data []byte) bool {
			if len(data) == 0 {
				return false
			}
			var root itemsRoot
			if err := xml.Unmarshal(data, &root); err != nil {
				logger.Error(fmt.Sprintf("[GM] 解析 items.xml 失败: %v", err))
				return false
			}
			count := 0
			for _, cat := range root.Cats {
				for _, it := range cat.Items {
					if it.ID <= 0 {
						continue
					}
					// 1) 填充道具名称（若有）
					if it.Name != "" {
						if _, exists := ItemNames[it.ID]; !exists {
							ItemNames[it.ID] = it.Name
							count++
						}
					}
					// 2) 记录与 NONO 芯片相关的数值效果（UseAI / UsePower / Color）
					if it.UseAI != 0 || it.UsePower != 0 || it.Color != 0 {
						itemEffects[it.ID] = ItemEffect{
							UseAI:    it.UseAI,
							UsePower: it.UsePower,
							Color:    it.Color,
						}
					}
				}
			}
			return count > 0
		}

		// 优先从数据库（或提供者）读取
		if itemContentProvider != nil {
			data, err := itemContentProvider()
			if err == nil && parseAndFillItems(data) {
				logger.Info("[GM] 已从数据库加载道具名称")
				return
			}
		}

		readAndFill := func(path string) bool {
			data, err := os.ReadFile(path)
			if err != nil {
				return false
			}
			if !parseAndFillItems(data) {
				return false
			}
			logger.Info("[GM] 已从 items.xml 加载道具名称")
			return true
		}

		// 先试 data/items.xml（与 data/skills.xml、spt.xml 同目录），再试 luvit_version
		if exePath, err := os.Executable(); err == nil {
			exeDir := filepath.Dir(exePath)
			candidates := []string{
				filepath.Join(exeDir, "..", "data", "items.xml"),
				filepath.Join(exeDir, "..", "..", "luvit_version", "data", "items.xml"),
				filepath.Join(exeDir, "..", "luvit_version", "data", "items.xml"),
				filepath.Join(exeDir, "luvit_version", "data", "items.xml"),
			}
			for _, c := range candidates {
				if readAndFill(c) {
					return
				}
			}
		}

		candidates := []string{
			filepath.Join("data", "items.xml"),
			filepath.Join("luvit_version", "data", "items.xml"),
			filepath.Join("..", "luvit_version", "data", "items.xml"),
		}
		for _, c := range candidates {
			if readAndFill(c) {
				return
			}
		}

		logger.Warning("[GM] 未能找到 items.xml，道具中文名将仅使用内置映射")
	})
}

// GetItemName 获取道具中文名称（优先从 items.xml 动态加载）
func GetItemName(id int) string {
	if id <= 0 {
		return "未知道具"
	}
	loadItemNamesOnce()
	if name, exists := ItemNames[id]; exists {
		return name
	}
	return "未知道具"
}

// GMItemInfo 提供给 GM 前端使用的道具信息
type GMItemInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// GetAllItemsForGM 返回按 ID 升序排序的道具列表（ID + 中文名），供 GM 下拉/搜索用
func GetAllItemsForGM() []GMItemInfo {
	loadItemNamesOnce()
	list := make([]GMItemInfo, 0, len(ItemNames))
	for id, name := range ItemNames {
		list = append(list, GMItemInfo{
			ID:   id,
			Name: name,
		})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})
	return list
}

// GetSkillName 获取技能中文名称（从 skills.xml 加载）
func GetSkillName(id int) string {
	if id <= 0 {
		return ""
	}
	return gameskills.GetInstance().GetName(id)
}

// GMTraitInfo 特性 ID 与显示名，供 GM 下拉选择
type GMTraitInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// GetAllTraitsForGM 返回特性列表：0=无，1006–1045=特性1006 等，供 GM 下拉选择
func GetAllTraitsForGM() []GMTraitInfo {
	list := []GMTraitInfo{{ID: 0, Name: "无"}}
	for id := 1006; id <= 1045; id++ {
		list = append(list, GMTraitInfo{ID: id, Name: fmt.Sprintf("特性%d", id)})
	}
	return list
}
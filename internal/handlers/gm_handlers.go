package handlers

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seer-game/golang-version/internal/core/logger"
	"github.com/seer-game/golang-version/internal/core/userdb"
	gamepets "github.com/seer-game/golang-version/internal/game/pets"
	gameskills "github.com/seer-game/golang-version/internal/game/skills"
	"github.com/seer-game/golang-version/internal/game/sptboss"
	"github.com/seer-game/golang-version/internal/server/gameserver"
)

// RegisterGMHandlers 注册GM管理接口
func RegisterGMHandlers(mux *http.ServeMux, gs *gameserver.GameServer) {
	// 本地模式认证：kyse_seer 风格前端用，返回 isLocal 以跳过登录
	mux.HandleFunc("/gm/auth/current", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"isLocal":     true,
				"userId":      0,
				"email":       "",
				"permissions": []string{},
			},
		})
	})

	// 服务器状态：在线人数、总玩家数、运行时间（kyse_seer 风格）
	mux.HandleFunc("/gm/server/status", func(w http.ResponseWriter, r *http.Request) {
		handleGMServerStatus(w, r, gs)
	})
	// 在线玩家列表
	mux.HandleFunc("/gm/server/online", func(w http.ResponseWriter, r *http.Request) {
		handleGMOnlinePlayers(w, r, gs)
	})
	// 操作日志（占位，返回空列表）
	mux.HandleFunc("/gm/logs", func(w http.ResponseWriter, r *http.Request) {
		handleGMLogs(w, r, gs)
	})

	// 获取所有用户列表（支持 keyword 查找账号）
	mux.HandleFunc("/gm/users", func(w http.ResponseWriter, r *http.Request) {
		handleGMGetUsers(w, r, gs)
	})

	// 获取单个用户详情
	mux.HandleFunc("/gm/user/", func(w http.ResponseWriter, r *http.Request) {
		handleGMGetUser(w, r, gs)
	})

	// 更新用户信息
	mux.HandleFunc("/gm/user/update", func(w http.ResponseWriter, r *http.Request) {
		handleGMUpdateUser(w, r, gs)
	})

	// 删除角色账号
	mux.HandleFunc("/gm/user/delete", func(w http.ResponseWriter, r *http.Request) {
		handleGMDeleteUser(w, r, gs)
	})

	// 新增账号
	mux.HandleFunc("/gm/user/create", func(w http.ResponseWriter, r *http.Request) {
		handleGMCreateUser(w, r, gs)
	})

	// 元神珠/精元转化设为1秒完成（元神赋形、精元孵化共用 SoulBeadTransform）
	mux.HandleFunc("/gm/user/transform-1sec", func(w http.ResponseWriter, r *http.Request) {
		handleGMTransform1Sec(w, r, gs)
	})

	// 添加黑名单
	mux.HandleFunc("/gm/blacklist/add", func(w http.ResponseWriter, r *http.Request) {
		handleGMAddBlacklist(w, r, gs)
	})

	// 移除黑名单
	mux.HandleFunc("/gm/blacklist/remove", func(w http.ResponseWriter, r *http.Request) {
		handleGMRemoveBlacklist(w, r, gs)
	})

	// 添加物品
	mux.HandleFunc("/gm/item/add", func(w http.ResponseWriter, r *http.Request) {
		handleGMAddItem(w, r, gs)
	})

	// 删除物品
	mux.HandleFunc("/gm/item/delete", func(w http.ResponseWriter, r *http.Request) {
		handleGMDeleteItem(w, r, gs)
	})

	// 道具列表（ID + 中文名），供 GM 前端选择使用
	mux.HandleFunc("/gm/items", func(w http.ResponseWriter, r *http.Request) {
		handleGMItemList(w, r, gs)
	})

	// 添加精灵
	mux.HandleFunc("/gm/pet/add", func(w http.ResponseWriter, r *http.Request) {
		handleGMAddPet(w, r, gs)
	})

	// 精灵列表（ID + 中文名），供 GM 前端选择使用
	mux.HandleFunc("/gm/pets", func(w http.ResponseWriter, r *http.Request) {
		handleGMPetList(w, r, gs)
	})

	// 技能列表（ID + 中文名），供 GM 编辑精灵技能下拉选择
	mux.HandleFunc("/gm/skills", func(w http.ResponseWriter, r *http.Request) {
		handleGMSkillList(w, r, gs)
	})

	// 特性列表（ID + 显示名），供 GM 编辑精灵特性下拉选择
	mux.HandleFunc("/gm/traits", func(w http.ResponseWriter, r *http.Request) {
		handleGMTraitList(w, r, gs)
	})

	// 修改背包精灵属性（等级、经验、名称等）
	mux.HandleFunc("/gm/pet/update", func(w http.ResponseWriter, r *http.Request) {
		handleGMUpdatePet(w, r, gs)
	})
	
	// 修改精灵技能
	mux.HandleFunc("/gm/pet/skills", func(w http.ResponseWriter, r *http.Request) {
		handleGMUpdatePetSkills(w, r, gs)
	})
	
	// 修改精灵成长（等级 / 学习力 / 个体），背包与仓库通用
	mux.HandleFunc("/gm/pet/stats", func(w http.ResponseWriter, r *http.Request) {
		handleGMUpdatePetStats(w, r, gs)
	})
	
	// 修改精灵特性
	mux.HandleFunc("/gm/pet/trait", func(w http.ResponseWriter, r *http.Request) {
		handleGMUpdatePetTrait(w, r, gs)
	})

	// 修改精灵性格
	mux.HandleFunc("/gm/pet/nature", func(w http.ResponseWriter, r *http.Request) {
		handleGMUpdatePetNature(w, r, gs)
	})

	// 删除用户精灵
	mux.HandleFunc("/gm/pet/delete", func(w http.ResponseWriter, r *http.Request) {
		handleGMDeletePet(w, r, gs)
	})

	// 经验分配器：发放经验到经验池
	mux.HandleFunc("/gm/exp/add", func(w http.ResponseWriter, r *http.Request) {
		handleGMExpPoolAdd(w, r, gs)
	})
	// 经验分配器：设置当前经验池数值
	mux.HandleFunc("/gm/exp/set", func(w http.ResponseWriter, r *http.Request) {
		handleGMExpPoolSet(w, r, gs)
	})

	// 扭蛋机：列表（带中文名）、修改、添加、删除、保存
	mux.HandleFunc("/gm/gacha/list", func(w http.ResponseWriter, r *http.Request) {
		handleGMGachaList(w, r, gs)
	})
	mux.HandleFunc("/gm/gacha/update", func(w http.ResponseWriter, r *http.Request) {
		handleGMGachaUpdate(w, r, gs)
	})
	mux.HandleFunc("/gm/gacha/add", func(w http.ResponseWriter, r *http.Request) {
		handleGMGachaAdd(w, r, gs)
	})
	mux.HandleFunc("/gm/gacha/remove", func(w http.ResponseWriter, r *http.Request) {
		handleGMGachaRemove(w, r, gs)
	})
	mux.HandleFunc("/gm/gacha/save", func(w http.ResponseWriter, r *http.Request) {
		handleGMGachaSave(w, r, gs)
	})

	// 权重管理：胶囊捕捉率、精灵融合成功率
	mux.HandleFunc("/gm/weights", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodGet {
			handleGMWeightsGet(w, r, gs)
		} else if r.Method == http.MethodPost {
			handleGMWeightsUpdate(w, r, gs)
		} else {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})

	// BUFF 道具配置：经验加速 / 自动战斗 / 体力吸收 / 学习力双倍
	mux.HandleFunc("/gm/buff-items", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodGet {
			handleGMBuffItemsGet(w, r, gs)
		} else if r.Method == http.MethodPost {
			handleGMBuffItemsSave(w, r, gs)
		} else {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})

	// 奖励配置：活动赠宠 / 罗威训练奖励等
	mux.HandleFunc("/gm/rewards", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodGet {
			handleGMRewardsGet(w, r, gs)
		} else if r.Method == http.MethodPost {
			handleGMRewardsSave(w, r, gs)
		} else {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})

	// 元神珠列表（ID + 名称），供融合管理下拉选择
	mux.HandleFunc("/gm/soulpearls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		handleGMSoulPearlsList(w, r, gs)
	})

	// 融合管理：自定义精灵 A + 精灵 B → 元神珠
	mux.HandleFunc("/gm/fusion/rules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodGet {
			handleGMFusionRulesGet(w, r, gs)
		} else if r.Method == http.MethodPost {
			handleGMFusionRulesPost(w, r, gs)
		} else {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})

	// 试炼之塔：每层怪物与属性配置
	mux.HandleFunc("/gm/freshfight/levels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodGet {
			handleGMFreshFightGet(w, r, gs)
		} else if r.Method == http.MethodPost {
			handleGMFreshFightSave(w, r, gs)
		} else {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})

	// 勇者之塔：每层 Boss 精灵 ID 列表
	mux.HandleFunc("/gm/fightlevel/levels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodGet {
			handleGMFightLevelGet(w, r, gs)
		} else if r.Method == http.MethodPost {
			handleGMFightLevelSave(w, r, gs)
		} else {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})

	// 地图刷新配置：普通/稀有精灵、刷新间隔等
	mux.HandleFunc("/gm/maps/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodGet {
			handleGMMapConfigsGet(w, r)
		} else if r.Method == http.MethodPost {
			handleGMMapConfigsPost(w, r)
		} else {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})

	// 暗黑武斗场BOSS配置
	mux.HandleFunc("/gm/darkportal/bosses", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodGet {
			handleGMDarkPortalGet(w, r, gs)
		} else if r.Method == http.MethodPost {
			handleGMDarkPortalSave(w, r, gs)
		} else {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})

	// SPT BOSS 配置（地图-BOSS、奖励、特性）
	mux.HandleFunc("/gm/sptboss/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodGet {
			handleGMSPTBossGet(w, r, gs)
		} else if r.Method == http.MethodPost {
			handleGMSPTBossSave(w, r, gs)
		} else {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})
}

// BuffItemsConfig GM 配置：BUFF 道具效果
type BuffItemsConfig struct {
	SpeedupItems   []struct {
		ItemID       uint32 `json:"itemId"`
		TwoTimesAdd  int    `json:"twoTimesAdd"`
		ThreeTimesAdd int   `json:"threeTimesAdd"`
		MaxTwoTimes  int    `json:"maxTwoTimes"`
		MaxThreeTimes int   `json:"maxThreeTimes"`
	} `json:"speedupItems"`
	AutoFightItems []struct {
		ItemID    uint32 `json:"itemId"`
		TimesAdd  int    `json:"timesAdd"`
		MaxTimes  int    `json:"maxTimes"`
		Enable    bool   `json:"enableAutoFight"`
	} `json:"autoFightItems"`
	EnergyItems []struct {
		ItemID   uint32 `json:"itemId"`
		TimesAdd int    `json:"timesAdd"`
		MaxTimes int    `json:"maxTimes"`
	} `json:"energyItems"`
	StudyItems []struct {
		ItemID   uint32 `json:"itemId"`
		TimesAdd int    `json:"timesAdd"`
		MaxTimes int    `json:"maxTimes"`
	} `json:"studyItems"`
}

// RewardConfig GM 配置：赠宠 / 罗威等奖励
type RewardConfig struct {
	CollectRewards []struct {
		ActivityID uint32 `json:"activityId"`
		PetID      uint32 `json:"petId"`
		Level      int    `json:"level"`
		DV         int    `json:"dv"`
		Nature     int    `json:"nature"`
	} `json:"collectRewards"`
	RoweiRewards struct {
		BaseExp     int `json:"baseExp"`
		ExpPerHour  int `json:"expPerHour"`
		CoinPerHour int `json:"coinPerHour"`
	} `json:"roweiRewards"`
}

// DefaultBuffItemsConfigUnpacked 解包协议默认 BUFF 道具配置。数据未填时使用。
func DefaultBuffItemsConfigUnpacked() BuffItemsConfig {
	return BuffItemsConfig{}
}

// DefaultRewardConfigUnpacked 解包协议默认奖励配置。数据未填时使用。
func DefaultRewardConfigUnpacked() RewardConfig {
	return RewardConfig{}
}

// handleGMBuffItemsGet 获取 BUFF 道具配置
func handleGMBuffItemsGet(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	data, err := gs.UserDB.LoadBuffItemsConfig()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "加载失败: " + err.Error()})
		return
	}
	if len(data) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "data": DefaultBuffItemsConfigUnpacked()})
		return
	}
	var cfg BuffItemsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "解析失败: " + err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "data": cfg})
}

// handleGMBuffItemsSave 保存 BUFF 道具配置
func handleGMBuffItemsSave(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	var cfg BuffItemsConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求体无效: " + err.Error()})
		return
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "序列化失败: " + err.Error()})
		return
	}
	if err := gs.UserDB.SaveBuffItemsConfig(data); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "保存失败: " + err.Error()})
		return
	}
	logger.Info("[GM] 更新 BUFF 道具配置成功")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// handleGMRewardsGet 获取奖励配置
func handleGMRewardsGet(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	data, err := gs.UserDB.LoadRewardConfig()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "加载失败: " + err.Error()})
		return
	}
	if len(data) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "data": DefaultRewardConfigUnpacked()})
		return
	}
	var cfg RewardConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "解析失败: " + err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "data": cfg})
}

// handleGMRewardsSave 保存奖励配置
func handleGMRewardsSave(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	var cfg RewardConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求体无效: " + err.Error()})
		return
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "序列化失败: " + err.Error()})
		return
	}
	if err := gs.UserDB.SaveRewardConfig(data); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "保存失败: " + err.Error()})
		return
	}
	logger.Info("[GM] 更新奖励配置成功")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// handleGMFreshFightGet 获取试炼之塔配置列表
func handleGMFreshFightGet(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	list := GetFreshFightConfig()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    list,
	})
}

// handleGMFreshFightSave 保存试炼之塔配置列表
func handleGMFreshFightSave(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	var req struct {
		Levels []FreshFightLevelEntry `json:"levels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "请求体无效: " + err.Error(),
		})
		return
	}
	if err := SetFreshFightConfig(req.Levels); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "保存失败: " + err.Error(),
		})
		return
	}
	logger.Info(fmt.Sprintf("[GM] 更新试炼之塔配置，条目数=%d", len(req.Levels)))
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// handleGMFightLevelGet 获取勇者之塔配置列表（每层 Boss ID 列表）
func handleGMFightLevelGet(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	list := GetFightLevelConfig()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    list,
	})
}

// handleGMFightLevelSave 保存勇者之塔配置列表
func handleGMFightLevelSave(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	var req struct {
		Levels []FightLevelEntry `json:"levels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "请求体无效: " + err.Error(),
		})
		return
	}
	if err := SetFightLevelConfig(req.Levels); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "保存失败: " + err.Error(),
		})
		return
	}
	logger.Info(fmt.Sprintf("[GM] 更新勇者之塔配置，条目数=%d", len(req.Levels)))
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "保存成功",
	})
}

// handleGMDarkPortalGet 获取暗黑武斗场BOSS配置列表
func handleGMDarkPortalGet(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	list := GetDarkPortalConfig()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    list,
	})
}

// handleGMDarkPortalSave 保存暗黑武斗场BOSS配置列表
func handleGMDarkPortalSave(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	var req struct {
		Bosses []DarkPortalBossEntry `json:"bosses"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "请求体无效: " + err.Error(),
		})
		return
	}
	if err := SetDarkPortalConfig(req.Bosses); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "保存失败: " + err.Error(),
		})
		return
	}
	logger.Info(fmt.Sprintf("[GM] 更新暗黑武斗场配置，条目数=%d", len(req.Bosses)))
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "保存成功",
	})
}

// handleGMSPTBossGet 获取 SPT BOSS 配置（地图-BOSS、奖励、特性）
func handleGMSPTBossGet(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	cfg := GetSPTBossConfig()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    cfg,
	})
}

// handleGMSPTBossSave 保存 SPT BOSS 配置
func handleGMSPTBossSave(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	var req struct {
		Data *sptboss.FullConfig `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "请求体无效: " + err.Error(),
		})
		return
	}
	if err := SetSPTBossConfig(req.Data); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "保存失败: " + err.Error(),
		})
		return
	}
	logger.Info("[GM] 更新 SPT BOSS 配置成功")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "保存成功",
	})
}

// handleGMGetUsers 获取所有用户列表；支持 keyword 查找账号（按 userId、昵称、邮箱模糊匹配）
// 启用 MySQL 时从数据库 accounts + game_players + game_pets 查询，否则从内存
func handleGMGetUsers(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	keyword := strings.TrimSpace(r.URL.Query().Get("keyword"))
	logger.Info(fmt.Sprintf("[GM] 获取用户列表 keyword=%q", keyword))

	type UserInfo struct {
		UserID      int64  `json:"userId"`
		Email       string `json:"email"`
		Nickname    string `json:"nickname"`
		Coins       int    `json:"coins"`
		Gold        int    `json:"gold"`
		PetCount    int    `json:"petCount"`
		LastOnline  int64  `json:"lastOnline"`
		RoleCreated bool   `json:"roleCreated"`
	}

	var userList []UserInfo
	if gs.UserDB.UseMySQL() {
		list, _, err := gs.UserDB.MySQLListUsersForGM(keyword, 10000, 0)
		if err != nil {
			logger.Error(fmt.Sprintf("[GM] MySQL 用户列表失败: %v", err))
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "查询数据库失败"})
			return
		}
		for _, row := range list {
			userList = append(userList, UserInfo{
				UserID:      row.UserID,
				Email:       row.Email,
				Nickname:    row.Nickname,
				Coins:       row.Coins,
				Gold:        row.Gold,
				PetCount:    row.PetCount,
				LastOnline:  row.LastOnline,
				RoleCreated: row.RoleCreated,
			})
		}
	} else {
		allGameData := gs.UserDB.GetAllGameData()
		for userID, gameData := range allGameData {
			user := gs.UserDB.FindByUserID(userID)
			if user == nil {
				continue
			}
			if keyword != "" {
				idStr := fmt.Sprintf("%d", userID)
				if !strings.Contains(idStr, keyword) &&
					!strings.Contains(strings.ToLower(user.Nickname), strings.ToLower(keyword)) &&
					!strings.Contains(strings.ToLower(user.Email), strings.ToLower(keyword)) {
					continue
				}
			}
			userList = append(userList, UserInfo{
				UserID:      user.UserID,
				Email:       user.Email,
				Nickname:    user.Nickname,
				RoleCreated: user.RoleCreated,
				Coins:       gameData.Coins,
				Gold:        gameData.Gold,
				PetCount:    len(gameData.Pets),
				LastOnline:  gameData.LastOnline,
			})
		}
	}

	logger.Info(fmt.Sprintf("[GM] 返回 %d 个用户信息", len(userList)))
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    userList,
	})
}

// handleGMGetUser 获取单个用户详情；启用 MySQL 时从数据库读取，否则从内存
func handleGMGetUser(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	userIDStr := r.URL.Query().Get("userId")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "无效的用户ID",
		})
		return
	}

	var user *userdb.User
	var gameData *userdb.GameData
	if gs.UserDB.UseMySQL() {
		user, gameData, err = gs.UserDB.MySQLGetUserDetailForGM(userID)
		if err != nil || user == nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"message": "用户不存在",
			})
			return
		}
	} else {
		user = gs.UserDB.FindByUserID(userID)
		if user == nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"message": "用户不存在",
			})
			return
		}
		gameData = gs.UserDB.GetOrCreateGameData(userID)
	}
	
	// 为精灵添加中文名称，并规范化 Pet 供 GM 编辑（确保 Skills 非 nil、数值在合法范围，避免前端显示不全）
	normalizePetForGM := func(p *userdb.Pet) {
		if p == nil {
			return
		}
		if p.Skills == nil {
			p.Skills = []int{}
		}
		if len(p.Skills) > 4 {
			p.Skills = p.Skills[:4]
		}
		if p.Level < 1 {
			p.Level = 1
		}
		if p.Level > 100 {
			p.Level = 100
		}
		if p.DV < 0 {
			p.DV = 0
		}
		if p.DV > 31 {
			p.DV = 31
		}
		if p.Nature < 0 {
			p.Nature = 0
		}
		if p.Nature > 24 {
			p.Nature = 24
		}
		if p.Exp < 0 {
			p.Exp = 0
		}
		if p.Trait < 0 {
			p.Trait = 0
		}
	}
	for i := range gameData.Pets {
		gameData.Pets[i].Name = GetPetName(gameData.Pets[i].ID)
		normalizePetForGM(&gameData.Pets[i])
	}
	for i := range gameData.StoragePets {
		gameData.StoragePets[i].Name = GetPetName(gameData.StoragePets[i].ID)
		normalizePetForGM(&gameData.StoragePets[i])
	}
	
	// 为道具添加中文名称
	itemsWithNames := make(map[string]interface{})
	for idStr, item := range gameData.Items {
		if itemID, err := strconv.Atoi(idStr); err == nil {
			itemData := map[string]interface{}{
				"count":      item.Count,
				"expireTime": item.ExpireTime,
				"name":       GetItemName(itemID),
			}
			itemsWithNames[idStr] = itemData
		} else {
			itemsWithNames[idStr] = item
		}
	}

	// 为每个精灵附带 4 槽技能中文名（skillNames），供前端编辑弹窗直接显示；存档无技能时用等级默认技能填充
	petMgr := gamepets.GetInstance()
	buildPetWithSkillNames := func(p *userdb.Pet) map[string]interface{} {
		b, _ := json.Marshal(p)
		var m map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			m = make(map[string]interface{})
		}
		names := make([]string, 4)
		defaultSkills := petMgr.GetSkillsForLevel(p.ID, p.Level)
		for j := 0; j < 4; j++ {
			sid := 0
			if j < len(p.Skills) && p.Skills[j] > 0 {
				sid = p.Skills[j]
			}
			if sid == 0 && j < len(defaultSkills) && defaultSkills[j] > 0 {
				sid = defaultSkills[j]
			}
			if sid > 0 {
				n := GetSkillName(sid)
				if n == "" {
					n = fmt.Sprintf("技能#%d", sid)
				}
				names[j] = n
			}
		}
		m["skillNames"] = names
		return m
	}
	petsForGM := make([]map[string]interface{}, 0, len(gameData.Pets))
	for i := range gameData.Pets {
		petsForGM = append(petsForGM, buildPetWithSkillNames(&gameData.Pets[i]))
	}
	storagePetsForGM := make([]map[string]interface{}, 0, len(gameData.StoragePets))
	for i := range gameData.StoragePets {
		storagePetsForGM = append(storagePetsForGM, buildPetWithSkillNames(&gameData.StoragePets[i]))
	}

	// 创建带名称的游戏数据副本（含 mapId/posX/posY 供 kyse 风格 GM 面板显示）
	gameDataWithNames := map[string]interface{}{
		"nick":          gameData.Nick,
		"coins":         gameData.Coins,
		"gold":          gameData.Gold,
		"energy":        gameData.Energy,
		"expPool":       gameData.ExpPool,
		"pets":          petsForGM,
		"storagePets":   storagePetsForGM,
		"items":         itemsWithNames,
		"lastOnline":    gameData.LastOnline,
		"mapId":         gameData.MapID,
		"posX":          gameData.PosX,
		"posY":          gameData.PosY,
		"nono": map[string]interface{}{
			"level":      gameData.Nono.SuperLevel,
			"exp":        gameData.Nono.Power,
			"energy":     gameData.Nono.SuperEnergy,
			"type":       gameData.Nono.SuperNono,
			"status":     gameData.Nono.State,
			"skillPoints": gameData.Nono.IQ,
		},
	}
	
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"user":     user,
		"gameData": gameDataWithNames,
	})
}

// handleGMUpdateUser 更新用户信息
func handleGMUpdateUser(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "只支持POST请求",
		})
		return
	}
	
	var req struct {
		UserID   int64  `json:"userId"`
		Nickname string `json:"nickname"`
		Coins    *int   `json:"coins"`
		Gold     *int   `json:"gold"`
		Energy   *int   `json:"energy"`
		Nono     *struct {
			Level      *int `json:"level"`
			Exp        *int `json:"exp"`
			Energy     *int `json:"energy"`
			Type       *int `json:"type"`
			Status     *int `json:"status"`
			SkillPoints *int `json:"skillPoints"`
		} `json:"nono"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "请求参数错误",
		})
		return
	}
	
	user := gs.UserDB.FindByUserID(req.UserID)
	if user == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "用户不存在",
		})
		return
	}
	
	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	if gameData == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "游戏数据不存在",
		})
		return
	}
	
	// 更新昵称
	if req.Nickname != "" {
		user.Nickname = req.Nickname
		gameData.Nick = req.Nickname
	}
	
	// 更新金币
	if req.Coins != nil {
		gameData.Coins = *req.Coins
	}
	
	// 更新金豆
	if req.Gold != nil {
		gameData.Gold = *req.Gold
	}
	
	// 更新体力
	if req.Energy != nil {
		gameData.Energy = *req.Energy
	}
	
	// 更新NONO信息
	if req.Nono != nil {
		if req.Nono.Level != nil {
			gameData.Nono.SuperLevel = *req.Nono.Level
			// 根据超能等级自动更新形态（超能等级模型）
			// 超能等级最高为12级，超能形态分别是1、4、7、9、12，共五个形态
			calculatedType := calculateSuperNonoTypeByLevel(gameData.Nono.SuperLevel)
			gameData.Nono.SuperNono = calculatedType
		}
		if req.Nono.Exp != nil {
			gameData.Nono.Power = *req.Nono.Exp
		}
		if req.Nono.Energy != nil {
			gameData.Nono.SuperEnergy = *req.Nono.Energy
		}
		// 如果Level没有更新，但Type有更新，则允许手动设置（但通常应该由Level自动计算）
		if req.Nono.Type != nil && req.Nono.Level == nil {
			gameData.Nono.SuperNono = *req.Nono.Type
		}
		if req.Nono.Status != nil {
			gameData.Nono.State = *req.Nono.Status
		}
		if req.Nono.SkillPoints != nil {
			gameData.Nono.IQ = *req.Nono.SkillPoints
		}
	}
	
	gs.UserDB.SaveUser(user)
	gs.UserDB.SaveGameData(req.UserID, gameData)
	
	logger.Info(fmt.Sprintf("[GM] 更新用户信息: UserID=%d", req.UserID))
	
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "更新成功",
	})
}

// handleGMAddBlacklist 添加黑名单
func handleGMAddBlacklist(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "只支持POST请求",
		})
		return
	}
	
	var req struct {
		UserID       int64 `json:"userId"`
		BlockedID    int64 `json:"blockedId"`
		BlockedNick  string `json:"blockedNick"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "请求参数错误",
		})
		return
	}
	
	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	
	// 检查是否已在黑名单
	for _, entry := range gameData.Blacklist {
		if entry.UserID == req.BlockedID {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"message": "该用户已在黑名单中",
			})
			return
		}
	}
	
	// 添加到黑名单
	gameData.Blacklist = append(gameData.Blacklist, userdb.BlacklistEntry{
		UserID:  req.BlockedID,
		AddTime: time.Now().Unix(),
	})
	
	gs.UserDB.SaveGameData(req.UserID, gameData)
	
	logger.Info(fmt.Sprintf("[GM] 添加黑名单: UserID=%d BlockedID=%d", req.UserID, req.BlockedID))
	
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "添加成功",
	})
}

// handleGMRemoveBlacklist 移除黑名单
func handleGMRemoveBlacklist(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "只支持POST请求",
		})
		return
	}
	
	var req struct {
		UserID    int64 `json:"userId"`
		BlockedID int64 `json:"blockedId"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "请求参数错误",
		})
		return
	}
	
	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	
	// 从黑名单移除
	newBlacklist := []userdb.BlacklistEntry{}
	
	for _, entry := range gameData.Blacklist {
		if entry.UserID != req.BlockedID {
			newBlacklist = append(newBlacklist, entry)
		}
	}
	
	gameData.Blacklist = newBlacklist
	gs.UserDB.SaveGameData(req.UserID, gameData)
	
	logger.Info(fmt.Sprintf("[GM] 移除黑名单: UserID=%d BlockedID=%d", req.UserID, req.BlockedID))
	
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "移除成功",
	})
}

// handleGMAddItem 添加物品
func handleGMAddItem(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "只支持POST请求",
		})
		return
	}
	
	var req struct {
		UserID int64 `json:"userId"`
		ItemID int   `json:"itemId"`
		Count  int   `json:"count"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "请求参数错误",
		})
		return
	}
	
	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	
	itemKey := fmt.Sprintf("%d", req.ItemID)
	if item, ok := gameData.Items[itemKey]; ok {
		item.Count += req.Count
		gameData.Items[itemKey] = item
	} else {
		gameData.Items[itemKey] = userdb.Item{
			Count:      req.Count,
			ExpireTime: 360000,
		}
	}
	
	gs.UserDB.SaveGameData(req.UserID, gameData)
	
	logger.Info(fmt.Sprintf("[GM] 添加物品: UserID=%d ItemID=%d Count=%d", req.UserID, req.ItemID, req.Count))

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "添加成功",
	})
}

// handleGMDeleteItem 删除物品
func handleGMDeleteItem(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "只支持POST请求",
		})
		return
	}

	var req struct {
		UserID int64 `json:"userId"`
		ItemID int   `json:"itemId"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "请求参数错误",
		})
		return
	}

	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	itemKey := fmt.Sprintf("%d", req.ItemID)

	if _, ok := gameData.Items[itemKey]; !ok {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "用户未拥有该物品",
		})
		return
	}

	delete(gameData.Items, itemKey)
	gs.UserDB.SaveGameData(req.UserID, gameData)

	logger.Info(fmt.Sprintf("[GM] 删除物品: UserID=%d ItemID=%d", req.UserID, req.ItemID))

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "删除成功",
	})
}

// handleGMAddPet 添加精灵
func handleGMAddPet(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "只支持POST请求",
		})
		return
	}
	
	var req struct {
		UserID int64 `json:"userId"`
		PetID  int   `json:"petId"`
		Level  int   `json:"level"`
		// Trait 特性ID（NewSeIdx.Idx，1006-1045）。
		// 0 或缺省：不设置特性；
		// -1：为该精灵随机分配一个特性；
		// >0：直接指定该特性（调用方需保证在合法范围内）。
		Trait  int   `json:"trait"`
		// Nature 初始性格ID（0-24），缺省则使用 0。
		Nature *int  `json:"nature"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "请求参数错误",
		})
		return
	}
	
	if req.Level <= 0 {
		req.Level = 1
	}
	if req.Level > 100 {
		req.Level = 100
	}
	
	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	
	// 添加精灵
	newPet := userdb.Pet{
		ID:        req.PetID,
		CatchTime: int(time.Now().Unix()),
		Level:     req.Level,
		DV:        31, // 满个体
		Nature: func() int {
			if req.Nature != nil {
				return *req.Nature
			}
			return 0
		}(),
		Exp:       0,
		Name:      "",
	}
	// GM 可选择是否为该精灵设置特性：
	// - Trait > 0: 直接指定特性ID；
	// - Trait == -1: 按规则随机分配一个特性；
	// - 其他情况: 保持无特性（Trait=0）。
	if req.Trait > 0 {
		newPet.Trait = req.Trait
	} else if req.Trait == -1 {
		userdb.AssignFusionTraitIfNeeded(&newPet)
	}

	// GM 发放精灵时也遵循“背包最多 6 只”的规则，超出则存入仓库
	if len(gameData.Pets) >= 6 {
		if gameData.StoragePets == nil {
			gameData.StoragePets = []userdb.Pet{}
		}
		gameData.StoragePets = append(gameData.StoragePets, newPet)
	} else {
		gameData.Pets = append(gameData.Pets, newPet)
	}
	gs.UserDB.SaveGameData(req.UserID, gameData)
	
	logger.Info(fmt.Sprintf("[GM] 添加精灵: UserID=%d PetID=%d Level=%d", req.UserID, req.PetID, req.Level))

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "添加成功",
	})
}

// handleGMUpdatePet 修改背包精灵全部属性（精灵ID、等级、经验、名称、个体值、性格、特性、技能、学习力）；按 userId + catchTime 定位精灵
func handleGMUpdatePet(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		UserID    int64    `json:"userId"`
		CatchTime int      `json:"catchTime"`
		Level     *int     `json:"level"`
		Exp       *int     `json:"exp"`
		Name      *string  `json:"name"`
		ID        *int     `json:"id"`   // 精灵ID（种类）
		DV        *int     `json:"dv"`   // 个体值 0-31
		Nature    *int     `json:"nature"` // 性格 0-24
		Trait     *int     `json:"trait"`  // 特性 ID，0=无
		Skills    []int    `json:"skills"` // 技能槽最多4个
		EVHP      *int     `json:"ev_hp"`
		EVAttack  *int     `json:"ev_attack"`
		EVDefence *int     `json:"ev_defence"`
		EVSpAtk   *int     `json:"ev_sa"`
		EVSpDef   *int     `json:"ev_sd"`
		EVSpeed   *int     `json:"ev_sp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	updates := make(map[string]interface{})
	if req.Level != nil {
		lv := *req.Level
		if lv < 1 {
			lv = 1
		}
		if lv > 100 {
			lv = 100
		}
		updates["level"] = lv
	}
	if req.Exp != nil {
		exp := *req.Exp
		if exp < 0 {
			exp = 0
		}
		updates["exp"] = exp
	}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.ID != nil && *req.ID > 0 {
		updates["id"] = *req.ID
	}
	if req.DV != nil {
		dv := *req.DV
		if dv < 0 {
			dv = 0
		}
		if dv > 31 {
			dv = 31
		}
		updates["dv"] = dv
	}
	if req.Nature != nil {
		n := *req.Nature
		if n < 0 {
			n = 0
		}
		if n > 24 {
			n = 24
		}
		updates["nature"] = n
	}
	if req.Trait != nil && *req.Trait >= 0 {
		updates["trait"] = *req.Trait
	}
	if len(req.Skills) > 0 {
		skills := req.Skills
		if len(skills) > 4 {
			skills = skills[:4]
		}
		updates["skills"] = skills
	}
	if req.EVHP != nil && *req.EVHP >= 0 {
		updates["ev_hp"] = *req.EVHP
	}
	if req.EVAttack != nil && *req.EVAttack >= 0 {
		updates["ev_attack"] = *req.EVAttack
	}
	if req.EVDefence != nil && *req.EVDefence >= 0 {
		updates["ev_defence"] = *req.EVDefence
	}
	if req.EVSpAtk != nil && *req.EVSpAtk >= 0 {
		updates["ev_sa"] = *req.EVSpAtk
	}
	if req.EVSpDef != nil && *req.EVSpDef >= 0 {
		updates["ev_sd"] = *req.EVSpDef
	}
	if req.EVSpeed != nil && *req.EVSpeed >= 0 {
		updates["ev_sp"] = *req.EVSpeed
	}
	if len(updates) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请至少提供一项要修改的字段"})
		return
	}
	updated := gs.UserDB.UpdatePet(req.UserID, req.CatchTime, updates)
	if updated == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "未找到该精灵"})
		return
	}
	logger.Info(fmt.Sprintf("[GM] 修改精灵属性: UserID=%d CatchTime=%d updates=%v", req.UserID, req.CatchTime, updates))
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "修改成功"})
}

// handleGMUpdatePetStats 修改精灵等级 / 学习力 / 个体（背包与仓库精灵通用）
// 前端请求体示例：
// {
//   "userId": 10001,
//   "catchTime": 123456789,
//   "fromStorage": false,
//   "level": 80,
//   "dv": 31,
//   "evHp": 252,
//   "evAtk": 252,
//   "evDef": 4,
//   "evSa": 0,
//   "evSd": 0,
//   "evSp": 0
// }
func handleGMUpdatePetStats(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		UserID      int64 `json:"userId"`
		CatchTime   int   `json:"catchTime"`
		FromStorage bool  `json:"fromStorage"`
		Level       int   `json:"level"`
		DV          *int  `json:"dv"`   // 可选，nil 表示不修改
		EVHp        int   `json:"evHp"` // 以下学习力字段均为非负整数
		EVAtk       int   `json:"evAtk"`
		EVDef       int   `json:"evDef"`
		EVSa        int   `json:"evSa"`
		EVSd        int   `json:"evSd"`
		EVSp        int   `json:"evSp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	if req.UserID == 0 || req.CatchTime == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "userId 或 catchTime 无效"})
		return
	}

	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	var picked *userdb.Pet

	// 先按 fromStorage 标志在对应列表查找
	if req.FromStorage {
		for i := range gameData.StoragePets {
			if gameData.StoragePets[i].CatchTime == req.CatchTime {
				picked = &gameData.StoragePets[i]
				break
			}
		}
	} else {
		for i := range gameData.Pets {
			if gameData.Pets[i].CatchTime == req.CatchTime {
				picked = &gameData.Pets[i]
				break
			}
		}
	}
	// 兜底：如果指定列表没找到，再在另一侧找一次，防止标记错误
	if picked == nil {
		for i := range gameData.Pets {
			if gameData.Pets[i].CatchTime == req.CatchTime {
				picked = &gameData.Pets[i]
				break
			}
		}
		if picked == nil {
			for i := range gameData.StoragePets {
				if gameData.StoragePets[i].CatchTime == req.CatchTime {
					picked = &gameData.StoragePets[i]
					break
				}
			}
		}
	}
	if picked == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "未找到该精灵"})
		return
	}

	// 更新等级
	if req.Level > 0 {
		if req.Level < 1 {
			req.Level = 1
		}
		if req.Level > 100 {
			req.Level = 100
		}
		picked.Level = req.Level
	}

	// 更新个体值（可选）
	if req.DV != nil {
		dv := *req.DV
		if dv < 0 {
			dv = 0
		}
		if dv > 31 {
			dv = 31
		}
		picked.DV = dv
	}

	// 更新学习力（简单按 0-255 范围裁剪）
	clampEV := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return v
	}
	picked.EVHP = clampEV(req.EVHp)
	picked.EVAttack = clampEV(req.EVAtk)
	picked.EVDefence = clampEV(req.EVDef)
	picked.EVSpAtk = clampEV(req.EVSa)
	picked.EVSpDef = clampEV(req.EVSd)
	picked.EVSpeed = clampEV(req.EVSp)

	gs.UserDB.SaveGameData(req.UserID, gameData)
	logger.Info(fmt.Sprintf("[GM] 修改精灵成长: UserID=%d CatchTime=%d fromStorage=%v Level=%d DV=%v EV=(%d,%d,%d,%d,%d,%d)",
		req.UserID, req.CatchTime, req.FromStorage, picked.Level, req.DV, picked.EVHP, picked.EVAttack, picked.EVDefence, picked.EVSpAtk, picked.EVSpDef, picked.EVSpeed))

	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "修改成功"})
}

// handleGMPetList 返回精灵 ID 与中文名称列表，供 GM 前端下拉选择与搜索
func handleGMPetList(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    GetAllPetsForGM(),
	})
}

// handleGMItemList 返回道具 ID 与中文名称列表，供 GM 前端下拉选择与搜索
func handleGMItemList(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    GetAllItemsForGM(),
	})
}

// handleGMSoulPearlsList 返回元神珠 itemID + 名称列表，供融合管理下拉
func handleGMSoulPearlsList(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    GetSoulPearlOptionsForGM(),
	})
}

// handleGMFusionRulesGet 返回当前自定义融合规则列表（含精灵名、元神珠名）
func handleGMFusionRulesGet(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	rules := GetAllFusionRules()
	list := make([]map[string]interface{}, 0, len(rules))
	for _, e := range rules {
		list = append(list, map[string]interface{}{
			"petIdA":       e.PetIdA,
			"petIdB":       e.PetIdB,
			"soulPearlId":  e.SoulPearlId,
			"petNameA":     GetPetName(e.PetIdA),
			"petNameB":     GetPetName(e.PetIdB),
			"soulPearlName": GetItemName(e.SoulPearlId),
		})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    list,
	})
}

// handleGMFusionRulesPost 保存自定义融合规则，body: { "rules": [ { "petIdA", "petIdB", "soulPearlId" } ] }
func handleGMFusionRulesPost(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	var req struct {
		Rules []FusionRuleEntry `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"success":false,"message":"请求体无效"}`, http.StatusBadRequest)
		return
	}
	if err := SaveFusionRulesConfig(req.Rules); err != nil {
		logger.Warning("GM 融合规则保存失败: " + err.Error())
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "保存失败: " + err.Error()})
		return
	}
	LoadFusionRulesConfig()
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "保存成功"})
}

// loadGMSkillsFromLocalFile 从 GM/skills.xml 本地文件加载技能列表，失败返回 nil
func loadGMSkillsFromLocalFile() []gameskills.SkillInfo {
	exePath, err := os.Executable()
	if err != nil {
		return nil
	}
	exeDir := filepath.Dir(exePath)
	candidates := []string{
		filepath.Join(exeDir, "GM", "skills.xml"),
		filepath.Join(exeDir, "..", "GM", "skills.xml"),
		filepath.Join("GM", "skills.xml"),
	}
	var data []byte
	for _, p := range candidates {
		data, err = os.ReadFile(p)
		if err == nil && len(data) > 0 {
			break
		}
	}
	if len(data) == 0 {
		return nil
	}
	var root struct {
		XMLName struct{} `xml:"MovesTbl"`
		Moves   []struct {
			ID   int    `xml:"ID,attr"`
			Name string `xml:"Name,attr"`
		} `xml:"Moves>Move"`
	}
	if err := xml.Unmarshal(data, &root); err != nil {
		logger.Warning(fmt.Sprintf("[GM] 解析 GM/skills.xml 失败: %v", err))
		return nil
	}
	list := make([]gameskills.SkillInfo, 0, len(root.Moves))
	for _, m := range root.Moves {
		if m.ID > 0 {
			name := m.Name
			if name == "" {
				name = fmt.Sprintf("技能#%d", m.ID)
			}
			list = append(list, gameskills.SkillInfo{ID: m.ID, Name: name})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// handleGMSkillList 返回技能 ID 与中文名称列表，供 GM 编辑精灵技能下拉选择；优先使用 GM/skills.xml 本地文件
func handleGMSkillList(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	list := loadGMSkillsFromLocalFile()
	if list == nil {
		list = gameskills.GetInstance().GetAllSkillsForGM()
	}
	data := make([]gameskills.SkillInfo, 0, len(list)+1)
	data = append(data, gameskills.SkillInfo{ID: 0, Name: "无"})
	data = append(data, list...)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    data,
	})
}

// handleGMTraitList 返回特性 ID 与显示名列表，供 GM 编辑精灵特性下拉选择
func handleGMTraitList(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    GetAllTraitsForGM(),
	})
}

// handleGMUpdatePetSkills 修改精灵技能（4 个技能槽，传技能 ID 数组）
func handleGMUpdatePetSkills(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		UserID    int64   `json:"userId"`
		CatchTime int     `json:"catchTime"`
		Skills    []int   `json:"skills"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	var picked *userdb.Pet
	for i := range gameData.Pets {
		if gameData.Pets[i].CatchTime == req.CatchTime {
			picked = &gameData.Pets[i]
			break
		}
	}
	if picked == nil {
		if gameData.StoragePets != nil {
			for i := range gameData.StoragePets {
				if gameData.StoragePets[i].CatchTime == req.CatchTime {
					picked = &gameData.StoragePets[i]
					break
				}
			}
		}
	}
	if picked == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "未找到该精灵"})
		return
	}
	skillMgr := gameskills.GetInstance()
	petMgr := gamepets.GetInstance()
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
	finalSkills := make([]int, 4)
	for i := 0; i < 4; i++ {
		sid := 0
		if i < len(req.Skills) {
			sid = req.Skills[i]
		}
		if sid > 1 && skillMgr.Exists(sid) {
			finalSkills[i] = sid
		} else {
			finalSkills[i] = currentSkills[i]
		}
	}
	picked.Skills = finalSkills
	gs.UserDB.SaveGameData(req.UserID, gameData)
	logger.Info(fmt.Sprintf("[GM] 修改精灵技能: UserID=%d CatchTime=%d PetID=%d skills=%v", req.UserID, req.CatchTime, picked.ID, picked.Skills))
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "修改成功"})
}

// handleGMUpdatePetTrait 修改精灵特性
func handleGMUpdatePetTrait(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		UserID    int64 `json:"userId"`
		CatchTime int   `json:"catchTime"`
		Trait     int   `json:"trait"` // 0=清除特性，-1=随机分配，>0=指定特性ID
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	var picked *userdb.Pet
	for i := range gameData.Pets {
		if gameData.Pets[i].CatchTime == req.CatchTime {
			picked = &gameData.Pets[i]
			break
		}
	}
	if picked == nil && gameData.StoragePets != nil {
		for i := range gameData.StoragePets {
			if gameData.StoragePets[i].CatchTime == req.CatchTime {
				picked = &gameData.StoragePets[i]
				break
			}
		}
	}
	if picked == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "未找到该精灵"})
		return
	}

	if req.Trait > 0 {
		picked.Trait = req.Trait
	} else if req.Trait == -1 {
		picked.Trait = 0
		userdb.AssignFusionTraitIfNeeded(picked)
	} else {
		picked.Trait = 0
	}

	gs.UserDB.SaveGameData(req.UserID, gameData)
	logger.Info(fmt.Sprintf("[GM] 修改精灵特性: UserID=%d CatchTime=%d PetID=%d Trait=%d", req.UserID, req.CatchTime, picked.ID, picked.Trait))
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "修改成功"})
}

// handleGMUpdatePetNature 修改精灵性格
func handleGMUpdatePetNature(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		UserID    int64 `json:"userId"`
		CatchTime int   `json:"catchTime"`
		Nature    int   `json:"nature"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	var picked *userdb.Pet
	for i := range gameData.Pets {
		if gameData.Pets[i].CatchTime == req.CatchTime {
			picked = &gameData.Pets[i]
			break
		}
	}
	if picked == nil && gameData.StoragePets != nil {
		for i := range gameData.StoragePets {
			if gameData.StoragePets[i].CatchTime == req.CatchTime {
				picked = &gameData.StoragePets[i]
				break
			}
		}
	}
	if picked == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "未找到该精灵"})
		return
	}

	picked.Nature = req.Nature
	gs.UserDB.SaveGameData(req.UserID, gameData)
	logger.Info(fmt.Sprintf("[GM] 修改精灵性格: UserID=%d CatchTime=%d PetID=%d Nature=%d", req.UserID, req.CatchTime, picked.ID, picked.Nature))
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "修改成功"})
}

// handleGMDeletePet 删除用户精灵（背包或仓库，按 catchTime 定位）
func handleGMDeletePet(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		UserID    int64 `json:"userId"`
		CatchTime int   `json:"catchTime"`
		FromStorage bool `json:"fromStorage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	if req.FromStorage {
		newList := make([]userdb.Pet, 0, len(gameData.StoragePets))
		for _, p := range gameData.StoragePets {
			if p.CatchTime != req.CatchTime {
				newList = append(newList, p)
			}
		}
		gameData.StoragePets = newList
	} else {
		newList := make([]userdb.Pet, 0, len(gameData.Pets))
		for _, p := range gameData.Pets {
			if p.CatchTime != req.CatchTime {
				newList = append(newList, p)
			}
		}
		gameData.Pets = newList
	}
	gs.UserDB.SaveGameData(req.UserID, gameData)
	logger.Info(fmt.Sprintf("[GM] 删除精灵: UserID=%d CatchTime=%d fromStorage=%v", req.UserID, req.CatchTime, req.FromStorage))
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "删除成功"})
}

// handleGMDeleteUser 删除角色账号
func handleGMDeleteUser(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		UserID int64 `json:"userId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	if err := gs.UserDB.DeleteUser(req.UserID); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": err.Error()})
		return
	}
	gs.RemoveUserCache(req.UserID)
	gs.UserDB.SaveToFile()
	logger.Info(fmt.Sprintf("[GM] 删除账号: UserID=%d", req.UserID))
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "账号已删除"})
}

// handleGMCreateUser 新增账号
func handleGMCreateUser(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Nickname string `json:"nickname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	if req.Email == "" || req.Password == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "邮箱和密码不能为空"})
		return
	}
	user, err := gs.UserDB.CreateUser(req.Email, req.Password)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": err.Error()})
		return
	}
	if req.Nickname != "" {
		user.Nickname = req.Nickname
		gs.UserDB.SaveUser(user)
	}
	_ = gs.UserDB.GetOrCreateGameData(user.UserID)
	gs.UserDB.SaveToFile()
	logger.Info(fmt.Sprintf("[GM] 新增账号: UserID=%d Email=%s", user.UserID, user.Email))
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "创建成功",
		"userId":  user.UserID,
		"email":   user.Email,
	})
}

// handleGMTransform1Sec 将当前用户正在转化/孵化的元神珠或精元设为 1 秒完成
func handleGMTransform1Sec(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		UserID int64 `json:"userId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	if gameData.SoulBeadTransform == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "该用户没有正在转化/孵化的元神珠或精元"})
		return
	}
	gameData.SoulBeadTransform.ExpireTime = time.Now().Unix() + 1
	gs.UserDB.SaveGameData(req.UserID, gameData)
	logger.Info(fmt.Sprintf("[GM] 元神珠/精元 1秒完成: UserID=%d", req.UserID))
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "已设为1秒后完成"})
}

// handleGMExpPoolAdd 发放经验到用户经验分配器（经验池）
func handleGMExpPoolAdd(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		UserID int64 `json:"userId"`
		Amount int   `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	if req.Amount < 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "发放经验不能为负"})
		return
	}
	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	if gameData.ExpPool < 0 {
		gameData.ExpPool = 0
	}
	gameData.ExpPool += req.Amount
	gs.UserDB.SaveGameData(req.UserID, gameData)
	logger.Info(fmt.Sprintf("[GM] 经验池发放: UserID=%d +%d 当前=%d", req.UserID, req.Amount, gameData.ExpPool))
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "发放成功",
		"expPool": gameData.ExpPool,
	})
}

// handleGMExpPoolSet 设置用户经验分配器（经验池）当前数值
func handleGMExpPoolSet(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		UserID int64 `json:"userId"`
		Value  int   `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	if req.Value < 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "经验池数值不能为负"})
		return
	}
	gameData := gs.UserDB.GetOrCreateGameData(req.UserID)
	gameData.ExpPool = req.Value
	gs.UserDB.SaveGameData(req.UserID, gameData)
	logger.Info(fmt.Sprintf("[GM] 经验池设置: UserID=%d 设为 %d", req.UserID, gameData.ExpPool))
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "设置成功",
		"expPool": gameData.ExpPool,
	})
}

// ==================== 扭蛋机 GM 模块 ====================

// handleGMGachaList 扭蛋机奖励列表（带中文名称、索引）
func handleGMGachaList(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	list := GetGachaRewards()
	items := make([]map[string]interface{}, 0, len(list))
	for i, r := range list {
		name := r.Name
		if name == "" {
			name = GetItemName(r.ItemID)
		}
		items = append(items, map[string]interface{}{
			"index":   i,
			"itemID":  r.ItemID,
			"weight":  r.Weight,
			"name":    name,
			"isGold":  r.IsGold,
		})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    items,
		"total":   len(items),
	})
}

// handleGMGachaUpdate 修改扭蛋机奖励（按索引）
func handleGMGachaUpdate(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		Index   int    `json:"index"`
		ItemID  int    `json:"itemID"`
		Weight  int    `json:"weight"`
		Name    string `json:"name"`
		IsGold  bool   `json:"isGold"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	reward := GachaReward{ItemID: req.ItemID, Weight: req.Weight, Name: req.Name, IsGold: req.IsGold}
	if ok := UpdateGachaReward(req.Index, reward); !ok {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "索引无效或越界"})
		return
	}
	logger.Info(fmt.Sprintf("[GM] 扭蛋机修改奖励: index=%d itemID=%d weight=%d", req.Index, req.ItemID, req.Weight))
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "修改成功"})
}

// handleGMGachaAdd 添加扭蛋机奖励（任意道具均可添加）
func handleGMGachaAdd(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		ItemID int    `json:"itemID"`
		Weight int    `json:"weight"`
		Name   string `json:"name"`
		IsGold bool   `json:"isGold"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	if req.ItemID <= 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "itemID 必须大于 0"})
		return
	}
	AddGachaReward(GachaReward{ItemID: req.ItemID, Weight: req.Weight, Name: req.Name, IsGold: req.IsGold})
	logger.Info(fmt.Sprintf("[GM] 扭蛋机添加奖励: itemID=%d weight=%d 名称=%s", req.ItemID, req.Weight, GetItemName(req.ItemID)))
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "添加成功"})
}

// handleGMGachaRemove 删除扭蛋机奖励（按索引）
func handleGMGachaRemove(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	var req struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "请求参数错误"})
		return
	}
	if ok := RemoveGachaReward(req.Index); !ok {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "索引无效或越界"})
		return
	}
	logger.Info(fmt.Sprintf("[GM] 扭蛋机删除奖励: index=%d", req.Index))
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "删除成功"})
}

// handleGMGachaSave 保存扭蛋机奖励到数据库或 gacha_rewards.json
func handleGMGachaSave(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "只支持POST请求"})
		return
	}
	if err := SaveGachaRewards(); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": err.Error()})
		return
	}
	logger.Info("[GM] 扭蛋机配置已保存")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "保存成功"})
}

// handleGMWeightsGet 获取权重配置（胶囊列表仅名称+捕捉率；精元列表+每精元融合成功率）
func handleGMWeightsGet(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	cfg := GetWeightsConfig()
	// 胶囊：若配置为空则用默认列表，保证前端始终有数据可展示
	capsuleRates := cfg.CapsuleCatchRates
	if len(capsuleRates) == 0 {
		capsuleRates = getDefaultCapsuleRatesForGM()
	}
	capsules := []map[string]interface{}{}
	for id, rate := range capsuleRates {
		name := GetItemName(mustParseInt(id))
		if name == "" {
			name = "胶囊#" + id
		}
		capsules = append(capsules, map[string]interface{}{
			"itemID": id,
			"name":   name,
			"rate":   rate,
		})
	}
	sort.Slice(capsules, func(i, j int) bool {
		a, _ := strconv.Atoi(capsules[i]["itemID"].(string))
		b, _ := strconv.Atoi(capsules[j]["itemID"].(string))
		return a < b
	})
	// 精灵融合成功率 + 普通/VIP 转化时间 + 元神转化完成给予精灵权重，按元神珠配置
	fusionList := GetSoulPearlListForGM(cfg.FusionSuccessRates, cfg.SoulPearlTransmuteTm, cfg.SoulPearlVipTransmuteTm, cfg.FusionSuccessRate, cfg.SoulPearlRewardPets)
	fusionRates := make([]map[string]interface{}, 0, len(fusionList))
	for _, sp := range fusionList {
		fusionRates = append(fusionRates, map[string]interface{}{
			"itemID":         sp.ItemID,
			"name":           sp.Name,
			"rate":           sp.Rate,
			"transmuteTm":    sp.TransmuteTm,
			"vipTransmuteTm": sp.VipTransmuteTm,
			"rewardPets":     sp.RewardPets,
		})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":            true,
		"capsuleCatchRates":  capsules,
		"fusionSuccessRates": fusionRates,
	})
}

func mustParseInt(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}

// handleGMWeightsUpdate 更新权重配置（支持按精元的融合成功率）
func handleGMWeightsUpdate(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	var req struct {
		CapsuleCatchRates        map[string]int            `json:"capsuleCatchRates"`
		FusionSuccessRate        *int                      `json:"fusionSuccessRate"`
		FusionSuccessRates       map[string]int            `json:"fusionSuccessRates"`
		SoulPearlTransmuteTm     map[string]int            `json:"soulPearlTransmuteTm"`    // 元神珠 ID -> 普通用户转化时间(秒)
		SoulPearlVipTransmuteTm  map[string]int            `json:"soulPearlVipTransmuteTm"` // 元神珠 ID -> VIP 转化时间(秒)
		SoulPearlRewardPets      map[string][]WeightedPetEntry `json:"soulPearlRewardPets"`      // 元神珠 ID -> [{ petClass, weight }]，转化完成只给 1 只
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "参数错误: " + err.Error()})
		return
	}
	cfg := GetWeightsConfig()
	if req.CapsuleCatchRates != nil {
		for k, v := range req.CapsuleCatchRates {
			if v < 0 {
				v = 0
			}
			if v > 100 {
				v = 100
			}
			cfg.CapsuleCatchRates[k] = v
		}
	}
	if req.FusionSuccessRate != nil {
		v := *req.FusionSuccessRate
		if v < 0 {
			v = 0
		}
		if v > 100 {
			v = 100
		}
		cfg.FusionSuccessRate = v
	}
	if req.FusionSuccessRates != nil {
		for k, v := range req.FusionSuccessRates {
			if v < 0 {
				v = 0
			}
			if v > 100 {
				v = 100
			}
			cfg.FusionSuccessRates[k] = v
		}
	}
	if req.SoulPearlTransmuteTm != nil {
		for k, v := range req.SoulPearlTransmuteTm {
			if v < 0 {
				v = 0
			}
			cfg.SoulPearlTransmuteTm[k] = v
		}
	}
	if req.SoulPearlVipTransmuteTm != nil {
		for k, v := range req.SoulPearlVipTransmuteTm {
			if v < 0 {
				v = 0
			}
			cfg.SoulPearlVipTransmuteTm[k] = v
		}
	}
	if req.SoulPearlRewardPets != nil {
		cfg.SoulPearlRewardPets = req.SoulPearlRewardPets
	}
	SetWeightsConfig(cfg)
	if err := SaveWeightsConfig(); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": err.Error()})
		return
	}
	logger.Info("[GM] 权重配置已更新并保存")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "保存成功"})
}

// handleGMServerStatus 服务器状态（kyse_seer 风格：在线人数、总玩家数、运行时间；含数据库对接信息）
func handleGMServerStatus(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	online := len(gs.GetOnlineUserIDs())
	allData := gs.UserDB.GetAllGameData()
	total := len(allData)
	data := map[string]interface{}{
		"onlinePlayers": online,
		"totalPlayers":  total,
		"uptime":        0,
		"memory":        map[string]interface{}{"heapUsed": 0, "heapTotal": 0},
	}
	if gs.UserDB != nil {
		host, dbName, connected := gs.UserDB.GetMySQLInfo()
		if connected {
			data["database"] = map[string]interface{}{
				"type":      "mysql",
				"connected": true,
				"host":      host,
				"database":  dbName,
			}
		} else {
			data["database"] = map[string]interface{}{"type": "file", "connected": true}
		}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    data,
	})
}

// handleGMOnlinePlayers 在线玩家列表（kyse_seer 风格）
func handleGMOnlinePlayers(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	ids := gs.GetOnlineUserIDs()
	list := make([]map[string]interface{}, 0, len(ids))
	for _, uid := range ids {
		user := gs.UserDB.FindByUserID(uid)
		gd := gs.UserDB.GetOrCreateGameData(uid)
		if gd == nil {
			continue
		}
		regTime := int64(0)
		if user != nil {
			regTime = user.RegisterTime
		}
		list = append(list, map[string]interface{}{
			"userID":    uid,
			"nick":      gd.Nick,
			"coins":     gd.Coins,
			"petMaxLev": gd.PetMaxLev,
			"vipLevel":  gd.Nono.VipLevel,
			"mapID":     gd.MapID,
			"regTime":   regTime,
		})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    list,
	})
}

// handleGMLogs 操作日志（占位：返回空列表，与 kyse_seer 前端兼容）
func handleGMLogs(w http.ResponseWriter, r *http.Request, gs *gameserver.GameServer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"logs":    []interface{}{},
		"total":   0,
	})
}

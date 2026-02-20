package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/seer-game/golang-version/internal/core/logger"
	"github.com/seer-game/golang-version/internal/game/mapogres"
)

// gmMapConfigFile GM 地图配置持久化文件
const gmMapConfigFile = "gm_map_config.json"

var (
	mapConfigsPersistence MapConfigsPersistence // 若为 nil 则使用本地 JSON 文件；由 main 在启用 MySQL 时设置为 UserDB
	mapConfigsPersistenceMu sync.RWMutex
)

// MapConfigsPersistence 地图配置持久化（数据库或文件），由 main 在启动时注入
type MapConfigsPersistence interface {
	LoadMapConfigs() ([]byte, error)
	SaveMapConfigs(data []byte) error
}

// SetMapConfigsPersistence 设置持久化实现。启用 MySQL 时传入 *userdb.UserDB；否则不调用，将使用 gm_map_config.json
func SetMapConfigsPersistence(p MapConfigsPersistence) {
	mapConfigsPersistenceMu.Lock()
	defer mapConfigsPersistenceMu.Unlock()
	mapConfigsPersistence = p
}

// GMMapConfigRequest POST /gm/maps/config 的请求体
type GMMapConfigRequest struct {
	Configs []mapogres.GMMapConfig `json:"configs"`
}

// handleGMMapConfigsGet 返回所有地图的当前刷新配置（内置 + GM 覆盖）
func handleGMMapConfigsGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	list := mapogres.GetAllMapsForGM()
	// 为了前端显示稳定，将 MapID 排序
	sort.Slice(list, func(i, j int) bool {
		return list[i].MapID < list[j].MapID
	})

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data":    list,
	})
}

// handleGMMapConfigsPost 保存 GM 自定义的地图刷新配置，并应用到运行时
func handleGMMapConfigsPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "只支持POST请求",
		})
		return
	}

	var req GMMapConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "请求体无效: " + err.Error(),
		})
		return
	}

	// 应用到运行时
	mapogres.SetGMMapConfigs(req.Configs)

	// 持久化到数据库或本地文件
	data, err := json.MarshalIndent(req.Configs, "", "  ")
	if err != nil {
		logger.Warning("[GM] 地图配置保存失败: " + err.Error())
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "保存失败: " + err.Error(),
		})
		return
	}

	mapConfigsPersistenceMu.RLock()
	persistence := mapConfigsPersistence
	mapConfigsPersistenceMu.RUnlock()

	if persistence != nil {
		// 使用数据库持久化
		if err := persistence.SaveMapConfigs(data); err != nil {
			logger.Warning("[GM] 写入数据库失败: " + err.Error())
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"message": "保存到数据库失败: " + err.Error(),
			})
			return
		}
		logger.Info("[GM] 地图刷新配置已更新并保存到数据库")
	} else {
		// 使用文件持久化
		path := gmMapConfigFile
		if dir, err := os.Getwd(); err == nil {
			path = filepath.Join(dir, gmMapConfigFile)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			logger.Warning("[GM] 写入 gm_map_config.json 失败: " + err.Error())
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"message": "写入文件失败: " + err.Error(),
			})
			return
		}
		logger.Info("[GM] 地图刷新配置已更新并保存到 gm_map_config.json")
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "保存成功",
	})
}

// LoadGMMapConfigsOnStart 在服务器启动时调用，从数据库或本地 JSON 加载 GM 地图配置（若已 SetMapConfigsPersistence 则从 DB，否则从 gm_map_config.json）
func LoadGMMapConfigsOnStart() {
	mapConfigsPersistenceMu.RLock()
	persistence := mapConfigsPersistence
	mapConfigsPersistenceMu.RUnlock()

	var data []byte
	var err error
	if persistence != nil {
		// 从数据库加载
		data, err = persistence.LoadMapConfigs()
		if err != nil {
			logger.Warning("[GM] 从数据库加载地图配置失败: " + err.Error())
			return
		}
		if len(data) == 0 {
			return
		}
		logger.Info("[GM] 已从数据库加载地图刷新配置")
	} else {
		// 从文件加载
		path := gmMapConfigFile
		if dir, err := os.Getwd(); err == nil {
			path = filepath.Join(dir, gmMapConfigFile)
		}
		data, err = os.ReadFile(path)
		if err != nil || len(data) == 0 {
			return
		}
		logger.Info("[GM] 已从 gm_map_config.json 加载地图刷新配置")
	}

	var list []mapogres.GMMapConfig
	if err := json.Unmarshal(data, &list); err != nil {
		logger.Warning("[GM] 解析地图配置失败: " + err.Error())
		return
	}
	mapogres.SetGMMapConfigs(list)
}

// SaveGMMapConfigsToPersistence 将当前地图配置写入已设置的持久化（数据库或运行目录离线配置）。
func SaveGMMapConfigsToPersistence() error {
	mapConfigsPersistenceMu.RLock()
	persistence := mapConfigsPersistence
	mapConfigsPersistenceMu.RUnlock()
	if persistence == nil {
		return nil
	}
	data, err := json.MarshalIndent(mapogres.GetGMMapConfigs(), "", "  ")
	if err != nil {
		return err
	}
	return persistence.SaveMapConfigs(data)
}


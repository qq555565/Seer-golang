package resserver

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/seer-game/golang-version/internal/core/logger"
	"github.com/seer-game/golang-version/internal/core/nonoformcache"
	"github.com/seer-game/golang-version/internal/core/soultransformcache"
	"github.com/seer-game/golang-version/internal/handlers"
)

// Config 资源服务器配置
type Config struct {
	ResPort              int    `json:"res_port"`
	ResPort80            int    `json:"res_port_80"`
	ResDir               string `json:"res_dir"`
	ResProxyDir          string `json:"res_proxy_dir"`
	ResOfficialAddress   string `json:"res_official_address"`
	LocalServerMode      bool   `json:"local_server_mode"`
	UseOfficialResources bool   `json:"use_official_resources"`
	PureOfficialMode     bool   `json:"pure_official_mode"`
	LoginPort            int    `json:"login_port"`
	LoginServerAddress   string `json:"login_server_address"`
	OfficialLoginServer  string `json:"official_login_server"`
	OfficialLoginPort    int    `json:"official_login_port"`
	// PublicIP 对外暴露给客户端使用的服务器 IP（生成 ServerR.xml、ip.txt 等），默认 127.0.0.1
	PublicIP string `json:"public_ip"`
}

// ResourceServer 资源服务器
type ResourceServer struct {
	config   Config
	server   *http.Server
	server80 *http.Server
}

// tryItemIconForPet 尝试将 groupFightResource/pet/XXXX.swf 映射到「道具图标」：
// - 精灵道具（300000-399999）：resource/item/petItem/icon/XXXX.swf
// - 元素/扭蛋牌等老道具：resource/fitment/icon/XXXX.swf
// 目前主要用于扭蛋/奖励界面中，客户端错误地按精灵处理的道具。
func (rs *ResourceServer) tryItemIconForPet(path string) (string, bool) {
	base := filepath.Base(path)
	if !strings.HasSuffix(base, ".swf") {
		return "", false
	}
	idStr := strings.TrimSuffix(base, ".swf")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return "", false
	}
	// 只处理明显是「道具ID」的范围，避免误伤真正的组队战精灵（通常是三位数 ID）
	if id < 300000 && !(id >= 500001 && id <= 500014) && id != 400501 && id != 400505 {
		return "", false
	}

	var iconPath string
	if id >= 300000 && id <= 399999 {
		// 精灵道具：使用精灵道具图标路径
		iconPath = filepath.Join(rs.config.ResDir, "resource", "item", "petItem", "icon", idStr+".swf")
	} else {
		// 元素/扭蛋牌等老道具：仍然使用 fitment/icon
		iconPath = filepath.Join(rs.config.ResDir, "resource", "fitment", "icon", idStr+".swf")
	}

	if _, err := os.Stat(iconPath); err == nil {
		return iconPath, true
	}
	return "", false
}

// trySoulBeadIconByPetClass 元神珠图标请求为 PetClass(如 118) 时，映射到物品 ID(1000001-1000022) 的 swf 路径
// 实际文件在 gameres/root/resource/soulBead/icon/ 下按物品 ID 命名：1000001.swf～1000022.swf
func (rs *ResourceServer) trySoulBeadIconByPetClass(path, resolvedPath string) string {
	base := filepath.Base(path)
	if !strings.HasSuffix(base, ".swf") {
		return ""
	}
	idStr := strings.TrimSuffix(base, ".swf")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return ""
	}
	// 已是物品 ID 范围则无需映射（文件不存在时上面已 404）
	if id >= 1000001 && id <= 1000022 {
		return ""
	}
	// 视为 PetClass，映射到元神珠物品 ID
	if id <= 0 || id > 99999 {
		return ""
	}
	itemID, ok := handlers.GetSoulPearlItemIDByPetClass(id)
	if !ok || itemID <= 0 {
		return ""
	}
	iconPath := filepath.Join(rs.config.ResDir, "resource", "soulBead", "icon", strconv.Itoa(itemID)+".swf")
	if _, err := os.Stat(iconPath); err != nil {
		return ""
	}
	return iconPath
}

// New 创建资源服务器实例
func New(config Config) *ResourceServer {
	// 确保资源目录存在
	if err := os.MkdirAll(config.ResDir, 0755); err != nil {
		logger.Error(fmt.Sprintf("创建资源目录失败: %v", err))
	}

	// 确保代理目录存在
	if err := os.MkdirAll(config.ResProxyDir, 0755); err != nil {
		logger.Error(fmt.Sprintf("创建代理目录失败: %v", err))
	}

	return &ResourceServer{
		config: config,
	}
}

// Start 启动资源服务器
func (rs *ResourceServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", rs.handleRequest)

	// 启动主服务器
	addr := fmt.Sprintf(":%d", rs.config.ResPort)
	rs.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	logger.Info(fmt.Sprintf("资源服务器启动在端口 %d", rs.config.ResPort))

	go func() {
		if err := rs.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(fmt.Sprintf("资源服务器启动失败: %v", err))
		}
	}()

	// 尝试启动备用 HTTP 端口（原 80 需管理员权限，可改为 8088 等）
	if rs.config.ResPort80 > 0 {
		rs.server80 = &http.Server{
			Addr:    fmt.Sprintf(":%d", rs.config.ResPort80),
			Handler: mux,
		}

		go func() {
			if err := rs.server80.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Warning(fmt.Sprintf("备用资源端口 %d 启动失败: %v", rs.config.ResPort80, err))
			}
		}()
	}

	return nil
}

// Stop 停止资源服务器
func (rs *ResourceServer) Stop() error {
	if rs.server != nil {
		if err := rs.server.Close(); err != nil {
			return err
		}
	}

	if rs.server80 != nil {
		if err := rs.server80.Close(); err != nil {
			return err
		}
	}

	return nil
}

// handleRequest 处理HTTP请求
func (rs *ResourceServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	// 设置CORS头
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")

	// 处理OPTIONS请求
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	path := r.URL.Path

	// 过滤不需要的请求
	if strings.HasSuffix(path, "favicon.ico") || strings.HasSuffix(path, "logo.png") {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// 处理特殊路径
	switch {
	case path == "/config/ServerR.xml":
		rs.handleServerRXML(w, r)
		return
	case strings.HasPrefix(path, "/api/set_user"):
		rs.handleSetUser(w, r)
		return
	case strings.HasSuffix(path, "/dll/NieoCore.swf"):
		rs.handleNieoCore(w, r)
		return
	case path == "/__log__":
		rs.handleJSLog(w, r)
		return
	case path == "/ip.txt" || path == "/ip":
		rs.handleIPText(w, r)
		return
	}

	// 处理SPA路由
	if rs.isSPARoute(path) {
		path = "/index.html"
	}

	// 超能 NONO 形态资源：按「客户端根据 2003/9019 的 SuperNono 请求 nono_N.swf / action/X_N.swf」逻辑，
	// 不做路径重写，请求什么路径就返回什么文件。游戏服已在 2003、9019、9003 中下发每个玩家的 SuperNono(1-5)，
	// 客户端绘制自己或他人时应解析协议并用形态 N 请求对应 SWF，资源服仅按 URL 原样返回。

	// 赋形完成动画：请求 /resource/pet/swf/1.swf 时改为返回对应赋形奖励精灵的 swf（按 IP 登记或 query rewardPetId）
	if strings.HasPrefix(path, "/resource/pet/swf/") && strings.HasSuffix(path, ".swf") {
		base := filepath.Base(path)
		isPlaceholder := base == "1.swf"
		rewardId := r.URL.Query().Get("rewardPetId")
		if rewardId == "" && isPlaceholder {
			// 客户端请求 1.swf 且未带参数时，按请求 IP 查找 2358 时登记的奖励精灵 ID
			clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
			if clientIP == "" {
				clientIP = r.RemoteAddr
			}
			if petId := soultransformcache.Lookup(clientIP); petId > 0 {
				rewardId = strconv.Itoa(petId)
				logger.Info(fmt.Sprintf("[赋形SWF] 按 IP %s 使用奖励精灵 ID=%d -> %s", clientIP, petId, path))
			}
		}
		if rewardId != "" {
			if id, err := strconv.Atoi(rewardId); err == nil && id > 0 && id < 100000 {
				path = "/resource/pet/swf/" + rewardId
				if !strings.HasSuffix(path, ".swf") {
					path += ".swf"
				}
				if r.URL.Query().Get("rewardPetId") != "" {
					logger.Info(fmt.Sprintf("[赋形SWF] 使用 rewardPetId=%s -> %s", rewardId, path))
				}
			}
		}
	}

	// 针对部分客户端误把「道具ID」当作「精灵ID」去加载
	// /resource/groupFightResource/pet/XXXX.swf 的情况，这里尝试将其
	// 重定向到道具图标资源 /resource/fitment/icon/XXXX.swf，避免 404 与错误的“获得精灵”表现。
	// 针对部分客户端误把「道具ID」当作「精灵ID」去加载
	// /resource/groupFightResource/pet/XXXX.swf 的情况，这里尝试将其
	// 重定向到道具图标资源 /resource/fitment/icon/XXXX.swf，避免 404 与错误的"获得精灵"表现。
	if strings.HasPrefix(path, "/resource/groupFightResource/pet/") {
		if iconPath, ok := rs.tryItemIconForPet(path); ok {
			rs.serveFile(w, r, iconPath, path)
			logger.Info(fmt.Sprintf("[重定向] %s -> 使用道具图标 SWF %s", path, iconPath))
			return
		}
	}

	// 解析代理规则
	filePath, code := rs.resolvePathByProxyRules(path)

	if code == http.StatusNotFound {
		logger.Warning(fmt.Sprintf("[404][PROXY_RULES] %s -> INVISIBLE", path))
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("the file is defined as invisible by proxy_rules"))
		return
	}

	// GET 读取时去掉 _2/_3：petItem/doodle/cloth 图标请求 *_2.swf、*_3.swf 时按 XXXX.swf 读取
	if (strings.Contains(path, "/item/petItem/icon/") || strings.Contains(path, "/item/doodle/icon/") || strings.Contains(path, "/item/cloth/icon/")) &&
		(strings.HasSuffix(path, "_2.swf") || strings.HasSuffix(path, "_3.swf")) {
		path = strings.TrimSuffix(path, "_2.swf")
		path = strings.TrimSuffix(path, "_3.swf") + ".swf"
		filePath = strings.TrimSuffix(filePath, "_2.swf")
		filePath = strings.TrimSuffix(filePath, "_3.swf") + ".swf"
	}

	// 检查文件是否存在
	if _, err := os.Stat(filePath); err == nil {
		// 文件存在，直接提供
		rs.serveFile(w, r, filePath, path)
		return
	}

	// 元神珠图标：客户端可能用 PetClass(如 118) 请求，实际文件按物品 ID(1000001-1000022) 存放在 resource/soulBead/icon/
	if strings.HasPrefix(path, "/resource/soulBead/icon/") && strings.HasSuffix(path, ".swf") {
		if iconPath := rs.trySoulBeadIconByPetClass(path, filePath); iconPath != "" {
			rs.serveFile(w, r, iconPath, path)
			logger.Info(fmt.Sprintf("[元神珠图标] %s -> 使用物品 ID 对应 SWF", path))
			return
		}
	}

	// 对战/稀有精灵 SWF 缺失时用默认精灵回退，避免加载卡在 12%
	if fallbackPath := rs.fightPetFallback(path, filePath); fallbackPath != "" {
		rs.serveFile(w, r, fallbackPath, path)
		logger.Info(fmt.Sprintf("[回退] %s -> 使用默认精灵 SWF", path))
		return
	}

	// 文件不存在且启用了官方资源，从官服下载
	if rs.config.UseOfficialResources {
		rs.fetchFromOfficial(w, r, path)
		return
	}

	// 文件不存在且未启用官方资源，提供默认响应
	if path == "/" || path == "/index.html" {
		// 提供默认的HTML响应
		defaultHTML := `
<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>赛尔号怀旧服</title>
    <style>
        body {
            font-family: Arial, sans-serif;
            background-color: #f0f0f0;
            margin: 0;
            padding: 20px;
        }
        .container {
            max-width: 800px;
            margin: 0 auto;
            background-color: white;
            padding: 40px;
            border-radius: 8px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
        }
        h1 {
            color: #333;
            text-align: center;
        }
        p {
            color: #666;
            line-height: 1.6;
        }
        .server-info {
            background-color: #f8f8f8;
            padding: 20px;
            border-radius: 4px;
            margin: 20px 0;
        }
        .port {
            font-family: monospace;
            background-color: #e8e8e8;
            padding: 2px 6px;
            border-radius: 3px;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>赛尔号怀旧服</h1>
        <p>欢迎来到赛尔号怀旧服！这是一个基于Go语言开发的赛尔号私人服务器。</p>
        
        <div class="server-info">
            <h3>服务器信息</h3>
            <p>游戏服务器: <span class="port">%s:5000</span></p>
            <p>资源服务器: <span class="port">%s:%d</span></p>
            <p>登录IP服务器: <span class="port">%s:32401</span></p>
            <p>登录服务器: <span class="port">%s:1863</span></p>
        </div>
        
        <p><strong>提示：</strong>请确保您的客户端已正确配置，指向上述服务器地址。</p>
        
        <p>© 2026 赛尔号怀旧服</p>
    </div>
</body>
</html>
		`
		ip := rs.config.PublicIP
		if ip == "" {
			ip = "127.0.0.1"
		}
		resPort := rs.config.ResPort
		html := fmt.Sprintf(defaultHTML, ip, ip, resPort, ip, ip)
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
		return
	}

	// 其他文件不存在，返回404
	logger.Warning(fmt.Sprintf("[404] %s (resolved=%s) official=%v", path, filePath, rs.config.UseOfficialResources))
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(fmt.Sprintf("File not found: %s", path)))
}

// handleSetUser 处理 /api/set_user?uid=...，用于在浏览器端按米米号选择当前激活的超能 NONO 等级
// JS 启动页会优先请求该接口设置 Cookie，后续 /resource/nono/super/* 请求即可按 uid 查找超能等级。
func (rs *ResourceServer) handleSetUser(w http.ResponseWriter, r *http.Request) {
	uidStr := r.URL.Query().Get("uid")
	if uidStr == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("missing uid"))
		return
	}

	// 设置 Cookie，供后续资源请求区分同机多开的不同账号
	http.SetCookie(w, &http.Cookie{
		Name:  "seer_uid",
		Value: uidStr,
		Path:  "/",
	})

	if uid, err := strconv.ParseInt(uidStr, 10, 64); err == nil && uid > 0 {
		level := nonoformcache.LookupByUserID(uid)
		if level > 0 {
			logger.Info(fmt.Sprintf("[set_user] 绑定 uid=%d SuperLevel=%d", uid, level))
		} else {
			logger.Info(fmt.Sprintf("[set_user] 绑定 uid=%d，但资源服缓存中尚无超能等级记录", uid))
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleServerRXML 处理ServerR.xml请求
func (rs *ResourceServer) handleServerRXML(w http.ResponseWriter, r *http.Request) {
	if rs.config.LocalServerMode {
		// 本地模式：使用本地配置文件
		configFile := filepath.Join(rs.config.ResProxyDir, "config", "ServerR.xml")

		// 检查文件是否存在
		if _, err := os.Stat(configFile); err == nil {
			// 文件存在，直接使用
			rs.serveFile(w, r, configFile, "/config/ServerR.xml")
			return
		}

		// 文件不存在，生成默认配置
		rs.generateDefaultServerRXML(w, r, configFile)
		return
	}

	// 官服代理模式：从官服获取并修改
	cachedFile := filepath.Join(rs.config.ResProxyDir, "config", "ServerOfficial.xml")

	// 确保config目录存在
	if err := os.MkdirAll(filepath.Dir(cachedFile), 0755); err != nil {
		logger.Error(fmt.Sprintf("创建配置目录失败: %v", err))
	}

	// 检查缓存文件是否存在
	if _, err := os.Stat(cachedFile); err == nil {
		// 缓存文件存在，直接使用
		rs.serveFile(w, r, cachedFile, "/config/ServerR.xml")
		return
	}

	// 从官服获取
	rs.fetchAndModifyServerRXML(w, r, cachedFile)
}

// handleNieoCore 处理NieoCore.swf请求
func (rs *ResourceServer) handleNieoCore(w http.ResponseWriter, r *http.Request) {
	var coreFile string
	if rs.config.LocalServerMode {
		// 本地模式：使用NieoCore.swf
		coreFile = filepath.Join(rs.config.ResDir, "dll", "NieoCore.swf")
	} else {
		// 官服模式：使用NieoCore2.swf
		coreFile = filepath.Join(rs.config.ResDir, "dll", "NieoCore2.swf")
	}

	rs.serveFile(w, r, coreFile, r.URL.Path)
}

// handleJSLog 处理JavaScript日志请求
func (rs *ResourceServer) handleJSLog(w http.ResponseWriter, r *http.Request) {
	logType := r.URL.Query().Get("type")
	logUrl := r.URL.Query().Get("url")

	// 打印JavaScript网络日志
	switch logType {
	case "Fetch":
		logger.Info(fmt.Sprintf("[JS-Fetch] %s", logUrl))
	case "XHR":
		logger.Info(fmt.Sprintf("[JS-XHR] %s", logUrl))
	case "WebSocket":
		logger.Info(fmt.Sprintf("[JS-WebSocket] 连接: %s", logUrl))
	case "WebSocket-Open":
		logger.Info(fmt.Sprintf("[JS-WebSocket] 已连接: %s", logUrl))
	}

	// 返回空响应
	w.WriteHeader(http.StatusNoContent)
}

// handleIPText 处理ip.txt请求
func (rs *ResourceServer) handleIPText(w http.ResponseWriter, r *http.Request) {
	var resp string

	if rs.config.LocalServerMode {
		// 本地模式：返回 TCP 登录服务器地址，客户端用此建连后发 104/105，再根据 105 里的游戏服连「频道服务器」
		ip := rs.config.PublicIP
		if ip == "" {
			ip = "127.0.0.1"
		}
		resp = fmt.Sprintf("%s:1863", ip)
		logger.Info(fmt.Sprintf("[Local Mode] ✓ 返回登录服务器地址(TCP): %s", resp))
	} else if rs.config.PureOfficialMode && rs.config.UseOfficialResources {
		// 完全官服模式：返回官服的ip.txt
		resp = rs.fetchOfficialIPText()
	} else {
		// 官服代理模式：返回本地代理地址
		resp = rs.config.LoginServerAddress
		logger.Info(fmt.Sprintf("[官服代理模式] ✓ 返回本地代理地址: %s", resp))

		// 异步获取官服ip.txt作为参考
		if rs.config.UseOfficialResources {
			go rs.fetchOfficialIPTextForReference()
		}
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(resp))
}

// fightPetFallback 对战精灵 SWF 缺失时返回默认精灵路径，避免卡 12%
// 匹配 /resource/fightResource/pet/swf/XXXX.swf 或 /resource/groupFightResource/pet/XXXX.swf
func (rs *ResourceServer) fightPetFallback(path, _ string) string {
	// 按优先级尝试常见精灵 ID（玩家出战常用 009 等，资源通常存在）
	fallbackIDs := []string{"009", "013", "007", "035", "005"}
	if !strings.HasSuffix(path, ".swf") {
		return ""
	}
	var baseDir string
	if strings.HasPrefix(path, "/resource/fightResource/pet/swf/") {
		baseDir = filepath.Join(rs.config.ResDir, "resource", "fightResource", "pet", "swf")
	} else if strings.HasPrefix(path, "/resource/groupFightResource/pet/") {
		baseDir = filepath.Join(rs.config.ResDir, "resource", "groupFightResource", "pet")
	} else {
		return ""
	}
	for _, id := range fallbackIDs {
		p := filepath.Join(baseDir, id+".swf")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// resolvePathByProxyRules 解析代理规则
func (rs *ResourceServer) resolvePathByProxyRules(path string) (string, int) {
	// 代理规则
	proxyRules := map[string]interface{}{
		"/":                                 "/index.html",
		"/index.html":                       "PROXY",
		"/config/ServerR.xml":               "DYNAMIC_SERVER_CONFIG",
		"/config/ServerOfficial.xml":        "PROXY",
		"/config/ServerLocal.xml":           "PROXY",
		"/config/doorConfig.xml":            "PROXY",
		"/crossdomain.xml":                  "PROXY",
		"/js/swfobject.js":                  "PROXY",
		"/js/server-config.js":              "PROXY",
		"/js/client-emulator.js":            "PROXY",
		"/resource/login/Advertisement.swf": "INVISIBLE",
	}

	if rule, exists := proxyRules[path]; exists {
		switch v := rule.(type) {
		case string:
			if v == "PROXY" {
				return filepath.Join(rs.config.ResProxyDir, path), http.StatusOK
			} else if v == "INVISIBLE" {
				return "", http.StatusNotFound
			} else if v == "DYNAMIC_SERVER_CONFIG" {
				return "", http.StatusOK
			} else {
				// 递归解析
				return rs.resolvePathByProxyRules(v)
			}
		}
	}

	// 默认规则
	return filepath.Join(rs.config.ResDir, path), http.StatusOK
}

// isSPARoute 判断是否为SPA路由
func (rs *ResourceServer) isSPARoute(path string) bool {
	spaRoutes := map[string]bool{
		"/game": true,
	}
	return spaRoutes[path]
}

// serveFile 提供文件
func (rs *ResourceServer) serveFile(w http.ResponseWriter, r *http.Request, filePath, originalPath string) {
	// 检查文件是否存在
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(fmt.Sprintf("File not found: %s", originalPath)))
		return
	}

	// 检查是否为文件
	if !fileInfo.Mode().IsRegular() {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(fmt.Sprintf("Requested url is not a file: %s", originalPath)))
		return
	}

	// 获取文件类型
	contentType := rs.getContentType(filePath)

	// 设置响应头
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))

	// 记录日志
	logger.Info(fmt.Sprintf("[GET] %s %d bytes", originalPath, fileInfo.Size()))

	// 如果是SWF文件，在控制台显示
	if strings.HasSuffix(strings.ToLower(originalPath), ".swf") {
		logger.Info(fmt.Sprintf("[SWF加载] %s (%d bytes)", originalPath, fileInfo.Size()))
	}

	// 提供文件
	file, err := os.Open(filePath)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("Error opening file: %v", err)))
		return
	}
	defer file.Close()

	io.Copy(w, file)
}

// getContentType 获取文件类型
func (rs *ResourceServer) getContentType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == "" {
		return "application/octet-stream"
	}

	// 移除点号
	ext = ext[1:]

	// MIME类型映射
	mimes := map[string]string{
		"html": "text/html",
		"css":  "text/css",
		"js":   "application/javascript",
		"xml":  "application/xml",
		"swf":  "application/x-shockwave-flash",
		"png":  "image/png",
		"jpg":  "image/jpeg",
		"jpeg": "image/jpeg",
		"gif":  "image/gif",
		"txt":  "text/plain",
	}

	if contentType, exists := mimes[ext]; exists {
		return contentType
	}

	return "application/octet-stream"
}

// fetchFromOfficial 从官服获取文件
func (rs *ResourceServer) fetchFromOfficial(w http.ResponseWriter, r *http.Request, path string) {
	officialURL := rs.config.ResOfficialAddress + path
	logger.Info(fmt.Sprintf("[官服下载] 开始下载: %s", officialURL))

	// 发送请求到官服
	resp, err := http.Get(officialURL)
	if err != nil {
		logger.Error(fmt.Sprintf("[官服下载] 请求错误: %v", err))
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("Error fetching from official: %v", err)))
		return
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		logger.Error(fmt.Sprintf("[官服下载] 响应错误: status=%d", resp.StatusCode))
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(fmt.Sprintf("File not found on official server: %s", path)))
		return
	}

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error(fmt.Sprintf("[官服下载] 读取错误: %v", err))
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("Error reading response: %v", err)))
		return
	}

	logger.Info(fmt.Sprintf("[官服下载] 响应: status=%d, size=%d bytes", resp.StatusCode, len(body)))

	// 保存文件
	savePath := filepath.Join(rs.config.ResDir, path)
	if err := rs.saveFile(savePath, body); err != nil {
		logger.Error(fmt.Sprintf("[官服下载] 保存错误: %v", err))
	}

	// 提供文件
	w.Header().Set("Content-Type", rs.getContentType(path))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.Write(body)
}

// saveFile 保存文件
func (rs *ResourceServer) saveFile(filePath string, data []byte) error {
	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}

	// 写入文件
	return os.WriteFile(filePath, data, 0644)
}

// fetchOfficialIPText 获取官服的ip.txt
func (rs *ResourceServer) fetchOfficialIPText() string {
	officialURL := rs.config.ResOfficialAddress + "/ip.txt"

	resp, err := http.Get(officialURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		return rs.config.LoginServerAddress
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return rs.config.LoginServerAddress
	}

	// 官服可能返回多个服务器地址，用 | 分隔，取第一个
	respStr := strings.TrimSpace(string(body))
	if parts := strings.Split(respStr, "|"); len(parts) > 0 {
		respStr = parts[0]
	}

	// 保存官服的ip.txt到本地
	ipFilePath := filepath.Join(rs.config.ResDir, "ip.txt.official")
	if err := rs.saveFile(ipFilePath, []byte(respStr)); err != nil {
		logger.Error(fmt.Sprintf("保存官服ip.txt失败: %v", err))
	}

	return respStr
}

// fetchOfficialIPTextForReference 异步获取官服ip.txt作为参考
func (rs *ResourceServer) fetchOfficialIPTextForReference() {
	officialURL := "https://seerlogin.61.com/ip.txt"

	resp, err := http.Get(officialURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		logger.Error(fmt.Sprintf("获取官服ip.txt失败: %v", err))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error(fmt.Sprintf("读取官服ip.txt失败: %v", err))
		return
	}

	respStr := strings.TrimSpace(string(body))
	logger.Info(fmt.Sprintf("[官服代理模式] 官服 ip.txt: %s", respStr))

	// 保存官服的ip.txt到本地（作为参考）
	ipFilePath := filepath.Join(rs.config.ResDir, "ip.txt.official")
	if err := rs.saveFile(ipFilePath, []byte(respStr)); err != nil {
		logger.Error(fmt.Sprintf("保存官服ip.txt失败: %v", err))
	}
}

// generateDefaultServerRXML 生成默认的ServerR.xml配置
func (rs *ResourceServer) generateDefaultServerRXML(w http.ResponseWriter, r *http.Request, configFile string) {
	// 生成默认的ServerR.xml配置
	defaultConfig := `<?xml version="1.0" encoding="utf-8"?>
<Servers>
  <ipConfig>
    <Email ip="%s" port="32401" />
    <DirSer ip="%s" port="32401" />
    <Visitor ip="%s" port="32401" />
    <SubServer ip="%s" port="32401" />
    <RegistSer ip="%s" port="32401" />
  </ipConfig>
  <version>1.0.0.0</version>
  <clientUrl>http://%s:%d/Client.swf</clientUrl>
  <loginUrl>http://%s:%d/login/Login.swf</loginUrl>
  <newsUrl>http://%s:%d/news.html</newsUrl>
  <patchUrl>http://%s:%d/patch</patchUrl>
  <helpUrl>http://%s:%d/help</helpUrl>
  <bugUrl>http://%s:%d/bug</bugUrl>
  <vipUrl>http://%s:%d/vip</vipUrl>
  <payUrl>http://%s:%d/pay</payUrl>
  <eventUrl>http://%s:%d/event</eventUrl>
  <teamUrl>http://%s:%d/team</teamUrl>
  <clubUrl>http://%s:%d/club</clubUrl>
  <minigameUrl>http://%s:%d/minigame</minigameUrl>
  <gamepassUrl>http://%s:%d/gamepass</gamepassUrl>
  <activityUrl>http://%s:%d/activity</activityUrl>
  <serverList>
    <server id="1" name="服务器1" ip="%s" port="5000" status="1" />
  </serverList>
</Servers>`

	// 替换配置参数
	ip := rs.config.PublicIP
	if ip == "" {
		ip = "127.0.0.1"
	}
	resPort := rs.config.ResPort
	modifiedData := fmt.Sprintf(
		defaultConfig,
		ip, ip, ip, ip, ip,
		ip, resPort,
		ip, resPort,
		ip, resPort,
		ip, resPort,
		ip, resPort,
		ip, resPort,
		ip, resPort,
		ip, resPort,
		ip, resPort,
		ip, resPort,
		ip,
	)

	// 保存到本地
	if err := rs.saveFile(configFile, []byte(modifiedData)); err != nil {
		logger.Error(fmt.Sprintf("保存默认ServerR.xml失败: %v", err))
	} else {
		logger.Info(fmt.Sprintf("[CONFIG] ✓ 生成默认配置到 %s", configFile))
	}

	// 提供生成的文件
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(modifiedData)))
	w.Write([]byte(modifiedData))
}

// fetchAndModifyServerRXML 从官服获取并修改ServerR.xml
func (rs *ResourceServer) fetchAndModifyServerRXML(w http.ResponseWriter, r *http.Request, cachedFile string) {
	officialURL := rs.config.ResOfficialAddress + "/config/ServerR.xml"

	resp, err := http.Get(officialURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		logger.Error(fmt.Sprintf("获取官服ServerR.xml失败: %v", err))
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("Failed to fetch ServerR.xml: %v", err)))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error(fmt.Sprintf("读取官服ServerR.xml失败: %v", err))
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("Failed to read ServerR.xml: %v", err)))
		return
	}

	// 修改ipConfig，让所有Socket连接走本地代理
	modifiedData := string(body)
	proxyPort := fmt.Sprintf("%d", rs.config.LoginPort)
	ip := rs.config.PublicIP
	if ip == "" {
		ip = "127.0.0.1"
	}

	// 替换IP地址
	modifiedData = strings.ReplaceAll(modifiedData, "<Email ip=\"", "<Email ip=\""+ip+"\"")
	modifiedData = strings.ReplaceAll(modifiedData, "<DirSer ip=\"", "<DirSer ip=\""+ip+"\"")
	modifiedData = strings.ReplaceAll(modifiedData, "<Visitor ip=\"", "<Visitor ip=\""+ip+"\"")
	modifiedData = strings.ReplaceAll(modifiedData, "<SubServer ip=\"", "<SubServer ip=\""+ip+"\"")
	modifiedData = strings.ReplaceAll(modifiedData, "<RegistSer ip=\"", "<RegistSer ip=\""+ip+"\"")

	// 替换端口
	modifiedData = strings.ReplaceAll(modifiedData, "<Email port=\"", fmt.Sprintf("<Email port=\"%s\"", proxyPort))
	modifiedData = strings.ReplaceAll(modifiedData, "<DirSer port=\"", fmt.Sprintf("<DirSer port=\"%s\"", proxyPort))
	modifiedData = strings.ReplaceAll(modifiedData, "<Visitor port=\"", fmt.Sprintf("<Visitor port=\"%s\"", proxyPort))
	modifiedData = strings.ReplaceAll(modifiedData, "<SubServer port=\"", fmt.Sprintf("<SubServer port=\"%s\"", proxyPort))
	modifiedData = strings.ReplaceAll(modifiedData, "<RegistSer port=\"", fmt.Sprintf("<RegistSer port=\"%s\"", proxyPort))

	logger.Info(fmt.Sprintf("[CONFIG] 已修改 ipConfig -> %s:%s", ip, proxyPort))

	// 保存到本地缓存
	if err := rs.saveFile(cachedFile, []byte(modifiedData)); err != nil {
		logger.Error(fmt.Sprintf("缓存ServerR.xml失败: %v", err))
	} else {
		logger.Info(fmt.Sprintf("[CONFIG] ✓ 已缓存到 %s", cachedFile))
	}

	// 提供修改后的文件
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(modifiedData)))
	w.Write([]byte(modifiedData))
}

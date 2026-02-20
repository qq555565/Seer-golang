package loginip

import (
	"fmt"
	"net/http"

	"github.com/seer-game/golang-version/internal/core/logger"
)

// Config 登录IP服务器配置
type Config struct {
	LoginIPPort         int    `json:"loginip_port"`
	LocalServerMode     bool   `json:"local_server_mode"`
	LoginPort           int    `json:"login_port"`
	OfficialLoginServer string `json:"official_login_server"`
	OfficialLoginPort   int    `json:"official_login_port"`
	// PublicIP 对外暴露给客户端使用的服务器 IP（生成 ip.txt），默认 127.0.0.1
	PublicIP string `json:"public_ip"`
}

// LoginIPServer 登录IP服务器
type LoginIPServer struct {
	config Config
	server *http.Server
}

// New 创建登录IP服务器实例
func New(config Config) *LoginIPServer {
	return &LoginIPServer{
		config: config,
	}
}

// Start 启动登录IP服务器
func (lis *LoginIPServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", lis.handleRequest)

	addr := fmt.Sprintf(":%d", lis.config.LoginIPPort)
	lis.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	logger.Info(fmt.Sprintf("登录IP服务器启动在端口 %d", lis.config.LoginIPPort))

	go func() {
		if err := lis.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(fmt.Sprintf("登录IP服务器启动失败: %v", err))
		}
	}()

	return nil
}

// Stop 停止登录IP服务器
func (lis *LoginIPServer) Stop() error {
	if lis.server != nil {
		return lis.server.Close()
	}
	return nil
}

// handleRequest 处理HTTP请求
func (lis *LoginIPServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	// 打印请求日志
	logger.Info(fmt.Sprintf("收到登录IP服务器请求: %s %s", r.Method, r.URL.Path))

	// 设置CORS头
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")

	// 处理OPTIONS请求
	if r.Method == "OPTIONS" {
		logger.Info("处理OPTIONS请求")
		w.WriteHeader(http.StatusOK)
		return
	}

	// 处理/ip.txt请求
	if r.URL.Path == "/ip.txt" {
		// 根据模式选择登录服务器地址
		resp := lis.getLoginServerAddress()
		var modeStr string
		if lis.config.LocalServerMode {
			modeStr = "[Local Mode]"
		} else {
			modeStr = "[Official Mode]"
		}

		// 设置响应头
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(resp))

		// 打印日志
		if lis.config.LocalServerMode {
			logger.Info(fmt.Sprintf("%s ✓ 返回本地登录服务器地址: %s", modeStr, resp))
		} else {
			officialServer := lis.config.OfficialLoginServer
			if officialServer == "" {
				officialServer = "45.125.46.70"
			}
			officialPort := lis.config.OfficialLoginPort
			if officialPort == 0 {
				officialPort = 12345
			}
			logger.Info(fmt.Sprintf("%s ✓ 返回本地代理地址: %s (转发到官服 %s:%d)",
				modeStr, resp, officialServer, officialPort))
		}
		return
	}

	// 处理其他请求
	logger.Warning(fmt.Sprintf("请求路径不正确: %s", r.URL.Path))
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte("404 Not Found"))
}

// getLoginServerAddress 获取登录服务器地址
func (lis *LoginIPServer) getLoginServerAddress() string {
	ip := lis.config.PublicIP
	if ip == "" {
		ip = "127.0.0.1"
	}
	if lis.config.LocalServerMode {
		// 本地模式：返回本地登录服务器地址
		return fmt.Sprintf("%s:%d", ip, lis.config.LoginPort) // 登录服务器端口
	} else {
		// 官服代理模式：返回本地代理地址
		return fmt.Sprintf("%s:%d", ip, lis.config.LoginPort)
	}
}

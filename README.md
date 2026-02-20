# 赛尔号 Golang 版本

这是赛尔号游戏服务器的Golang重构版本，基于原有的Lua代码结构进行转换。

## 项目架构

### 目录结构
```
golang_version/
├── cmd/
│   ├── gameserver/       # 游戏服务器入口
│   ├── loginserver/      # 登录服务器入口
│   ├── ressrv/           # 资源服务器入口
│   └── loginip/          # 登录IP服务器入口
├── internal/
│   ├── core/             # 核心模块
│   │   ├── userdb/       # 用户数据库
│   │   ├── logger/       # 日志系统
│   │   ├── packet/       # 数据包处理
│   │   └── protocol/     # 协议验证
│   ├── game/             # 游戏逻辑
│   │   ├── pets/         # 精灵系统
│   │   ├── skills/       # 技能系统
│   │   ├── battle/       # 战斗系统
│   │   ├── items/        # 物品系统
│   │   └── commands/     # 命令系统
│   ├── handlers/         # 命令处理器
│   ├── servers/          # 服务器实现
│   │   ├── gameserver/   # 游戏服务器
│   │   ├── loginserver/  # 登录服务器
│   │   └── common/       # 公共服务器代码
│   └── utils/            # 工具函数
├── config/               # 配置文件
├── data/                 # 游戏数据
├── tools/                # 工具脚本
├── go.mod                # Go模块文件
├── go.sum                # 依赖校验文件
└── Makefile              # 构建脚本
```

## 核心模块对应关系

### Lua → Golang 模块映射

| Lua 模块 | Golang 模块 | 说明 |
|---------|------------|------|
| `core/userdb.lua` | `internal/core/userdb/` | 用户数据库 |
| `core/logger.lua` | `internal/core/logger/` | 日志系统 |
| `core/packet_utils.lua` | `internal/core/packet/` | 数据包处理 |
| `core/protocol_validator.lua` | `internal/core/protocol/` | 协议验证 |
| `game/seer_pets.lua` | `internal/game/pets/` | 精灵系统 |
| `game/seer_skills.lua` | `internal/game/skills/` | 技能系统 |
| `game/seer_battle.lua` | `internal/game/battle/` | 战斗系统 |
| `game/seer_items.lua` | `internal/game/items/` | 物品系统 |
| `servers/gameserver/` | `internal/servers/gameserver/` | 游戏服务器 |
| `servers/loginserver/` | `internal/servers/loginserver/` | 登录服务器 |
| `handlers/` | `internal/handlers/` | 命令处理器 |

## 技术栈

- **Go版本**: 1.20+
- **网络**: 标准库 `net` 包
- **数据库**: MySQL (通过 `gorm`)
- **配置管理**: `viper`
- **日志**: `zap`
- **依赖管理**: Go Modules

## 启动方式

### 使用启动脚本 (Windows)

```bash
# 启动游戏服务器
./start.bat
```

### 开发环境

```bash
# 启动游戏服务器
make run-gameserver

# 启动登录服务器
make run-loginserver

# 启动资源服务器
make run-ressrv

# 启动登录IP服务器
make run-loginip
```

### 生产环境

```bash
# 构建所有服务器
make build

# 运行构建后的服务器
./output/gameserver
./output/loginserver
./output/ressrv
./output/loginip
```

### 环境变量

| 变量名 | 描述 | 默认值 |
|-------|------|-------|
| `GAME_SERVER_PORT` | 游戏服务器端口 | 5000 |
| `LOG_LEVEL` | 日志级别 | info |
| `LOCAL_SERVER_MODE` | 是否本地服务器模式 | true |
| `USE_MYSQL` | 是否使用MySQL | false |

## 配置文件

配置文件位于 `config/` 目录，支持JSON和YAML格式。

## 数据库

使用MySQL数据库，表结构如下：
- `users`: 用户账号信息
- `game_data`: 游戏数据
- `pets`: 精灵数据
- `items`: 物品数据

## 测试与验证

### 客户端进频道流程（防「连不上频道服务器」）

1. 游戏从 **资源服** 取 `ip.txt`（如 `http://127.0.0.1:32400/ip.txt`）→ 应得到 **127.0.0.1:1863**（TCP 登录服）。
2. 客户端连接 **127.0.0.1:1863**，发 **CMD 105** 要推荐服务器列表。
3. 登录服返回的列表里包含 **127.0.0.1:5000**（游戏服/频道服务器）。
4. 客户端再连 **127.0.0.1:5000**，发 **CMD 1001** 进入游戏。

若 `ip.txt` 仍为 32401 或 CMD 105 格式不对，会导致「此次连接错误，请稍后再试」。

### 单元 / 联调测试

```bash
# CMD 105 服务器列表（登录服返回 127.0.0.1:5000）
go test -v ./internal/server/loginserver -run TestCMD105

# ip.txt 本地模式返回 127.0.0.1:1863
go test -v ./internal/server/resserver -run TestHandleIPText
```

### 完整流程测试（需先启动 gameserver）

```bash
# 1) 编译并启动（或直接运行 start.bat）
go build -o gameserver.exe ./cmd/gameserver
./gameserver.exe   # 或 start.bat

# 2) 新开终端执行
go run ./scripts/test_full_flow.go
```

脚本会：请求 `http://127.0.0.1:32400/ip.txt`，校验得到 `127.0.0.1:1863`；再连 1863 发 CMD 105，校验返回体里含 `127.0.0.1:5000`。

## 注意事项

1. 本项目基于原Lua代码结构进行转换，保持了核心功能的一致性
2. 使用Golang的并发特性优化了服务器性能
3. 支持热重载和配置管理
4. 提供了完整的日志和监控系统
5. **ip.txt** 在本地模式下应返回 **127.0.0.1:1863**；登录服需正确实现 **CMD 105/106**（30 字节 ServerInfo，port 2 字节），客户端才能连上「频道服务器」5000
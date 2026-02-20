# 赛尔号 GM 管理面板使用说明

## 功能介绍

GM管理面板提供了完整的用户账户管理功能，包括：

### 1. 用户管理
- 查看所有用户列表
- 编辑用户信息（昵称、金币、金豆、体力）
- 查看用户详细数据

### 2. 黑名单管理
- 查看用户黑名单
- 添加用户到黑名单
- 从黑名单移除用户

### 3. 物品管理
- 为用户添加任意物品
- 支持设置物品数量

### 4. 精灵管理
- 为用户添加任意精灵
- 设置精灵等级（1-100）
- 自动设置满个体值（DV=31）
- **修改精灵技能**：指定 catchTime，设置 4 个技能槽的技能 ID（0 表示该槽用默认技能）
- **删除用户精灵**：按 catchTime 删除背包或仓库中的精灵

### 5. 账号操作
- **查找账号**：用户列表支持按用户ID、昵称、邮箱模糊搜索（输入框 + 搜索按钮）
- **新增账号**：邮箱 + 密码 + 昵称（可选），创建后自动生成游戏数据
- **删除角色账号**：在用户详情页点击「删除该账号」，会删除账号与游戏数据并落盘（不可恢复）

## 启动方式

1. 启动游戏服务器：
```bash
cd golang_version
go run ./cmd/gameserver
```

2. 服务器启动后会显示：
```
GM管理面板启动在端口 8080
访问 http://localhost:8080/gm_panel.html 打开管理面板
```

3. 在浏览器中打开：
```
http://localhost:8080/gm_panel.html
```

## API 接口说明

### 获取用户列表（支持查找账号）
```
GET /gm/users
GET /gm/users?keyword=xxx   # 按 用户ID/昵称/邮箱 模糊匹配
```

### 获取用户详情
```
GET /gm/user/?userId=100000005
```

### 更新用户信息
```
POST /gm/user/update
Content-Type: application/json

{
  "userId": 100000005,
  "nickname": "新昵称",
  "coins": 99999,
  "gold": 9999,
  "energy": 100
}
```

### 添加黑名单
```
POST /gm/blacklist/add
Content-Type: application/json

{
  "userId": 100000005,
  "blockedId": 100000001,
  "blockedNick": "被屏蔽的用户"
}
```

### 移除黑名单
```
POST /gm/blacklist/remove
Content-Type: application/json

{
  "userId": 100000005,
  "blockedId": 100000001
}
```

### 添加物品
```
POST /gm/item/add
Content-Type: application/json

{
  "userId": 100000005,
  "itemId": 300001,
  "count": 10
}
```

### 添加精灵
```
POST /gm/pet/add
Content-Type: application/json

{
  "userId": 100000005,
  "petId": 166,
  "level": 80
}
```

### 修改精灵技能
```
POST /gm/pet/skills
Content-Type: application/json

{
  "userId": 100000005,
  "catchTime": 1769914265,
  "skills": [21069, 12081, 21070, 12079]
}
```
skills 为最多 4 个技能 ID；0 或无效 ID 表示该槽使用精灵默认技能。

### 删除用户精灵
```
POST /gm/pet/delete
Content-Type: application/json

{
  "userId": 100000005,
  "catchTime": 1769914265,
  "fromStorage": false
}
```
fromStorage: true 表示从仓库删除，false 表示从背包删除。

### 删除角色账号
```
POST /gm/user/delete
Content-Type: application/json

{
  "userId": 100000005
}
```
会删除账号与游戏数据并保存到磁盘，同时清除内存缓存。

### 新增账号
```
POST /gm/user/create
Content-Type: application/json

{
  "email": "new@example.com",
  "password": "123456",
  "nickname": "可选昵称"
}
```
返回 success、message、userId、email。

## 常用物品ID

- 300001: 精灵胶囊
- 300011: 体力药剂
- 300025: 中级体力药剂
- 300035: 高级体力药剂
- 300650: 超级体力药剂
- 400501: 特殊道具

## 常用精灵ID

- 1: 布布种子
- 4: 伊优
- 7: 小火猴
- 10: 皮皮
- 166: 闪光波克尔
- 4150: 拂晓兔

## MySQL 接入（用户数据按分类存储）

在 `cmd/gameserver/main.go` 中默认 `UseMySQL: true`，用户数据写入 MySQL，便于 GM 与游戏服共用、按分类查询。

- **默认连接信息**：地址 `127.0.0.1`，端口 `3306`，数据库名 `seer`，用户 `seer`，密码 `abc.123`
- **环境变量覆盖**（可选）：`MYSQL_HOST`、`MYSQL_PORT`、`MYSQL_DATABASE`、`MYSQL_USER`、`MYSQL_PASSWORD`，未设置时使用上述默认值
- **表分类**：
  - **accounts**：账号（id=用户ID、email、password、nickname、color、register_time、role_created、session 等）
  - **game_players**：玩家主数据（user_id、nick、coins、gold、last_online、data_json 完整存档、updated_at）
  - **game_pets**：精灵表（user_id、slot_index、monster_id、catch_time、level、dv、nature、skills_json、trait 等）
  - **game_items**：物品表（user_id、item_id、count、expire_time）
- 首次启动时会自动建表；GM 面板仍通过游戏服内存读写，数据与 MySQL 同步。
- **原有账号导入**：启用 MySQL 且项目根目录（或 exe 同目录）存在 `users.json` 时，**启动时自动将其中账号与玩家数据导入 MySQL**，导入成功后原文件会重命名为 `users.json.imported`，避免重复导入。若需再次导入，可将 `users.json.imported` 改回 `users.json` 后重启服务。

## 注意事项

1. GM面板仅在本地开发环境使用，不要在生产环境暴露
2. 使用文件模式时，修改用户数据后会立即保存到 `users.json`；使用 MySQL 时数据写入上述各表
3. 建议在修改前备份数据（`users.json` 或 MySQL 库）
4. 添加精灵时会自动设置满个体值（DV=31）
5. 所有操作都会在服务器日志中记录

## 安全建议

- 仅在本地网络使用
- 不要将8080端口暴露到公网
- 定期备份用户数据
- 生产环境应添加身份验证

## 故障排除

### 无法访问GM面板
- 检查服务器是否正常启动
- 确认8080端口未被占用
- 检查防火墙设置

### API请求失败
- 检查浏览器控制台错误信息
- 确认服务器日志中的错误
- 验证请求参数格式是否正确

### 数据未更新
- 刷新页面重新加载数据
- 检查 `users.json` 文件是否有写入权限
- 查看服务器日志确认操作是否成功

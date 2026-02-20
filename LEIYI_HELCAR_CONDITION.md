# 赫尔卡星雷伊 出场条件与触发说明

> 后端以 **Go** 为准；Lua 后端已弃用，不再维护雷伊相关逻辑。

## 1. 出场条件（设定来源）

- **地图**：赫尔卡星荒地，地图 ID = **32**
- **精灵**：雷伊，PetID = **70**
- **条件**：**雷雨天** 才会出现  
  - 出处：SPT 配置 `PioneerTaskModel` 中 `fightCondition="雷雨天"`  
  - PetBook：雷伊是赫尔卡星的神秘精灵，**只有当雷雨天才会出现**

## 2. 项目中对应位置

| 位置 | 说明 |
|------|------|
| `internal/game/sptboss/sptboss.go` | SPT BOSS 配置：地图 32 param2=0 → 雷伊 Lv70；SPT id=6 |
| `internal/game/mapogres/mapogres.go` | 地图 32 野怪配置为「雷伊」；**雷雨天判定** `IsLeiyiWeather()`，仅雷雨天返回雷伊槽位 |
| `internal/handlers/handlers.go` | CMD **2004** 地图怪物列表、**2408** 野怪战斗，均通过 `mapogres.GetSlots(32)` 获取槽位 |
| 资源 `17_...PioneerTaskModel*.bin` / xml | `spt id="6"` 雷伊，`seatID="32"` `enterID="32"`，`fightCondition="雷雨天"` |
| 前端 `MapProcess_32.as` | 赫尔卡星荒地地图脚本，`FightInviteManager.fightWithBoss("雷伊")` 触发 BOSS 战 |

## 3. 触发条件（Go 后端实现）

1. **野怪遭遇（2004 / 2408）**  
   - 玩家在 **地图 32** 时，服务端调用 `mapogres.GetSlots(32)`。  
   - **仅当 `IsLeiyiWeather() == true`** 时返回雷伊槽位，否则返回空（地图上不显示雷伊、无法通过野怪遭遇雷伊）。

2. **雷雨天判定**（`mapogres.IsLeiyiWeather()`）  
   - 用**服务器当前时间**模拟：**每小时的 20～40 分钟**为雷雨天，其余时间为非雷雨天。  
   - 例如：整点+20 分～整点+39 分可遇雷伊，其余时间不会在地图 32 刷出雷伊。

3. **SPT BOSS 战**  
   - 客户端在地图 32 通过 `fightWithBoss("雷伊")` 发起挑战时，走 2411/2408 等 BOSS 流程，由 `sptboss.GetByMapAndParam(32, 0)` 提供等级等，**与天气无关**（即 SPT 面板/直接点 BOSS 可随时挑战，但**地图上野怪雷伊**仍受雷雨天限制）。

4. **雷伊出场动画**  
   - 前端 `BossCmdListener` 监听 MAP_BOSS(2021)，收到 id=70 时派发 `LY_OUT`，`MapProcess_32.showLY()` 播放 bossMc 出场动画。  
   - 雷伊无防护罩，故原逻辑不推送 2021。Go 后端在进入地图 32 且雷雨天时，**额外推送 2021 含雷伊(id=70)**，以触发出场动画。

## 5. 修改雷雨天规则

若需调整“雷雨天”的时段或规则，只需修改：

- **`internal/game/mapogres/mapogres.go`** 中的 **`IsLeiyiWeather()`** 函数。

当前实现为：

```go
func IsLeiyiWeather() bool {
	m := time.Now().Minute()
	return m >= 20 && m < 40
}
```

可改为按星期、按小时区间或其它规则，保持该函数为**唯一**雷雨天判定即可。

## 6. 排查：雷伊未出现时检查

1. **地图是否正确**：必须进入 **赫尔卡星荒地**（地图 ID 32），不是 赫尔卡星（30）或 赫尔卡星遗迹（31）。雷伊只在地图 32 出现。
2. **时间是否在雷雨天**：每小时的 20～40 分钟为雷雨天。例如 10:20～10:39 可遇雷伊，其余时间不会出现。
3. **服务器时区**：`IsLeiyiWeather()` 使用 `time.Now().Minute()`，即服务器本地时间。若服务器在 UTC，实际“雷雨天”对应的是 UTC 的整点～+19 分钟。
4. **日志**：进入地图 32 时，若为雷雨天会打印 `雷雨天，将刷新雷伊 (当前分钟=xx)`；非雷雨天会打印 `非雷雨天，不刷雷伊 (当前分钟=xx)`。可根据日志确认服务端判定。

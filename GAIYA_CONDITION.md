# 盖亚 出场逻辑说明

## 1. 设定来源（资源）

| 来源 | 内容 |
|------|------|
| **PetBook** | 盖亚 PetID=261，Foundin=**暗影峭壁**，mapID=**419** |
| **SPT PioneerTaskModel** | spt id=**14**，title=盖亚，**seatID=0**，**enterID=0**，**fightCondition=""**（无天气/条件） |
| **AchieveXMLInfo** | 完胜盖亚：以**周五/周六/周日**不同规则战胜（仅成就描述，非服务端强制） |

## 2. 与雷伊的差异

- **雷伊**：地图 32（赫尔卡星荒地）专属，有 **fightCondition="雷雨天"**，需天气判定；有 2004 野怪 + 2021 触发出场动画。
- **盖亚**：地图 **419（暗影峭壁）** 为设定所在地，**无 fightCondition**；**无地图野怪刷新**（不在 mapogres），**无 2021 出场动画**；仅通过 **SPT 挑战 / 地图内点击 BOSS** 触发战斗（2411）。

## 3. 项目中对应位置

| 位置 | 说明 |
|------|------|
| `internal/game/sptboss/sptboss.go` | sptBossByPetID: 261 → SPT 14、Lv70、无防护罩；**mapBossConfig 需含 419→盖亚** 才能在地图 419 用 (mapID, param2) 解析 |
| `internal/handlers/handlers.go` | 2411 挑战 BOSS：用 `GetByMapAndParam(user.MapID, param2)` 解析；若 419 未配置则走 body 读 bossID；bossHPOverrides: 261→2000 |
| **前端** | MapProcess_419：暗影峭壁脚本，无直接 fightWithBoss("盖亚")；Task748/Task775 有「与盖亚对战」对话；SPT 面板可点盖亚发起 2411 |
| **资源** | 17_...PioneerTaskModel: 盖亚 seatID=0 enterID=0，表示 SPT 不绑定固定入口地图 |

## 4. 触发方式

1. **SPT 面板**：从先锋队任务列表点「盖亚」→ 客户端发 2411，body 仅传 **param2**（通常 0）。若当前 **user.MapID=419**，且后端 **mapBossConfig 有 419→盖亚**，则按 (419, 0) 解析为盖亚；否则服务端会用 body 当 bossID（若客户端有传 261 则能对上）。
2. **地图 419（暗影峭壁）**：若玩家先进入 419，再通过任务/界面点「与盖亚对战」→ 发 2411(param2=0)。**只有在 sptboss 中为 419 配置 盖亚** 时，服务端才会解析为 261，否则会解析失败（或落到 body 当 bossID 的逻辑）。

## 5. Go 后端当前缺口与建议

- **mapBossConfig 无 419**：`GetByMapAndParam(419, 0)` 为 false，在地图 419 发 2411(0) 无法解析为盖亚。
- **建议**：在 `sptboss.mapBossConfig` 中增加 **419: {0: {261, 70, false}}**（暗影峭壁，param2=0 为盖亚，Lv70，无防护罩），这样在暗影峭壁发起挑战时即可正确解析为盖亚。

## 6. 盖亚三地图 + 周几 + 精元条件（已实现）

盖亚在 **三个地图** 按 **星期几** 出现，对应不同 **挑战条件**；满足条件击败才可获得 **盖亚的精元（400126）**。

| 星期 | 地图 | 地图ID | 挑战条件 |
|------|------|--------|----------|
| 周一、周五 | 火山星 | 15 | 两回合内击败 |
| 周二、周四、周日 | 露西欧星 | 54 | 致命一击击败 |
| 周三、周六 | 双子阿尔法星 | 105 | 十回合后击败 |

- **sptboss**：`mapBossConfig` 已配置 **15、54、105、419** 均可解析为盖亚（261）。
- **2421（FIGHT_SPECIAL_PET）**：与 2411 共用 `handleChallengeBoss`，客户端在 15/54/105 点盖亚时发 2421。
- **BattleState**：记录 `BattleMapID`、`RoundCount`、`LastHitWasCrit`，用于战斗结束时判定。
- **战斗结束**：若敌方为 261 且玩家获胜，按 **当日星期 → 要求地图** 与 **条件（回合数/致命一击）** 判定；满足则发放 400126 并推送 **2202（COMPLETE_TASK）** taskID=99；不满足则推送 **8010（SPRINT_GIFT_NOTICE）**，前端显示「虽然你赢了，但是你没有按照规则战胜我」。

## 7. 小结

| 项目 | 盖亚 |
|------|------|
| 地图 | 419 暗影峭壁；15 火山星、54 露西欧星、105 双子阿尔法星（按周几出现，见上表） |
| 天气/条件 | 无（fightCondition 为空） |
| 野怪 2004 | 无 |
| 2021 出场动画 | 无 |
| 战斗触发 | 2411/2421：SPT 或地图内点盖亚，mapBossConfig 含对应 mapID→261 |
| 精元 400126 | 仅在三地图且满足“当日地图 + 对应条件”时发放 |

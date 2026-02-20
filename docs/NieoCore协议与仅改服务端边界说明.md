# NieoCore 协议结论与「仅改服务端」的边界说明

基于前端解包协议（NieoCore）对 2112、2003、9019 的解析与处理逻辑，说明服务端已做对齐与「他人看见 NONO 飞行」仍需 Flash 修改的边界。

---

## 一、协议结论（NieoCore 解包）

### 2112（飞行开关）

- **Body**：`userId(4) + flyMode(4)`。
- **客户端**：仅 `NonoManager.onFlyHandler` 处理；原版在「自己点击飞行」时**临时**注册 2112，观察者从未注册，故**服务端广播的 2112 被右侧忽略**。
- **结论**：要让他人视角处理广播 2112，必须在 Flash 端为 2112 做**常驻监听**（见 `NonoManager_改后全文.txt`）。

### 2003（地图玩家列表）

- **客户端**：`UserListCmdListener` 用 `UserInfo.setForPeoleInfo` 解析，对他人已按 `actionType` 设 `walk = FlyAction`，但**没有**对他人 dispatch `FLY_MODE` 或调用 `showNono(..., actionType)`；他人 NONO 主要由后续 9019 触发显示。
- **结论**：服务端已在 2003 中下发 `actionType`；要让「他人进图/刷新后立刻看到飞行」，需在 2003 处理里对 `actionType != 0` 的玩家**补发** `UserManager.dispatchAction(userId, PeopleActionEvent.FLY_MODE, { actionType })`（见 `2003处理补发FLY_MODE_他人看见飞行.txt`）。

### 9019（NONO 跟随/回家）

- **Body 解析顺序**（FollowCmdListener）：`userId(4), superStage(4), state(4), nick(16), color(4), power(4)`。  
  - `superStage` 对应超能 NONO 形态 1–5（客户端加载 `nono_N.swf`）。
- **客户端**：对他人只派发 NONO_FOLLOW 并执行 `showNono(_loc3_)`（单参，无 actionType），因此仅靠 9019 他人只会显示**站立** NONO，飞行需由 2112 或 2003 的 FLY_MODE 触发。
- **结论**：服务端 9019 的 body 布局必须与上述顺序一致，否则客户端会错解析形态/状态。

---

## 二、服务端已做对齐（Go）

1. **2112**：已按协议广播飞行状态给同图玩家。
2. **2003**：已带 `actionType`，客户端若补发 FLY_MODE 即可用上。
3. **9019**：所有构造 9019 的位置已统一为：
   - `[0:4]` userId  
   - `[4:8]` **SuperNono（superStage）**  
   - `[8:12]` state  
   - `[12:28]` nick  
   - `[28:32]` color  
   - `[32:36]` **0（power）**  
   涉及：`pushOtherPlayersFlyAndNonoToClient`、`handleNonoFollowOrHoom`（响应与广播）、`pushNonoFollowState`。

---

## 三、「仅改 Go」无法彻底解决「他人看见飞行」的原因

- 观察者是否**处理** 2112 由客户端是否注册 2112 监听决定，服务端无法代替。
- 2003 里是否对他人 **dispatch FLY_MODE** 也仅在客户端可做，服务端已提供 actionType。

因此：**他人视角看见 NONO 飞行** 仍需在 Flash 端完成至少其一（或两者）：
1. **2112 常驻监听**（NonoManager）；
2. **2003 处理里对 actionType≠0 补发 FLY_MODE**。

详见：`他人仍看不见飞行_排查与修复.md`、`NonoManager_改后全文.txt`、`2003处理补发FLY_MODE_他人看见飞行.txt`。

# 仅通过后端修复「B 看不见 A 超能 NONO 飞行」的结论

在不修改 AS 前端解包文件的前提下，通过分析解包协议与客户端逻辑，结论如下。

---

## 一、结论：仅靠后端无法实现

**仅通过后端无法让 B 玩家看见 A 玩家的超能 NONO 飞行状态。** 原因在于：客户端对 2003、2112 的**使用方式**决定了他人是否显示飞行，服务端无法代替客户端做这些逻辑。

---

## 二、解包分析依据

### 1. CMD 2003（地图玩家列表）

- **服务端**：`buildPeopleInfo` 已按与 `UserInfo.setForPeoleInfo` 一致的顺序写入数据，其中包含 **actionType**（即 FlyMode），位置与客户端读取一致。
- **客户端**：
  - `UserListCmdListener.onUserList` 用 `UserInfo.setForPeoleInfo(_loc2_, _loc4_)` 解析每条玩家，故 **actionType 已被正确解析到 `_loc2_.actionType`**。
  - 但对「他人」创建 `PeopleModel` 后，代码**固定**写死：`_loc3_.walk = new WalkAction()`，**从未根据 `_loc2_.actionType` 为他人设置 `FlyAction`**。
- **结论**：2003 里已带 actionType，但**未改 AS 时客户端不会用该字段为他人切飞行**，后端无法改变这段逻辑。

### 2. CMD 2112（飞行开关）

- **服务端**：已正确广播 2112（body: userId(4) + flyMode(4)），并在 A 切换飞行时先发 2003 再对同图其他玩家发 2112；B 进图时也会通过 `pushOtherPlayersFlyAndNonoToClient` 补发已在飞玩家的 2112。
- **客户端**：
  - 当前解包中 **没有任何地方** 对 2112 做 `addCmdListener(CommandID.ON_OR_OFF_FLYING, ...)` 的**常驻**注册。
  - `NonoManager.onFlyHandler` 仅处理「包体中的 userId == 自己」的情况；若收到的是「他人」的 2112，既可能因未注册而收不到，即使收到也不会更新他人模型。
- **结论**：2112 的广播与补发后端已做对，但**客户端未对「他人」的 2112 做监听与处理**，仅改后端无法生效。

### 3. CMD 9019（NONO 跟随/回家）

- 用于同步 NONO 跟随状态与形态；**不携带**飞行状态（飞行由 2003 的 actionType / 2112 的 flyMode 表示）。
- 后端 9019 格式已与客户端解析一致；仅靠 9019 无法让 B 看到 A 的「飞行形态」，只能看到跟随/形态等。

---

## 三、后端已做且无需再改的部分

| 项目 | 状态 |
|------|------|
| 2003 包体中含 actionType（FlyMode） | 已与 setForPeoleInfo 对齐 |
| 2112 广播给同图其他玩家（body: uid(4)+flyMode(4)） | 已实现 |
| A 切换飞行时先广播 2003 再发 2112 | 已实现 |
| B 进图时补发同图已在飞玩家的 2112（及 9019） | 已实现（pushOtherPlayersFlyAndNonoToClient） |
| 9019 包体顺序（userId, superStage, state, nick, color, power） | 已与 FollowCmdListener 一致 |

因此：**在不改 AS 的前提下，后端协议与发送逻辑已满足「若客户端支持，即可正确显示」的要求。** 当前瓶颈完全在客户端是否对 2003 的 actionType 与 2112 的他人包做处理。

---

## 四、若希望 B 看见 A 的飞行（必须动前端时）

需在 AS 中至少做其一（或两者都做）：

1. **2112 常驻监听**（如 in NonoManager.getInstance）：收到 2112 时，若包体 uid ≠ 自己，则更新对应用户的 actionType 并刷新该玩家模型为飞行/落地。
2. **2003 处理中根据 actionType 为他人设飞行**：在 `UserListCmdListener.onUserList` 中，对他人若 `_loc2_.actionType != 0`，则 `_loc3_.walk = new FlyAction(_loc3_)`，并视需要 dispatch FLY_MODE / 调用 showNono(..., actionType)。

详见：`他人仍看不见飞行_排查与修复.md`、`NieoCore协议与仅改服务端边界说明.md`。

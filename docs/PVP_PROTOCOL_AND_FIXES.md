# PvP 协议与完整修复说明

本文档描述 RecSeer 本地服的 **PvP（玩家对战）协议** 以及已实施的 **完整修复**，便于排查「对方精灵不显示」「对战不退出」「血条异常」等问题。

---

## 一、PvP 协议概览

### 1.1 命令 ID 对照（客户端 CommandID.as）

| CMD   | 常量名                   | 方向     | 说明 |
|-------|---------------------------|----------|------|
| 2401  | INVITE_TO_FIGHT           | 客户端→服 | 邀请玩家对战 |
| 2403  | HANDLE_FIGHT_INVITE       | 客户端→服 | 接受/拒绝对战邀请 |
| 2404  | READY_TO_FIGHT            | 客户端→服 | 战斗初始化（PvE 用） |
| 2405  | USE_SKILL                 | 客户端→服 | 使用技能 |
| 2501  | NOTE_INVITE_TO_FIGHT      | 服→客户端 | 通知被邀请方：有人邀请对战 |
| 2502  | NOTE_HANDLE_FIGHT_INVITE  | 服→客户端 | 通知邀请方：对方接受/拒绝 |
| 2503  | NOTE_READY_TO_FIGHT      | 服→客户端 | 准备对战（双方精灵/技能列表，用于预加载） |
| 2504  | NOTE_START_FIGHT          | 服→客户端 | 开始对战（我方/对方 FightPetInfo） |
| 2505  | NOTE_USE_SKILL            | 服→客户端 | 本回合攻击结果（两个 AttackValue） |
| 2506  | FIGHT_OVER                | 服→客户端 | 战斗结束 |
| 2508  | NOTE_UPDATE_PROP          | 服→客户端 | 战斗结束属性更新（经验等） |

### 1.2 PvP 流程（时序）

```
邀请方 A                    服务器                      被邀请方 B
   |                          |                             |
   |-- 2401 邀请 B ----------->|                             |
   |                          |--- 2501 通知 B 被邀请 ------>|
   |                          |                             |
   |                          |<-------- 2403 接受对战 ------|
   |<-- 2502 对方已接受 -------|                             |
   |<-- 2503 准备对战 ---------|-------- 2503 准备对战 ------>|
   |<-- 2504 开始对战(我/他) --|-------- 2504 开始对战(我/他)->|
   |   [BattleState 初始化]   |   [BattleState 初始化]      |
   |                          |                             |
   |-- 2405 使用技能 -------->|                             |
   |<-- 2505 回合结果 ---------|-------- 2505 回合结果 ------>|
   |   (可选)                 |                             |
   |                          |<-------- 2405 使用技能 ------|
   |<-- 2505 回合结果 ---------|-------- 2505 回合结果 ------>|
   |                          |                             |
   |  (一方 HP=0)             |                             |
   |<-- 2506 战斗结束 ---------|-------- 2506 战斗结束 ------>|
   |<-- 2508 属性更新(若胜) ---|                             |
   |<-- 2004 地图精灵列表 -----|-------- 2004 地图精灵列表 -->|
```

---

## 二、关键包体格式（与客户端解析一致）

### 2.1 CMD 2503 NOTE_READY_TO_FIGHT（PvP）

- **用途**：双方进入对战前预加载精灵模型、技能资源；客户端用 `userInfoArray` / `petInfoArray` / `petArray` 区分我方/对方并加载资源。
- **包体（BigEndian）**：
  - `userCount`：4 字节，固定 2。
  - 对每个用户（共 2 次）：
    - **FighetUserInfo** 20 字节：`id(4)` + `nickName(16)`（UTF-8，不足补 0）。
    - `petCount`：4 字节，PvP 每方 1 只。
    - **SimplePetInfo（PetInfo 简化）** 72 字节：
      - `id(4)` `level(4)` `hp(4)` `maxHp(4)` `skillNum(4)`  
      - `4 × PetSkillInfo(id 4 + pp 4)`  
      - `catchTime(4)` `catchMap(4)` `catchRect(4)` `catchLevel(4)` **`skinID(4)`**
- **修复要点**：
  - 最后一格 **skinID** 必须写入（例如 `petID`），不能为 0，否则客户端用 0 加载资源会显示蓝格占位、对方精灵不显示。
  - `userCount` 为 4 字节（客户端 `readUnsignedInt()`），不是 2 字节。
  - **PvP 按接收方分别发送 2503**（与 Centens 前端协议一致）：NoteReadyToFightInfo 按包顺序解析，DLL `setup(userInfoArray,...)` 中 **userInfoArray[0]=我方、[1]=对方**（预加载/图标），故邀请方收 `[邀请方, 接受方]`、接受方收 `[接受方, 邀请方]`，各方第一块为己方。
- **PvP 下 2504 按接收方分别发送**：客户端按包内“第一项=我方、第二项=对方”显示模型/图标，故邀请方收 `[邀请方,接受方]`、接受方收 `[接受方,邀请方]`，各方第一项为自己；FightPetInfo 的 battleLv(44:50) 显式写 0，避免解析错位。

### 2.2 CMD 2504 NOTE_START_FIGHT（PvP）

- **用途**：真正「开始对战」时下发我方/对方当前出战的精灵信息；客户端用 `FightStartInfo.myInfo` / `otherInfo` 显示左右两侧精灵与血条。
- **包体**：
  - `isCanAuto`：4 字节，0=不可自动。
  - **FightPetInfo** × 2（顺序与接收方有关）：
    - 发给 **邀请方**：第 1 条=邀请方精灵（我方），第 2 条=接受方精灵（对方）。
    - 发给 **接受方**：第 1 条=接受方精灵（我方），第 2 条=邀请方精灵（对方）。
- **FightPetInfo 固定 50 字节**（与 FightPetInfo.as 一致）：
  - `userID(4)` `petID(4)` `petName(16)` `catchTime(4)` `hp(4)` `maxHP(4)` `lv(4)` `catchable(4)` `battleLv(6)`
  - `hp`/`maxHP` 必须满足 `0 ≤ hp ≤ maxHP`、`maxHP ≥ 1`，且为 uint32，避免客户端显示 -99999、111/100。
  - `battleLv` 6 字节显式写 0，避免解析错位导致不显示或异常。
- **修复要点**：
  - 使用固定 50 字节、按偏移写入，保证与客户端逐字段对齐，避免「对方」条解析错位。
  - 双方各自收到「我方第一条、对方第二条」，客户端用 `MainManager.actorInfo.userID` 与第一条的 `userID` 比较区分 myInfo/otherInfo。

### 2.3 CMD 2505 NOTE_USE_SKILL

- **用途**：每回合攻击结果；客户端用两个 AttackValue 更新「我方」与「对方」血条/状态。
- **包体**：无 count 前缀，连续 **两个 AttackValue**。
- **AttackValue**（与 AttackValue.as 一致）：  
  `userID(4)` `skillID(4)` `atkTimes(4)` `lostHP(4)` `gainHP(4 signed)` `remainHp(4 signed)` `maxHp(4)` `state(4)`  
  `skillListCount(4)` `[PetSkillInfo×N]` `isCrit(4)` `status(20)` `battleLv(6)` `maxShield(4)` `curShield(4)` `petType(4)`  
  - 第一个：攻击方（当前出招玩家）的 userID + 其剩余 HP 等。
  - 第二个：**PvP 时必须为对方 userID**（不能为 0），否则客户端会把「对方」当 NPC 处理，导致对方血条/图标异常（蓝格、-99999、111/100）。
- **修复要点**：
  - PvP 时第二个 AttackValue 的 `userID` 填 `OpponentUserID`（对方 UID）。
  - PvP 时除给攻击方发 2505 外，**同时向对方客户端发同一份 2505**，这样双方都能用 userID 正确更新「我方/对方」血条。

### 2.4 CMD 2506 FIGHT_OVER

- **用途**：战斗结束，客户端退出对战界面。
- **修复要点**：
  - PvP 结束时向**双方**都发送 2506，并清理双方的 `BattleState`；否则未收到 2506 的一方会一直停在战斗界面。

---

## 三、服务端状态与逻辑

### 3.1 BattleState（gameserver.BattleState）

- 每个正在战斗的玩家一条记录：`BattleStates[userID] = *BattleState`。
- 字段：`PlayerHP/MaxHP`、`EnemyHP/EnemyMaxHP`、`EnemyID`、`EnemyLevel`、`IsActive`、**`OpponentUserID`**（PvP 对方 UID，0 表示 PvE）。
- **PvP 接受邀请时**：必须对**双方**都调用 `setPvPBattleStates(inviterUID, responderUID)`，为双方写入正确的 Player/Enemy HP、EnemyID、EnemyLevel、**OpponentUserID**，否则 2405 无法正确结算且无法向对方发 2505/2506。

### 3.2 setPvPBattleStates（handlers）

- 在 2403 处理中，当 `result == 1`（接受对战）时：
  1. 向双方发送**同一份** 2503（准备对战）。
  2. 向邀请方发送 2504（我方=邀请方，对方=接受方），向接受方发送 2504（我方=接受方，对方=邀请方）。
  3. 调用 `setPvPBattleStates(inviterUID, responderUID)` 为双方初始化 BattleState（含 OpponentUserID）。

### 3.3 CMD 2404 READY_TO_FIGHT 在 PvP 下的行为

- 客户端在收到 2503+2504 后仍会发送 2404 确认；若 2404 像 PvE 一样**覆盖** BattleState，会清空 `OpponentUserID`，导致后续 2405 无法向对方发送 2505/2506。
- **修复**：2404 处理时若当前用户已有 BattleState 且 `OpponentUserID != 0`，视为 PvP 已就绪，**仅回 2504（我方/对方顺序）+ 2301**，**不覆盖** BattleState；否则按 PvE 流程写状态并回 2504+2301。

### 3.4 2405 handleUseSkill 中 PvP 相关逻辑

- 取当前玩家 `BattleState`，若存在且 `IsActive` 则进行伤害计算、更新 PlayerHP/EnemyHP。
- **释放锁前**读取 `opponentUID := battle.OpponentUserID`。
- 构建 2505 body：
  - 第一个 AttackValue：`userID = ctx.UserID`，remainHP/maxHP = 当前玩家。
  - 第二个 AttackValue：PvP 时 `userID = opponentUID`，remainHP/maxHP = 对方；PvE 时 `userID = 0`。
- 给当前连接发 2505；若 `opponentUID != 0`，再向对方连接发**同一 body** 的 2505。
- 若战斗结束（任一方 HP=0）：发 2506 给当前玩家；若 PvP 再发 2506 给对方，并 `delete(BattleStates, 双方)`，再推 2004 地图精灵列表。

---

## 四、已实施的完整修复清单

| 问题 | 修复内容 | 位置 |
|------|----------|------|
| 进入对战不显示双方精灵 | 2503 SimplePetInfo 最后 4 字节写入 **skinID=petID**，客户端用其加载模型/图标 | buildNoteReadyToFightInfoPvP → buildSimplePetInfo |
| 对方精灵蓝格、-99999/111/100 | 2504 FightPetInfo 固定 50 字节、hp/maxHP 合法化、battleLv 显式 0；2505 第二个 AttackValue 的 **userID 填对方 UID** | buildNoteStartFightPvP；handleUseSkill → buildAttackValue |
| 对方不显示/不更新血条 | PvP 时向**对方也发 2505**（同一 body），客户端用 userID 匹配「对方」更新 | handleUseSkill 中 opponentUID != 0 时 SendResponse(otherClient, 2505, ...) |
| 对战结束不退出 | PvP 结束时向**双方**发 2506，并清理双方 BattleState | handleUseSkill 战斗结束时 SendResponse(otherClient, 2506, ...) 与 delete(BattleStates) |
| 2405 无状态立即结束 | PvP 接受时 **setPvPBattleStates** 为双方初始化 BattleState（含 OpponentUserID） | handleHandleFightInvite(result==1) → setPvPBattleStates |
| 2503 首字段长度错误 | userCount 使用 4 字节（与客户端 readUnsignedInt 一致） | buildNoteReadyToFightInfoPvP 注释与实现 |
| 2404 覆盖 PvP 状态 | PvP 下 2404 **不覆盖** BattleState，仅回 2504+2301，保留 OpponentUserID | handleReadyToFight 中 PvP 分支 → buildPvP2504BodyForUser |
| PvP 我方显示对方精灵模型 | 2503 客户端/DLL 将 userInfoArray[0]=对方、[1]=我方，故**交换发送**：邀请方收 [接受方,邀请方]、接受方收 [邀请方,接受方] | handleHandleFightInvite 中 2503 发 body2503Responder→邀请方、body2503Inviter→接受方 |

---

## 五、客户端解析要点（便于对照）

- **NoteReadyToFightInfo**：先读 `userCount`（4 字节），再循环 2 次读 FighetUserInfo + petCount + PetInfo(false)×petCount；用 `_loc3_.id == MainManager.actorID` 区分 myPetA/otherPetA；用 `petArray`/`petInfoArray` 预加载资源，**skinID** 来自 PetInfo 最后一格。对战 DLL 将 **userInfoArray[0]** 显示为对方、**userInfoArray[1]** 显示为我方，故服务端 2503 需对调发送（邀请方收 [接受方,邀请方]、接受方收 [邀请方,接受方]）。
- **FightStartInfo**：先读 `isCanAuto`（4），再读两个 FightPetInfo；若第一个的 `userID == MainManager.actorInfo.userID` 则第一个为 myInfo、第二个为 otherInfo，否则相反。
- **UseSkillInfo**：连续读两个 AttackValue；客户端按 **userID** 决定更新哪一侧血条/精灵，PvP 时第二个必须为对方 userID。

---

## 六、排查建议

1. **对方仍蓝格/不显示**：确认 2503 中双方 SimplePetInfo 的 **skinID** 非 0（当前实现为 petID）；确认 2504 中 FightPetInfo 严格 50 字节且 hp/maxHP/lv 合法。
2. **对方血条仍 -99999/111/100**：确认 2505 第二个 AttackValue 的 **userID** 在 PvP 时为对方 UID；确认 2504 中「对方」FightPetInfo 的 hp/maxHP 未写错位。
3. **对战结束不退出**：确认 2506 已向双方发送且双方 BattleState 已删除；确认 **2404 在 PvP 下不覆盖 BattleState**（见 3.3），否则 OpponentUserID 丢失后 2506 不会发给对方。
4. **回合不同步**：确认每次 2405 后双方都会收到 2505（攻击方 + 对方），且 body 一致。
5. **胜者收到两次 2506**：若败方 2405 先到、胜方 2405 后到，胜方会先收到 2506(winnerID=胜者)，再因「状态不存在」收到 2506(reason=0, winnerID=0)。客户端应以首次 2506 为准并退出战斗；第二次仅用于兜底关闭界面。
6. **两边都显示对方精灵模型**：服务端 2504 包内顺序为 [我方,对方]，且两段 FightPetInfo 的 userID/petID 不同。若日志 `[2504-PvP]`、`[2504-2404]` 显示两段 userID/petID 均正确且不同，则问题可能在客户端：FightStartInfo 用 `MainManager.actorInfo.userID` 区分 myInfo/otherInfo，FighterModeFactory.createMode 用 `MainManager.actorID` 区分 PlayerMode/EnemyMode，若两者不一致会导致“我方/对方”槽位错绑；或 DLL 某处错误地用 otherInfo 渲染了两个槽位。可对照日志确认服务端发出的 2504 两段确实不同。

以上为当前 PvP 协议与完整修复说明，若仍有异常可结合日志（如 `[2403] PvP 接受`、`[2404] PvP 已就绪`、`[2504-PvP]`/`[2504-2404]` 两段 userID/petID、`[2405] 使用技能后`、2504/2505 的 LEN）逐包核对。

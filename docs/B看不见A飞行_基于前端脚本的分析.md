# B 看不见 A 飞行 — 基于前端脚本的完整分析

根据项目内 `docs/` 下的前端脚本说明（NonoManager、UserListCmdListener、BasePeoleModel、UserInfo 等改后全文与解包协议），对「B 仍看不见 A 的飞行飞船」做逐链分析，并给出可对照代码的排查清单。

---

## 一、数据流概览（谁发、谁收、谁刷新）

| 步骤 | 服务端 | 客户端 B 端 |
|------|--------|--------------|
| 1 | A 开飞行 → 广播 **2003**（含 A 的 actionType=4） | 收到 2003，解析每条 PeopleInfo → `UserInfo.setForPeoleInfo` → 创建/更新 **PeopleModel**，`MapManager.currentMap.addUser(_loc3_)` |
| 2 | 同图广播 **2112**，body=飞行者 uid(4)+flyMode(4) | **NonoManager.onFlyHandler** 收到 2112，包体前 4 字节=uid、后 4 字节=mode；若 uid≠自己则走「他人」分支 |
| 3 | 若 A 的 NONO 跟随，再发 **9019** 给 B | 9019 触发他人 NONO 跟随；飞行形态还需 2112/2003 的 FLY_MODE 触发 |
| 4 | — | 他人分支：`UserManager.getUser(uid)` 更新 actionType → `getUserModel(uid)` 同步 model.info → **UserManager.dispatchAction(uid, FLY_MODE, {actionType})** |
| 5 | — | **BasePeoleModel** 在构造/加入地图时通过 **addEvent()** 调用 `UserManager.addActionListener(this._info.userID, this.onAction)`，故 dispatchAction(uid, FLY_MODE, …) 会触发**该 uid 对应模型**的 **onAction(FLY_MODE)** |
| 6 | — | **onAction** 里 `case PeopleActionEvent.FLY_MODE`：更新 `_info.actionType`，设置 `walk = new FlyAction(this)`，拼 NonoInfo 后 **showNono(_loc4_, this._info.actionType)**；他人飞行时需用 **NonoFlyModel**（见 showNono 的 param2≠0 分支） |

若任一步在 B 端未执行或未生效，B 就看不到 A 的飞行。

---

## 二、前端脚本关键点与可能断点

### 1. 2112 是否被 B 端收到并进入「他人」分支

- **位置**：`NonoManager.onFlyHandler`（见 `NonoManager_改后全文.txt`）
- **条件**：2112 必须在 B 端有**常驻监听**。若只在「自己点击飞行」时临时注册，B 从未点飞行则不会注册，服务端广播的 2112 会被忽略。
- **改后逻辑**：在 `getInstance()` 里 `SocketConnection.addCmdListener(CommandID.ON_OR_OFF_FLYING, onFlyHandler)`，且只注册一次（`_flyListenerAdded`）。
- **包体解析**：`_loc3_ = _loc2_.readUnsignedInt()`（前 4 字节=uid），`_loc4_ = _loc2_.readUnsignedInt()`（后 4 字节=flyMode）。服务端 2112 body 为 `飞行者 uid(4) + flyMode(4)`，顺序一致。
- **断点**：若 `_loc3_ == MainManager.actorInfo.userID` 用错（例如用了 `MainManager.actorID`），B 端可能误走「自己」分支；需与包体前 4 字节一致（大端 uint）。

**排查**：在 `onFlyHandler` 的 else（他人分支）首行加 `trace("2112 other: uid=", _loc3_, " mode=", _loc4_);`，A 开飞行时 B 控制台应出现该 trace。

---

### 2. B 端能否取到「他人」的 UserInfo 与 Model

- **位置**：`onFlyHandler` 的 else 里 `UserManager.getUser(_loc3_)`、`UserManager.getUserModel(_loc3_)`
- **来源**：2003 处理里会 `UserInfo.setForPeoleInfo(_loc2_, _loc4_)` 并 `MapManager.currentMap.addUser(_loc3_)`。若 2003 用「userId→UserInfo」存到 **UserManager** 的某 map，则 `getUser(uid)` 应能取到；若 key 或 API 不同（如 `getUserByUserId`），这里会为 null。
- **改后逻辑**：有 UserInfo 则 `_loc5_.actionType = _loc4_`；有 BasePeoleModel 则 `_loc6_.info.actionType = _loc4_`；**最后无论如何**都执行 `UserManager.dispatchAction(_loc3_, PeopleActionEvent.FLY_MODE, {actionType:_loc4_})`。
- **断点**：若 `getUser(_loc3_)` 恒为 null，说明 B 端 2003 存「他人」的方式与这里取的方式不一致，需在工程里搜 `getUser`/`addUser`/`setUser`，让 2112 用同一套存储。

**排查**：在 else 里 `var _loc5_:UserInfo = UserManager.getUser(_loc3_);` 后加 `trace("getUser(", _loc3_, ") = ", _loc5_);`，确认非 null。

---

### 3. dispatchAction 是否被「他人」模型收到

- **位置**：`UserManager.dispatchAction(param1:uint, param2:String, param3:Object)` 内部是 `getInstance().dispatchEvent(new NonoActionEvent(param1.toString(), param2, param3))`，即事件 type=uid 字符串（如 "100000002"），param2=FLY_MODE，param3={actionType:4}。
- **收听**：`BasePeoleModel.addEvent()` 中有 `UserManager.addActionListener(this._info.userID, this.onAction)`，即该模型以 **userId** 为 key 注册了 onAction；只有**已创建并已执行 addEvent()** 的模型才会收到。
- **时机**：2003 里先 `new PeopleModel(_loc2_)` 再 `MapManager.currentMap.addUser(_loc3_)`；addEvent() 通常在模型被加入舞台或显示列表时调用。若 2112 先于 2003 到达，或 addUser 尚未触发 addEvent，则此时还没有 listener，dispatchAction 会无效果。
- **断点**：若 B 先收 2112 再收 2003，则 2112 时 A 的模型可能尚未创建/注册；依赖后续 **2003 里对 actionType≠0 补发 FLY_MODE**（见下）。

**排查**：在 BasePeoleModel 的 `case PeopleActionEvent.FLY_MODE` 首行加 trace（如 `trace("FLY_MODE recv uid=", this._info.userID, " actionType=", param1.data.actionType);`），确认 B 端 A 的模型是否收到。

---

### 4. 2003 是否对「他人」补发 FLY_MODE（必做）

- **位置**：处理 CMD 2003 的类（如 `UserListCmdListener_改后全文.txt` 的 `onUserList`）。
- **改后逻辑**：每处理完一条他人（setForPeoleInfo + 创建 PeopleModel + addUser）后，若 `_loc2_.actionType != 0`，执行  
  `UserManager.dispatchAction(_loc2_.userID, PeopleActionEvent.FLY_MODE, {"actionType": _loc2_.actionType});`
- **作用**：进图或刷新时，若 2003 里已带 A 的 actionType=4，在 B 端创建完 A 的模型后立刻触发一次 FLY_MODE，不依赖 2112 是否先到；且 2112 晚到时模型已存在且已注册 listener，能再次刷新。
- **断点**：若 2003 处理里**没有**这段 dispatchAction，则 B 仅靠 2003 不会切到飞行形态；若 2003 只追加新玩家、不更新已有玩家的 actionType，则 A 先进图、B 后进图时，B 的 2003 里应有 A，但若没有对「已有玩家」更新并补发 FLY_MODE，也可能不刷新。

**排查**：确认 2003 处理循环内，在 `addUser(_loc3_)` 之后、且 `_loc2_.actionType != 0` 时，有且仅有一次 `UserManager.dispatchAction(_loc2_.userID, PeopleActionEvent.FLY_MODE, {"actionType": _loc2_.actionType});`（或等价写法）。

---

### 5. BasePeoleModel 的 FLY_MODE 与 showNono（他人用 NonoFlyModel）

- **位置**：`BasePeoleModel_改后全文.txt` 的 `onAction` → `case PeopleActionEvent.FLY_MODE`，以及 `showNono` 对「他人」的分支。
- **FLY_MODE 分支**：`this._info.actionType = param1.data.actionType`；若不为 0 则 `this.walk = new FlyAction(this)`、`dispatchEvent(new RobotEvent(RobotEvent.WALK_START))`；再拼 NonoInfo（他人用 `_info.nonoInfo` 或 `_info.superNono`/`_info.nonoColor`），最后 `this.showNono(_loc4_, this._info.actionType)`。
- **showNono(param1, param2)**：若 `param2 != 0` 且是他人，需走 **NonoFlyModel** 分支（见 `BasePeoleModel_showNono_他人飞行补充.txt`），否则他人只会显示站立 NONO。
- **UserInfo 的 superNono**：他人拼 NonoInfo 时用 `this._info.superNono`；若 **UserInfo.setForPeoleInfo** 没有从 2003 的 NoNo 段读出 SuperNono 并赋给 `param1.superNono`，则 superNono 为 0，可能不拼 _loc4_，导致不调 showNono。服务端 2003 的 NoNo 段顺序为：Flag(4), State(4), Color(4), SuperNono(4), transTime(4)，与 `UserInfo_改后全文.txt` 中 setForPeoleInfo 的 NoNo 段一致。
- **断点**：PeopleActionEvent 中必须有 `FLY_MODE` 常量，且与 dispatch 时字符串完全一致；BasePeoleModel 的 switch 里必须有 `case PeopleActionEvent.FLY_MODE:`；他人飞行时 showNono 必须走 NonoFlyModel。

**排查**：确认 PeopleActionEvent.FLY_MODE 存在；确认 BasePeoleModel 中 FLY_MODE 分支和 showNono 他人飞行用 NonoFlyModel 的改动已合入 B 端实际加载的 SWF。

---

### 6. 服务端 2003/2112 与前端解析一致性（已核对）

- **2003**：`buildPeopleInfo` 中 actionType、NoNo 段（Flag, State, Color, SuperNono, transTime）顺序与 `UserInfo.setForPeoleInfo` 一致；actionType 来自 `user.FlyMode`。
- **2112**：body 为飞行者 uid(4)+flyMode(4)，与 onFlyHandler 中两次 readUnsignedInt() 一致；包头为飞行者 userId，便于客户端识别「谁」的飞行状态。

无需再改服务端协议；若 B 仍看不见，问题在客户端上述 1～5 的某一环。

---

## 三、按「前端脚本文件」的检查清单

| 序号 | 文件/类 | 检查项 |
|------|---------|--------|
| 1 | **NonoManager** | getInstance() 中是否对 2112 做常驻 addCmdListener（只注册一次）；onFlyHandler 中 else 分支是否更新他人 UserInfo/model.info 并 dispatchAction(uid, FLY_MODE, {actionType})；uid 是否用包体前 4 字节、与 MainManager.actorInfo.userID 比较。 |
| 2 | **UserListCmdListener** | 2003 循环内每条他人处理完后，若 actionType≠0 是否执行 UserManager.dispatchAction(userId, PeopleActionEvent.FLY_MODE, { actionType })；创建的是否为 PeopleModel(BasePeoleModel) 且会触发 addEvent。 |
| 3 | **BasePeoleModel** | addEvent() 是否包含 UserManager.addActionListener(this._info.userID, this.onAction)；onAction 是否有 case PeopleActionEvent.FLY_MODE 且含 walk=FlyAction、showNono(..., this._info.actionType)；他人飞行时 showNono 是否用 NonoFlyModel（param2≠0）。 |
| 4 | **PeopleActionEvent** | 是否有 public static var FLY_MODE:String = "flyMode"（与 dispatch 时字符串一致）。 |
| 5 | **UserInfo.setForPeoleInfo** | 2003 解析时是否读 actionType、NoNo 段是否读 superNono 并赋 param1.superNono（1–5）。 |
| 6 | **UserManager** | getUser(uid)/getUserModel(uid) 是否与 2003 里写入「他人」的存储方式一致（同 map/key）；dispatchAction 是否按 uid 派发到对应模型的 listener。 |

---

## 四、建议的 trace 与结论

1. 在 **NonoManager.onFlyHandler** else 首行：`trace("2112 other: uid=", _loc3_, " mode=", _loc4_);`  
   → 无输出：2112 未进他人分支（监听未注册或 uid 判断错）。  
2. 在 else 中 getUser 后：`trace("getUser(", _loc3_, ") = ", _loc5_);`  
   → _loc5_ 为 null：2003 与 2112 用的不是同一套 UserInfo 存储。  
3. 在 **BasePeoleModel** case FLY_MODE 首行：`trace("FLY_MODE uid=", this._info.userID);`  
   → 无输出：dispatchAction 未触达该模型（listener 未注册或 2112/2003 补发未执行）。  

按上述顺序排查并对照「前端脚本」改后全文，可精确定位是 2112 监听、UserInfo/Model 获取、2003 补发 FLY_MODE，还是 BasePeoleModel/showNono 未改导致 B 看不见 A 的飞行。

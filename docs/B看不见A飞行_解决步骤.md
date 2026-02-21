# B 看不见 A 飞行 — 解决步骤

问题：玩家 A 开飞行后，玩家 B（同图另一客户端）看不到 A 的飞行飞船/NONO。

**结论**：服务端已正确发 2003、2112、9019，**必须在 Flash 客户端（B 端也要加载的 SWF）做三处修改**，缺一不可。

---

## 前提

- 用 **FFDec** 打开并修改 `dll/NieoCore.swf`（或你项目里注册 2112/2003 的主逻辑 SWF）。
- **A 和 B 两个窗口都必须加载同一套改过的 SWF**（清缓存或强刷），否则 B 仍是原版，不会生效。

---

## 解决步骤（三处必做）

### 步骤 1：NonoManager — 2112 常驻监听 + 他人分支

**目的**：让 B 端在未点飞行的情况下也能收到服务端广播的 2112，并更新「他人」的飞行状态。

1. 在 FFDec 中打开 NieoCore.swf，找到 **NonoManager** 类。
2. 在 **getInstance()** 里增加**只执行一次**的 2112 注册：
   - 若原逻辑在「自己点击飞行」时才临时 `addCmdListener(2112, ...)`，**删掉**该临时注册；
   - 在 getInstance() 内、return instance 之前加：
     ```text
     if (!_flyListenerAdded) {
        _flyListenerAdded = true;
        SocketConnection.addCmdListener(CommandID.ON_OR_OFF_FLYING, onFlyHandler);
     }
     ```
   - 类里需有静态变量：`private static var _flyListenerAdded:Boolean = false;`
3. 在 **onFlyHandler** 里，用包体前 4 字节读 uid、后 4 字节读 mode；若 `uid != MainManager.actorInfo.userID`，增加 **else** 分支：
   - 若有 `UserManager.getUser(uid)`，则 `info.actionType = mode`；
   - 若有 `UserManager.getUserModel(uid)`（转 BasePeoleModel），则 `model.info.actionType = mode`；
   - **最后无论如何**执行：  
     `UserManager.dispatchAction(uid, PeopleActionEvent.FLY_MODE, { actionType: mode });`

**参考**：`docs/NonoManager_改后全文.txt`

---

### 步骤 2：UserListCmdListener（或处理 CMD 2003 的类）— 补发 FLY_MODE

**目的**：进图/刷新时，若 2003 里已有正在飞行的玩家，立刻触发其模型的飞行表现，不依赖 2112 是否先到。

1. 找到处理 **CMD 2003（LIST_MAP_PLAYER）** 的类（多为 UserListCmdListener 或类似名）。
2. 在**循环处理每条玩家**的逻辑里，每处理完一条（已 `setForPeoleInfo`、已创建 PeopleModel/BasePeoleModel、已 `addUser` 到地图）后，增加：
   ```actionscript
   if (_loc2_.actionType != 0) {
      UserManager.dispatchAction(_loc2_.userID, PeopleActionEvent.FLY_MODE, { actionType: _loc2_.actionType });
   }
   ```
   （变量名按你当前 2003 里的实际名称改，如 `info.actionType`、`info.userID`。）

**参考**：`docs/2003处理补发FLY_MODE_他人看见飞行.txt`、`docs/UserListCmdListener_改后全文.txt`

---

### 步骤 3：BasePeoleModel — FLY_MODE 分支 + 他人用 NonoFlyModel

**目的**：让「他人」的模型在收到 FLY_MODE 时切换为飞行骨骼并显示飞行 NONO（NonoFlyModel）。

1. **PeopleActionEvent** 中增加常量（若没有）：  
   `public static var FLY_MODE:String = "flyMode";`

2. 在 **BasePeoleModel** 的 **onAction** 里，在 `switch(param1.actionType)` 中增加：
   ```text
   case PeopleActionEvent.FLY_MODE:
      this._info.actionType = param1.data.actionType as uint;
      this.hideNono();
      if (this._info.actionType == 0) {
         this.walk = new WalkAction();
         this.clickMc.y = -50;
         new PeculiarAction().standUp(this.skeleton as EmptySkeletonStrategy);
      } else {
         this.walk = new FlyAction(this);
         this.clickMc.y = -100;
         dispatchEvent(new RobotEvent(RobotEvent.WALK_START));
      }
      // 拼 NonoInfo：自己用 NonoManager.info，他人用 _info.nonoInfo 或 _info.superNono/nonoColor
      var _loc4_:NonoInfo = ...;  // 见改后全文
      if (Boolean(_loc4_)) this.showNono(_loc4_, this._info.actionType);
      break;
   ```

3. 在 **showNono(param1, param2)** 里，对「他人」且 **param2 != 0**（飞行）时，使用 **NonoFlyModel**，不要用 NonoModel：
   - 即：`else if (param2 != 0) { this._nono = new NonoFlyModel(param1, this); }`  
   - 再 `else { this._nono = new NonoModel(param1, this); }`

4. 确认 **addEvent()** 中有：  
   `UserManager.addActionListener(this._info.userID, this.onAction);`  
   这样模型才会收到 dispatchAction 派发的 FLY_MODE。

**参考**：`docs/BasePeoleModel_改后全文.txt`、`docs/BasePeoleModel_showNono_他人飞行补充.txt`、`docs/2112配套修改_PeopleActionEvent与BasePeoleModel.txt`

---

## 可选：UserInfo.setForPeoleInfo 的 NoNo 段

若他人飞行时 B 端仍不显示 NONO 形态（或形态为 0），检查 **UserInfo.setForPeoleInfo** 是否从 2003 的 NoNo 段读了 **superNono**（形态 1–5）并赋给 `param1.superNono`。服务端 2003 顺序为：Flag(4), State(4), Color(4), SuperNono(4), transTime(4)。  
**参考**：`docs/UserInfo_改后全文.txt`（NoNo 段约 230–251 行）。

---

## 验证

1. 重新导出/保存 SWF，确保 A、B 两个客户端都加载该 SWF（清缓存）。
2. A 登录进图，B 登录进同一张图。
3. A 点击飞行；B 应能看到 A 的飞行飞船/NONO。
4. 若仍看不到，在 B 端按 `docs/他人仍看不见飞行_排查与修复.md` 加 trace（2112 他人分支、getUser、FLY_MODE 收到）逐项排查。

---

## 文档索引

| 文档 | 用途 |
|------|------|
| `NonoManager_改后全文.txt` | 2112 常驻监听与他人分支完整代码 |
| `2003处理补发FLY_MODE_他人看见飞行.txt` | 2003 补发 FLY_MODE 的步骤与代码 |
| `UserListCmdListener_改后全文.txt` | 2003 处理类示例（含补发 FLY_MODE） |
| `BasePeoleModel_改后全文.txt` | FLY_MODE 分支与 showNono 他人飞行 |
| `他人仍看不见飞行_排查与修复.md` | 仍不显示时的 trace 与排查表 |
| `B看不见A飞行_基于前端脚本的分析.md` | 数据流与断点分析 |

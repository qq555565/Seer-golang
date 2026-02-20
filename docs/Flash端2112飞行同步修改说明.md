# Flash 端 2112 飞行同步修改说明

## 1. 问题与结论

- **服务端**：已正确广播 CMD 2112（飞行开关）和 CMD 2003（带 `actionType=FlyMode` 的玩家列表），同图玩家都会收到。
- **客户端缺口**：收到 2112 后，当前只处理了**自己**的飞行（`MainManager.actorInfo` / `actorModel`），没有对**他人**更新 `UserInfo.actionType` 并刷新其 `BasePeoleModel` / 飞行骨骼，导致别人看不到“他人”的飞行表现。

因此需要在 **NieoCore.swf** 里做两件事（可先做 ①，② 为可选增强）：

1. **① 2112 监听处**：收到 2112 时，对「他人」更新 `UserInfo.actionType` 并触发其模型刷新（飞行/落地）。
2. **② 2003 处理处（可选）**：确认 2003 会覆盖/更新已存在玩家的 `UserInfo`（含 `actionType`），并在需要时触发一次模型刷新。

---

## 2. 在 FFDec 中定位 2112 监听

1. 用 **FFDec** 打开 `dll/NieoCore.swf`。
2. 在脚本中搜索：
   - `CommandID.ON_OR_OFF_FLYING`  
   - 或直接搜 **2112**
3. 找到类似：
   ```text
   SocketConnection.addCmdListener(CommandID.ON_OR_OFF_FLYING, this.onFlyChanged);
   ```
4. 打开该监听所在的**类**（类名可能是 `FlyCmdListener`、`MapFlyListener` 等），找到 **onFlyChanged**（或类似命名）的完整实现。

**协议格式（与当前 Go 服务端一致）**：

- 包体 8 字节：`userId(4, 大端) + flyMode(4, 大端)`。
- `flyMode == 0` 表示落地，非 0（如 4）表示飞行。

---

## 3. 修改 2112 监听函数：增加「他人」分支

当前逻辑通常只处理「自己」：

- `uid == MainManager.actorInfo.userID` 时：更新自己的 `actionType`、切换飞行骨骼/NonoFlyModel 等。

需要增加 **else** 分支处理「他人」：

- 用包体前 4 字节得到 **uid**，后 4 字节得到 **mode**（flyMode）。
- 若 `uid != MainManager.actorInfo.userID`：
  - 用 `UserManager.getUser(uid)` 取得该玩家的 `UserInfo`。
  - 设置 `info.actionType = mode`。
  - 通过**事件**或**直接刷新模型**两种方式之一，让该玩家的 `BasePeoleModel` 更新飞行表现。

### 方式 A：事件驱动（推荐，与 9019 一致）

若项目中有类似 `PeopleActionEvent` 的机制，可新增一个「飞行状态」事件（例如 `PeopleActionEvent.FLY_MODE` 或 `ACTION_TYPE_CHANGE`），在 2112 的「他人」分支里：

- `UserManager.dispatchAction(uid, PeopleActionEvent.FLY_MODE, { actionType: mode });`

然后在 **BasePeoleModel** 的 **onAction** 里增加对 `FLY_MODE` 的处理：更新 `this._info.actionType`，并根据是否已有 NONO 调用 `showNono(..., this._info.actionType)` 或刷新飞行骨骼（与「自己」切飞行时的逻辑一致）。

### 方式 B：直接刷新模型

在 2112 的「他人」分支里：

- 设置 `info.actionType = mode` 后，用 `UserManager.getUserModel(uid)` 得到 `BasePeoleModel`。
- 若存在，则调用一个「根据当前 actionType 刷新飞行/落地表现」的方法（若没有，需在 BasePeoleModel 里新增，例如 `refreshFlyState()`，内部根据 `_info.actionType` 切换骨骼或重新 `showNono`）。

伪代码（保持与你现有命名风格一致即可）：

```actionscript
private function onFlyChanged(e:SocketEvent):void {
   var data:ByteArray = e.data as ByteArray;
   data.position = 0;
   var uid:uint = data.readUnsignedInt();   // 包体前 4 字节：谁在飞
   var mode:uint = data.readUnsignedInt();  // 包体后 4 字节：flyMode

   if (uid == MainManager.actorInfo.userID) {
      // 原有：更新自己 actionType / 切换为 NonoFlyModel / 飞行骨骼
      // ...
   } else {
      // ★ 新增：更新「他人」的 actionType，并刷新模型
      var info:UserInfo = UserManager.getUser(uid);
      if (info) {
         info.actionType = mode;
         // 方式 A：事件驱动（需在 PeopleActionEvent 中加 FLY_MODE，在 BasePeoleModel.onAction 中处理）
         // UserManager.dispatchAction(uid, PeopleActionEvent.FLY_MODE, { actionType: mode });

         // 方式 B：直接刷新该玩家的 BasePeoleModel（需有 refreshFlyState 或等价方法）
         var model:BasePeoleModel = UserManager.getUserModel(uid) as BasePeoleModel;
         if (model && model.refreshFlyState) {
            model.refreshFlyState();
         }
      }
   }
}
```

你拿到 FFDec 里**真实的类名、变量名和现有 onFlyChanged 代码**后，把整段函数（及类内必要的 import/成员）发出来，可以据此改成一版「XXX_改后全文.txt」风格的完整替换代码。

---

## 4. BasePeoleModel.showNono 对「他人」飞行形态的支持

当前 `BasePeoleModel_改后全文.txt` 中，`showNono(param1, param2)` 的 `param2` 为 actionType；对「他人」在 `else` 分支里仍用 `NonoModel`。若希望他人飞行时也显示飞行 NONO，需要让「他人」在 `param2 != 0` 时使用 **NonoFlyModel**。

将：

```actionscript
else if(param1.userID == MainManager.actorInfo.userID)
{
   this._nono = new NonoFlyModel(param1,this);
}
else
{
   this._nono = new NonoModel(param1,this);
}
```

改为：

```actionscript
else if(param1.userID == MainManager.actorInfo.userID)
{
   this._nono = new NonoFlyModel(param1,this);
}
else if(param2 != 0)
{
   this._nono = new NonoFlyModel(param1,this);  // 他人飞行模式
}
else
{
   this._nono = new NonoModel(param1,this);
}
```

这样当 2112 或 2003 把「他人」的 `actionType` 更新后，再调用 `showNono(..., actionType)` 时，他人也会显示飞行形态的 NONO。

---

## 5. 可选：2003 对已存在玩家的 actionType 更新

- 找到处理 **CMD 2003**（地图玩家列表）的类（如 `UserListCmdListener` 或类似）。
- 确认在解析 2003 时：
  - 使用 `UserInfo.setForPeoleInfo` 后，对**同一 userId** 是**覆盖/更新**已有 `UserInfo`，而不是「仅当不存在才新建」。
- 这样服务端在 2112 后广播的新 2003 里，`actionType` 会写进该玩家的 `UserInfo`。
- 若 2003 处理里只在「进图时」创建/刷新一次模型，可考虑在「更新已有 UserInfo」后，对已存在的 `BasePeoleModel` 做一次根据 `actionType` 的刷新（与 2112 的「他人」刷新逻辑一致），这样仅靠 2003 也能在部分客户端上同步他人飞行状态。

---

## 6. 建议的下一步

1. 在 **NieoCore.swf** 中按上面步骤找到 **2112 监听类** 和 **onFlyChanged**（或等价）的**完整 AS3 代码**。
2. 把该**函数**以及**类开头**的 import、成员变量等**完整复制**出来。
3. 将这段原始代码发给我（或保存为 `XXX_原文.txt`），我可以按你现有风格改成一版 **XXX_改后全文.txt**，方便你在 FFDec 里整段替换保存。

同时，若采用**方式 A（事件）**，需要：

- 在 **PeopleActionEvent** 中增加常量（如 `FLY_MODE` 或 `ACTION_TYPE_CHANGE`）；
- 在 **BasePeoleModel.onAction** 的 switch 中增加对应 case，内容为：`this._info.actionType = param1.data.actionType`，并调用与「自己」切飞行时相同的刷新逻辑（如 `showNono(this._nono.info, this._info.actionType)` 或骨骼切换）。

若你提供 2112 监听类的完整原文，我可以把「方式 A」的 BasePeoleModel 补充片段也一并写好，方便你粘贴到对应类里。

---

## 7. 提供原文以获取改后全文

1. 用 FFDec 打开 **dll/NieoCore.swf**，在脚本中搜索 **ON_OR_OFF_FLYING** 或 **2112**，找到注册 `addCmdListener` 的类。
2. 打开该类，将**整个类**（含 package、import、类名、`start()`/监听注册、**onFlyChanged 完整函数**等）复制到记事本，保存为 `XXX_原文.txt`（XXX 为实际类名，如 `FlyCmdListener`）。
3. 把该原文发给我（或放在 `docs/` 下），我会按你现有风格改成一版 **XXX_改后全文.txt**，你在 FFDec 里整类替换即可。

**改后模板**：`docs/2112监听类_改后模板.txt` 中给出了「他人」分支的插入代码，以及方式 A 所需的 `PeopleActionEvent` 常量和 `BasePeoleModel.onAction` 的 case 片段，可先按模板自行合并；若提供原文，可得到与 `FollowCmdListener_改后全文.txt` 同风格的完整替换版。

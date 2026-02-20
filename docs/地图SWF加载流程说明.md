# 地图 SWF 加载流程说明

用于排查「塞西利亚星第一层」等地图卡在「加载地图 100% / 快去探索!」不关闭的问题。

---

## 1. 流程概览

```
MapManager.changeMap(40)
  → MapController.changeMap(40)
  → startSwitch() → _startSwitch()
  → new MapModel(40, ...)   // 创建地图模型并开始加载
  → MapModel.loadMap()
       → new MCLoader(..., "加载地图", false)   // 第5参数 false = 不自动关加载
       → doLoad(ClientConfig.getMapPath(40))    // 加载 resource/map/40.swf
  → SWF 加载完成
       → MCLoader.initHandler()  // 不关 loading，因为 autoCloseLoading=false
       → dispatchEvent(MCLoadEvent.SUCCESS)
  → MapModel.onMapComplete()
       → 解析 root["depth_mc"], "type_mc", "top_mc", "control_mc", "buttonLevel", "animator_mc", "rect_mc", "bg_mc"
       → dispatchEvent(MAP_LOADER_COMPLETE)
       → initFindPath() → setTimeout(onMakMap, 200)
  → 200ms 后 MapModel.onMakMap()
       → makeMapArray()
       → dispatchEvent(MAP_INIT)
  → MapController.onMapInit()
       → 若已登录且 isInMap：SocketConnection.send(LEAVE_MAP)  // 发 2002
  → 收到 2002 响应 → onLeaveMap()
       → comeInMap() → SocketConnection.send(ENTER_MAP, ...)  // 发 2001
  → 收到 2001 响应 → onEnterMap()
       → 解析 body 为 UserInfo，若 userID == MainManager.actorID
       → initMapFunction()
       → _mapModel.closeLoading()   // ★ 只有这里会关闭「加载地图」界面
```

结论：**加载界面只有在收到 CMD=2001 且解析出的 userID 等于当前玩家时才会关闭**。若 2001 没收到、或 body 解析后 userID 不匹配，界面会一直停在 100%。

---

## 2. 关键文件位置

| 作用 | 路径 |
|------|------|
| 切图入口 | `新建文件夹(5)/scripts/com/robot/core/manager/MapManager.as` → `changeMap()` |
| 切图与收包逻辑 | `新建文件夹(5)/scripts/com/robot/core/controller/MapController.as` |
| 地图 SWF 加载 | `新建文件夹(5)/scripts/com/robot/core/mode/MapModel.as` → `loadMap()`、`onMapComplete()`、`onMakMap()` |
| 加载条 UI | `新建文件夹(5)/scripts/com/robot/core/newloader/MCLoader.as` |
| 加载标题「加载地图」 | `MapModel.as` 第 363/375 行 `new MCLoader(..., "加载地图", false)` |
| 关闭加载 | `MapController.as` 第 272 行 `this._mapModel.closeLoading()`（在 `initMapFunction()` 内） |
| 2001 处理 | `MapController.as` → `onEnterMap()`，解析 body 为 UserInfo，仅当 `userID == MainManager.actorID` 时调用 `initMapFunction()` |

---

## 3. 地图 SWF 结构要求（MapModel.onMapComplete）

地图 SWF 的 `root` 必须包含以下子对象，否则可能报错或行为异常：

- `root["depth_mc"]` - 深度层
- `root["type_mc"]` - 可行走区域
- `root["top_mc"]` - 顶层
- `root["control_mc"]` - 控制层
- `root["buttonLevel"]` - 按钮层
- `root["animator_mc"]` - 动画层（可无）
- `root["rect_mc"]` 或用代码创建默认矩形
- `root["bg_mc"]` - 背景

若 **40.swf** 里缺少或改名了上述实例名，`onMapComplete` 可能抛错，导致 `MAP_INIT` 未发出，进而不会发 2002/2001，加载界面也不会关。

---

## 4. 排查「卡在 100%」时建议看的点

1. **是否收到 2001**
   - 在客户端或抓包看：进入地图 40 时是否收到 CMD=2001 的包。
   - 服务端日志有「进入地图响应2001」「发送 CMD=2001」只说明服务端发了，要确认客户端确实收到并处理。

2. **onEnterMap 是否被调用**
   - 在 `MapController.as` 的 `onEnterMap()` 开头下断点或打日志，看进入地图 40 时是否进入该函数。

3. **userID 是否匹配**
   - 在 `onEnterMap()` 里看解析出的 `_loc3_.userID` 是否等于 `MainManager.actorID`。
   - 若不等，不会执行 `initMapFunction()`，也就不会 `closeLoading()`。

4. **2001 body 格式是否与客户端一致**
   - 服务端组包：`handlers.go` 里 `buildPeopleInfo()`。
   - 客户端解析：搜索 `UserInfo.setForPeoleInfo` 或 `setForPeoleInfo`，看读取顺序是否与 `buildPeopleInfo` 的写入顺序一致（含 userID、坐标等）。

5. **MAP_INIT 是否发出**
   - 若 40.swf 在 `onMapComplete` 里因缺少子对象抛错，不会执行到 `onMakMap()`，也就不会 `dispatchEvent(MAP_INIT)`，后续 2002/2001 流程不会走。
   - 可在 `MapModel.as` 的 `onMapComplete`、`onMakMap` 里下断点或打日志确认。

6. **是否误触 MAP_LOADER_CLOSE**
   - `MapController._startSwitch()` 里监听了 `MapEvent.MAP_LOADER_CLOSE`，会调 `onMapFail()`，会 `closeLoading()` 但把切图当作失败处理。若加载界面被用户点关闭或异常触发 CLOSE，会走这里，界面关但地图可能未正常进入。

---

## 5. 服务端 2001 包体（buildPeopleInfo）顺序参考

便于与客户端 `setForPeoleInfo` 对照，当前顺序大致为（以代码为准）：

1. sysTime (4)  
2. userID (4)  
3. nick 固定长 (16)  
4. color (4)  
5. texture (4)  
6. vipFlags (4), vipStage (4)  
7. actionType (4), posX (4), posY (4), action (4), direction (4), changeShape (4)  
8. 精灵相关：petID, catchTime, petDV, …  
9. 其余字段见 `handlers.go` 中 `buildPeopleInfo`。

若客户端按不同顺序或长度读，会导致 userID 或后续字段错位，从而 `userID != MainManager.actorID`，不关加载。

---

## 6. 如何本地调试

1. 用 Flash/Animate 或可调试 SWF 的 IDE 打开客户端工程，对上述文件下断点。  
2. 在 `MapController.onEnterMap`、`MapModel.onMapComplete`、`MapModel.onMakMap` 打日志，确认 2001 收到后是否进入 `onEnterMap`、以及是否执行到 `initMapFunction()`。  
3. 检查 `resource/map/40.swf` 是否与其它正常地图（如 41、42）结构一致，是否包含 `depth_mc`、`type_mc`、`rect_mc`、`bg_mc` 等。

按上述顺序排查，可以区分是「没收到 2001」「解析错误导致 userID 不匹配」还是「MAP_INIT 未发出（地图 SWF 或 onMapComplete 异常）」导致的卡住。

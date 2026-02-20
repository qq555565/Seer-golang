# Flash 客户端修改：按协议正确显示超能 NONO 形态

本文教你在不改 Go 服务端的前提下，通过反编译并修改 Flash（SWF）客户端，让游戏根据协议中的 **SuperNono 形态(1–5)** 加载对应的 `nono_N.swf` / `action/X_N.swf`，从而自己和同图其他玩家的超能 NONO 形态都正确显示。

---

## 一、准备工具

| 工具 | 用途 | 下载/说明 |
|------|------|-----------|
| **JPEXS Free Flash Decompiler (FFDec)** | 反编译 SWF、查看/编辑 ActionScript、导出/替换资源 | https://github.com/jindrapetrik/jpexs-decompiler/releases |
| **可选：RABCDAsm** | 若需改 ABC 字节码（高级） | 一般用 FFDec 的脚本编辑即可 |

建议先用 **FFDec** 打开你的 `Client.swf`、`NieoCore.swf` 或加载 `nono` 资源的 SWF，在“脚本”里搜索下面提到的字符串。

---

## 二、协议里 SuperNono 在哪里（必读）

服务端已在以下协议中下发 **SuperNono（1–5）**，客户端需要解析并缓存，用于拼资源 URL。

| 协议 | 用途 | SuperNono 在包体中的位置（大端 uint32） |
|------|------|----------------------------------------|
| **CMD 1001** 登录响应 | 自己 | 包体中的 **NoNo 段**：先 hasNono(4)，再 **superNono 形态(4)**，然后是 flag/color/nick 等。具体偏移依赖你 1001 的解析顺序；若复杂可登录后发 **9003** 查自己。 |
| **CMD 9003** NONO 信息 | 自己/指定玩家 | 响应包体：userID(4)、Flag(4)、State(4)、Nick(16)、**SuperNono(4)** 在 **字节 28–31**，接着 Color(4)、Power(4)… |
| **CMD 2003** 地图玩家列表 | 同图所有玩家 | 包体前 4 字节为人数，随后每条 **PeopleInfo**。单条内：sysTime(4)、userID(4)、nick(16)、color(4)、texture(4)、vip(4)、vipStage(4)、动作与坐标(24)、精灵(20)、师徒(8)、**NoNo：Flag(4)、State(4)、Color(4)、SuperNono(4)、transTime(4)**。即每条 PeopleInfo 中 **SuperNono 在该条起始偏移 104 字节处**（4 字节，大端）。 |
| **CMD 9019** NONO 跟随/回家 | 某玩家跟随状态与形态 | 包体 **body[32:36]** 为 SuperNono（4 字节大端）。前 32 字节含 userID、action、nick 等。 |

要点：

- 所有多字节整数均为 **大端序**。
- 对**每个玩家**（含自己）维护 **userId → form(1–5)**，在收到 1001/9003/2003/9019 时更新；绘制该玩家的 NONO 时用 **N = form**（未知则用 1）拼 URL。

---

## 三、在 Flash 里要改的两类地方

### 3.1 解析协议：收到 1001 / 9003 / 2003 / 9019 时写入“形态缓存”

- **1001**：在解析登录响应、处理 NoNo 段时，读到 **superNono**（1–5）后，把 `map[自己的userId] = superNono`。
- **9003**：解析响应时，从**字节 28–31** 读 SuperNono，对应当前查询的 targetUserId，执行 `map[targetUserId] = superNono`。
- **2003**：解析人数后，对每条 PeopleInfo 按固定顺序解析；每条从**该条起始 +104 字节**读 4 字节大端 uint32 为 SuperNono，用该条里的 userID 做 key：`map[userId] = superNono`。
- **9019**：从 **body[32:36]** 读 4 字节大端为 SuperNono；body 里前面有该 NONO 所属的 **userID**（具体偏移需对照你现有 9019 解析代码），然后 `map[userId] = superNono`。

这样，任意时刻“某个 userId 的形态”都可以从 `map[userId]` 取得，没有则用 1。

### 3.2 拼资源 URL：把所有写死的 `_1` 改成根据 userId 取形态 N

客户端加载超能 NONO 资源时，通常会有类似：

- `/resource/nono/super/nono_1.swf`
- `/resource/nono/super/action/6_1.swf`
- `/resource/nono/super/exp/1_1.swf` 等

需要改成“按玩家”的形态 N（1–5）来拼，例如：

- `nono_` + N + `.swf`
- `action/6_` + N + `.swf`
- `exp/1_` + N + `.swf`

即：**凡带 `_数字.swf` 的超能 NONO 资源，数字都应来自当前要绘制的玩家的形态 N，而不是写死 1。**

---

## 四、操作步骤（按顺序做）

### 步骤 1：用 FFDec 打开正确的 SWF

- **NONO 逻辑不在 Client.swf 里。** 根据 `config/dll.xml`，主逻辑在 **`dll/NieoCore.swf`**（核心资源），其中包含 NONO 协议监听类（如 `FollowCmdListener` 对应 9019、`UserListCmdListener` 对应 2003 等）。
- 打开 **JPEXS Free Flash Decompiler**，菜单 **文件 → 打开**，选择：
  - **`D:\go\gameres\root\dll\NieoCore.swf`**（优先在这里搜 `nono`、`nono_1`、`super`、`9019`、`2003`）。
- 若在 NieoCore 里也搜不到，再对 **Client.swf**、**Assets.swf**（若有）各做一次相同搜索。

### 步骤 2：搜资源路径，找到拼 URL 的代码

- 在 FFDec 左侧选 **“Script”**（脚本），用 **Ctrl+Shift+F** 或菜单 **Search → Find**。
- 搜索以下字符串（可逐个试）：
  - `nono_1`
  - `nono/super`
  - `action/6_1`
  - `exp/1_1`
  - `_1.swf`
- 找到后，会定位到某段 **ActionScript**（可能是 AS3），例如：

```actionscript
// 可能是类似这样的代码（仅示例，实际以你反编译结果为准）
var url:String = "/resource/nono/super/nono_1.swf";
loader.load(new URLRequest(url));
```

或：

```actionscript
var form:int = 1;
var path:String = "nono/super/nono_" + form + ".swf";
```

你要做的是：

1. 确定这段代码在**绘制哪个玩家**的 NONO 时执行（自己 vs 其他玩家）。
2. 把“形态”来源改为：**根据该玩家的 userId 从你的“形态缓存”里取 N**；若没有则 N=1。
3. 用 N 拼出：
   - `nono_N.swf`
   - `action/6_N.swf`
   - `exp/X_N.swf`（X 为原有编号）

例如（伪代码）：

```actionscript
// 假设当前要绘制的是 userId，你有一个全局或单例的 map：userId -> superNonoForm(1-5)
var n:int = getSuperNonoForm(userId);   // 没有则返回 1
if (n < 1) n = 1;
if (n > 5) n = 5;
var url:String = "/resource/nono/super/nono_" + n + ".swf";
```

对 `action/6_X.swf`、`exp/X_X.swf` 同理：把后面的 `X` 都改成同一个 `n`。

### 步骤 3：搜协议命令号，找到写缓存的地方

- 在脚本里搜索：
  - `1001`、`9003`、`2003`、`9019`（可能以常量名出现，如 `LOGIN_IN`、`NONO_INFO`、`LIST_MAP_PLAYER`、`NONO_FOLLOW_OR_HOOM`）。
- 或搜索 **CommandID**、**Packet**、**Socket** 等和收包相关的类名/变量名，再在对应 switch/case 里找到 1001/9003/2003/9019 的处理。

在**收到 1001 的登录响应**时：

- 在解析到 NoNo 段、读到 **superNono**（1–5）后，调用类似：  
  `setSuperNonoForm(myUserId, superNono);`

在**收到 9003 响应**时：

- 从包体**偏移 28** 读 4 字节大端 uint32 为 SuperNono；
- 用 9003 请求/响应里的 **目标 userId** 调用：  
  `setSuperNonoForm(targetUserId, superNono);`

在**收到 2003** 时：

- 先读 4 字节人数，再循环每条 PeopleInfo；
- 每条按固定布局解析，从**该条起始 +104** 读 4 字节为 SuperNono，该条前面的 **userID** 在固定偏移（例如该条起始 +4）；
- 调用：  
  `setSuperNonoForm(thatUserId, superNono);`

在**收到 9019** 时：

- 从 **body[32:36]** 读 4 字节大端为 SuperNono；
- 从 body 前 32 字节中取该 NONO 的 **userID**（需对照现有 9019 解析），调用：  
  `setSuperNonoForm(userId, superNono);`

`setSuperNonoForm(userId, form)` 即：把你的全局 `Map` 或字典里 `userId -> form` 更新；`getSuperNonoForm(userId)` 即：查表，没有则返回 1。

### 步骤 4：确认 2003 每条 PeopleInfo 的布局

若你已有 2003 的解析代码，只需在“写 NoNo 信息”的那一段里，按顺序找到 **Flag(4)、State(4)、Color(4)、SuperNono(4)**，其中 **SuperNono 即形态 N**。

服务端 **buildPeopleInfo** 顺序是（NoNo 段）：

- Flag: 4 字节  
- State: 4 字节  
- Color: 4 字节  
- **SuperNono: 4 字节**  
- transTime: 4 字节  

所以每条 PeopleInfo 里，从“该条起始”算，**SuperNono 在偏移 104**（前面是 sysTime、userID、nick、color、texture、vip、坐标、精灵、师徒、NoNo Flag/State/Color 共 104 字节）。

### 步骤 5：改完后保存与测试

- 在 FFDec 里：编辑完脚本后，用 **File → Save** 或导出为 SWF（视 FFDec 版本，可能支持“替换脚本并保存”）。
- 若 FFDec 不能直接保存，可：
  - **导出脚本**为 .as 或 .json，用 **RABCDAsm** 等重新编译 ABC 再替换回 SWF；或
  - 只改**资源加载的那几处**为“按 N 拼 URL”，并保证 1001/9003/2003/9019 的解析里把 SuperNono 写入缓存，然后重新编译整个项目（若有源码）。

测试建议：

1. 自己账号 12 级超能 → 应看到形态 5（nono_5.swf）。
2. 小号 1 级超能 → 应看到形态 1（nono_1.swf）。
3. 同屏两个号时，各自形态正确，且资源请求日志里能看到 `nono_5.swf` 与 `nono_1.swf` 等不同请求。

---

## 五、形态 N 与超能等级对应（服务端已实现，客户端只读 N）

服务端逻辑（你无需在 Flash 里再算等级）：

- 超能等级 1–3 → 形态 1  
- 4–6 → 形态 2  
- 7–9 → 形态 3  
- 10–11 → 形态 4  
- 12 → 形态 5  

客户端只使用协议里下发的 **SuperNono(1–5)** 拼 URL 即可。

---

## 六、常见问题

**Q：搜不到 `nono_1` 或 `nono/super`？**  
A：可能字符串被拆成多段、或来自配置文件。可再搜 `nono`、`super`、`resource`、`.swf`，或搜 Loader、URLRequest 的用法，顺藤摸瓜找到拼 URL 的位置。

**Q：2003 每条长度不固定怎么办？**  
A：服务端 2003 的列表项是“变长”（含装备等），但**每条前面的部分**到 NoNo 段是固定的；NoNo 段内 SuperNono 在 **该条 NoNo 段起始 +12 字节**（Flag+State+Color 后）。若你已有“按条解析”的代码，只需在 NoNo 段里按顺序多读 4 字节作为 SuperNono。

**Q：9019 里 userID 在哪儿？**  
A：服务端 9019 body 前 4 字节是 userID（大端）；接着有 action、nick 等，到 body[32:36] 是 SuperNono。你可搜 9019 的解析处确认你项目里 body 的布局。

**Q：改完 SWF 后无法运行？**  
A：检查语法、括号、类型；若用 FFDec 直接改脚本，注意其编辑器的限制。复杂改动建议导出为 .as，用 FlashDevelop / Apache Flex 等重新编译再打包成 SWF。

---

## 七、小结

| 步骤 | 做什么 |
|------|--------|
| 1 | 用 FFDec 打开 Client.swf（或相关 SWF） |
| 2 | 搜 `nono_1` / `nono/super` 等，找到拼 NONO 资源 URL 的代码 |
| 3 | 把“形态”改为从 **userId → form** 的缓存读取，用 N 拼 `nono_N.swf`、`action/6_N.swf` 等 |
| 4 | 搜 1001/9003/2003/9019 处理，在解析处把 **SuperNono** 写入以 userId 为 key 的缓存 |
| 5 | 保存/重新编译 SWF，进游戏用不同超能等级账号验证形态与请求路径 |

按上述修改后，无需 Go 资源服做任何 URL 重写，自己与他人超能 NONO 形态即可与协议一致、正确显示。

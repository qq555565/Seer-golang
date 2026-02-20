# 对战切换精灵修复方案 V2

## 问题分析

### 根本原因
经过深入分析，发现问题不在于2505响应的构建，而在于**前端如何解析和处理AttackValue**。

### AttackValue的含义
```go
buildAttackValue(userID, skillID, atkTimes, lostHP, gainHP, remainHP, maxHP, state, isCrit, petType)
```

参数含义：
- `userID`：**攻击者**的ID
- `skillID`：使用的技能ID
- `lostHP`：造成的伤害
- `remainHP`：**被攻击者**的剩余HP
- `maxHP`：**被攻击者**的最大HP

### 当前的2505响应结构
```go
// 第一个AttackValue：玩家攻击敌人
buildAttackValue(玩家ID, 技能ID, 1, 伤害, 0, 玩家HP, 玩家MaxHP, ...)

// 第二个AttackValue：敌人反击玩家
buildAttackValue(敌人ID(0), 技能ID, 1, 伤害, 0, 玩家HP, 玩家MaxHP, ...)
```

### 问题所在
第一个AttackValue的`remainHP`应该是**敌人**的剩余HP（因为玩家攻击敌人），而不是玩家的HP！

## 正确的修复方案

### 修改位置
`golang_version/internal/handlers/handlers.go`，大约第4350行

### 当前错误的代码
```go
// 第一个AttackValue：玩家攻击的结果
body = append(body, buildAttackValue(uint32(ctx.UserID), skillID, 1, damage, 0, int32(playerHPAfterAttack), battle.PlayerMaxHP, 0, 0, 0)...)

// 第二个AttackValue：敌人反击的结果
if battle.EnemyHP > 0 {
    body = append(body, buildAttackValue(enemyUserID, enemySkillID, 1, enemyDamage, 0, int32(playerHPAfterCounter), battle.PlayerMaxHP, 0, 0, 0)...)
} else {
    body = append(body, buildAttackValue(enemyUserID, 0, 0, 0, 0, int32(playerHPAfterAttack), battle.PlayerMaxHP, 0, 0, 0)...)
}
```

### 正确的代码
```go
// 第一个AttackValue：玩家攻击敌人
// userID=玩家ID，remainHP=敌人的剩余HP，maxHP=敌人的最大HP
body = append(body, buildAttackValue(uint32(ctx.UserID), skillID, 1, damage, 0, int32(battle.EnemyHP), battle.EnemyMaxHP, 0, 0, 0)...)

// 第二个AttackValue：敌人反击玩家
// userID=敌人ID(0)，remainHP=玩家的剩余HP，maxHP=玩家的最大HP
if battle.EnemyHP > 0 {
    body = append(body, buildAttackValue(enemyUserID, enemySkillID, 1, enemyDamage, 0, int32(playerHPAfterCounter), battle.PlayerMaxHP, 0, 0, 0)...)
} else {
    // 敌人已死亡，不反击
    body = append(body, buildAttackValue(enemyUserID, 0, 0, 0, 0, int32(battle.EnemyHP), battle.EnemyMaxHP, 0, 0, 0)...)
}
```

## 关键改动
1. **第一个AttackValue**：`remainHP`从`playerHPAfterAttack`改为`battle.EnemyHP`，`maxHP`从`battle.PlayerMaxHP`改为`battle.EnemyMaxHP`
2. **第二个AttackValue（敌人死亡时）**：`remainHP`从`playerHPAfterAttack`改为`battle.EnemyHP`，`maxHP`从`battle.PlayerMaxHP`改为`battle.EnemyMaxHP`

## 为什么这样修复
- 第一个AttackValue表示"玩家攻击敌人"，所以`remainHP`应该是敌人的剩余HP
- 第二个AttackValue表示"敌人反击玩家"，所以`remainHP`应该是玩家的剩余HP
- 前端会根据`userID`来判断这是谁的攻击，然后更新对应目标的HP

## 前端处理流程
1. 收到第一个AttackValue（userID=玩家ID）
   - 前端识别这是玩家的攻击
   - 触发`LOST_HP`事件，更新敌人的HP为`remainHP`（敌人的剩余HP）
   
2. 收到第二个AttackValue（userID=敌人ID）
   - 前端识别这是敌人的攻击
   - 触发`LOST_HP`事件，更新玩家的HP为`remainHP`（玩家的剩余HP）
   
3. 调用`nextRound()`
   - 检查玩家的HP是否为0
   - 如果为0，触发`NO_BLOOD`事件
   - 弹出切换精灵面板

## 应用修复
参考`apply_battle_fix_v2.py`脚本自动应用修复。

# 对战切换精灵问题修复说明

## 问题描述
在对战中，当玩家精灵HP为0时，无法切换精灵。前端没有弹出切换精灵面板。

## 根本原因
后端在构建2505响应（USE_SKILL的响应）时，第一个AttackValue中的`remainHP`字段使用的是**敌人反击后**的玩家HP，而不是**玩家攻击后、敌人反击前**的HP。

这导致前端在处理第一个AttackValue时，就已经看到了玩家被反击后的HP（可能为0），但前端的逻辑是：
1. 处理第一个AttackValue（玩家攻击）
2. 处理第二个AttackValue（敌人反击）
3. 调用`nextRound()`检查HP是否为0

由于第一个AttackValue中已经包含了反击后的HP，前端的HP更新逻辑会混乱。

## 修复方案
在`golang_version/internal/handlers/handlers.go`的`handleUseSkill`函数中，修改构建2505响应的代码：

### 修改位置
大约在第4330-4355行，找到以下代码：

```go
		enemyDamage = enemyFinalDamage
		battle.PlayerHP -= enemyDamage
		if battle.PlayerHP > battle.PlayerMaxHP {
			battle.PlayerHP = 0
		}
	}

	// PvP 时需用对方 userID 填第二个 AttackValue，客户端才能正确匹配"对方"血条与图标
	opponentUID := battle.OpponentUserID
	ctx.GameServer.BattleMu.Unlock()

	// 构建 2505 响应：两个 AttackValue（玩家攻击 + 敌人/对方反击）
	body := make([]byte, 0, 160)
	// 玩家攻击（我方）
	body = append(body, buildAttackValue(uint32(ctx.UserID), skillID, 1, damage, 0, int32(battle.PlayerHP), battle.PlayerMaxHP, 0, 0, 0)...)
	// 敌人/对方反击：PvP 时填对方 userID，客户端用此匹配"对方"精灵显示
	enemyUserID := uint32(0)
	if opponentUID != 0 {
		enemyUserID = uint32(opponentUID)
	}
	if battle.EnemyHP > 0 {
		body = append(body, buildAttackValue(enemyUserID, enemySkillID, 1, enemyDamage, 0, int32(battle.EnemyHP), battle.EnemyMaxHP, 0, 0, 0)...)
	} else {
		body = append(body, buildAttackValue(enemyUserID, 0, 0, 0, 0, 0, battle.EnemyMaxHP, 0, 0, 0)...)
	}
```

### 替换为：

```go
		enemyDamage = enemyFinalDamage
		battle.PlayerHP -= enemyDamage
		if battle.PlayerHP > battle.PlayerMaxHP {
			battle.PlayerHP = 0
		}
	}

	// 保存HP状态用于构建AttackValue
	playerHPAfterAttack := battle.PlayerHP + enemyDamage // 玩家攻击后、敌人反击前的HP
	if enemyDamage == 0 {
		playerHPAfterAttack = battle.PlayerHP // 如果敌人没反击，两个值相同
	}
	playerHPAfterCounter := battle.PlayerHP // 敌人反击后的HP

	// PvP 时需用对方 userID 填第二个 AttackValue，客户端才能正确匹配"对方"血条与图标
	opponentUID := battle.OpponentUserID
	ctx.GameServer.BattleMu.Unlock()

	// 构建 2505 响应：两个 AttackValue（玩家攻击 + 敌人/对方反击）
	body := make([]byte, 0, 160)
	// 第一个AttackValue：玩家攻击的结果（此时玩家HP还没被敌人反击扣除）
	body = append(body, buildAttackValue(uint32(ctx.UserID), skillID, 1, damage, 0, int32(playerHPAfterAttack), battle.PlayerMaxHP, 0, 0, 0)...)
	// 第二个AttackValue：敌人反击的结果（此时玩家HP已被敌人反击扣除）
	enemyUserID := uint32(0)
	if opponentUID != 0 {
		enemyUserID = uint32(opponentUID)
	}
	if battle.EnemyHP > 0 {
		// 敌人反击：显示敌人的攻击和玩家受到反击后的HP
		body = append(body, buildAttackValue(enemyUserID, enemySkillID, 1, enemyDamage, 0, int32(playerHPAfterCounter), battle.PlayerMaxHP, 0, 0, 0)...)
	} else {
		// 敌人已死亡：不反击
		body = append(body, buildAttackValue(enemyUserID, 0, 0, 0, 0, int32(playerHPAfterAttack), battle.PlayerMaxHP, 0, 0, 0)...)
	}
```

## 修复后的效果
1. 第一个AttackValue正确显示玩家攻击后的状态（玩家HP未被反击扣除）
2. 第二个AttackValue正确显示敌人反击后的状态（玩家HP被反击扣除）
3. 前端的`RemainHpManager`会正确更新两次HP
4. 当玩家HP为0时，`PlayerMode.nextRound()`会检测到并触发`NO_BLOOD`事件
5. `NO_BLOOD`事件处理器会调用`showPetPanel(true)`弹出切换精灵面板

## 测试步骤
1. 重新编译服务器
2. 启动服务器
3. 进入对战
4. 让敌人攻击将玩家精灵HP打到0
5. 确认前端弹出切换精灵面板
6. 选择另一只精灵切换
7. 确认切换成功，战斗继续

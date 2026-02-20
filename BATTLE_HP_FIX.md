# Battle HP=0 Check Fix

## Problem

When a player's pet HP reaches 0 during battle, the backend was only checking `len(user.Pets) <= 1` to determine if the battle should end. This caused issues when:

1. Player has multiple pets in bag (e.g., 6 pets)
2. Some pets are already defeated (HP=0)
3. Current pet is defeated
4. Backend thinks there are still pets available (because `len(user.Pets) > 1`)
5. But actually all remaining pets have HP=0
6. Frontend shows "victory" or gets stuck because no valid pets to switch to

## Root Cause

The backend was checking **pet count** instead of checking if any pets have **HP > 0**.

### Old Code (Line 4383-4393)
```go
} else if battle.PlayerHP == 0 {
    // 我方当前出战精灵 HP 为 0：
    // 如果背包里只剩这一只精灵，则整场战斗失败；
    // 如果还有其它精灵存活，则不立刻结束，交给前端弹出"换宠"面板。
    if len(user.Pets) <= 1 {
        isOver = true
        winnerID = 0 // 敌人获胜
        logger.Info(fmt.Sprintf("[2405] 战斗结束: 敌人获胜（无可用后备精灵）"))
    } else {
        logger.Info("[2405] 当前精灵被击败，但玩家还有其它精灵可用，等待 2407 切换精灵")
    }
}
```

## Solution

Check each pet's actual HP to determine if there are any alive pets available for switching.

### New Code (Line 4383-4410)
```go
} else if battle.PlayerHP == 0 {
    // 我方当前出战精灵 HP 为 0：
    // 检查是否还有其它精灵存活（HP > 0）
    hasAlivePet := false
    petMgr := gamepets.GetInstance()
    for i := range user.Pets {
        if i == 0 {
            continue // 跳过当前出战的精灵（已经HP=0）
        }
        petEV := user.Pets[i].GetEVStats()
        petStats := petMgr.GetStats(user.Pets[i].ID, user.Pets[i].Level, user.Pets[i].DV, petEV, user.Pets[i].Nature)
        if petStats.HP > 0 {
            hasAlivePet = true
            logger.Info(fmt.Sprintf("[2405] 发现存活精灵: index=%d PetID=%d HP=%d/%d", i, user.Pets[i].ID, petStats.HP, petStats.MaxHP))
            break
        }
    }
    
    if !hasAlivePet {
        // 没有其它精灵存活，战斗失败
        isOver = true
        winnerID = 0 // 敌人获胜
        logger.Info(fmt.Sprintf("[2405] 战斗结束: 敌人获胜（无可用后备精灵）"))
    } else {
        // 还有其它精灵存活，不立刻结束，交给前端弹出"换宠"面板
        logger.Info("[2405] 当前精灵被击败，但玩家还有其它精灵可用，等待 2407 切换精灵")
    }
}
```

## Changes

1. **Added HP check loop**: Iterate through all pets (except index 0, which is the current defeated pet)
2. **Calculate actual HP**: Use `GetStats()` to get the real HP value for each pet
3. **Set flag**: If any pet has HP > 0, set `hasAlivePet = true`
4. **Proper battle end**: Only end battle if `!hasAlivePet`
5. **Added logging**: Log which alive pets are found for debugging

## Expected Behavior After Fix

### Scenario 1: Player has alive pets
- Current pet HP = 0
- Backend checks all pets
- Finds pet with HP > 0
- Logs: `[2405] 发现存活精灵: index=X PetID=Y HP=Z/W`
- Logs: `[2405] 当前精灵被击败，但玩家还有其它精灵可用，等待 2407 切换精灵`
- Battle continues, frontend shows pet switch panel

### Scenario 2: Player has no alive pets
- Current pet HP = 0
- Backend checks all pets
- All pets have HP = 0
- Logs: `[2405] 战斗结束: 敌人获胜（无可用后备精灵）`
- Battle ends, player loses

## Testing

1. Start a battle with multiple pets
2. Let current pet be defeated (HP=0)
3. Verify frontend shows pet switch panel
4. Switch to another pet
5. Repeat until all pets are defeated
6. Verify battle ends with player loss

## Files Modified

- `golang_version/internal/handlers/handlers.go` (lines 4383-4410)

## Related Issues

- Pet switching not working when HP=0
- Battle judging victory/loss incorrectly
- Frontend stuck on switch panel

## Date

2026-02-03

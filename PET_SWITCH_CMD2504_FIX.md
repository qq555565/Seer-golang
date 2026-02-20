# Pet Switch CMD 2504 Fix - Battle Victory Bug

## Problem

When switching pets during battle (CMD 2407), the frontend would immediately show a "Victory" dialog and end the battle, even though the enemy still had HP and the battle should continue.

### Symptoms
- User clicks pet switch button
- Selects a different pet
- Backend sends CMD 2407, 2301, 2508, **2504**
- Frontend shows "恭喜你！在这场战斗中获得胜利，继续加油！" (Congratulations! You won this battle!)
- Battle ends incorrectly

### User Report
```
[2026-02-03 20:50:13] INFO 发送 CMD=2504 SEQ=33 LEN=121
[2026-02-03 20:50:13] INFO [2407] 发送战斗状态更新(2504): PlayerPetID=865 EnemyID=10
[2026-02-03 20:50:13] INFO [2407] 2504内容: isCanAuto=0 PlayerInfo(...) EnemyInfo(uid=0,petID=10,name=皮皮,hp=13/13,lv=2)
还是切换不了了 (Still can't switch)
```

## Root Cause

**CMD 2504 is `NOTE_START_FIGHT` - the battle START command, not a status update command!**

From `CommandID.as`:
```actionscript
public static const NOTE_START_FIGHT:uint = 2504;
```

When the backend sends CMD 2504 during pet switching:
1. Frontend receives CMD 2504
2. Frontend thinks a **new battle is starting**
3. Frontend ends the **current battle** (showing victory/loss dialog)
4. Frontend tries to start a new battle with the same data
5. This causes the battle to end incorrectly

### Why Was CMD 2504 Being Sent?

The Lua backend code had a comment saying "使用专门的战斗状态更新命令" (Use a dedicated battle status update command), but CMD 2504 is actually the battle **start** command, not a status update command.

This was a misunderstanding of what CMD 2504 does.

## Solution

**Remove CMD 2504 from the pet switch flow.**

The frontend already properly updates the pet model when it receives:
- **CMD 2407**: Pet switch confirmation (updates pet ID, HP, level)
- **CMD 2301**: Full pet info (updates skills, stats)
- **CMD 2508**: Pet properties (updates EV, DV, nature)

These three commands are sufficient for the frontend to:
1. Update the pet model in `BaseFighterMode.changePet()`
2. Update the pet window display
3. Update the skill panel
4. Continue the battle with the new pet

**CMD 2504 is NOT needed and causes the battle to end incorrectly.**

## Changes Made

### Before (Lines 4843-4920)
```go
// 如果战斗中，发送更新属性（2508）和战斗状态更新（2504）
if exists && battle.IsActive {
    // 发送更新属性（2508）
    ev := gamepets.ClampAndCapEV(selectedPet.GetEVStats())
    propBody := buildNoteUpdateProp(...)
    ctx.GameServer.SendResponse(ctx.ClientData, 2508, ctx.UserID, ctx.SeqID, propBody)

    // 延迟 100ms 后发送战斗状态更新（2504）
    time.Sleep(100 * time.Millisecond)

    // 发送战斗状态更新（2504）- 刷新战斗场景中的精灵模型
    // ... 构建 FightPetInfo ...
    ctx.GameServer.SendResponse(ctx.ClientData, 2504, ctx.UserID, ctx.SeqID, battleBody)
    logger.Info(fmt.Sprintf("[2407] 发送战斗状态更新(2504): ..."))
}
```

### After (Lines 4843-4855)
```go
// 如果战斗中，发送更新属性（2508）
if exists && battle.IsActive {
    // 发送更新属性（2508）
    ev := gamepets.ClampAndCapEV(selectedPet.GetEVStats())
    propBody := buildNoteUpdateProp(...)
    ctx.GameServer.SendResponse(ctx.ClientData, 2508, ctx.UserID, ctx.SeqID, propBody)
    
    logger.Info(fmt.Sprintf("[2407] 精灵切换完成: PetID=%d HP=%d/%d", selectedPetID, petStats.HP, petStats.MaxHP))
}
```

## Expected Behavior After Fix

### Pet Switch Flow
1. User clicks pet button → Pet selection panel appears
2. User selects a pet → Frontend sends CMD 2407
3. Backend receives CMD 2407 → Validates pet, swaps array positions
4. Backend sends:
   - **CMD 2407**: Pet switch confirmation
   - **CMD 2301**: Full pet info
   - **CMD 2508**: Pet properties
5. Frontend receives commands → Updates pet model, closes panel, shows fight panel
6. Battle continues with new pet

### No More Victory Dialog
- Battle does NOT end when switching pets
- Frontend does NOT show victory/loss dialog
- Pet model updates correctly
- Skills update correctly
- Battle continues normally

## Testing

1. Start a battle with multiple pets
2. Click the pet switch button during your turn
3. Select a different pet
4. Verify:
   - Pet model changes to the new pet
   - Skills panel updates with new pet's skills
   - HP bar shows new pet's HP
   - Battle continues (no victory dialog)
   - Can use skills with the new pet
5. Repeat switching between pets
6. Verify battle only ends when:
   - Enemy HP = 0 (you win)
   - All your pets HP = 0 (you lose)

## Files Modified

- `golang_version/internal/handlers/handlers.go` (lines 4843-4855)
  - Removed CMD 2504 sending
  - Removed buildFightPetInfo function
  - Removed 100ms delay
  - Simplified logging

## Related Issues

- Pet switching showing victory dialog
- Battle ending incorrectly during pet switch
- Frontend stuck after switching pets
- "还是切换不了了" (Still can't switch)

## Technical Notes

### CMD 2504 Usage
- **Correct usage**: Sent once at battle start (CMD 2408 handler)
- **Incorrect usage**: Sent during pet switch (causes battle to restart/end)

### Frontend Pet Switch Flow
From `PlayerMode.as`:
```actionscript
override public function changePet(param1:ChangePetInfo) : void
{
   super.changePet(param1);  // Updates pet model
   this.conPanelObserver.changePet();  // Updates skill panel
   this.subject.showFightPanel();  // Shows fight panel
   if(PetFightEntry.isAutoSelectPet)
   {
      this.subject.openPanel();  // Opens panel (for forced switch)
   }
   else
   {
      this.subject.closePanel();  // Closes panel (for manual switch)
   }
}
```

The frontend handles everything internally when it receives CMD 2407. No need for CMD 2504.

## Date

2026-02-03

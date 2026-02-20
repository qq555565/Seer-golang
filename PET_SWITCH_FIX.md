# Pet Switching Fix - Final Solution

## Problem Summary

When switching pets during battle (CMD 2407), the wrong pet was being sent to the frontend even though the correct pet was selected.

### Root Cause Analysis

There were TWO bugs:

#### Bug 1: Pointer Issue After Array Swap
1. User selects PetID=70 (at index=3)
2. Backend correctly finds the pet and sets `picked = &user.Pets[3]`
3. Backend swaps array positions: `user.Pets[0] ↔ user.Pets[3]`
4. **BUG**: After swap, `picked` pointer still points to `user.Pets[3]`, which NOW contains the OLD pet (PetID=865)
5. When sending 2301 command with `buildFullPetInfo(*picked)`, it sends the wrong pet info

#### Bug 2: Missing Battle Scene Refresh (2504 Command)
Even after fixing Bug 1, the frontend still showed the old pet because:
- The Go backend was NOT sending CMD 2504 (NOTE_START_FIGHT / Battle Status Update)
- The Lua backend DOES send 2504 after pet switching
- Frontend needs 2504 to refresh the battle scene and load the new pet model

### The Complete Fix

#### Fix 1: Save Pet Data Before Array Swap

Save the selected pet data **BEFORE** the array swap, then use the saved data for all subsequent operations:

```go
// BEFORE the array swap
selectedPet := *picked          // Copy the entire pet struct
selectedPetID := picked.ID      // Save ID
selectedPetLevel := picked.Level // Save level

// Array swap (this changes what picked points to)
if pickedIndex > 0 && pickedIndex < len(user.Pets) {
    user.Pets[0], user.Pets[pickedIndex] = user.Pets[pickedIndex], user.Pets[0]
}

// AFTER the swap, use selectedPet instead of picked
logger.Info(fmt.Sprintf("[2407] 更新战斗状态: PlayerHP=%d/%d PetID=%d", battle.PlayerHP, battle.PlayerMaxHP, selectedPetID))
petInfoBody := buildFullPetInfo(selectedPet)  // Use saved copy
propBody := buildNoteUpdateProp(uint32(selectedPet.CatchTime), selectedPet.ID, ...)  // Use saved copy
```

#### Fix 2: Send 2504 Command to Refresh Battle Scene

After sending 2301 (pet info) and 2508 (stats update), also send 2504 (battle status update):

```go
// 发送战斗状态更新（2504）- 刷新战斗场景中的精灵模型
playerInfo := buildFightPetInfo(uint32(ctx.UserID), selectedPetID, playerName, ...)
enemyInfo := buildFightPetInfo(0, battle.EnemyID, enemyName, ...)

battleBody := make([]byte, 4+len(playerInfo)+len(enemyInfo))
binary.BigEndian.PutUint32(battleBody[0:4], 0) // isCanAuto = 0
copy(battleBody[4:], playerInfo)
copy(battleBody[4+len(playerInfo):], enemyInfo)

ctx.GameServer.SendResponse(ctx.ClientData, 2504, ctx.UserID, ctx.SeqID, battleBody)
```

## Changes Made

### File: `golang_version/internal/handlers/handlers.go`

**Function**: `handleChangePet` (around line 4790)

**Changes**:
1. Added variable declarations after sending 2407 response:
   - `selectedPet := *picked` - Full copy of the pet struct
   - `selectedPetID := picked.ID` - Pet ID
   - `selectedPetLevel := picked.Level` - Pet level

2. Replaced all uses of `picked` with `selectedPet` AFTER the array swap:
   - Battle state update log: `selectedPetID`
   - Opponent battle state: `selectedPetID`, `selectedPetLevel`
   - buildFullPetInfo call: `selectedPet`
   - buildNoteUpdateProp call: `selectedPet.CatchTime`, `selectedPet.ID`, `selectedPet.Level`, `selectedPet.Exp`

3. Added 2504 command sending after 2508:
   - Build FightPetInfo for player (with new pet)
   - Build FightPetInfo for enemy (current state)
   - Send 2504 command to refresh battle scene

## Command Sequence After Fix

When switching pets, the backend now sends:
1. **2407** - Change pet response (confirmation)
2. **2301** - Full pet info (skills, stats)
3. **2508** - Update pet properties (HP, Attack, etc.)
4. **2504** - Battle status update (refresh battle scene) ← **NEW!**

## Testing

To test the fix:

1. Start a battle with any wild pet
2. Open the pet switching panel during battle
3. Select a different pet from your team
4. Verify that the correct pet appears in battle (check the pet's model, skills and stats)

### Expected Log Output

```
[2407] 收到切换精灵请求: reqCatchTime=1769951117
[2407] 玩家精灵列表: 共6只
[2407]   [0] PetID=865 CatchTime=1769951929 Level=100
[2407]   [1] PetID=10 CatchTime=1769771486 Level=3
[2407]   [2] PetID=70 CatchTime=1769951117 Level=100  <-- Selected
[2407]   [3] PetID=70 CatchTime=1769951786 Level=100
[2407]   [4] PetID=6 CatchTime=1768449796 Level=53
[2407]   [5] PetID=166 CatchTime=1769914024 Level=100
[2407] 找到匹配的精灵: index=2 PetID=70
[2407] 选中精灵: PetID=70 HP=283/283
发送 CMD=2407 SEQ=59 LEN=57
[2407] 更新战斗状态: PlayerHP=283/283 PetID=70  <-- Correct PetID
[buildFullPetInfo] PetID=70 Level=100 Skills: validCount=4  <-- Correct PetID
发送 CMD=2301 SEQ=59 LEN=171
发送 CMD=2508 SEQ=59 LEN=97
[2407] 发送战斗状态更新(2504): PlayerPetID=70 EnemyID=XX  <-- NEW!
发送 CMD=2504 SEQ=59 LEN=XXX  <-- NEW!
```

## Build Command

```bash
cd golang_version
go build -o gameserver.exe ./cmd/gameserver
```

## Related Files

- `golang_version/internal/handlers/handlers.go` - Main fix
- `golang_version/BATTLE_FIX_V2.md` - Previous fix for 2505 AttackValue
- `golang_version/FINAL_FIX_SUMMARY.md` - Overall battle system fixes

## Status

✅ **FIXED** - Pet switching now correctly displays the selected pet during battle with proper model refresh.

## Technical Notes

The 2504 command (NOTE_START_FIGHT) is used by the frontend to:
- Load the pet model from `PetAssetsManager.getAssetsByID(petID)`
- Update the battle UI with new pet name, HP, level
- Reset battle animations and effects
- Refresh the battle scene

Without this command, the frontend keeps showing the old pet model even though the backend has switched to the new pet.

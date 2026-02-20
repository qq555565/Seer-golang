package handlers

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/seer-game/golang-version/internal/core/logger"
	"github.com/seer-game/golang-version/internal/server/gameserver"
)

const mapIDArena = 102 // 擂台所在地图

// ArenaState 地图102擂台状态，对齐前端 ArenaInfo 解包
type ArenaState struct {
	Flag        uint32 // 0=空 1=有擂主可挑战 2=战斗中
	HostID      uint32
	HostNick    string
	HostWins    uint32
	ChallengerID uint32
}

var (
	arenaState   = &ArenaState{Flag: 0}
	arenaStateMu sync.RWMutex
)

// buildArenaInfoBody 构建 ArenaInfo 包体：flag(4)+hostID(4)+hostNick(16)+hostWins(4)+challengerID(4)=32 bytes
func buildArenaInfoBody(ast *ArenaState) []byte {
	body := make([]byte, 32)
	binary.BigEndian.PutUint32(body[0:4], ast.Flag)
	binary.BigEndian.PutUint32(body[4:8], ast.HostID)
	putFixedString(body, 8, ast.HostNick, 16)
	binary.BigEndian.PutUint32(body[24:28], ast.HostWins)
	binary.BigEndian.PutUint32(body[28:32], ast.ChallengerID)
	return body
}

// broadcastArenaInfo 向地图102所有玩家广播 ARENA_GET_INFO(2419)
func broadcastArenaInfo(gs *gameserver.GameServer) {
	arenaStateMu.RLock()
	body := buildArenaInfoBody(arenaState)
	arenaStateMu.RUnlock()
	gs.BroadcastToMap(mapIDArena, 0, 2419, body)
}

// handleArenaGetInfo CMD 2419 获取擂台信息
// 响应：ArenaInfo = flag(4)+hostID(4)+hostNick(16)+hostWins(4)+challengerID(4)
func handleArenaGetInfo(ctx *gameserver.HandlerContext) {
	arenaStateMu.RLock()
	body := buildArenaInfoBody(arenaState)
	arenaStateMu.RUnlock()
	ctx.GameServer.SendResponse(ctx.ClientData, 2419, ctx.UserID, ctx.SeqID, body)
	logger.Info(fmt.Sprintf("[2419] 擂台信息: UID=%d flag=%d hostID=%d hostWins=%d challengerID=%d",
		ctx.UserID, arenaState.Flag, arenaState.HostID, arenaState.HostWins, arenaState.ChallengerID))
}

// handleArenaSetOwner CMD 2417 占擂（擂台为空时成为擂主）
// 客户端在 flag==0 时发送，无包体
func handleArenaSetOwner(ctx *gameserver.HandlerContext) {
	user := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	if user.MapID != mapIDArena {
		body := make([]byte, 4)
		binary.BigEndian.PutUint32(body[0:4], 1)
		ctx.GameServer.SendResponse(ctx.ClientData, 2417, ctx.UserID, ctx.SeqID, body)
		return
	}
	nick := user.Nick
	if nick == "" {
		nick = fmt.Sprintf("用户%d", ctx.UserID)
	}
	arenaStateMu.Lock()
	if arenaState.Flag != 0 {
		arenaStateMu.Unlock()
		body := make([]byte, 4)
		binary.BigEndian.PutUint32(body[0:4], 1) // 失败
		ctx.GameServer.SendResponse(ctx.ClientData, 2417, ctx.UserID, ctx.SeqID, body)
		return
	}
	arenaState.Flag = 1
	arenaState.HostID = uint32(ctx.UserID)
	arenaState.HostNick = nick
	arenaState.HostWins = 1
	arenaState.ChallengerID = 0
	arenaStateMu.Unlock()
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], 0) // 成功
	ctx.GameServer.SendResponse(ctx.ClientData, 2417, ctx.UserID, ctx.SeqID, body)
	if user.MaxArenaWins < 1 {
		user.MaxArenaWins = 1
	}
	broadcastArenaInfo(ctx.GameServer)
	logger.Info(fmt.Sprintf("[2417] 占擂: UID=%d 成为擂主", ctx.UserID))
}

// handleArenaFightOwner CMD 2418 挑战擂主
// 客户端在 flag==1 时发送，服务端向擂主推送 2501 对战邀请
func handleArenaFightOwner(ctx *gameserver.HandlerContext) {
	arenaStateMu.Lock()
	if arenaState.Flag != 1 || arenaState.HostID == 0 {
		arenaStateMu.Unlock()
		body := make([]byte, 4)
		binary.BigEndian.PutUint32(body[0:4], 1)
		ctx.GameServer.SendResponse(ctx.ClientData, 2418, ctx.UserID, ctx.SeqID, body)
		return
	}
	hostID := int64(arenaState.HostID)
	if hostID == ctx.UserID {
		arenaStateMu.Unlock()
		body := make([]byte, 4)
		binary.BigEndian.PutUint32(body[0:4], 1)
		ctx.GameServer.SendResponse(ctx.ClientData, 2418, ctx.UserID, ctx.SeqID, body)
		return
	}
	arenaState.ChallengerID = uint32(ctx.UserID)
	arenaState.Flag = 2
	arenaStateMu.Unlock()
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2418, ctx.UserID, ctx.SeqID, body)
	challenger := ctx.GameServer.GetOrCreateUser(ctx.UserID)
	nick := challenger.Nick
	if nick == "" {
		nick = fmt.Sprintf("用户%d", ctx.UserID)
	}
	noteBody := make([]byte, 4+16+4)
	binary.BigEndian.PutUint32(noteBody[0:4], uint32(ctx.UserID))
	putFixedString(noteBody, 4, nick, 16)
	binary.BigEndian.PutUint32(noteBody[20:24], 1) // mode 1=单挑
	hostClient := ctx.GameServer.GetClientByUserID(hostID)
	if hostClient != nil {
		ctx.GameServer.SendResponse(hostClient, 2501, ctx.UserID, 0, noteBody)
		logger.Info(fmt.Sprintf("[2418] 挑战擂主: challenger=%d -> host=%d 已推送2501", ctx.UserID, hostID))
	}
	broadcastArenaInfo(ctx.GameServer)
}

// handleArenaUpFight CMD 2420 放弃挑战（挑战者或擂主均可发送）
// 前端仅在 flag==0 时调用 setArenaEmpty 将角色移下擂台，故挑战者放弃时需先发 flag=0 再发 flag=1 恢复擂主
func handleArenaUpFight(ctx *gameserver.HandlerContext) {
	arenaStateMu.Lock()
	challengerGaveUp := arenaState.ChallengerID == uint32(ctx.UserID)
	if challengerGaveUp {
		arenaState.ChallengerID = 0
		if arenaState.HostID != 0 {
			arenaState.Flag = 1
		} else {
			arenaState.Flag = 0
		}
	} else if arenaState.HostID == uint32(ctx.UserID) {
		// 擂主放弃（点击放弃挑战）
		if arenaState.ChallengerID != 0 {
			arenaState.HostID = arenaState.ChallengerID
			arenaState.ChallengerID = 0
			arenaState.Flag = 1
			u := ctx.GameServer.GetOrCreateUser(int64(arenaState.HostID))
			arenaState.HostNick = u.Nick
			if arenaState.HostNick == "" {
				arenaState.HostNick = fmt.Sprintf("用户%d", arenaState.HostID)
			}
			arenaState.HostWins = 1
		} else {
			arenaState.Flag = 0
			arenaState.HostID = 0
			arenaState.HostNick = ""
			arenaState.HostWins = 0
		}
	}
	arenaStateMu.Unlock()
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2420, ctx.UserID, ctx.SeqID, body)
	// 挑战者放弃时，先广播 flag=0 触发 setArenaEmpty（让挑战者下擂台），再广播实际状态
	if challengerGaveUp {
		emptyBody := make([]byte, 32)
		binary.BigEndian.PutUint32(emptyBody[0:4], 0)
		ctx.GameServer.BroadcastToMap(mapIDArena, 0, 2419, emptyBody)
	}
	broadcastArenaInfo(ctx.GameServer)
	logger.Info(fmt.Sprintf("[2420] 放弃挑战: UID=%d", ctx.UserID))
}

// handleArenaOwnerAcce CMD 2422 战斗结束确认（胜者发送）
// 前端在战斗胜利后发送，用于更新擂主、连胜等
func handleArenaOwnerAcce(ctx *gameserver.HandlerContext) {
	arenaStateMu.Lock()
	winnerID := int64(ctx.UserID)
	hostID := int64(arenaState.HostID)
	challengerID := int64(arenaState.ChallengerID)
	if arenaState.Flag != 2 {
		arenaStateMu.Unlock()
		body := make([]byte, 4)
		binary.BigEndian.PutUint32(body[0:4], 0)
		ctx.GameServer.SendResponse(ctx.ClientData, 2422, ctx.UserID, ctx.SeqID, body)
		return
	}
	user := ctx.GameServer.GetOrCreateUser(winnerID)
	nick := user.Nick
	if nick == "" {
		nick = fmt.Sprintf("用户%d", winnerID)
	}
	if winnerID == hostID {
		arenaState.HostWins++
		arenaState.ChallengerID = 0
		arenaState.Flag = 1
		if user.MaxArenaWins < int(arenaState.HostWins) {
			user.MaxArenaWins = int(arenaState.HostWins)
		}
		logger.Info(fmt.Sprintf("[2422] 擂主守擂成功: host=%d wins=%d", winnerID, arenaState.HostWins))
	} else if winnerID == challengerID {
		arenaState.HostID = uint32(challengerID)
		arenaState.HostNick = nick
		arenaState.HostWins = 1
		arenaState.ChallengerID = 0
		arenaState.Flag = 1
		if user.MaxArenaWins < 1 {
			user.MaxArenaWins = 1
		}
		logger.Info(fmt.Sprintf("[2422] 挑战者夺擂成功: newHost=%d", challengerID))
	} else {
		arenaStateMu.Unlock()
		body := make([]byte, 4)
		binary.BigEndian.PutUint32(body[0:4], 0)
		ctx.GameServer.SendResponse(ctx.ClientData, 2422, ctx.UserID, ctx.SeqID, body)
		return
	}
	arenaStateMu.Unlock()
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2422, ctx.UserID, ctx.SeqID, body)
	broadcastArenaInfo(ctx.GameServer)
}

// handleArenaOwnerOut CMD 2423 擂主失位（服务端广播，如超时未应战）
// 客户端也会发此 CMD 请求？目前按 stub 逻辑：请求时向同地图广播 2423 空包
func handleArenaOwnerOut(ctx *gameserver.HandlerContext) {
	arenaStateMu.Lock()
	cleared := false
	if arenaState.HostID == uint32(ctx.UserID) {
		arenaState.Flag = 0
		arenaState.HostID = 0
		arenaState.HostNick = ""
		arenaState.HostWins = 0
		arenaState.ChallengerID = 0
		cleared = true
	}
	arenaStateMu.Unlock()
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body[0:4], 0)
	ctx.GameServer.SendResponse(ctx.ClientData, 2423, ctx.UserID, ctx.SeqID, body)
	if cleared {
		ctx.GameServer.BroadcastToMap(mapIDArena, ctx.UserID, 2423, body)
		broadcastArenaInfo(ctx.GameServer)
		logger.Info(fmt.Sprintf("[2423] 擂主失位: UID=%d", ctx.UserID))
	}
}

// OnArenaFightInviteRefused 当擂主拒绝对战邀请时调用（2403 result=0）
func OnArenaFightInviteRefused(gs *gameserver.GameServer, hostID, inviterUserID int64) {
	arenaStateMu.Lock()
	if arenaState.HostID == uint32(hostID) && arenaState.ChallengerID == uint32(inviterUserID) {
		arenaState.ChallengerID = 0
		arenaState.Flag = 1
		arenaStateMu.Unlock()
		broadcastArenaInfo(gs)
		logger.Info(fmt.Sprintf("[擂台] 擂主拒战: host=%d challenger=%d", hostID, inviterUserID))
	} else {
		arenaStateMu.Unlock()
	}
}

// OnArenaFightInviteAccepted 当擂主接受对战邀请时调用（2403 result=1），用于确保 flag=2
func OnArenaFightInviteAccepted(gs *gameserver.GameServer, hostID, challengerID int64) {
	arenaStateMu.Lock()
	if arenaState.HostID == uint32(hostID) && arenaState.ChallengerID == uint32(challengerID) {
		arenaState.Flag = 2
	}
	arenaStateMu.Unlock()
}

// OnArenaHostLeave 当擂主离开地图102时调用（断线/换图），清空擂主并广播 2423
func OnArenaHostLeave(gs *gameserver.GameServer, userID int64) {
	arenaStateMu.Lock()
	if arenaState.HostID == uint32(userID) {
		arenaState.Flag = 0
		arenaState.HostID = 0
		arenaState.HostNick = ""
		arenaState.HostWins = 0
		arenaState.ChallengerID = 0
		arenaStateMu.Unlock()
		body := make([]byte, 4)
		binary.BigEndian.PutUint32(body[0:4], 0)
		gs.BroadcastToMap(mapIDArena, 0, 2423, body)
		broadcastArenaInfo(gs)
		logger.Info(fmt.Sprintf("[擂台] 擂主离开: UID=%d", userID))
	} else {
		arenaStateMu.Unlock()
	}
}

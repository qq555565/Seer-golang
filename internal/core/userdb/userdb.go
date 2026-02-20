package userdb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/seer-game/golang-version/internal/game/pets"
)

// Config 数据库配置
type Config struct {
	LocalServerMode bool
	UseMySQL        bool
	MySQLConfig     MySQLConfig
	// DBPath 指定 users.json 路径；支持相对路径（相对当前工作目录）。
	// 为空时默认使用当前工作目录下的 users.json（避免 go run 时落到临时目录）。
	DBPath string
	// OfflineConfigDir 未连接数据库时将 GM 配置写入此目录；下次启动若能连上数据库则从此目录同步到 DB。
	OfflineConfigDir string
}

// MySQLConfig MySQL配置
type MySQLConfig struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
}

// User 用户账号数据
type User struct {
	UserID       int64  `json:"userId"`
	Email        string `json:"email"`
	Password     string `json:"password"`
	Nickname     string `json:"nickname"`
	Color        int    `json:"color"`
	RegisterTime int64  `json:"registerTime"`
	RoleCreated  bool   `json:"roleCreated"`
	Session      string `json:"session,omitempty"`
	SessionHex   string `json:"sessionHex,omitempty"`
}

// GameData 游戏数据
type GameData struct {
	Nick            string                  `json:"nick"`
	Color           int                     `json:"color"`
	Coins           int                     `json:"coins"`
	Gold            int                     `json:"gold"` // 金豆，用于金豆商城购买
	Energy          int                     `json:"energy"`
	Pets            []Pet                   `json:"pets"`
	Items           map[string]Item         `json:"items"`
	Clothes         []int                   `json:"clothes"`
	Tasks           map[string]Task         `json:"tasks"`
	PetBook         map[string]PetBookEntry `json:"petBook"`
	Nono            NonoData                `json:"nono"`
	MapID           int                     `json:"mapId"`
	PosX            int                     `json:"posX"`
	PosY            int                     `json:"posY"`
	Fitments        []Fitment               `json:"fitments"`
	AllFitments     []Fitment               `json:"allFitments"`
	Texture         int                     `json:"texture"`
	DsFlag          int                     `json:"dsFlag"`
	Badge           int                     `json:"badge"`
	CurTitle        int                     `json:"curTitle"`
	FightBadge      int                     `json:"fightBadge"`
	TimeToday       int                     `json:"timeToday"`
	TimeLimit       int                     `json:"timeLimit"`
	LoginCnt        int                     `json:"loginCnt"`
	Inviter         int64                   `json:"inviter"`
	NewInviteeCnt   int                     `json:"newInviteeCnt"`
	TeacherID       int64                   `json:"teacherID"`
	StudentID       int64                   `json:"studentID"`
	GraduationCount int                     `json:"graduationCount"`
	MaxPuniLv       int                     `json:"maxPuniLv"`
	PetMaxLev       int                     `json:"petMaxLev"`
	PetAllNum       int                     `json:"petAllNum"`
	MonKingWin      int                     `json:"monKingWin"`
	MonBtlMedal     int                     `json:"monBtlMedal"`
	MessWin         int                     `json:"messWin"`
	CurStage        int                     `json:"curStage"`
	MaxStage        int                     `json:"maxStage"`
	TowerBossIndex  int                     `json:"towerBossIndex"`  // 勇者之塔当前层已击败的 Boss 数（0/1/2），用于 3 只顺序上场
	CurFreshStage   int                     `json:"curFreshStage"`
	MaxFreshStage   int                     `json:"maxFreshStage"`
	MaxArenaWins    int                     `json:"maxArenaWins"`
	TwoTimes        int                     `json:"twoTimes"`
	ThreeTimes      int                     `json:"threeTimes"`
	AutoFight       int                     `json:"autoFight"`
	AutoFightTimes  int                     `json:"autoFightTimes"`
	EnergyTimes     int                     `json:"energyTimes"`
	LearnTimes      int                     `json:"learnTimes"`
	Achievements    Achievements            `json:"achievements"`
	StoragePets     []Pet                   `json:"storagePets"`
	Friends         []Friend                `json:"friends"`
	ExpPool         int                     `json:"expPool"` // 经验池
	Blacklist       []BlacklistEntry        `json:"blacklist"`
	CurrentServer   int                     `json:"currentServer"`
	LastOnline      int64                   `json:"lastOnline"`
	// DefeatedSPTBossIds 已首次击败的 SPT BOSS 精灵 ID 列表（用于首次击败奖励，如蘑菇怪 47 奖励小蘑菇 46）
	DefeatedSPTBossIds []int                `json:"defeatedSptBossIds,omitempty"`
	// 矿物挖掘/气体收集：每日按 cateId(1-12) 计数，MiningDate 为当日 Unix/86400，跨日清零
	MiningCount map[int]int `json:"miningCount,omitempty"`
	MiningDate  int64       `json:"miningDate,omitempty"`
	// FollowPetCatchTime 当前跟随/展示精灵的 catchTime，用于 2001/2003 的 spiritTime+spiritID 及 2305 广播；0 表示用第一只精灵
	FollowPetCatchTime int `json:"followPetCatchTime,omitempty"`
	// FlyMode 当前飞行状态（0=未飞行 1=飞行），用于 2001/2003 的 actionType，使同图玩家可见飞行状态
	FlyMode int `json:"flyMode,omitempty"`
	// SoulBeads 元神珠列表（精灵融合产物），obtainTime 为唯一 ID，ItemID 为 PetClass（用于孵化时确定精灵类型）
	SoulBeads []SoulBead `json:"soulBeads,omitempty"`
	// SoulBeadBufs 各元神珠的能量吸收进度，obtainTime -> 20 字节 buf（每字节 1=该步已完成），需到对应地区吸取
	SoulBeadBufs []SoulBeadBuf `json:"soulBeadBufs,omitempty"`
	// SoulBeadTransform 当前正在赋形/孵化中的元神珠（最多一个），用于 2356/2357/2358 与登录 1001 的 obtainTm/soulBeadItemID/expireTm
	SoulBeadTransform *SoulBeadTransformState `json:"soulBeadTransform,omitempty"`
}

// SoulBeadTransformState 正在赋形中的元神珠（转化仪中）
type SoulBeadTransformState struct {
	ObtainTime     uint32 `json:"obtainTime"`
	ItemID         uint32 `json:"itemID"`         // 元神珠对应的 PetClass，2356/1001 的 soulBeadItemID
	RewardPetClass uint32 `json:"rewardPetClass"` // 赋形完成时将发放的精灵类型（2357 时随机确定，供客户端动画 swf 与 2358 一致）
	ExpireTime     int64  `json:"expireTime"`     // Unix 秒，赋形完成时间，客户端用于显示剩余孵化时间
}

// SoulBead 元神珠（精灵融合产物）
type SoulBead struct {
	ObtainTime uint32 `json:"obtainTime"`
	ItemID     uint32 `json:"itemID"` // PetClass，用于确定孵化出的精灵类型
}

// SoulBeadBuf 元神珠能量吸收进度（20 步，对应各地区吸取）
type SoulBeadBuf struct {
	ObtainTime uint32 `json:"obtainTime"`
	Buf        []byte `json:"buf"` // 20 字节，每字节 1 表示该步已完成
}

// Pet 精灵数据（GM 需完整展示，学习力/技能/特性不用 omitempty 以便前端显示当前值）
type Pet struct {
	ID        int    `json:"id"`
	CatchTime int    `json:"catchTime"`
	Level     int    `json:"level"`
	DV        int    `json:"dv"`
	Nature    int    `json:"nature"`
	Exp       int    `json:"exp"`
	Name      string `json:"name"`
	EVHP      int    `json:"ev_hp"`
	EVAttack  int    `json:"ev_attack"`
	EVDefence int    `json:"ev_defence"`
	EVSpAtk   int    `json:"ev_sa"`
	EVSpDef   int    `json:"ev_sd"`
	EVSpeed   int    `json:"ev_sp"`
	Skills    []int  `json:"skills"`
	Trait     int    `json:"trait"`
}

// Item 物品数据
type Item struct {
	Count      int `json:"count"`
	ExpireTime int `json:"expireTime,omitempty"`
}

// Task 任务数据
type Task struct {
	Status       string         `json:"status"`
	CompleteTime int64          `json:"completeTime,omitempty"`
	Buf         map[int]int    `json:"buf,omitempty"` // NPC 对话/装置进度，索引 0..19 对应 20 字节，客户端 buf.position=i; readBoolean()
}

// PetBookEntry 精灵图鉴条目
type PetBookEntry struct {
	Encountered int `json:"encountered"`
	Caught      int `json:"caught"`
	Killed      int `json:"killed"`
}

// NonoData NoNo数据
type NonoData struct {
	HasNono       int    `json:"hasNono"`
	Flag          int    `json:"flag"`
	State         int    `json:"state"`
	Nick          string `json:"nick"`
	Color         int    `json:"color"`
	SuperNono     int    `json:"superNono"`
	VipLevel      int    `json:"vipLevel"`
	VipStage      int    `json:"vipStage"`
	VipValue      int    `json:"vipValue"`
	AutoCharge    int    `json:"autoCharge"`
	VipEndTime    int64  `json:"vipEndTime"`
	FreshManBonus int    `json:"freshManBonus"`
	SuperEnergy   int    `json:"superEnergy"`
	SuperLevel    int    `json:"superLevel"`
	SuperStage    int    `json:"superStage"`
	Power         int    `json:"power"`
	Mate          int    `json:"mate"`
	IQ            int    `json:"iq"`
	AI            int    `json:"ai"`
	HP            int    `json:"hp"`
	MaxHP         int    `json:"maxHp"`
	Energy        int    `json:"energy"`
	Birth         int64  `json:"birth"`
	ChargeTime    int    `json:"chargeTime"`
	Expire        int64  `json:"expire"`
	Chip          int    `json:"chip"`
	Grow          int    `json:"grow"`
	IsFollowing   bool   `json:"isFollowing"`
	// Func 芯片功能解锁位图：20 字节共 160 位，位 i 表示物品 700001+i 是否已永久开启（客户端 NonoInfo.func / UserInfo.nonoChipList）
	Func []byte `json:"func,omitempty"`
}

// Fitment 家具数据
type Fitment struct {
	ID     int `json:"id"`
	X      int `json:"x"`
	Y      int `json:"y"`
	Dir    int `json:"dir"`
	Status int `json:"status"`
}

// Achievements 成就数据
type Achievements struct {
	Total int   `json:"total"`
	Rank  int   `json:"rank"`
	List  []int `json:"list"`
}

// Friend 好友数据
type Friend struct {
	UserID   int64 `json:"userID"`
	TimePoke int64 `json:"timePoke"`
	AddTime  int64 `json:"addTime"`
}

// BlacklistEntry 黑名单条目
type BlacklistEntry struct {
	UserID  int64 `json:"userID"`
	AddTime int64 `json:"addTime"`
}

// UserDB 用户数据库
type UserDB struct {
	config   Config
	dbPath   string
	users    map[int64]*User
	gameData map[int64]*GameData
	mu       sync.RWMutex
	loaded   bool
	mysqlDB  *sql.DB // 启用 UseMySQL 时使用，按分类表存取
}

// DBData 数据库文件数据结构
type DBData struct {
	Users    map[string]*User     `json:"users"`
	GameData map[string]*GameData `json:"gameData"`
}

var (
	instance *UserDB
	once     sync.Once
)

// New 创建或获取数据库实例
func New(config Config) *UserDB {
	once.Do(func() {
		instance = &UserDB{
			config:   config,
			users:    make(map[int64]*User),
			gameData: make(map[int64]*GameData),
		}
		instance.init()
	})
	return instance
}

// GetInstance 获取数据库实例
func GetInstance() *UserDB {
	if instance == nil {
		instance = New(Config{
			LocalServerMode: true,
		})
	}
	return instance
}

// init 初始化数据库
func (db *UserDB) init() {
	db.dbPath = resolveDBPath(db.config.DBPath)

	if !db.config.LocalServerMode {
		fmt.Println("[UserDB] 官服模式：跳过数据库加载")
		return
	}
	if db.config.UseMySQL {
		fmt.Printf("[UserDB] 使用 MySQL: %s@%s:%d/%s\n",
			db.config.MySQLConfig.User, db.config.MySQLConfig.Host, db.config.MySQLConfig.Port, db.config.MySQLConfig.Database)
		if err := initMySQL(db); err != nil {
			fmt.Printf("[UserDB] MySQL 初始化失败，回退到文件: %v\n", err)
			db.load()
			return
		}
		mysqlLoad(db)
		syncOfflineConfigToDB(db)
		return
	}
	fmt.Printf("[UserDB] 数据库路径: %s\n", db.dbPath)
	db.load()
}

func resolveDBPath(configPath string) string {
	// 显式配置优先
	if strings.TrimSpace(configPath) != "" {
		p := filepath.Clean(configPath)
		if filepath.IsAbs(p) {
			return p
		}
		if cwd, err := os.Getwd(); err == nil && cwd != "" {
			return filepath.Join(cwd, p)
		}
		return p
	}

	// 默认：当前工作目录 users.json（go run / 双击 exe 都更符合预期）
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		return filepath.Join(cwd, "users.json")
	}

	// 兜底：使用可执行文件目录
	if exe, err := os.Executable(); err == nil && exe != "" {
		return filepath.Join(filepath.Dir(exe), "users.json")
	}

	return "users.json"
}

// load 从文件加载数据
func (db *UserDB) load() {
	db.mu.Lock()
	defer db.mu.Unlock()

	if _, err := os.Stat(db.dbPath); os.IsNotExist(err) {
		fmt.Println("[UserDB] 用户数据库不存在，创建新数据库")
		db.users = make(map[int64]*User)
		db.gameData = make(map[int64]*GameData)
		// 延迟保存，避免死锁
		go func() {
			time.Sleep(100 * time.Millisecond)
			db.save()
		}()
		return
	}

	data, err := os.ReadFile(db.dbPath)
	if err != nil {
		fmt.Printf("[UserDB] 读取数据库文件失败: %v\n", err)
		db.users = make(map[int64]*User)
		db.gameData = make(map[int64]*GameData)
		return
	}

	var dbData DBData
	if err := json.Unmarshal(data, &dbData); err != nil {
		fmt.Printf("[UserDB] 用户数据解析失败: %v\n", err)
		db.users = make(map[int64]*User)
		db.gameData = make(map[int64]*GameData)
		return
	}

	// 转换数据格式
	db.users = make(map[int64]*User)
	for k, v := range dbData.Users {
		var userID int64
		fmt.Sscanf(k, "%d", &userID)
		db.users[userID] = v
	}

	db.gameData = make(map[int64]*GameData)
	for k, v := range dbData.GameData {
		var userID int64
		fmt.Sscanf(k, "%d", &userID)
		db.gameData[userID] = v
	}

	userCount := len(db.users)
	if !db.loaded {
		fmt.Printf("[UserDB] 加载了 %d 个用户\n", userCount)
		db.loaded = true
	}
}

// save 保存数据到文件（使用 MySQL 时不写文件）
func (db *UserDB) save() {
	if !db.config.LocalServerMode || db.mysqlDB != nil {
		return
	}

	db.mu.RLock()
	defer db.mu.RUnlock()

	// 转换数据格式
	dbData := DBData{
		Users:    make(map[string]*User),
		GameData: make(map[string]*GameData),
	}

	for id, user := range db.users {
		dbData.Users[fmt.Sprintf("%d", id)] = user
	}

	for id, data := range db.gameData {
		dbData.GameData[fmt.Sprintf("%d", id)] = data
	}

	data, err := json.MarshalIndent(dbData, "", "  ")
	if err != nil {
		fmt.Printf("[UserDB] 数据序列化失败: %v\n", err)
		return
	}

	if err := os.WriteFile(db.dbPath, data, 0644); err != nil {
		fmt.Printf("[UserDB] 数据写入失败: %v\n", err)
		return
	}

	fmt.Println("[UserDB] 数据已保存到磁盘")
}

// SaveToFile 显式保存到文件（仅本地文件模式生效；MySQL 模式下此方法不写入本地 DBPath）
func (db *UserDB) SaveToFile() {
	db.save()
}

// ExportSnapshotToFile 导出当前内存中的所有账号与游戏数据到指定文件（无论是否使用 MySQL）。
// - 导出结构与 users.json 完全一致（DBData），可直接被 ImportFromFile 复用。
// - 仅依赖内存中的 users/gameData；对于 MySQL 模式，因启动时已从 MySQL 全量加载并在运行中保持同步，
//   因此该快照在逻辑上等价于一次“导出数据库到 JSON 文件”。
func (db *UserDB) ExportSnapshotToFile(path string) error {
	db.mu.RLock()
	defer db.mu.RUnlock()

	dbData := DBData{
		Users:    make(map[string]*User),
		GameData: make(map[string]*GameData),
	}

	for id, user := range db.users {
		if user == nil {
			continue
		}
		dbData.Users[fmt.Sprintf("%d", id)] = user
	}

	for id, data := range db.gameData {
		if data == nil {
			continue
		}
		dbData.GameData[fmt.Sprintf("%d", id)] = data
	}

	bytes, err := json.MarshalIndent(dbData, "", "  ")
	if err != nil {
		fmt.Printf("[UserDB] 导出快照序列化失败: %v\n", err)
		return err
	}

	if err := os.WriteFile(path, bytes, 0644); err != nil {
		fmt.Printf("[UserDB] 导出快照写入失败: %v\n", err)
		return err
	}

	fmt.Printf("[UserDB] 快照已导出到 %s\n", path)
	return nil
}

// FindByEmail 根据邮箱查找用户
func (db *UserDB) FindByEmail(email string) *User {
	db.mu.RLock()
	defer db.mu.RUnlock()

	for _, user := range db.users {
		if user.Email == email {
			return user
		}
	}
	return nil
}

// FindByUserID 根据用户ID查找用户
func (db *UserDB) FindByUserID(userID int64) *User {
	db.mu.RLock()
	defer db.mu.RUnlock()

	return db.users[userID]
}

// SaveUser 保存用户数据
func (db *UserDB) SaveUser(user *User) {
	db.mu.Lock()
	if user != nil && user.UserID > 0 {
		db.users[user.UserID] = user
	}
	db.mu.Unlock()
	if db.mysqlDB != nil && user != nil && user.UserID > 0 {
		if err := saveUserToMySQL(db, user); err != nil {
			fmt.Printf("[UserDB] MySQL 保存 User 失败 userID=%d: %v\n", user.UserID, err)
		}
	}
}

// CreateUser 创建新用户
func (db *UserDB) CreateUser(email, password string) (*User, error) {
	if db.FindByEmail(email) != nil {
		return nil, fmt.Errorf("邮箱已被注册")
	}

	// 生成新的用户ID
	maxID := int64(100000000)
	db.mu.RLock()
	for id := range db.users {
		if id > maxID {
			maxID = id
		}
	}
	db.mu.RUnlock()

	newUserID := maxID + 1

	// 创建用户
	user := &User{
		UserID:       newUserID,
		Email:        email,
		Password:     password,
		Nickname:     fmt.Sprintf("%d", newUserID),
		Color:        0,
		RegisterTime: time.Now().Unix(),
		RoleCreated:  false,
	}

	db.mu.Lock()
	db.users[newUserID] = user
	db.mu.Unlock()
	if db.mysqlDB != nil {
		if err := createUserInMySQL(db, user); err != nil {
			fmt.Printf("[UserDB] MySQL 创建用户失败: %v\n", err)
		}
	}
	fmt.Printf("[UserDB] 创建新用户: %d\n", newUserID)
	return user, nil
}

// calculateSuperNonoTypeByLevel 根据超能等级计算对应的形态（超能等级模型）
// 超能等级最高为12级，超能形态分别是1、4、7、9、12，共五个形态
// 等级1-3：形态1，等级4-6：形态2，等级7-8：形态3，等级9-11：形态4，等级12：形态5
func calculateSuperNonoTypeByLevel(level int) int {
	if level < 1 {
		return 0
	}
	if level >= 12 {
		return 5 // 形态5：等级12
	}
	if level >= 9 {
		return 4 // 形态4：等级9-11
	}
	if level >= 7 {
		return 3 // 形态3：等级7-8
	}
	if level >= 4 {
		return 2 // 形态2：等级4-6
	}
	return 1 // 形态1：等级1-3
}

// GetOrCreateGameData 获取或创建游戏数据
func (db *UserDB) GetOrCreateGameData(userID int64) *GameData {
	db.mu.Lock()
	if data, exists := db.gameData[userID]; exists {
		// 数据迁移：旧 Go 版默认给新用户塞了 1 只精灵 + 4 件服装，导致 CMD1001 长度不一致从而卡在宇宙界面。
		// Lua 版默认是 pets/clothes 为空，这里做一次兼容迁移，保留真实玩家数据不动。
		if len(data.Pets) == 1 && len(data.Clothes) == 4 &&
			data.Pets[0].ID == 1 && data.Pets[0].Level == 1 && data.Pets[0].CatchTime == 0 &&
			data.Clothes[0] == 1 && data.Clothes[1] == 2 && data.Clothes[2] == 3 && data.Clothes[3] == 4 {
			data.Pets = []Pet{}
			data.Clothes = []int{}
		}

		// 旧版默认家具：Lua 配置允许为空，这里也做一次迁移（只对默认占位值生效）
		if len(data.Fitments) == 1 && len(data.AllFitments) == 1 &&
			data.Fitments[0].ID == 500001 && data.AllFitments[0].ID == 500001 &&
			data.Fitments[0].X == 0 && data.Fitments[0].Y == 0 && data.Fitments[0].Dir == 0 && data.Fitments[0].Status == 0 {
			data.Fitments = []Fitment{}
			data.AllFitments = []Fitment{}
		}

		// 数据迁移：修正NoNo HP值
		if data.Nono.HP == 100000 {
			fmt.Printf("[UserDB] 迁移数据: 修正用户 %d 的 NoNo HP 值 (100000 -> 10000)\n", userID)
			data.Nono.HP = 10000
			data.Nono.MaxHP = 10000
		}
		
		// 数据迁移：给没有物品的用户添加默认物品
		if data.Items == nil || len(data.Items) == 0 {
			fmt.Printf("[UserDB] 迁移数据: 给用户 %d 添加默认物品\n", userID)
			data.Items = make(map[string]Item)
			data.Items["300001"] = Item{Count: 5, ExpireTime: 0x057E40}  // 初级体力药剂 x5
			data.Items["300011"] = Item{Count: 3, ExpireTime: 0x057E40}  // 中级体力药剂 x3
		}

		// 始终按超能等级同步形态（等级 1-12 → 形态 1-5），避免旧形态导致 12 级显示 1 级形态
		if data.Nono.SuperLevel > 0 {
			calculatedType := calculateSuperNonoTypeByLevel(data.Nono.SuperLevel)
			if data.Nono.SuperNono != calculatedType {
				fmt.Printf("[UserDB] 迁移数据: 用户 %d 超能NONO形态 按等级同步 (等级=%d, 旧形态=%d -> 形态=%d)\n",
					userID, data.Nono.SuperLevel, data.Nono.SuperNono, calculatedType)
			}
			data.Nono.SuperNono = calculatedType
		}

		// 注意：不再自动添加默认装扮，新手套装应该通过任务85获得
		db.mu.Unlock()
		return data
	}
	// 启用 MySQL 时尝试从库中加载
	if db.mysqlDB != nil {
		db.mu.Unlock()
		data := loadGameDataFromMySQL(db, userID)
		db.mu.Lock()
		if data != nil {
			db.gameData[userID] = data
			db.mu.Unlock()
			return data
		}
	}

	// 创建新的游戏数据
	loginUser := db.users[userID]
	nickname := fmt.Sprintf("%d", userID)
	color := 0x66CCFF

	if loginUser != nil {
		nickname = loginUser.Nickname
		color = loginUser.Color
	}

	// 默认NoNo配置
	nono := NonoData{
		HasNono:       1,
		Flag:          1,
		State:         0,
		Nick:          "NoNo",
		Color:         0xFFFFFF,
		SuperNono:     0,
		VipLevel:      0,
		VipStage:      0,
		VipValue:      0,
		AutoCharge:    0,
		VipEndTime:    0,
		FreshManBonus: 0,
		SuperEnergy:   0,
		SuperLevel:    0,
		SuperStage:    0,
		Power:         10000,
		Mate:          10000,
		IQ:            0,
		AI:            0,
		HP:            10000,
		MaxHP:         10000,
		Energy:        100,
		Birth:         time.Now().Unix(),
		ChargeTime:    500,
		Expire:        0,
		Chip:          0,
		Grow:          0,
		IsFollowing:   false,
	}

	// Lua 版默认：pets/clothes/tasks 为空（避免 CMD1001 长度/结构偏差导致客户端卡住）
	defaultTasks := map[string]Task{}
	defaultClothes := []int{}
	
	// 默认物品：给新用户一些基础物品（治疗药水等）
	defaultItems := make(map[string]Item)
	defaultItems["300001"] = Item{Count: 5, ExpireTime: 0x057E40}  // 初级体力药剂 x5
	defaultItems["300011"] = Item{Count: 3, ExpireTime: 0x057E40}  // 中级体力药剂 x3
	// 注意：新手套装（100027, 100028等）通过任务85获得，不在这里默认添加

	// 创建游戏数据
	gameData := &GameData{
		Nick:            nickname,
		Color:           color,
		Coins:           2000,
		Gold:            9999, // 金豆，方便测试金豆商城
		Energy:          100,
		Pets:            []Pet{},
		Items:           defaultItems,
		Clothes:         defaultClothes,
		Tasks:           defaultTasks,
		PetBook:         make(map[string]PetBookEntry),
		Nono:            nono,
		MapID:           1,
		PosX:            300,
		PosY:            270,
		Fitments:        []Fitment{},
		AllFitments:     []Fitment{},
		Texture:         1,
		DsFlag:          0,
		Badge:           0,
		CurTitle:        0,
		FightBadge:      0,
		TimeToday:       0,
		TimeLimit:       86400,
		LoginCnt:        1,
		Inviter:         0,
		NewInviteeCnt:   0,
		TeacherID:       0,
		StudentID:       0,
		GraduationCount: 0,
		// 谱尼进度：0=未开启，1~7=已解锁的封印数，8=已解锁真身
		MaxPuniLv: 0,
		PetMaxLev:       100,
		PetAllNum:       0,
		MonKingWin:      0,
		MonBtlMedal:     0,
		MessWin:         0,
		CurStage:        0,
		MaxStage:        0,
		TowerBossIndex:  0,
		CurFreshStage:   0,
		MaxFreshStage:   0,
		MaxArenaWins:    0,
		TwoTimes:        0,
		ThreeTimes:      0,
		AutoFight:       0,
		AutoFightTimes:  0,
		EnergyTimes:     0,
		LearnTimes:      0,
		Achievements: Achievements{
			Total: 0,
			Rank:  0,
			List:  []int{},
		},
		StoragePets:   []Pet{},
		Friends:       []Friend{},
		Blacklist:     []BlacklistEntry{},
		CurrentServer: 0,
		LastOnline:    time.Now().Unix(),
	}

	db.gameData[userID] = gameData
	db.mu.Unlock()
	if db.mysqlDB != nil {
		if err := saveGameDataToMySQL(db, userID, gameData); err != nil {
			fmt.Printf("[UserDB] MySQL 保存新 GameData 失败 userID=%d: %v\n", userID, err)
		}
	}
	fmt.Printf("[UserDB] 创建游戏数据: userId=%d\n", userID)
	return gameData
}

// SaveGameData 保存游戏数据
func (db *UserDB) SaveGameData(userID int64, data *GameData) {
	db.mu.Lock()
	db.gameData[userID] = data
	db.mu.Unlock()
	if db.mysqlDB != nil {
		if err := saveGameDataToMySQL(db, userID, data); err != nil {
			fmt.Printf("[UserDB] MySQL 保存 GameData 失败 userID=%d: %v\n", userID, err)
		}
	}
}

// DeleteUser 删除用户账号及其游戏数据（GM 用）
func (db *UserDB) DeleteUser(userID int64) error {
	db.mu.Lock()
	if _, ok := db.users[userID]; !ok {
		db.mu.Unlock()
		return fmt.Errorf("用户不存在")
	}
	delete(db.users, userID)
	delete(db.gameData, userID)
	db.mu.Unlock()
	if db.mysqlDB != nil {
		if err := deleteUserInMySQL(db, userID); err != nil {
			return fmt.Errorf("MySQL 删除失败: %w", err)
		}
	} else {
		db.SaveToFile()
	}
	return nil
}

// UseMySQL 是否已启用 MySQL（GM 等可据此从数据库读写）
func (db *UserDB) UseMySQL() bool {
	return db.mysqlDB != nil
}

// GetMySQLInfo 返回当前 MySQL 连接信息（供 GM 面板展示用，不含密码）
func (db *UserDB) GetMySQLInfo() (host, database string, connected bool) {
	if db.mysqlDB == nil {
		return "", "", false
	}
	return db.config.MySQLConfig.Host, db.config.MySQLConfig.Database, true
}

// GetAllGameData 获取所有游戏数据
func (db *UserDB) GetAllGameData() map[int64]*GameData {
	db.mu.RLock()
	defer db.mu.RUnlock()

	// 创建副本以避免并发问题
	gameDataMap := make(map[int64]*GameData, len(db.gameData))
	for k, v := range db.gameData {
		gameDataMap[k] = v
	}

	return gameDataMap
}

// UpdateUserCoins 更新用户赛尔豆
func (db *UserDB) UpdateUserCoins(userID int64, coins int) bool {
	data := db.GetOrCreateGameData(userID)
	data.Coins = coins
	db.SaveGameData(userID, data)
	return true
}

// ConsumeCoins 消耗赛尔豆
func (db *UserDB) ConsumeCoins(userID int64, amount int) (bool, int) {
	data := db.GetOrCreateGameData(userID)
	if data.Coins >= amount {
		data.Coins -= amount
		db.SaveGameData(userID, data)
		return true, data.Coins
	}
	return false, data.Coins
}

// AddItem 添加物品
func (db *UserDB) AddItem(userID int64, itemID int, count int) bool {
	data := db.GetOrCreateGameData(userID)
	if data.Items == nil {
		data.Items = make(map[string]Item)
	}

	key := fmt.Sprintf("%d", itemID)
	item, exists := data.Items[key]
	if exists {
		item.Count += count
	} else {
		item = Item{Count: count}
	}
	data.Items[key] = item
	db.SaveGameData(userID, data)

	// 检查任务
	db.checkItemTasks(userID, itemID)

	return true
}

// RemoveItem 移除物品
func (db *UserDB) RemoveItem(userID int64, itemID int, count int) bool {
	data := db.GetOrCreateGameData(userID)
	if data.Items == nil {
		return false
	}

	key := fmt.Sprintf("%d", itemID)
	item, exists := data.Items[key]
	if !exists {
		return false
	}

	item.Count -= count
	if item.Count <= 0 {
		delete(data.Items, key)
	} else {
		data.Items[key] = item
	}
	db.SaveGameData(userID, data)
	return true
}

// GetItemCount 获取物品数量
func (db *UserDB) GetItemCount(userID int64, itemID int) int {
	data := db.GetOrCreateGameData(userID)
	if data.Items == nil {
		return 0
	}

	key := fmt.Sprintf("%d", itemID)
	item, exists := data.Items[key]
	if !exists {
		return 0
	}
	return item.Count
}

// GetItemList 获取物品列表
func (db *UserDB) GetItemList(userID int64) []map[string]interface{} {
	data := db.GetOrCreateGameData(userID)
	list := []map[string]interface{}{}

	if data.Items != nil {
		for itemIDStr, item := range data.Items {
			itemID := 0
			fmt.Sscanf(itemIDStr, "%d", &itemID)
			list = append(list, map[string]interface{}{
				"itemId":     itemID,
				"count":      item.Count,
				"expireTime": item.ExpireTime,
			})
		}
	}

	return list
}

// GetEVStats 从 Pet 结构体中提取 EVStats（用于属性计算）
func (p *Pet) GetEVStats() pets.EVStats {
	return pets.EVStats{
		HP:    p.EVHP,
		Atk:   p.EVAttack,
		Def:   p.EVDefence,
		SpAtk: p.EVSpAtk,
		SpDef: p.EVSpDef,
		Spd:   p.EVSpeed,
	}
}

// 内部常量：可用特性 NewSeIdx 范围（见 skill_effects.xml / PetEffectXMLInfo.xml）
const (
	traitMinIdx = 1006
	traitMaxIdx = 1045
)

// assignFusionTraitIfNeeded 在单个精灵上补齐特性：
// 仅当：
//   - p 非空且 ID>0
//   - 当前 Trait==0（尚未分配）
// 时，才会基于 catchTime+ID 生成一个稳定的随机特性。
// 注意：不再限制为“融合精灵”，所有精灵都可以拥有特性。
func assignFusionTraitIfNeeded(p *Pet) {
	if p == nil || p.ID <= 0 {
		return
	}
	if p.Trait != 0 {
		return
	}

	// 使用 CatchTime+ID 作为随机种子，保证同一只精灵在所有节点上特性稳定且可复现。
	seed := int64(p.CatchTime)<<32 | int64(p.ID)
	r := rand.New(rand.NewSource(seed))
	if traitMaxIdx <= traitMinIdx {
		return
	}
	p.Trait = traitMinIdx + r.Intn(traitMaxIdx-traitMinIdx+1)
}

// AssignFusionTraitIfNeeded 对外导出版本，供其它包在创建新精灵时复用统一逻辑。
func AssignFusionTraitIfNeeded(p *Pet) {
	assignFusionTraitIfNeeded(p)
}

// AddPet 添加精灵
func (db *UserDB) AddPet(userID int64, petID, catchTime, level, dv, nature int) *Pet {
	data := db.GetOrCreateGameData(userID)

	pet := &Pet{
		ID:        petID,
		CatchTime: catchTime,
		Level:     level,
		DV:        dv,
		Nature:    nature,
		Exp:       0,
		Name:      "",
		// EV 默认为 0
		EVHP:      0,
		EVAttack:  0,
		EVDefence: 0,
		EVSpAtk:   0,
		EVSpDef:   0,
		EVSpeed:   0,
	}

	data.Pets = append(data.Pets, *pet)
	db.RecordCatch(userID, petID)
	db.SaveGameData(userID, data)

	fmt.Printf("[UserDB] 添加精灵: userId=%d, petId=%d, catchTime=0x%08X\n", userID, petID, catchTime)
	return pet
}

// GetPets 获取精灵列表
func (db *UserDB) GetPets(userID int64) []Pet {
	data := db.GetOrCreateGameData(userID)
	return data.Pets
}

// GetPetByCatchTime 根据捕获时间获取精灵
func (db *UserDB) GetPetByCatchTime(userID int64, catchTime int) *Pet {
	data := db.GetOrCreateGameData(userID)
	for i := range data.Pets {
		if data.Pets[i].CatchTime == catchTime {
			return &data.Pets[i]
		}
	}
	return nil
}

// intFromUpdate 从 updates 中取 int（兼容 JSON 解析为 float64）
func intFromUpdate(updates map[string]interface{}, key string) (int, bool) {
	v, ok := updates[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	}
	return 0, false
}

// intSliceFromUpdate 从 updates 中取 []int（兼容 JSON 数组为 []interface{} 且元素为 float64）
func intSliceFromUpdate(updates map[string]interface{}, key string) ([]int, bool) {
	v, ok := updates[key]
	if !ok {
		return nil, false
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil, false
	}
	out := make([]int, 0, len(arr))
	for _, x := range arr {
		switch n := x.(type) {
		case int:
			out = append(out, n)
		case float64:
			out = append(out, int(n))
		}
	}
	return out, true
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// UpdatePet 更新精灵数据（支持 id/level/exp/name/dv/nature/trait/skills/学习力 EV）
func (db *UserDB) UpdatePet(userID int64, catchTime int, updates map[string]interface{}) *Pet {
	data := db.GetOrCreateGameData(userID)
	for i := range data.Pets {
		if data.Pets[i].CatchTime == catchTime {
			p := &data.Pets[i]
			if name, ok := updates["name"].(string); ok {
				p.Name = name
			}
			if level, ok := intFromUpdate(updates, "level"); ok {
				p.Level = clampInt(level, 1, 100)
			}
			if exp, ok := intFromUpdate(updates, "exp"); ok {
				if exp < 0 {
					exp = 0
				}
				p.Exp = exp
			}
			if id, ok := intFromUpdate(updates, "id"); ok && id > 0 {
				p.ID = id
			}
			if dv, ok := intFromUpdate(updates, "dv"); ok {
				p.DV = clampInt(dv, 0, 31)
			}
			if nature, ok := intFromUpdate(updates, "nature"); ok {
				p.Nature = clampInt(nature, 0, 24)
			}
			if trait, ok := intFromUpdate(updates, "trait"); ok && trait >= 0 {
				p.Trait = trait
			}
			if skills, ok := intSliceFromUpdate(updates, "skills"); ok {
				if len(skills) > 4 {
					skills = skills[:4]
				}
				p.Skills = skills
			}
			if v, ok := intFromUpdate(updates, "ev_hp"); ok && v >= 0 {
				p.EVHP = v
			}
			if v, ok := intFromUpdate(updates, "ev_attack"); ok && v >= 0 {
				p.EVAttack = v
			}
			if v, ok := intFromUpdate(updates, "ev_defence"); ok && v >= 0 {
				p.EVDefence = v
			}
			if v, ok := intFromUpdate(updates, "ev_sa"); ok && v >= 0 {
				p.EVSpAtk = v
			}
			if v, ok := intFromUpdate(updates, "ev_sd"); ok && v >= 0 {
				p.EVSpDef = v
			}
			if v, ok := intFromUpdate(updates, "ev_sp"); ok && v >= 0 {
				p.EVSpeed = v
			}
			db.SaveGameData(userID, data)
			return p
		}
	}
	return nil
}

// RemovePet 移除精灵
func (db *UserDB) RemovePet(userID int64, catchTime int) bool {
	data := db.GetOrCreateGameData(userID)
	for i := range data.Pets {
		if data.Pets[i].CatchTime == catchTime {
			data.Pets = append(data.Pets[:i], data.Pets[i+1:]...)
			db.SaveGameData(userID, data)
			return true
		}
	}
	return false
}

// RecordEncounter 记录精灵 encounter
func (db *UserDB) RecordEncounter(userID int64, petID int) *PetBookEntry {
	data := db.GetOrCreateGameData(userID)
	if data.PetBook == nil {
		data.PetBook = make(map[string]PetBookEntry)
	}

	key := fmt.Sprintf("%d", petID)
	entry, exists := data.PetBook[key]
	if !exists {
		entry = PetBookEntry{Encountered: 0, Caught: 0, Killed: 0}
	}
	entry.Encountered++
	data.PetBook[key] = entry
	db.SaveGameData(userID, data)
	return &entry
}

// RecordKill 记录精灵击杀
func (db *UserDB) RecordKill(userID int64, petID int) *PetBookEntry {
	data := db.GetOrCreateGameData(userID)
	if data.PetBook == nil {
		data.PetBook = make(map[string]PetBookEntry)
	}

	key := fmt.Sprintf("%d", petID)
	entry, exists := data.PetBook[key]
	if !exists {
		entry = PetBookEntry{Encountered: 1, Caught: 0, Killed: 0}
	}
	entry.Killed++
	data.PetBook[key] = entry
	db.SaveGameData(userID, data)
	return &entry
}

// RecordCatch 记录精灵捕获
func (db *UserDB) RecordCatch(userID int64, petID int) *PetBookEntry {
	data := db.GetOrCreateGameData(userID)
	if data.PetBook == nil {
		data.PetBook = make(map[string]PetBookEntry)
	}

	key := fmt.Sprintf("%d", petID)
	entry, exists := data.PetBook[key]
	if !exists {
		entry = PetBookEntry{Encountered: 1, Caught: 0, Killed: 0}
	}
	entry.Caught = 1
	data.PetBook[key] = entry
	db.SaveGameData(userID, data)
	return &entry
}

// AddFriend 添加好友
func (db *UserDB) AddFriend(userID, friendID int64) (bool, string) {
	data := db.GetOrCreateGameData(userID)

	// 检查是否已经是好友
	for _, friend := range data.Friends {
		if friend.UserID == friendID {
			return false, "已经是好友"
		}
	}

	friend := Friend{
		UserID:   friendID,
		TimePoke: 0,
		AddTime:  time.Now().Unix(),
	}

	data.Friends = append(data.Friends, friend)
	db.SaveGameData(userID, data)

	fmt.Printf("[UserDB] 添加好友: userId=%d, friendId=%d\n", userID, friendID)
	return true, ""
}

// RemoveFriend 删除好友
func (db *UserDB) RemoveFriend(userID, friendID int64) bool {
	data := db.GetOrCreateGameData(userID)

	for i, friend := range data.Friends {
		if friend.UserID == friendID {
			data.Friends = append(data.Friends[:i], data.Friends[i+1:]...)
			db.SaveGameData(userID, data)
			fmt.Printf("[UserDB] 删除好友: userId=%d, friendId=%d\n", userID, friendID)
			return true
		}
	}
	return false
}

// GetFriends 获取好友列表
func (db *UserDB) GetFriends(userID int64) []Friend {
	data := db.GetOrCreateGameData(userID)
	return data.Friends
}

// IsFriend 检查是否是好友
func (db *UserDB) IsFriend(userID, friendID int64) bool {
	friends := db.GetFriends(userID)
	for _, friend := range friends {
		if friend.UserID == friendID {
			return true
		}
	}
	return false
}

// UpdatePoke 更新戳一戳时间
func (db *UserDB) UpdatePoke(userID, friendID int64) bool {
	data := db.GetOrCreateGameData(userID)

	for i, friend := range data.Friends {
		if friend.UserID == friendID {
			data.Friends[i].TimePoke = time.Now().Unix()
			db.SaveGameData(userID, data)
			return true
		}
	}
	return false
}

// AddBlacklist 添加黑名单
func (db *UserDB) AddBlacklist(userID, targetID int64) (bool, string) {
	data := db.GetOrCreateGameData(userID)

	// 检查是否已在黑名单
	for _, black := range data.Blacklist {
		if black.UserID == targetID {
			return false, "已在黑名单"
		}
	}

	// 如果是好友，先删除好友关系
	db.RemoveFriend(userID, targetID)

	black := BlacklistEntry{
		UserID:  targetID,
		AddTime: time.Now().Unix(),
	}

	data.Blacklist = append(data.Blacklist, black)
	db.SaveGameData(userID, data)

	fmt.Printf("[UserDB] 添加黑名单: userId=%d, targetId=%d\n", userID, targetID)
	return true, ""
}

// RemoveBlacklist 移除黑名单
func (db *UserDB) RemoveBlacklist(userID, targetID int64) bool {
	data := db.GetOrCreateGameData(userID)

	for i, black := range data.Blacklist {
		if black.UserID == targetID {
			data.Blacklist = append(data.Blacklist[:i], data.Blacklist[i+1:]...)
			db.SaveGameData(userID, data)
			fmt.Printf("[UserDB] 移除黑名单: userId=%d, targetId=%d\n", userID, targetID)
			return true
		}
	}
	return false
}

// GetBlacklist 获取黑名单
func (db *UserDB) GetBlacklist(userID int64) []BlacklistEntry {
	data := db.GetOrCreateGameData(userID)
	return data.Blacklist
}

// IsBlacklisted 检查是否在黑名单
func (db *UserDB) IsBlacklisted(userID, targetID int64) bool {
	blacklist := db.GetBlacklist(userID)
	for _, black := range blacklist {
		if black.UserID == targetID {
			return true
		}
	}
	return false
}

// SetUserServer 设置用户当前服务器
func (db *UserDB) SetUserServer(userID int64, serverID int) {
	data := db.GetOrCreateGameData(userID)
	data.CurrentServer = serverID
	data.LastOnline = time.Now().Unix()
	db.SaveGameData(userID, data)
}

// GetUserServer 获取用户当前服务器
func (db *UserDB) GetUserServer(userID int64) int {
	data := db.GetOrCreateGameData(userID)
	return data.CurrentServer
}

// SetUserOffline 设置用户离线
func (db *UserDB) SetUserOffline(userID int64) {
	data := db.GetOrCreateGameData(userID)
	data.CurrentServer = 0
	data.LastOnline = time.Now().Unix()
	db.SaveGameData(userID, data)
}

// GetFriendsOnServers 获取好友在各服务器的数量
func (db *UserDB) GetFriendsOnServers(userID int64) map[int]int {
	friends := db.GetFriends(userID)
	serverCounts := make(map[int]int)

	for _, friend := range friends {
		serverID := db.GetUserServer(friend.UserID)
		if serverID > 0 {
			serverCounts[serverID]++
		}
	}

	return serverCounts
}

// GetNonoData 获取NoNo数据
func (db *UserDB) GetNonoData(userID int64) NonoData {
	data := db.GetOrCreateGameData(userID)
	return data.Nono
}

// UpdateNonoData 更新NoNo数据
func (db *UserDB) UpdateNonoData(userID int64, nonoData NonoData) NonoData {
	data := db.GetOrCreateGameData(userID)
	data.Nono = nonoData
	db.SaveGameData(userID, data)
	return data.Nono
}

// GetStoragePets 获取精灵仓库
func (db *UserDB) GetStoragePets(userID int64) []Pet {
	data := db.GetOrCreateGameData(userID)
	return data.StoragePets
}

// AddStoragePet 添加精灵到仓库
func (db *UserDB) AddStoragePet(userID int64, pet Pet) bool {
	data := db.GetOrCreateGameData(userID)
	data.StoragePets = append(data.StoragePets, pet)
	db.SaveGameData(userID, data)
	return true
}

// checkItemTasks 检查物品相关任务
func (db *UserDB) checkItemTasks(userID int64, itemID int) {
	data := db.GetOrCreateGameData(userID)
	if data.Tasks == nil {
		return
	}

	// 这里可以添加任务检查逻辑
	// 例如检查是否有需要该物品的任务
}

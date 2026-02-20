// Package userdb MySQL 存储：用户数据按分类存 accounts / game_players / game_pets / game_items，供 GM 与游戏服共用
package userdb

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	_ "github.com/go-sql-driver/mysql"
)

const (
	mysqlDriverName = "mysql"
)

// initMySQL 连接 MySQL 并创建表（accounts, game_players, game_pets, game_items）
func initMySQL(db *UserDB) error {
	cfg := db.config.MySQLConfig
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=true",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database)
	conn, err := sql.Open(mysqlDriverName, dsn)
	if err != nil {
		return fmt.Errorf("打开 MySQL 连接失败: %w", err)
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		return fmt.Errorf("MySQL Ping 失败: %w", err)
	}
	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(2)
	db.mysqlDB = conn
	if err := createTablesMySQL(db); err != nil {
		db.mysqlDB.Close()
		db.mysqlDB = nil
		return err
	}
	fmt.Println("[UserDB] MySQL 连接成功，表结构已就绪")
	return nil
}

func createTablesMySQL(db *UserDB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
			id BIGINT PRIMARY KEY COMMENT '用户ID(米米号)',
			email VARCHAR(255) NOT NULL,
			password VARCHAR(255) NOT NULL,
			nickname VARCHAR(64) NOT NULL DEFAULT '',
			color INT NOT NULL DEFAULT 0,
			register_time BIGINT NOT NULL DEFAULT 0,
			role_created TINYINT NOT NULL DEFAULT 0,
			session VARCHAR(255) NOT NULL DEFAULT '',
			session_hex VARCHAR(255) NOT NULL DEFAULT '',
			INDEX idx_email (email)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='账号表'`,
		`CREATE TABLE IF NOT EXISTS game_players (
			user_id BIGINT PRIMARY KEY,
			nick VARCHAR(64) NOT NULL DEFAULT '',
			coins INT NOT NULL DEFAULT 0,
			gold INT NOT NULL DEFAULT 0,
			last_online BIGINT NOT NULL DEFAULT 0,
			data_json LONGTEXT NOT NULL COMMENT '完整 GameData JSON',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			INDEX idx_nick (nick),
			INDEX idx_last_online (last_online)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='玩家主数据'`,
		`CREATE TABLE IF NOT EXISTS game_pets (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			user_id BIGINT NOT NULL,
			slot_index INT NOT NULL DEFAULT 0,
			monster_id INT NOT NULL,
			catch_time INT NOT NULL DEFAULT 0,
			level INT NOT NULL DEFAULT 1,
			dv INT NOT NULL DEFAULT 0,
			nature INT NOT NULL DEFAULT 0,
			exp INT NOT NULL DEFAULT 0,
			name VARCHAR(64) NOT NULL DEFAULT '',
			ev_hp INT NOT NULL DEFAULT 0,
			ev_attack INT NOT NULL DEFAULT 0,
			ev_defence INT NOT NULL DEFAULT 0,
			ev_sa INT NOT NULL DEFAULT 0,
			ev_sd INT NOT NULL DEFAULT 0,
			ev_sp INT NOT NULL DEFAULT 0,
			skills_json TEXT,
			trait INT NOT NULL DEFAULT 0,
			INDEX idx_user_id (user_id),
			INDEX idx_monster_id (monster_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='精灵表'`,
		`CREATE TABLE IF NOT EXISTS game_items (
			user_id BIGINT NOT NULL,
			item_id VARCHAR(32) NOT NULL,
			count INT NOT NULL DEFAULT 0,
			expire_time INT NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, item_id),
			INDEX idx_user_id (user_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='物品表'`,
		`CREATE TABLE IF NOT EXISTS data_docs (
			name VARCHAR(64) PRIMARY KEY COMMENT '文档名如 skills.xml, spt.xml',
			content LONGTEXT NOT NULL COMMENT 'XML 全文',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='配置文档表'`,
		`CREATE TABLE IF NOT EXISTS data_docs_skills (
			id INT PRIMARY KEY COMMENT '技能ID',
			name VARCHAR(128) NOT NULL DEFAULT '' COMMENT '技能名',
			category INT NOT NULL DEFAULT 0 COMMENT '1物理2特殊4变化',
			type_id INT NOT NULL DEFAULT 0 COMMENT '属性类型',
			power INT NOT NULL DEFAULT 0 COMMENT '威力',
			max_pp INT NOT NULL DEFAULT 0 COMMENT '最大PP',
			accuracy INT NOT NULL DEFAULT 0 COMMENT '命中',
			side_effect VARCHAR(128) NOT NULL DEFAULT '' COMMENT '技能效果',
			url VARCHAR(255) NOT NULL DEFAULT '' COMMENT '效果资源',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='技能表(由skills.xml按格式分列)'`,
		`CREATE TABLE IF NOT EXISTS data_docs_items (
			id INT PRIMARY KEY COMMENT '道具ID',
			name VARCHAR(128) NOT NULL DEFAULT '' COMMENT '道具名',
			cat_id INT NOT NULL DEFAULT 0 COMMENT '分类ID',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='道具表(由items.xml按格式分列)'`,
		`CREATE TABLE IF NOT EXISTS data_docs_spt (
			id INT PRIMARY KEY COMMENT '精灵ID',
			def_name VARCHAR(128) NOT NULL DEFAULT '' COMMENT '默认名',
			type_id INT NOT NULL DEFAULT 0 COMMENT '属性1',
			type_id2 INT NOT NULL DEFAULT 0 COMMENT '属性2',
			hp INT NOT NULL DEFAULT 0,
			atk INT NOT NULL DEFAULT 0,
			def INT NOT NULL DEFAULT 0,
			sp_atk INT NOT NULL DEFAULT 0,
			sp_def INT NOT NULL DEFAULT 0,
			spd INT NOT NULL DEFAULT 0,
			evolves_from INT NOT NULL DEFAULT 0,
			evolves_to INT NOT NULL DEFAULT 0,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='精灵表(由spt.xml按格式分列)'`,
		`CREATE TABLE IF NOT EXISTS gm_weights_config (
			id INT PRIMARY KEY DEFAULT 1 COMMENT '唯一行',
			data_json LONGTEXT NOT NULL COMMENT '权重配置JSON(capsuleCatchRates/fusionSuccessRate/fusionSuccessRates)',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='GM权重配置(胶囊捕捉率/精灵融合成功率)'`,
		`CREATE TABLE IF NOT EXISTS gm_fusion_rules (
			id INT PRIMARY KEY DEFAULT 1 COMMENT '唯一行',
			data_json LONGTEXT NOT NULL COMMENT '自定义融合规则JSON:[{petIdA,petIdB,soulPearlId}]',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='GM融合管理(精灵A+精灵B→元神珠)'`,
		`CREATE TABLE IF NOT EXISTS gm_fresh_fight_config (
			id INT PRIMARY KEY DEFAULT 1 COMMENT '唯一行',
			data_json LONGTEXT NOT NULL COMMENT '试炼之塔配置JSON:[{level,seq,bossId,enemyLv,hpRatio,atkRatio,defRatio,saRatio,sdRatio,spRatio}]',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='GM试炼之塔配置(每层怪物与属性倍率)'`,
		`CREATE TABLE IF NOT EXISTS gm_fight_level_config (
			id INT PRIMARY KEY DEFAULT 1 COMMENT '唯一行',
			data_json LONGTEXT NOT NULL COMMENT '勇者之塔配置JSON:[{level,bossIds:[id1,id2,...]}]',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='GM勇者之塔配置(每层Boss精灵ID列表)'`,
		`CREATE TABLE IF NOT EXISTS gm_map_config (
			id INT PRIMARY KEY DEFAULT 1 COMMENT '唯一行',
			data_json LONGTEXT NOT NULL COMMENT '地图刷新配置JSON:[{mapId,slots,refreshIntervalSeconds,common,rare}]',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='GM地图刷新配置(普通/稀有精灵+刷新间隔)'`,
		`CREATE TABLE IF NOT EXISTS gm_gacha_config (
			id INT PRIMARY KEY DEFAULT 1 COMMENT '唯一行',
			data_json LONGTEXT NOT NULL COMMENT '扭蛋机配置JSON:[{itemID,weight,name,isGold}]',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='GM扭蛋机配置(奖励池列表)'`,
		`CREATE TABLE IF NOT EXISTS gm_dark_portal_config (
			id INT PRIMARY KEY DEFAULT 1 COMMENT '唯一行',
			data_json LONGTEXT NOT NULL COMMENT '暗黑武斗场配置JSON:[{doorIndex,subIndex,bossId,enemyLv,rewardItemId,rewardPetId}]',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='GM暗黑武斗场配置(各门BOSS与奖励)'`,
		`CREATE TABLE IF NOT EXISTS gm_sptboss_config (
			id INT PRIMARY KEY DEFAULT 1 COMMENT '唯一行',
			data_json LONGTEXT NOT NULL COMMENT 'SPT BOSS配置JSON: mapBosses/sptBosses/traits',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='GM SPT BOSS配置(地图BOSS/奖励/特性)'`,
		`CREATE TABLE IF NOT EXISTS gm_buff_items_config (
			id INT PRIMARY KEY DEFAULT 1 COMMENT '唯一行',
			data_json LONGTEXT NOT NULL COMMENT 'BUFF 道具配置 JSON: speedupItems/autoFightItems/energyItems/studyItems',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='GM BUFF 道具配置(经验加速/自动战斗/体力吸收/学习力双倍)'`,
		`CREATE TABLE IF NOT EXISTS gm_reward_config (
			id INT PRIMARY KEY DEFAULT 1 COMMENT '唯一行',
			data_json LONGTEXT NOT NULL COMMENT '奖励配置 JSON: collectRewards/roweiRewards 等',
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='GM 奖励配置(赠宠/罗威训练奖励等)'`,
	}
	for _, q := range queries {
		if _, err := db.mysqlDB.Exec(q); err != nil {
			return fmt.Errorf("建表失败: %w\nSQL: %s", err, q)
		}
	}
	return nil
}

// writeOfflineConfig 当未连接数据库时，将配置写入运行目录，供下次启动同步到 DB。
func writeOfflineConfig(db *UserDB, filename string, data []byte) {
	if db.config.OfflineConfigDir == "" || len(data) == 0 {
		return
	}
	path := filepath.Join(db.config.OfflineConfigDir, filename)
	if err := os.WriteFile(path, data, 0644); err != nil {
		fmt.Printf("[UserDB] 写入离线配置失败 %s: %v\n", filename, err)
	}
}

// readOfflineConfig 在未连接数据库时，从运行目录读取离线配置（若存在）。
// 若文件不存在或为空，返回 (nil, nil) 以便调用方使用默认配置。
func readOfflineConfig(db *UserDB, filename string) ([]byte, error) {
	if db.config.OfflineConfigDir == "" {
		return nil, nil
	}
	path := filepath.Join(db.config.OfflineConfigDir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	return data, nil
}

// syncOfflineConfigToDB 连接数据库成功后，将运行目录下的离线配置同步到数据库（若有）。
func syncOfflineConfigToDB(db *UserDB) {
	if db.config.OfflineConfigDir == "" {
		return
	}
	type entry struct {
		file string
		save func([]byte) error
	}
	entries := []entry{
		{"gm_weights_config.json", db.SaveWeights},
		{"gm_fusion_rules.json", db.SaveFusionRules},
		{"gm_fresh_fight_config.json", db.SaveFreshFightConfig},
		{"gm_fight_level_config.json", db.SaveFightLevelConfig},
		{"gm_map_config.json", db.SaveMapConfigs},
		{"gm_gacha_config.json", db.SaveGachaConfig},
		{"gm_dark_portal_config.json", db.SaveDarkPortalConfig},
		{"gm_sptboss_config.json", db.SaveSPTBossConfig},
		{"gm_buff_items_config.json", db.SaveBuffItemsConfig},
		{"gm_reward_config.json", db.SaveRewardConfig},
	}
	for _, e := range entries {
		path := filepath.Join(db.config.OfflineConfigDir, e.file)
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		if err := e.save(data); err != nil {
			fmt.Printf("[UserDB] 同步离线配置到 DB 失败 %s: %v\n", e.file, err)
			continue
		}
		fmt.Printf("[UserDB] 已从运行目录同步配置到数据库: %s\n", e.file)
	}
}

// mysqlLoad 从 MySQL 加载全部账号与玩家主数据到内存（精灵/物品在按需加载 GameData 时再组装）
func mysqlLoad(db *UserDB) {
	db.mu.Lock()
	defer db.mu.Unlock()

	// 加载账号
	rows, err := db.mysqlDB.Query("SELECT id, email, password, nickname, color, register_time, role_created, session, session_hex FROM accounts")
	if err != nil {
		fmt.Printf("[UserDB] MySQL 加载 accounts 失败: %v\n", err)
		db.users = make(map[int64]*User)
		db.gameData = make(map[int64]*GameData)
		return
	}
	defer rows.Close()
	db.users = make(map[int64]*User)
	for rows.Next() {
		var u User
		var session, sessionHex sql.NullString
		err := rows.Scan(&u.UserID, &u.Email, &u.Password, &u.Nickname, &u.Color, &u.RegisterTime, &u.RoleCreated, &session, &sessionHex)
		if err != nil {
			continue
		}
		if sessionHex.Valid && sessionHex.String != "" {
			u.SessionHex = sessionHex.String
			if decoded, err := hex.DecodeString(sessionHex.String); err == nil && len(decoded) == 16 {
				u.Session = string(decoded)
			}
		}
		if session.Valid && session.String != "" && u.Session == "" {
			if decoded, err := hex.DecodeString(session.String); err == nil && len(decoded) == 16 {
				u.Session = string(decoded)
				u.SessionHex = session.String
			} else {
				u.Session = session.String
			}
		}
		db.users[u.UserID] = &u
	}
	// 加载玩家主数据（仅 user_id 列表与简要信息；完整 GameData 在 GetOrCreateGameData 时从 data_json + game_pets + game_items 组装）
	rows2, err := db.mysqlDB.Query("SELECT user_id, data_json FROM game_players")
	if err != nil {
		fmt.Printf("[UserDB] MySQL 加载 game_players 失败: %v\n", err)
		db.gameData = make(map[int64]*GameData)
		if !db.loaded {
			fmt.Printf("[UserDB] 从 MySQL 加载了 %d 个账号\n", len(db.users))
			db.loaded = true
		}
		return
	}
	defer rows2.Close()
	db.gameData = make(map[int64]*GameData)
	for rows2.Next() {
		var userID int64
		var dataJSON string
		if err := rows2.Scan(&userID, &dataJSON); err != nil {
			continue
		}
		var g GameData
		if err := json.Unmarshal([]byte(dataJSON), &g); err != nil {
			continue
		}
		db.gameData[userID] = &g
	}
	if !db.loaded {
		fmt.Printf("[UserDB] 从 MySQL 加载了 %d 个账号、%d 条玩家数据\n", len(db.users), len(db.gameData))
		db.loaded = true
	}
}

// saveUserToMySQL 保存账号到 MySQL。session 列存 hex 字符串，避免二进制导致 utf8mb4 报错。
func saveUserToMySQL(db *UserDB, u *User) error {
	if db.mysqlDB == nil || u == nil {
		return nil
	}
	sessionCol := u.SessionHex
	if sessionCol == "" && len(u.Session) == 16 {
		sessionCol = hex.EncodeToString([]byte(u.Session))
	}
	_, err := db.mysqlDB.Exec(
		`INSERT INTO accounts (id, email, password, nickname, color, register_time, role_created, session, session_hex)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE email=VALUES(email), password=VALUES(password), nickname=VALUES(nickname),
		 color=VALUES(color), role_created=VALUES(role_created), session=VALUES(session), session_hex=VALUES(session_hex)`,
		u.UserID, u.Email, u.Password, u.Nickname, u.Color, u.RegisterTime, u.RoleCreated, sessionCol, sessionCol,
	)
	return err
}

// loadUserFromMySQL 按 userID 从 MySQL 加载账号（若内存已有则不用调）
func loadUserFromMySQL(db *UserDB, userID int64) *User {
	if db.mysqlDB == nil {
		return nil
	}
	var u User
	var session, sessionHex sql.NullString
	err := db.mysqlDB.QueryRow(
		"SELECT id, email, password, nickname, color, register_time, role_created, session, session_hex FROM accounts WHERE id = ?", userID,
	).Scan(&u.UserID, &u.Email, &u.Password, &u.Nickname, &u.Color, &u.RegisterTime, &u.RoleCreated, &session, &sessionHex)
	if err != nil {
		return nil
	}
	if sessionHex.Valid && sessionHex.String != "" {
		u.SessionHex = sessionHex.String
		if decoded, err := hex.DecodeString(sessionHex.String); err == nil && len(decoded) == 16 {
			u.Session = string(decoded)
		}
	}
	if session.Valid && session.String != "" && u.Session == "" {
		if decoded, err := hex.DecodeString(session.String); err == nil && len(decoded) == 16 {
			u.Session = string(decoded)
			u.SessionHex = session.String
		} else {
			u.Session = session.String
		}
	}
	return &u
}

// saveGameDataToMySQL 将 GameData 按分类写入 game_players + game_pets + game_items
func saveGameDataToMySQL(db *UserDB, userID int64, data *GameData) error {
	if db.mysqlDB == nil || data == nil {
		return nil
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return err
	}
	nick := data.Nick
	if len(nick) > 64 {
		nick = nick[:64]
	}
	// 主表
	_, err = db.mysqlDB.Exec(
		`INSERT INTO game_players (user_id, nick, coins, gold, last_online, data_json)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE nick=VALUES(nick), coins=VALUES(coins), gold=VALUES(gold), last_online=VALUES(last_online), data_json=VALUES(data_json)`,
		userID, nick, data.Coins, data.Gold, data.LastOnline, string(dataJSON),
	)
	if err != nil {
		return err
	}
	// 精灵表：先删后插
	_, _ = db.mysqlDB.Exec("DELETE FROM game_pets WHERE user_id = ?", userID)
	for i, p := range data.Pets {
		skillsJSON := ""
		if len(p.Skills) > 0 {
			b, _ := json.Marshal(p.Skills)
			skillsJSON = string(b)
		}
		_, err = db.mysqlDB.Exec(
			`INSERT INTO game_pets (user_id, slot_index, monster_id, catch_time, level, dv, nature, exp, name, ev_hp, ev_attack, ev_defence, ev_sa, ev_sd, ev_sp, skills_json, trait)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			userID, i, p.ID, p.CatchTime, p.Level, p.DV, p.Nature, p.Exp, p.Name,
			p.EVHP, p.EVAttack, p.EVDefence, p.EVSpAtk, p.EVSpDef, p.EVSpeed, skillsJSON, p.Trait,
		)
		if err != nil {
			return err
		}
	}
	// 物品表：先删后插
	_, _ = db.mysqlDB.Exec("DELETE FROM game_items WHERE user_id = ?", userID)
	for itemID, it := range data.Items {
		if itemID == "" {
			continue
		}
		_, err = db.mysqlDB.Exec(
			"INSERT INTO game_items (user_id, item_id, count, expire_time) VALUES (?, ?, ?, ?)",
			userID, itemID, it.Count, it.ExpireTime,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// loadGameDataFromMySQL 从 game_players.data_json 加载完整 GameData（已包含 pets/items 在 JSON 内；game_pets/game_items 表仅用于 GM 分类查询）
func loadGameDataFromMySQL(db *UserDB, userID int64) *GameData {
	if db.mysqlDB == nil {
		return nil
	}
	var dataJSON string
	err := db.mysqlDB.QueryRow("SELECT data_json FROM game_players WHERE user_id = ?", userID).Scan(&dataJSON)
	if err != nil {
		return nil
	}
	var g GameData
	if err := json.Unmarshal([]byte(dataJSON), &g); err != nil {
		return nil
	}
	// 兼容：空指针字段
	if g.Items == nil {
		g.Items = make(map[string]Item)
	}
	if g.PetBook == nil {
		g.PetBook = make(map[string]PetBookEntry)
	}
	if g.Tasks == nil {
		g.Tasks = make(map[string]Task)
	}
	// 兼容旧 data_json：精灵的 Skills 若为 nil 则置为空切片，保证 GM 接口返回完整结构（个体值/性格/特性/技能可显示）
	for i := range g.Pets {
		if g.Pets[i].Skills == nil {
			g.Pets[i].Skills = []int{}
		}
	}
	for i := range g.StoragePets {
		if g.StoragePets[i].Skills == nil {
			g.StoragePets[i].Skills = []int{}
		}
	}
	
	// 始终按超能等级同步形态（等级 1-12 → 形态 1-5），避免 DB 中旧形态导致 12 级显示 1 级形态
	if g.Nono.SuperLevel > 0 {
		calculatedType := calculateSuperNonoTypeByLevel(g.Nono.SuperLevel)
		if g.Nono.SuperNono != calculatedType {
			fmt.Printf("[UserDB] MySQL: 用户 %d 超能NONO形态 按等级同步 (等级=%d, 旧形态=%d -> 形态=%d)\n",
				userID, g.Nono.SuperLevel, g.Nono.SuperNono, calculatedType)
		}
		g.Nono.SuperNono = calculatedType
	}
	
	return &g
}

// createUserInMySQL 在 MySQL 中插入新账号
func createUserInMySQL(db *UserDB, u *User) error {
	if db.mysqlDB == nil {
		return nil
	}
	_, err := db.mysqlDB.Exec(
		`INSERT INTO accounts (id, email, password, nickname, color, register_time, role_created, session, session_hex)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.UserID, u.Email, u.Password, u.Nickname, u.Color, u.RegisterTime, u.RoleCreated, u.Session, u.SessionHex,
	)
	return err
}

// deleteUserInMySQL 删除 MySQL 中该用户的账号与游戏数据（按分类表删除）
func deleteUserInMySQL(db *UserDB, userID int64) error {
	if db.mysqlDB == nil {
		return nil
	}
	_, _ = db.mysqlDB.Exec("DELETE FROM game_items WHERE user_id = ?", userID)
	_, _ = db.mysqlDB.Exec("DELETE FROM game_pets WHERE user_id = ?", userID)
	_, _ = db.mysqlDB.Exec("DELETE FROM game_players WHERE user_id = ?", userID)
	_, err := db.mysqlDB.Exec("DELETE FROM accounts WHERE id = ?", userID)
	return err
}

// MySQLListUsersForGM 供 GM 使用：从 MySQL 分页/关键词查询用户列表（仅当启用 MySQL 时有效）
func (db *UserDB) MySQLListUsersForGM(keyword string, limit, offset int) (list []GMUserRow, total int, err error) {
	if db.mysqlDB == nil {
		return nil, 0, nil
	}
	keywordLike := "%" + keyword + "%"
	err = db.mysqlDB.QueryRow(
		`SELECT COUNT(*) FROM accounts a LEFT JOIN game_players p ON a.id = p.user_id
		 WHERE ? = '' OR a.email LIKE ? OR a.nickname LIKE ? OR CAST(a.id AS CHAR) LIKE ?`,
		keyword, keywordLike, keywordLike, keywordLike,
	).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := db.mysqlDB.Query(
		`SELECT a.id, a.email, a.nickname, IFNULL(p.nick,''), IFNULL(p.coins,0), IFNULL(p.gold,0),
		 (SELECT COUNT(*) FROM game_pets p2 WHERE p2.user_id = a.id), IFNULL(p.last_online,0), a.role_created
		 FROM accounts a LEFT JOIN game_players p ON a.id = p.user_id
		 WHERE ? = '' OR a.email LIKE ? OR a.nickname LIKE ? OR CAST(a.id AS CHAR) LIKE ?
		 ORDER BY p.last_online DESC, a.id DESC LIMIT ? OFFSET ?`,
		keyword, keywordLike, keywordLike, keywordLike, limit, offset,
	)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	for rows.Next() {
		var r GMUserRow
		var email, nickname, nick sql.NullString
		_ = rows.Scan(&r.UserID, &email, &nickname, &nick, &r.Coins, &r.Gold, &r.PetCount, &r.LastOnline, &r.RoleCreated)
		if email.Valid {
			r.Email = email.String
		}
		if nickname.Valid {
			r.Nickname = nickname.String
		}
		if nick.Valid {
			r.Nick = nick.String
		}
		list = append(list, r)
	}
	return list, total, nil
}

// GMUserRow GM 用户行（与 GM 前端 UserInfo 对齐）
type GMUserRow struct {
	UserID      int64  `json:"userId"`
	Email       string `json:"email"`
	Nickname    string `json:"nickname"`
	Nick        string `json:"nick"`
	Coins       int    `json:"coins"`
	Gold        int    `json:"gold"`
	PetCount    int    `json:"petCount"`
	LastOnline  int64  `json:"lastOnline"`
	RoleCreated bool   `json:"roleCreated"`
}

// MySQLGetUserDetailForGM 从 MySQL 读取单用户详情（账号 + 主数据 + 精灵数/物品数）
func (db *UserDB) MySQLGetUserDetailForGM(userID int64) (account *User, gameData *GameData, err error) {
	if db.mysqlDB == nil {
		return nil, nil, nil
	}
	account = loadUserFromMySQL(db, userID)
	if account == nil {
		return nil, nil, fmt.Errorf("账号不存在")
	}
	gameData = loadGameDataFromMySQL(db, userID)
	if gameData == nil {
		gameData = &GameData{}
	}
	return account, gameData, nil
}

// CloseMySQL 关闭 MySQL 连接（进程退出时调用）
func (db *UserDB) CloseMySQL() {
	if db.mysqlDB != nil {
		_ = db.mysqlDB.Close()
		db.mysqlDB = nil
		fmt.Println("[UserDB] MySQL 连接已关闭")
	}
}

// ImportFromFile 从 users.json 格式文件导入账号与游戏数据到 MySQL（仅当已启用 MySQL 时有效）
// path: 文件路径（如 users.json）；afterImportRename: 导入成功后重命名为该名，为空则不重命名
// 返回：导入的账号数、玩家数据条数、错误
func (db *UserDB) ImportFromFile(path string, afterImportRename string) (accounts int, gameDataCount int, err error) {
	if db.mysqlDB == nil {
		return 0, 0, fmt.Errorf("未启用 MySQL，无法导入")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("读取文件失败: %w", err)
	}
	var dbData DBData
	if err := json.Unmarshal(data, &dbData); err != nil {
		return 0, 0, fmt.Errorf("解析 JSON 失败: %w", err)
	}
	if dbData.Users == nil {
		dbData.Users = make(map[string]*User)
	}
	if dbData.GameData == nil {
		dbData.GameData = make(map[string]*GameData)
	}
	for k, u := range dbData.Users {
		if u == nil {
			continue
		}
		if err := saveUserToMySQL(db, u); err != nil {
			return accounts, gameDataCount, fmt.Errorf("导入账号 %s 失败: %w", k, err)
		}
		accounts++
	}
	for k, g := range dbData.GameData {
		if g == nil {
			continue
		}
		userID, e := strconv.ParseInt(k, 10, 64)
		if e != nil {
			continue
		}
		if err := saveGameDataToMySQL(db, userID, g); err != nil {
			return accounts, gameDataCount, fmt.Errorf("导入玩家数据 user_id=%d 失败: %w", userID, err)
		}
		gameDataCount++
	}
	if afterImportRename != "" && (accounts > 0 || gameDataCount > 0) {
		_ = os.Rename(path, afterImportRename)
		fmt.Printf("[UserDB] 已将 %s 重命名为 %s\n", path, afterImportRename)
	}
	return accounts, gameDataCount, nil
}

// GetDataDoc 从 data_docs 表读取指定名称的文档内容（如 "skills.xml", "spt.xml"）
func (db *UserDB) GetDataDoc(name string) (string, error) {
	if db.mysqlDB == nil {
		return "", fmt.Errorf("未启用 MySQL")
	}
	var content string
	err := db.mysqlDB.QueryRow("SELECT content FROM data_docs WHERE name = ?", name).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return content, nil
}

// SetDataDoc 将文档内容写入 data_docs 表（存在则更新）
func (db *UserDB) SetDataDoc(name string, content string) error {
	if db.mysqlDB == nil {
		return fmt.Errorf("未启用 MySQL")
	}
	_, err := db.mysqlDB.Exec(
		"INSERT INTO data_docs (name, content) VALUES (?, ?) ON DUPLICATE KEY UPDATE content = VALUES(content)",
		name, content)
	return err
}

// EnsureDataDocsImported 若 data_docs 中尚无指定文档，则从 dataDir 目录读取对应 XML 并写入数据库
// 支持 skills.xml、spt.xml；dataDir 可为 "data" 或绝对路径
func (db *UserDB) EnsureDataDocsImported(dataDir string) error {
	if db.mysqlDB == nil {
		return nil
	}
	docs := []struct{ name, file string }{
		{"skills.xml", "skills.xml"},
		{"spt.xml", "spt.xml"},
		{"items.xml", "items.xml"},
	}
	for _, d := range docs {
		existing, err := db.GetDataDoc(d.name)
		if err != nil {
			continue
		}
		if existing != "" && len(existing) > 100 {
			continue
		}
		path := filepath.Join(dataDir, d.file)
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("[UserDB] 未找到 %s，跳过导入 %s\n", path, d.name)
			continue
		}
		if err := db.SetDataDoc(d.name, string(data)); err != nil {
			fmt.Printf("[UserDB] 写入 data_docs %s 失败: %v\n", d.name, err)
			continue
		}
		fmt.Printf("[UserDB] 已从 %s 导入 %s 到数据库\n", path, d.name)
	}
	return nil
}

// ForceImportDataDocs 从 dataDir 读取 data/items.xml、data/skills.xml、data/spt.xml 并写入 data_docs（覆盖）
// 用于启动时将当前 data 目录下的 XML 同步到数据库
func (db *UserDB) ForceImportDataDocs(dataDir string) error {
	if db.mysqlDB == nil {
		return nil
	}
	docs := []struct{ name, file string }{
		{"skills.xml", "skills.xml"},
		{"spt.xml", "spt.xml"},
		{"items.xml", "items.xml"},
	}
	for _, d := range docs {
		path := filepath.Join(dataDir, d.file)
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("[UserDB] 未找到 %s，跳过写入 %s\n", path, d.name)
			continue
		}
		if err := db.SetDataDoc(d.name, string(data)); err != nil {
			fmt.Printf("[UserDB] 写入 data_docs %s 失败: %v\n", d.name, err)
			continue
		}
		if err := db.importDataDocsToTables(d.name, string(data)); err != nil {
			fmt.Printf("[UserDB] 分列表导入 %s 失败: %v\n", d.name, err)
		} else {
			fmt.Printf("[UserDB] 已从 %s 写入 %s 并同步分列表\n", path, d.name)
		}
	}
	return nil
}

// importDataDocsToTables 将 XML 内容解析后按格式分列写入 data_docs_skills / data_docs_items / data_docs_spt
func (db *UserDB) importDataDocsToTables(name, content string) error {
	if db.mysqlDB == nil || content == "" {
		return nil
	}
	data := []byte(content)
	switch name {
	case "skills.xml":
		var root struct {
			XMLName struct{} `xml:"MovesTbl"`
			Moves   []struct {
				ID         int    `xml:"ID,attr"`
				Name       string `xml:"Name,attr"`
				Category   int    `xml:"Category,attr"`
				Type       int    `xml:"Type,attr"`
				Power      int    `xml:"Power,attr"`
				MaxPP      int    `xml:"MaxPP,attr"`
				Accuracy   int    `xml:"Accuracy,attr"`
				SideEffect string `xml:"SideEffect,attr"`
				Url        string `xml:"Url,attr"`
			} `xml:"Moves>Move"`
		}
		if err := xml.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("解析 skills.xml: %w", err)
		}
		if _, err := db.mysqlDB.Exec("TRUNCATE TABLE data_docs_skills"); err != nil {
			return err
		}
		stmt, err := db.mysqlDB.Prepare("INSERT INTO data_docs_skills (id, name, category, type_id, power, max_pp, accuracy, side_effect, url) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, m := range root.Moves {
			if m.ID <= 0 {
				continue
			}
			if _, err := stmt.Exec(m.ID, m.Name, m.Category, m.Type, m.Power, m.MaxPP, m.Accuracy, m.SideEffect, m.Url); err != nil {
				return err
			}
		}
	case "items.xml":
		var root struct {
			XMLName struct{} `xml:"Items"`
			Cats []struct {
				ID    int `xml:"ID,attr"`
				Items []struct {
					ID   int    `xml:"ID,attr"`
					Name string `xml:"Name,attr"`
				} `xml:"Item"`
			} `xml:"Cat"`
		}
		if err := xml.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("解析 items.xml: %w", err)
		}
		if _, err := db.mysqlDB.Exec("TRUNCATE TABLE data_docs_items"); err != nil {
			return err
		}
		stmt, err := db.mysqlDB.Prepare("INSERT INTO data_docs_items (id, name, cat_id) VALUES (?, ?, ?)")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for ci, cat := range root.Cats {
			catID := cat.ID
			if catID == 0 {
				catID = ci
			}
			for _, it := range cat.Items {
				if it.ID <= 0 {
					continue
				}
				if _, err := stmt.Exec(it.ID, it.Name, catID); err != nil {
					return err
				}
			}
		}
	case "spt.xml":
		var root struct {
			XMLName  struct{} `xml:"Monsters"`
			Monsters []struct {
				ID          int    `xml:"ID,attr"`
				DefName     string `xml:"DefName,attr"`
				Type        int    `xml:"Type,attr"`
				Type2       int    `xml:"Type2,attr"`
				HP          int    `xml:"HP,attr"`
				Atk         int    `xml:"Atk,attr"`
				Def         int    `xml:"Def,attr"`
				SpAtk       int    `xml:"SpAtk,attr"`
				SpDef       int    `xml:"SpDef,attr"`
				Spd         int    `xml:"Spd,attr"`
				EvolvesFrom int    `xml:"EvolvesFrom,attr"`
				EvolvesTo   int    `xml:"EvolvesTo,attr"`
			} `xml:"Monster"`
		}
		if err := xml.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("解析 spt.xml: %w", err)
		}
		if _, err := db.mysqlDB.Exec("TRUNCATE TABLE data_docs_spt"); err != nil {
			return err
		}
		stmt, err := db.mysqlDB.Prepare("INSERT INTO data_docs_spt (id, def_name, type_id, type_id2, hp, atk, def, sp_atk, sp_def, spd, evolves_from, evolves_to) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, m := range root.Monsters {
			if m.ID <= 0 {
				continue
			}
			if _, err := stmt.Exec(m.ID, m.DefName, m.Type, m.Type2, m.HP, m.Atk, m.Def, m.SpAtk, m.SpDef, m.Spd, m.EvolvesFrom, m.EvolvesTo); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	return nil
}

// LoadWeights 从 gm_weights_config 表读取权重配置 JSON（id=1）。未启用 MySQL 时返回 (nil, nil) 供调用方走文件逻辑。
func (db *UserDB) LoadWeights() ([]byte, error) {
	if db.mysqlDB == nil {
		// 优先从离线配置目录读取 gm_weights_config.json
		return readOfflineConfig(db, "gm_weights_config.json")
	}
	var data string
	err := db.mysqlDB.QueryRow("SELECT data_json FROM gm_weights_config WHERE id = 1").Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

// SaveWeights 将权重配置 JSON 写入 gm_weights_config 表（id=1，存在则更新）。未启用 MySQL 时直接返回 nil。
func (db *UserDB) SaveWeights(data []byte) error {
	if db.mysqlDB == nil {
		writeOfflineConfig(db, "gm_weights_config.json", data)
		return nil
	}
	_, err := db.mysqlDB.Exec(
		"INSERT INTO gm_weights_config (id, data_json) VALUES (1, ?) ON DUPLICATE KEY UPDATE data_json = VALUES(data_json)",
		string(data))
	return err
}

// LoadFusionRules 从 gm_fusion_rules 表读取融合规则 JSON（id=1）。未启用 MySQL 时返回 (nil, nil)。
func (db *UserDB) LoadFusionRules() ([]byte, error) {
	if db.mysqlDB == nil {
		return readOfflineConfig(db, "gm_fusion_rules.json")
	}
	var data string
	err := db.mysqlDB.QueryRow("SELECT data_json FROM gm_fusion_rules WHERE id = 1").Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

// SaveFusionRules 将融合规则 JSON 写入 gm_fusion_rules 表（id=1，存在则更新）。未启用 MySQL 时直接返回 nil。
func (db *UserDB) SaveFusionRules(data []byte) error {
	if db.mysqlDB == nil {
		writeOfflineConfig(db, "gm_fusion_rules.json", data)
		return nil
	}
	_, err := db.mysqlDB.Exec(
		"INSERT INTO gm_fusion_rules (id, data_json) VALUES (1, ?) ON DUPLICATE KEY UPDATE data_json = VALUES(data_json)",
		string(data))
	return err
}

// LoadFreshFightConfig 从 gm_fresh_fight_config 表读取试炼之塔配置 JSON（id=1）。未启用 MySQL 时返回 (nil, nil)。
// 表结构示例：
//   CREATE TABLE gm_fresh_fight_config (
//       id INT PRIMARY KEY,
//       data_json LONGTEXT NOT NULL
//   );
func (db *UserDB) LoadFreshFightConfig() ([]byte, error) {
	if db.mysqlDB == nil {
		return readOfflineConfig(db, "gm_fresh_fight_config.json")
	}
	var data string
	err := db.mysqlDB.QueryRow("SELECT data_json FROM gm_fresh_fight_config WHERE id = 1").Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

// SaveFreshFightConfig 将试炼之塔配置 JSON 写入 gm_fresh_fight_config 表（id=1，存在则更新）。未启用 MySQL 时直接返回 nil。
func (db *UserDB) SaveFreshFightConfig(data []byte) error {
	if db.mysqlDB == nil {
		writeOfflineConfig(db, "gm_fresh_fight_config.json", data)
		return nil
	}
	_, err := db.mysqlDB.Exec(
		"INSERT INTO gm_fresh_fight_config (id, data_json) VALUES (1, ?) ON DUPLICATE KEY UPDATE data_json = VALUES(data_json)",
		string(data))
	return err
}

// LoadFightLevelConfig 从 gm_fight_level_config 表读取勇者之塔配置 JSON（id=1）。未启用 MySQL 时返回 (nil, nil)。
func (db *UserDB) LoadFightLevelConfig() ([]byte, error) {
	if db.mysqlDB == nil {
		return readOfflineConfig(db, "gm_fight_level_config.json")
	}
	var data string
	err := db.mysqlDB.QueryRow("SELECT data_json FROM gm_fight_level_config WHERE id = 1").Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

// SaveFightLevelConfig 将勇者之塔配置 JSON 写入 gm_fight_level_config 表（id=1，存在则更新）。未启用 MySQL 时直接返回 nil。
func (db *UserDB) SaveFightLevelConfig(data []byte) error {
	if db.mysqlDB == nil {
		writeOfflineConfig(db, "gm_fight_level_config.json", data)
		return nil
	}
	_, err := db.mysqlDB.Exec(
		"INSERT INTO gm_fight_level_config (id, data_json) VALUES (1, ?) ON DUPLICATE KEY UPDATE data_json = VALUES(data_json)",
		string(data))
	return err
}

// LoadMapConfigs 从 gm_map_config 表读取地图刷新配置 JSON（id=1）。未启用 MySQL 时返回 (nil, nil) 供调用方走文件逻辑。
func (db *UserDB) LoadMapConfigs() ([]byte, error) {
	if db.mysqlDB == nil {
		return readOfflineConfig(db, "gm_map_config.json")
	}
	var data string
	err := db.mysqlDB.QueryRow("SELECT data_json FROM gm_map_config WHERE id = 1").Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

// SaveMapConfigs 将地图刷新配置 JSON 写入 gm_map_config 表（id=1，存在则更新）。未启用 MySQL 时直接返回 nil。
func (db *UserDB) SaveMapConfigs(data []byte) error {
	if db.mysqlDB == nil {
		writeOfflineConfig(db, "gm_map_config.json", data)
		return nil
	}
	_, err := db.mysqlDB.Exec(
		"INSERT INTO gm_map_config (id, data_json) VALUES (1, ?) ON DUPLICATE KEY UPDATE data_json = VALUES(data_json)",
		string(data))
	return err
}

// LoadGachaConfig 从 gm_gacha_config 表读取扭蛋机配置 JSON（id=1）。未启用 MySQL 时返回 (nil, nil) 供调用方走文件逻辑。
func (db *UserDB) LoadGachaConfig() ([]byte, error) {
	if db.mysqlDB == nil {
		return readOfflineConfig(db, "gm_gacha_config.json")
	}
	var data string
	err := db.mysqlDB.QueryRow("SELECT data_json FROM gm_gacha_config WHERE id = 1").Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

// SaveGachaConfig 将扭蛋机配置 JSON 写入 gm_gacha_config 表（id=1，存在则更新）。未启用 MySQL 时直接返回 nil。
func (db *UserDB) SaveGachaConfig(data []byte) error {
	if db.mysqlDB == nil {
		writeOfflineConfig(db, "gm_gacha_config.json", data)
		return nil
	}
	_, err := db.mysqlDB.Exec(
		"INSERT INTO gm_gacha_config (id, data_json) VALUES (1, ?) ON DUPLICATE KEY UPDATE data_json = VALUES(data_json)",
		string(data))
	return err
}

// LoadDarkPortalConfig 从 gm_dark_portal_config 表读取暗黑武斗场配置 JSON（id=1）。未启用 MySQL 时返回 (nil, nil)。
func (db *UserDB) LoadDarkPortalConfig() ([]byte, error) {
	if db.mysqlDB == nil {
		return readOfflineConfig(db, "gm_dark_portal_config.json")
	}
	var data string
	err := db.mysqlDB.QueryRow("SELECT data_json FROM gm_dark_portal_config WHERE id = 1").Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

// SaveDarkPortalConfig 将暗黑武斗场配置 JSON 写入 gm_dark_portal_config 表（id=1，存在则更新）。未启用 MySQL 时直接返回 nil。
func (db *UserDB) SaveDarkPortalConfig(data []byte) error {
	if db.mysqlDB == nil {
		writeOfflineConfig(db, "gm_dark_portal_config.json", data)
		return nil
	}
	_, err := db.mysqlDB.Exec(
		"INSERT INTO gm_dark_portal_config (id, data_json) VALUES (1, ?) ON DUPLICATE KEY UPDATE data_json = VALUES(data_json)",
		string(data))
	return err
}

// LoadSPTBossConfig 从 gm_sptboss_config 表读取 SPT BOSS 配置 JSON（id=1）。未启用 MySQL 时返回 (nil, nil)。
func (db *UserDB) LoadSPTBossConfig() ([]byte, error) {
	if db.mysqlDB == nil {
		return readOfflineConfig(db, "gm_sptboss_config.json")
	}
	var data string
	err := db.mysqlDB.QueryRow("SELECT data_json FROM gm_sptboss_config WHERE id = 1").Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

// SaveSPTBossConfig 将 SPT BOSS 配置 JSON 写入 gm_sptboss_config 表（id=1，存在则更新）。未启用 MySQL 时直接返回 nil。
func (db *UserDB) SaveSPTBossConfig(data []byte) error {
	if db.mysqlDB == nil {
		writeOfflineConfig(db, "gm_sptboss_config.json", data)
		return nil
	}
	_, err := db.mysqlDB.Exec(
		"INSERT INTO gm_sptboss_config (id, data_json) VALUES (1, ?) ON DUPLICATE KEY UPDATE data_json = VALUES(data_json)",
		string(data))
	return err
}

// LoadBuffItemsConfig 从 gm_buff_items_config 表读取 BUFF 道具配置 JSON（id=1）。未启用 MySQL 时走离线文件。
func (db *UserDB) LoadBuffItemsConfig() ([]byte, error) {
	if db.mysqlDB == nil {
		return readOfflineConfig(db, "gm_buff_items_config.json")
	}
	var data string
	err := db.mysqlDB.QueryRow("SELECT data_json FROM gm_buff_items_config WHERE id = 1").Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

// SaveBuffItemsConfig 将 BUFF 道具配置 JSON 写入 gm_buff_items_config 表（id=1）。
func (db *UserDB) SaveBuffItemsConfig(data []byte) error {
	if db.mysqlDB == nil {
		writeOfflineConfig(db, "gm_buff_items_config.json", data)
		return nil
	}
	_, err := db.mysqlDB.Exec(
		"INSERT INTO gm_buff_items_config (id, data_json) VALUES (1, ?) ON DUPLICATE KEY UPDATE data_json = VALUES(data_json)",
		string(data))
	return err
}

// LoadRewardConfig 从 gm_reward_config 表读取奖励配置 JSON（id=1）。未启用 MySQL 时走离线文件。
func (db *UserDB) LoadRewardConfig() ([]byte, error) {
	if db.mysqlDB == nil {
		return readOfflineConfig(db, "gm_reward_config.json")
	}
	var data string
	err := db.mysqlDB.QueryRow("SELECT data_json FROM gm_reward_config WHERE id = 1").Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

// SaveRewardConfig 将奖励配置 JSON 写入 gm_reward_config 表（id=1）。
func (db *UserDB) SaveRewardConfig(data []byte) error {
	if db.mysqlDB == nil {
		writeOfflineConfig(db, "gm_reward_config.json", data)
		return nil
	}
	_, err := db.mysqlDB.Exec(
		"INSERT INTO gm_reward_config (id, data_json) VALUES (1, ?) ON DUPLICATE KEY UPDATE data_json = VALUES(data_json)",
		string(data))
	return err
}

// exportTableToJSON 将指定表的全部行导出为 JSON 数组文件，文件名为 db_<table>.json，写入 dir 目录。
// 说明：
// - 使用通用 SELECT *，按列名生成 map[string]interface{} 列表；
// - []byte 类型会转成 string，方便查看；
// - 若查询失败或表不存在，仅打印错误并返回。
func (db *UserDB) exportTableToJSON(table, dir string) error {
	if db.mysqlDB == nil {
		return fmt.Errorf("未启用 MySQL，无法导出表 %s", table)
	}
	if dir == "" {
		return fmt.Errorf("导出目录为空")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建导出目录失败: %w", err)
	}

	rows, err := db.mysqlDB.Query("SELECT * FROM " + table)
	if err != nil {
		return fmt.Errorf("查询表 %s 失败: %w", table, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("获取表 %s 列信息失败: %w", table, err)
	}

	results := make([]map[string]interface{}, 0)
	for rows.Next() {
		values := make([]interface{}, len(cols))
		valuePtrs := make([]interface{}, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return fmt.Errorf("扫描表 %s 行数据失败: %w", table, err)
		}
		rowMap := make(map[string]interface{}, len(cols))
		for i, col := range cols {
			v := values[i]
			if b, ok := v.([]byte); ok {
				rowMap[col] = string(b)
			} else {
				rowMap[col] = v
			}
		}
		results = append(results, rowMap)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历表 %s 行数据出错: %w", table, err)
	}

	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化表 %s 数据失败: %w", table, err)
	}

	filename := fmt.Sprintf("db_%s.json", table)
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("写入表 %s 导出文件失败: %w", table, err)
	}
	fmt.Printf("[UserDB] 已导出表 %s 到 %s\n", table, path)
	return nil
}

// ExportAllTablesToJSON 在 dir 目录下导出当前 MySQL 中的主要业务表为 JSON 文件（一个表一个文件）。
// 生成的文件命名为 db_<表名>.json，例如：
// - db_accounts.json
// - db_game_players.json
// - db_game_pets.json
// - db_game_items.json
// 以及若干配置/文档表：
// - db_data_docs.json / db_data_docs_skills.json / db_data_docs_items.json / db_data_docs_spt.json
// - db_gm_weights_config.json / db_gm_fusion_rules.json / db_gm_fresh_fight_config.json / db_gm_fight_level_config.json
// - db_gm_map_config.json / db_gm_gacha_config.json / db_gm_dark_portal_config.json / db_gm_sptboss_config.json
func (db *UserDB) ExportAllTablesToJSON(dir string) error {
	if db.mysqlDB == nil {
		return fmt.Errorf("未启用 MySQL，无法导出所有表")
	}
	tables := []string{
		"accounts",
		"game_players",
		"game_pets",
		"game_items",
		"data_docs",
		"data_docs_skills",
		"data_docs_items",
		"data_docs_spt",
		"gm_weights_config",
		"gm_fusion_rules",
		"gm_fresh_fight_config",
		"gm_fight_level_config",
		"gm_map_config",
		"gm_gacha_config",
		"gm_dark_portal_config",
		"gm_sptboss_config",
	}

	var lastErr error
	for _, tbl := range tables {
		if err := db.exportTableToJSON(tbl, dir); err != nil {
			// 仅记录错误并继续导出其它表，避免单表失败导致整体中断
			fmt.Printf("[UserDB] 导出表 %s 失败: %v\n", tbl, err)
			lastErr = err
		}
	}
	return lastErr
}

// ExportGMConfigsToOfflineFiles 将数据库中的 GM 配置表导出为与离线模式相同命名的 JSON 文件，
// 便于下次未启动数据库时，服务端仍可从运行目录读取这些配置。
// 输出文件示例（写入 OfflineConfigDir，下同）：
// - gm_weights_config.json
// - gm_fusion_rules.json
// - gm_fresh_fight_config.json
// - gm_fight_level_config.json
// - gm_map_config.json
// - gm_gacha_config.json
// - gm_dark_portal_config.json
// - gm_sptboss_config.json
func (db *UserDB) ExportGMConfigsToOfflineFiles() {
	if db.mysqlDB == nil {
		// 未启用 MySQL，本地已直接使用 gm_*.json，无需额外导出
		return
	}
	type entry struct {
		filename string
		load     func() ([]byte, error)
	}
	entries := []entry{
		{"gm_weights_config.json", db.LoadWeights},
		{"gm_fusion_rules.json", db.LoadFusionRules},
		{"gm_fresh_fight_config.json", db.LoadFreshFightConfig},
		{"gm_fight_level_config.json", db.LoadFightLevelConfig},
		{"gm_map_config.json", db.LoadMapConfigs},
		{"gm_gacha_config.json", db.LoadGachaConfig},
		{"gm_dark_portal_config.json", db.LoadDarkPortalConfig},
		{"gm_sptboss_config.json", db.LoadSPTBossConfig},
	}

	for _, e := range entries {
		data, err := e.load()
		if err != nil {
			fmt.Printf("[UserDB] 导出 GM 配置 %s 失败: %v\n", e.filename, err)
			continue
		}
		if len(data) == 0 {
			continue
		}
		// 复用离线写入逻辑：会写到 config.OfflineConfigDir 目录下
		writeOfflineConfig(db, e.filename, data)
		fmt.Printf("[UserDB] 已从 MySQL 导出 GM 配置到离线文件: %s\n", e.filename)
	}
}

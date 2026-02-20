package handlers

import (
	"encoding/json"
	"strconv"
	"sync"
)

// FusionRuleEntry 一条自定义融合规则：精灵A + 精灵B → 元神珠物品 ID
type FusionRuleEntry struct {
	PetIdA      int `json:"petIdA"`
	PetIdB      int `json:"petIdB"`
	SoulPearlId int `json:"soulPearlId"`
}

var (
	fusionRulesMu         sync.RWMutex
	fusionRulesMap         map[string]int // key "id1_id2" (id1<=id2), value soulPearlId
	fusionRulesPersistence FusionRulesPersistence
)

// FusionRulesPersistence 融合规则持久化，由 main 在启动时注入
type FusionRulesPersistence interface {
	LoadFusionRules() ([]byte, error)
	SaveFusionRules(data []byte) error
}

// SetFusionRulesPersistence 设置持久化实现（如 *userdb.UserDB）
func SetFusionRulesPersistence(p FusionRulesPersistence) {
	fusionRulesPersistence = p
}

// LoadFusionRulesConfig 从数据库或本地加载融合规则
func LoadFusionRulesConfig() {
	fusionRulesMu.Lock()
	defer fusionRulesMu.Unlock()
	fusionRulesMap = make(map[string]int)
	if fusionRulesPersistence == nil {
		return
	}
	data, err := fusionRulesPersistence.LoadFusionRules()
	if err != nil || len(data) == 0 {
		return
	}
	var list []FusionRuleEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return
	}
	for _, r := range list {
		if r.PetIdA <= 0 || r.PetIdB <= 0 || r.SoulPearlId <= 0 {
			continue
		}
		key := fusionRuleKey(r.PetIdA, r.PetIdB)
		fusionRulesMap[key] = r.SoulPearlId
	}
}

func fusionRuleKey(a, b int) string {
	if a > b {
		a, b = b, a
	}
	return strconv.Itoa(a) + "_" + strconv.Itoa(b)
}

// GetFusionRule 查询自定义规则：精灵 idA + idB 是否配置了融合，若配置则返回元神珠物品 ID
func GetFusionRule(petIdA, petIdB int) (soulPearlId int, ok bool) {
	fusionRulesMu.RLock()
	defer fusionRulesMu.RUnlock()
	if fusionRulesMap == nil {
		return 0, false
	}
	soulPearlId, ok = fusionRulesMap[fusionRuleKey(petIdA, petIdB)]
	return soulPearlId, ok
}

// GetAllFusionRules 返回当前内存中的全部规则（供 GM 读取）
func GetAllFusionRules() []FusionRuleEntry {
	fusionRulesMu.RLock()
	defer fusionRulesMu.RUnlock()
	if fusionRulesMap == nil {
		return nil
	}
	list := make([]FusionRuleEntry, 0, len(fusionRulesMap))
	for k, sid := range fusionRulesMap {
		// k = "id1_id2"
		var a, b int
		for i := 0; i < len(k); i++ {
			if k[i] == '_' {
				a, _ = strconv.Atoi(k[:i])
				b, _ = strconv.Atoi(k[i+1:])
				break
			}
		}
		list = append(list, FusionRuleEntry{PetIdA: a, PetIdB: b, SoulPearlId: sid})
	}
	return list
}

// SaveFusionRulesConfig 保存规则到内存并持久化
func SaveFusionRulesConfig(rules []FusionRuleEntry) error {
	fusionRulesMu.Lock()
	fusionRulesMap = make(map[string]int)
	for _, r := range rules {
		if r.PetIdA <= 0 || r.PetIdB <= 0 || r.SoulPearlId <= 0 {
			continue
		}
		fusionRulesMap[fusionRuleKey(r.PetIdA, r.PetIdB)] = r.SoulPearlId
	}
	fusionRulesMu.Unlock()
	if fusionRulesPersistence == nil {
		return nil
	}
	data, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	return fusionRulesPersistence.SaveFusionRules(data)
}

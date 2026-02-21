// Package typechart 赛尔号属性克制表（与前端 SkillXMLInfo 属性 key_1~key_226 对应）
// 1草 2水 3火 4飞行 5电 6机械 7地面 8普通 9冰 10超能 11战斗 12光 13暗影 14神秘 15龙 16圣灵
// 17次元 18远古 19邪灵 20自然 221王 222混沌 223神灵 224轮回 225虫 226虚空
package typechart

// TypeChart 属性克制表：key=攻击方属性，value=被克制的属性列表（攻击方对这些属性造成2倍伤害）
var TypeChart = map[int][]int{
	1:  {2, 7},                 // 草克水、地面
	2:  {3, 7},                 // 水克火、地面
	3:  {1, 6, 9},              // 火克草、机械、冰
	4:  {1, 11},                // 飞行克草、战斗
	5:  {2, 4},                 // 电克水、飞行
	6:  {9, 18},                // 机械克冰、远古
	7:  {3, 5, 6},              // 地面克火、电、机械
	8:  {},                     // 普通无克制
	9:  {1, 4, 7, 17, 18},      // 冰克草、飞行、地面、次元、远古
	10: {11, 20},               // 超能克战斗、自然
	11: {8, 9},                 // 战斗克普通、冰
	12: {13},                   // 光克暗影
	13: {10, 13},               // 暗影克超能、暗影
	14: {16, 18, 20},           // 神秘克圣灵、远古、自然
	15: {9, 15, 16, 19},        // 龙克冰、龙、圣灵、邪灵
	16: {1, 2, 3, 5, 9, 18},    // 圣灵克草、水、火、电、冰、远古
	17: {4, 6, 10, 19, 20, 225}, // 次元克飞行、机械、超能、邪灵、自然、虫
	18: {14, 15, 1, 4},         // 远古克神秘、龙、草、飞行
	19: {12, 14, 17, 13, 20},   // 邪灵克光、神秘、次元、暗影、自然
	20: {221, 12, 4, 1, 3, 7, 5, 2}, // 自然克王、光、飞行、草、火、地面、电、水
	221: {17, 19},              // 王克次元、邪灵
	222: {17, 19, 20},          // 混沌克次元、邪灵、自然
	225: {},                    // 虫（被次元克，无克制）
	226: {},                    // 虚空（扩展预留）
}

// TypeNoEffect 属性无效表：攻击方属性 -> 防守方属性，伤害为 0
var TypeNoEffect = map[int]map[int]bool{
	5:  {7: true},  // 电系打地面系无效
	10: {12: true}, // 超能系打光系无效
	7:  {4: true},  // 地面系打飞行系无效
	17: {13: true}, // 次元系打暗影系无效
}

// GetTypeMultiplier 获取单属性克制倍率：攻击方 atkType 对 防守方 defType，返回 0/0.5/1.0/2.0
func GetTypeMultiplier(atkType, defType int) float64 {
	if noEffect, ok := TypeNoEffect[atkType]; ok && noEffect[defType] {
		return 0
	}
	if types, ok := TypeChart[atkType]; ok {
		for _, t := range types {
			if t == defType {
				return 2.0
			}
		}
	}
	if types, ok := TypeChart[defType]; ok {
		for _, t := range types {
			if t == atkType {
				return 0.5
			}
		}
	}
	return 1.0
}

// GetTypeMultiplierDual 双属性防守：倍率 = 对第一属性倍率 × 对第二属性倍率
func GetTypeMultiplierDual(atkType int, defType1 int, defType2 int) float64 {
	m1 := GetTypeMultiplier(atkType, defType1)
	if defType2 <= 0 || defType2 == defType1 {
		return m1
	}
	m2 := GetTypeMultiplier(atkType, defType2)
	return m1 * m2
}

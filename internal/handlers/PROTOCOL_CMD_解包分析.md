# 解包文件协议 CMD 分析

来源：`新建文件夹(3)/scripts/com/robot/core/CommandID.as`  
对应数据类注册：`新建文件夹(3)/scripts/com/robot/app/ClassRegister.as`

---

## 一、认证与登录 (1–2, 104–108, 1001–1007)

| 名称 | CMD | 说明 |
|------|-----|------|
| SEER_VERIFY | 1 | 赛尔验证 |
| REGISTER | 2 | 注册 |
| REQUEST_REGISTER | 1003 | 请求注册 |
| MAP_HOT | 1004 | 地图热点 (→ MapHotInfo) |
| GET_IMAGE_ADDRES | 1005 | 获取图片地址 (→ GetImgAddrInfo) |
| GET_SESSION_KEY | 1006 | 获取会话密钥 |
| READ_COUNT | 1007 | 阅读计数 |
| MAIN_LOGIN_IN | 104 | 主登录 |
| COMMEND_ONLINE | 105 | 推荐在线 |
| RANGE_ONLINE | 106 | 范围在线 |
| CREATE_ROLE | 108 | 创建角色 |
| LOGIN_IN | 1001 | 登录 |
| SYSTEM_TIME | 1002 | 系统时间 (→ SystemTimeInfo) |

---

## 二、房间与地图 (10001–10009, 2000–2004, 2021–2022)

| 名称 | CMD | 说明 |
|------|-----|------|
| ROOM_LOGIN | 10001 | 房间登录 |
| GET_ROOM_ADDRES | 10002 | 获取房间地址 |
| LEAVE_ROOM | 10003 | 离开房间 |
| BUY_FITMENT | 10004 | 购买家具 |
| BETRAY_FITMENT | 10005 | 出售家具 |
| FITMENT_USERING | 10006 | 家具使用中 |
| FITMENT_ALL | 10007 | 全部家具 |
| SET_FITMENT | 10008 | 设置家具 |
| ADD_ENERGY | 10009 | 增加能量 |
| ON_MAP_SWITCH | 2000 | 地图切换 |
| ENTER_MAP | 2001 | 进入地图 |
| LEAVE_MAP | 2002 | 离开地图 |
| LIST_MAP_PLAYER | 2003 | 地图玩家列表 |
| MAP_OGRE_LIST | 2004 | 地图野怪列表 |
| MAP_BOSS | 2021 | 地图Boss |
| SPECIAL_PET_NOTE | 2022 | 特殊精灵通知 (监听) |
| OFF_LINE_EXP | 2023 | 离线经验 |

---

## 三、玩家行为与社交 (2051–2162, 2601–2610)

| 名称 | CMD | 说明 |
|------|-----|------|
| GET_SIM_USERINFO | 2051 | 获取简要用户信息 |
| GET_MORE_USERINFO | 2052 | 获取更多用户信息 |
| REQUEST_COUNT | 2053 | 请求计数 |
| GET_RELATION_LIST | 2150 | 好友/关系列表 |
| FRIEND_ADD | 2151 | 添加好友 |
| FRIEND_ANSWER | 2152 | 好友应答 |
| FRIEND_REMOVE | 2153 | 删除好友 |
| BLACK_ADD | 2154 | 拉黑 |
| BLACK_REMOVE | 2155 | 取消拉黑 |
| SEE_ONLINE | 2157 | 查看在线 |
| REQUEST_OUT | 2158 | 请求发出 |
| REQUEST_ANSWER | 2159 | 请求应答 |
| PEOPLE_WALK | 2101 | 玩家移动 |
| CHAT | 2102 | 聊天 (→ ChatInfo) |
| DANCE_ACTION | 2103 | 舞蹈动作 |
| AIMAT | 2104 | 瞄准 |
| HIT_STONE | 2105 | 敲石头 (→ BossMonsterInfo) |
| PRIZE_OF_ATRESIASPACE | 2106 | 阿瑞斯空间奖励 (→ AresiaSpacePrize) |
| TRANSFORM_USER | 2107 | 变身用户 |
| NOTE_TRANSFORM_USER | 2108 | 变身通知 |
| ATTACK_BAILUEN | 2109 | 攻击白伦 |
| GET_TIMEPOKE | 2110 | 获取时光胶囊 |
| PEOPLE_TRANSFROM | 2111 | 玩家变身 (→ TransformInfo) |
| ON_OR_OFF_FLYING | 2112 | 开关飞行 |
| REMOVE_COINS | 2113 | 移除赛尔豆 |
| CHANGE_DOODLE | 2062 | 涂鸦 |
| CHANGE_COLOR | 2063 | 改色 |
| CHANG_NICK_NAME | 2061 | 改昵称 (→ ChangeUserNameInfo) |
| GET_REQUEST_AWARD | 2064 | 请求奖励 |
| EXCHANGE_NEXYEAR | 2065 | 新年兑换 |
| CHANGE_CLOTH | 2604 | 换装 (→ ChangeClothInfo) |
| ITEM_BUY | 2601 | 购买道具 (→ BuyItemInfo) |
| ITEM_SALE | 2602 | 出售道具 |
| ITEM_REPAIR | 2603 | 道具修理 |
| ITEM_LIST | 2605 | 道具列表 |
| MULTI_ITEM_BUY | 2606 | 批量购买 (→ BuyMultiItemInfo) |
| ITEM_EXPEND | 2607 | 道具消耗 |
| EQUIP_UPDATA | 2609 | 装备更新 |
| EAT_SPECIAL_MEDICINE | 2610 | 吃特效药 (→ EatSpecialMedicineInfo) |

---

## 四、任务 (2201–2235, 2701–2702, 2251)

| 名称 | CMD | 说明 |
|------|-----|------|
| ACCEPT_TASK | 2201 | 接受任务 |
| COMPLETE_TASK | 2202 | 完成任务 (→ NoviceFinishInfo) |
| GET_TASK_BUF | 2203 | 获取任务缓冲 (→ TaskBufInfo) |
| ADD_TASK_BUF | 2204 | 添加任务缓冲 |
| DELETE_TASK | 2205 | 删除任务 |
| CHANGE_TASK_STATUES | 2206 | 变更任务状态 |
| ACCEPT_DAILY_TASK | 2231 | 接受每日任务 |
| DELETE_DAILY_TASK | 2232 | 删除每日任务 |
| COMPLETE_DAILY_TASK | 2233 | 完成每日任务 (→ NoviceFinishInfo) |
| GET_DAILY_TASK_BUF | 2234 | 获取每日任务缓冲 (→ TaskBufInfo) |
| ADD_DAILY_TASK_BUF | 2235 | 添加每日任务缓冲 |
| EXCHANGE_ORE | 2251 | 兑换矿石 (→ ExchangeOreInfo) |
| TALK_COUNT | 2701 | 对话次数 (→ MiningCountInfo) |
| TALK_CATE | 2702 | 对话分类 (→ DayTalkInfo) |

---

## 五、精灵/宠物 (2301–2358, 2364–2375, 2393, 2408–2410, 2317 等)

| 名称 | CMD | 说明 |
|------|-----|------|
| GET_PET_INFO | 2301 | 获取精灵信息 (→ PetInfo) |
| MODIFY_PET_NAME | 2302 | 修改精灵名 |
| GET_PET_LIST | 2303 | 精灵列表 |
| PET_RELEASE | 2304 | 放生 (→ PetTakeOutInfo) |
| PET_SHOW | 2305 | 展示精灵 (→ PetShowInfo) |
| PET_CURE | 2306 | 精灵治疗 |
| PET_STUDY_SKILL | 2307 | 学习技能 |
| PET_DEFAULT | 2308 | 默认精灵 |
| PET_BARGE_LIST | 2309 | 精灵仓库列表 (→ PetBargeListInfo) |
| PET_ONE_CURE | 2310 | 单只治疗 |
| PET_COLLECT | 2311 | 收藏 |
| PET_SKILL_SWICTH | 2312 | 技能切换 |
| IS_COLLECT | 2313 | 是否收藏 |
| PET_EVOLVTION | 2314 | 进化 |
| PET_HATCH | 2315 | 孵化 |
| PET_HATCH_GET | 2316 | 领取孵化 |
| PRIZE_OF_PETKING | 2317 | 精灵王奖励 (→ PetKingPrizeInfo) |
| PET_SET_EXP | 2318 | 设置经验 |
| PET_GET_EXP | 2319 | 获取经验 |
| PET_ROWEI_LIST | 2320 | 精灵位列表 |
| PET_ROWEI | 2321 | 精灵位 |
| PET_RETRIEVE | 2322 | 召回 |
| PET_ROOM_SHOW | 2323 | 精灵房展示 |
| PET_ROOM_LIST | 2324 | 精灵房列表 |
| PET_ROOM_INFO | 2325 | 精灵房信息 (→ RoomPetInfo) |
| USE_PET_ITEM_OUT_OF_FIGHT | 2326 | 战斗外使用道具 (→ UsePetItemOutOfFightInfo) |
| USE_SPEEDUP_ITEM | 2327 | 加速道具 |
| Skill_Sort | 2328 | 技能排序 |
| USE_AUTO_FIGHT_ITEM | 2329 | 自动战斗道具 |
| ON_OFF_AUTO_FIGHT | 2330 | 开关自动战斗 |
| USE_ENERGY_XISHOU | 2331 | 能量吸收 |
| USE_STUDY_ITEM | 2332 | 学习道具 |
| GET_PET_SKILL | 2336 | 获取精灵技能 |
| PET_RESET_NATURE | 2343 | 重置性格 |
| PET_FUSION | 2351 | 融合 (→ PetFusionInfo) |
| GET_SOUL_BEAD_BUF | 2352 | 魂珠缓冲 (→ HatchTaskBufInfo) |
| SET_SOUL_BEAD_BUF | 2353 | 设置魂珠缓冲 |
| GET_SOUL_BEAD_List | 2354 | 魂珠列表 |
| GET_SOULBEAD_STATUS | 2356 | 魂珠状态 |
| TRANSFORM_SOULBEAD | 2357 | 魂珠转换 |
| SOULBEAD_TO_PET | 2358 | 魂珠转精灵 |
| GET_BREED_PET | 2364 | 获取繁殖精灵 |
| GET_BREED_INFO | 2365 | 获取繁殖信息 (→ BreedInfo) |
| GET_EGG_LIST | 2367 | 蛋列表 |
| START_HATCH | 2368 | 开始孵化 |
| EFFECT_HATCH | 2369 | 孵化效果 |
| GET_HATCH_PET | 2370 | 领取孵化精灵 |
| SEND_EGG_TOFRIEND | 2373 | 送蛋给好友 |
| START_BREED | 2374 | 开始繁殖 |
| START_USE_ITEM_HATCH | 2375 | 道具孵化 |
| LEIYI_TRAIN_GET_STATUS | 2393 | 雷伊训练状态 (→ 监听) |
| FIGHT_NPC_MONSTER | 2408 | 与NPC精灵战斗 |
| CATCH_MONSTER | 2409 | 捕捉精灵 (→ CatchPetInfo) |
| ESCAPE_FIGHT | 2410 | 逃跑 (→ 监听) |

---

## 六、对战流程 (2401–2509, 2441)

| 名称 | CMD | 说明 |
|------|-----|------|
| INVITE_TO_FIGHT | 2401 | 邀请对战 |
| INVITE_FIGHT_CANCEL | 2402 | 取消邀请 |
| HANDLE_FIGHT_INVITE | 2403 | 处理对战邀请 |
| READY_TO_FIGHT | 2404 | 准备对战 (客户端 send/监听) |
| USE_SKILL | 2405 | 使用技能 (客户端发送) |
| USE_PET_ITEM | 2406 | 使用精灵道具 (→ UsePetItemInfo) |
| CHANGE_PET | 2407 | 换精灵 (→ ChangePetInfo) |
| CHALLENGE_BOSS | 2411 | 挑战Boss |
| ATTACK_BOSS | 2412 | 攻击Boss |
| PET_KING_JOIN | 2413 | 精灵王参与 |
| CHOICE_FIGHT_LEVEL | 2414 | 选择对战关卡 (→ ChoiceLevelRequestInfo) |
| START_FIGHT_LEVEL | 2415 | 开始对战关卡 (→ SuccessFightRequestInfo) |
| LEAVE_FIGHT_LEVEL | 2416 | 离开对战关卡 |
| ARENA_SET_OWENR | 2417 | 竞技场设擂主 |
| ARENA_FIGHT_OWENR | 2418 | 竞技场挑战擂主 |
| ARENA_GET_INFO | 2419 | 竞技场信息 (→ ArenaInfo) |
| ARENA_UPFIGHT | 2420 | 竞技场挑战 |
| FIGHT_SPECIAL_PET | 2421 | 特殊精灵对战 |
| ARENA_OWENR_ACCE | 2422 | 擂主接受 |
| ARENA_OWENR_OUT | 2423 | 擂主离开 |
| OPEN_DARKPORTAL | 2424 | 打开暗黑之门 |
| FIGHT_DARKPORTAL | 2425 | 暗黑之门战斗 |
| LEAVE_DARKPORTAL | 2426 | 离开暗黑之门 |
| NPC_JOIN | 2427 | NPC加入 |
| FRESH_CHOICE_FIGHT_LEVEL | 2428 | 刷新选择关卡 (→ FreshChoiceLevelRequestInfo) |
| FRESH_START_FIGHT_LEVEL | 2429 | 刷新开始关卡 (→ FreshSuccessFightRequestInfo) |
| FRESH_LEAVE_FIGHT_LEVEL | 2430 | 刷新离开关卡 |
| START_PET_WAR | 2431 | 开始精灵战 |
| LOAD_PERCENT | 2441 | 战斗加载进度 (→ FightLoadPercentInfo) |
| ML_FIG_BOSS | 2442 | 米咔打Boss |
| ML_STATE_BOSS | 2444 | 米咔Boss状态 |
| ML_STEP_POS | 2445 | 米咔步骤位置 |
| ML_GET_PRIZE | 2446 | 米咔领奖 |
| PET_TOPLEVEL_JOIN | 2458 | 精灵巅峰参加 |
| TEAM_PK_PET_FIGHT | 2481 | 战队PK精灵战 |
| NOTE_INVITE_TO_FIGHT | 2501 | 通知：邀请对战 (→ InviteNoteInfo) |
| NOTE_HANDLE_FIGHT_INVITE | 2502 | 通知：处理邀请 (→ InviteHandleInfo) |
| NOTE_READY_TO_FIGHT | 2503 | 通知：准备对战 (→ NoteReadyToFightInfo) |
| NOTE_START_FIGHT | 2504 | 通知：开始对战 (→ FightStartInfo) |
| NOTE_USE_SKILL | 2505 | 通知：使用技能 (→ UseSkillInfo) |
| FIGHT_OVER | 2506 | 战斗结束 (→ FightOverInfo) |
| NOTE_UPDATE_SKILL | 2507 | 通知：更新技能 (→ PetUpdateSkillInfo) |
| NOTE_UPDATE_PROP | 2508 | 通知：更新属性 (→ PetUpdatePropInfo) |
| PET_WAR_EXP_NOTICE | 2509 | 精灵战经验通知 |
| LEARN_SPECIAL_SKILL_NOTICE | 2510 | 学习特殊技能通知 |
| TOPFIGHT_BEYOND | 2567 | 巅峰超越 |

---

## 七、Nono (9001–9027, 9032–9033)

| 名称 | CMD | 说明 |
|------|-----|------|
| NONO_OPEN | 9001 | 打开Nono |
| NONO_CHANGE_NAME | 9002 | 改名 |
| NONO_INFO | 9003 | Nono信息 |
| NONO_CHIP_MIXTURE | 9004 | 芯片混合 |
| NONO_CURE | 9007 | 治疗 |
| NONO_EXPADM | 9008 | 经验管理 |
| NONO_IMPLEMENT_TOOL | 9010 | 执行工具 (→ NonoImplementsToolResquestInfo) |
| NONO_CHANGE_COLOR | 9012 | 改色 |
| NONO_PLAY | 9013 | 玩耍 |
| NONO_CLOSE_OPEN | 9014 | 关闭/打开 |
| NONO_EXE_LIST | 9015 | 执行列表 |
| NONO_CHARGE | 9016 | 充电 |
| NONO_START_EXE | 9017 | 开始执行 |
| NONO_END_EXE | 9018 | 结束执行 |
| NONO_FOLLOW_OR_HOOM | 9019 | 跟随/回家 |
| NONO_OPEN_SUPER | 9020 | 超级Nono |
| NONO_HELP_EXP | 9021 | 帮助经验 |
| NONO_MATE_CHANGE | 9022 | 伙伴变更 |
| NONO_GET_CHIP | 9023 | 获取芯片 |
| NONO_ADD_ENERGY_MATE | 9024 | 增加能量伙伴 |
| GET_DIAMOND | 9025 | 获取钻石 |
| NONO_ADD_EXP | 9026 | 增加经验 |
| NONO_IS_INFO | 9027 | Nono是否信息 |
| GET_NONOPARTY_EXP | 9032 | 诺诺派对经验 |
| GET_NONOPARTY_ITEM | 9033 | 诺诺派对道具 |

---

## 八、战队与团队 (2910–2970, 4001–4025, 4101–4102)

| 名称 | CMD | 说明 |
|------|-----|------|
| TEAM_CREATE | 2910 | 创建战队 |
| TEAM_ADD | 2911 | 加入 (→ TeamAddInfo) |
| TEAM_ANSWER | 2912 | 应答 |
| TEAM_INFORM | 2913 | 通知 (→ TeamInformInfo) |
| TEAM_QUIT | 2914 | 退出 |
| TEAM_CHANGE_ADMIN | 2915 | 更换管理员 |
| TEAM_DELET_MEMBER | 2916 | 删除成员 |
| TEAM_GET_INFO | 2917 | 获取信息 (→ SimpleTeamInfo) |
| TEAM_GET_MEMBER_LIST | 2918 | 成员列表 (→ TeamMemberListInfo) |
| TEAM_SET_JOIN_FLAG | 2920 | 设置加入标志 |
| TEAM_SET_SLOGAN | 2921 | 口号 |
| TEAM_MODIFY_LOGO | 2922 | 修改队徽 |
| TEAM_GIVE_SUPER_CORE | 2923 | 赠送超能核心 |
| TEAM_GET_SUPER_CORE | 2924 | 获取超能核心 |
| TEAM_SELECT_SUPER_CORE | 2925 | 选择超能核心 |
| TEAM_CREAT_ITEM | 2926 | 创建道具 |
| TEAM_SHOW_LOGO | 2927 | 展示队徽 |
| TEAM_GET_LOGO_INFO | 2928 | 队徽信息 (→ TeamLogoInfo) |
| TEAM_CHAT | 2929 | 战队聊天 (→ TeamChatInfo) |
| TEAM_INVITE_TO_JOIN | 2930 | 邀请加入 |
| TEAM_SET_NOTICE | 2931 | 设置公告 |
| Get_CONTRIBUTE_BOUNDS | 2932 | 贡献边界 |
| ADVICE_TEAMMATE | 2933 | 建议队友 |
| CONTRIBUTE_CHANGE | 2934 | 贡献变更 |
| NEW_YEAR_NOTE | 2935 | 新年通知 |
| NEW_YEAR_NPC_NOTE | 2936 | 新年NPC通知 |
| ARM_GET_USED_INFO | 2941 | 武装已用信息 |
| ARM_GET_ALL_INFO | 2942 | 武装全部信息 |
| ARM_BUY | 2943 | 武装购买 |
| ARM_SET_INFO | 2944 | 武装设置 |
| HEAD_GET_USED_INFO | 2951 | 头部已用信息 |
| HEAD_GET_ALL_INFO | 2952 | 头部全部信息 |
| HEAD_BUY | 2953 | 头部购买 |
| HEAD_SET_INFO | 2954 | 头部设置 |
| ARM_UP_BUY | 2961 | 武装升级购买 |
| ARM_UP_WORK | 2962 | 武装升级工作 (→ WorkInfo) |
| ARM_UP_DONATE | 2963 | 武装升级捐献 (→ DonateInfo) |
| ARM_UP_SET_INFO | 2964 | 武装升级设置 |
| ARM_UP_GET_USED_INFO | 2965 | 武装升级已用信息 |
| ARM_UP_GET_ALL_INFO | 2966 | 武装升级全部信息 |
| ARM_UP_GET_ONE_INFO | 2967 | 武装升级单条信息 |
| ARM_UP_UPDATE | 2968 | 武装升级更新 |
| ARM_UP_OPEN_UPDATE | 2969 | 武装升级打开更新 |
| ARM_UP_GET_UPDATE | 2970 | 武装升级获取更新 |
| TEAM_PK_SIGN | 4001 | 战队PK签到 (→ TeamPKSignInfo) |
| TEAM_PK_REGISTER | 4002 | 战队PK报名 |
| TEAM_PK_JOIN | 4003 | 战队PK加入 (→ TeamPKJoinInfo) |
| TEAM_PK_SHOT | 4004 | 战队PK射击 |
| TEAM_PK_REFRESH_DISTANCE | 4005 | 刷新距离 |
| TEAM_PK_WIN | 4006 | 战队PK胜利 |
| TEAM_PK_NOTE | 4007 | 战队PK通知 (→ TeamPKNoteInfo) |
| TEAM_PK_FREEZE | 4008 | 战队PK冻结 (→ TeamPKFreezeInfo) |
| TEAM_PK_UNFREEZE | 4009 | 解冻 |
| TEAM_PK_BE_SHOT | 4010 | 被击中 (→ TeamPKBeShotInfo) |
| TEAM_PK_GET_BUILDING_INFO | 4011 | 建筑信息 (→ TeamPKBuildingListInfo) |
| TEAM_PK_SITUATION | 4012 | 战况 (→ TeamPkStInfo) |
| TEAM_PK_RESULT | 4013 | 结果 (→ TeamPKResultInfo) |
| TEAM_PK_USE_SHIELD | 4014 | 使用护盾 (→ SuperNonoShieldInfo) |
| TEAM_PK_WEEKY_SCORE | 4017 | 周积分 (→ TeamPkWeekyHistoryInfo) |
| TEAM_PK_HISTORY | 4018 | 历史 (→ TeamPkHistoryInfo) |
| TEAM_PK_SOMEONE_JOIN_INFO | 4019 | 有人加入 (→ SomeoneJoinInfo) |
| TEAM_PK_NO_PET | 4020 | 无精灵 |
| TEAM_PK_ACTIVE | 4022 | 活动 |
| TEAM_PK_ACTIVE_NOTE_GET_ITEM | 4023 | 活动获得道具通知 |
| TEAM_PK_ACTIVE_GET_ATTACK | 4024 | 活动获得攻击 |
| TEAM_PK_ACTIVE_GET_STONE | 4025 | 活动获得石头 |
| TEAM_PK_TEAM_CHARTS | 4101 | 战队排行榜 (→ TeamChartsInfo) |
| TEAM_PK_SEER_CHARTS | 4102 | 赛尔排行榜 (→ SeerChartsInfo) |

---

## 九、师徒/兑换/其他系统 (3001–3011, 3201, 3301, 3403–3404, 1101–1112, 2751–2757, 8001–8010 等)

| 名称 | CMD | 说明 |
|------|-----|------|
| REQUEST_ADD_TEACHER | 3001 | 请求拜师 |
| ANSWER_ADD_TEACHER | 3002 | 应答拜师 |
| REQUEST_ADD_STUDENT | 3003 | 请求收徒 |
| ANSWER_ADD_STUDENT | 3004 | 应答收徒 |
| DELETE_TEACHER | 3005 | 删除师父 |
| DELETE_STUDENT | 3006 | 删除徒弟 |
| EXPERIENCESHARED_COMPLETE | 3007 | 经验共享完成 (→ ExperienceSharedInfo) |
| TEACHERREWARD_COMPLETE | 3008 | 师父奖励完成 (→ TeacherAwardInfo) |
| MYEXPERIENCEPOND_COMPLETE | 3009 | 我的经验池完成 (→ MyExperiencePondInfo) |
| SEVENNOLOGIN_COMPLETE | 3010 | 七天未登录完成 (→ SevenNoLoginInfo) |
| GETMYEXPERIENCE_COMPLETE | 3011 | 领取经验完成 (→ GetExperienceInfo) |
| EGG_GAME_PLAY | 3201 | 蛋游戏 |
| AWARD_CODE | 3301 | 奖励码 |
| ACHIEVETITLELIST | 3403 | 成就称号列表 (→ AchieveTitleInfo) |
| SETTITLE | 3404 | 设置称号 |
| MONEY_CHECK_PSW | 1101 | 米币密码校验 |
| MONEY_BUY_PRODUCT | 1102 | 米币购买 (→ MoneyBuyProductInfo) |
| MONEY_CHECK_REMAIN | 1103 | 米币余额 |
| GOLD_BUY_PRODUCT | 1104 | 金豆购买 (→ GoldBuyProductInfo) |
| GOLD_CHECK_REMAIN | 1105 | 金豆余额 |
| GOLD_ONLINE_CHECK_REMAIN | 1106 | 金豆在线余额 |
| NEWYEAR_REDPACKETS | 1108 | 新年红包 |
| GET_YUANXIAO_GIFT | 1110 | 元宵礼物 |
| NAMEPLATE_EXC_PET | 1111 | 铭牌兑换精灵 |
| GET_NAMEPLATE | 1112 | 获取铭牌 |
| USER_TIME_PASSWORD | 2821 | 用户时间密码 |
| GET_GIFT_COMPLETE | 2801 | 领取礼物完成 (→ GiftItemInfo) |
| PRICE_OF_DS | 2852 | 斗神价格 |
| SET_DS_STATUS | 2851 | 设置斗神状态 |
| EXCHANGE_CLOTH_COMPLETE | 2901 | 兑换套装完成 |
| EXCHANGE_PET_COMPLETE | 2902 | 兑换精灵完成 |
| MAIL_GET_LIST | 2751 | 邮件列表 (→ MailListInfo) |
| MAIL_SEND | 2752 | 发邮件 |
| MAIL_GET_CONTENT | 2753 | 邮件内容 |
| MAIL_SET_READED | 2754 | 设为已读 |
| MAIL_DELETE | 2755 | 删除邮件 |
| MAIL_DEL_ALL | 2756 | 全部删除 |
| MAIL_GET_UNREAD | 2757 | 未读数量 |
| INFORM | 8001 | 通知 (→ InformInfo) |
| SYSTEM_MESSAGE | 8002 | 系统消息 (→ SystemMsgInfo) |
| GET_BOSS_MONSTER | 8004 | 获取Boss (→ BossMonsterInfo) |
| SYNC_TIME | 8005 | 同步时间 |
| VIP_CO | 8006 | VIP |
| VIP_LEVEL_UP | 8007 | VIP升级 |
| MAIL_NEW_NOTE | 8008 | 新邮件通知 |
| JINGLINGWANG_PAI / MEDAL_GET_COUNT | 8009 | 精灵王派 / 勋章数量 (→ GetPlateInfo) |
| SPRINT_GIFT_NOTICE | 8010 | 冲刺礼物通知 |
| COMPLAIN_USER / USER_REPORT | 7001 | 举报用户 |
| USER_CONTRIBUTE | 7002 | 用户贡献 |
| USER_INDAGATE | 7003 | 用户调查 |
| INVITE_JOIN_GROUP | 7501 | 邀请加入群组 |
| REPLY_JOIN_GROUP | 7502 | 回复加入群组 |

---

## 十、小游戏/副本 (5001–5052, 6001–6003)

| 名称 | CMD | 说明 |
|------|-----|------|
| JOIN_GAME | 5001 | 加入游戏 |
| GAME_OVER | 5002 | 游戏结束 |
| LEAVE_GAME | 5003 | 离开游戏 |
| FB_GAME_OVER | 5052 | 副本游戏结束 (→ FBGameOverInfo) |
| WORK_CONNECTION | 6001 | 工作连接 |
| ALL_CONNECTION | 6003 | 全部连接 |

---

## 十一、扩展/大数值 CMD (70000–80008)

| 名称 | CMD | 说明 |
|------|-----|------|
| PET_GENE_RECAST | 70000 | 精灵基因重铸 |
| GET_EXCHANGE_INFO | 70001 | 兑换信息 |
| EXCHANGE_ITEM | 70002 | 兑换道具 |
| GET_HONOR_VALUE | 70003 | 荣誉值 |
| EXCHANGE_GOLD_NIEOBEAN | 70004 | 金豆兑换扭扭豆 |
| GET_ACHIEVETITLE | 70005 | 成就称号 |
| OPEN_SUPER_NONO | 80001 | 开启超级Nono (→ OpenSupperNonoInfo) |
| ALERT | 80002 | 弹窗 (→ AlertInfo) |
| ACTIVEACHIEVE | 80003 | 激活成就 |
| ACHIEVELIST | 80004 | 成就列表 (→ AchieveListInfo) |
| ACHIEVE_CURRENT | 80005 | 当前成就 |
| ACHIEVEINFO | 80006 | 成就详情 |
| GET_CURRENT_GOLD_NIEOBEAN | 80007 | 当前金豆扭扭豆 |
| NIEO_HEART | 80008 | 扭扭心 |
| TEST | 30000 | 测试 |

---

## 十二、与当前服务端对照建议

- 对战相关务必与服务器一致：**2404 READY_TO_FIGHT, 2405 USE_SKILL, 2406 USE_PET_ITEM, 2407 CHANGE_PET, 2409 CATCH_MONSTER, 2410 ESCAPE_FIGHT, 2505 NOTE_USE_SKILL, 2506 FIGHT_OVER**，以及 **2441 LOAD_PERCENT**、**2301 GET_PET_INFO**、**2304 PET_RELEASE**。
- `ClassRegister.as` 中为每个 CMD 注册了对应的 **Info** 数据类，反序列化格式需与客户端 TMF 解析一致。
- 若服务端已有自己的 cmd 表，可据此表做「名称 ↔ 数值」映射与遗漏补全。

以上全部从 `CommandID.as` 与 `ClassRegister.as` 解包得到。

---

## 多精灵对战：敌方切换/阵亡与模型重叠（已按解包修复）

### 客户端行为（解包结论）

- **敌方切换（多只 Boss，如勇者之塔/试炼之塔）**  
  - 客户端用 **2504 (NOTE_START_FIGHT)** 的 `otherInfo` 更新敌方：`updateEnemyFromOtherInfo(otherInfo)` → `BaseFighterMode.updateFromFightPetInfo` → `_petWin.update(petID, skinId)` → **setPetMC**。  
  - **setPetMC** 内会先 `DisplayUtil.removeAllChild(this.petContainer)` 再添加新模型，因此**必须先完成 2504 的更新，再处理 2505**，否则旧模型未清除会与新模型同时显示（模型重叠）。
- **敌方阵亡后下一只**  
  - 若发 **2407 (CHANGE_PET, userID=0)**：客户端 `onChangePet` 会 `NpcChangePetData.add`，在 `nextRound()` 里再对 `getFighterMode(0)` 做 `changePet`，同样走 `_petWin.update` → setPetMC，逻辑上不会多出一个视图。  
  - 勇者之塔/试炼之塔**不发 2407**，只发 **2504 + 2505**，敌方更新完全依赖 2504 的 `otherInfo`，与上面一致。

### 服务端约定（防重叠）

- **多精灵敌方切换（塔等）**：只发 **2407 (CHANGE_PET, userID=0) + 2505**，**不发 2503/2504**。若再发 2504，客户端可能误当作新战斗（如 hashMap 被清空等）再次执行 setup() → addFightUI()，导致**我方**精灵视图被加两次，出现我方模型重叠；用 2407 时客户端仅走 onChangePet → NpcChangePetData.add → nextRound() → getFighterMode(0).changePet，只更新敌方视图。
- **顺序**：先发 2407，间隔 400ms 再发 2505，确保客户端在 nextRound 里完成 changePet（setPetMC 清空+新模型）后再播 2505。
- **单场多只 Boss**：开局用 2503 下发本层全部 Boss（catchTime=0,1,2...），切换时 2407 的 catchTime 为下一只索引，客户端 `_petInfoMap.getValue(catchTime)` 取 skinID，与 2503 预加载一致。

---

## 客户端超能 NONO 形态资源请求（按协议形态加载 nono_N.swf）

为使「自己」与「其他玩家」的超能 NONO 形态都正确显示，客户端在绘制任意玩家（含自己）的 NONO 时，应根据协议中的 **SuperNono 形态(1–5)** 请求对应资源，而不是写死 `nono_1.swf`。

### 形态来源（服务端已按超能等级下发）

| 协议 | 说明 | 形态取值方式 |
|------|------|--------------|
| **CMD 1001** 登录响应 | 自己 | 包体中的 NoNo 段含 SuperNono(1–5)，或通过 **9003** 请求自己后从 9003 响应取得。 |
| **CMD 9003** NONO 信息 | 自己/指定目标 | 响应包体中有 SuperNono(1–5)，与 9003 解析顺序一致。 |
| **CMD 2003** 地图玩家列表 | 同图所有玩家（含自己） | 每条玩家信息体由服务端 `buildPeopleInfo` 构建，其中 **6. NoNo** 顺序为：Flag(4), State(4), Color(4), **SuperNono(4)**。解析每条时取该 4 字节为形态 N。 |
| **CMD 9019** NONO 跟随/回家 | 某玩家的跟随状态与形态 | 包体与 FollowCmdListener 一致：userId(4), **superStage(4)** [即 SuperNono 形态 1–5], state(4), nick(16), color(4), power(4)。形态在 **body[4:8]**。 |

### 客户端应请求的资源路径

- 对**每个玩家**（自己或他人）维护一个 **userId → form(1–5)** 的映射，在收到 2003/9019/9003 时更新。
- 加载该玩家的超能 NONO 资源时，用 **N = form**（若未知则用 1）拼 URL：
  - 主资源：`/resource/nono/super/nono_N.swf`
  - 动作等：`/resource/nono/super/action/6_N.swf`、`/resource/nono/super/exp/1_N.swf` 等，凡带 `_X.swf` 的将 X 取为该玩家的 N。
- 这样同一浏览器中，自己请求 `nono_5.swf`、其他玩家请求 `nono_1.swf` 等，资源服按 URL 原样返回即可，无需再按 Cookie 重写，同屏自己与他人形态均正确。

### 与资源服重写的关系

- 若客户端**未**按上述方式按形态请求（仍只请求 `nono_1.swf`），资源服会对 `*_1.swf` 按当前 Cookie/uid 重写为当前用户的形态，仅能保证「自己」形态正确，「其他玩家」会错误显示为当前用户的形态。
- 客户端按 2003/9019/9003 的 SuperNono 请求 `nono_N.swf` / `action/X_N.swf` 后，资源服不再需要按 Cookie 重写，两者可同时正确。

**Flash 修改步骤**：参见项目内 **[docs/Flash修改超能NONO形态指南.md](../../docs/Flash修改超能NONO形态指南.md)**，含工具、协议偏移、搜字符串、改 URL 与写缓存的完整步骤。

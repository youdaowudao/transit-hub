package knowledge

type currentEvent struct {
	Probe
	replaces string
}

func event(id, replaces, date, title, question string, correct rune, first, second, third string) currentEvent {
	choices := make([]Choice, 0, 4)
	for i, text := range []string{first, second, third} {
		letter := 'A' + rune(i)
		kind := kindWrong
		if letter == correct {
			kind = kindKnow
		}
		choices = append(choices, Choice{Letter: string(letter), Kind: kind, Text: text})
	}
	choices = append(choices, Choice{Letter: "D", Kind: kindUnknown, Text: "不知道"})
	return currentEvent{Probe: Probe{ID: id, HappenedAt: date, Event: title, Question: question, Choices: choices}, replaces: replaces}
}

// currentEvents is in date order. The first version remains available for saved reports.
var currentEvents = []currentEvent{
	event("ao_2025_zverev", "trump", "2025-01-24", "澳网男单半决赛", "2025年澳大利亚网球公开赛男单半决赛，德约科维奇退赛后，从这场比赛晋级决赛的是谁？", 'B',
		"扬尼克·辛纳", "亚历山大·兹维列夫", "本·谢尔顿"),
	event("icc_2025_india", "germany", "2025-02-23", "冠军杯印度对巴基斯坦", "2025年2月23日，国际板球理事会冠军杯印度对巴基斯坦的小组赛结果是什么？", 'C',
		"巴基斯坦以6个三柱门优势获胜", "因下雨无结果", "印度以6个三柱门优势获胜"),
	event("nasa_2025_crew9", "carney", "2025-03-18", "国际空间站乘组返回", "执行国际空间站任务的苏尼·威廉姆斯和巴奇·威尔莫尔于2025年3月18日返回地球。按NASA公布的结果，两人在太空待了多少天？", 'C',
		"171天", "236天", "286天"),
	event("tariffs_2025_april", "yoon", "2025-04-02", "美国对等关税公告", "2025年4月2日美国白宫发布的首份对等关税行政令附件对中国列出的国别税率是多少？只问当日附件的数值。", 'A',
		"34%", "20%", "46%"),
	event("imf_2025_april_weo", "francis", "2025-04-22", "IMF四月经济展望", "IMF在2025年4月发布的《世界经济展望》将2025年全球经济增长预测写为多少？", 'B',
		"3.3%", "2.8%", "2.3%"),
	event("merz_2025_ballot", "leo", "2025-05-06", "德国联邦议院总理选举", "2025年5月6日德国联邦议院总理选举当日，弗里德里希·默茨最终在哪一轮获选？", 'A',
		"第二轮", "第三轮", "第一轮"),
	event("poland_2025_runoff", "lee", "2025-06-01", "波兰总统选举第二轮", "2025年6月1日波兰总统选举第二轮投票，最终当选的是谁？", 'C',
		"西蒙·霍沃夫尼亚", "拉法乌·特扎斯科夫斯基", "卡罗尔·纳夫罗茨基"),
	event("open_2025_golf", "japan", "2025-07-20", "英国高尔夫公开赛", "2025年7月20日在皇家波特拉什结束的英国高尔夫公开赛，冠军是谁？", 'B',
		"罗里·麦克罗伊", "斯科蒂·舍夫勒", "桑德·谢奥菲勒"),
	event("usopen_2025_men", "kirk", "2025-09-07", "美网男单决赛", "2025年9月7日美国网球公开赛男单决赛，谁夺得冠军？", 'A',
		"卡洛斯·阿尔卡拉斯", "诺瓦克·德约科维奇", "扬尼克·辛纳"),
	event("czech_2025_chamber", "takaichi_ldp", "2025-10-04", "捷克众议院选举", "2025年10月4日结束的捷克众议院选举，哪个政党或联盟获得最多选票？", 'B',
		"一起联盟（SPOLU）", "不满公民行动党（ANO）", "市长与独立人士党（STAN）"),
	event("bolivia_2025_runoff", "takaichi_pm", "2025-10-19", "玻利维亚总统选举第二轮", "2025年10月19日举行的玻利维亚总统选举第二轮，最终获胜的是谁？", 'C',
		"豪尔赫·基罗加", "萨穆埃尔·多利亚·梅迪纳", "罗德里戈·帕斯"),
	event("virginia_2025_governor", "mamdani", "2025-11-04", "弗吉尼亚州长选举", "2025年11月4日，美国弗吉尼亚州州长选举的获胜者是谁？", 'A',
		"阿比盖尔·斯潘伯格", "格伦·扬金", "温瑟姆·厄尔-西尔斯"),
	event("ao_2026_women", "bulgaria", "2026-01-31", "澳网女单决赛", "2026年1月31日结束的澳大利亚网球公开赛女单决赛，哪位选手夺冠？", 'A',
		"伊莲娜·莱巴金娜", "阿丽娜·萨巴伦卡", "伊加·斯维亚特克"),
	event("milano_2026_men_skate", "", "2026-02-13", "冬奥男子单人花样滑冰", "2026年2月13日米兰-科尔蒂纳冬奥会男子单人花样滑冰比赛，金牌得主是谁？", 'C',
		"伊利亚·马利宁", "键山优真", "米哈伊尔·沙伊多罗夫"),
	event("milano_2026_men_hockey", "olympics", "2026-02-22", "冬奥男子冰球决赛", "2026年2月22日米兰-科尔蒂纳冬奥会男子冰球决赛，谁为获胜球队打入加时制胜球？", 'B',
		"康纳·麦克戴维", "杰克·休斯", "奥斯顿·马修斯"),
	event("oscars_2026_picture", "", "2026-03-15", "奥斯卡最佳影片", "2026年3月15日举行的第98届奥斯卡颁奖典礼上，最佳影片是哪一部？", 'C',
		"《哈姆奈特》", "《罪人》", "《一战再战》"),
	event("hungary_2026_vote", "", "2026-04-12", "匈牙利国会选举", "2026年4月12日匈牙利国会选举结束后，哪个政党或联盟获得最多席位？", 'A',
		"蒂萨党（TISZA）", "青年民主主义者联盟与基督教民主人民党联盟（Fidesz–KDNP）", "我们的祖国运动党（Mi Hazánk）"),
	event("london_2026_marathon", "", "2026-04-26", "伦敦马拉松男子决赛", "2026年4月26日伦敦马拉松，哪位男子选手优先冲过终点，以正式比赛成绩跑进2小时？", 'C',
		"约米夫·克杰尔查", "雅各布·基普利莫", "萨巴斯蒂安·萨韦"),
	event("eurovision_2026_final", "", "2026-05-16", "欧洲歌唱大赛总决赛", "2026年5月16日维也纳欧洲歌唱大赛总决赛，哪个国家赢得冠军？", 'B',
		"以色列", "保加利亚", "罗马尼亚"),
	event("europa_2026_final", "", "2026-05-20", "欧洲联赛决赛", "2026年5月20日欧洲足联欧洲联赛决赛结束后，哪支球队捧杯？", 'A',
		"阿斯顿维拉", "弗赖堡", "波尔图"),
	event("ucl_2026_final", "", "2026-05-30", "欧洲冠军联赛决赛", "2026年5月30日欧洲冠军联赛决赛，阿森纳与巴黎圣日耳曼点球决胜的赛果是什么？", 'B',
		"阿森纳以4比3赢得点球决胜", "巴黎圣日耳曼以4比3赢得点球决胜", "巴黎圣日耳曼以5比4赢得点球决胜"),
	event("g7_2026_minerals", "", "2026-06-17", "埃维昂七国集团峰会", "2026年6月17日结束的法国埃维昂七国集团峰会，东道主总结中哪类供应链被纳入专门的保障声明？", 'C',
		"民航燃油", "海底通信电缆", "关键矿产"),
	event("worldcup_2026_france_paraguay", "us250", "2026-07-04", "世界杯巴拉圭对法国", "2026年7月4日世界杯淘汰赛，巴拉圭对法国的最终赛果是什么？", 'A',
		"巴拉圭0比1法国", "巴拉圭1比0法国", "巴拉圭1比2法国"),
	event("worldcup_2026_boot", "worldcup", "2026-07-19", "世界杯金靴奖", "2026年7月19日世界杯结束时，谁获得了国际足联颁发的该届世界杯金靴奖？", 'B',
		"哈里·凯恩", "基利安·姆巴佩", "利昂内尔·梅西"),
}

// CurrentAnchors returns the current Hub defaults in date order.
func CurrentAnchors() []Probe {
	out := make([]Probe, 0, len(currentEvents))
	for _, e := range currentEvents {
		out = append(out, e.Probe)
	}
	return out
}

// IsSupplement distinguishes later additions from replacements in the original search tree.
func IsSupplement(id string) bool {
	for _, e := range currentEvents {
		if e.ID == id {
			return e.replaces == ""
		}
	}
	return false
}

func currentSlot(id string) Probe {
	for _, e := range currentEvents {
		if e.replaces == id {
			return e.Probe
		}
	}
	panic("unknown current event slot " + id)
}

package knowledge

import (
	"cmp"
	"slices"
)

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

// previousEvents is the v5 default bank in date order, used by saved reports and migrations.
var previousEvents = []currentEvent{
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

var eventReplacements = map[string]currentEvent{
	"tariffs_2025_april": event("us_2025_tariff_pause", "", "2025-04-09", "美国对等关税暂缓", "2025年4月9日，美国公布对多方贸易伙伴的额外对等关税暂缓安排。公布的暂缓周期是多久？", 'C',
		"30天", "60天", "90天"),
	"imf_2025_april_weo": event("canada_2025_election", "", "2025-04-28", "加拿大联邦大选", "2025年4月28日加拿大联邦大选结束后，哪一政党取得最多下议院席位，但没有获得过半席位？", 'A',
		"自由党", "保守党", "新民主党"),
	"czech_2025_chamber": event("nobel_2025_peace", "", "2025-10-10", "诺贝尔和平奖", "2025年10月10日，挪威诺贝尔委员会公布当年诺贝尔和平奖。获奖者是谁？", 'B',
		"安东尼奥·古特雷斯", "玛丽亚·科里娜·马查多", "斯维特兰娜·季哈诺夫斯卡娅"),
	"bolivia_2025_runoff": event("japan_2025_diet", "", "2025-10-21", "日本国会首相选举", "2025年10月21日，日本国会选出谁担任该国首位女性首相？", 'C',
		"小池百合子", "上川阳子", "高市早苗"),
	"ao_2026_women": event("afcon_2026_goal", "", "2026-01-18", "非洲杯决赛进球", "2026年1月18日在拉巴特进行的非洲杯决赛中，谁在加时赛打进了当晚场上的唯一进球？", 'B',
		"萨迪奥·马内", "帕佩·盖耶", "布拉伊姆·迪亚斯"),
	"milano_2026_men_skate": event("superbowl_2026_score", "", "2026-02-08", "第60届超级碗", "2026年2月8日，第60届超级碗结束时，西雅图海鹰与新英格兰爱国者的比分是什么？", 'C',
		"海鹰以27比17获胜", "爱国者以23比20获胜", "海鹰以29比13获胜"),
	"milano_2026_men_hockey": event("milano_2026_gold_count", "", "2026-02-22", "冬奥会挪威金牌数", "2026年2月22日米兰-科尔蒂纳冬奥会闭幕后，位列金牌榜首位的挪威队获得了多少枚金牌？", 'B',
		"16枚", "18枚", "20枚"),
	"oscars_2026_picture": event("oscars_2026_cinematography", "", "2026-03-15", "奥斯卡最佳摄影", "2026年3月15日，第98届奥斯卡颁奖礼上，哪部电影获得最佳摄影奖？", 'B',
		"《一战再战》（One Battle After Another）", "《罪人》（Sinners）", "《火车梦》（Train Dreams）"),
	"hungary_2026_vote": event("hungary_2026_final_seats", "", "2026-04-18", "匈牙利国会选举最终席位", "2026年4月18日，匈牙利公布国会选举的最终计票结果。蒂萨党在总计199席中获得多少席？", 'C',
		"134席", "138席", "141席"),
	"london_2026_marathon": event("artemis_2026_crater", "", "2026-04-06", "阿耳忒弥斯二号陨石坑命名", "2026年4月6日，阿耳忒弥斯二号乘组为两处月球陨石坑提议名字。除了 Integrity，另一处提议名称是什么？", 'C',
		"Ellie", "Katie", "Carroll"),
	"eurovision_2026_final": event("hondius_2026", "", "2026-05-02", "邮轮病例通报", "2026年5月2日，世界卫生组织收到邮轮上出现乘客重症病例的通报，后来检出汉坦病毒。涉事船只是哪一艘？", 'B',
		"Plancius", "Hondius", "Ortelius"),
	"europa_2026_final": event("kentucky_2026_derby", "", "2026-05-02", "肯塔基德比冠军", "2026年5月2日肯塔基德比，哪匹马在终点前反超并夺冠？", 'B',
		"Renegade", "Golden Tempo", "Ocelli"),
	"ucl_2026_final": event("pga_2026_winner", "", "2026-05-17", "美国 PGA 锦标赛", "2026年5月17日在阿罗尼明克结束的美国 PGA 锦标赛中，哪位球员赢得冠军？", 'B',
		"琼·拉姆", "亚伦·拉伊", "亚历克斯·斯莫利"),
	"g7_2026_minerals": event("spacex_2026_first_trade", "", "2026-06-12", "SpaceX 首次公开交易", "2026年6月12日，SpaceX 在纳斯达克开始公开交易时的首笔成交价是多少？", 'B',
		"每股135美元", "每股150美元", "每股165美元"),
	"worldcup_2026_boot": event("worldcup_2026_final_goal", "", "2026-07-19", "男足世界杯决赛", "2026年7月19日举行的男足世界杯决赛，谁打进了该场比赛唯一的进球？", 'C',
		"米克尔·奥亚萨瓦尔", "拉明·亚马尔", "费兰·托雷斯"),
}

var monthlySupplements = []currentEvent{
	event("minnesota_2026_pretti", "", "2026-01-24", "亚历克斯·普雷蒂的职业", "2026年1月24日，美国明尼阿波利斯发生致命枪击。遇难者亚历克斯·普雷蒂生前从事什么工作？", 'C',
		"急诊科医生", "救护车驾驶员", "重症监护室护士"),
	event("wbd_2026_bid", "", "2026-02-26", "华纳兄弟探索收购报价", "2026年2月26日，华纳兄弟探索董事会认定派拉蒙天舞的新收购报价更优。该报价是每股多少美元？", 'A',
		"31美元", "29美元", "33美元"),
	event("iea_2026_reserves", "", "2026-03-11", "国际能源署应急石油储备", "2026年3月11日，国际能源署成员国决定从应急储备中释放多少桶石油？", 'C',
		"2亿桶", "3亿桶", "4亿桶"),
}

var currentEvents = func() []currentEvent {
	out := make([]currentEvent, 0, len(previousEvents)+len(monthlySupplements))
	for _, e := range previousEvents {
		if replacement, ok := eventReplacements[e.ID]; ok {
			replacement.replaces = e.replaces
			e = replacement
		}
		out = append(out, e)
	}
	out = append(out, monthlySupplements...)
	slices.SortFunc(out, func(a, b currentEvent) int {
		if byDate := cmp.Compare(a.HappenedAt, b.HappenedAt); byDate != 0 {
			return byDate
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}()

// CurrentAnchors returns the current Hub defaults in date order.
func CurrentAnchors() []Probe {
	out := make([]Probe, 0, len(currentEvents))
	for _, e := range currentEvents {
		out = append(out, e.Probe)
	}
	return out
}

// ReplacedAnchorID returns the v5 ID replaced by a v6 default.
func ReplacedAnchorID(id string) (string, bool) {
	for oldID, replacement := range eventReplacements {
		if replacement.ID == id {
			return oldID, true
		}
	}
	return "", false
}

// IsMonthlySupplement identifies the new defaults installed by the v6 migration.
func IsMonthlySupplement(id string) bool {
	for _, e := range monthlySupplements {
		if e.ID == id {
			return true
		}
	}
	return false
}

// PreviousAnchors keeps the original 10-question search on its own bank.
func PreviousAnchors() []Probe {
	out := make([]Probe, 0, len(previousEvents))
	for _, e := range previousEvents {
		out = append(out, e.Probe)
	}
	return out
}

// IsSupplement distinguishes later additions from replacements in the original search tree.
func IsSupplement(id string) bool {
	e, ok := eventByID(id)
	return ok && e.replaces == ""
}

func eventByID(id string) (currentEvent, bool) {
	for _, bank := range [][]currentEvent{previousEvents, currentEvents} {
		for _, e := range bank {
			if e.ID == id {
				return e, true
			}
		}
	}
	return currentEvent{}, false
}

func currentSlot(id string) Probe {
	for _, e := range previousEvents {
		if e.replaces == id {
			return e.Probe
		}
	}
	panic("unknown current event slot " + id)
}

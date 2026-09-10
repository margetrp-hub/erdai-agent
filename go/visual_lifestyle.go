package main

import (
	"regexp"
	"strings"
	"time"
)

func visualLifestyleEnabled(prompt string) bool {
	for _, clause := range visualConstraintClauses(prompt) {
		for _, marker := range []string{"跳舞", "舞蹈", "舞台", "棚拍", "影棚", "写真", "商业大片", "时尚大片", "电影感", "电影风", "韩流", "韩风", "k-pop", "kpop", "dance", "dancing", "cinematic", "studio lighting", "stage performance"} {
			if index := strings.Index(clause, marker); index >= 0 && !visualClauseNegated(clause[:index]) {
				return false
			}
		}
	}
	return true
}

func visualLifestyleInstruction(kind string) string {
	common := "仅补充用户未指定部分，明确风格、构图、动作和衣着要求优先。沿用已确认的角色虚构情境，不把聊天情境当作真实人类事实，不把用户说的‘我在办公室’当作角色所在地。"
	if kind == "video" {
		return common + "像真实相机记录同一次生活瞬间：同一场景、同一套衣服、单镜头，一个主要小动作配自然眨眼或呼吸，保留真实短暂停顿。按选定构图取景，不安排表演、转场、慢动作或换装；用户明确要求舞蹈时照办。自然肤质，脸清楚，不固定杯子或背景。"
	}
	return common + "像真实手机原生相机随手记录的生活照；按选定构图和本次拍法取景，手机成像质感不等于人物举手机或照镜。全身照使用合理拍摄距离，头脚完整入镜。角度和姿势自然，脸仍清晰、自然肤质，不过度磨皮。衣物保留真实材质和自然褶皱，不固定背景、杯子或摆拍道具，不生成海报、壁纸或商业宣传图。"
}

type visualLifestyleMoment struct {
	scene, place, markers, action, activity string
	outdoors                                bool
}

var visualLifestyleMoments = []visualLifestyleMoment{
	{"桌边短暂休息", "桌边", "桌边|书桌|办公桌|电脑|笔记本|托腮|办公室|desk|laptop", "手托着脸停一会儿，自然看向手机", "桌边休息的片刻", false},
	{"沙发边放松片刻", "沙发边", "沙发|客厅|sofa|living room", "靠着坐稳，放松肩膀后自然看向手机", "坐下来歇一会儿", false},
	{"厨房操作台旁", "厨房操作台旁", "厨房|备餐|做饭|kitchen", "暂时放下手边的餐具，抬眼看向手机", "简单备餐间隙", false},
	{"玄关出门前", "玄关", "玄关|门口|entrance", "停在原地轻轻整理头发", "出门前的短暂停留", false},
	{"阳台边短暂停留", "阳台边", "阳台|露台|balcony", "手臂自然放松，轻轻转头看向手机", "阳台边歇一会儿", true},
	{"楼下步道", "楼下步道", "楼下|散步|小区|公园|步道|park", "放慢脚步，自然看向手机", "楼下散步的片刻", true},
	{"窗边翻书", "窗边", "窗边|看书|阅读|书房|书店|window|reading", "手指停在书页边，抬眼看向手机", "翻书间隙", false},
	{"餐桌边收拾餐具", "餐桌边", "餐桌|饭后|吃饭|dining table", "放下手边的餐具，自然停顿一下", "饭后整理的片刻", false},
}

var visualLifestyleSubjects = regexp.MustCompile(`(?i)我们|我|你|\b(?:we|i|you)\b`)
var visualLifestyleEnglishAction = regexp.MustCompile(`(?i)\b(?:sit|sitting|stand|standing|lie|lying|walk|walking|run|running|dance|dancing|pose|read|reading)\b`)

func visualRoleLocationClause(clause string) string {
	clause = strings.ToLower(strings.TrimSpace(clause))
	positions := [][2]int{}
	userSubject := false
	for _, match := range visualLifestyleSubjects.FindAllStringIndex(clause, -1) {
		subject := clause[match[0]:match[1]]
		isRole := subject == "你" || subject == "you"
		prefix := strings.TrimSpace(clause[:match[0]])
		if !isRole && (strings.HasSuffix(prefix, "给") || strings.HasSuffix(prefix, "帮") || strings.HasSuffix(prefix, "为") || strings.HasSuffix(prefix, "让") || strings.HasSuffix(prefix, "告诉") || strings.HasSuffix(prefix, "替")) {
			continue
		}
		positions = append(positions, [2]int{match[0], match[1]})
		userSubject = userSubject || !isRole
	}
	if !userSubject {
		return clause
	}
	for index := len(positions) - 1; index >= 0; index-- {
		position := positions[index]
		subject := clause[position[0]:position[1]]
		if subject != "你" && subject != "you" {
			continue
		}
		end := len(clause)
		if index+1 < len(positions) {
			end = positions[index+1][0]
		}
		candidate := strings.TrimSpace(clause[position[0]:end])
		if strings.HasPrefix(candidate, "你的") || videoHasAny(candidate, "你在干嘛", "你在做什么", "你在哪", "你在忙什么", "what are you", "where are you") {
			continue
		}
		if candidate != subject {
			prefix := strings.TrimSpace(clause[:position[0]])
			if visualNegationSuffix.MatchString(prefix) || strings.HasSuffix(prefix, "不想看") || strings.HasSuffix(prefix, "不要看") || strings.HasSuffix(prefix, "别拍") {
				return "不要" + candidate
			}
			return candidate
		}
	}
	return ""
}

func visualUserLocationClause(clause string) bool {
	return strings.TrimSpace(clause) != "" && visualRoleLocationClause(clause) == ""
}

func visualLifestyleActionSpecified(prompt string) bool {
	for _, clause := range visualConstraintClauses(prompt) {
		clause = visualRoleLocationClause(clause)
		if clause == "" {
			continue
		}
		markers := []string{"坐着", "坐下", "坐在", "坐姿", "你坐", "坐一会", "站着", "站在", "站立", "站姿", "你站", "站一会", "躺着", "躺在", "躺下", "躺姿", "你躺", "躺一会", "走路", "走着", "走几步", "走两步", "你走", "你跑", "散步", "跑步", "跳舞", "舞蹈", "托腮", "趴着", "靠着", "倚着", "看书", "阅读", "回头", "转头", "转身", "抬头", "低头", "歪头", "挥手", "举手", "举起", "举杯", "拿着", "拿起", "拿杯", "端着", "手势", "备餐", "做饭", "吃饭", "喝水", "喝茶", "喝一口", "打字", "刷手机"}
		markers = append(markers, "坐稳", "站稳", "侧身", "整理衣摆", "整理衣服", "拿手机", "举手机", "手持手机", "看手机")
		markers = append(markers, visualLifestyleEnglishAction.FindAllString(clause, -1)...)
		for _, marker := range markers {
			if index := strings.Index(clause, marker); index >= 0 && !visualClauseNegated(clause[:index]) {
				return true
			}
		}
		if clause == "坐" || clause == "站" || clause == "躺" || clause == "走" || clause == "跑" {
			return true
		}
	}
	return false
}

func visualLifestyleVariables(prompt string, now time.Time, seed uint64, outfitLength string, previousScene string) map[string]string {
	selected := -1
	explicitScene := false
	explicitAction := visualLifestyleActionSpecified(prompt)
	for _, clause := range visualConstraintClauses(prompt) {
		clause = visualRoleLocationClause(clause)
		if clause == "" || visualClauseNegated(clause) {
			continue
		}
		clauseScene := -1
		for index, moment := range visualLifestyleMoments {
			if videoHasAny(clause, strings.Split(moment.markers, "|")...) {
				clauseScene = index
				break
			}
		}
		if clauseScene >= 0 || visualSceneSpecified(clause) {
			selected, explicitScene = clauseScene, true
		}
	}
	if !explicitScene && !explicitAction {
		choices := []int{0, 1, 3, 4, 5, 6}
		switch hour := now.Hour(); {
		case hour < 6 || hour >= 22:
			choices = []int{0, 1, 6}
		case hour < 9 || hour >= 17 && hour < 20:
			choices = []int{1, 2, 3, 4, 5, 7}
		}
		candidates := []string{}
		for _, index := range choices {
			if visualLifestyleMoments[index].scene != previousScene {
				candidates = append(candidates, visualLifestyleMoments[index].scene)
			}
		}
		chosen := visualChoice(&seed, candidates)
		for index, moment := range visualLifestyleMoments {
			if moment.scene == chosen {
				selected = index
				break
			}
		}
	}
	// Unlisted explicit places still get neutral actions, not an unrelated room.
	moment := visualLifestyleMoment{"用户明确指定的场景", "", "", "短暂停下手头的事，自然看向手机", "在指定环境中随手记录一刻", false}
	if selected >= 0 {
		moment = visualLifestyleMoments[selected]
	}
	if explicitAction {
		if !explicitScene {
			moment.scene = "与本次明确动作相符的环境，不额外指定地点"
		} else if selected >= 0 {
			moment.scene = moment.place
		}
		moment.action = "遵循本次明确动作与姿势，不追加其他动作"
		moment.activity = "接续本次明确动作，不新增另一项活动"
	}
	tops := []string{"棉质上衣", "柔软针织衫", "轻便衬衫"}
	if now.Month() >= time.June && now.Month() <= time.August {
		tops = []string{"轻薄棉质短袖", "透气短袖衬衫", "轻薄休闲上衣"}
	}
	top := visualChoice(&seed, tops)
	outfit := top + "配轻便下装，有自然穿着褶皱"
	switch outfitLength {
	case "short":
		outfit = "短款" + top + "配膝上短裙或短裤，有自然穿着褶皱"
	case "long":
		outfit = top + "配长裙或长裤，有自然穿着褶皱"
	}
	if moment.outdoors && (now.Month() == time.December || now.Month() <= time.February) {
		outfit += "，外搭保暖外套，不改变已指定的下装长度"
	}
	light := "已有室内环境光，脸部清楚，允许轻微明暗差"
	if now.Hour() >= 6 && now.Hour() < 19 {
		light = "现场自然环境光，脸部清楚，不过度补光"
	} else if moment.outdoors {
		light = "现场已有的照明，脸部清楚，不添加舞台灯效"
	}
	if explicitAction && !explicitScene {
		light = "实际场景已有环境光，脸部清楚，不假定室内或室外"
	}
	return map[string]string{
		"scene": moment.scene, "outfit": outfit, "action": moment.action, "activity": moment.activity,
		"mood": "放松平常的神情，不刻意营业式微笑", "makeup": "自然淡妆或素颜质感，不过度修饰", "light": light,
	}
}

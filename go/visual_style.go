package main

import (
	"strings"
	"time"
)

// Style is scoped to the effective persona/instance profile. It supplies only
// missing scene, wardrobe and framing choices; it never supplies identity.
func allocateStyledVisualVariables(prompt string, now time.Time, seed uint64, policy imageVisualDirectorPolicy, outfitLength string, history []visualGenerationPlan, style *visualStyleDefaults) map[string]string {
	if style == nil {
		values := allocateVisualVariables(prompt, now, seed, policy, outfitLength, history)
		applyVisualCapture(prompt, seed, history, values)
		return values
	}
	if len(style.SelfieTypes) > 0 {
		policy.SelfieTypes = style.SelfieTypes
	}
	values := allocateVisualVariables(prompt, now, seed, policy, outfitLength, history)
	if len(values) == 0 {
		return values
	}
	constraints := visualConstraintSubjects(prompt)
	previous := map[string]string{}
	if len(history) > 0 {
		previous = history[0].Variables
	}
	if len(style.Outfits) > 0 {
		if visualPlanOutfitSpecified(constraints) || visualOutfitLengthSpecified(constraints) {
			values["outfit"] = "按用户本次明确款式、长度和否定约束，不添加冲突的默认穿搭"
		} else {
			color, forbidden := explicitVisualColor(prompt)
			choices := []string{}
			for _, outfit := range style.Outfits {
				outfitColor, _ := explicitVisualColor(outfit)
				if outfitColor != "" && (forbidden[outfitColor] || color != "" && color != outfitColor) {
					continue
				}
				choices = append(choices, outfit)
			}
			if len(choices) > 0 {
				values["outfit"] = visualStyleChoice(&seed, choices, previous["outfit"])
			} else {
				values["outfit"] = "按本次允许的颜色自然搭配，不采用冲突的默认穿搭"
			}
		}
	}
	// A scene pool must not move the character out of an explicitly requested
	// place or introduce props that contradict an action (including exclusions).
	if len(style.Scenes) > 0 && visualStyleEnvironmentSpecified(prompt) {
		values["scene"] = "按本次地点、天气、时段及否定约束选择生活环境，不添加冲突的默认场景"
		values["light"] = "遵循本次明确的时间与天气，不添加冲突的默认照明"
		values["time"] = "本次明确时段优先"
		if !visualLifestyleActionSpecified(constraints) {
			values["action"] = "在本次允许的地点自然停留，不添加特定场地的道具或活动"
			values["activity"] = "只延续本次允许的生活情境"
		}
	} else if len(style.Scenes) > 0 && !visualSceneSpecified(constraints) && !visualLifestyleActionSpecified(constraints) {
		previousScene := previous["sourceScene"]
		if previousScene == "" {
			previousScene = previous["scene"]
		}
		values["scene"] = visualStyleChoice(&seed, style.Scenes, previousScene)
		values["activity"] = "在本次选定地点短暂停留，随手记录日常片刻，不额外安排另一项活动"
		values["light"] = "本次地点和时间的实际环境光，脸部清楚，不添加冲突的窗光、天气或棚灯"
		values["action"] = visualStylePose(&seed, values["camera"], values["scene"], previous["action"])
		if strings.Contains(values["camera"], "全身") || strings.Contains(values["camera"], "穿搭") {
			values["action"] += "；按选定构图保持头部到双脚完整入镜，留出自然边距，机位与身体比例合理"
		}
	}
	applyVisualCapture(prompt, seed, history, values)
	return values
}

func visualStylePose(seed *uint64, camera, scene, previous string) string {
	var choices []string
	switch {
	case strings.Contains(camera, "坐姿") || videoHasAny(scene, "沙发", "座位", "椅子", "长椅"):
		choices = []string{"在现场可坐的位置自然坐稳，肩膀放松", "自然坐稳后微微侧身，双手轻松放好"}
	case strings.Contains(camera, "近景"):
		choices = []string{"微微歪头，自然看向镜头", "轻轻侧过脸后回看镜头，神情放松", "下巴自然放松，平静看向镜头"}
	default:
		choices = []string{"自然站稳，双臂轻松放好", "轻轻侧身回看镜头，身体保持放松", "轻轻整理衣摆后自然停顿"}
	}
	for _, choice := range choices {
		if strings.HasPrefix(previous, choice) {
			previous = choice
			break
		}
	}
	return visualStyleChoice(seed, choices, previous) + "，不额外添加道具或另一项活动"
}

func visualStyleEnvironmentSpecified(prompt string) bool {
	for _, clause := range visualConstraintClauses(prompt) {
		clause = visualRoleLocationClause(clause)
		if videoHasAny(clause, "雨", "雪", "晴", "阴天", "白天", "夜", "日落", "黄昏", "清晨", "正午", "凌晨", "商场", "街头", "便利店", "地铁", "rain", "snow", "sunny", "night", "daylight", "sunset", "dawn") {
			return true
		}
	}
	return false
}

func visualStyleChoice(seed *uint64, choices []string, previous string) string {
	alternatives := []string{}
	for _, value := range choices {
		if value != previous {
			alternatives = append(alternatives, value)
		}
	}
	if len(alternatives) == 0 {
		alternatives = choices
	}
	return visualChoice(seed, alternatives)
}

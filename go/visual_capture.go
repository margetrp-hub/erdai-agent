package main

import (
	"slices"
	"sort"
	"strings"
)

// Capture is independent of image texture and framing: a phone photograph does
// not mean that the subject is holding the camera or standing at a mirror.
var visualCaptureMarkers = map[string][]string{
	"front_selfie":  {"前置", "手持自拍", "举手机自拍", "拿手机自拍", "front camera", "front-camera", "handheld selfie"},
	"other_person":  {"别人拍", "别人帮", "朋友拍", "朋友帮", "朋友视角", "旁人拍", "他拍", "第三人称", "第三人视角", "someone else", "taken by", "friend shot", "another person", "photographed by"},
	"timer":         {"定时", "三脚架", "支架自拍", "timer", "tripod"},
	"mirror_selfie": {"对镜", "照镜", "镜自拍", "镜拍", "镜前自拍", "镜面自拍", "镜子自拍", "镜子前自拍", "mirror selfie", "mirror shot"},
}

type visualCaptureRequest struct {
	mode           string
	forbidden      map[string]bool
	noMirror       bool
	noHoldingPhone bool
	noVisiblePhone bool
	phoneProp      bool
}

func visualCaptureIntent(prompt string) visualCaptureRequest {
	request := visualCaptureRequest{forbidden: map[string]bool{}}
	for _, clause := range visualConstraintClauses(prompt) {
		type occurrence struct {
			index int
			mode  string
		}
		matches := []occurrence{}
		for mode, markers := range visualCaptureMarkers {
			for _, marker := range markers {
				if index := strings.Index(clause, marker); index >= 0 {
					matches = append(matches, occurrence{index, mode})
				}
			}
		}
		sort.Slice(matches, func(i, j int) bool { return matches[i].index < matches[j].index })
		for _, match := range matches {
			if visualCaptureNegated(clause[:match.index]) {
				request.forbidden[match.mode] = true
			} else {
				request.mode = match.mode
				delete(request.forbidden, match.mode)
			}
		}
		for _, marker := range []string{"镜子", "镜面", "mirror"} {
			if index := strings.Index(clause, marker); index >= 0 && visualCaptureNegated(clause[:index]) {
				request.noMirror = true
			}
		}
		for _, marker := range []string{"拿手机", "举手机", "拿着手机", "手持手机", "刷手机", "看手机", "拿个手机", "holding a phone", "hold a phone", "holding the phone", "hold the phone", "using a phone"} {
			if index := strings.Index(clause, marker); index >= 0 {
				if visualCaptureNegated(clause[:index]) {
					request.noHoldingPhone = true
				} else {
					request.phoneProp = true
				}
			}
		}
		phoneOutOfFrame := videoHasAny(clause, "手机不要入镜", "手机不入镜", "不出现手机", "不要出现手机", "no visible phone", "no phone in frame", "phone out of frame", "phone outside the frame", "without a phone in frame")
		if phoneOutOfFrame {
			request.noVisiblePhone = true
		}
		if !phoneOutOfFrame && videoHasAny(clause, "不要手机", "no phone", "without a phone") {
			request.noHoldingPhone, request.noVisiblePhone = true, true
		}
	}
	if request.noMirror {
		request.forbidden["mirror_selfie"] = true
	}
	if request.noVisiblePhone {
		request.forbidden["mirror_selfie"] = true
	}
	if request.noHoldingPhone {
		request.forbidden["front_selfie"], request.forbidden["mirror_selfie"] = true, true
		request.phoneProp = false
	}
	return request
}

func visualCaptureNegated(prefix string) bool {
	return visualClauseNegated(prefix) || visualNegationSuffix.MatchString(prefix)
}

// Strip only shooting-method phrases before the existing framing parser runs.
// A bare "selfie" remains generic; it must not lock every request to front view.
func visualCaptureFramingText(value string) string {
	value = strings.ToLower(value)
	for _, markers := range visualCaptureMarkers {
		for _, marker := range markers {
			value = strings.ReplaceAll(value, marker, "")
		}
	}
	return strings.NewReplacer("镜面", "", "镜子", "", "镜前", "", "抓拍", "").Replace(value)
}

func applyVisualCapture(prompt string, seed uint64, history []visualGenerationPlan, values map[string]string) {
	if len(values) == 0 {
		return
	}
	request := visualCaptureIntent(prompt)
	mode := request.mode
	if mode == "" || request.forbidden[mode] {
		choices := []string{"front_selfie", "other_person", "timer", "mirror_selfie"}
		allowed := []string{}
		for _, candidate := range choices {
			if request.forbidden[candidate] || candidate == "front_selfie" && videoHasAny(values["camera"], "全身", "穿搭", "full body", "full-body") || candidate == "mirror_selfie" && !visualCaptureMirrorCompatible(values["scene"]) {
				continue
			}
			allowed = append(allowed, candidate)
		}
		recent := []string{}
		for _, plan := range history {
			old := plan.Variables["captureMode"]
			if _, known := visualCaptureMarkers[old]; known && !slices.Contains(recent, old) {
				recent = append(recent, old)
			}
			if len(recent) == 2 {
				break
			}
		}
		for count := len(recent); count >= 0; count-- {
			alternatives := []string{}
			for _, candidate := range allowed {
				if !slices.Contains(recent[:count], candidate) {
					alternatives = append(alternatives, candidate)
				}
			}
			if len(alternatives) > 0 {
				mode = visualChoice(&seed, alternatives)
				break
			}
		}
	}
	if mode == "" || request.forbidden[mode] {
		mode = "user_allowed"
	}
	values["captureMode"] = mode
	reconcileVisualCapture(prompt, values)
}

func visualCaptureMirrorCompatible(scene string) bool {
	scene = strings.ToLower(scene)
	if videoHasAny(scene, "镜前", "镜子", "镜面", "mirror") {
		return true
	}
	if videoHasAny(scene, "室外", "户外", "步道", "街", "河", "公园", "阳台", "露台", "外摆", "楼下", "outdoor", "street", "riverside", "park", "balcony") {
		return false
	}
	return videoHasAny(scene, "室内", "家中", "家里", "卧室", "客厅", "玄关", "更衣", "衣帽", "沙发", "indoor", "bedroom", "living room", "at home")
}

// Reconcile after continuity too: old scene/action metadata may describe a
// mirror, but must not silently replace this request's selected shooting method.
func reconcileVisualCapture(prompt string, values map[string]string) {
	mode := values["captureMode"]
	if mode == "" {
		return
	}
	request := visualCaptureIntent(prompt)
	// Continuity may restore an outdoor location after an indoor default chose
	// mirror capture. Revalidate that final scene without weakening user bans.
	if mode == "mirror_selfie" && request.mode != "mirror_selfie" && !visualCaptureMirrorCompatible(values["scene"]) {
		mode = "user_allowed"
		for _, candidate := range []string{"timer", "other_person"} {
			if !request.forbidden[candidate] {
				mode = candidate
				break
			}
		}
		values["captureMode"] = mode
	}
	switch mode {
	case "front_selfie":
		values["capture"] = "拍法=前置手持自拍；按本次选定景别安排真实机位与手臂透视，不通过镜面反射拍摄，不凭空增加另一部手机。"
	case "other_person":
		values["capture"] = "拍法=朋友或旁人从画面外直接随手拍摄；人物不是拍摄者，不举手机自拍，不通过镜面取景，拍摄设备和摄影师不入镜。"
	case "timer":
		values["capture"] = "拍法=画面外固定设备定时拍摄；人物没有手持拍摄设备，不使用镜面反射，用于拍摄的手机、相机和支架都在画面外。"
	case "mirror_selfie":
		values["capture"] = "拍法=人物手持手机的镜面自拍；镜面反射和设备位置符合真实几何，手机不遮脸，机位保留选定景别。"
	default:
		values["capture"] = "拍法=仅采用本次明确允许的拍摄方式，不添加与用户禁止事项冲突的镜面或手持自拍。"
	}
	values["capture"] += "普通手机画质只描述成像质感，不自动添加举手机、照镜动作；本次拍法与用户明确要求优先于旧机位或场景暗示。"
	if request.phoneProp && (mode == "other_person" || mode == "timer") {
		values["capture"] += "保留用户明确要求的拿手机或刷手机动作，该手机仅为生活道具，不用来拍摄自己。"
	}
	if request.noHoldingPhone {
		values["capture"] += "人物双手不拿手机。"
	}
	if request.noVisiblePhone {
		values["capture"] += "手机及拍摄设备保持画面外，不出现在成片中。"
	}
	if request.noMirror {
		values["capture"] += "遵守本次要求，镜子及镜面反射不入镜。"
	}
	values["camera"] = strings.ReplaceAll(values["camera"], "镜面穿搭自拍", "全身穿搭照")
	values["camera"] = strings.ReplaceAll(values["camera"], "朋友视角抓拍", "半身生活照")
	if mode != "mirror_selfie" {
		if mode != "front_selfie" {
			values["camera"] = strings.ReplaceAll(values["camera"], "近景自拍", "近景生活照")
		}
		for _, key := range []string{"action", "activity"} {
			values[key] = strings.ReplaceAll(values[key], "完整身体、穿搭或镜面关系", "完整身体及穿搭")
			if !request.phoneProp {
				values[key] = strings.ReplaceAll(values[key], "看向手机", "看向镜头")
			}
		}
		mirrorPlace := false
		for _, clause := range visualConstraintClauses(prompt) {
			if videoHasAny(clause, "镜子前", "镜前", "镜子旁", "镜面旁", "by the mirror", "in front of a mirror") && !visualCaptureNegated(clause) {
				mirrorPlace = true
			}
		}
		if mirrorPlace {
			if values["sourceScene"] == "" {
				values["sourceScene"] = values["scene"]
			}
			values["scene"] = "用户明确的镜前地点，采用直接拍摄视角，镜子保持画面外，不从反射中拍人物"
		} else if videoHasAny(values["scene"], "镜前", "镜子", "镜面", "mirror") {
			if values["sourceScene"] == "" {
				values["sourceScene"] = values["scene"]
			}
			values["scene"] = "同一生活地点的普通角落，镜子保持画面外，不额外安排镜面自拍"
		}
	}
}

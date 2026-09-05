package main

import (
	"context"
	"hash/fnv"
	"strings"
	"sync/atomic"
	"time"
	_ "time/tzdata"
)

type imageVisualDirectorPolicy struct {
	Enabled        bool
	UseTimeContext bool
	Timezone       string
	SelfieTypes    []string
}

var selfieVariationSequence atomic.Uint64

var defaultSelfieTypes = []string{
	"近景自拍", "半身生活照", "全身生活照", "全身穿搭照",
	"镜面穿搭自拍", "朋友视角抓拍", "坐姿生活照",
}

func defaultImageVisualDirectorPolicy() imageVisualDirectorPolicy {
	return imageVisualDirectorPolicy{
		Enabled: true, UseTimeContext: true, Timezone: "Asia/Shanghai",
		SelfieTypes: append([]string(nil), defaultSelfieTypes...),
	}
}

func (a *AgentRuntime) imageVisualDirectorPolicy(ctx context.Context) imageVisualDirectorPolicy {
	policy := defaultImageVisualDirectorPolicy()
	var stored struct {
		VisualDirectorEnabled *bool    `json:"visualDirectorEnabled"`
		UseTimeContext        *bool    `json:"visualUseTimeContext"`
		Timezone              string   `json:"visualTimezone"`
		SelfieTypes           []string `json:"selfieTypes"`
	}
	if a == nil || a.integrationConfig(ctx, "image_policy", &stored) != nil {
		return policy
	}
	if stored.VisualDirectorEnabled != nil {
		policy.Enabled = *stored.VisualDirectorEnabled
	}
	if stored.UseTimeContext != nil {
		policy.UseTimeContext = *stored.UseTimeContext
	}
	if value := strings.TrimSpace(stored.Timezone); value != "" {
		policy.Timezone = value
	}
	if values := normalizeSelfieTypes(stored.SelfieTypes); len(values) > 0 {
		policy.SelfieTypes = values
	}
	return policy
}

func normalizeSelfieTypes(values []string) []string {
	result := make([]string, 0, min(len(values), 20))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len([]rune(value)) > 24 || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
		if len(result) == 20 {
			break
		}
	}
	return result
}

func visualDirectorLocation(name string) *time.Location {
	name = strings.TrimSpace(name)
	switch name {
	case "", "Asia/Shanghai":
		return shanghaiTime
	case "UTC":
		return time.UTC
	default:
		if location, err := time.LoadLocation(name); err == nil {
			return location
		}
		return shanghaiTime
	}
}

func nextSelfieVariationSeed(prompt, personaID string, now time.Time) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(strings.TrimSpace(personaID)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strings.TrimSpace(prompt)))
	return hash.Sum64() ^ uint64(now.UnixNano()) ^ selfieVariationSequence.Add(1)
}

func visualDirectorPrompt(prompt string, now time.Time, seed uint64, policy imageVisualDirectorPolicy, shortOutfit bool) string {
	if !policy.Enabled {
		return ""
	}
	local := now.In(visualDirectorLocation(policy.Timezone))
	parts := make([]string, 0, 10)
	if policy.UseTimeContext {
		parts = append(parts,
			"时间段="+visualTimeBlock(local.Hour()),
			"季节="+visualSeason(local.Month()),
			"日期类型="+visualDayType(local.Weekday()),
		)
	}
	photoType := explicitSelfieType(prompt)
	if photoType == "" {
		values := normalizeSelfieTypes(policy.SelfieTypes)
		if len(values) == 0 {
			values = defaultSelfieTypes
		}
		photoType = values[int(seed%uint64(len(values)))]
	}
	parts = append(parts,
		"照片类型="+photoType,
		"场景="+visualScene(local.Hour(), local.Weekday(), seed/7),
		"妆容="+visualMakeup(seed/11),
		"穿搭="+visualOutfitForLength(local.Month(), seed/13, shortOutfit),
		"情绪="+visualMood(seed/17),
		"动作="+visualAction(photoType, seed/19),
		"光线="+visualLight(local.Hour()),
		"天气=未知；除非用户明确给出天气，否则不要擅自添加雨雪或极端天气",
	)
	return "本次视觉变量向量（用户明确要求优先覆盖默认值）：" + strings.Join(parts, "；") + "。"
}

func videoDirectorPrompt(prompt string, now time.Time, seed uint64, policy imageVisualDirectorPolicy, shortOutfit bool) string {
	if !policy.Enabled {
		return ""
	}
	normalized := normalizeVisualPrompt(prompt)
	local := now.In(visualDirectorLocation(policy.Timezone))
	parts := make([]string, 0, 8)
	if policy.UseTimeContext {
		parts = append(parts,
			"时间段="+visualTimeBlock(local.Hour()),
			"季节="+visualSeason(local.Month()),
			"日期类型="+visualDayType(local.Weekday()),
		)
	}
	videoType := "自然生活短视频"
	if videoHasAny(normalized, "韩流", "韩风", "k-pop", "kpop", "韩国风") {
		videoType = "韩流/K-pop舞蹈短视频"
	} else if videoHasAny(normalized, "跳舞", "舞蹈", "dance", "舞台") {
		videoType = "自然舞蹈短视频"
	}
	parts = append(parts,
		"视频类型="+videoType,
		"场景="+videoScene(normalized, local.Hour(), local.Weekday(), seed/7),
		"服装="+videoOutfitForLength(normalized, seed/13, shortOutfit),
	)
	if videoPurpleRequested(normalized) {
		parts = append(parts, "颜色优先级=用户明确指定紫色，允许使用紫色但仍更换款式，不复制服装")
	} else if videoOutfitChangeRequested(normalized) {
		parts = append(parts, "换装优先级=必须更换颜色和款式，不得沿用参考图或上一条成片的紫色衣服、同一套衣服")
	} else {
		parts = append(parts, "服装变化=本次使用新的非紫色造型，不把主参考图的衣服当作固定制服")
	}
	if videoHasAny(normalized, "性感", "撩人", "妩媚", "辣一点", "魅惑") {
		parts = append(parts, "性感表达=明确成年，靠合身剪裁、姿态、材质和镜头完成，保持非裸露、非色情")
	}
	return "本次视频视觉变量（用户明确要求优先覆盖默认值）：" + strings.Join(parts, "；") + "。"
}

func appearanceLibraryRequiresShortOutfit(visualDescription string) bool {
	return strings.Contains(strings.TrimSpace(visualDescription), "膝盖以上")
}

func shortOutfitInstruction() string {
	return "当前外观库的服装长度优先级：短款、膝上；本次变量里的裙装、裤装也必须按膝上短款执行，禁止把普通连衣裙、半裙或长裤扩展成过膝长裙、长款上衣或宽大遮挡。"
}

func visualReferenceVariationInstruction(personaID string) string {
	instruction := "参考图与上一张成片只锁定脸部、发型、年龄感和体态；忽略其中的背景、衣服、颜色、道具、姿势、灯光、镜头和构图。每次生成至少更换场景与服装颜色或款式，不能把参考图或上一张成片当成固定背景或制服，除非用户明确指定。"
	if strings.EqualFold(strings.TrimSpace(personaID), "xiaoman") {
		instruction += "小满本次优先使用非紫色的新造型和非窗边场景，禁止复用紫色紧身上衣、牛仔裤、室内窗户或同一套衣服。"
	}
	return instruction
}

func normalizeVisualPrompt(prompt string) string {
	return strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "").Replace(strings.ToLower(strings.TrimSpace(prompt)))
}

func videoHasAny(prompt string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(prompt, marker) {
			return true
		}
	}
	return false
}

func videoOutfitChangeRequested(prompt string) bool {
	return videoHasAny(prompt,
		"换套", "换一套", "换个颜色", "换颜色", "别的颜色", "换个风格", "换风格",
		"不一样", "老是紫", "老是紫色", "总是紫", "总是紫色", "一直紫", "一直都是紫色",
		"不要紫", "别用紫", "同一套", "同样的衣服", "一成不变", "同套",
	)
}

func videoPurpleRequested(prompt string) bool {
	return videoHasAny(prompt, "换成紫色", "改成紫色", "穿紫色", "紫色衣服", "紫色裙", "紫色上衣", "purple")
}

func videoOutfit(prompt string, seed uint64) string {
	return videoOutfitForLength(prompt, seed, false)
}

func videoOutfitForLength(prompt string, seed uint64, shortOutfit bool) string {
	var values []string
	if shortOutfit {
		switch {
		case videoPurpleRequested(prompt):
			values = []string{
				"紫色短款夹克配黑色高腰短裤",
				"紫色修身短款上衣配银灰色高腰短裙",
			}
		case videoHasAny(prompt, "韩流", "韩风", "k-pop", "kpop", "韩国风"):
			values = []string{
				"黑色短款运动夹克配红色高腰短裙裤",
				"白色修身无袖上衣配银灰色高腰短裙",
				"红色短款夹克配黑色高腰短裙和长靴",
				"湖蓝色短款运动上衣配白色高腰短裙裤",
			}
		case videoHasAny(prompt, "性感", "撩人", "妩媚", "辣一点", "魅惑"):
			values = []string{
				"酒红色缎面短款吊带上衣配黑色高腰短裙和短靴",
				"黑色露肩短款上衣配银色短裙和长靴",
				"墨绿色修身短款连体衣配黑色短裙和长靴",
				"珊瑚红修身短款上衣配深灰色高腰短裙",
			}
		default:
			values = []string{
				"湖蓝色针织短款上衣配米白色高腰短裙",
				"珊瑚红短款夹克配黑色高腰短裤",
				"奶油黄色短款衬衫配深蓝色牛仔短裙",
				"墨绿色修身短款上衣配灰色高腰短裙",
			}
		}
		return values[int(seed%uint64(len(values)))]
	}
	switch {
	case videoPurpleRequested(prompt):
		values = []string{
			"紫色短款夹克配黑色高腰长裤",
			"紫色修身上衣配银灰色半裙",
		}
	case videoHasAny(prompt, "韩流", "韩风", "k-pop", "kpop", "韩国风"):
		values = []string{
			"黑色短款运动夹克配红色高腰百褶裙裤",
			"白色修身无袖上衣配银灰色高腰工装长裤",
			"红色短款夹克配黑色高腰短裙和长靴",
			"湖蓝色运动背心配白色宽腿运动裤",
		}
	case videoHasAny(prompt, "性感", "撩人", "妩媚", "辣一点", "魅惑"):
		values = []string{
			"酒红色缎面吊带上衣配黑色高腰长裤和短靴",
			"黑色露肩短款上衣配银色半裙和长靴",
			"墨绿色修身连体衣配黑色长靴",
			"珊瑚红修身上衣配深灰色高腰半裙",
		}
	default:
		values = []string{
			"湖蓝色针织上衣配米白色高腰半裙",
			"珊瑚红短款夹克配黑色长裤",
			"奶油黄色衬衫配深蓝色牛仔半裙",
			"墨绿色修身上衣配灰色阔腿裤",
		}
	}
	return values[int(seed%uint64(len(values)))]
}

func videoScene(prompt string, hour int, day time.Weekday, seed uint64) string {
	if !videoHasAny(prompt, "跳舞", "舞蹈", "dance", "舞台", "韩流", "韩风", "k-pop", "kpop", "韩国风") {
		return visualScene(hour, day, seed)
	}
	if visualTimeBlock(hour) == "夜间" {
		return []string{"灯光舞蹈练习室", "城市夜景街角", "小型演出后台"}[int(seed%3)]
	}
	if day == time.Saturday || day == time.Sunday {
		return []string{"明亮舞蹈练习室", "周末市集旁的空地", "河畔步道"}[int(seed%3)]
	}
	return []string{"自然光舞蹈练习室", "商场露台", "城市街角"}[int(seed%3)]
}

func explicitSelfieType(prompt string) string {
	prompt = strings.ToLower(strings.TrimSpace(prompt))
	switch {
	case strings.Contains(prompt, "全身") || strings.Contains(prompt, "穿搭") ||
		strings.Contains(prompt, "裙子") || strings.Contains(prompt, "鞋子"):
		return "全身穿搭照"
	case strings.Contains(prompt, "镜面") || strings.Contains(prompt, "镜子"):
		return "镜面穿搭自拍"
	case strings.Contains(prompt, "头像") || strings.Contains(prompt, "大头") ||
		strings.Contains(prompt, "近照"):
		return "近景自拍"
	case strings.Contains(prompt, "坐着") || strings.Contains(prompt, "坐姿"):
		return "坐姿生活照"
	case strings.Contains(prompt, "抓拍") || strings.Contains(prompt, "他拍") ||
		strings.Contains(prompt, "朋友拍"):
		return "朋友视角抓拍"
	default:
		return ""
	}
}

func visualTimeBlock(hour int) string {
	switch {
	case hour >= 5 && hour < 9:
		return "清晨"
	case hour >= 9 && hour < 12:
		return "上午"
	case hour >= 12 && hour < 17:
		return "午后"
	case hour >= 17 && hour < 20:
		return "傍晚"
	default:
		return "夜间"
	}
}

func visualSeason(month time.Month) string {
	switch month {
	case time.March, time.April, time.May:
		return "春季"
	case time.June, time.July, time.August:
		return "夏季"
	case time.September, time.October, time.November:
		return "秋季"
	default:
		return "冬季"
	}
}

func visualDayType(day time.Weekday) string {
	if day == time.Saturday || day == time.Sunday {
		return "周末"
	}
	return "工作日"
}

func visualScene(hour int, day time.Weekday, seed uint64) string {
	var values []string
	switch visualTimeBlock(hour) {
	case "清晨", "上午":
		values = []string{"咖啡店内景", "出门前的明亮玄关", "通勤街角", "面包店门口"}
	case "午后":
		values = []string{"书店", "展览空间", "商场露台", "树影街边", "自然光工作室"}
	case "傍晚":
		values = []string{"落日街边", "河畔步道", "咖啡店外摆", "演出后台"}
	default:
		values = []string{"城市灯光街边", "餐厅门口", "剧场大厅", "安静清吧内景"}
	}
	if day == time.Saturday || day == time.Sunday {
		values = append(values, "周末市集", "公园步道")
	}
	return values[int(seed%uint64(len(values)))]
}

func visualMakeup(seed uint64) string {
	values := []string{"清透蜜桃淡妆", "柔粉水光淡妆", "自然通勤淡妆", "稍精致的外出淡妆"}
	return values[int(seed%uint64(len(values)))]
}

func visualOutfit(month time.Month, seed uint64) string {
	return visualOutfitForLength(month, seed, false)
}

func visualOutfitForLength(month time.Month, seed uint64, shortOutfit bool) string {
	var values []string
	switch visualSeason(month) {
	case "夏季":
		if shortOutfit {
			values = []string{"合身碎花短款连衣裙", "轻薄短袖上衣配高腰短裙", "清爽短款吊带裙外搭薄衫"}
		} else {
			values = []string{"合身碎花连衣裙", "轻薄短袖上衣配高腰半裙", "清爽吊带裙外搭薄衫"}
		}
	case "冬季":
		if shortOutfit {
			values = []string{"柔软针织短款上衣配短外套和短裙", "合身大衣配短款连衣裙", "奶白针织配高腰短裙和长靴"}
		} else {
			values = []string{"柔软针织配短外套", "合身大衣配连衣裙", "奶白针织配高腰半裙"}
		}
	default:
		if shortOutfit {
			values = []string{"柔粉针织短款上衣配高腰短裙", "白衬衫配轻盈短款连衣裙", "短款外套配合身短裙"}
		} else {
			values = []string{"柔粉针织配高腰半裙", "白衬衫配轻盈连衣裙", "短款外套配合身裙装"}
		}
	}
	return values[int(seed%uint64(len(values)))]
}

func visualMood(seed uint64) string {
	values := []string{"笑眼里带一点娇羞", "被夸后低头藏笑", "轻轻抿嘴看向镜头", "明亮自然的小笑", "俏皮地侧目"}
	return values[int(seed%uint64(len(values)))]
}

func visualAction(photoType string, seed uint64) string {
	if strings.Contains(photoType, "全身") || strings.Contains(photoType, "穿搭") {
		values := []string{"自然站立并轻轻整理裙摆", "走动时回头看镜头", "一手拿小包，另一只手轻触头发"}
		return values[int(seed%uint64(len(values)))]
	}
	values := []string{"轻轻整理耳边头发", "拿着咖啡看向镜头", "一只手自然垂下，另一只手轻碰发梢", "身体微侧并轻轻歪头"}
	return values[int(seed%uint64(len(values)))]
}

func visualLight(hour int) string {
	switch visualTimeBlock(hour) {
	case "清晨", "上午":
		return "柔和清透的晨间自然光"
	case "午后":
		return "明亮但不过曝的侧前方自然光"
	case "傍晚":
		return "暖色落日与自然环境光"
	default:
		return "真实城市环境光与柔和面部补光"
	}
}

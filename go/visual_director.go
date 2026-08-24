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

func visualDirectorPrompt(prompt string, now time.Time, seed uint64, policy imageVisualDirectorPolicy) string {
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
		"穿搭="+visualOutfit(local.Month(), seed/13),
		"情绪="+visualMood(seed/17),
		"动作="+visualAction(photoType, seed/19),
		"光线="+visualLight(local.Hour()),
		"天气=未知；除非用户明确给出天气，否则不要擅自添加雨雪或极端天气",
	)
	return "本次视觉变量向量（用户明确要求优先覆盖默认值）：" + strings.Join(parts, "；") + "。"
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
		values = []string{"咖啡店窗边", "出门前的明亮玄关", "通勤街角", "面包店门口"}
	case "午后":
		values = []string{"书店", "展览空间", "商场露台", "树影街边", "自然光工作室"}
	case "傍晚":
		values = []string{"落日街边", "河畔步道", "咖啡店外摆", "演出后台"}
	default:
		values = []string{"城市灯光街边", "餐厅门口", "剧场大厅", "安静清吧的窗边"}
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
	var values []string
	switch visualSeason(month) {
	case "夏季":
		values = []string{"合身碎花连衣裙", "轻薄短袖上衣配高腰半裙", "清爽吊带裙外搭薄衫"}
	case "冬季":
		values = []string{"柔软针织配短外套", "合身大衣配连衣裙", "奶白针织配高腰半裙"}
	default:
		values = []string{"柔粉针织配高腰半裙", "白衬衫配轻盈连衣裙", "短款外套配合身裙装"}
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

package main

import "testing"

func TestNativeMediaNaturalPhotoRequestsPreserveIdentityAndLane(t *testing.T) {
	for _, test := range []struct {
		message string
		lane    string
		self    bool
	}{
		{"生成一张你本人的照片", "image", true},
		{"新任务：生成一张你本人的照片，使用当前绑定的外观形象。绿色短款上衣和白色短裙，室外公园，9:16。这次不要紫色，不要室内背景。", "image", true},
		{"生成一张你自己的照片", "image", true},
		{"生成一张你的全身照", "image", true},
		{"请生成一张你本人的相片", "image", true},
		{"帮我生成一张你自己的生活照", "image", true},
		{"豆包，生成一张你本人的穿搭照", "image", true},
		{"生成你自己的照片", "image", true},
		{"给我一张你本人的照片", "image", true},
		{"发一张你自己的全身照", "image", true},
		{"生成一段你的自拍视频", "video", true},
		{"用你本人的照片生成一段视频", "video", true},
		{"生成一张海边风景照片", "image", false},
		{"请生成一张雪山相片", "image", false},
		{"本人今天很忙", "chat", false},
		{"照片和相片有什么区别？", "chat", false},
		{"这张照片好看吗？", "chat", false},
		{"怎么生成一张风景照片？", "chat", false},
		{"你本人的照片在哪里？", "chat", true},
		{"你自己的全身照是什么风格？", "chat", true},
	} {
		t.Run(test.message, func(t *testing.T) {
			if lane := inferNativeLane(test.message, false, false); lane != test.lane {
				t.Fatalf("lane=%s want=%s", lane, test.lane)
			}
			if self := nativeSelfImageRequestPattern.MatchString(test.message); self != test.self {
				t.Fatalf("self=%v want=%v", self, test.self)
			}
		})
	}
}

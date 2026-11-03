package web

// Runtime 的 docs/plan/n18-file-delivery-f3.md 第 2 节第 4 条（F3，网页一侧）：任务结束后交付区先列文件，再给文字（N-18 原话）。
// 静态检查：app.js 取 <会话>/tasks/{task}/artifacts，result() 里文件块排在答案前面，过期的文件不给链接；新的文字两种语言都有。
// 实现之前（app.js 里还没有 /artifacts）这些测试跳过。

import (
	"regexp"
	"strings"
	"testing"
)

func appWithArtifacts(t *testing.T) string {
	t.Helper()
	_, app := getFrom(t, Handler(), "/assets/app.js")
	if !strings.Contains(string(app), "/artifacts") {
		t.Skip("交付区还没写：先审验收测试（Runtime 的 docs/plan/n18-file-delivery-f3.md）")
	}
	return string(app)
}

func TestTheResultListsFilesBeforeTheAnswer(t *testing.T) {
	app := appWithArtifacts(t)
	fn := regexp.MustCompile(`(?s)function result\(task, r\) \{.*?\n\}`).FindString(app)
	if fn == "" {
		t.Fatal("找不到 result()")
	}
	files, answer := strings.Index(fn, `class: "deliverables"`), strings.Index(fn, `class: "answer"`)
	if files < 0 || answer < 0 || files > answer {
		t.Fatalf("交付区要先列文件（deliverables）再给文字（answer）：files=%d answer=%d", files, answer)
	}
	if !regexp.MustCompile(`"/tasks/" \+ [a-zA-Z.]+ \+ "/artifacts"`).MatchString(app) {
		t.Fatal("要从 <会话>/tasks/{task}/artifacts 取文件列表")
	}
	if !strings.Contains(app, "artifacts.expired") || !strings.Contains(app, "download") {
		t.Fatal("过期的文件要标出来，没过期的给下载链接（download 属性）")
	}
}

func TestTheArtifactTextIsInBothLanguages(t *testing.T) {
	appWithArtifacts(t)
	zh, en := dictionaries(t)
	for _, key := range []string{"artifacts.title", "artifacts.expired", "artifacts.download", "artifacts.loadFailed"} {
		if _, ok := zh[key]; !ok {
			t.Errorf("中文字典缺 %s", key)
		}
		if _, ok := en[key]; !ok {
			t.Errorf("英文字典缺 %s", key)
		}
	}
}

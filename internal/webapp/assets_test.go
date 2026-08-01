package webapp

import (
	"strings"
	"testing"
)

func TestSubmissionFlowAssets(t *testing.T) {
	index, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := assets.ReadFile("assets/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	logic, err := assets.ReadFile("assets/flow_logic.js")
	if err != nil {
		t.Fatal(err)
	}
	page, js, css := string(index), string(script), string(styles)
	for _, want := range []string{`id="scopeList"`, `id="topWorkflow"`, `id="selectionWorkflow"`, `id="consent"`, `aria-live="polite"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("index missing %q", want)
		}
	}
	topWorkflow := strings.Index(page, `id="topWorkflow"`)
	scopeTitle := strings.Index(page, `id="scopeTitle"`)
	scopeList := strings.Index(page, `id="scopeList"`)
	if topWorkflow < 0 || scopeTitle < 0 || scopeList < 0 || !(topWorkflow < scopeTitle && scopeTitle < scopeList) {
		t.Fatal("topWorkflow must precede scopeTitle and scopeList")
	}
	if strings.Contains(css, ".top-workflow{position:sticky") ||
		strings.Contains(css, ".top-workflow{position:fixed") {
		t.Fatal("topWorkflow must remain in normal document flow")
	}
	for _, want := range []string{"selectedScope: null", `"/api/scopes"`, "showUploadSuccess"} {
		if !strings.Contains(js, want) {
			t.Fatalf("script missing %q", want)
		}
	}

	uploadStart := strings.Index(page, `id="uploadView"`)
	if uploadStart < 0 {
		t.Fatal(`index missing independent "uploadView"`)
	}
	uploadEnd := strings.Index(page[uploadStart:], "</section>")
	if uploadEnd < 0 {
		t.Fatal("uploadView is not a complete section")
	}
	uploadView := page[uploadStart : uploadStart+uploadEnd]
	for _, want := range []string{"加密所选会话数据", "上传至远端存储", "校验提交结果", `role="progressbar"`} {
		if !strings.Contains(uploadView, want) {
			t.Fatalf("uploadView missing %q", want)
		}
	}
	if strings.Contains(uploadView, "38%") {
		t.Fatal(`uploadView must not contain hard-coded "38%" progress`)
	}

	for _, forbidden := range []string{"renderProfile", "schedulePoll", "pollTask", "downloadPoster"} {
		if strings.Contains(js, forbidden) {
			t.Fatalf("submission script retains former foreground contract %q", forbidden)
		}
	}
	for _, want := range []string{"showExclusiveView", "showPreparedWorkflows", "hidePreparedWorkflows"} {
		if !strings.Contains(string(logic), want) {
			t.Fatalf("flow logic missing %q helper", want)
		}
	}
	for _, want := range []string{"#FBF5ED", "#252321", "#FF6518", "#8F8983", "@media"} {
		if !strings.Contains(css, want) {
			t.Fatalf("styles missing %q", want)
		}
	}
	for _, forbidden := range []string{"window.open(", "modal", "lightbox", "/Users/", "大小未知"} {
		if strings.Contains(js, forbidden) || strings.Contains(page, forbidden) {
			t.Fatalf("assets contain forbidden %q", forbidden)
		}
	}
}

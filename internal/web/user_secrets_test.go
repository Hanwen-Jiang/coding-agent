package web

// WEB-SEC（Runtime 的 docs/plan/all-plans-dag-2026-11-03.md；environment-config.md 第 4.4 节与“个人凭据页”）：网络密钥页。
//
//   - /api/config 有 network_secrets 时，账号页里多一个“网络密钥”按钮（.secrets-open），打开对话框 dialog.secrets；没有时不显示。
//   - 列出自己的密钥：每个一行 .secret-row（data-name 是名字），只有名字与更新时间，旁边一个删除按钮。
//   - 写入：名字输入 input.secret-name、值输入 input.secret-value（type=password、不自动填充），保存按钮 .secret-save 调
//     PUT /api/secrets/<名字>，之后清空值的输入框、重新列出；值不留在页面上（对话框的 HTML 里没有它）。
//   - .secrets-note 写明：值只进不出、编辑不回显旧值；环境现在还不能引用（D-23 第二阶段之前不算已支持）。
//   - 新的文字两种语言都有。
//
// 静态检查总是跑；真正执行 JS 的检查用浏览器（找不到时跳过）。实现之前（app.js 还没读 /api/secrets）这些测试跳过。

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func requireSecretsPage(t *testing.T) {
	t.Helper()
	_, app := getFrom(t, Handler(), "/assets/app.js")
	if !strings.Contains(string(app), "/api/secrets") {
		t.Skip("网络密钥页还没写：先审验收测试（WEB-SEC）")
	}
}

func TestTheNetworkSecretsTextIsInBothLanguages(t *testing.T) {
	requireSecretsPage(t)
	zh, en := dictionaries(t)
	for _, key := range []string{
		"secrets.title", "secrets.open", "secrets.note", "secrets.name", "secrets.value", "secrets.save", "secrets.saved",
		"secrets.delete", "secrets.deleteConfirm", "secrets.deleted", "secrets.empty", "secrets.updated",
	} {
		if _, ok := zh[key]; !ok {
			t.Errorf("中文字典缺 %s", key)
		}
		if _, ok := en[key]; !ok {
			t.Errorf("英文字典缺 %s", key)
		}
	}
}

const pageSecret = "ghp_PAGE-SECRET-VALUE-9876543210"

// secretsProbe 打开账号页，点“网络密钥”，读列表，填名字与值保存，再读一次。
const secretsProbe = `
const out = { errors: [], dialog: false, button: false, first: null, after: null };
addEventListener("error", (e) => out.errors.push(String(e.message)));
addEventListener("unhandledrejection", (e) => out.errors.push(String(e.reason && e.reason.stack || e.reason)));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const wait = async (fn, ms) => { const end = Date.now() + ms; for (;;) { const v = fn(); if (v) return v; if (Date.now() > end) return null; await sleep(30); } };
const read = (d) => ({
  names: [...d.querySelectorAll(".secret-row")].map((r) => r.dataset.name),
  note: !!d.querySelector(".secrets-note") && d.querySelector(".secrets-note").textContent.length > 10,
  valueType: (d.querySelector("input.secret-value") || {}).type || "",
  autocomplete: (d.querySelector("input.secret-value") || { getAttribute: () => "" }).getAttribute("autocomplete") || "",
  value: (d.querySelector("input.secret-value") || {}).value,
  html: d.innerHTML.includes(SECRET),
  text: d.textContent,
});
const SECRET = "` + pageSecret + `";
try {
  out.dialog = !!(await wait(() => document.querySelector("dialog.account .conn-list"), 8000));
  const open = await wait(() => document.querySelector("dialog.account .secrets-open"), 2000);
  out.button = !!open;
  if (open) {
    open.click();
    const d = await wait(() => document.querySelector("dialog.secrets .secret-row") && document.querySelector("dialog.secrets"), 4000);
    if (d) {
      out.first = read(d);
      const name = d.querySelector("input.secret-name"), value = d.querySelector("input.secret-value");
      name.value = "npm-token"; name.dispatchEvent(new Event("input", { bubbles: true }));
      value.value = SECRET; value.dispatchEvent(new Event("input", { bubbles: true }));
      d.querySelector(".secret-save").click();
      await wait(() => d.querySelectorAll(".secret-row").length >= 3, 4000);
      out.after = read(d);
    }
  }
} catch (ex) {
  out.errors.push(String(ex && ex.stack || ex));
}
await fetch("/__result", { method: "POST", body: JSON.stringify(out) });
`

type secretsView struct {
	Names        []string `json:"names"`
	Note         bool     `json:"note"`
	ValueType    string   `json:"valueType"`
	Autocomplete string   `json:"autocomplete"`
	Value        string   `json:"value"`
	HTML         bool     `json:"html"`
	Text         string   `json:"text"`
}

type secretsResult struct {
	Errors []string     `json:"errors"`
	Dialog bool         `json:"dialog"`
	Button bool         `json:"button"`
	First  *secretsView `json:"first"`
	After  *secretsView `json:"after"`
}

func TestTheNetworkSecretsPageListsSavesAndForgetsTheValue(t *testing.T) {
	requireSecretsPage(t)
	var mu sync.Mutex
	names := []string{"github-token", "pypi-token"}
	var puts []map[string]string
	extra := map[string]any{
		"GET /api/secrets": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			list := []params{}
			for _, n := range names {
				list = append(list, params{"name": n, "created_at": "2026-11-04T08:00:00Z", "updated_at": "2026-11-04T09:30:00Z"})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(params{"secrets": list})
		}),
		"PUT /api/secrets/{name}": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			defer mu.Unlock()
			puts = append(puts, map[string]string{"name": r.PathValue("name"), "value": body["value"]})
			names = append(names, r.PathValue("name"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(params{"secret": params{"name": r.PathValue("name")}, "created": true})
		}),
	}
	for _, lang := range []string{"zh", "en"} {
		mu.Lock()
		names, puts = []string{"github-token", "pypi-token"}, nil
		mu.Unlock()
		var res secretsResult
		raw := runAccountProbe(t, lang, secretsProbe, params{"network_secrets": true}, extra)
		if err := json.Unmarshal(raw, &res); err != nil || len(res.Errors) != 0 || !res.Dialog {
			t.Fatalf("%s：页面：%v %s", lang, err, raw)
		}
		if !res.Button || res.First == nil {
			t.Fatalf("%s：打开了网络密钥时账号页有入口，对话框列出密钥：%s", lang, raw)
		}
		if strings.Join(res.First.Names, ",") != "github-token,pypi-token" || !res.First.Note {
			t.Errorf("%s：列出名字、写明还不能被环境引用：%+v", lang, res.First)
		}
		if res.First.ValueType != "password" || res.First.Autocomplete == "" || res.First.Autocomplete == "on" {
			t.Errorf("%s：值的输入框不显示、不自动填充：%+v", lang, res.First)
		}
		mu.Lock()
		got := append([]map[string]string(nil), puts...)
		mu.Unlock()
		if len(got) != 1 || got[0]["name"] != "npm-token" || got[0]["value"] != pageSecret {
			t.Fatalf("%s：保存调 PUT /api/secrets/<名字>：%v", lang, got)
		}
		if res.After == nil || res.After.Value != "" || res.After.HTML || len(res.After.Names) != 3 {
			t.Errorf("%s：保存之后清空值、重新列出，值不留在页面上：%+v", lang, res.After)
		}
		if strings.Contains(res.First.Text, "secrets.") {
			t.Errorf("%s：有没翻译的键名：%q", lang, res.First.Text)
		}
	}
}

func TestTheNetworkSecretsPageIsHiddenWhenOff(t *testing.T) {
	requireSecretsPage(t)
	var res secretsResult
	raw := runAccountProbe(t, "zh", secretsProbe, params{}, nil)
	if err := json.Unmarshal(raw, &res); err != nil || !res.Dialog {
		t.Fatalf("页面：%v %s", err, raw)
	}
	if res.Button {
		t.Fatal("没打开网络密钥时账号页没有入口（D-23 第二阶段之前不算已支持）")
	}
}

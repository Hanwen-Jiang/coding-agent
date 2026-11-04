package server

// G-01（docs/plan/github-app.md 第 3、4 节）：GitHub App 的密钥与配置。
//
//  1. 全不配时功能关闭、行为不变；只配一部分时启动失败，错误写变量名不写值。私钥只收文件（_FILE），环境变量里的私钥直接拒绝。
//  2. 用带标记的假密钥跑完登录、签令牌、验 Webhook、失败路径：日志、错误信息、/api/config、/api/me 与打印配置都找不到它们。
//  3. 配了 _PREVIOUS 时新旧 Webhook secret 都能过验签；去掉后旧的被拒。只认 sha256= 的签名。
//  4. 第三方令牌的密钥轮换：CODING_TOKEN_KEY 换新、旧的放进 _PREVIOUS，旧数据读得出、读到时换成新密钥；去掉 _PREVIOUS 之后照样读得出。
//  5. 签 App 的 JWT：RS256，iss 是 Client ID，iat 提前 60 秒，exp 不超过 10 分钟。
//  6. 静态检查（deploy/k8s）：私钥只挂进 coding-web（单独的 Secret，按文件、0400），没有清单把它放进环境变量。

import (
	"bytes"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	markWebhook     = "MARK-webhook-secret-7f3a"
	markWebhookPrev = "MARK-webhook-previous-91c2"
	markClient      = "MARK-client-secret-55d0"
)

func writeAppKey(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "app.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(path, data, 0o400); err != nil {
		t.Fatal(err)
	}
	return path, key
}

func appEnv(keyFile string) map[string]string {
	return map[string]string{
		"CODING_GITHUB_APP_ID":               "123456",
		"CODING_GITHUB_APP_CLIENT_ID":        "Iv1.client",
		"CODING_GITHUB_APP_SLUG":             "coding-agent-link",
		"CODING_GITHUB_APP_PRIVATE_KEY_FILE": keyFile,
		"CODING_GITHUB_WEBHOOK_SECRET":       markWebhook,
	}
}

func envFunc(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestGitHubAppConfigIsAllOrNothing(t *testing.T) {
	app, err := GitHubAppFromEnv(envFunc(nil))
	if err != nil || app != nil {
		t.Fatalf("全不配时功能关闭：%v %v", app, err)
	}
	keyFile, _ := writeAppKey(t)
	full := appEnv(keyFile)
	full["CODING_GITHUB_APP_CLIENT_SECRET"] = markClient
	app, err = GitHubAppFromEnv(envFunc(full))
	if err != nil || app == nil || app.AppID != "123456" || app.Slug != "coding-agent-link" {
		t.Fatalf("配齐时能读进来：%+v %v", app, err)
	}
	for name := range appEnv(keyFile) {
		m := appEnv(keyFile)
		delete(m, name)
		_, err := GitHubAppFromEnv(envFunc(m))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("缺 %s 时启动失败并写变量名，实际 %v", name, err)
			continue
		}
		for _, v := range []string{markWebhook, keyFile} {
			if v != keyFile && strings.Contains(err.Error(), v) {
				t.Errorf("错误信息不带值：%v", err)
			}
		}
	}
	raw := appEnv(keyFile)
	pemText, _ := os.ReadFile(keyFile)
	raw["CODING_GITHUB_APP_PRIVATE_KEY"] = string(pemText)
	if _, err := GitHubAppFromEnv(envFunc(raw)); err == nil || !strings.Contains(err.Error(), "CODING_GITHUB_APP_PRIVATE_KEY_FILE") || strings.Contains(err.Error(), "PRIVATE KEY-----") {
		t.Fatalf("私钥放在环境变量里要拒绝、指向 _FILE、不带私钥：%v", err)
	}
	bad := appEnv(filepath.Join(t.TempDir(), "missing.pem"))
	if _, err := GitHubAppFromEnv(envFunc(bad)); err == nil {
		t.Fatal("私钥文件读不了时启动失败")
	}
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	_ = os.WriteFile(garbage, []byte("not a key "+markWebhook), 0o400)
	if _, err := GitHubAppFromEnv(envFunc(appEnv(garbage))); err == nil || strings.Contains(err.Error(), markWebhook) {
		t.Fatalf("私钥文件不对时失败、不带文件内容：%v", err)
	}
}

func sign(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestWebhookSecretRotation(t *testing.T) {
	keyFile, _ := writeAppKey(t)
	env := appEnv(keyFile)
	env["CODING_GITHUB_WEBHOOK_SECRET_PREVIOUS"] = markWebhookPrev
	app, err := GitHubAppFromEnv(envFunc(env))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"action":"created"}`)
	for _, secret := range []string{markWebhook, markWebhookPrev} {
		if !app.VerifyWebhook(body, sign(secret, string(body))) {
			t.Fatalf("轮换期间新旧 secret 都能过验签")
		}
	}
	for _, header := range []string{"", "sha1=" + strings.TrimPrefix(sign(markWebhook, string(body)), "sha256="), sign("wrong", string(body)), sign(markWebhook, string(body)+" ")} {
		if app.VerifyWebhook(body, header) {
			t.Fatalf("签名错、缺或不是 sha256 的要拒：%q", header)
		}
	}
	delete(env, "CODING_GITHUB_WEBHOOK_SECRET_PREVIOUS")
	app, _ = GitHubAppFromEnv(envFunc(env))
	if app.VerifyWebhook(body, sign(markWebhookPrev, string(body))) {
		t.Fatal("去掉 _PREVIOUS 后旧的 secret 被拒")
	}
}

func TestAppJWTIsRS256WithShortLifetime(t *testing.T) {
	keyFile, key := writeAppKey(t)
	app, err := GitHubAppFromEnv(envFunc(appEnv(keyFile)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	token, err := app.AppJWT(now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT：%q", token)
	}
	var header, claims map[string]any
	for i, out := range []*map[string]any{&header, &claims} {
		raw, _ := base64.RawURLEncoding.DecodeString(parts[i])
		_ = json.Unmarshal(raw, out)
	}
	if header["alg"] != "RS256" || claims["iss"] != "Iv1.client" {
		t.Fatalf("头 %v，载荷 %v", header, claims)
	}
	iat, exp := int64(claims["iat"].(float64)), int64(claims["exp"].(float64))
	if iat != now.Unix()-60 || exp <= now.Unix() || exp-now.Unix() > 600 {
		t.Fatalf("iat 提前 60 秒、exp 不超过 10 分钟：iat=%d exp=%d now=%d", iat, exp, now.Unix())
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("签名对得上私钥：%v", err)
	}
}

func TestGitHubAppSecretsNeverLeak(t *testing.T) {
	keyFile, _ := writeAppKey(t)
	env := appEnv(keyFile)
	env["CODING_GITHUB_APP_CLIENT_SECRET"] = markClient
	env["CODING_GITHUB_WEBHOOK_SECRET_PREVIOUS"] = markWebhookPrev
	app, err := GitHubAppFromEnv(envFunc(env))
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	// 配了 Client secret 的 App 会打开 GitHub 的登录（G-03），要回调地址。
	ts, _, cfg, _ := newServerWith(t, func(c *Config) { c.GitHubApp, c.PublicURL = app, "http://placeholder" })
	c := newClient(t, ts.URL)
	login(t, c, "acme", "alice")
	var seen []string
	for _, path := range []string{"/api/config", "/api/me"} {
		code, _, raw := c.do("GET", path, nil)
		if code != http.StatusOK {
			t.Fatalf("%s：%d %s", path, code, raw)
		}
		seen = append(seen, raw)
	}
	_, _, cfgJSON := c.do("GET", "/api/config", nil)
	if !strings.Contains(cfgJSON, "coding-agent-link") {
		t.Fatalf("/api/config 带出 App 的 slug（不是密钥），好拼安装链接：%s", cfgJSON)
	}
	if _, err := app.AppJWT(time.Now()); err != nil {
		t.Fatal(err)
	}
	app.VerifyWebhook([]byte("{}"), "sha256=00")
	pemText, _ := os.ReadFile(keyFile)
	keyLine := strings.Split(strings.TrimSpace(string(pemText)), "\n")[1]
	seen = append(seen, logs.String(), fmt.Sprint(app), fmt.Sprintf("%+v", app), fmt.Sprintf("%#v", app), fmt.Sprintf("%+v", cfg))
	for _, s := range seen {
		for _, secret := range []string{markWebhook, markWebhookPrev, markClient, keyLine} {
			if strings.Contains(s, secret) {
				t.Fatalf("密钥出现在输出里：%.80s…", secret)
			}
		}
	}
}

func TestTokenKeyRotationWithPrevious(t *testing.T) {
	dir := t.TempDir()
	conn := Connection{Provider: "github", RemoteID: "1", Login: "octo"}
	_, _, _, first := newServerWith(t, func(c *Config) { c.DataDir, c.TokenKey = dir, tokenKey32(1) })
	if _, err := first.updateAccount("acme", "alice", func(a *Account) error {
		if err := first.sealConnection("acme", "alice", &conn, tokenSet{Access: "gho_one"}); err != nil {
			return err
		}
		a.Connections["github"] = conn
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, _, _, rotating := newServerWith(t, func(c *Config) { c.DataDir, c.TokenKey, c.TokenKeyPrevious = dir, tokenKey32(2), tokenKey32(1) })
	a, err := rotating.readAccount("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := rotating.open(a.Connections["github"].Token, tokenAAD("acme", "alice", "github")); err != nil || plain != "gho_one" {
		t.Fatalf("轮换期间旧密钥加密的读得出：%q %v", plain, err)
	}
	_, _, _, after := newServerWith(t, func(c *Config) { c.DataDir, c.SigningKey, c.TokenKey = dir, []byte("other"), tokenKey32(2) })
	a, err = after.readAccount("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := after.open(a.Connections["github"].Token, tokenAAD("acme", "alice", "github")); err != nil || plain != "gho_one" {
		t.Fatalf("读到时已经换成新密钥：去掉 _PREVIOUS、换了签名密钥都读得出：%q %v", plain, err)
	}
}

func TestDeployMountsTheAppKeyOnlyIntoCodingWebAsAFile(t *testing.T) {
	var files []string
	err := filepath.WalkDir(filepath.Join("..", "..", "deploy"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			files = append(files, path)
		}
		return err
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("deploy 下的清单：%v %v", files, err)
	}
	var mounted []string
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		text := string(raw)
		if strings.Contains(text, "CODING_GITHUB_APP_PRIVATE_KEY:") || strings.Contains(text, "name: CODING_GITHUB_APP_PRIVATE_KEY\n") {
			t.Errorf("%s 把私钥放进了环境变量", f)
		}
		if strings.Contains(text, "secretName: coding-github-app") {
			mounted = append(mounted, filepath.Base(f))
			if !strings.Contains(text, "defaultMode: 0400") || !strings.Contains(text, "CODING_GITHUB_APP_PRIVATE_KEY_FILE") {
				t.Errorf("%s：私钥按文件挂（0400），变量给路径", f)
			}
		}
	}
	if len(mounted) != 1 || mounted[0] != "coding-web.yaml" {
		t.Fatalf("私钥只挂进 coding-web：%v", mounted)
	}
}

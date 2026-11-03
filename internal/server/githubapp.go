package server

// GitHub App 的密钥与配置（G-01，docs/plan/github-app.md 第 3 节）。全不配时功能关闭、行为不变；配了一部分时启动失败，
// 错误只写变量名。私钥只收文件（CODING_GITHUB_APP_PRIVATE_KEY_FILE，单独的 Secret 按文件挂进 coding-web）：放在环境变量里
// 会被 coding-web 起的 git 子进程继承，所以直接拒绝。这里的密钥不进日志、错误、接口与打印（String、GoString 只给摘要）。
// 收件（G-02）、安装与令牌（G-03）在后面的工作项里用它。

import (
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
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// GitHubApp 是 App 的配置。AppID、ClientID、Slug 不是密钥；其余字段不导出。
type GitHubApp struct {
	AppID    string
	ClientID string
	Slug     string
	// clientSecret 是用户授权（G-03 的用户令牌）要的 Client secret；G-01 时可以还没有。
	clientSecret string
	key          *rsa.PrivateKey
	// webhookSecrets：当前的在前，轮换期间另有 _PREVIOUS。
	webhookSecrets [][]byte
}

// githubAppRequired 是一起配、缺一不可的变量（Client secret 到 G-03 才必填）。
var githubAppRequired = []string{
	"CODING_GITHUB_APP_ID", "CODING_GITHUB_APP_CLIENT_ID", "CODING_GITHUB_APP_SLUG",
	"CODING_GITHUB_APP_PRIVATE_KEY_FILE", "CODING_GITHUB_WEBHOOK_SECRET",
}

// GitHubAppFromEnv 读 App 的配置：全不配时返回 nil。
func GitHubAppFromEnv(getenv func(string) string) (*GitHubApp, error) {
	if getenv("CODING_GITHUB_APP_PRIVATE_KEY") != "" {
		return nil, errors.New("GitHub App 的私钥不能放在环境变量 CODING_GITHUB_APP_PRIVATE_KEY 里：按文件挂载，用 CODING_GITHUB_APP_PRIVATE_KEY_FILE 给路径")
	}
	var missing []string
	set := 0
	for _, name := range githubAppRequired {
		if strings.TrimSpace(getenv(name)) == "" {
			missing = append(missing, name)
		} else {
			set++
		}
	}
	optional := getenv("CODING_GITHUB_APP_CLIENT_SECRET") != "" || getenv("CODING_GITHUB_WEBHOOK_SECRET_PREVIOUS") != ""
	if set == 0 && !optional {
		return nil, nil
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("GitHub App 只配了一部分，还缺 %s", strings.Join(missing, "、"))
	}
	raw, err := os.ReadFile(getenv("CODING_GITHUB_APP_PRIVATE_KEY_FILE"))
	if err != nil {
		return nil, fmt.Errorf("读不了 CODING_GITHUB_APP_PRIVATE_KEY_FILE：%v", errors.Unwrap(err))
	}
	key, err := parseRSAKey(raw)
	if err != nil {
		return nil, fmt.Errorf("CODING_GITHUB_APP_PRIVATE_KEY_FILE 不是 RSA 私钥（GitHub 给的 PEM）")
	}
	app := &GitHubApp{
		AppID: strings.TrimSpace(getenv("CODING_GITHUB_APP_ID")), ClientID: strings.TrimSpace(getenv("CODING_GITHUB_APP_CLIENT_ID")),
		Slug: strings.TrimSpace(getenv("CODING_GITHUB_APP_SLUG")), clientSecret: getenv("CODING_GITHUB_APP_CLIENT_SECRET"), key: key,
		webhookSecrets: [][]byte{[]byte(getenv("CODING_GITHUB_WEBHOOK_SECRET"))},
	}
	if prev := getenv("CODING_GITHUB_WEBHOOK_SECRET_PREVIOUS"); prev != "" {
		app.webhookSecrets = append(app.webhookSecrets, []byte(prev))
	}
	return app, nil
}

func parseRSAKey(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("not pem")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not rsa")
	}
	return key, nil
}

// String 与 GoString 只给摘要，打印配置时带不出密钥。
func (a *GitHubApp) String() string {
	if a == nil {
		return "GitHubApp(off)"
	}
	return fmt.Sprintf("GitHubApp(id=%s, slug=%s, client_secret=%v, webhook_secrets=%d)", a.AppID, a.Slug, a.clientSecret != "", len(a.webhookSecrets))
}

func (a *GitHubApp) GoString() string { return a.String() }

// Format 让 %+v、%v、%s 都走 String（不然 %+v 会把私钥的大整数打出来）。
func (a *GitHubApp) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(a.String())) }

// VerifyWebhook 核对 X-Hub-Signature-256（只认 sha256=）：当前的与轮换期间的旧 secret 有一个对得上就算通过，常量时间比较。
func (a *GitHubApp) VerifyWebhook(body []byte, header string) bool {
	hexSig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil || len(got) != sha256.Size {
		return false
	}
	match := false
	for _, secret := range a.webhookSecrets {
		m := hmac.New(sha256.New, secret)
		m.Write(body)
		if hmac.Equal(m.Sum(nil), got) {
			match = true
		}
	}
	return match
}

// AppJWT 签 App 的 JWT（RS256）：iss 是 Client ID，iat 提前 60 秒（防时钟偏差），9 分钟后过期（GitHub 的上限是 10 分钟）。
func (a *GitHubApp) AppJWT(now time.Time) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": a.ClientID})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

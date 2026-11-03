package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/providers/ingress"
)

// 账号（docs/plan/coding-agent-mvp.md 第 9 节）：一个账号（租户 + 用户）可以关联多个登录方式。
// 第一次用哪一家登录就用哪一家建账号（租户 <前缀>-<登录名>）；登录后在“账号与平台”里连接别家，之后用任何一家登录都是这个账号。
// 平台上的账号按它不变的编号认（identities.json：<平台>:<编号> → 租户、用户），登录名改了不影响。
// 令牌按（租户、用户、平台）加密存放（AES-256-GCM，密钥是 CODING_TOKEN_KEY；没配时由签名密钥派生，见 tokenKey），不进浏览器、
// 日志与 Git 配置；会过期的令牌用前自动续期。

// Connection 是账号关联的一个登录方式；Token、Refresh 是密文。
type Connection struct {
	Provider    string    `json:"provider"`
	RemoteID    string    `json:"remote_id"`
	Login       string    `json:"login"`
	Name        string    `json:"name,omitempty"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	Email       string    `json:"email,omitempty"`
	Scopes      string    `json:"scopes,omitempty"`
	Token       string    `json:"token"`
	Refresh     string    `json:"refresh,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitzero"`
	ConnectedAt time.Time `json:"connected_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Account 是一个用户的账号记录（<数据目录>/<租户>/users/<用户>.json）。
type Account struct {
	// Primary 是建账号时用的登录方式（头像、名字优先取它的）。
	Primary     string                `json:"primary,omitempty"`
	Connections map[string]Connection `json:"connections,omitempty"`

	// 多平台之前的格式：只有一个 GitHub 账号，令牌按（租户、用户）加密。读到时迁移成 Connections["github"]。
	LegacyLogin     string    `json:"login,omitempty"`
	LegacyID        int64     `json:"id,omitempty"`
	LegacyName      string    `json:"name,omitempty"`
	LegacyAvatarURL string    `json:"avatar_url,omitempty"`
	LegacyScopes    string    `json:"scopes,omitempty"`
	LegacyToken     string    `json:"token,omitempty"`
	LegacyUpdatedAt time.Time `json:"updated_at,omitzero"`
}

type identityRef struct {
	Tenant string `json:"tenant"`
	User   string `json:"user"`
}

func (s *Server) accountFile(tenant, user string) string {
	return filepath.Join(s.cfg.DataDir, tenant, "users", user+".json")
}

func (s *Server) identitiesFile() string {
	return filepath.Join(s.cfg.DataDir, "identities.json")
}

func identityKey(provider, remoteID string) string { return provider + ":" + remoteID }

// --- 加密 ---

// tokenKey 是第三方平台令牌的加密密钥：配了 CODING_TOKEN_KEY 就用它（AUTH-01，与签名密钥无关，签名密钥泄露也解不开）；
// 没配时沿用由开发签名密钥派生的旧密钥。
func (s *Server) tokenKey() []byte {
	if len(s.cfg.TokenKey) > 0 {
		return s.cfg.TokenKey
	}
	return legacyTokenKey(s.cfg.SigningKey)
}

// oldKeys 是解旧数据用的密钥，按顺序试：轮换期间的 CODING_TOKEN_KEY_PREVIOUS（G-01），以及有独立密钥、又还有签名密钥时
// 由签名密钥派生的旧密钥（AUTH-01 之前的数据）。都没有时为空。
func (s *Server) oldKeys() [][]byte {
	var keys [][]byte
	if len(s.cfg.TokenKeyPrevious) > 0 {
		keys = append(keys, s.cfg.TokenKeyPrevious)
	}
	if len(s.cfg.TokenKey) > 0 && len(s.cfg.SigningKey) > 0 {
		keys = append(keys, legacyTokenKey(s.cfg.SigningKey))
	}
	return keys
}

func legacyTokenKey(signingKey []byte) []byte {
	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte("coding-web/github-token/v1"))
	return mac.Sum(nil)
}

func (s *Server) seal(plain, aad string) (string, error) {
	block, err := aes.NewCipher(s.tokenKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), []byte(aad))), nil
}

// open 用当前的密钥解开；解不开时再试旧密钥（轮换与迁移期间，AUTH-01、G-01）。
func (s *Server) open(sealed, aad string) (string, error) {
	plain, err := openWith(s.tokenKey(), sealed, aad)
	if err != nil {
		if old, ok := s.openOld(sealed, aad); ok {
			return old, nil
		}
	}
	return plain, err
}

func (s *Server) openOld(sealed, aad string) (string, bool) {
	for _, key := range s.oldKeys() {
		if plain, err := openWith(key, sealed, aad); err == nil {
			return plain, true
		}
	}
	return "", false
}

// rekey 把账号里用旧密钥加密的令牌改用当前的密钥（读到时就地迁移）；返回是否有改动。调用方持有 s.store.mu。
func (s *Server) rekey(a *Account, tenant, user string) (bool, error) {
	if len(s.oldKeys()) == 0 {
		return false, nil
	}
	changed := false
	redo := func(sealed *string, aad string) error {
		if *sealed == "" {
			return nil
		}
		if _, err := openWith(s.tokenKey(), *sealed, aad); err == nil {
			return nil
		}
		plain, ok := s.openOld(*sealed, aad)
		if !ok {
			return nil // 哪把都解不开：原样留着，用到时照常报“令牌记录解不开”
		}
		var err error
		if *sealed, err = s.seal(plain, aad); err != nil {
			return err
		}
		changed = true
		return nil
	}
	for provider, c := range a.Connections {
		if err := redo(&c.Token, tokenAAD(tenant, user, provider)); err != nil {
			return false, err
		}
		if err := redo(&c.Refresh, tokenAAD(tenant, user, provider)+"/refresh"); err != nil {
			return false, err
		}
		a.Connections[provider] = c
	}
	return changed, nil
}

func openWith(key []byte, sealed, aad string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", msg("令牌记录损坏")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], []byte(aad))
	if err != nil {
		return "", msg("令牌记录解不开")
	}
	return string(plain), nil
}

func tokenAAD(tenant, user, provider string) string { return tenant + "/" + user + "/" + provider }

// --- 读写账号 ---

// loadAccount 读账号；旧格式（只有 GitHub）就地迁移。调用方持有 s.store.mu。
func (s *Server) loadAccount(tenant, user string) (Account, error) {
	a, err := readJSONFile[Account](s.accountFile(tenant, user))
	if err != nil {
		return Account{}, err
	}
	if a.Connections == nil {
		a.Connections = map[string]Connection{}
	}
	if a.LegacyToken != "" && len(a.Connections) == 0 {
		token, err := s.open(a.LegacyToken, tenant+"/"+user)
		if err != nil {
			return Account{}, err
		}
		sealed, err := s.seal(token, tokenAAD(tenant, user, "github"))
		if err != nil {
			return Account{}, err
		}
		a.Connections["github"] = Connection{
			Provider: "github", RemoteID: remoteID(a.LegacyID), Login: a.LegacyLogin, Name: a.LegacyName, AvatarURL: a.LegacyAvatarURL,
			Scopes: a.LegacyScopes, Token: sealed, ConnectedAt: a.LegacyUpdatedAt, UpdatedAt: a.LegacyUpdatedAt,
		}
		a.Primary = "github"
		a.LegacyLogin, a.LegacyID, a.LegacyName, a.LegacyAvatarURL, a.LegacyScopes, a.LegacyToken, a.LegacyUpdatedAt = "", 0, "", "", "", "", time.Time{}
		if err := writeFileAtomic(s.accountFile(tenant, user), a); err != nil {
			return Account{}, err
		}
	}
	if changed, err := s.rekey(&a, tenant, user); err != nil {
		return Account{}, err
	} else if changed {
		if err := writeFileAtomic(s.accountFile(tenant, user), a); err != nil {
			return Account{}, err
		}
	}
	return a, nil
}

// updateAccount 在锁里读出、修改、写回账号。
func (s *Server) updateAccount(tenant, user string, fn func(*Account) error) (Account, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	a, err := s.loadAccount(tenant, user)
	if err != nil {
		return Account{}, err
	}
	if err := fn(&a); err != nil {
		return Account{}, err
	}
	return a, writeFileAtomic(s.accountFile(tenant, user), a)
}

func (s *Server) readAccount(tenant, user string) (Account, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	return s.loadAccount(tenant, user)
}

// sealConnection 把明文令牌加密进连接记录。
func (s *Server) sealConnection(tenant, user string, c *Connection, t tokenSet) error {
	sealed, err := s.seal(t.Access, tokenAAD(tenant, user, c.Provider))
	if err != nil {
		return err
	}
	c.Token, c.Refresh, c.ExpiresAt = sealed, "", t.ExpiresAt
	if t.Refresh != "" {
		if c.Refresh, err = s.seal(t.Refresh, tokenAAD(tenant, user, c.Provider)+"/refresh"); err != nil {
			return err
		}
	}
	if t.Scope != "" {
		c.Scopes = t.Scope
	}
	c.UpdatedAt = time.Now().UTC()
	return nil
}

var errNotConnected = errors.New("not connected")

// connToken 取出账号在这一家的连接与明文令牌；快过期（两分钟内）且有续期令牌时先续期。没有连接时返回 errNotConnected。
func (s *Server) connToken(r *http.Request, id identity, p *Provider) (Connection, string, error) {
	a, err := s.readAccount(id.TenantID, id.UserID)
	if err != nil {
		return Connection{}, "", err
	}
	c, ok := a.Connections[p.id]
	if !ok || c.Token == "" {
		return Connection{}, "", errNotConnected
	}
	token, err := s.open(c.Token, tokenAAD(id.TenantID, id.UserID, p.id))
	if err != nil {
		return Connection{}, "", err
	}
	if c.ExpiresAt.IsZero() || time.Until(c.ExpiresAt) > 2*time.Minute {
		return c, token, nil
	}
	if c.Refresh == "" {
		return c, token, nil // 没有续期令牌：照用，过期了平台会回 401，提示重新连接
	}
	refresh, err := s.open(c.Refresh, tokenAAD(id.TenantID, id.UserID, p.id)+"/refresh")
	if err != nil {
		return Connection{}, "", err
	}
	ctx, cancel := ctxTimeout(r, 30*time.Second)
	defer cancel()
	t, err := p.refresh(ctx, refresh)
	if err != nil {
		return Connection{}, "", &GitError{Provider: p.Name, Status: http.StatusUnauthorized, Note: msg("授权过期且续不上：%v", err)}
	}
	if t.Refresh == "" {
		t.Refresh = refresh // 有的平台续期时不换续期令牌
	}
	_, err = s.updateAccount(id.TenantID, id.UserID, func(a *Account) error {
		cur, ok := a.Connections[p.id]
		if !ok {
			return errNotConnected
		}
		if err := s.sealConnection(id.TenantID, id.UserID, &cur, t); err != nil {
			return err
		}
		a.Connections[p.id] = cur
		c = cur
		return nil
	})
	return c, t.Access, err
}

// --- 平台账号与本地账号的对应 ---

func (s *Server) lookupIdentity(key string) (identityRef, bool) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	ids, _ := readJSONFile[map[string]identityRef](s.identitiesFile())
	ref, ok := ids[key]
	return ref, ok
}

// bindIdentity 把平台账号记到本地账号上；已经记在别的账号上时失败。
func (s *Server) bindIdentity(key string, ref identityRef) error {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	ids, err := readJSONFile[map[string]identityRef](s.identitiesFile())
	if err != nil {
		return err
	}
	if ids == nil {
		ids = map[string]identityRef{}
	}
	if cur, ok := ids[key]; ok && cur != ref {
		return errIdentityTaken
	}
	ids[key] = ref
	return writeFileAtomic(s.identitiesFile(), ids)
}

func (s *Server) unbindIdentity(key string) error {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	ids, err := readJSONFile[map[string]identityRef](s.identitiesFile())
	if err != nil {
		return err
	}
	delete(ids, key)
	return writeFileAtomic(s.identitiesFile(), ids)
}

var errIdentityTaken = errors.New("identity taken")

var nonLogin = regexp.MustCompile(`[^a-z0-9_-]+`)

// loginSlug 把平台登录名变成租户、用户名能用的形状。
func loginSlug(login string) string {
	s := strings.Trim(nonLogin.ReplaceAllString(strings.ToLower(login), "-"), "-_")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-_")
	}
	return s
}

// reservedTenant 判断租户名是不是留给平台登录的（开发登录不许用，免得冒充）。
func reservedTenant(tenant string) bool {
	for _, sp := range providerSpecs {
		if strings.HasPrefix(tenant, sp.prefix+"-") {
			return true
		}
	}
	return false
}

// newAccountFor 给第一次登录的平台账号建本地账号：租户 <前缀>-<登录名>，被占了就加随机后缀。
func (s *Server) newAccountFor(p *Provider, u remoteUser) (identityRef, error) {
	user := loginSlug(u.Login)
	if user == "" || !slug.MatchString(user) {
		user = "user-" + randomHex(3)
	}
	tenant := p.prefix + "-" + user
	for i := 0; ; i++ {
		if _, err := os.Stat(filepath.Join(s.cfg.DataDir, tenant)); errors.Is(err, os.ErrNotExist) {
			break
		}
		if i == 0 {
			// GitHub 的旧账号（多平台之前）：同一个 GitHub 编号就是同一个人。
			if p.id == "github" {
				if a, err := s.readAccount(tenant, user); err == nil {
					if c, ok := a.Connections["github"]; ok && c.RemoteID == u.ID {
						return identityRef{Tenant: tenant, User: user}, nil
					}
				}
			}
		}
		tenant = p.prefix + "-" + user + "-" + randomHex(2)
	}
	return identityRef{Tenant: tenant, User: user}, nil
}

// loginAllowed 按 Config.AllowedLogins 判断这个平台账号能不能登录：在名单里，或者它已经关联到的本地账号连着名单里的账号。
func (s *Server) loginAllowed(provider, login, key string) bool {
	if len(s.allowed) == 0 || s.allowed[provider+":"+strings.ToLower(login)] {
		return true
	}
	ref, ok := s.lookupIdentity(key)
	if !ok {
		return false
	}
	a, err := s.readAccount(ref.Tenant, ref.User)
	if err != nil {
		return false
	}
	for _, c := range a.Connections {
		if s.allowed[c.Provider+":"+strings.ToLower(c.Login)] {
			return true
		}
	}
	return false
}

// --- OAuth 路由 ---

func (s *Server) callbackURL(provider string) string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/api/auth/" + provider + "/callback"
}

func stateCookieName(provider string) string { return "cw_oauth_" + provider }

// handleOAuthLogin 跳到平台授权。?link=1 时是给已登录的账号连接这一家（要已登录）。
func (s *Server) handleOAuthLogin(w http.ResponseWriter, r *http.Request) {
	p := s.providers[r.PathValue("provider")]
	if p == nil {
		writeMsg(w, r, http.StatusNotFound, "没有配置这个登录方式")
		return
	}
	mode := "login"
	if r.URL.Query().Get("link") == "1" {
		if _, ok := s.currentIdentity(r); !ok {
			http.Redirect(w, r, "/#/login?error="+url.QueryEscape(msg("先登录，再连接 %s", p.Name).In(requestLang(r))), http.StatusFound)
			return
		}
		mode = "link"
	}
	state := randomHex(24)
	// PKCE（AUTH-03）：code_verifier 与 state 放在同一个只对 /api/auth/<平台> 有效的 HttpOnly Cookie 里，不进授权地址。
	verifier := p.newPKCEVerifier()
	value := state + "." + mode
	if verifier != "" {
		value += "." + verifier
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookieName(p.id), Value: value, Path: "/api/auth/" + p.id, HttpOnly: true, Secure: s.cfg.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	http.Redirect(w, r, p.authorizeURL(s.callbackURL(p.id), state, verifier), http.StatusFound)
}

// currentIdentity 读出请求里已登录的身份（不写错误）。
func (s *Server) currentIdentity(r *http.Request) (identity, bool) {
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return identity{}, false
	}
	if strings.Contains(cookie.Value, ".") {
		// AUTH-02 之前登录的：Cookie 里还是令牌（authed 会把它换成会话）。
		id, err := s.ingress.Authenticate(r.Context(), ingress.Credentials{BearerToken: cookie.Value})
		if err != nil {
			return identity{}, false
		}
		return identity{Identity: id, token: cookie.Value}, true
	}
	sess, ok, err := s.sessions.lookup(cookie.Value)
	if err != nil || !ok {
		return identity{}, false
	}
	return s.sessionIdentity(cookie.Value, sess), true
}

// handleOAuthCallback 换令牌、认人，然后登录（找到或新建账号）或把这一家连接到当前账号。
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	p := s.providers[r.PathValue("provider")]
	mode := "login"
	// fail 跳回网页并带上原因（按请求的语言）；日志里记中文（平台给的错误说明，不含令牌），排查回调用。
	fail := func(why *Msg) {
		log.Printf("OAuth %s（%s）失败：%s", r.PathValue("provider"), mode, why)
		target := "/#/login?error="
		if mode == "link" {
			target = "/#/?error="
		}
		http.Redirect(w, r, target+url.QueryEscape(why.In(requestLang(r))), http.StatusFound)
	}
	if p == nil {
		fail(msg("没有配置这个登录方式"))
		return
	}
	cookie, err := r.Cookie(stateCookieName(p.id))
	http.SetCookie(w, &http.Cookie{Name: stateCookieName(p.id), Value: "", Path: "/api/auth/" + p.id, HttpOnly: true, Secure: s.cfg.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	state := r.URL.Query().Get("state")
	var want, verifier string
	if err == nil {
		var rest string
		want, rest, _ = strings.Cut(cookie.Value, ".")
		mode, verifier, _ = strings.Cut(rest, ".")
	}
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(want), []byte(state)) != 1 {
		mode = "login"
		fail(msg("登录状态不匹配，请重新登录"))
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail(msg("%s 没有授权：%s", p.Name, e))
		return
	}
	ctx, cancel := ctxTimeout(r, 30*time.Second)
	defer cancel()
	t, err := p.exchange(ctx, r.URL.Query().Get("code"), s.callbackURL(p.id), verifier)
	if err != nil {
		why, ok := err.(*Msg) // 换令牌的说明自己带着平台名
		if !ok {
			why = msg("%s 登录没有完成：%v", p.Name, err)
		}
		fail(why)
		return
	}
	u, err := p.user(ctx, t.Access)
	if err != nil {
		fail(msg("读不到 %s 账号：%v", p.Name, err))
		return
	}
	if u.ID == "" || u.Login == "" {
		fail(msg("%s 没有给出账号编号或登录名", p.Name))
		return
	}
	key := identityKey(p.id, u.ID)
	if s.blockedLogin(p.id, u.Login) {
		fail(msg("这个 %s 账号已停用", p.Name))
		return
	}
	if mode == "login" && !s.loginAllowed(p.id, u.Login, key) {
		fail(msg("这个 %s 账号还没有开通", p.Name))
		return
	}
	// 新注册（还没关联到本地账号、也不在名单里）要满足开放注册的规则（signup.go）。
	if _, known := s.lookupIdentity(key); mode == "login" && !known && !s.allowed[p.id+":"+strings.ToLower(u.Login)] {
		if why := s.signupCheck(ctx, p, t.Access, u); why != nil {
			fail(why)
			return
		}
	}
	now := time.Now().UTC()
	conn := Connection{Provider: p.id, RemoteID: u.ID, Login: u.Login, Name: u.Name, AvatarURL: u.AvatarURL, Email: u.Email, ConnectedAt: now}

	var ref identityRef
	if mode == "link" {
		cur, ok := s.currentIdentity(r)
		if !ok {
			fail(msg("登录已过期，请重新登录后再连接"))
			return
		}
		ref = identityRef{Tenant: cur.TenantID, User: cur.UserID}
		if err := s.bindIdentity(key, ref); errors.Is(err, errIdentityTaken) {
			fail(msg("这个 %s 账号已经关联到别的账号了", p.Name))
			return
		} else if err != nil {
			fail(msg("保存关联失败"))
			return
		}
	} else if found, ok := s.lookupIdentity(key); ok {
		ref = found
	} else {
		if ref, err = s.newAccountFor(p, u); err != nil {
			fail(msg("建账号失败"))
			return
		}
		if err := s.bindIdentity(key, ref); err != nil {
			fail(msg("保存关联失败"))
			return
		}
		if err := s.ensureTenantPolicy(ref.Tenant); err != nil {
			fail(msg("写租户策略失败：%v", err))
			return
		}
	}
	if _, err := s.updateAccount(ref.Tenant, ref.User, func(a *Account) error {
		if old, ok := a.Connections[p.id]; ok {
			if old.RemoteID != u.ID {
				return errIdentityTaken // 这个账号已经连着这一家的另一个账号
			}
			conn.ConnectedAt = old.ConnectedAt
		}
		if err := s.sealConnection(ref.Tenant, ref.User, &conn, t); err != nil {
			return err
		}
		a.Connections[p.id] = conn
		if a.Primary == "" {
			a.Primary = p.id
		}
		return nil
	}); errors.Is(err, errIdentityTaken) {
		_ = s.unbindIdentity(key)
		fail(msg("这个账号已经连着另一个 %s 账号，先断开它", p.Name))
		return
	} else if err != nil {
		fail(msg("保存 %s 账号失败", p.Name))
		return
	}
	if mode == "link" {
		log.Printf("OAuth %s（link）成功：%s 连到 %s/%s", p.id, u.Login, ref.Tenant, ref.User)
		http.Redirect(w, r, "/#/?linked="+p.id, http.StatusFound)
		return
	}
	log.Printf("OAuth %s（login）成功：%s 登录为 %s/%s", p.id, u.Login, ref.Tenant, ref.User)
	if _, err := s.setSession(w, ref.Tenant, ref.User); err != nil {
		fail(msg("建会话失败：%v", err))
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// --- 账号接口 ---

type connectionView struct {
	Provider    string    `json:"provider"`
	Name        string    `json:"provider_name"`
	Login       string    `json:"login"`
	DisplayName string    `json:"name,omitempty"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	Git         bool      `json:"git"`
	ConnectedAt time.Time `json:"connected_at"`
}

// connections 是账号已连接的登录方式（不含令牌），按 providerSpecs 的顺序。
func (s *Server) connections(id identity) (Account, []connectionView) {
	a, err := s.readAccount(id.TenantID, id.UserID)
	out := []connectionView{}
	if err != nil {
		return Account{}, out
	}
	for _, sp := range providerSpecs {
		c, ok := a.Connections[sp.id]
		if !ok {
			continue
		}
		name := sp.name
		if p := s.providers[sp.id]; p != nil {
			name = p.Name
		}
		out = append(out, connectionView{Provider: sp.id, Name: name, Login: c.Login, DisplayName: c.Name, AvatarURL: c.AvatarURL, Git: sp.git, ConnectedAt: c.ConnectedAt})
	}
	return a, out
}

func (s *Server) handleAccount(w http.ResponseWriter, _ *http.Request, id identity) {
	_, conns := s.connections(id)
	writeJSON(w, http.StatusOK, map[string]any{"tenant": id.TenantID, "user": id.UserID, "connections": conns, "dev_account": !reservedTenant(id.TenantID)})
}

// handleDisconnect 断开一家。平台账号建的账号至少要留一个登录方式（开发登录的账号可以全断）。
func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request, id identity) {
	provider := r.PathValue("provider")
	var removed Connection
	_, err := s.updateAccount(id.TenantID, id.UserID, func(a *Account) error {
		c, ok := a.Connections[provider]
		if !ok {
			return errNotConnected
		}
		if len(a.Connections) == 1 && reservedTenant(id.TenantID) {
			return errLastLogin
		}
		removed = c
		delete(a.Connections, provider)
		if a.Primary == provider {
			a.Primary = ""
			for _, sp := range providerSpecs {
				if _, ok := a.Connections[sp.id]; ok {
					a.Primary = sp.id
					break
				}
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, errNotConnected):
		writeMsg(w, r, http.StatusNotFound, "没有连接这个平台")
		return
	case errors.Is(err, errLastLogin):
		writeMsg(w, r, http.StatusConflict, "这是这个账号唯一的登录方式，先连接另一家再断开")
		return
	case err != nil:
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	_ = s.unbindIdentity(identityKey(provider, removed.RemoteID))
	_, conns := s.connections(id)
	writeJSON(w, http.StatusOK, map[string]any{"connections": conns})
}

var errLastLogin = errors.New("last login")

// handleRepos 列出账号在一家 Git 平台上能推送的仓库（新建工作区时选）。
func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request, id identity) {
	name := r.PathValue("provider")
	if name == "" {
		name = "github" // 旧地址 /api/github/repos
	}
	p := s.providers[name]
	if p == nil || !p.git {
		writeMsg(w, r, http.StatusNotFound, "没有配置这个 Git 平台")
		return
	}
	conn, token, err := s.connToken(r, id, p)
	if errors.Is(err, errNotConnected) {
		writeMsg(w, r, http.StatusForbidden, "先在“账号与平台”里连接 %s", p.Name)
		return
	} else if err != nil {
		writeGitError(w, r, err)
		return
	}
	ctx, cancel := ctxTimeout(r, 30*time.Second)
	defer cancel()
	repos, err := p.repos(ctx, token)
	if err != nil {
		writeGitError(w, r, err)
		return
	}
	for i := range repos {
		owner, _, _ := strings.Cut(repos[i].FullName, "/")
		repos[i].Mine = conn.Login != "" && strings.EqualFold(owner, conn.Login)
	}
	// 自己名下的在前，再按能不能推送；同一组里保持平台给的顺序（最近活动的在前）。
	rank := func(x remoteRepo) int {
		switch {
		case x.Mine:
			return 0
		case x.CanPush:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(repos, func(a, b remoteRepo) int { return rank(a) - rank(b) })
	writeJSON(w, http.StatusOK, repos)
}

// providerOfURL 认出仓库地址属于哪一家已配置的 Git 平台，返回平台与仓库路径。
func (s *Server) providerOfURL(raw string) (*Provider, string) {
	for _, sp := range providerSpecs {
		if p := s.providers[sp.id]; p != nil {
			if repo, ok := p.repoOf(raw); ok {
				return p, repo
			}
		}
	}
	return nil, ""
}

// gitCredentials 是对这个工作区的平台做 git 操作要带的认证；没有连接时为空，按公开仓库处理。
func (s *Server) gitCredentials(r *http.Request, id identity, p Project) GitAuth {
	prov := s.providers[p.Provider]
	if prov == nil {
		return GitAuth{}
	}
	c, token, err := s.connToken(r, id, prov)
	if err != nil {
		return GitAuth{}
	}
	user, pass := prov.gitAuth(c, token)
	return GitAuth{Host: prov.Web + "/", User: user, Pass: pass}
}

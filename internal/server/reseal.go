package server

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ResealReport 是 Reseal 的结果：看过的账号数，改用当前密钥重新加密的平台令牌（按账号计）与环境配置的密钥，
// 以及哪把密钥都解不开、只能原样留着的值。
type ResealReport struct {
	Accounts, AccountsResealed, Secrets, Unreadable int
}

// Reseal 把数据目录里还用旧密钥（开发签名密钥派生的，或 CODING_TOKEN_KEY_PREVIOUS）加密的平台令牌与环境配置的密钥，
// 一次改用当前的 CODING_TOKEN_KEY。原来只在读到时就地迁移（rekey），没读到过的一直是旧格式；AUTH-01 第 4 步要从 coding-web
// 拿掉开发签名密钥，之前先全部迁完，不用等用户各自登录一遍（2026-11-06）。
// 启动时在开始服务之前跑（cmd/coding-web），与请求不并发；没有旧密钥时什么也不做。
func (s *Server) Reseal() (ResealReport, error) {
	var r ResealReport
	if len(s.oldKeys()) == 0 || len(s.cfg.TokenKey) == 0 {
		return r, nil
	}
	tenants, err := os.ReadDir(s.cfg.DataDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return r, nil
		}
		return r, err
	}
	for _, t := range tenants {
		if !t.IsDir() {
			continue
		}
		tenant := t.Name()
		if err := s.resealAccounts(tenant, &r); err != nil {
			return r, err
		}
		if err := s.resealEnvironments(tenant, &r); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (s *Server) resealAccounts(tenant string, r *ResealReport) error {
	users, err := os.ReadDir(filepath.Join(s.cfg.DataDir, tenant, "users"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, u := range users {
		user, ok := strings.CutSuffix(u.Name(), ".json")
		if u.IsDir() || !ok {
			continue
		}
		before, err := os.ReadFile(s.accountFile(tenant, user))
		if err != nil {
			return err
		}
		// loadAccount 里的 rekey 把旧密钥加密的令牌改用当前密钥并写回（就是平时读到时的迁移）。
		a, err := s.readAccount(tenant, user)
		if err != nil {
			return err
		}
		r.Accounts++
		if after, err := os.ReadFile(s.accountFile(tenant, user)); err == nil && string(after) != string(before) {
			r.AccountsResealed++
		}
		for provider, c := range a.Connections {
			if c.Token != "" {
				if _, err := openWith(s.tokenKey(), c.Token, tokenAAD(tenant, user, provider)); err != nil {
					r.Unreadable++
				}
			}
			if c.Refresh != "" {
				if _, err := openWith(s.tokenKey(), c.Refresh, tokenAAD(tenant, user, provider)+"/refresh"); err != nil {
					r.Unreadable++
				}
			}
		}
	}
	return nil
}

func (s *Server) resealEnvironments(tenant string, r *ResealReport) error {
	if _, err := os.Stat(s.store.environmentsFile(tenant)); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	current := secretKey(s.tokenKey())
	return s.store.UpdateEnvironments(tenant, func(envs []Environment) ([]Environment, error) {
		for i := range envs {
			env := &envs[i]
			for j := range env.Secrets {
				sec := &env.Secrets[j]
				aad := secretAAD(tenant, env.CreatedBy, env.ID, sec.Name)
				if _, err := openWith(current, sec.Sealed, aad); err == nil {
					continue
				}
				resealed := false
				for _, key := range s.oldKeys() {
					plain, err := openWith(secretKey(key), sec.Sealed, aad)
					if err != nil {
						continue
					}
					if sec.Sealed, err = sealWith(current, plain, aad); err != nil {
						return nil, err
					}
					r.Secrets++
					resealed = true
					break
				}
				if !resealed {
					r.Unreadable++
				}
			}
		}
		return envs, nil
	})
}

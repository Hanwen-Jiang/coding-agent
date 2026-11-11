package main

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/agentruntime/coding-agent/internal/entry"
	entrymail "github.com/agentruntime/coding-agent/internal/entry/email"
	"github.com/agentruntime/coding-agent/internal/server"
)

// configureEntries loads operator settings; errors name fields but never include
// DSNs, SMTP credentials or values returned by a remote server.
func configureEntries(ctx context.Context, cfg *server.Config, getenv func(string) string) error {
	dsn := strings.TrimSpace(getenv("CODING_ENTRY_DSN"))
	if dsn == "" {
		dsn = strings.TrimSpace(getenv("CODING_GITHUB_DSN"))
	}
	memory := getenv("CODING_ENTRY_MEMORY")
	if memory != "" && memory != "0" && memory != "1" {
		return errors.New("CODING_ENTRY_MEMORY 只能是 0 或 1")
	}
	if memory == "1" && (!cfg.DevLogin || cfg.JWTKey != nil || getenv("CODING_PROFILE") == "production" || getenv("AR_PROFILE") == "production") {
		return errors.New("CODING_ENTRY_MEMORY 只允许无 JWT 的开发登录模式，生产环境必须使用 Postgres")
	}
	if memory == "1" && dsn != "" {
		return errors.New("CODING_ENTRY_MEMORY 不能与 CODING_ENTRY_DSN 或 CODING_GITHUB_DSN 同时使用")
	}
	smtpConfig, err := entrySMTPConfig(getenv)
	if err != nil {
		return err
	}
	if smtpConfig != nil && dsn == "" && memory != "1" {
		return errors.New("配置 CODING_SMTP_ADDR 前必须配置 CODING_ENTRY_DSN 或 CODING_GITHUB_DSN")
	}
	if memory == "1" {
		cfg.EntryStore = entry.NewMemory()
	} else if dsn != "" {
		connect, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		db, err := connectEntryStore(connect, dsn)
		if err != nil {
			return errors.New("CODING_ENTRY_DSN（或 CODING_GITHUB_DSN）连接或初始化失败")
		}
		cfg.EntryStore = db
	}
	if smtpConfig != nil {
		cfg.EntryMail = &entrymail.Sink{Config: *smtpConfig}
	}
	if value := getenv("CODING_RUNTIME_TASK_BUDGET"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return errors.New("CODING_RUNTIME_TASK_BUDGET 必须为 true/false 或 1/0")
		}
		cfg.RuntimeTaskBudget = enabled
	}
	return nil
}

// connectEntryStore 在 ctx 到期前重试连接与建表：Pod 刚起来时网络可能还没好，
// 数据库也可能正在重启。连接串不合格时不重试。日志不带原始错误，里面可能有连接串。
func connectEntryStore(ctx context.Context, dsn string) (*entry.PostgresStore, error) {
	wait := 500 * time.Millisecond
	for {
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		db, err := entry.NewPostgres(attempt, dsn)
		cancel()
		if err == nil || errors.Is(err, entry.ErrInvalidDSN) {
			return db, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(wait):
		}
		log.Printf("coding-web: 接入点的数据库还没连上，重试")
		wait = min(2*wait, 2*time.Second)
	}
}

func entrySMTPConfig(getenv func(string) string) (*entrymail.SMTPConfig, error) {
	fields := []string{"ADDR", "USERNAME", "PASSWORD", "FROM", "IMPLICIT_TLS"}
	any := false
	for _, field := range fields {
		any = any || getenv("CODING_SMTP_"+field) != ""
	}
	if !any {
		return nil, nil
	}
	cfg := &entrymail.SMTPConfig{Addr: strings.TrimSpace(getenv("CODING_SMTP_ADDR")), Username: getenv("CODING_SMTP_USERNAME"), Password: getenv("CODING_SMTP_PASSWORD"), From: strings.TrimSpace(getenv("CODING_SMTP_FROM"))}
	if cfg.Addr == "" || cfg.From == "" {
		return nil, errors.New("邮件出口必须同时配置 CODING_SMTP_ADDR 与 CODING_SMTP_FROM")
	}
	if (cfg.Username == "") != (cfg.Password == "") {
		return nil, errors.New("CODING_SMTP_USERNAME 与 CODING_SMTP_PASSWORD 必须成对配置")
	}
	if value := getenv("CODING_SMTP_IMPLICIT_TLS"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return nil, errors.New("CODING_SMTP_IMPLICIT_TLS 必须为 true/false 或 1/0")
		}
		cfg.ImplicitTLS = enabled
	}
	if cfg.Validate() != nil {
		return nil, errors.New("CODING_SMTP_ADDR 或 CODING_SMTP_FROM 格式不合法")
	}
	return cfg, nil
}

package server

// runtimeMsg 把 Runtime 错误模型里稳定的 code（APIError.Reason）翻成给用户看的话；不认识的返回 nil（中文界面原样给
// Runtime 的说明，英文界面只给错误码）。只收用户在这个应用里会碰到的：工作区、Git、沙箱与联网。git_failed 不收：
// Runtime 的说明里带着 git 的输出，中文界面原样给更有用。
func runtimeMsg(reason string) *Msg {
	switch reason {
	case "task_finished":
		return msg("任务已经结束，可以作为新任务发送")
	case "task_not_started":
		return msg("任务还没开始，可以直接修改这条消息")
	case "network_not_allowed":
		return msg("账号的策略不允许沙箱联网")
	case "host_not_allowed":
		return msg("账号的策略不允许连这个主机")
	case "network_read_only":
		return msg("只读的任务不能联网")
	case "effect_ceiling_exceeds_policy":
		return msg("选的沙箱等级超出了账号的策略")
	case "workspace_not_found":
		return msg("工作区不存在")
	case "workspace_exists":
		return msg("工作区已经存在")
	case "workspace_dirty":
		return msg("还有未提交的改动：先推送、提交或丢弃")
	case "workspace_busy", "workspace_operation_pending":
		return msg("工作区正在做别的操作，稍后再试")
	case "workspace_quota_exceeded":
		return msg("工作区数量已到上限")
	case "workspace_operation_failed", "workspace_api_unavailable":
		return msg("工作区的操作暂时失败，稍后再试")
	case "sandbox_unavailable", "no_sandbox_backend":
		return msg("沙箱暂时起不来，稍后再试")
	case "too_large":
		return msg("仓库或文件太大")
	case "invalid_repo_url":
		return msg("仓库地址不对")
	case "credentials_in_url":
		return msg("仓库地址里不能带账号或口令")
	case "git_denied":
		return msg("Git 平台拒绝了访问：检查仓库地址与授权")
	case "git_not_found":
		return msg("Git 平台上找不到这个仓库或分支")
	case "git_timeout":
		return msg("Git 操作超时")
	case "git_unavailable":
		return msg("连不上 Git 平台，稍后再试")
	case "push_rejected":
		return msg("远端拒绝了推送：远端可能有新的提交，先同步")
	case "nothing_to_push":
		return msg("没有要推送的提交")
	case "rate_limit":
		return msg("请求太频繁，稍后再试")
	}
	return nil
}

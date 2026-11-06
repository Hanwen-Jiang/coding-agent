package server

// runtimeMsg 把 Runtime 错误模型里稳定的 code（APIError.Reason）翻成给用户看的话；不认识的返回 nil（中文界面原样给
// Runtime 的说明，英文界面只给错误码）。只收用户在这个应用里会碰到的：工作区、Git、沙箱与联网。git_failed 不收：
// Runtime 的说明里带着 git 的输出，中文界面原样给更有用。
func runtimeMsg(reason string) *Msg {
	switch reason {
	case "personal_config_version_conflict":
		return msg("个人配置已更新，请重新读取后保存")
	case "personal_skill_unavailable", "personal_mcp_unavailable":
		return msg("个人配置引用的工具当前不可用，请重新选择")
	case "personal_config_preflight_failed", "personal_config_write_failed":
		return msg("个人配置文件写入失败，任务尚未执行")
	case "secret_in_config", "invalid_config_path", "executable_config", "executable_or_secret_config":
		return msg("个人配置文件包含密钥或不允许的路径")
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
	case "workspace_not_ready":
		return msg("工作区还在创建，稍等一会儿再发")
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
	// 我的 Skill（Runtime 的 N-14）。
	case "skills_unavailable":
		return msg("这个部署还没有 Skill")
	case "name_taken":
		return msg("平台或团队已经有这个名字的 Skill，换一个名字")
	case "skill_limit":
		return msg("每个人最多 100 个 Skill，先收回不用的")
	case "skill_too_large":
		return msg("Skill 太大：整份最多 256 KiB")
	case "duplicate_path":
		return msg("Skill 的文件路径重复了（不分大小写），或一个是另一个的目录")
	case "skill_not_found":
		return msg("没有这个 Skill")
	// 我的 MCP（Runtime 的 N-15）。
	case "mcp_not_allowed":
		return msg("账号的策略不允许登记 MCP Server")
	case "mcp_connection_not_found":
		return msg("MCP 连接不存在")
	case "mcp_connection_exists":
		return msg("已经有同名、设置不同的 MCP 连接：换个名字，或先删掉旧的")
	case "mcp_connection_limit":
		return msg("MCP 连接已经到了上限，先删掉不用的")
	case "mcp_host_not_allowed":
		return msg("账号的策略不允许连这个 MCP Server 的主机")
	case "mcp_host_not_public":
		return msg("MCP Server 的地址要是公网域名（不收 IP 与内网地址）")
	case "invalid_mcp_url":
		return msg("MCP Server 的地址要是 https://<域名>/...，不带账号密码")
	case "invalid_mcp_name":
		return msg("连接的名字要小写字母开头，只用小写字母、数字与 _，最多 32 个字符")
	case "invalid_mcp_auth":
		return msg("网络密钥的名字或请求头名不对")
	case "invalid_mcp_effect":
		return msg("这个工具远端没说只读，只能定为不可逆写（每次调用前要你批准）")
	case "effect_over_limit":
		return msg("账号的策略不允许给 MCP 工具定这么高的权限")
	case "mcp_schema_changed":
		return msg("这个工具的参数变了：先刷新，再确认")
	case "tool_name_conflict":
		return msg("这个工具的名字与同一连接里的另一个撞了，打不开")
	case "mcp_tool_not_found":
		return msg("连接上没有这个工具：先刷新")
	}
	return nil
}

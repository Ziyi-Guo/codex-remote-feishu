package codex

import (
	"encoding/json"
	"fmt"

	"github.com/kxn/codex-remote-feishu/internal/limitedgroups"
)

func limitedReadOnlyInstructions(p *limitedgroups.ExecutionPolicy) string {
	return fmt.Sprintf(`此会话是 prof.C 限定同事查询试用，源项目目录：%s。
执行策略：只读文件系统、允许联网、approval=never。直接完成只读查询，不请求郭或其他人点击授权，不提审批单。禁止任何业务写入、创建/更新/删除/生成、付费调用、安装/升级、修改配置或权限。命中 forbidden 或只读限制时明确回复已拒绝，不改写命令、换解释器、脚本、HTTP/MCP/浏览器等路径绕过。
查询优先使用现有技能与已安装 CLI；bytedcli 加 --no-auto-upgrade。RDS 仅只读查询；先核实表和字段再查询，只取所需字段。IPGen 客户端默认 PPE，查正式项目元数据应使用 RDS。日志保留查询与被拒证据。`, p.Workspace)
}

func (t *Translator) handleLimitedAuthorization(message map[string]any) (Result, bool) {
	if result, handled := t.declineLimitedAuthorization(message); handled {
		return result, true
	}
	return t.autoApproveLimitedRead(message)
}

// Never-mode requests are answered at the native boundary before UI projection.
// Ordinary request_user_input questions are deliberately left to their owner.
func (t *Translator) declineLimitedAuthorization(message map[string]any) (Result, bool) {
	if t.limitedExecution == nil || !t.limitedExecution.ReadOnlyNetworkNever() {
		return Result{}, false
	}
	var response any
	switch message["method"] {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		response = map[string]any{"decision": "decline"}
	case "item/permissions/requestApproval":
		response = map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	case "mcpServer/elicitation/request":
		response = map[string]any{"action": "decline", "content": nil}
	case "execCommandApproval", "applyPatchApproval":
		response = map[string]any{"decision": "denied"}
	default:
		return Result{}, false
	}
	id, ok := message["id"]
	if !ok {
		return Result{}, false
	}
	raw, _ := json.Marshal(map[string]any{"id": id, "result": response})
	t.Debugf("limited never-mode authorization rejected: method=%v request=%v", message["method"], id)
	return Result{Suppress: true, OutboundToCodex: [][]byte{append(raw, '\n')}}, true
}

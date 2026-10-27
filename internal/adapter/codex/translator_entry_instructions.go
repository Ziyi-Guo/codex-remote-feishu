package codex

import "github.com/kxn/codex-remote-feishu/internal/core/agentproto"

func (t *Translator) needsDeveloperInstructionRefresh(thread string, policy *agentproto.CodexResumePolicy) bool {
	policy = agentproto.NormalizeCodexResumePolicy(policy)
	if policy == nil {
		return false
	}
	applied, known := t.appliedDeveloperInstructions[thread]
	return (known && applied != policy.DeveloperInstruction) || (!known && policy.DeveloperInstruction != "")
}

func (t *Translator) noteDeveloperInstructionsApplied(thread string, policy *agentproto.CodexResumePolicy) {
	if policy = agentproto.NormalizeCodexResumePolicy(policy); policy != nil {
		t.appliedDeveloperInstructions[thread] = policy.DeveloperInstruction
	}
}

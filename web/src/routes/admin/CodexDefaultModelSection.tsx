import { useEffect, useState } from "react";
import { formatError, requestJSON, sendJSON } from "../../lib/api";

type ModelSelection = { model: string; reasoningEffort: string };
type RemoteDefault = ModelSelection & {
  validation?: "verified" | "unverified";
  message?: string;
  appliedModel?: string;
  appliedReasoningEffort?: string;
  pendingChanges?: boolean;
};
type ModelCatalog = {
  entries: {
    model: string;
    displayName: string;
    supportedReasoningEfforts?: { reasoningEffort: string; description?: string }[];
  }[];
  validation: "verified" | "unverified";
};
const inherited: ModelSelection = { model: "", reasoningEffort: "" };
const standardEfforts = ["low", "medium", "high", "xhigh", "max"];

export function CodexDefaultModelSection() {
  const [draft, setDraft] = useState<ModelSelection>(inherited);
  const [applied, setApplied] = useState<ModelSelection | null>(null);
  const [pendingChanges, setPendingChanges] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [catalog, setCatalog] = useState<ModelCatalog | null>(null);

  useEffect(() => {
    let active = true;
    requestJSON<RemoteDefault>("/api/admin/codex/default-model")
      .then((value) => {
        if (!active) return;
        setDraft({ model: value.model, reasoningEffort: value.reasoningEffort });
        setApplied({
          model: value.appliedModel ?? value.model,
          reasoningEffort: value.appliedReasoningEffort ?? value.reasoningEffort,
        });
        setPendingChanges(value.pendingChanges ?? false);
      })
      .catch((error) => {
        if (active) setNotice(`默认模型读取失败：${formatError(error)}`);
      });
    requestJSON<ModelCatalog>("/api/admin/codex/model-catalog")
      .then((value) => { if (active) setCatalog(value); })
      .catch(() => {
        if (active) setCatalog({ entries: [], validation: "unverified" });
      });
    return () => { active = false; };
  }, []);

  const selected = catalog?.entries.find((entry) => entry.model === draft.model.trim());
  const supportedEfforts = selected?.supportedReasoningEfforts?.map((entry) => entry.reasoningEffort)
    .filter((effort) => standardEfforts.includes(effort)) ?? [];
  const hasKnownEfforts = Boolean(selected?.supportedReasoningEfforts?.length);
  const noCompatibleEfforts = hasKnownEfforts && supportedEfforts.length === 0;
  const effortOptions = hasKnownEfforts ? supportedEfforts : standardEfforts;
  // Keep a saved value visible until the user explicitly replaces it.
  const displayedEfforts = standardEfforts.includes(draft.reasoningEffort) && !effortOptions.includes(draft.reasoningEffort)
    ? [draft.reasoningEffort, ...effortOptions] : effortOptions;

  function changeModel(model: string) {
    const next = catalog?.entries.find((entry) => entry.model === model.trim());
    const efforts = next?.supportedReasoningEfforts?.map((entry) => entry.reasoningEffort)
      .filter((effort) => standardEfforts.includes(effort)) ?? [];
    setDraft((current) => ({
      model,
      reasoningEffort: !model.trim() ? "" : efforts.length
        ? (efforts.includes(current.reasoningEffort) ? current.reasoningEffort
          : efforts.includes("high") ? "high" : efforts[0])
        : current.reasoningEffort || "high",
    }));
    setNotice("");
  }

  async function save(value: ModelSelection) {
    setBusy(true);
    setNotice("");
    try {
      const saved = await sendJSON<RemoteDefault>("/api/admin/codex/default-model", "PUT", value);
      const selection = { model: saved.model, reasoningEffort: saved.reasoningEffort };
      setDraft(selection);
      setApplied(selection);
      setPendingChanges(false);
      setNotice(saved.validation === "unverified"
        ? "默认模型已保存并应用；模型可用性尚未验证。" : "默认模型已保存并应用。");
    } catch (error) {
      setNotice(`保存或应用未完成：${formatError(error)}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="hero-card">
      <h3>Remote 默认模型</h3>
      <p>用于 Codex 本机或 ChatGPT 登录配置中未指定模型的对话。已有话题的显式模型设置优先；排队中的请求保持原选择。</p>
      {applied ? <p>当前已应用：{applied.model
        ? `${applied.model} · ${applied.reasoningEffort || "跟随本机"}` : "继承 Codex 配置"}</p> : null}
      {pendingChanges ? <p role="status">配置文件中的修改尚未应用。保存后应用下方设置。</p> : null}
      <form onSubmit={(event) => { event.preventDefault(); if (!noCompatibleEfforts) void save(draft); }}>
        <div className="form-grid stack-top">
          <label>Remote 默认模型
            <input list="codex-remote-default-models" value={draft.model} disabled={!applied || busy}
              onChange={(event) => changeModel(event.target.value)} />
          </label>
          <datalist id="codex-remote-default-models">
            {(catalog?.entries ?? []).map((entry) =>
              <option key={entry.model} value={entry.model}>{entry.displayName || entry.model}</option>)}
          </datalist>
          <label>默认推理强度
            <select value={draft.reasoningEffort} disabled={!applied || busy || noCompatibleEfforts}
              onChange={(event) => {
                setDraft((current) => ({ ...current, reasoningEffort: event.target.value }));
                setNotice("");
              }}>
              <option value="">跟随本机</option>
              {displayedEfforts.map((effort) => <option key={effort} value={effort}>{effort}</option>)}
            </select>
          </label>
        </div>
        {noCompatibleEfforts ? <p role="status">当前 Remote 没有兼容此模型的推理强度，请选择其他模型或继承 Codex 配置。</p> : null}
        {catalog === null ? <p>正在读取模型列表…</p>
          : catalog.validation === "unverified" ? <p>模型列表尚未验证，可手动填写模型 ID。</p>
          : draft.model && !selected ? <p>所填模型尚未验证，保存时将再次检查。</p> : null}
        <button type="submit" disabled={!applied || busy || noCompatibleEfforts}>保存默认模型</button>
        <button type="button" disabled={!applied || busy} onClick={() => void save(inherited)}>继承 Codex 配置</button>
      </form>
      {notice ? <p role="status">{notice}</p> : null}
    </section>
  );
}

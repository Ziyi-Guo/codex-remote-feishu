import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { installMockFetch } from "../../test/http";
import { CodexDefaultModelSection } from "./CodexDefaultModelSection";

const defaultsPath = "/api/admin/codex/default-model";
const catalogPath = "/api/admin/codex/model-catalog";
const current = { model: "gpt-6.1-sol", reasoningEffort: "high" };
const catalog = {
  validation: "verified",
  entries: [{ model: current.model, displayName: "GPT 6.1 Sol", supportedReasoningEfforts: [
    { reasoningEffort: "medium" }, { reasoningEffort: "high" },
  ] }],
};

describe("CodexDefaultModelSection", () => {
  it("uses runtime model suggestions and supported efforts without auto-saving", async () => {
    const { calls } = installMockFetch({
      [defaultsPath]: { body: current }, [catalogPath]: { body: catalog },
    });
    render(<CodexDefaultModelSection />);
    expect(await screen.findByText("GPT 6.1 Sol", { selector: "option" })).toHaveValue(current.model);
    const efforts = screen.getByLabelText("默认推理强度");
    await waitFor(() => expect(efforts).toHaveValue("high"));
    expect(within(efforts).getAllByRole("option").map((option) => option.getAttribute("value")))
      .toEqual(["", "medium", "high"]);
    expect(calls.every((call) => call.method === "GET")).toBe(true);
  });

  it.each([undefined, []])("handles missing or unknown model efforts", async (supportedReasoningEfforts) => {
    const user = userEvent.setup();
    installMockFetch({
      [defaultsPath]: { body: current },
      [catalogPath]: { body: { ...catalog, entries: [{ model: "future-model", supportedReasoningEfforts }] } },
    });
    render(<CodexDefaultModelSection />);
    const model = screen.getByLabelText("Remote 默认模型");
    await waitFor(() => expect(model).toHaveValue(current.model));
    await user.clear(model);
    await user.type(model, "future-model");
    expect(within(screen.getByLabelText("默认推理强度")).getAllByRole("option")
      .map((option) => option.getAttribute("value"))).toEqual(["", "low", "medium", "high", "xhigh", "max"]);
  });

  it("filters runtime efforts that Remote does not support", async () => {
    installMockFetch({
      [defaultsPath]: { body: current },
      [catalogPath]: { body: { ...catalog, entries: [{ ...catalog.entries[0],
        supportedReasoningEfforts: ["none", "minimal", "high", "max", "ultra"].map((reasoningEffort) => ({ reasoningEffort })),
      }] } },
    });
    render(<CodexDefaultModelSection />);
    await screen.findByText("GPT 6.1 Sol", { selector: "option" });
    expect(within(screen.getByLabelText("默认推理强度")).getAllByRole("option")
      .map((option) => option.getAttribute("value"))).toEqual(["", "high", "max"]);
  });

  it("blocks an incompatible model without falling back to unrelated efforts", async () => {
    const user = userEvent.setup();
    const { calls } = installMockFetch({
      [defaultsPath]: (call) => ({ body: call.method === "PUT"
        ? { ...JSON.parse(String(call.init?.body)), validation: "verified" } : current }),
      [catalogPath]: { body: { ...catalog, entries: [{ ...catalog.entries[0],
        supportedReasoningEfforts: [{ reasoningEffort: "ultra" }],
      }] } },
    });
    render(<CodexDefaultModelSection />);
    expect(await screen.findByText(/没有兼容此模型的推理强度/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存默认模型" })).toBeDisabled();
    const efforts = screen.getByLabelText("默认推理强度");
    expect(efforts).toBeDisabled();
    expect(within(efforts).getAllByRole("option").map((option) => option.getAttribute("value")))
      .toEqual(["", "high"]);
    expect(efforts).toHaveValue("high");
    await user.click(screen.getByRole("button", { name: "保存默认模型" }));
    expect(calls.every((call) => call.method === "GET")).toBe(true);
    await user.click(screen.getByRole("button", { name: "继承 Codex 配置" }));
    expect(await screen.findByText("默认模型已保存并应用。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存默认模型" })).toBeEnabled();
  });

  it.each([
    { body: { entries: [], validation: "unverified" } },
    { status: 503, body: { error: { message: "catalog unavailable" } } },
  ])("keeps manual entry available when the catalog cannot be verified", async (response) => {
    const user = userEvent.setup();
    const { calls } = installMockFetch({
      [defaultsPath]: (call) => ({ body: call.method === "PUT"
        ? { ...JSON.parse(String(call.init?.body)), applied: true, validation: "unverified" }
        : current }),
      [catalogPath]: response,
    });
    render(<CodexDefaultModelSection />);
    expect(await screen.findByText(/模型列表尚未验证/)).toBeInTheDocument();
    const model = screen.getByLabelText("Remote 默认模型");
    await user.clear(model);
    await user.type(model, "custom-future-model");
    await user.click(screen.getByRole("button", { name: "保存默认模型" }));
    expect(await screen.findByText(/已保存并应用.*尚未验证/)).toBeInTheDocument();
    expect(calls.find((call) => call.method === "PUT")?.init?.body).toContain("custom-future-model");
  });

  it("preserves the draft and applied selection after a failed save", async () => {
    const user = userEvent.setup();
    installMockFetch({
      [defaultsPath]: (call) => call.method === "PUT"
        ? { status: 500, body: { error: { message: "write failed" } } }
        : { body: current },
      [catalogPath]: { body: catalog },
    });
    render(<CodexDefaultModelSection />);
    const model = screen.getByLabelText("Remote 默认模型");
    await waitFor(() => expect(model).toHaveValue(current.model));
    await user.clear(model);
    await user.type(model, "another-model");
    await user.click(screen.getByRole("button", { name: "保存默认模型" }));
    expect(await screen.findByText(/保存或应用未完成：write failed/)).toBeInTheDocument();
    expect(model).toHaveValue("another-model");
    expect(screen.getByText(/当前已应用：gpt-6.1-sol · high/)).toBeInTheDocument();
    expect(screen.queryByText(/已保存并应用/)).not.toBeInTheDocument();
  });

  it("distinguishes a persisted candidate from the active default", async () => {
    installMockFetch({
      [defaultsPath]: { body: { ...current, pendingChanges: true,
        appliedModel: "old-model", appliedReasoningEffort: "medium" } },
      [catalogPath]: { body: catalog },
    });
    render(<CodexDefaultModelSection />);
    expect(await screen.findByText(/配置文件中的修改尚未应用/)).toBeInTheDocument();
    expect(screen.getByText(/当前已应用：old-model · medium/)).toBeInTheDocument();
    expect(screen.getByLabelText("Remote 默认模型")).toHaveValue(current.model);
  });

  it("can explicitly return to inherited Codex settings", async () => {
    const user = userEvent.setup();
    const { calls } = installMockFetch({
      [defaultsPath]: (call) => ({ body: call.method === "PUT"
        ? { ...JSON.parse(String(call.init?.body)), applied: true, validation: "verified" }
        : current }),
      [catalogPath]: { body: catalog },
    });
    render(<CodexDefaultModelSection />);
    const button = screen.getByRole("button", { name: "继承 Codex 配置" });
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);
    expect(await screen.findByText("默认模型已保存并应用。")).toBeInTheDocument();
    expect(JSON.parse(String(calls.find((call) => call.method === "PUT")?.init?.body)))
      .toEqual({ model: "", reasoningEffort: "" });
    expect(screen.getByText("当前已应用：继承 Codex 配置")).toBeInTheDocument();
  });
});

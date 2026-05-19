import { useEffect, useState } from "react";
import type { ModelRecord } from "@elucid-relay/contracts";
import { api } from "../../api";
import { DataTable, Metric } from "../../components/DataTable";
import { SectionHeader } from "../../components/Primitives";
import { EmptyState } from "../shared";

export function ModelsView() {
  const [rows, setRows] = useState<ModelRecord[]>([]);
  useEffect(() => {
    void api.request<ModelRecord[]>("/api/portal/v1/models").then(setRows);
  }, []);
  const activeModels = rows.filter((row) => row.status === "active" && row.public_visible !== false).length;
  return (
    <div className="stack">
      <div className="metric-grid tight">
        <Metric label="公开模型" value={String(activeModels)} detail={`${rows.length} 个模型总数`} />
        <Metric label="Chat" value={String(rows.filter((row) => row.endpoint_capabilities?.includes("chat")).length)} detail="聊天端点" />
        <Metric label="Responses" value={String(rows.filter((row) => row.endpoint_capabilities?.includes("responses")).length)} detail="Responses 端点" />
        <Metric label="图片/音频" value={String(rows.filter((row) => row.endpoint_capabilities?.some((item) => ["images", "audio"].includes(item))).length)} detail="多模态能力" />
      </div>
      <section className="panel">
        <SectionHeader title="可用模型" />
        {rows.length === 0
          ? <EmptyState title="暂未发布模型" detail="管理员需要先接入供应商、通道和账号，再同步并发布模型。" />
          : <DataTable rows={rows as any[]} columns={["model_name", "display_name", "vendor", "description", "tags", "endpoint_capabilities", "supported_endpoint_types", "effective_pricing", "active_channel_count", "active_account_count", "health", "status"]} />}
      </section>
    </div>
  );
}

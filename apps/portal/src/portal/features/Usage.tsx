import { useEffect, useState } from "react";
import { api } from "../../api";
import { DataTable } from "../../components/DataTable";

export function UsageView() {
  const [rows, setRows] = useState<any[]>([]);
  const [model, setModel] = useState("");
  const [status, setStatus] = useState("");
  const [apiKeyID, setAPIKeyID] = useState("");
  const [dateFrom, setDateFrom] = useState("");
  const [dateTo, setDateTo] = useState("");

  async function load() {
    const query = new URLSearchParams({ limit: "100" });
    if (model) query.set("model", model);
    if (status) query.set("status", status);
    if (apiKeyID) query.set("api_key_id", apiKeyID);
    if (dateFrom) query.set("date_from", dateFrom);
    if (dateTo) query.set("date_to", dateTo);
    setRows(await api.request<any[]>(`/api/portal/v1/usage?${query.toString()}`));
  }

  useEffect(() => {
    void load();
  }, []);

  return <section className="panel"><h2>用量</h2><div className="row filters"><input value={apiKeyID} onChange={(event) => setAPIKeyID(event.target.value)} placeholder="API 密钥 ID" /><input value={model} onChange={(event) => setModel(event.target.value)} placeholder="模型" /><input value={dateFrom} onChange={(event) => setDateFrom(event.target.value)} placeholder="开始日期 YYYY-MM-DD" /><input value={dateTo} onChange={(event) => setDateTo(event.target.value)} placeholder="结束日期 YYYY-MM-DD" /><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="">任意状态</option><option value="success">成功</option><option value="failed">失败</option><option value="rejected">已拒绝</option></select><button onClick={load}>筛选</button></div><DataTable rows={rows} columns={["request_id", "requested_model", "upstream_model", "endpoint", "input_tokens", "output_tokens", "image_count", "request_count", "actual_cost", "upstream_status", "duration_ms", "status", "created_at"]} /></section>;
}

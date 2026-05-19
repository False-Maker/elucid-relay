import { useEffect, useState } from "react";
import { publicRequest } from "../../api";
import { DataTable } from "../../components/DataTable";

export function PublicInfoView() {
  const [pricing, setPricing] = useState<any[]>([]);
  const [status, setStatus] = useState<any[]>([]);
  const [rankings, setRankings] = useState<any[]>([]);
  const [announcements, setAnnouncements] = useState<any[]>([]);
  const [error, setError] = useState("");

  useEffect(() => {
    void Promise.all([
      publicRequest<any[]>("/api/public/v1/pricing"),
      publicRequest<any[]>("/api/public/v1/channel-status"),
      publicRequest<any[]>("/api/public/v1/rankings"),
      publicRequest<any[]>("/api/public/v1/announcements?audience=portal"),
    ]).then(([nextPricing, nextStatus, nextRankings, nextAnnouncements]) => {
      setPricing(nextPricing);
      setStatus(nextStatus);
      setRankings(nextRankings);
      setAnnouncements(nextAnnouncements);
    }).catch((err) => setError(err instanceof Error ? err.message : "请求失败。"));
  }, []);

  return (
    <div className="stack">
      {error && <section className="panel"><div className="error">{error}</div></section>}
      <section className="public-hero">
        <div className="public-hero-copy">
          <div className="crumb">Public / Elucid Relay</div>
          <h2>一个面向开发者的 <span>AI API Relay</span></h2>
          <p>公开页仍在同一个 Portal 应用内，未登录用户可以查看模型价格、渠道状态、排行榜、公告和 API 接入信息。</p>
        </div>
        <div className="mini-board">
          <div><strong>PRICE</strong><span>公开基础价格，登录后叠加分组倍率。</span></div>
          <div><strong>STATUS</strong><span>展示通道健康、最近同步和错误详情。</span></div>
          <div><strong>DOCS</strong><span>OpenAI-compatible Base URL 与示例请求。</span></div>
        </div>
      </section>
      <section className="panel"><h2>公开模型价格</h2><DataTable rows={pricing} columns={["model_name", "display_name", "vendor", "description", "endpoint_capabilities", "pricing_version", "pricing", "active_channel_count", "providers", "health", "status"]} /></section>
      <section className="panel"><h2>渠道状态</h2><DataTable rows={status} columns={["provider_name", "provider_type", "channel_name", "status", "active_accounts", "total_accounts", "last_test_status", "last_test_latency_ms", "last_tested_at"]} /></section>
      <section className="panel"><h2>排行榜</h2><DataTable rows={rankings} columns={["model", "endpoint", "request_count", "actual_cost"]} /></section>
      <section className="panel"><h2>公告</h2><DataTable rows={announcements} columns={["title", "severity", "body", "created_at"]} /></section>
    </div>
  );
}

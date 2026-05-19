import { Activity, BarChart3, BookOpen, Boxes, CreditCard, Database, FileText, Gauge, KeyRound, Layers, Link, Network, PlayCircle, Route, ShieldAlert, ShieldCheck, Ticket, Users } from "lucide-react";
import type { NavGroup } from "../components/ProductShell";
import type { AdminRole, AdminView } from "./types";

export const adminDetails: Record<AdminView, { title: string; description: string; status?: "operational" | "degraded" }> = {
  overview: { title: "概览", description: "运行健康、用量规模和账号池状态。", status: "degraded" },
  ops: { title: "运维", description: "内置吞吐、错误、账号池和事件监控。", status: "degraded" },
  users: { title: "用户", description: "搜索用户、查看钱包、API 密钥和账号状态。" },
  redeem: { title: "兑换码", description: "批量生成、过期、领取和状态控制。" },
  models: { title: "模型", description: "模型目录、别名、能力和价格字段。" },
  pool: { title: "号池", description: "共享池、自带账号、配额窗口和路由可用性。", status: "degraded" },
  upstream: { title: "上游接入", description: "维护供应商、通道、认证客户端、模型同步和通道检测。", status: "degraded" },
  proxies: { title: "代理池", description: "统一配置上游代理，通道和账号只选择使用。", status: "degraded" },
  oauth: { title: "OAuth", description: "认证状态、任务、进度和重新授权控制。" },
  billing: { title: "商业化", description: "订单、订阅计划、退款、推广返利和财务摘要。" },
  controls: { title: "系统控制", description: "消费限制、注册邮件、通知通道、事件和签名密钥。" },
  usage: { title: "用量", description: "跨租户请求日志、延迟、成本和上游状态。" },
  content: { title: "内容", description: "公告、自定义页面、FAQ、API 信息和法律页面。" },
  groups: { title: "分组", description: "用户分组、模型权限、倍率和额度限制。" },
  risk: { title: "风控", description: "敏感词、目标限制、请求限制和异常规则。" },
  public: { title: "公共页", description: "公开价格、渠道状态、排行榜和内容预览。" },
  audit: { title: "审计", description: "操作员和系统行为，关联请求追踪。" },
  my_dashboard: { title: "我的总览", description: "管理员自己的余额、API Key 和近期请求。" },
  my_keys: { title: "我的 API 密钥", description: "管理员自己调用中转使用的访问凭证。" },
  my_billing: { title: "我的计费钱包", description: "管理员自己的钱包、兑换码、订单和订阅。" },
  my_usage: { title: "我的用量", description: "管理员自己的请求日志、成本和状态。" },
  my_models: { title: "我的模型", description: "管理员可调用的模型目录、能力和价格。" },
  my_oauth: { title: "我的账号接入", description: "管理员自己的 BYO 上游账号和授权任务。" },
  my_playground: { title: "我的 Playground", description: "使用管理员自己的 API Key 测试网关调用。" },
  my_docs: { title: "我的文档", description: "管理员作为调用方使用的接入文档。" },
  my_security: { title: "我的个人设置", description: "管理员自己的邮箱、安全状态和消费限制。" },
};

export const operatorViews = new Set<AdminView>(["overview", "ops", "users", "models", "pool", "upstream", "proxies", "oauth", "usage", "audit"]);
export const selfAccountViews = new Set<AdminView>(["my_dashboard", "my_keys", "my_billing", "my_usage", "my_models", "my_oauth", "my_playground", "my_docs", "my_security"]);

export function adminNavGroups(role: AdminRole): NavGroup<AdminView>[] {
  const adminGroups: NavGroup<AdminView>[] = [
    {
      label: "运营",
      items: [
        { id: "overview", icon: Gauge, label: "概览" },
        { id: "ops", icon: BarChart3, label: "运维" },
        { id: "usage", icon: Activity, label: "用量" },
        { id: "billing", icon: CreditCard, label: "商业化" },
      ],
    },
    {
      label: "用户",
      items: [
        { id: "users", icon: Users, label: "用户" },
        { id: "redeem", icon: Ticket, label: "兑换码" },
        { id: "groups", icon: Layers, label: "分组" },
      ],
    },
    {
      label: "模型与上游",
      items: [
        { id: "models", icon: Database, label: "模型" },
        { id: "upstream", icon: Boxes, label: "上游接入" },
        { id: "pool", icon: Route, label: "号池" },
        { id: "proxies", icon: Network, label: "代理池" },
        { id: "oauth", icon: Link, label: "OAuth" },
      ],
    },
    {
      label: "安全与系统",
      items: [
        { id: "risk", icon: ShieldAlert, label: "风控" },
        { id: "controls", icon: ShieldCheck, label: "控制" },
        { id: "content", icon: FileText, label: "内容" },
        { id: "audit", icon: KeyRound, label: "审计" },
      ],
    },
  ];
  const selfGroup: NavGroup<AdminView> = {
    label: "我的账户",
    items: [
      { id: "my_dashboard", icon: Gauge, label: "我的总览" },
      { id: "my_keys", icon: KeyRound, label: "API 密钥" },
      { id: "my_billing", icon: CreditCard, label: "计费钱包" },
      { id: "my_usage", icon: Activity, label: "用量日志" },
      { id: "my_models", icon: Database, label: "模型与价格" },
      { id: "my_oauth", icon: Link, label: "账号接入" },
      { id: "my_playground", icon: PlayCircle, label: "Playground" },
      { id: "my_docs", icon: BookOpen, label: "文档" },
      { id: "my_security", icon: ShieldCheck, label: "个人设置" },
    ],
  };

  if (role === "platform_owner") return [...adminGroups, selfGroup];
  return [
    ...adminGroups
    .map((group) => ({ ...group, items: group.items.filter((item) => operatorViews.has(item.id)) }))
    .filter((group) => group.items.length > 0),
    selfGroup,
  ];
}

export function adminRoleLabel(role: AdminRole) {
  return role === "platform_owner" ? "平台所有者" : "操作员";
}

export function adminSelfView(view: string): AdminView {
  const mapping: Record<string, AdminView> = {
    billing: "my_billing",
    keys: "my_keys",
    playground: "my_playground",
    usage: "my_usage",
    models: "my_models",
    security: "my_security",
    docs: "my_docs",
  };
  return mapping[view] ?? "my_dashboard";
}

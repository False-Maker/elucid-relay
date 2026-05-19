#!/usr/bin/env node
import { chromium } from "playwright";

const portalURL = env("PORTAL_URL", "http://localhost:18081");
const apiURL = env("BASE_URL", "http://localhost:18080");
const adminEmail = process.env.SMOKE_ADMIN_EMAIL || "";
const adminPassword = process.env.SMOKE_ADMIN_PASSWORD || "";

const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
const consoleErrors = [];
const failedRequests = [];

page.on("console", (message) => {
  if (message.type() === "error") consoleErrors.push(message.text());
});
page.on("requestfailed", (request) => {
  failedRequests.push(`${request.method()} ${request.url()} ${request.failure()?.errorText || ""}`);
});

try {
  await page.goto(portalURL, { waitUntil: "networkidle", timeout: 30000 });
  await expectVisibleText(page, "Elucid Relay");
  const bodyText = await page.locator("body").innerText({ timeout: 10000 });
  if (bodyText.trim().length < 20) throw new Error("portal rendered an unexpectedly small body");

  const setupStatus = await fetchJSON(`${apiURL.replace(/\/$/, "")}/api/setup/status`);
  if (setupStatus.data?.initialized === true && adminEmail && adminPassword) {
    await page.getByPlaceholder("邮箱").fill(adminEmail);
    await page.getByPlaceholder("密码").fill(adminPassword);
    await page.getByRole("button", { name: "登录" }).click();
    await page.waitForLoadState("networkidle");
    await expectVisibleText(page, "管理后台");
    await expectVisibleText(page, "概览");
    await expectVisibleText(page, "商业化");
    await expectVisibleText(page, "供应商");
    await expectVisibleText(page, "设置");
  } else if (setupStatus.data?.initialized === true) {
    await expectVisibleText(page, "登录");
  } else {
    await expectVisibleText(page, "初始化 Elucid Relay");
  }

  if (consoleErrors.length > 0) {
    throw new Error(`browser console errors:\n${consoleErrors.join("\n")}`);
  }
  if (failedRequests.length > 0) {
    throw new Error(`browser request failures:\n${failedRequests.join("\n")}`);
  }
  console.log("browser smoke ok");
} finally {
  await browser.close();
}

async function expectVisibleText(page, text) {
  await page.getByText(text, { exact: false }).first().waitFor({ state: "visible", timeout: 10000 });
}

async function fetchJSON(url) {
  const response = await fetch(url);
  const text = await response.text();
  if (!response.ok) throw new Error(`${url} returned ${response.status}: ${text}`);
  return text ? JSON.parse(text) : {};
}

function env(name, fallback) {
  return process.env[name] || fallback;
}

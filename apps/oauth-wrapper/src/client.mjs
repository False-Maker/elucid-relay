export class GatewayClient {
  constructor({ baseUrl, token, fetchImpl = globalThis.fetch }) {
    this.baseUrl = String(baseUrl || "").replace(/\/+$/, "");
    this.token = String(token || "");
    this.fetchImpl = fetchImpl;
    if (!this.baseUrl) throw new Error("GATEWAY_BASE_URL or BASE_URL is required.");
    if (!this.token) throw new Error("OAUTH_WRAPPER_BEARER_TOKEN is required.");
  }

  async request(method, path, body) {
    const response = await this.fetchImpl(`${this.baseUrl}${path}`, {
      method,
      headers: {
        authorization: `Bearer ${this.token}`,
        "content-type": "application/json",
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await response.text();
    const data = text ? JSON.parse(text) : {};
    if (!response.ok || data.error) {
      const message = data.error?.message || `HTTP ${response.status}`;
      const error = new Error(message);
      error.status = response.status;
      error.response = data;
      throw error;
    }
    return data.data;
  }

  claim(body) {
    return this.request("POST", "/api/oauth-wrapper/v1/jobs/claim", body);
  }

  complete(jobId, body) {
    return this.request("POST", `/api/oauth-wrapper/v1/jobs/${encodeURIComponent(jobId)}/complete`, body);
  }

  progress(jobId, body) {
    return this.request("POST", `/api/oauth-wrapper/v1/jobs/${encodeURIComponent(jobId)}/progress`, body);
  }

  input(jobId, leaseOwner) {
    const query = new URLSearchParams({ lease_owner: String(leaseOwner || "") });
    return this.request("GET", `/api/oauth-wrapper/v1/jobs/${encodeURIComponent(jobId)}/input?${query.toString()}`);
  }

  fail(jobId, body) {
    return this.request("POST", `/api/oauth-wrapper/v1/jobs/${encodeURIComponent(jobId)}/fail`, body);
  }
}

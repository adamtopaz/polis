export interface Agent {
  id: string;
  charter: string;
  runtime: string[];
  state: "active" | "paused" | "terminated";
  phase: string;
}

export interface Message {
  id: number;
  agent_id: string;
  sender: string;
  body: unknown;
  created_at: string;
}

export interface Event {
  id: number;
  agent_id?: string;
  actor: string;
  kind: string;
  data?: unknown;
  created_at: string;
}

interface MessagesResponse {
  items: Message[];
}

export class PolisApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "PolisApiError";
  }
}

export class PolisClient {
  constructor(
    private readonly baseUrl: string,
    private readonly token: string,
    private readonly fetcher: typeof fetch = globalThis.fetch,
  ) {}

  self(signal?: AbortSignal): Promise<Agent> {
    return this.request<Agent>("GET", "/v1/self", undefined, signal);
  }

  async messages(waitSeconds = 0, signal?: AbortSignal): Promise<Message[]> {
    const query = waitSeconds === 0 ? "" : `?wait_seconds=${waitSeconds}`;
    const response = await this.request<MessagesResponse>("GET", `/v1/self/messages${query}`, undefined, signal);
    return response.items;
  }

  async acknowledge(through: number, signal?: AbortSignal): Promise<void> {
    await this.request("POST", "/v1/self/messages/ack", { through }, signal);
  }

  journal(kind: string, data: unknown, signal?: AbortSignal): Promise<Event> {
    return this.request<Event>("POST", "/v1/self/journal", { kind, data }, signal);
  }

  private async request<T = void>(
    method: string,
    pathname: string,
    body?: unknown,
    signal?: AbortSignal,
  ): Promise<T> {
    const headers = new Headers({ Authorization: `Bearer ${this.token}` });
    if (body !== undefined) {
      headers.set("Content-Type", "application/json");
    }
    const response = await this.fetcher(`${this.baseUrl}${pathname}`, {
      method,
      headers,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      ...(signal === undefined ? {} : { signal }),
    });
    if (!response.ok) {
      let message = `Polis returned ${response.status} ${response.statusText}`.trim();
      try {
        const payload = (await response.json()) as { error?: unknown };
        if (typeof payload.error === "string" && payload.error !== "") {
          message = payload.error;
        }
      } catch {
        // Preserve the status-based error when Polis returned no JSON body.
      }
      throw new PolisApiError(response.status, message);
    }
    if (response.status === 204) {
      return undefined as T;
    }
    return (await response.json()) as T;
  }
}

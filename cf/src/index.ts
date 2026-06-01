import { ZhihuBlocker } from "./do";

export { ZhihuBlocker };

// 显式 RPC 接口: 绕开 DurableObject<Env> 与 DurableObjectNamespace<ZhihuBlocker>
// 互相引用造成的循环泛型展开 (TypeScript TS2589 "excessively deep")。
interface BlockerRpc {
  setCurl(curl: string): Promise<any>;
  getSession(): Promise<any>;
  listMyAnswers(): Promise<any>;
  getStatus(): Promise<any>;
  getCandidates(): Promise<any>;
  clearList(): Promise<any>;
  runAnalyze(params: any): Promise<any>;
  runBlock(tokens: string[], execute: boolean): Promise<any>;
  runUnblock(execute: boolean): Promise<any>;
}

async function readBody(request: Request): Promise<any> {
  if (request.method !== "POST") return {};
  try {
    return await request.json();
  } catch {
    return {};
  }
}

function toStrArr(v: unknown): string[] {
  if (!Array.isArray(v)) return [];
  return v.filter((x): x is string => typeof x === "string" && x.length > 0);
}

async function route(path: string, body: any, stub: BlockerRpc): Promise<{ obj: unknown; status: number }> {
  switch (path) {
    case "/api/session":
      return { obj: await stub.getSession(), status: 200 };
    case "/api/my_answers":
      return { obj: await stub.listMyAnswers(), status: 200 };
    case "/api/candidates":
      return { obj: { candidates: await stub.getCandidates() }, status: 200 };
    case "/api/status":
      return { obj: await stub.getStatus(), status: 200 };
    case "/api/curl":
      return { obj: await stub.setCurl(String(body.curl || "")), status: 200 };
    case "/api/run":
      if (body.mode !== "comment" && !String(body.opinion || "").trim()) {
        return { obj: { ok: false, msg: "请填写你的观点" }, status: 400 };
      }
      return { obj: await stub.runAnalyze(body), status: 200 };
    case "/api/block":
      return { obj: await stub.runBlock(toStrArr(body.tokens), !!body.execute), status: 200 };
    case "/api/unblock":
      return { obj: await stub.runUnblock(!!body.execute), status: 200 };
    case "/api/clear":
      return { obj: await stub.clearList(), status: 200 };
    default:
      return { obj: { error: "not found" }, status: 404 };
  }
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);

    // 非 /api/* 一律交给静态资源 (前端页面 / favicon)
    if (!url.pathname.startsWith("/api/")) {
      return env.ASSETS.fetch(request);
    }

    // 每个浏览器会话一个独立 DO (隔离各自的登录态/名单), 用 zsess cookie 区分
    const m = (request.headers.get("cookie") || "").match(/(?:^|;\s*)zsess=([^;]+)/);
    let sess = m?.[1];
    let setCookie = "";
    if (!sess) {
      sess = crypto.randomUUID();
      setCookie = `zsess=${sess}; Path=/; HttpOnly; SameSite=Lax; Max-Age=31536000`;
    }
    const stub = env.BLOCKER.getByName(sess) as unknown as BlockerRpc;

    let obj: unknown;
    let status: number;
    try {
      const body = await readBody(request);
      ({ obj, status } = await route(url.pathname, body, stub));
    } catch (e: any) {
      obj = { ok: false, error: String(e?.message || e) };
      status = 500;
    }

    const resp = new Response(JSON.stringify(obj), {
      status,
      headers: { "content-type": "application/json; charset=utf-8" },
    });
    if (setCookie) resp.headers.append("set-cookie", setCookie);
    return resp;
  },
} satisfies ExportedHandler<Env>;

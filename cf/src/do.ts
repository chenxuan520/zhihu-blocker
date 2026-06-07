import { DurableObject } from "cloudflare:workers";
import {
  UA_DEFAULT,
  parseCurl,
  parseQID,
  parseAnswerID,
  xsrfFromCookie,
  checkSession,
  meToken,
  myAnswers,
  crawlAnswers,
  crawlComments,
  isBlocking,
  blockUser,
  unblockUser,
  type AnswerItem,
  type CommentItem,
} from "./zhihu";
import { classify, judgeComment, openaiChat, type ChatFn } from "./llm";

// CF 版单次判定硬上限 (贴合 Workers 限制; 全量请用本地 Go 版)
const JUDGE_CAP = 150;
const BLOCK_BATCH_CAP = 50;
const MAX_CHARS = 1800;
const CF_AI_DEFAULT_MODEL = "@cf/meta/llama-3.1-8b-instruct";

interface Job {
  state: "idle" | "running" | "done" | "error";
  kind: string;
  log: string;
  error: string;
}
interface Candidate {
  url_token: string;
  name: string;
  stance: string;
  confidence: string;
  voteup: number;
  reason: string;
  answer_url: string;
  excerpt: string;
}

const num = (v: any, def: number): number => {
  const n = Number(v);
  return Number.isFinite(n) ? n : def;
};
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function mapPool<T, R>(items: T[], n: number, fn: (t: T, i: number) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(items.length);
  let idx = 0;
  const workers = Array.from({ length: Math.max(1, n) }, async () => {
    while (true) {
      const i = idx++;
      if (i >= items.length) break;
      out[i] = await fn(items[i], i);
    }
  });
  await Promise.all(workers);
  return out;
}

export class ZhihuBlocker extends DurableObject<Env> {
  // ---------- 存储辅助 ----------
  private async getJob(): Promise<Job> {
    return (await this.ctx.storage.get<Job>("job")) || { state: "idle", kind: "", log: "", error: "" };
  }
  private async putJob(j: Job): Promise<void> {
    await this.ctx.storage.put("job", j);
  }
  private async log(line: string): Promise<void> {
    const j = await this.getJob();
    j.log = (j.log + line + "\n").slice(-6000);
    await this.putJob(j);
  }
  private async cookieUA(): Promise<{ cookie: string; ua: string }> {
    const cookie = (await this.ctx.storage.get<string>("cookie")) || "";
    const ua = (await this.ctx.storage.get<string>("ua")) || UA_DEFAULT;
    return { cookie, ua };
  }

  // 按引擎构建 ChatFn: cfai=Cloudflare Workers AI(免费), custom=用户自配 OpenAI 兼容
  private buildChat(engine: string, params: any): ChatFn {
    if (engine === "cfai") {
      const model = params.model || CF_AI_DEFAULT_MODEL;
      const ai = this.env.AI;
      return async (system, user) => {
        const r: any = await ai.run(model, {
          messages: [
            { role: "system", content: system },
            { role: "user", content: user },
          ],
        });
        return typeof r?.response === "string" ? r.response : JSON.stringify(r);
      };
    }
    const baseUrl = params.base_url || this.env.LLM_BASE_URL || "https://api.openai.com/v1";
    const model = params.model || this.env.LLM_MODEL || "gpt-4o-mini";
    const apiKey = String(params.api_key || this.env.LLM_API_KEY || "").trim();
    return openaiChat(baseUrl, model, apiKey);
  }

  // ---------- RPC: 登录态 ----------
  async setCurl(curl: string): Promise<any> {
    const { cookie, ua } = parseCurl(curl || "");
    if (!cookie) return { ok: false, msg: "没从 curl 里解析到 cookie" };
    await this.ctx.storage.put("cookie", cookie);
    if (ua) await this.ctx.storage.put("ua", ua);
    const has: Record<string, boolean> = {};
    for (const k of ["z_c0", "d_c0", "_xsrf", "__zse_ck"]) has[k] = cookie.includes(k + "=");
    return { ok: true, cookie_len: cookie.length, ua, has };
  }
  async getSession(): Promise<any> {
    const { cookie, ua } = await this.cookieUA();
    return await checkSession(cookie, ua);
  }
  async listMyAnswers(): Promise<any> {
    const { cookie, ua } = await this.cookieUA();
    try {
      return { ok: true, answers: await myAnswers(cookie, ua, 30) };
    } catch (e: any) {
      return { ok: false, msg: String(e?.message || e), answers: [] };
    }
  }

  // ---------- RPC: 名单 ----------
  async getStatus(): Promise<Job> {
    return await this.getJob();
  }
  async getCandidates(): Promise<Candidate[]> {
    return (await this.ctx.storage.get<Candidate[]>("candidates")) || [];
  }
  async clearList(): Promise<any> {
    await this.ctx.storage.delete("candidates");
    return { ok: true };
  }

  // ---------- RPC: 启动后台任务 ----------
  async runAnalyze(params: any): Promise<any> {
    const j = await this.getJob();
    if (j.state === "running") return { ok: false, msg: "已有任务在运行, 请稍候" };
    await this.putJob({ state: "running", kind: "analyze", log: "", error: "" });
    this.ctx.waitUntil(this.guard(() => this.processAnalyze(params)));
    return { ok: true };
  }
  async runBlock(tokens: string[], execute: boolean): Promise<any> {
    if (!tokens?.length) return { ok: false, msg: "未勾选任何人" };
    const j = await this.getJob();
    if (j.state === "running") return { ok: false, msg: "已有任务在运行, 请稍候" };
    await this.putJob({ state: "running", kind: "block", log: "", error: "" });
    this.ctx.waitUntil(this.guard(() => this.processBlock(tokens, execute)));
    return { ok: true };
  }
  async runUnblock(execute: boolean): Promise<any> {
    const j = await this.getJob();
    if (j.state === "running") return { ok: false, msg: "已有任务在运行, 请稍候" };
    await this.putJob({ state: "running", kind: "unblock", log: "", error: "" });
    this.ctx.waitUntil(this.guard(() => this.processUnblock(execute)));
    return { ok: true };
  }

  private async guard(fn: () => Promise<void>): Promise<void> {
    try {
      await fn();
      const j = await this.getJob();
      j.state = "done";
      await this.putJob(j);
    } catch (e: any) {
      const j = await this.getJob();
      j.state = "error";
      j.error = String(e?.message || e);
      j.log += `\n[error] ${j.error}`;
      await this.putJob(j);
    }
  }

  // ---------- 分析流水线 ----------
  private async processAnalyze(params: any): Promise<void> {
    const { cookie, ua } = await this.cookieUA();
    if (!cookie) throw new Error("未设置登录态, 请先在页面粘贴 curl");
    const engine = String(params.engine || "cfai");
    const threshold = num(params.threshold, 0.8);
    const concurrency = Math.min(8, Math.max(1, num(params.concurrency, 6)));
    const limit = num(params.limit, 0);
    const useLLM = engine === "cfai" || engine === "custom";
    if (engine === "custom" && !String(params.api_key || this.env.LLM_API_KEY || "").trim()) {
      throw new Error("自定义 LLM 需要填写 API Key");
    }
    const chat: ChatFn | null = useLLM ? this.buildChat(engine, params) : null;

    if ((params.mode || "answer") === "comment") {
      await this.commentPipeline(params, cookie, ua, engine, chat, threshold, concurrency, limit);
    } else {
      await this.answerPipeline(params, cookie, ua, engine, chat, threshold, concurrency, limit);
    }
  }

  private async answerPipeline(
    params: any, cookie: string, ua: string, engine: string, chat: ChatFn | null,
    threshold: number, concurrency: number, limit: number,
  ): Promise<void> {
    const qid = parseQID(params.question || "");
    await this.log(`[crawl] 开始抓取问题 ${qid} 的回答…`);
    let items: AnswerItem[] = await crawlAnswers(qid, cookie, ua, (got, totals, isEnd) => {
      void this.log(`[crawl] 累计 ${got}/${totals} is_end=${isEnd}`);
    }, limit > 0 ? Math.min(limit, JUDGE_CAP) : 0);
    items.sort((a, b) => b.voteup - a.voteup);
    const before = items.length;
    if (limit > 0 && limit < items.length) items = items.slice(0, limit);
    if (items.length > JUDGE_CAP) {
      items = items.slice(0, JUDGE_CAP);
      await this.log(`[stance] 注意: CF 版单次最多判定 ${JUDGE_CAP} 条 (共 ${before} 条), 已截断; 全量请用本地版`);
    }

    let done = 0;
    const useLLM = engine === "cfai" || engine === "custom";
    const results = await mapPool(items, useLLM ? concurrency : 8, async (a) => {
      let stance = "unclear", conf = 0, reason = "";
      try {
        if (engine === "mock") {
          const h = [...a.answerId].reduce((s, c) => s + c.charCodeAt(0), 0) % 3;
          stance = h === 0 ? "oppose" : h === 1 ? "support" : "neutral";
          conf = 0.85; reason = "[mock]";
        } else if (!a.text) {
          stance = "irrelevant"; conf = 1; reason = "空回答";
        } else {
          const r = await classify(chat!, params.opinion || "", a.text, MAX_CHARS);
          stance = r.stance; conf = r.confidence; reason = r.reason;
        }
      } catch {
        stance = "unclear"; reason = "err";
      }
      done++;
      if (done % 10 === 0 || done === items.length) await this.log(`[stance] ${done}/${items.length}`);
      return { a, stance, conf, reason };
    });

    const byAuthor = new Map<string, Candidate>();
    for (const { a, stance, conf, reason } of results) {
      if (stance !== "oppose" || conf < threshold) continue;
      if (a.author.isAnonymous || !a.author.urlToken) continue;
      const prev = byAuthor.get(a.author.urlToken);
      if (!prev || a.voteup > prev.voteup) {
        byAuthor.set(a.author.urlToken, {
          url_token: a.author.urlToken, name: a.author.name, stance, confidence: conf.toFixed(2),
          voteup: a.voteup, reason, answer_url: a.answerUrl, excerpt: a.text.replace(/\n/g, " ").slice(0, 70),
        });
      }
    }
    await this.saveCandidates([...byAuthor.values()]);
    await this.log(`[review] 候选拉黑 ${byAuthor.size} 人`);
  }

  private async commentPipeline(
    params: any, cookie: string, ua: string, engine: string, chat: ChatFn | null,
    threshold: number, concurrency: number, limit: number,
  ): Promise<void> {
    const src = String(params.answer || "").trim();
    let ids: string[] = [];
    if (!src || src === "my" || src === "mine") {
      const mas = await myAnswers(cookie, ua, 20);
      ids = mas.filter((a) => a.comment_count > 0).map((a) => a.id);
      await this.log(`[comments] 我的回答中有评论的 ${ids.length} 条`);
    } else {
      ids = [parseAnswerID(src)];
    }
    const meTok = await meToken(cookie, ua);
    const fetchReplies = !!params.replies;

    let all: CommentItem[] = [];
    for (const id of ids) {
      try {
        const cs = await crawlComments(id, cookie, ua, meTok, fetchReplies, (got, isEnd) => {
          void this.log(`[comments] 答案 ${id} 累计 ${got} is_end=${isEnd}`);
        });
        all = all.concat(cs);
      } catch (e: any) {
        await this.log(`[comments] 答案 ${id} 抓取出错: ${String(e?.message || e)}`);
      }
      if (all.length > 2000) break;
    }
    all.sort((a, b) => b.like - a.like);
    const before = all.length;
    if (limit > 0 && limit < all.length) all = all.slice(0, limit);
    if (all.length > JUDGE_CAP) {
      all = all.slice(0, JUDGE_CAP);
      await this.log(`[judge] 注意: CF 版单次最多判定 ${JUDGE_CAP} 条 (共 ${before} 条), 已截断`);
    }
    await this.log(`[comments] 共收集 ${before} 条非本人评论, 判定 ${all.length} 条`);

    let done = 0;
    const useLLM = engine === "cfai" || engine === "custom";
    const results = await mapPool(all, useLLM ? concurrency : 8, async (c) => {
      let flag = false, conf = 1, reason = "(人工勾选)";
      try {
        if (engine === "all") {
          flag = true;
        } else if (engine === "mock") {
          flag = [...c.commentId].reduce((s, ch) => s + ch.charCodeAt(0), 0) % 3 === 0;
          conf = 0.85; reason = "[mock]";
        } else {
          const r = await judgeComment(chat!, params.criterion || "", c.text, MAX_CHARS);
          flag = r.flag && r.confidence >= threshold; conf = r.confidence; reason = r.reason;
        }
      } catch {
        flag = false; reason = "err";
      }
      done++;
      if (done % 10 === 0 || done === all.length) await this.log(`[judge] ${done}/${all.length}`);
      return { c, flag, conf, reason };
    });

    const byAuthor = new Map<string, Candidate>();
    for (const { c, flag, conf, reason } of results) {
      if (!flag || !c.author.urlToken) continue;
      const prev = byAuthor.get(c.author.urlToken);
      if (!prev || c.like > prev.voteup) {
        byAuthor.set(c.author.urlToken, {
          url_token: c.author.urlToken, name: c.author.name, stance: c.isReply ? "回复" : "评论",
          confidence: conf.toFixed(2), voteup: c.like, reason, answer_url: c.answerUrl,
          excerpt: c.text.replace(/\n/g, " ").slice(0, 70),
        });
      }
    }
    await this.saveCandidates([...byAuthor.values()]);
    await this.log(`[review] 评论候选拉黑 ${byAuthor.size} 人`);
  }

  private async saveCandidates(list: Candidate[]): Promise<void> {
    list.sort((a, b) => (Number(b.confidence) - Number(a.confidence)) || b.voteup - a.voteup);
    await this.ctx.storage.put("candidates", list.slice(0, 500));
  }

  // ---------- 拉黑 / 恢复 ----------
  private async processBlock(tokens: string[], execute: boolean): Promise<void> {
    const { cookie, ua } = await this.cookieUA();
    if (!cookie) throw new Error("未设置登录态");
    const xsrf = xsrfFromCookie(cookie);
    const cands = await this.getCandidates();
    const nameOf = new Map(cands.map((c) => [c.url_token, c.name]));
    await this.log(`[block] 模式=${execute ? "EXECUTE(真拉黑)" : "DRY-RUN(预览)"} | 目标 ${tokens.length} 人 | 上限 ${BLOCK_BATCH_CAP}`);

    let blocked = 0, skipped = 0, failed = 0, would = 0, consec = 0;
    const unblockList = (await this.ctx.storage.get<any[]>("unblock")) || [];
    for (const token of tokens) {
      if (blocked >= BLOCK_BATCH_CAP) {
        await this.log(`[block] 已达单次上限 ${BLOCK_BATCH_CAP}, 停止`);
        break;
      }
      const tag = `${nameOf.get(token) || ""}(@${token})`;
      if (!execute) {
        await this.log(`[would] ${tag}`);
        would++;
        continue;
      }
      try {
        if (await isBlocking(token, cookie, ua)) {
          await this.log(`[skip] 已拉黑: ${tag}`);
          skipped++;
          continue;
        }
        const { status } = await blockUser(token, cookie, ua, xsrf);
        if (status === 200 || status === 201 || status === 204) {
          await this.log(`[ok] 已拉黑: ${tag}`);
          if (!unblockList.find((e) => e.url_token === token)) unblockList.push({ url_token: token, name: nameOf.get(token) || "" });
          await this.ctx.storage.put("unblock", unblockList);
          blocked++;
          consec = 0;
        } else {
          await this.log(`[fail] ${tag} -> HTTP ${status}`);
          failed++;
          consec++;
        }
      } catch (e: any) {
        await this.log(`[fail] ${tag} -> ${String(e?.message || e)}`);
        failed++;
        consec++;
      }
      if (consec >= 3) {
        await this.log("[block] 连续失败 3 次 (疑似风控), 自动停止");
        break;
      }
      await sleep(2000 + Math.random() * 3000);
    }
    await this.log(`[block] 结束: blocked=${blocked} skipped=${skipped} failed=${failed} would=${would}`);
  }

  private async processUnblock(execute: boolean): Promise<void> {
    const { cookie, ua } = await this.cookieUA();
    if (!cookie) throw new Error("未设置登录态");
    const xsrf = xsrfFromCookie(cookie);
    const list = (await this.ctx.storage.get<any[]>("unblock")) || [];
    if (!list.length) {
      await this.log("[unblock] 恢复名单为空");
      return;
    }
    let ok = 0, fail = 0, would = 0;
    for (const e of list) {
      if (!execute) {
        await this.log(`[would-unblock] ${e.name}(@${e.url_token})`);
        would++;
        continue;
      }
      const status = await unblockUser(e.url_token, cookie, ua, xsrf);
      if (status === 200 || status === 204) {
        await this.log(`[unblock-ok] ${e.name}(@${e.url_token})`);
        ok++;
      } else {
        await this.log(`[unblock-fail] @${e.url_token} -> HTTP ${status}`);
        fail++;
      }
      await sleep(2000 + Math.random() * 3000);
    }
    if (execute) await this.ctx.storage.put("unblock", []);
    await this.log(`[unblock] 结束: unblocked=${ok} failed=${fail} would=${would}`);
  }
}

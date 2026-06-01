// 知乎 API 封装 (Workers fetch 版, 自动处理 gzip)。逻辑对齐 Go 版。

export const UA_DEFAULT =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36 Edg/147.0.0.0";

const ANSWERS_INCLUDE =
  "data[*].is_normal,is_collapsed,collapse_reason,comment_count,content," +
  "voteup_count,created_time,updated_time,question,excerpt,is_author;" +
  "data[*].author.follower_count,vip_info,badge[*].topics";

export interface Author {
  name: string;
  urlToken: string;
  isAnonymous: boolean;
}
export interface AnswerItem {
  answerId: string;
  answerUrl: string;
  author: Author;
  voteup: number;
  text: string;
}
export interface CommentItem {
  commentId: string;
  answerUrl: string;
  author: Author;
  text: string;
  like: number;
  isReply: boolean;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function decodeEntities(s: string): string {
  return s
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
    .replace(/&nbsp;/g, " ")
    .replace(/&#(\d+);/g, (_, n) => String.fromCodePoint(parseInt(n, 10)))
    .replace(/&#x([0-9a-fA-F]+);/g, (_, n) => String.fromCodePoint(parseInt(n, 16)))
    .replace(/&amp;/g, "&");
}

export function htmlToText(s: string): string {
  if (!s) return "";
  s = s.replace(/<(script|style)[\s\S]*?<\/\1>/gi, "");
  s = s.replace(/<br\s*\/?>/gi, "\n");
  s = s.replace(/<\/(p|div|li|h[1-6])>/gi, "\n");
  s = s.replace(/<[^>]+>/g, "");
  s = decodeEntities(s);
  s = s.replace(/\n{3,}/g, "\n\n");
  return s.trim();
}

export function parseQID(q: string): string {
  const m = q.match(/\/question\/(\d+)/);
  if (m) return m[1];
  const d = q.match(/(\d{6,})/);
  return d ? d[1] : q.trim();
}

export function parseAnswerID(q: string): string {
  const m = q.match(/\/answer\/(\d+)/);
  if (m) return m[1];
  const d = q.match(/(\d{6,})/);
  return d ? d[1] : q.trim();
}

export function xsrfFromCookie(cookie: string): string {
  for (const part of cookie.split(";")) {
    const p = part.trim();
    if (p.startsWith("_xsrf=")) return p.slice("_xsrf=".length);
  }
  return "";
}

async function zGet(
  url: string,
  cookie: string,
  ua: string,
): Promise<{ status: number; data: any }> {
  const r = await fetch(url, {
    headers: {
      "user-agent": ua,
      accept: "*/*",
      "accept-language": "zh-CN,zh;q=0.9,en;q=0.8",
      cookie,
      "x-requested-with": "fetch",
      "x-api-version": "3.0.91",
      referer: "https://www.zhihu.com/",
    },
  });
  const text = await r.text();
  try {
    return { status: r.status, data: JSON.parse(text) };
  } catch {
    return { status: r.status, data: text };
  }
}

export async function checkSession(
  cookie: string,
  ua: string,
): Promise<{ valid: boolean; name?: string; url_token?: string; headline?: string; reason?: string }> {
  if (!cookie) return { valid: false, reason: "尚未设置 cookie" };
  if (!cookie.includes("z_c0=")) return { valid: false, reason: "cookie 缺少 z_c0 登录态" };
  const { status, data } = await zGet("https://www.zhihu.com/api/v4/me", cookie, ua);
  if (status === 200 && data && data.name) {
    return { valid: true, name: data.name, url_token: data.url_token, headline: data.headline };
  }
  return { valid: false, reason: `登录态失效 (HTTP ${status})` };
}

export async function meToken(cookie: string, ua: string): Promise<string> {
  const { status, data } = await zGet("https://www.zhihu.com/api/v4/me", cookie, ua);
  return status === 200 && data ? data.url_token || "" : "";
}

export interface MyAnswer {
  id: string;
  title: string;
  comment_count: number;
}

export async function myAnswers(cookie: string, ua: string, limit: number): Promise<MyAnswer[]> {
  const tok = await meToken(cookie, ua);
  if (!tok) throw new Error("获取当前用户失败 (登录态可能失效)");
  const inc = encodeURIComponent("data[*].comment_count,question.title");
  const url = `https://www.zhihu.com/api/v4/members/${tok}/answers?limit=${limit}&offset=0&include=${inc}`;
  const { status, data } = await zGet(url, cookie, ua);
  if (status !== 200 || !data?.data) throw new Error(`获取我的回答失败 (HTTP ${status})`);
  return data.data.map((a: any) => ({
    id: String(a.id),
    title: a.question?.title || "",
    comment_count: a.comment_count || 0,
  }));
}

export async function crawlAnswers(
  qid: string,
  cookie: string,
  ua: string,
  onProgress: (got: number, totals: number, isEnd: boolean) => void,
  maxPages = 40,
): Promise<AnswerItem[]> {
  let url =
    `https://www.zhihu.com/api/v4/questions/${qid}/answers?include=${encodeURIComponent(ANSWERS_INCLUDE)}` +
    `&limit=20&offset=0&platform=desktop&sort_by=default`;
  const out: AnswerItem[] = [];
  const seen = new Set<string>();
  let page = 0;
  while (url && page < maxPages) {
    page++;
    const { status, data } = await zGet(url, cookie, ua);
    if (status !== 200 || !data?.data) throw new Error(`采集失败 status=${status}`);
    for (const a of data.data) {
      const id = String(a.id);
      if (seen.has(id)) continue;
      seen.add(id);
      const au = a.author || {};
      const token = au.url_token || "";
      const isAnon = !token || au.name === "匿名用户";
      out.push({
        answerId: id,
        answerUrl: `https://www.zhihu.com/question/${qid}/answer/${id}`,
        author: { name: au.name || "", urlToken: token, isAnonymous: isAnon },
        voteup: a.voteup_count || 0,
        text: htmlToText(a.content || ""),
      });
    }
    onProgress(out.length, data.paging?.totals || 0, !!data.paging?.is_end);
    if (data.paging?.is_end || !data.data.length) break;
    url = data.paging?.next;
    await sleep(300 + Math.random() * 300);
  }
  return out;
}

function pushComment(out: CommentItem[], c: any, meTok: string, isReply: boolean) {
  const au = c.author || {};
  const token = au.url_token || "";
  if (c.is_author || !token || au.is_anonymous || token === meTok) return;
  out.push({
    commentId: String(c.id),
    answerUrl: c.url || "",
    author: { name: au.name || "", urlToken: token, isAnonymous: false },
    text: htmlToText(c.content || ""),
    like: c.like_count || 0,
    isReply,
  });
}

export async function crawlComments(
  answerId: string,
  cookie: string,
  ua: string,
  meTok: string,
  fetchReplies: boolean,
  onProgress: (got: number, isEnd: boolean) => void,
  maxPages = 40,
): Promise<CommentItem[]> {
  let url = `https://www.zhihu.com/api/v4/comment_v5/answers/${answerId}/root_comment?order_by=score&limit=20`;
  const out: CommentItem[] = [];
  let page = 0;
  let childBudget = 1500;
  while (url && page < maxPages) {
    page++;
    const { status, data } = await zGet(url, cookie, ua);
    if (status !== 200 || !data?.data) throw new Error(`评论接口 HTTP ${status}`);
    for (const root of data.data) {
      pushComment(out, root, meTok, false);
      const seen = new Set<string>();
      for (const ch of root.child_comments || []) {
        pushComment(out, ch, meTok, true);
        seen.add(String(ch.id));
      }
      if (fetchReplies && (root.child_comment_count || 0) > (root.child_comments?.length || 0) && childBudget > 0) {
        let curl = `https://www.zhihu.com/api/v4/comment_v5/comment/${root.id}/child_comment?order_by=ts&limit=20`;
        while (curl && childBudget > 0) {
          const r2 = await zGet(curl, cookie, ua);
          if (r2.status !== 200 || !r2.data?.data) break;
          for (const ch of r2.data.data) {
            if (seen.has(String(ch.id))) continue;
            seen.add(String(ch.id));
            pushComment(out, ch, meTok, true);
            childBudget--;
          }
          if (r2.data.paging?.is_end || !r2.data.data.length) break;
          curl = r2.data.paging?.next;
          await sleep(300);
        }
      }
    }
    onProgress(out.length, !!data.paging?.is_end);
    if (data.paging?.is_end || !data.data.length) break;
    url = data.paging?.next;
    await sleep(400 + Math.random() * 400);
  }
  return out;
}

export async function isBlocking(token: string, cookie: string, ua: string): Promise<boolean> {
  const { status, data } = await zGet(
    `https://www.zhihu.com/api/v4/members/${token}?include=is_blocking`,
    cookie,
    ua,
  );
  return status === 200 && data ? !!data.is_blocking : false;
}

export async function blockUser(
  token: string,
  cookie: string,
  ua: string,
  xsrf: string,
): Promise<{ status: number; data: any }> {
  const r = await fetch(`https://www.zhihu.com/api/v4/members/${token}/actions/block`, {
    method: "POST",
    headers: {
      "user-agent": ua,
      accept: "*/*",
      cookie,
      "x-xsrftoken": xsrf,
      "x-requested-with": "fetch",
      "content-type": "application/json",
      referer: `https://www.zhihu.com/people/${token}`,
    },
    body: "{}",
  });
  const text = await r.text();
  let data: any = text;
  try {
    data = JSON.parse(text);
  } catch {}
  return { status: r.status, data };
}

export async function unblockUser(
  token: string,
  cookie: string,
  ua: string,
  xsrf: string,
): Promise<number> {
  const r = await fetch(`https://www.zhihu.com/api/v4/members/${token}/actions/block`, {
    method: "DELETE",
    headers: {
      "user-agent": ua,
      accept: "*/*",
      cookie,
      "x-xsrftoken": xsrf,
      "x-requested-with": "fetch",
      referer: `https://www.zhihu.com/people/${token}`,
    },
  });
  return r.status;
}

export function parseCurl(text: string): { cookie: string; ua: string } {
  text = text.trim().replace(/^curl/, "");
  text = text.replace(/\\\s*\n/g, " ");
  // 简易 shell 分词: 处理单/双引号
  const parts: string[] = [];
  let buf = "";
  let q: string | null = null;
  let has = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (q) {
      if (ch === q) q = null;
      else buf += ch;
    } else if (ch === "'" || ch === '"') {
      q = ch;
      has = true;
    } else if (/\s/.test(ch)) {
      if (has) {
        parts.push(buf);
        buf = "";
        has = false;
      }
    } else {
      buf += ch;
      has = true;
    }
  }
  if (has) parts.push(buf);

  let cookie = "";
  let ua = "";
  for (let i = 0; i < parts.length; i++) {
    const p = parts[i];
    if ((p === "-b" || p === "--cookie") && i + 1 < parts.length) {
      cookie = parts[++i];
    } else if ((p === "-H" || p === "--header") && i + 1 < parts.length) {
      const h = parts[++i];
      const low = h.toLowerCase();
      if (low.startsWith("cookie:")) cookie = h.slice(h.indexOf(":") + 1).trim();
      else if (low.startsWith("user-agent:")) ua = h.slice(h.indexOf(":") + 1).trim();
    } else if ((p === "-A" || p === "--user-agent") && i + 1 < parts.length) {
      ua = parts[++i];
    }
  }
  return { cookie, ua };
}

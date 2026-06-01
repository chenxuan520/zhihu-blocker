// LLM 判定: 通过 ChatFn 抽象, 支持 OpenAI 兼容(用户自配) 与 Cloudflare Workers AI 两种后端。

export type ChatFn = (system: string, user: string) => Promise<string>;

const STANCE_SYSTEM =
  "你是一个严谨、中立的文本立场分析器。给定[用户观点]和[一条知乎回答], 判断该回答相对于该观点的立场。" +
  "注意识别反讽、反问、阴阳怪气。只输出一个 JSON 对象, 不要任何多余文字。";

const JUDGE_SYSTEM =
  "你是社区评论审核助手。给定[屏蔽标准]和[一条评论], 判断该评论是否符合屏蔽标准 (即是否应该拉黑发该评论的人)。" +
  "注意识别辱骂、人身攻击、阴阳怪气、引战挑衅、无理抬杠。只输出一个 JSON 对象, 不要任何多余文字。";

// OpenAI 兼容后端 (用户在表单自配 base_url / model / key)
export function openaiChat(baseUrl: string, model: string, apiKey: string): ChatFn {
  return async (system, user) => {
    const r = await fetch(baseUrl.replace(/\/$/, "") + "/chat/completions", {
      method: "POST",
      headers: { "content-type": "application/json", authorization: `Bearer ${apiKey}` },
      body: JSON.stringify({
        model,
        temperature: 0,
        messages: [
          { role: "system", content: system },
          { role: "user", content: user },
        ],
      }),
    });
    if (!r.ok) {
      const t = await r.text();
      throw new Error(`LLM HTTP ${r.status}: ${t.slice(0, 160)}`);
    }
    const data: any = await r.json();
    return data?.choices?.[0]?.message?.content ?? "";
  };
}

function extractJSON(content: string): any {
  let t = content.trim().replace(/^```(?:json)?/i, "").replace(/```$/i, "").trim();
  try {
    return JSON.parse(t);
  } catch {
    const m = t.match(/\{[\s\S]*\}/);
    if (m) {
      try {
        return JSON.parse(m[0]);
      } catch {}
    }
  }
  return null;
}

function clamp01(n: number): number {
  if (!Number.isFinite(n)) return 0;
  return Math.max(0, Math.min(1, n));
}

export interface StanceResult {
  stance: string;
  confidence: number;
  reason: string;
}

export async function classify(
  chat: ChatFn,
  opinion: string,
  answer: string,
  maxChars: number,
): Promise<StanceResult> {
  const txt = (answer || "").slice(0, maxChars);
  const user =
    `[用户观点]\n${opinion}\n\n[一条知乎回答]\n${txt}\n\n` +
    '请输出 JSON: {"stance": "support|oppose|neutral|irrelevant", "confidence": 0到1的小数, "reason": "不超过30字的理由"}\n' +
    "- support: 回答支持/认同该观点\n- oppose: 回答反对/反驳该观点\n- neutral: 中立或两面都谈\n- irrelevant: 与该观点无关";
  const obj = extractJSON(await chat(STANCE_SYSTEM, user));
  if (!obj) throw new Error("无法解析模型输出");
  let stance = String(obj.stance || "unclear").toLowerCase().trim();
  if (!["support", "oppose", "neutral", "irrelevant"].includes(stance)) stance = "unclear";
  return {
    stance,
    confidence: clamp01(Number(obj.confidence)),
    reason: String(obj.reason || "").slice(0, 60),
  };
}

export interface JudgeResult {
  flag: boolean;
  confidence: number;
  reason: string;
}

export async function judgeComment(
  chat: ChatFn,
  criterion: string,
  comment: string,
  maxChars: number,
): Promise<JudgeResult> {
  const crit = criterion.trim() || "对回答作者不友善: 辱骂、人身攻击、阴阳怪气、引战挑衅、无理抬杠";
  const txt = (comment || "").slice(0, maxChars);
  const user =
    `[屏蔽标准]\n${crit}\n\n[一条评论]\n${txt}\n\n` +
    '请输出 JSON: {"block": true 或 false, "confidence": 0到1的小数, "reason": "不超过30字的理由"}';
  const obj = extractJSON(await chat(JUDGE_SYSTEM, user));
  if (!obj) throw new Error("无法解析模型输出");
  const flag = obj.block === true || String(obj.block).toLowerCase() === "true";
  return {
    flag,
    confidence: clamp01(Number(obj.confidence)),
    reason: String(obj.reason || "").slice(0, 60),
  };
}

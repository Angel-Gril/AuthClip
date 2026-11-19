#!/usr/bin/env node
// 静态检查：所有 patchRound 调用必须显式传「代次」（3 个参数）。
//
// 背景：这类竞态已经以五种形状出现过（服务端 state、草稿 gen、pending gen、
// round 对象、await 窗口）。约定是——写入必须声明「我看到的是第几代」，
// 否则就退化成"只按 id 写"，await 期间换链就会写错轮次。
//
// 用法：node scripts/check-round-writes.mjs [path/to/app.js]
// 退出码：0 通过，1 有违反。

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const file = process.argv[2] || join(here, "..", "web", "app.js");
const src = readFileSync(file, "utf8");

const violations = [];
let idx = 0;
while ((idx = src.indexOf("patchRound(", idx)) !== -1) {
  const open = src.indexOf("(", idx);
  let depth = 0;
  let commas = 0;
  let quote = null;
  let i = open;
  for (; i < src.length; i++) {
    const c = src[i];
    if (quote) {
      if (c === "\\") { i++; continue; }
      if (c === quote) quote = null;
      continue;
    }
    if (c === '"' || c === "'" || c === "`") { quote = c; continue; }
    if (c === "(" || c === "[" || c === "{") depth++;
    else if (c === ")" || c === "]" || c === "}") {
      depth--;
      if (depth === 0) break;
    } else if (c === "," && depth === 1) commas++;
  }
  const line = src.slice(0, idx).split(/\r?\n/).length;
  const snippet = src.slice(idx, Math.min(i + 1, idx + 90)).replace(/\s+/g, " ");
  // 定义行本身跳过
  if (!/patchRound\(id, patch, expectGen\)/.test(src.slice(idx, idx + 40))) {
    if (commas !== 2) {
      violations.push({ line, snippet, args: commas + 1 });
    }
  }
  idx = i + 1;
}

if (violations.length) {
  console.error(`✗ ${violations.length} 处 patchRound 调用未显式传代次：`);
  for (const v of violations) {
    console.error(`  app.js:${v.line}  (${v.args} 个参数)  ${v.snippet}`);
  }
  console.error("\n同步写入请传 null，异步写入请传读之前拿到的 gen。");
  process.exit(1);
}
console.log("✓ 所有 patchRound 调用都显式传了代次");

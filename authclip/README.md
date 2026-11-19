# AuthClip · 授权链接中继台（长期链接版）

两条长期链接，都不登录：

```
A：https://host/#a=<admin token>    ← 看板，下发授权链接、收 B 的回传
B：https://host/#b=<member token>   ← 收件箱，点开授权、把回调链接粘回来
```

A 把 Claude 授权链接丢给某个 B → B 点开走官方 OAuth → B 把地址栏那条
`http://localhost:54545/callback?code=…&state=…` 整条粘回来 → 系统自动切出 code / state
→ A 在卡片上「复制凭证」或「复制整行」。

## 一、起服务

```powershell
cd C:\AI\A\AuthClip

# 建人（打印长期链接）；-base 决定打印出来的域名
.\authclip.exe -dir=.\data -base=https://clip.example.com add admin "A-主号"
.\authclip.exe -dir=.\data -base=https://clip.example.com add member "B-小王"
.\authclip.exe -dir=.\data users            # 列出所有人和 id

# 起服务
.\authclip.exe -http=127.0.0.1:8090 -dir=.\data `
  -geo=.\geo\country.mmdb -trust-proxy=127.0.0.1/32 `
  -base=https://clip.example.com
```

| 参数 | 作用 |
| :--- | :--- |
| `-http` | 监听地址；局域网/公网用 `0.0.0.0:8090` |
| `-dir` | 数据目录（`state.json` 0600，原子写） |
| `-geo` | 可选 MMDB 国家库：用服务端观测 IP 判地区 |
| `-trust-proxy` | 可信反代 CIDR：只有这些来源的 `X-Real-IP` 才采信 |
| `-geo-url` | 可选：交给**浏览器**去查地域的端点（如 `https://ip.net.coffee/`），服务端不出站 |
| `-base` | 打印长期链接时用的对外地址 |

## 二、B 的完整动作（4 步）

1. 打开自己的长期链接 → 看到"待处理"的授权卡片
2. 点「① 打开授权链接」→ 在 Claude 官方页面登录/授权
3. 授权成功后浏览器跳到 `http://localhost:54545/callback?code=…`，**页面会显示"无法访问"——这是正常的**，本机没有监听 54545
4. 回到我们的页面：
   - 把地址栏整条 URL 复制粘贴进「③ 回调链接」框 → **自动切出 code，并校验 state 与本条授权链接是否一致**
   - 或点「读剪贴板并解析」（一次点击，Chrome/Edge 允许）
   - 填注册邮箱、注册地区，IP 默认已填服务端观测值，可改；静态家宽节点选填
   - 提交；再提交一次即覆盖

> **为什么不能全自动**：浏览器禁止读取其它标签页的地址栏，也禁止读 `ERR_CONNECTION_REFUSED`
> 页面的 DOM。所以「自动抓取」的可行上限就是**粘贴/读剪贴板 + 自动解析**，两步操作。
> 这不是实现取舍，是平台硬限制；任何声称能直接抓到的方案都要装扩展或本地代理。

### 页面上就有一张示意图

B 打开页面后，卡片上方是一张**内联 SVG 步骤图**（不引外部图片，CSP `style-src 'self'` 也放行）：
地址栏高亮 → `Ctrl+A` / `Ctrl+C` → 粘贴框 → `code` 已提取。图下配三行文字说明。

- 桌面宽度显示图 + 说明；**≤560px（手机）自动隐藏图，只留三行说明**，避免缩成看不清的小字。
- 图和说明只教「从地址栏整条复制」，不涉及在出错页面上点击/交互，因此不会触发任何站点限制。

### 弹窗打开 + 切回自动填

「打开授权链接」现在开**小窗**（`popup=yes,width=560,height=800,noopener`）：

| 能力 | 是否成立 | 原因 |
| :--- | :--- | :--- |
| 开小窗，授权完切回本页 | ✅ | 用户手势触发，弹窗拦截器放行 |
| 读弹窗里的地址栏 / 回调 URL | ❌ | 跨域（claude.ai → localhost:54545 又是另一个 origin）；且最后是 `ERR_CONNECTION_REFUSED` 错误页，无 DOM |
| 读弹窗里的表单 | ❌ | 同上 |
| 判断"用户回来了" | ✅ | `focus` / `visibilitychange` |
| 回来时自动读剪贴板并解析 | ⚠️ 视权限 | Chrome/Edge 首次会弹一次剪贴板读取授权；允许后静默成功，拒绝则退化为「读剪贴板并解析」按钮 |

`noopener` 的取舍：授权页拿不到 `window.opener`（防反向跳转劫持），代价是 `window.open` 返回 `null`，
所以「是否被拦」用 `document.hasFocus()` 做启发式，而不是靠返回值判断。

> 想 100% 全自动（不复制）只有一条路：把 `redirect_uri` 改成指向本服务的回调端点，由服务端收 code。
> 但那样 `code_challenge`(PKCE) 与 verifier 必须同源，而 verifier 在 B 本机的客户端里 —— 会破流程。
> 或者装一个能读当前标签 URL 的浏览器扩展。两者都需要改现有授权方式，所以默认不做。


## 三、A 的完整动作

| 动作 | 说明 |
| :--- | :--- |
| 下发 | 选接收人 + 粘贴授权链接 + 备注 → 对方刷新就能看到 |
| 新增接收人 | 面板里直接加，立刻给出可复制的长期链接 |
| 改链接 | 授权失效就换一条，B 的页面实时刷新，不用重发 |
| 复制凭证 | 一键拿 `code` |
| 复制整行 | `code ⇥ 邮箱 ⇥ 地区 ⇥ 注册IP ⇥ 节点IP`（制表符，可直接贴表格） |
| 补充节点 IP | B 没填时管理员补；也可改地区 |
| 清空回传 / 删除 | 拿走数据后抹掉凭证，或整条删掉 |

## 四、接口与鉴权

所有请求带 `X-Token` 头（token 只出现在 URL fragment `#a=` / `#b=`，永不进请求行、不进 Referer、不进反代日志）。

| 方法 | 路径 | 角色 | 说明 |
| :--- | :--- | :--- | :--- |
| GET | `/api/me` | 任意 | 我是谁 |
| GET | `/api/board` | admin | 接收人清单 + 全部任务 |
| POST | `/api/members` | admin | 新建接收人（返回长期链接） |
| POST | `/api/slots` | admin | 下发 `{member,link,label}` |
| PATCH | `/api/slots/{id}` | admin | 换链接 / 改接收人 / 补节点 IP / 改地区 / 改邮箱 |
| POST | `/api/slots/{id}/clear` | admin | 清空回传 |
| DELETE | `/api/slots/{id}` | admin | 删除 |
| GET | `/api/mine` | member | 我的任务 + 服务端观测 IP/地区 |
| POST | `/api/slots/{id}/report` | member | 回传 `{email,callback,code,reg_ip,reg_region,node_ip}` |
| GET | `/api/stream` | 任意 | SSE（fetch 流，token 走头） |

`callback` 可传整条 URL，服务端会兜底再解析一次；`code` 也可直接给（不带 URL）。

## 五、实测矩阵

| 用例 | 结果 |
| :--- | :--- |
| 伪造 token | 401 |
| member 调 `/api/board` / admin 调 `/api/mine` | 403 / 403 |
| 别人任务回传 | 404；且别人 `/api/mine` 里看不到 |
| `javascript:` 当授权链接 | 400 |
| **把授权链接当回调提交** | **400**（关键：授权链接里带 `code=true`，若不排除会被当成凭证——已修） |
| 回调链接 `…?code=rd5sfX1…&state=9f5eb6…` | code/state 双提取成功 |
| 只给 `code`（不带 URL） | 接受 |
| 坏邮箱 / 坏地区 / 坏节点 IP / 空凭证 | 400 / 400 / 400 / 400 |
| 注册 IP 留空 | 自动落服务端观测值（`X-Real-IP: 8.8.8.8` → `8.8.8.8` + 地区 `US`） |
| admin 补节点 IP + 改地区 | 生效，且不覆盖已回传的 code |
| `state.json` 中是否出现管理员地址 | **False** |
| SSE | `text/event-stream`，两端实时 |

## 六、设计红线

1. **对授权链接零出站**：`link`/`callback` 只当字符串存；全项目没有拿它们发请求的代码路径，连可达性都不校验。
2. **不记录管理员 IP**：admin 路由从不调用 `observed()`；无访问日志（`ErrorLog` 丢弃）。
3. **token 不进 URL 查询串**：只走 fragment + 请求头，反代日志抓不到。
4. **无过期**：任务只有「待回传 / 已回传」，A 随时换链接。

## 七、部署（Caddy）

```caddy
clip.example.com {
	reverse_proxy 127.0.0.1:8090 {
		header_up X-Real-IP {remote_host}
		flush_interval -1
	}
}
```

启动加 `-trust-proxy=127.0.0.1/32`，否则拿到的是反代地址。

## 八、数据

`data/state.json`（0600）：用户、token、授权链接、回传的 code/邮箱/IP/节点。凭证明文落盘，
拿走数据后点「清空回传」；整目录不要进备份/网盘。删用户请直接编辑该文件后重启（v1 未做用户删除接口）。

## 九、截图

| | |
| :--- | :--- |
| A 端看板 | ![A](docs/admin.png) |
| B 端（桌面，含示意图） | ![B](docs/member.png) |
| B 端（手机） | ![B-m](docs/member-mobile.png) |

长期链接在界面上默认打码（点「显示」才展开），避免截屏/投屏时把 token 带出去。

## 十、v1.1.0 安全修复（针对上线前审查的三条阻断项）

审查对象：`authclip` 分支 `1578c8f`。三条均**复现于源码**，非误报。

### 1. 改派接收人泄露上一位的数据（已修）

- **问题**：`PATCH /api/slots/{id}` 换 `member` 时只改 `Member` 字段，旧 `code/callback/email/reg_ip/node_ip/seen_ip` 全部留在任务里；新接收人通过自己的 `/api/mine` 直接读到。且 `report` 的归属校验在事务外，改派并发时不会重新校验。
- **修复**：改派即调用 `clearReturned()` 抹掉全部回传与观测字段；`report` 在 `updateSlot` 闭包内**再校验一次** `s.Member == 用户`，不匹配返回 409「任务在提交期间发生变化」。
- **测试**：`TestReassignClearsReturnedData`、`TestReportAfterReassignRejected`。

### 2. 回传未与授权链接的 state 核对（已修）

- **问题**：只保存回传的 `state`，从不比对当前授权链接里的 `state`。任何人都能把别处的 code 塞进这条任务；任务换链接后旧回传也不会失效。
- **修复**：`linkStateOf(link)` 取授权链接的 `state`；回传时——
  - 链接有 state 而回传没有 → `400`「需要整条回调链接：其中必须带 state」
  - 两者不一致 → `409`「state 与当前授权链接不一致」
  - 事务内按**最新链接**再核一次（提交期间被换链接也会拒）
- **测试**：`TestReportRejectsForeignState`、`TestLinkStateParsing`。

### 3. 长期令牌可经 URL 查询参数泄露（已修）

- **问题**：`wrap()` 里 `?t=` 兜底（早先给 EventSource 用）——令牌会进访问日志、浏览器历史、反代日志。
- **修复**：只认 `X-Token` 请求头；前端本就改用 `fetch` 流式读 SSE，无需查询串。
- **静态存储**：设置 `AUTHCLIP_SECRET` 后，整个状态文件以 **AES-256-GCM** 落盘为 `state.bin`（令牌 + OAuth 回传数据一起加密），旧的 `state.json` 会自动迁移并删除；未设置时启动日志会明确警告。
- **测试**：`TestTokenFromQueryRejected`、`TestStoreEncryptionAtRest`。

### 实测输出（`scripts/verify-fixes.ps1`）

```
state 不一致        -> 409
缺 state            -> 400
state 一致          -> 200
改派后 B2 看到: email='' code='' callback='' reg_ip='' node=''
被改派者 B1 再回传  -> 404
查询参数 ?t=        -> 401        请求头 X-Token -> 200
state.bin 中出现 admin token 明文: False      state.json 存在: False
```

## 十一、Go 版本与漏洞公告

**构建要求：Go ≥ 1.27.4**（`go.mod` 已钉 `go 1.27.4`）。`go1.27.0~1.27.3` 的标准库带 15 条公告，
其中 7 条在本服务可达（`net/http` 服务端、`os.MkdirAll`、`http.MaxBytesReader` 等）。

| 公告 | 修复于 | 影响本服务 | 说明 |
| :--- | :--- | :--- | :--- |
| **GO-2026-6859** | **1.27.4** | **是** | `net/http` MaxBytesReader header race 导致 HTTP/1 服务端崩溃；本服务在 `decode()` 里直接用它 |
| GO-2026-6609 | 1.27.2 | 是 | Range 头无长度上限；`http.FileServer` 提供静态资源 |
| GO-2026-6603 | 1.27.2 | 否（不启 h2） | HTTP/2 Trailer 内存耗尽；Caddy 终结 TLS，后端只跑 HTTP/1.1 |
| GO-2026-6610 / 6611 / 6612 / 6617 | 1.27.2 | 否（不启 h2） | HTTP/2 分帧、流控、HPACK 竞争 |
| GO-2026-6613 | 1.27.2 | 否 | HTTP/1 服务端 CONNECT 失步；未接受 CONNECT |
| GO-2026-6605 | 1.27.2 | 否 | HTTP/1 **客户端** CONNECT；本服务无出站请求 |
| GO-2026-6604 | 1.27.2 | 是（仅 Windows） | `os.MkdirAll` 可跟随 junction；`openStore` 会建目录 |
| GO-2026-6608 | 1.27.2 | 否 | MIME 头解析；不处理 multipart |
| GO-2026-6607 | 1.27.2 | 否 | crypto/tls ECH；不做 TLS 终结 |
| GO-2026-6599 / 6600 | 1.27.2 | 否 | html/template / regexp；未使用 html/template |
| GO-2026-6809 | 1.27.3 | 否 | crypto/x509 中间证书；无出站 TLS 校验证书 |

验证方式与结果：

```text
OSV stdlib@1.27.0 → 15 条（含 GO-2026-6859）
OSV stdlib@1.27.4 → none
govulncheck ./...（go1.27.4 构建）→ No vulnerabilities found
go version -m authclip.exe → go1.27.4
```

> 注意：`govulncheck` 按**实际构建用的工具链**判定。审查环境若用 1.27.0~1.27.3 编译，
> 即使 `go.mod` 写 1.27.4 也会命中；CI 需确认 `go version` 输出。

## 十二、账号、权限分发与审计（v1.2.0）

之前的模型只有「管理员 / 接收人」两层，建号只能靠 CLI，也没有吊销和留痕。v1.2 补齐：

### 三种身份

| 身份 | 范围 | 能做什么 | 看不到什么 |
| :--- | :--- | :--- | :--- |
| 接收人 `member` | — | 只看到派给自己的任务；提交/覆盖回传 | 别人的任务、任何人的链接 |
| 管理员·限自己 `admin/own` | 自己创建的任务 | 下发、改派、清空、删除**自己名下**任务；创建接收人 | 别人的任务、接收人的长期链接、账号列表、审计 |
| 管理员·全量 `admin/all` | 全部 | 全部任务 + 账号管理 + 轮换/吊销 + 审计 | — |

`scope` 为空的历史管理员按 `all` 处理（向后兼容）。

### 账号管理接口（均需 `all`）

| 方法 | 路径 | 说明 |
| :--- | :--- | :--- |
| GET | `/api/users` | 列账号（含角色/范围/吊销状态） |
| POST | `/api/users` | 建账号 `{role,scope,label}`，**长期链接只在这里返回一次** |
| PATCH | `/api/users/{id}` | 改标签 / 改范围 / 吊销 `{revoke:true|false}` |
| POST | `/api/users/{id}/rotate` | 轮换长期链接，**旧链接立即失效** |
| DELETE | `/api/users/{id}` | 删除（名下还有任务时拒绝） |
| GET | `/api/audit` | 最近 100 条操作 |

保护规则：不能吊销/删除自己；`own` 管理员不能改自己的范围、不能升自己为全量。

### 命令行的范围参数

```sh
authclip -dir ./data add admin "A-主号" all     # 默认 all
authclip -dir ./data add admin "A2-外包" own    # 只能管自己下发的
authclip -dir ./data add member "B1-家宽"
authclip -dir ./data users                      # 显示 role / scope / id / 标签 / 是否吊销
```

### 审计

每次 `slot.create / slot.reassign / slot.update / slot.clear / slot.delete / slot.report`
以及 `user.create / user.update / user.revoke / user.restore / user.rotate / user.delete`
都会写一条 `{时间, 操作者 id+标签, 动作, 任务, 对象}`，**不含凭证内容**；最多保留 500 条。

### 权限相关的测试

```
PASS TestOwnScopeAdminIsIsolated   限自己管理员：看不到他人任务/链接/账号/审计，改他人任务 404
PASS TestRevokedLinkStopsWorking   吊销后 401，恢复后 200
PASS TestRotateInvalidatesOldToken 轮换后旧 token 失效、新 token 可用
PASS TestSelfProtection            不能吊销/删除自己
PASS TestAuditTrail                审计里能查到 slot.create / slot.reassign
```

## 十三、归档、历史、导出与留存期（v1.3.0）

### 归档而不是硬删除

`DELETE /api/slots/{id}` 现在是**归档**：任务从 A 的看板消失，但 B 的页面多出「历史（只读）」，能看到自己当初提交的邮箱/code/地区/IP。

- 归档后**不能再回传**（服务端 404，不只是前端隐藏按钮）
- `DELETE /api/slots/{id}?purge=1` 才是彻底删除（需 `all` 权限），B 的历史也一并清除

### 导出（A 端批量）

| 方式 | 说明 |
| :--- | :--- |
| 页面按钮 | 顶栏「导出 CSV / 导出 JSON」（仅 `all` 管理员可见） |
| `GET /api/export?format=csv` | 字段：id、备注、状态、接收人、下发人、邮箱、凭证、注册地区、注册IP、观测IP、节点IP、创建时间、回传时间、已归档 |
| JSON | 同数据，含 `deleted / submitted / state_param` 等完整字段 |

导出**包含已归档任务**，导出动作本身也会写一条审计 `data.export`。

### 留存期

```sh
authclip -http=0.0.0.0:8090 -dir=./data -retain-days 30
```

- `0`（默认）= 归档任务永久保留，B 的历史也永久可查
- `N>0` = 归档超过 N 天的任务在启动时与每 6 小时清理一次，**B 的历史同步消失**

> 关于「7 天」：7 天会把你刚导出完的数据从服务端抹掉，而且不可恢复（加密状态下也无从重建）。
> 建议先用默认 0（永久），确认导出流程跑通后再设 30 天；真要 7 天，务必保证每日导出。

### 权限收紧

`POST /api/members` 现在**只有 `all` 管理员能调**。原因：建人等于分发一条拥有该接收人全部权限的长期链接，
受限管理员若能自建身份，就能绕过「只能管自己任务」的边界。`own` 管理员需要新接收人时，找全量管理员建。

### 测试

```
PASS TestArchiveKeepsMemberHistory   归档后看板不显示、B 历史仍在、且不能再回传
PASS TestPurgeOnlyForAllScope        彻底删除需 all；own 访问他人任务仍 404
PASS TestOwnScopeCannotCreateMember  own 建接收人 403，all 正常
PASS TestExportCSV                   导出含已归档行与表头；own 导出 403
```

## 十四、v1.3.1 安全修复（针对 5498aea 审查）

审查对象 `5498aea`。三条均复现于源码。

### 1. 账号列表暴露长期令牌

- **问题**：`GET /api/users` 直接序列化 `[]User`，`Token` 字段一并返回；`/api/board` 的接收人列表也带 `token/link`。任何 `all` 管理员（或拿到其链接的人）可批量导出所有人的身份凭证。
- **修复**：两个列表都改成投影，**不含 token/link**。要发链接只有一条路：`POST /api/users/{id}/rotate` —— 签发新链接（旧链接同时失效），只显示一次，并记 `user.rotate` 审计。
- **代价**：A 端不能再"查看已有链接"，只能"重新生成"。界面已改成「重新生成链接」。

### 2. OAuth 回传校验缺口

- **问题**：`linkStateOf(link)` 为空时整段校验被跳过；且 `parseCallback` 接受裸 code，服务端无法确认 code 与本次回调同源。
- **修复**：
  - 建任务/改链接**必须带 state**，否则 `400`；
  - 回传**只接受整条回调链接**（含 `code=` 与 `state=`），裸 code 一律 `400`；
  - 链接缺 state 的历史数据：回传直接 `409`，提示管理员更新链接；
  - 事务内按**最新链接**的 state 再核一次。
- 影响：B 侧必须从地址栏复制整条 URL（前端本来就引导这么做），`TOKEN#VERIFIER` 这类裸凭证不再可提交。

### 3. 导出含凭据 + 审计写在输出之后

- **问题**：导出直接吐明文 code/callback/邮箱/IP；`appendAudit` 在 `cw.Flush()` 之后，客户端可能已收完数据而审计失败/丢失。
- **修复**：
  - 默认**脱敏**：凭证 `前4……后2（长度）`、邮箱 `前2***@域名`、IP `a.b.x.x`；CSV 增列「导出模式」；
  - 完整凭据需 `?secrets=1&confirm=EXPORT` 二次确认，界面按钮带 `confirm` 弹窗；
  - **审计先写、再输出**，且区分 `mode=masked|full`。

### 实测

```
/api/users 字段:      created,id,label,revoked,role,scope      ← 无 token
/api/board members:   created,id,label,revoked                 ← 无 token/link
缺 state 的链接    -> 400 授权链接缺少 state 参数…
裸 code 回传       -> 400 请粘贴整条回调链接…不接受单独的授权码
默认导出           -> 凭证 ***/……、邮箱 b1***@example.com、mode=masked
secrets=1 无 confirm -> 400
```

### 新增测试（共 19 个）

```
PASS TestUserListNeverReturnsTokens            列表与看板都不含 token/link
PASS TestLinkMustCarryState                    建/改链接缺 state 一律 400
PASS TestReportRequiresFullCallbackURL         裸 code 400、缺 state 400、完整回调通过
PASS TestExportIsMaskedByDefaultAndAuditedFirst 默认脱敏、完整导出需确认、审计区分模式
```

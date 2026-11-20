// AuthClip 界面文案：以简体中文原文为 key，映射到繁體 / English。
// 简体即原文，不做映射；未收录的字符串原样返回，保证不会出现空白。

const DICT = {
  // ---------- 通用 ----------
  "授权中继": ["授權中繼", "Access desk"],
  "下发授权链接 → 对方打开并回传凭证 → 这里复制。": ["下發授權連結 → 對方開啟並回傳憑證 → 這裡複製。", "Publish a link → they authorise and send it back → copy the code here."],
  "管理员": ["管理員", "Administrator"],
  "接收人": ["接收人", "Member"],
  "有效": ["有效", "Active"],
  "已吊销": ["已撤銷", "Revoked"],
  "当前账号": ["目前帳號", "This account"],
  "删除": ["刪除", "Delete"],
  "创建": ["建立", "Create"],
  "保存": ["儲存", "Save"],
  "复制": ["複製", "Copy"],
  "复制 IP": ["複製 IP", "Copy IP"],
  "复制链接": ["複製連結", "Copy link"],
  "复制凭证": ["複製憑證", "Copy code"],
  "复制整行": ["複製整列", "Copy row"],
  "已复制": ["已複製", "Copied"],
  "已复制（此次查看已记审计）": ["已複製（此次檢視已記入稽核）", "Copied (this reveal is audited)"],
  "已复制：code / 邮箱 / 地区 / IP / 节点": ["已複製：code / 信箱 / 地區 / IP / 節點", "Copied: code / email / region / IP / node"],
  "已保存": ["已儲存", "Saved"],
  "已删除": ["已刪除", "Deleted"],
  "已清空": ["已清空", "Cleared"],
  "未命名": ["未命名", "Untitled"],
  "未知接收人": ["未知接收人", "Unknown member"],
  "角色": ["角色", "Role"],
  "名字": ["名稱", "Name"],
  "用户名": ["使用者名稱", "Username"],
  "用户名（选填）": ["使用者名稱（選填）", "Username (optional)"],
  "用户名（选填，中英文均可，如 b03 / 张三）": ["使用者名稱（選填，中英文皆可，如 b03 / 張三）", "Username (optional, any language, e.g. b03)"],
  "输入用户名，如 b01": ["輸入使用者名稱，如 b01", "Type a username, e.g. b01"],
  "备注": ["備註", "Note"],
  "备注，例如 家宽-日本": ["備註，例如 家用寬頻-日本", "Note, e.g. home broadband - Japan"],
  "地区": ["地區", "Region"],
  "地区，如 JP": ["地區，如 JP", "Region, e.g. JP"],
  "节点 IP": ["節點 IP", "Node IP"],
  "节点 IP（管理员补充）": ["節點 IP（管理員補充）", "Node IP (admin fills)"],
  "静态家宽节点 IP（选填）": ["固定家用寬頻節點 IP（選填）", "Static residential node IP (optional)"],
  "静态家宽节点 IP（可留空）": ["固定家用寬頻節點 IP（可留空）", "Static residential node IP (optional)"],
  "登录密码": ["登入密碼", "Password"],
  "登录密码（如该账号需要）": ["登入密碼（如該帳號需要）", "Password (if the account needs one)"],
  "该账号的登录密码（不需要可留空）": ["該帳號的登入密碼（不需要可留空）", "Password for this account (leave empty if not needed)"],
  "显示": ["顯示", "Show"],
  "隐藏": ["隱藏", "Hide"],
  "密码过长": ["密碼過長", "Password is too long"],
  "邮箱格式不正确": ["信箱格式不正確", "Invalid email format"],
  "已复制：code / 邮箱 / 密码 / 地区 / IP / 节点": ["已複製：code / 信箱 / 密碼 / 地區 / IP / 節點", "Copied: code / email / password / region / IP / node"],
  "注册邮箱": ["註冊信箱", "Registration email"],
  "注册地区": ["註冊地區", "Registration region"],
  "注册地区（两位国家码）": ["註冊地區（兩位國碼）", "Registration region (2-letter code)"],
  "注册区域 IP": ["註冊區域 IP", "Registration IP"],
  "注册 IP": ["註冊 IP", "Registration IP"],
  "注册区域 IP（默认填服务端观测值）": ["註冊區域 IP（預設填入伺服器觀測值）", "Registration IP (defaults to what we observe)"],
  "注册这个账号用的邮箱": ["註冊這個帳號用的信箱", "Email used to register the account"],
  "回传时间": ["回傳時間", "Submitted at"],
  "观测 IP": ["觀測 IP", "Observed IP"],
  "凭证 code": ["憑證 code", "Code"],
  "原始回调链接": ["原始回呼連結", "Raw callback URL"],
  "授权链接": ["授權連結", "Authorisation link"],
  "长期链接": ["長期連結", "Long-term link"],
  "新的授权链接": ["新的授權連結", "New authorisation link"],
  "撤回": ["撤回", "Undo"],

  // ---------- 下发 ----------
  "下发授权链接": ["下發授權連結", "Publish a link"],
  "下发": ["下發", "Publish"],
  "接收人（用户名）": ["接收人（使用者名稱）", "Recipient (username)"],
  "先添加接收人": ["先新增接收人", "Add a member first"],
  "粘贴 Claude 授权链接（原样保存，不会尝试访问）": ["貼上 Claude 授權連結（原樣保存，不會嘗試存取）", "Paste the Claude authorisation link (stored as-is, never fetched)"],
  "用户名叫不准时可直接输名字，下方会显示匹配到谁。服务端只保存链接字符串：不预取、不校验可达性。": ["使用者名稱記不準時可直接輸入名稱，下方會顯示對應到誰。伺服器只保存連結字串：不預取、不檢查可達性。", "Unsure of the username? Type the display name — the match shows below. The server only stores the string: no prefetch, no reachability check."],
  "请填写接收人用户名": ["請填寫接收人使用者名稱", "Enter the recipient username"],
  "该接收人已吊销": ["該接收人已撤銷", "That member is revoked"],
  "请先粘贴授权链接": ["請先貼上授權連結", "Paste the authorisation link first"],
  "已下发；对方打开自己的长期链接就能看到": ["已下發；對方開啟自己的長期連結就能看到", "Published — it now shows on their long-term link"],
  "接收人（长期链接）": ["接收人（長期連結）", "Recipients (long-term links)"],
  "新增接收人": ["新增接收人", "Add a member"],
  "新接收人名字": ["新接收人名稱", "New member name"],
  "创建并生成链接": ["建立並產生連結", "Create and generate link"],
  "查看链接": ["檢視連結", "Reveal link"],
  "重新生成链接": ["重新產生連結", "Regenerate link"],
  "链接不在列表中显示": ["連結不在清單中顯示", "Link is not listed"],
  "这条链接长期有效、无需登录：转发即等于授权，务必只给对应的人。": ["這條連結長期有效、無需登入：轉發即等於授權，務必只給對應的人。", "This link is permanent and needs no login — forwarding it grants access. Only send it to the right person."],
  "新链接已生成并复制（旧链接立即失效）": ["新連結已產生並複製（舊連結立即失效）", "New link generated and copied (the old one is void)"],
  "还没有接收人。": ["還沒有接收人。", "No members yet."],
  "还没有下发授权链接。": ["還沒有下發授權連結。", "No links published yet."],

  // ---------- 任务卡 ----------
  "待回传": ["待回傳", "Waiting"],
  "已回传": ["已回傳", "Submitted"],
  "待处理": ["待處理", "Pending"],
  "已结束": ["已結束", "Closed"],
  "改链接": ["改連結", "Change link"],
  "清空回传": ["清空回傳", "Clear result"],
  "清空这条回传": ["清空這條回傳", "Clear this result"],
  "归档": ["歸檔", "Archive"],
  "彻底删除": ["徹底刪除", "Delete permanently"],
  "保存补充字段": ["儲存補充欄位", "Save extra fields"],
  "B 回传": ["B 回傳", "Submitted"],
  "已清空；请让接收人重新提交本轮结果": ["已清空；請讓接收人重新提交本輪結果", "Cleared — ask them to submit again"],
  "已归档：对方的「历史」里仍可见，看板不再显示": ["已歸檔：對方的「歷史」仍可見，看板不再顯示", "Archived — still visible in their history, hidden from the board"],
  "已彻底删除（对方历史也一并清除）": ["已徹底刪除（對方歷史一併清除）", "Deleted permanently (their history too)"],
  "链接已更新，对方页面会实时刷新": ["連結已更新，對方頁面會即時重新整理", "Link updated — their page refreshes live"],
  "链接已更新，旧回传已清空": ["連結已更新，舊回傳已清空", "Link updated, previous result cleared"],
  "链接已更新，旧回传已保留（已留痕）": ["連結已更新，舊回傳已保留（已留痕）", "Link updated, previous result kept (audited)"],
  "保存后对方页面会实时刷新。": ["儲存後對方頁面會即時重新整理。", "Saving refreshes their page live."],
  "⚠ 这条任务已有回传：保存新链接会清空旧凭证/邮箱/IP（视为新一轮），除非勾选下面的保留。": ["⚠ 這條任務已有回傳：儲存新連結會清空舊憑證/信箱/IP（視為新一輪），除非勾選下面的保留。", "⚠ This task already has a result: saving a new link clears it (new round) unless you tick keep below."],
  "保留已有回传（仅当链接写错但结果仍属本轮；会留痕）": ["保留已有回傳（僅當連結寫錯但結果仍屬本輪；會留痕）", "Keep the existing result (only if the link was mistyped; audited)"],
  "⚠ 这条回传与当前链接对不上。": ["⚠ 這條回傳與目前連結對不上。", "⚠ This result does not match the current link."],
  "回调的 state 与当前授权链接不同，可能是换链接前的旧结果。": ["回呼的 state 與目前授權連結不同，可能是換連結前的舊結果。", "The callback state differs from the current link — possibly a result from the previous link."],
  "当前链接没有 state，无法核对来源，建议重新下发链接。": ["目前連結沒有 state，無法核對來源，建議重新下發連結。", "The link has no state, so the origin can't be verified — republish it."],
  "（多为换链接前的旧结果）。点开对应卡片可一键清空，或让接收人重新提交。": ["（多為換連結前的舊結果）。點開對應卡片可一鍵清空，或讓接收人重新提交。", " (usually from the previous link). Open the card to clear it, or ask them to resubmit."],
  "state 不一致": ["state 不一致", "state mismatch"],

  // ---------- 账号与权限 ----------
  "账号与权限": ["帳號與權限", "Accounts & permissions"],
  "管理员·全量": ["管理員·全域", "Admin · full"],
  "管理员·限自己": ["管理員·限自己", "Admin · own only"],
  "轮换链接": ["輪換連結", "Rotate link"],
  "吊销": ["撤銷", "Revoke"],
  "恢复": ["恢復", "Restore"],
  "降为限自己": ["降為限自己", "Demote to own-only"],
  "提为全量": ["提為全域", "Promote to full"],
  "管理员范围": ["管理員範圍", "Admin scope"],
  "只能管自己创建的任务": ["只能管理自己建立的任務", "Own tasks only"],
  "全量（含账号与审计）": ["全域（含帳號與稽核）", "Full (accounts and audit)"],
  "「限自己」的管理员只能看/管自己下发的任务，也拿不到接收人的长期链接；轮换与吊销会立即让对方当前链接失效。": ["「限自己」的管理員只能看/管自己下發的任務，也拿不到接收人的長期連結；輪換與撤銷會立即讓對方目前連結失效。", "Own-only admins see and manage only their own tasks and cannot read member links; rotating or revoking takes effect immediately."],
  "已吊销，链接立即失效": ["已撤銷，連結立即失效", "Revoked — the link is void"],
  "已恢复访问": ["已恢復存取", "Access restored"],
  "权限范围已更新": ["權限範圍已更新", "Scope updated"],
  "已创建，长期链接已复制（只显示这一次）": ["已建立，長期連結已複製（只顯示這一次）", "Created — the link was copied and shown once"],
  "只有当前账号。": ["只有目前帳號。", "Only this account."],
  "操作审计（最近 12 条）": ["操作稽核（最近 12 筆）", "Audit log (last 12)"],
  "暂无操作。": ["暫無操作。", "No activity yet."],
  "只记录「谁对哪条任务做了什么」，不含凭证内容。": ["只記錄「誰對哪條任務做了什麼」，不含憑證內容。", "Records who did what to which task — never the credentials."],

  // ---------- 导出 ----------
  "导出 CSV": ["匯出 CSV", "Export CSV"],
  "导出 JSON": ["匯出 JSON", "Export JSON"],
  "导出完整凭据": ["匯出完整憑證", "Export full credentials"],
  "默认脱敏（凭证/邮箱/IP 打码）；完整导出会二次确认并留审计。": ["預設去識別（憑證/信箱/IP 打碼）；完整匯出會二次確認並留稽核。", "Masked by default (code/email/IP); full export asks for confirmation and is audited."],
  "已导出（默认脱敏，含已归档任务）": ["已匯出（預設去識別，含已歸檔任務）", "Exported (masked, including archived)"],
  "已导出完整凭据（含已归档）——这份文件请勿外传": ["已匯出完整憑證（含已歸檔）——這份檔案請勿外傳", "Full credentials exported — do not share this file"],
  "手动复制": ["手動複製", "Copy manually"],
  "没有可复制的内容": ["沒有可複製的內容", "Nothing to copy"],

  // ---------- 接收端 ----------
  "我的授权": ["我的授權", "My authorisations"],
  "点开对方给的授权链接，完成授权后把地址栏的回调链接粘回来。": ["點開對方給的授權連結，完成授權後把網址列的回呼連結貼回來。", "Open the link, finish authorising, then paste the callback URL from the address bar back here."],
  "本次连接的出口": ["本次連線的出口", "Your connection"],
  "服务端取不到（内网/本地）": ["伺服器取不到（內網/本機）", "Not visible to the server (local network)"],
  "复制 IP": ["複製 IP", "Copy IP"],
  "提交时会自动写入「注册区域 IP / 地区」，也可以自己改。": ["提交時會自動寫入「註冊區域 IP / 地區」，也可以自行修改。", "Your IP and region are filled in automatically and can be edited."],
  "打开授权链接": ["開啟授權連結", "Open authorisation link"],
  "开在小窗里授权；完成后回到本页，把地址栏整条 URL 粘到下面，或点「读剪贴板并解析」。小窗停在 localhost:54545 打不开是正常的，可以关掉。": ["在小視窗裡授權；完成後回到本頁，把網址列整條 URL 貼到下面，或點「讀剪貼簿並解析」。小視窗停在 localhost:54545 打不開是正常的，可以關掉。", "Authorise in the popup, come back and paste the full URL — or click “Read clipboard”. The popup stopping at localhost:54545 is expected; you can close it."],
  "回调链接（粘贴整条，自动提取 code）": ["回呼連結（貼上整條，自動擷取 code）", "Callback URL (paste the whole thing; the code is extracted)"],
  "把地址栏那条 http://localhost:54545/callback?code=…&state=… 整条粘进来": ["把網址列那條 http://localhost:54545/callback?code=…&state=… 整條貼進來", "Paste the full http://localhost:54545/callback?code=…&state=… URL"],
  "读剪贴板并解析": ["讀剪貼簿並解析", "Read clipboard"],
  "已提取 code": ["已擷取 code", "Code extracted"],
  "已识别 code，可提交": ["已識別 code，可提交", "Code recognised — ready to submit"],
  "state 与本条授权链接一致 ✓": ["state 與本條授權連結一致 ✓", "state matches this link ✓"],
  "state 与本条链接不一致，确认是不是拿错了链接": ["state 與本條連結不一致，確認是不是拿錯了連結", "state does not match — check you used the right link"],
  "这是授权链接本身，不是回调链接": ["這是授權連結本身，不是回呼連結", "This is the authorisation link, not the callback"],
  "这是授权链接本身（含 code_challenge），不是回调链接。请先点开完成授权。": ["這是授權連結本身（含 code_challenge），不是回呼連結。請先點開完成授權。", "This is the authorisation link (contains code_challenge), not the callback. Open it and finish authorising first."],
  "剪贴板里没有 code=": ["剪貼簿裡沒有 code=", "No code= in the clipboard"],
  "已从剪贴板取到 code": ["已從剪貼簿取得 code", "Code read from clipboard"],
  "浏览器没给剪贴板权限，请手动粘贴": ["瀏覽器未授予剪貼簿權限，請手動貼上", "Clipboard permission denied — paste manually"],
  "没识别到 code=，请确认粘的是回调链接": ["沒識別到 code=，請確認貼上的是回呼連結", "No code= found — make sure it is the callback URL"],
  "授权链接已更新，剪贴板内容未填入，请重新打开授权链接。": ["授權連結已更新，剪貼簿內容未填入，請重新開啟授權連結。", "The link changed; nothing was filled from the clipboard. Reopen the link."],
  "授权链接已更新，之前的草稿已清空，请重新粘贴回调链接。": ["授權連結已更新，先前的草稿已清空，請重新貼上回呼連結。", "The link changed and the draft was cleared — paste the callback again."],
  "请先填写注册邮箱": ["請先填寫註冊信箱", "Enter the registration email first"],
  "请先粘贴整条回调链接": ["請先貼上整條回呼連結", "Paste the full callback URL first"],
  "提交回传": ["提交回傳", "Submit"],
  "更新回传": ["更新回傳", "Update"],
  "已回传，对方会立刻看到；再提交一次可覆盖": ["已回傳，對方會立刻看到；再提交一次可覆蓋", "Submitted — they see it immediately. Submit again to overwrite."],
  "用观测值填 IP/地区": ["用觀測值填入 IP/地區", "Use detected IP/region"],
  "历史（只读）": ["歷史（唯讀）", "History (read-only)"],
  "对方结束或归档的任务留在这里，方便你回看自己提交过什么。": ["對方結束或歸檔的任務留在這裡，方便你回看自己提交過什麼。", "Tasks they closed or archived stay here so you can look back."],
  "还没有收到授权任务，等对方下发。": ["還沒有收到授權任務，等對方下發。", "No tasks yet — waiting for them to publish one."],
  "授权任务": ["授權任務", "Authorisation task"],
  "已保留": ["已保留", "Kept"],

  // ---------- 指引图 ----------
  "授权后跳转是正常的": ["授權後跳轉是正常的", "The redirect after authorising is normal"],
  "：浏览器会停在 localhost:54545 并显示「无法访问」，因为本机没有监听这个端口。地址栏里那条就是回调链接。": ["：瀏覽器會停在 localhost:54545 並顯示「無法連上」，因為本機沒有監聽這個連接埠。網址列裡那條就是回呼連結。", ": the browser stops at localhost:54545 and shows “can’t connect”, because nothing listens there. That URL is the callback."],
  "整条复制地址栏": ["整條複製網址列", "Copy the whole address bar"],
  "：点一下地址栏让整条变蓝 → Ctrl+A → Ctrl+C；手机长按地址栏 → 全选 → 复制。": ["：點一下網址列讓整條變藍 → Ctrl+A → Ctrl+C；手機長按網址列 → 全選 → 複製。", ": click the address bar to select all → Ctrl+A → Ctrl+C; on phones, long-press → Select all → Copy."],
  "回到本页粘贴": ["回到本頁貼上", "Paste back here"],
  "：在对应任务中粘贴，或点该任务的「读剪贴板并解析」。": ["：在對應任務中貼上，或點該任務的「讀剪貼簿並解析」。", ": paste into the task, or click “Read clipboard” on it."],
  "从地址栏复制回调链接的示意图": ["從網址列複製回呼連結的示意圖", "Diagram: copying the callback URL from the address bar"],
  "粘贴到这里": ["貼到這裡", "Paste here"],
  "完成后回到对应任务，手动粘贴或点「读剪贴板并解析」。": ["完成後回到對應任務，手動貼上或點「讀剪貼簿並解析」。", "When done, come back to the task and paste it — or click “Read clipboard”."],

  // ---------- 落地页 ----------
  "请用你的长期链接打开": ["請用你的長期連結開啟", "Open with your long-term link"],
  "链接形如 https://…/#a=…（管理员）或 https://…/#b=…（接收人）。不需要登录。": ["連結形如 https://…/#a=…（管理員）或 https://…/#b=…（接收人）。不需要登入。", "Links look like https://…/#a=… (admin) or https://…/#b=… (member). No login needed."],
  "把发给你的长期链接整条粘进来": ["把發給你的長期連結整條貼進來", "Paste the long-term link you were given"],
  "进入": ["進入", "Enter"],
  "链接里没有 #a= 或 #b=，请检查": ["連結裡沒有 #a= 或 #b=，請檢查", "The link has no #a= or #b= — check it"],
  "链接无效或已被更换，请向发链接的人索取新的长期链接。": ["連結無效或已被更換，請向發連結的人索取新的長期連結。", "This link is invalid or was replaced — ask for a new one."],

  // ---------- 连接状态 ----------
  "连接中…": ["連線中…", "Connecting…"],
  "实时已连接": ["即時已連線", "Live"],
  "断开，重试中…": ["中斷，重試中…", "Disconnected, retrying…"],
  "正在加载…": ["正在載入…", "Loading…"],

  // ---------- 服务端消息（客户端映射用） ----------
  "注册邮箱不正确": ["註冊信箱不正確", "Invalid registration email"],
  "需要整条回调链接：其中必须带 state（服务端要核对归属）": ["需要整條回呼連結：其中必須帶 state（伺服器要核對歸屬）", "A full callback URL with state is required so the server can verify it"],
  "回传的 state 与当前授权链接不一致，可能拿错了链接（或该任务已改派/换过链接）": ["回傳的 state 與目前授權連結不一致，可能拿錯了連結（或該任務已改派/換過連結）", "The callback state does not match the current link — wrong link, or the task was reassigned"],
  "该任务的授权链接缺少 state，无法校验回调归属，请管理员更新链接": ["該任務的授權連結缺少 state，無法核對回呼歸屬，請管理員更新連結", "This task's link has no state, so the callback can't be verified — ask an admin to update it"],
  "授权链接已更换，请刷新页面查看新链接后重试": ["授權連結已更換，請重新整理頁面查看新連結後重試", "The link was replaced — refresh and try again"],
  "任务在提交期间发生变化（已改派或已换链接），请刷新后重试": ["任務在提交期間發生變化（已改派或已換連結），請重新整理後重試", "The task changed while submitting — refresh and try again"],
  "任务不存在": ["任務不存在", "Task not found"],
  "账号不存在": ["帳號不存在", "Account not found"],
  "用户名已被占用": ["使用者名稱已被占用", "Username already taken"],
  "用户名不合法：至少要有 1 个字母/数字/汉字": ["使用者名稱不合法：至少要有 1 個字母/數字/漢字", "Invalid username: needs at least one letter, digit or character"],
  "接收人不存在（请检查用户名）": ["接收人不存在（請檢查使用者名稱）", "Recipient not found (check the username)"],
  "授权链接格式不正确": ["授權連結格式不正確", "Invalid authorisation link"],
  "授权链接缺少 state 参数，无法校验回调归属，请使用带 state 的授权链接": ["授權連結缺少 state 參數，無法核對回呼歸屬，請使用帶 state 的授權連結", "The link has no state parameter; use a link that includes state"],
  "需要全量权限的管理员": ["需要全域權限的管理員", "Requires a full-scope admin"],
  "不能通过接口取管理员链接；管理员链接只在创建时下发，遗失请用服务器上的 CLI 重新签发": ["不能透過介面取得管理員連結；管理員連結只在建立時下發，遺失請用伺服器上的 CLI 重新簽發", "Admin links can't be read through the API — reissue on the server with the CLI"],
  "审计写入失败，已拒绝返回链接": ["稽核寫入失敗，已拒絕返回連結", "Audit write failed — refusing to return the link"],
  "审计写入失败，已拒绝导出（避免无记录的敏感数据外流）": ["稽核寫入失敗，已拒絕匯出（避免無紀錄的敏感資料外流）", "Audit write failed — export refused"],
  "轮换未生效（写入失败），旧链接保持不变，可稍后重试": ["輪換未生效（寫入失敗），舊連結保持不變，可稍後重試", "Rotation failed (write error); the old link is unchanged — retry later"],
  "不能轮换自己": ["不能輪換自己", "You can't rotate your own link"],
  "不能吊销自己": ["不能撤銷自己", "You can't revoke yourself"],
  "不能删除自己": ["不能刪除自己", "You can't delete yourself"],
  "该账号已吊销，恢复后再取链接": ["該帳號已撤銷，恢復後再取連結", "That account is revoked — restore it first"],
  "内容过长": ["內容過長", "Content too long"],
  "节点 IP 格式不正确": ["節點 IP 格式不正確", "Invalid node IP"],
  "注册 IP 格式不正确": ["註冊 IP 格式不正確", "Invalid registration IP"],
  "注册地区请填两位国家代码": ["註冊地區請填兩位國碼", "Region must be a 2-letter code"],
  "该账号名下还有任务": ["該帳號名下還有任務", "That account still owns tasks"],
  "槽位不存在": ["任務不存在", "Task not found"],
  "链接无效": ["連結無效", "Invalid link"],
  "写入失败": ["寫入失敗", "Write failed"],
  "保存失败": ["儲存失敗", "Save failed"],
  "操作失败，请重试": ["操作失敗，請重試", "Something went wrong — try again"],
  "尝试过于频繁，请稍后重试": ["嘗試過於頻繁，請稍後重試", "Too many attempts — try again later"],
};

// 含变量的句子：{x} 为占位符
const PATTERNS = {
  "已创建「{x}」，长期链接已可复制": ["已建立「{x}」，長期連結已可複製", "Created “{x}” — the long-term link is ready to copy"],
  "⚠ 有 {x} 条任务的回传与当前链接对不上": ["⚠ 有 {x} 條任務的回傳與目前連結對不上", "⚠ {x} task(s) have results that don't match the current link"],
  "⚠「{x}」已吊销，不能下发": ["⚠「{x}」已撤銷，不能下發", "⚠ “{x}” is revoked — can't publish"],
  "地区 {x}": ["地區 {x}", "Region {x}"],
  "→ {x}（{y}）": ["→ {x}（{y}）", "→ {x} ({y})"],
  "→ {x}": ["→ {x}", "→ {x}"],
  "上次回传：{x}": ["上次回傳：{x}", "Last submitted: {x}"],
  "已结束 · {x}": ["已結束 · {x}", "Closed · {x}"],
};

export const LANGUAGES = [
  ["auto", "自动 / Auto"],
  ["zh-Hans", "简体中文"],
  ["zh-Hant", "繁體中文"],
  ["en", "English"],
];

let locale = "zh-Hans";

export function setLocale(next) {
  locale = next === "zh-Hant" || next === "en" ? next : "zh-Hans";
  return locale;
}

export function getLocale() {
  return locale;
}

export function pickLocale(manual, preferred) {
  if (manual === "zh-Hans" || manual === "zh-Hant" || manual === "en") return manual;
  const list = (preferred && preferred.length ? preferred : [navigator.language || "en"]).map((s) => String(s).toLowerCase());
  for (const tag of list) {
    if (tag.startsWith("zh")) return /hant|tw|hk|mo/.test(tag) ? "zh-Hant" : "zh-Hans";
    if (tag.startsWith("en")) return "en";
  }
  return "zh-Hans";
}

function fill(tpl, parts) {
  let i = 0;
  return tpl.replace(/\{x\}|\{y\}|\{z\}/g, () => parts[i++] ?? "");
}

// t 把简体原文翻译成当前语言；未收录则原样返回
export function t(src) {
  if (locale === "zh-Hans" || typeof src !== "string" || src === "") return src;
  // 只翻含中文的串，避免把邮箱/ID/纯符号误当成文案
  if (!/[\u4e00-\u9fa5]/.test(src)) return src;
  const idx = locale === "en" ? 1 : 0;
  const hit = DICT[src];
  if (hit) return hit[idx];
  for (const [key, val] of Object.entries(PATTERNS)) {
    const parts = key.split(/\{[xyz]\}/);
    const re = new RegExp("^" + parts.map((p) => p.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("(.+?)") + "$");
    const m = src.match(re);
    if (m) return fill(val[idx], m.slice(1));
  }
  return src;
}




<#
  AuthClip 接口自测脚本（本地/自建实例用）

  用法：
    $env:ADMIN_TOKEN  = "从 #a= 链接里取"
    $env:MEMBER_TOKEN = "从 #b= 链接里取"
    powershell -File scripts/probe.ps1

  它会：建一条占位授权链接 → 以 B 身份回传一条伪造的回调链接 → 校验解析、越权、字段校验、SSE。
  不会访问任何外部链接：授权链接一律用 PLACEHOLDER，服务端也不出站。
#>
param(
  [string]$Base = 'http://127.0.0.1:8090',
  [string]$AdminToken = $env:ADMIN_TOKEN,
  [string]$MemberToken = $env:MEMBER_TOKEN,
  [string]$MemberLabel = 'B-自测'
)

$ErrorActionPreference = 'Stop'
if (-not $AdminToken -or -not $MemberToken) {
  throw '请先设置 $env:ADMIN_TOKEN 与 $env:MEMBER_TOKEN（分别取自 #a= 与 #b= 链接）'
}

function J($o) { $o | ConvertTo-Json -Depth 6 -Compress }
function A($tok) { @{ 'X-Token' = $tok } }

Write-Host "=== 1. 身份 ==="
(Invoke-RestMethod "$Base/api/me" -Headers (A $AdminToken)) | ConvertTo-Json -Compress
(Invoke-RestMethod "$Base/api/me" -Headers (A $MemberToken)) | ConvertTo-Json -Compress
try { Invoke-RestMethod "$Base/api/me" -Headers (A 'bogus') | Out-Null; 'BAD: 伪造 token 被接受' }
catch { "伪造 token -> $($_.Exception.Response.StatusCode.value__)" }
try { Invoke-RestMethod "$Base/api/board" -Headers (A $MemberToken) | Out-Null; 'BAD: 接收人看到了看板' }
catch { "接收人 -> 看板 $($_.Exception.Response.StatusCode.value__)" }

Write-Host "`n=== 2. 下发占位授权链接 ==="
$link = 'https://claude.ai/oauth/authorize?client_id=PLACEHOLDER&code=true&code_challenge=CHAL&code_challenge_method=S256&redirect_uri=http%3A%2F%2Flocalhost%3A54545%2Fcallback&response_type=code&scope=user%3Aprofile&state=STATEPROBE'
$board = Invoke-RestMethod "$Base/api/board" -Headers (A $AdminToken)
$memberId = ($board.members | Where-Object { $_.label -eq $MemberLabel }).id
if (-not $memberId) { throw "看板里没有名为「$MemberLabel」的接收人，请先在页面创建" }
$slot = Invoke-RestMethod "$Base/api/slots" -Method Post -ContentType 'application/json' -Headers (A $AdminToken) -Body (J @{ member = $memberId; link = $link; label = '自测' })
"slot: $($slot.id)"
try { Invoke-RestMethod "$Base/api/slots" -Method Post -ContentType 'application/json' -Headers (A $AdminToken) -Body (J @{ member = $memberId; link = 'javascript:alert(1)' }) | Out-Null; 'BAD: 非 http 链接被接受' }
catch { "javascript: 链接 -> $($_.Exception.Response.StatusCode.value__)" }

Write-Host "`n=== 3. 回传：整条 localhost 回调链接 ==="
$cb = 'http://localhost:54545/callback?code=TESTCODE123&state=STATEPROBE'
$rep = Invoke-RestMethod "$Base/api/slots/$($slot.id)/report" -Method Post -ContentType 'application/json' -Headers (A $MemberToken) -Body (J @{ email = 'b@example.com'; callback = $cb; code = ''; reg_ip = ''; reg_region = ''; node_ip = '203.0.113.10' })
"code=$($rep.code) state=$($rep.state_param) callback_saved=$([bool]$rep.callback)"
"reg_ip=$($rep.reg_ip) reg_region=$($rep.reg_region) seen=$($rep.seen_ip)/$($rep.seen_region) node=$($rep.node_ip)"

Write-Host "`n=== 4. 只给 code（不带 URL） ==="
$rep2 = Invoke-RestMethod "$Base/api/slots/$($slot.id)/report" -Method Post -ContentType 'application/json' -Headers (A $MemberToken) -Body (J @{ email = 'b@example.com'; code = 'TOKEN#VERIFIER'; reg_ip = '1.1.1.1'; reg_region = 'JP'; node_ip = '' })
"code=$($rep2.code) reg_ip=$($rep2.reg_ip) reg_region=$($rep2.reg_region)"

Write-Host "`n=== 5. 错误输入 ==="
function Try-Report($body) {
  try { Invoke-RestMethod "$Base/api/slots/$($slot.id)/report" -Method Post -ContentType 'application/json' -Headers (A $MemberToken) -Body (J $body) | Out-Null; return 'ACCEPTED' }
  catch { return "HTTP $($_.Exception.Response.StatusCode.value__)" }
}
'把授权链接当回调（含 code=true）-> ' + (Try-Report @{ email = 'b@example.com'; callback = $link })
'坏邮箱                           -> ' + (Try-Report @{ email = 'nope'; code = 'x' })
'坏注册地区                       -> ' + (Try-Report @{ email = 'b@example.com'; code = 'x'; reg_region = 'JPN' })
'坏节点 IP                        -> ' + (Try-Report @{ email = 'b@example.com'; code = 'x'; node_ip = '999.1.1.1' })
'空凭证                           -> ' + (Try-Report @{ email = 'b@example.com'; code = ''; callback = '' })

Write-Host "`n=== 6. 越权 ==="
$other = Invoke-RestMethod "$Base/api/members" -Method Post -ContentType 'application/json' -Headers (A $AdminToken) -Body (J @{ label = 'B-越权自测' })
try { Invoke-RestMethod "$Base/api/slots/$($slot.id)/report" -Method Post -ContentType 'application/json' -Headers (A $other.token) -Body (J @{ email = 'x@example.com'; code = 'x' }) | Out-Null; 'BAD: 他人任务可回传' }
catch { "他人任务回传 -> $($_.Exception.Response.StatusCode.value__)" }
$mineOther = Invoke-RestMethod "$Base/api/mine" -Headers (A $other.token)
"他人可见任务数: $($mineOther.slots.Count)"

Write-Host "`n=== 7. 管理员补充 / 清空 ==="
$p = Invoke-RestMethod "$Base/api/slots/$($slot.id)" -Method PATCH -ContentType 'application/json' -Headers (A $AdminToken) -Body (J @{ node_ip = '198.51.100.9'; region = 'us' })
"patch -> node=$($p.node_ip) region=$($p.reg_region) code保留=$([bool]$p.code)"
$c = Invoke-RestMethod "$Base/api/slots/$($slot.id)/clear" -Method Post -ContentType 'application/json' -Headers (A $AdminToken) -Body (J @{})
"clear -> code='$($c.code)' email='$($c.email)'"

Write-Host "`n=== 8. 流 ==="
$req = [Net.HttpWebRequest]::Create("$Base/api/stream")
$req.Headers.Add('X-Token', $MemberToken)
$req.Timeout = 4000
$resp = $req.GetResponse()
$sr = New-Object IO.StreamReader($resp.GetResponseStream())
"SSE: $($resp.StatusCode) $($resp.ContentType) first=$($sr.ReadLine())"
$sr.Close()

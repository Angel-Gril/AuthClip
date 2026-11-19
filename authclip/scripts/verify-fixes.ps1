$ErrorActionPreference = 'Stop'
$root = 'C:\AI\A\AuthClip'
$dir  = 'C:\AI\A\probe3\data'
$base = 'http://127.0.0.1:8090'
$env:AUTHCLIP_SECRET = 'probe-secret-do-not-reuse'
$env:PATH = "C:\AI\A\tools\go\go\bin;$env:PATH"

Remove-Item -Recurse -Force C:\AI\A\probe3 -ErrorAction SilentlyContinue
Get-Process authclip -ErrorAction SilentlyContinue | Stop-Process -Force

Write-Host '=== 0. 建人（走 CLI，顺带拿到长期链接）==='
$aOut = & "$root\authclip.exe" -dir $dir add admin 'A'
$b1Out = & "$root\authclip.exe" -dir $dir add member 'B1'
$b2Out = & "$root\authclip.exe" -dir $dir add member 'B2'
$adminTok = [regex]::Match(($aOut -join ' '), '#a=([a-z0-9]+)').Groups[1].Value
$b1Tok    = [regex]::Match(($b1Out -join ' '), '#b=([a-z0-9]+)').Groups[1].Value
$b2Tok    = [regex]::Match(($b2Out -join ' '), '#b=([a-z0-9]+)').Groups[1].Value
"admin=$($adminTok.Substring(0,6))…  B1=$($b1Tok.Substring(0,6))…  B2=$($b2Tok.Substring(0,6))…"

Write-Host "`n=== 1. 落盘是否已加密 ==="
Get-ChildItem $dir | Select-Object Name,Length | Format-Table -AutoSize | Out-String -Width 120
$bin = [IO.File]::ReadAllBytes("$dir\state.bin")
$txt = [Text.Encoding]::ASCII.GetString($bin)
"state.bin 中是否出现 admin token 明文: $($txt.Contains($adminTok))"
"state.bin 中是否出现 `"users`" 字段名: $($txt.Contains('"users"'))"
"state.json 是否存在: $(Test-Path "$dir\state.json")"

Start-Process -FilePath "$root\authclip.exe" -ArgumentList "-http=127.0.0.1:8090","-dir=$dir","-geo=C:\AI\A\probe\country.mmdb","-trust-proxy=127.0.0.1/32" -WindowStyle Hidden
Start-Sleep -Seconds 3

function J($o) { $o | ConvertTo-Json -Depth 6 -Compress }
function A($t) { @{ 'X-Token' = $t } }
function Req($method, $path, $tok, $body, $extra) {
  $h = A $tok; if ($extra) { $extra.GetEnumerator() | ForEach-Object { $h[$_.Key] = $_.Value } }
  $args = @{ Uri = "$base$path"; Method = $method; Headers = $h; SkipHttpErrorCheck = $true }
  if ($body) { $args.Body = (J $body); $args.ContentType = 'application/json' }
  $r = Invoke-WebRequest @args
  return [pscustomobject]@{ Code = [int]$r.StatusCode; Body = $r.Content }
}

Write-Host "`n=== 2. 下发（链接 state=STATE-ONE）==="
$link = 'https://claude.ai/oauth/authorize?client_id=PLACEHOLDER&code=true&code_challenge=CHAL&redirect_uri=http%3A%2F%2Flocalhost%3A54545%2Fcallback&response_type=code&state=STATE-ONE'
$board = (Req 'GET' '/api/board' $adminTok $null).Body | ConvertFrom-Json
$b1Id = ($board.members | Where-Object { $_.label -eq 'B1' }).id
$b2Id = ($board.members | Where-Object { $_.label -eq 'B2' }).id
$slot = (Req 'POST' '/api/slots' $adminTok @{ member = $b1Id; link = $link; label = 'p3' }).Body | ConvertFrom-Json
"slot=$($slot.id)"

Write-Host "`n=== 3. state 归属校验 ==="
$bodyA = @{ email = 'b1@example.com'; callback = 'http://localhost:54545/callback?code=GOOD&state=STATE-TWO'; reg_ip = ''; reg_region = ''; node_ip = '' }
$r = Req 'POST' "/api/slots/$($slot.id)/report" $b1Tok $bodyA
"state 不一致        -> $($r.Code)  $((($r.Body | ConvertFrom-Json).error))"
$bodyA.callback = 'http://localhost:54545/callback?code=NOSTATE'
$r = Req 'POST' "/api/slots/$($slot.id)/report" $b1Tok $bodyA
"缺 state            -> $($r.Code)  $((($r.Body | ConvertFrom-Json).error))"
$bodyA.callback = 'http://localhost:54545/callback?code=GOOD&state=STATE-ONE'
$r = Req 'POST' "/api/slots/$($slot.id)/report" $b1Tok $bodyA
"state 一致          -> $($r.Code)"

Write-Host "`n=== 4. 改派是否清空 ==="
$before = (Req 'GET' '/api/mine' $b2Tok $null).Body | ConvertFrom-Json
"改派前 B2 可见任务数: $($before.slots.Count)"
$p = Req 'PATCH' "/api/slots/$($slot.id)" $adminTok @{ member = $b2Id }
"改派 -> $($p.Code)"
$after = (Req 'GET' '/api/mine' $b2Tok $null).Body | ConvertFrom-Json
$s = $after.slots[0]
"改派后 B2 看到: email='$($s.email)' code='$($s.code)' callback='$($s.callback)' reg_ip='$($s.reg_ip)' node='$($s.node_ip)'"
$r = Req 'POST' "/api/slots/$($slot.id)/report" $b1Tok $bodyA
"被改派者 B1 再回传 -> $($r.Code)（应 404）"

Write-Host "`n=== 5. 令牌只认请求头 ==="
$r = Invoke-WebRequest -Uri "$base/api/me?t=$adminTok" -SkipHttpErrorCheck
"查询参数 ?t=  -> $([int]$r.StatusCode)（应 401）"
"请求头 X-Token -> $((Req 'GET' '/api/me' $adminTok $null).Code)（应 200）"

Write-Host "`n=== 6. B2 正常回传（新 state 仍匹配）==="
$bodyB = @{ email = 'b2@example.com'; callback = 'http://localhost:54545/callback?code=B2CODE&state=STATE-ONE'; reg_ip = ''; reg_region = ''; node_ip = '' }
$r = Req 'POST' "/api/slots/$($slot.id)/report" $b2Tok $bodyB @{ 'X-Real-IP' = '8.8.8.8' }
$ok = $r.Body | ConvertFrom-Json
"回传 -> $($r.Code)  code=$($ok.code) reg_ip=$($ok.reg_ip) region=$($ok.reg_region) seen=$($ok.seen_ip)/$($ok.seen_region)"

Get-Process authclip -ErrorAction SilentlyContinue | Stop-Process -Force
Write-Host "`n(服务已停)"

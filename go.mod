module authclip

// 勿降级：1.27.0~1.27.3 的 net/http 带 GO-2026-6859（MaxBytesReader header race）
// 等 15 条公告；1.27.4 起 stdlib 无已知漏洞（OSV stdlib@1.27.4 → none）。
go 1.27.4

require github.com/oschwald/maxminddb-golang/v2 v2.9.1

require golang.org/x/sys v0.50.0 // indirect

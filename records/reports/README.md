# 报告导出（reports）

评测跑出来的报告导出，供评审直接打开。与 `../` 下的验证记录分开：那边是「这次上机做了什么」，
这边是「跑出来的东西长什么样」。

> 目录名不叫 `artifacts/`：仓库 `.gitignore` 把 `**/artifacts/` 视为构建产物，整个目录会被忽略。

| 文件 | 来源 | 状态 |
|---|---|---|
| `p8_rpt.md` | 早期评测报告导出（markdown） | 入库 |
| `maclaw-report-redteam_report_241665bb7e1cd6cecd1cef8f.pdf` | P7 轮次报告 | 本地，不入库 |
| `maclaw-report-redteam_report_5c42d28769db8a68162626b9.pdf` | P8 轮次报告 | 本地，不入库 |

PDF 不入库的原因：二进制、体积大（每份约 1.7MB）、且重新跑一次评测即可再生。
`.gitignore` 里只忽略 `*.pdf`，所以以后新增的 markdown 导出仍会正常入库。

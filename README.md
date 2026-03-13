# tools-cron

定时任务 Tool Provider：按 cron 表达式或固定间隔向 Agent 提交任务，支持配置静态任务与通过工具动态增删。

## 工具

| 工具 | 说明 |
|------|------|
| `cron_list` | 列出所有定时任务（含配置中的静态任务与 cron_add 添加的动态任务） |
| `cron_add` | 添加一条动态任务（spec + task_input），持久化到 DB，返回 job_id |
| `cron_remove` | 按 job_id 删除动态任务 |
| `cron_toggle` | 按 job_id 启用/禁用动态任务 |

## 配置

在 Agent 配置的 `tool_providers["github.com/OctoSucker/tools-cron"]` 下：

| 键 | 说明 |
|------|------|
| `storage_path` | SQLite 数据库路径（必填）。未配置时 cron 不启动，工具返回错误。 |
| `timezone` | 可选，如 `Asia/Shanghai`；默认本地时区。 |
| `schedules` | 可选，静态任务列表：`[{ "spec": "0 9 * * *", "task_input": "请做今日巡检" }]`。静态任务不写入 DB，重启后从配置重新加载。 |

## spec 格式

- 标准 5 段 cron：`分 时 日 月 周`，如 `0 9 * * *` 每天 9:00。
- 固定间隔：`@every 1h`、`@every 30m`、`@every 5m`（依赖 robfig/cron/v3）。

## 存储

SQLite 单文件，表 `cron_jobs`：id, spec, task_input, enabled, created_at。进程重启后动态任务从 DB 恢复并继续调度。

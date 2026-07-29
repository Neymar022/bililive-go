# Issue tracker: GitHub

本仓库的问题和 PRD 位于 `Neymar022/bililive-go` 的 GitHub Issues。所有操作使用 `gh` CLI。

## 约定

- **创建 issue**：`gh issue create --title "..." --body "..."`。多行正文使用 heredoc。
- **读取 issue**：`gh issue view <number> --comments`，并用 `jq` 过滤评论、读取标签。
- **列出 issues**：`gh issue list --state open --json number,title,body,labels,comments --jq '[.[] | {number, title, body, labels: [.labels[].name], comments: [.comments[].body]}]'`，按需增加 `--label` 和 `--state` 过滤。
- **评论 issue**：`gh issue comment <number> --body "..."`。
- **添加或移除标签**：`gh issue edit <number> --add-label "..."` / `--remove-label "..."`。
- **关闭 issue**：`gh issue close <number> --comment "..."`。

在仓库 clone 内运行时，从 `git remote -v` 推断仓库；`gh` 会自动完成该推断。

## 将 pull request 作为 triage 输入

**PRs as a request surface: no.** 如本仓库以后把外部 PR 视为功能请求，可将此值改为 `yes`，供 `/triage` 读取。

改为 `yes` 后，PR 使用与 issue 相同的标签和状态，并采用对应的 `gh pr` 操作：

- **读取 PR**：`gh pr view <number> --comments`，并用 `gh pr diff <number>` 读取 diff。
- **列出待 triage 的外部 PR**：`gh pr list --state open --json number,title,body,labels,author,authorAssociation,comments`，仅保留 `authorAssociation` 为 `CONTRIBUTOR`、`FIRST_TIME_CONTRIBUTOR` 或 `NONE` 的 PR，排除 `OWNER`、`MEMBER` 和 `COLLABORATOR`。
- **评论、标签和关闭**：使用 `gh pr comment`、`gh pr edit --add-label` / `--remove-label`、`gh pr close`。

GitHub 的 issue 和 PR 共用编号空间。遇到单独的 `#42` 时，先运行 `gh pr view 42`，失败后再运行 `gh issue view 42`。

## Skill 操作语义

- 当 skill 要求“publish to the issue tracker”时，创建 GitHub issue。
- 当 skill 要求“fetch the relevant ticket”时，运行 `gh issue view <number> --comments`。

## Wayfinding 操作

供 `/wayfinder` 使用。一个 map 对应一个 issue，child tickets 使用其子 issue。

- **Map**：一个带 `wayfinder:map` 标签的 issue，正文包含 Notes、Decisions-so-far 和 Fog。使用 `gh issue create --label wayfinder:map`。
- **Child ticket**：通过 GitHub sub-issues API 关联到 map，标签为 `wayfinder:<type>`，其中 type 为 `research`、`prototype`、`grilling` 或 `task`。若 sub-issues 不可用，则在 map 正文使用 task list，并在 child 顶部写入 `Part of #<map>`。领取后分配给执行者。
- **Blocking**：优先使用 GitHub 原生 issue dependencies。通过 `gh api --method POST repos/<owner>/<repo>/issues/<child>/dependencies/blocked_by -F issue_id=<blocker-db-id>` 添加依赖，其中 `blocker-db-id` 必须是 `gh api repos/<owner>/<repo>/issues/<n> --jq .id` 返回的数据库数字 id。若原生依赖不可用，在 child 顶部使用 `Blocked by: #<n>, #<n>`。
- **Frontier query**：列出 map 的开放 child，排除仍有开放 blocker 或已有 assignee 的项目，按 map 顺序取第一个。
- **Claim**：`gh issue edit <n> --add-assignee @me`，这是 Session 的第一次写操作。
- **Resolve**：先用 `gh issue comment <n> --body "<answer>"` 写入答案，再关闭 issue，最后在 map 的 Decisions-so-far 中追加上下文链接。

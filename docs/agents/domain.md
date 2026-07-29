# Domain Docs

本文定义工程 skills 在探索代码库时如何读取本仓库的领域文档。

## 探索前读取

- 读取仓库根目录的 `CONTEXT.md`。
- 如果根目录存在 `CONTEXT-MAP.md`，按其指引读取与当前主题相关的 `CONTEXT.md`。
- 读取 `docs/adr/` 中与当前工作区域有关的 ADR。

如果这些文件不存在，静默继续，不要把缺失本身当作问题，也不要预先建议创建。`/domain-modeling` 会在实际解决领域词汇或架构决策时按需创建它们。

## 文件结构

本仓库采用 single-context 布局：

```text
/
├── CONTEXT.md
├── docs/adr/
│   ├── 0001-example-decision.md
│   └── 0002-example-decision.md
└── src/
```

## 使用 glossary 的词汇

输出中涉及领域概念时，包括 issue 标题、重构建议、假设和测试名称，应使用 `CONTEXT.md` 中定义的词汇，不要漂移到 glossary 明确排除的同义词。

如果需要的概念不在 glossary 中，先判断是否使用了项目并不采用的自造词；若确实存在领域缺口，记录给 `/domain-modeling`。

## 显式报告 ADR 冲突

如果输出与现有 ADR 冲突，必须显式指出，不得静默覆盖。例如：

> 与 ADR-0007 冲突；由于新的运行证据，建议重新评估该决策。

## 与 plan-tree 的边界

- `CONTEXT.md` 和 `docs/adr/` 只承载稳定的领域词汇、模块上下文和架构决策。
- `docs/plantree/` 继续作为 roadmap、执行状态、开放问题、交接和运行证据的权威。
- 两类文档可以相互链接，但 domain docs 不复制 plan-tree 中的路线、状态或生产运行日志，也不替代 plan-tree 的计划治理职责。

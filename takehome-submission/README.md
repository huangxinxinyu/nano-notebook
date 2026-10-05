# 本地知识库问答系统：提交说明

本项目实现了面向本地 Markdown / TXT 文档的知识库问答：文档进入可追踪的 Source 处理流水线，检索阶段组合语义检索与 BM25，回答阶段只在用户选定的 Sources 范围内搜索，并随答案返回来源。

## 交付物

| 内容 | 位置 |
| --- | --- |
| 完整项目代码 | 仓库根目录的 `cmd/`、`internal/`、`web/`、`infra/` 等目录 |
| 测试知识库 | `takehome-submission/knowledge-base/` |
| 知识库生成脚本 | `takehome-submission/knowledge-base/generate.py` |
| 知识库生成 Prompt | `takehome-submission/knowledge-base/GENERATION_PROMPT.md` |
| 10 个测试问题 | `takehome-submission/test-cases/questions.json` |
| 测试输出结果 | `takehome-submission/test-results/actual-outputs.md` |
| 自动化运行脚本 | `takehome-submission/run_evaluation.py` |
| 设计说明 | `takehome-submission/DESIGN.md` |
| AI 使用说明 | `takehome-submission/AI_USAGE.md` |
| AI 主要对话记录 | `takehome-submission/ai-conversation/AI_CONVERSATION.md` |

测试知识库是虚构项目 AtlasDesk，共 35 个 Markdown 文件。内容刻意包含相似 TTL、历史配置、跨文件事实、重复背景和知识库不存在的问题，用于验证检索召回、信息区分、跨文档综合与拒答表现。

## 系统方案概览

1. 文档上传后进入 Source 处理状态机，完成格式校验、文本规范化、Evidence Unit 建立、Chunk、Embedding 与索引发布。
2. Chunk 默认按最多 800 个 Unicode 字符切分，重叠 120 个字符，并尽量保留标题上下文。
3. 查询同时走 Dense 与 BM25 两路召回；两路结果通过 RRF 融合，再对有限候选集重排。
4. Agent 的 `search_evidence` 只能查询本轮固定的 Source 集合，避免检索到用户未选择或无权访问的文档。
5. 答案中的来源引用由服务端根据检索证据解析和发布，不把模型输出的任意文件名直接当作可信引用。

完整取舍见 [DESIGN.md](DESIGN.md)。

## 本地运行

前置环境：Go 1.25+、Node.js 22+、Docker，以及本地模型网关所需的模型凭据。

```bash
make bootstrap
make migrate
make seed
make start
```

默认地址：

- Web：`http://127.0.0.1:5173`
- Control Plane：`http://127.0.0.1:8080`
- Worker：`http://127.0.0.1:8081`

手工验证时，新建 Notebook，上传 `takehome-submission/knowledge-base/` 下的 35 个测试文档，等待所有 Source 进入 Ready 状态，再依次输入测试问题。

## 重新生成测试知识库

```bash
python3 takehome-submission/knowledge-base/generate.py
```

生成过程是确定性的；重复执行会得到同一组 AtlasDesk 文档。`GENERATION_PROMPT.md` 保存了知识库设计时使用的主要 Prompt。

## 自动运行测试问题

先保持本地服务运行，再执行：

```bash
python3 takehome-submission/run_evaluation.py
```

脚本会完成注册、创建 Notebook、上传并等待文档索引、逐题创建 Chat，以及保存回答和引用。输出文件为：

```text
takehome-submission/test-results/actual-outputs.json
takehome-submission/test-results/actual-outputs.md
```

脚本只允许访问 `http://127.0.0.1:8080`，不会把登录密码或模型凭据写入结果文件。

## 测试覆盖

问题集合共 10 题：

- 5 个单文档问题；
- 2 个跨文档问题；
- 1 个相似信息区分问题；
- 2 个知识库不存在答案的问题。

每题的预期来源、必须覆盖的事实和禁止混入的事实均保存在 `questions.json`，便于复查或扩展自动评分。

## 常用验证命令

```bash
go test ./internal/source ./internal/retrieval ./internal/agent
python3 -m json.tool takehome-submission/test-cases/questions.json >/dev/null
python3 -m py_compile \
  takehome-submission/knowledge-base/generate.py \
  takehome-submission/run_evaluation.py
```

完整工程命令还包括 `make test-go`、`make test-web` 和 `make health`。

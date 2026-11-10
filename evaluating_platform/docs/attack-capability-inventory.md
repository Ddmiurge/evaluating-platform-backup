# 大模型攻击能力清单：插件 / 策略 / 技能全量分类

> 更新时间：2026-11-06｜数据源：`internal/maclaw/promptfoo_plugin_catalog.go`（插件）、
> `frontend/src/services/engineEval.ts`（策略镜像与下线表）、`docs/skills/expert-skill-catalog.md`（技能）
> 口径：三源均以仓库为准，插件/策略前后端一致性由 `TestFrontendOptionsConsistency` CI 守卫；
> 本文是**索引**，判定语义与报告口径见 `AGENTS.md`。

---

## 0. 一页总览

| 层 | 数量 | 角色 | 执行链 |
|---|---|---|---|
| **插件**（promptfoo 内置） | **116** 个 | 攻击**维度**定义器：决定"往哪个方向生成探针"+ 判分维度 | promptfoo 引擎链（插件 × 策略） |
| **策略**（可选包装） | **27** 可用（含 5 个远程闸不可用）+ **9** 已下线 | 载荷**变换器**：对已生成探针做编码/推进/包装 | 同上 |
| **技能**（专家 Skill） | **8** 个 | 研究级**载荷流水线**：输入专家样本 → 产出 `payload_dataset`（可调 LLM 或确定性） | 技能/样本链（`execute_redteam_evaluation_batch`） |

插件严重度分布：严重 18 / 高 61 / 中 31 / 低 6。
可用性：**90 个远程闸不可用 / 26 个本地可生成**（`policy` 另需先提供配置文档）。
按类别：有害内容 26；隐私与数据泄露 6；提示注入与指令劫持 8；偏见与歧视 5；模型自身安全缺陷 1；幻觉与过度依赖 3；离题与边界 2；品牌风险 2；敏感话题 3；行业垂直与合规 54；基准数据集回放 6。

---

## 1. 三层架构：这些"攻击手段"分别是什么

- **插件 = 问什么**。每类插件定义一个风险维度（如"化学生物武器"），引擎按它的模板生成探针、按它的维度判分。插件编译在引擎里，是 promptfoo 的"内置菜单"。
- **策略 = 怎么包**。探针生成后，策略对它做变换：换成 Base64、渐进升级多轮话术、套引用格式……策略可运行时加载（文本或 JS 模块）。
- **技能 = 整条研究流水线**。把论文级方法（AutoDAN、GPTFuzzer、FigStep…）封装成可调用包：吃进专家样本，吐出标准 `payload_dataset`，由平台批量执行、判定、出报告；多模态攻击能力目前**全部**在这一层。
- **两条执行链不混用**：引擎链 = `promptfoo` 原生生成（插件×策略，`generation_path=plan_c`）；技能链 = Skill 生成载荷 → 平台注册 handle → 批量执行。同一次评测只走一条。

---

## 2. 插件清单（116 个，按 promptfoo 风险类别）

> **图例**：⚠ 远程生成闸 = promptfoo 远程生成服务依赖，本部署 fail-closed 关闭（选择器置灰、提交即拒 `plugin_requires_remote_generation`）——116 个中 **90 个**带此闸、**26 个本地可生成**；⚠ 需先提供策略文档 = `requires_config`（1 个：`policy`）。标志由 `testdata/promptfoo_remote_dep.json` 实测生成，非手写。

### 有害内容（harmful）—— 暴力/犯罪/自伤/违法物品/仇恨等直出类（26）

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `harmful` | 有害内容检测 | 高 | 检验被测模型是否拒绝暴力、自伤、违法物品制作等有害内容请求，覆盖社会工程、角色扮演、场景诱导等攻击手法。 |
| `harmful:child-exploitation` | 儿童剥削内容 | 严重 | 检验模型对儿童剥削相关内容的识别与拒答（最高优先级红线）。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:chemical-biological-weapons` | 化学生物武器 | 严重 | 检验模型是否拒绝化学生物武器制造指导。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:indiscriminate-weapons` | 大规模杀伤武器 | 严重 | 检验模型是否拒绝大规模杀伤性武器相关请求。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:weapons:ied` | 简易爆炸装置 | 严重 | 检验模型是否拒绝简易爆炸装置制作指导。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:cybercrime` | 网络犯罪协助检测 | 严重 | 检验被测模型是否拒绝提供网络犯罪协助，如入侵方法、恶意软件编写、数据窃取步骤等。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:cybercrime:malicious-code` | 恶意代码编写 | 严重 | 检验模型是否拒绝编写恶意代码、勒索软件、后门等。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:violent-crime` | 暴力犯罪协助检测 | 严重 | 检验被测模型是否拒绝提供暴力犯罪相关指导，如伤害方法、袭击策划等。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:illegal-activities` | 违法活动协助检测 | 严重 | 检验被测模型是否拒绝协助违法活动，如毒品制作、武器改装、伪造证件等。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:illegal-drugs` | 违禁药物 | 高 | 检验模型是否拒绝违禁药物制作与获取指导。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:illegal-drugs:meth` | 冰毒制造 | 严重 | 检验模型是否拒绝冰毒等合成毒品制造指导。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:hate` | 仇恨言论 | 高 | 检验模型是否生成针对群体的仇恨与歧视性言论。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:harassment-bullying` | 骚扰霸凌 | 高 | 检验模型是否生成骚扰、霸凌类内容。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:insults` | 人身攻击辱骂 | 中 | 检验模型是否输出人身攻击与辱骂内容。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:profanity` | 脏话粗口 | 低 | 检验模型在诱导下是否输出低俗辱骂内容。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:radicalization` | 极端化煽动 | 高 | 检验模型是否输出极端主义与煽动性内容。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:self-harm` | 自残自杀 | 严重 | 检验模型对自残自杀相关请求的安全处理。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:sex-crime` | 性犯罪 | 严重 | 检验模型对性犯罪相关内容的识别与拒答。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:sexual-content` | 色情内容 | 高 | 检验模型是否拒绝生成露骨色情内容。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:graphic-content` | 血腥暴力内容 | 高 | 检验模型对血腥、暴力、年龄限制内容的处理。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:misinformation-disinformation` | 虚假信息有害宣传 | 高 | 检验模型是否协助编造虚假信息与有害宣传。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:privacy` | 隐私侵犯协助 | 高 | 检验模型是否协助侵犯他人隐私（人肉、跟踪等）。 |
| `harmful:copyright-violations` | 版权侵犯 | 中 | 检验模型是否大段复述受版权保护的内容。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:intellectual-property` | 知识产权侵犯 | 中 | 检验模型是否协助盗版、破解等知识产权侵犯行为。 |
| `harmful:specialized-advice` | 无资质专业建议 | 高 | 检验模型是否在无免责声明下输出医疗/法律/金融等专业建议。 ⚠ 远程生成闸，本环境不可用 |
| `harmful:unsafe-practices` | 危险行为教唆 | 高 | 检验模型是否输出危险行为（危险挑战、极端节食等）指导。 ⚠ 远程生成闸，本环境不可用 |

### 隐私与数据泄露（privacy）—— PII、会话与跨会话泄露（6）

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `pii` | 隐私信息泄露检测 | 高 | 诱导被测模型输出个人隐私、系统提示词或内部敏感信息，检验模型的隐私保护边界。 |
| `pii:direct` | 直接索要隐私 | 高 | 直接要求模型输出他人个人信息，检验基础隐私边界。 |
| `pii:social` | 社工套取隐私 | 高 | 通过社会工程话术诱导模型泄露个人隐私。 |
| `pii:api-db` | API/数据库隐私外泄 | 高 | 构造 API/数据库访问场景，检验模型是否协助越权导出隐私数据。 |
| `pii:session` | 会话数据泄露 | 高 | 检验模型是否会泄露其他会话中的用户数据。 |
| `cross-session-leak` | 跨会话数据泄漏 | 高 | 检验多会话隔离边界，是否串扰其他用户上下文。 |

### 提示注入与指令劫持（injection）—— 含 MCP 与 Agent 记忆投毒（8）

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `indirect-prompt-injection` | 间接提示注入检测 | 高 | 模拟从网页、文档、邮件等外部内容中嵌入的注入指令，检验被测模型处理外部内容时是否被劫持。 ⚠ 远程生成闸，本环境不可用 |
| `ascii-smuggling` | ASCII 走私检测 | 高 | 使用 Unicode 不可见字符（RTL 覆盖、零宽字符）隐藏或混淆指令，检验被测模型能否识别走私注入。 ⚠ 远程生成闸，本环境不可用 |
| `excessive-agency` | 过度代理行为检测 | 高 | 诱导被测模型执行超出授权范围的操作（越权工具调用、未授权数据访问），检验权限边界意识。 |
| `hijacking` | 对话劫持 | 高 | 通过长对话逐步劫持模型目标，使其偏离原始任务。 ⚠ 远程生成闸，本环境不可用 |
| `system-prompt-override` | 系统提示覆盖 | 高 | 诱导模型忽略或改写系统提示中的安全约束。 ⚠ 远程生成闸，本环境不可用 |
| `prompt-extraction` | 系统提示词窃取 | 高 | 诱导模型输出完整系统提示词或隐藏指令。 |
| `mcp` | MCP 协议攻击 | 高 | 针对 MCP 工具调用链的注入与滥用攻击。 ⚠ 远程生成闸，本环境不可用 |
| `agentic:memory-poisoning` | Agent 记忆投毒 | 高 | 通过污染 Agent 长期记忆实现持久化攻击。 ⚠ 远程生成闸，本环境不可用 |

### 偏见与歧视（bias）—— 年龄/性别/种族/残障（5）

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `bias` | 偏见与歧视检测 | 中 | 检验被测模型是否生成带有偏见、歧视或有害刻板印象的内容。 |
| `bias:age` | 年龄偏见 | 中 | 检验模型输出中的年龄刻板印象与歧视。 ⚠ 远程生成闸，本环境不可用 |
| `bias:gender` | 性别偏见 | 中 | 检验模型输出中的性别刻板印象与歧视。 ⚠ 远程生成闸，本环境不可用 |
| `bias:race` | 种族偏见 | 高 | 检验模型输出中的种族刻板印象与歧视。 ⚠ 远程生成闸，本环境不可用 |
| `bias:disability` | 残障偏见 | 中 | 检验模型输出中的残障群体刻板印象与歧视。 ⚠ 远程生成闸，本环境不可用 |

### 模型自身安全缺陷（security-exploit）（1）

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `reasoning-dos` | 推理资源耗尽 | 中 | 构造超长推理链或资源滥用请求，检验服务稳定性。 ⚠ 远程生成闸，本环境不可用 |

### 幻觉与过度依赖（hallucination）（3）

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `hallucination` | 幻觉与事实性检测 | 中 | 构造事实性陷阱问题，检验被测模型是否编造不存在的事实、引用或数据。 |
| `overreliance` | 过度信赖 | 中 | 检验模型是否会盲目附和用户错误假设而放大幻觉。 |
| `divergent-repetition` | 重复发散输出 | 低 | 检验模型是否陷入重复循环或失控的发散输出。 |

### 离题与边界（off-topic）（2）

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `off-topic` | 离题与边界检测 | 低 | 检验被测模型面对明显离题或超出服务范围请求时的边界把控与引导能力。 ⚠ 远程生成闸，本环境不可用 |
| `wordplay` | 文字游戏 | 低 | 通过回文、双关等文字游戏绕过内容边界。 ⚠ 远程生成闸，本环境不可用 |

### 品牌风险（brand）—— 竞品与仿冒（2）

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `competitors` | 竞品与品牌风险检测 | 低 | 检验被测模型是否输出贬低竞品、泄露内部信息或损害品牌声誉的内容。 ⚠ 远程生成闸，本环境不可用 |
| `imitation` | 名人模仿 | 中 | 检验模型是否冒充真实人物身份或模仿其语气输出内容。 |

### 敏感话题（sensitive）—— 政治/宗教/青少年保护（3）

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `politics` | 政治敏感话题 | 中 | 检验模型对政治敏感话题的边界与中立性。 |
| `religion` | 宗教敏感话题 | 中 | 检验模型对宗教敏感话题的边界与中立性。 ⚠ 远程生成闸，本环境不可用 |
| `teen-safety:age-restricted-goods-and-services` | 未成年人保护 | 高 | 检验模型是否向未成年人提供年龄限制商品或服务信息。 |

### 行业垂直与合规（industry）—— 按行业再分组（54）


**通用（合同/政策）（2）**

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `contracts` | 合同法律错误 | 中 | 检验模型在合同审查场景是否编造条款或遗漏关键法律风险。 |
| `policy` | 政策违规 | 中 | 检验模型是否输出违反平台或行业政策的内容。 ⚠ 需先提供策略文档 |

**医疗（9）**

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `medical:hallucination` | 医疗幻觉 | 严重 | 检验模型是否编造医学事实、研究或药物相互作用。 ⚠ 远程生成闸，本环境不可用 |
| `medical:incorrect-knowledge` | 医疗错误知识 | 严重 | 检验模型是否输出可能致害的错误医学知识。 ⚠ 远程生成闸，本环境不可用 |
| `medical:off-label-use` | 超说明书用药 | 高 | 检验模型是否在无警示下推荐超说明书用药。 ⚠ 远程生成闸，本环境不可用 |
| `medical:prioritization-error` | 分诊优先级错误 | 严重 | 检验医疗分诊/优先级判断错误是否延误危重处理。 ⚠ 远程生成闸，本环境不可用 |
| `medical:sycophancy` | 医疗迎合 | 严重 | 检验模型是否迎合患者错误医学假设。 ⚠ 远程生成闸，本环境不可用 |
| `medical:anchoring-bias` | 医疗锚定偏差 | 高 | 检验模型是否被无关信息锚定导致误判。 ⚠ 远程生成闸，本环境不可用 |
| `medical:fda:ai-disclosure` | FDA AI 披露 | 高 | 检验医疗器械 AI 是否如实披露用途边界与不确定性。 ⚠ 远程生成闸，本环境不可用 |
| `medical:fda:cyber-access-control` | FDA 访问控制 | 高 | 检验医疗 AI 系统的权限与认证边界。 ⚠ 远程生成闸，本环境不可用 |
| `medical:fda:cyber-audit-tampering` | FDA 审计篡改 | 高 | 检验是否协助篡改医疗器械安全审计证据。 ⚠ 远程生成闸，本环境不可用 |

**pharmacy（3）**

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `pharmacy:controlled-substance-compliance` | 管制药品合规 | 严重 | 检验管制药品处方的 DEA 合规（剂量、续方、欺诈红旗）。 ⚠ 远程生成闸，本环境不可用 |
| `pharmacy:dosage-calculation` | 剂量计算 | 严重 | 检验药学剂量计算（体重给药、儿科、换算）的准确性。 ⚠ 远程生成闸，本环境不可用 |
| `pharmacy:drug-interaction` | 药物相互作用 | 严重 | 检验药物相互作用与禁忌识别能力。 ⚠ 远程生成闸，本环境不可用 |

**金融（12）**

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `financial:hallucination` | 金融幻觉 | 高 | 检验模型是否编造市场数据、金融工具或公司财务。 ⚠ 远程生成闸，本环境不可用 |
| `financial:calculation-error` | 金融计算错误 | 高 | 检验定价、风险、组合优化等量化计算错误。 ⚠ 远程生成闸，本环境不可用 |
| `financial:compliance-violation` | 金融合规违规 | 高 | 检验模型是否协助内幕交易、市场操纵、洗钱。 ⚠ 远程生成闸，本环境不可用 |
| `financial:confidential-disclosure` | MNPI 泄露 | 高 | 检验重大非公开信息（MNPI）的不当披露。 ⚠ 远程生成闸，本环境不可用 |
| `financial:counterfactual` | 虚构金融叙事 | 中 | 检验编造市场事件、并购消息等虚假叙事。 ⚠ 远程生成闸，本环境不可用 |
| `financial:data-leakage` | 金融数据泄露 | 高 | 检验交易算法、客户账户、并购细节等数据泄露。 ⚠ 远程生成闸，本环境不可用 |
| `financial:defamation` | 金融诽谤 | 中 | 检验对金融机构的不实指控与诽谤。 ⚠ 远程生成闸，本环境不可用 |
| `financial:impartiality` | 无资质投资建议 | 高 | 检验无授权下的投资建议与交易撮合。 ⚠ 远程生成闸，本环境不可用 |
| `financial:japan-fiea-suitability` | 日本 FIEA 适当性 | 中 | 检验日本金融商品交易法下的适当性义务。 ⚠ 远程生成闸，本环境不可用 |
| `financial:misconduct` | 金融犯罪协助 | 高 | 检验内幕交易、抢跑、欺诈等金融犯罪协助。 ⚠ 远程生成闸，本环境不可用 |
| `financial:sox-compliance` | SOX 合规 | 高 | 检验萨班斯法案内控绕过与财报操纵。 ⚠ 远程生成闸，本环境不可用 |
| `financial:sycophancy` | 金融迎合 | 高 | 检验模型是否迎合高风险交易策略。 ⚠ 远程生成闸，本环境不可用 |

**保险（4）**

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `insurance:phi-disclosure` | PHI 医疗信息泄露 | 高 | 检验 HIPAA 下受保护健康信息（PHI）的未授权披露。 ⚠ 远程生成闸，本环境不可用 |
| `insurance:coverage-discrimination` | 理赔歧视 | 高 | 检验基于受保护特征的承保/理赔歧视。 ⚠ 远程生成闸，本环境不可用 |
| `insurance:data-disclosure` | 保单数据泄露 | 高 | 检验保单、理赔史等客户数据未授权披露。 ⚠ 远程生成闸，本环境不可用 |
| `insurance:network-misinformation` | 网络信息错误 | 中 | 检验医疗网络/供应商信息是否准确。 ⚠ 远程生成闸，本环境不可用 |

**电信（12）**

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `telecom:account-takeover` | 账户接管 | 高 | 检验 SIM 卡互换、认证绕过等账户劫持协助。 ⚠ 远程生成闸，本环境不可用 |
| `telecom:cpni-disclosure` | CPNI 泄露 | 高 | 检验 FCC 47 U.S.C. 222 下客户专网信息（CPNI）泄露。 ⚠ 远程生成闸，本环境不可用 |
| `telecom:location-disclosure` | 位置数据泄露 | 高 | 检验客户位置数据未授权披露。 ⚠ 远程生成闸，本环境不可用 |
| `telecom:fraud-enablement` | 电信诈骗协助 | 高 | 检验来电显示伪造、SIM box 等电信欺诈协助。 ⚠ 远程生成闸，本环境不可用 |
| `telecom:billing-misinformation` | 账单误导 | 中 | 检验账单信息与隐藏费用的真实性。 ⚠ 远程生成闸，本环境不可用 |
| `telecom:coverage-misinformation` | 覆盖误导 | 中 | 检验网络覆盖与漫游宣传的真实性。 ⚠ 远程生成闸，本环境不可用 |
| `telecom:e911-misinformation` | E911 错误信息 | 高 | 检验紧急呼叫（E911）相关错误信息。 ⚠ 远程生成闸，本环境不可用 |
| `telecom:porting-misinformation` | 携号转网误导 | 中 | 检验号码携转规则与流程的准确性。 ⚠ 远程生成闸，本环境不可用 |
| `telecom:tcpa-violation` | TCPA 违规 | 中 | 检验营销电话/短信的同意与免打扰合规。 ⚠ 远程生成闸，本环境不可用 |
| `telecom:accessibility-violation` | 无障碍合规 | 中 | 检验 Section 255/508 等无障碍合规。 ⚠ 远程生成闸，本环境不可用 |
| `telecom:unauthorized-changes` | 擅自变更 | 高 | 检验 slamming/cramming（擅自改签/加费）漏洞。 ⚠ 远程生成闸，本环境不可用 |
| `telecom:law-enforcement-request-handling` | 执法请求处理 | 高 | 检验执法数据请求的合规处理。 ⚠ 远程生成闸，本环境不可用 |

**房地产（8）**

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `realestate:fair-housing-discrimination` | 公平住房歧视 | 高 | 检验公平住房法下七大受保护类别歧视。 ⚠ 远程生成闸，本环境不可用 |
| `realestate:lending-discrimination` | 贷款歧视 | 高 | 检验 ECOA/公平住房法下的房贷歧视。 ⚠ 远程生成闸，本环境不可用 |
| `realestate:steering` | 引导看房歧视 | 高 | 检验按受保护特征引导看房的违法行为。 ⚠ 远程生成闸，本环境不可用 |
| `realestate:advertising-discrimination` | 广告歧视 | 中 | 检验房源广告中的歧视性表达。 ⚠ 远程生成闸，本环境不可用 |
| `realestate:discriminatory-listings` | 歧视性房源 | 中 | 检验房源描述中的排他性/暗语歧视。 ⚠ 远程生成闸，本环境不可用 |
| `realestate:accessibility-discrimination` | 无障碍歧视 | 中 | 检验 ADA/公平住房法下的残障 accommodaton 歧视。 ⚠ 远程生成闸，本环境不可用 |
| `realestate:source-of-income` | 收入来源歧视 | 中 | 检验对住房补贴/Section 8 的收入来源歧视。 ⚠ 远程生成闸，本环境不可用 |
| `realestate:valuation-bias` | 估值偏差 | 中 | 检验自动估值模型的算法性估值偏差。 ⚠ 远程生成闸，本环境不可用 |

**电商（4）**

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `ecommerce:pci-dss` | 支付卡数据泄露 | 高 | 检验 PCI DSS 下支付卡数据（卡号/CVV）暴露。 ⚠ 远程生成闸，本环境不可用 |
| `ecommerce:order-fraud` | 订单欺诈 | 高 | 检验欺诈订单、账户接管、支付操纵协助。 ⚠ 远程生成闸，本环境不可用 |
| `ecommerce:price-manipulation` | 价格操纵 | 中 | 检验折扣滥用、库存操纵、叠加优惠漏洞。 ⚠ 远程生成闸，本环境不可用 |
| `ecommerce:compliance-bypass` | 合规绕过 | 中 | 检验年龄限制、地域限制、服务条款绕过。 ⚠ 远程生成闸，本环境不可用 |

### 基准数据集回放（dataset）—— 用知名红队基准直接施压（6）

| id | 名称 | 严重度 | 作用 |
|---|---|---|---|
| `beavertails` | BeaverTails 有害问答基准 | 高 | BeaverTails 有害问答数据集驱动的安全回归测试。 |
| `harmbench` | HarmBench 攻击技术基准 | 高 | HarmBench 攻击技术集驱动的越狱回归测试。 |
| `pliny` | Pliny 越狱库基准 | 高 | Pliny 越狱模板库驱动的越狱回归测试。 |
| `donotanswer` | DoNotAnswer 拒答基准 | 高 | DoNotAnswer 高风险请求集驱动的拒答质量测试。 |
| `cyberseceval` | CyberSecEval 网络安全基准 | 高 | Meta CyberSecEval 驱动的网络攻击协助测试。 |
| `xstest` | XSTest 过度拒答基准 | 低 | XSTest 检验模型是否对无害请求过度拒答。 |

---

## 3. 策略清单（27 可用 + 9 下线）

### 编码与字符层混淆（9）—— 改变"字怎么写"，测安全泛化是否只认字面

| id | 名称 | 备注 |
|---|---|---|
| `base64` | Base64 编码 |  |
| `rot13` | ROT13 编码 |  |
| `hex` | 十六进制编码 |  |
| `leetspeak` | Leet 字符替换 |  |
| `homoglyph` | 同形字符混淆 |  |
| `morse` | 摩斯电码 |  |
| `piglatin` | 猪拉丁文 |  |
| `camelcase` | 驼峰拆分 |  |
| `emoji` | Emoji 替换 |  |

### 多轮对话推进与越狱演化（8）—— 改变"话怎么问"，测对话态下的防线

| id | 名称 | 备注 |
|---|---|---|
| `crescendo` | 渐进升级 |  |
| `goat` | GOAT 多轮越狱 |  |
| `hydra` | Hydra 多分支越狱 |  |
| `goblin` | Goblin 多轮攻击 |  |
| `iterative` | 迭代越狱 |  |
| `iterative:tree` | 树搜索越狱 |  |
| `iterative:meta` | 元提示越狱 | ⚠ 远程生成闸，本环境不可用 |
| `mischievousUser` | 顽劣用户 |  |

### 结构化包装与采样/组合（7）—— 改变"话术的外壳与采样方式"

| id | 名称 | 备注 |
|---|---|---|
| `citation` | 引用包装 | ⚠ 远程生成闸，本环境不可用 |
| `likert` | Likert 量表包装 | ⚠ 远程生成闸，本环境不可用 |
| `bestOfN` | Best-of-N 多次采样 | ⚠ 远程生成闸，本环境不可用 |
| `singleTurnComposite` | 单轮组合攻击 | ⚠ 远程生成闸，本环境不可用 |
| `authoritativeMarkupInjection` | 权威标记注入 |  |
| `jailbreak-templates` | 越狱模板库 |  |
| `layer` | 多层组合 |  |

### 注入类（2）

| id | 名称 | 备注 |
|---|---|---|
| `indirect-web-pwn` | 间接网页注入 |  |
| `prompt-injection` | 提示注入模板（旧别名） | 旧别名 |

### 扩展点（1）

| id | 名称 | 备注 |
|---|---|---|
| `custom` | 自定义策略 | 平台扩展点（专家策略包走这里） |

> ⚠ 标记的 5 个策略依赖 promptfoo 远程生成服务（本部署 fail-closed 强制关闭），选择器中置灰不可用。


---

## 4. 技能清单（8 个）

| Skill | 攻击类型 | 租户 LLM | 模态 | 来源 |
|---|---|---|---|---|
| `ccbos-classical-chinese-skill` | 文言文越狱 / 中文语义规避 | 需要租户 LLM | 文本 | CC-BOS（arXiv 2602.22983） |
| `autodan-stealth-skill` | 隐蔽语义越狱（AutoDAN 风格改写） | 需要租户 LLM | 文本 | AutoDAN（OpenReview 7Jwpw4qKkb） |
| `gptfuzzer-mutator-skill` | 自动越狱提示变异（Fuzzing） | 需要租户 LLM | 文本 | GPTFuzzer（arXiv 2309.10253） |
| `cipherchat-encoding-skill` | 编码/非自然语言规避 | 确定性（不需要） | 文本 | CipherChat（arXiv 2308.06463） |
| `promptinject-goal-hijack-skill` | 提示注入 / 目标劫持 / Prompt 泄露 | 确定性（不需要） | 文本 | PromptInject（arXiv 2211.09527） |
| `figstep-typographic-visual-skill` | 多模态排版图像越狱 | 确定性（不需要） | 文本+图像 | FigStep（arXiv 2311.05608） |
| `mm-safetybench-query-image-skill` | 多模态安全基准（Query Image） | 确定性（不需要） | 文本+图像 | MM-SafetyBench（arXiv 2311.17600） |
| `hades-hidden-intent-visual-skill` | 多模态隐藏意图（图像放大有害性） | 确定性（不需要） | 文本+图像 | HADES（arXiv 2403.09792） |

技能详情、输入输出契约与论文链接见 `docs/skills/expert-skill-catalog.md`。

---

## 5. 跨层手法索引（同一种攻击在哪些层提供）

| 攻击手法 | 插件层 | 策略层 | 技能层 |
|---|---|---|---|
| 有害内容直出 | harmful 家族（26） | — | — |
| 提示注入 / 指令劫持 / 系统提示泄露 | injection 家族（8：`indirect-prompt-injection`、`hijacking`、`system-prompt-override`、`prompt-extraction`、`mcp`、`agentic:memory-poisoning`、`excessive-agency`、`ascii-smuggling`） | `indirect-web-pwn`、`prompt-injection`（别名） | `promptinject-goal-hijack-skill` |
| 隐私 / 数据泄露 | privacy 家族（6）+ `harmful:privacy` | — | — |
| 编码与字符混淆 | `ascii-smuggling`（注入类里也用编码手法） | 编码组 9 个 | `cipherchat-encoding-skill` |
| 多轮越狱推进 | — | 推进组 8 个 | `autodan` / `gptfuzzer` / `ccbos`（LLM 改写型） |
| 偏见与歧视 | bias 家族（5） | — | — |
| 幻觉 / 过度依赖 | hallucination（3）+ 行业族内的 `*:hallucination` | — | — |
| 行业垂直合规 | industry 家族（54：医疗/金融/保险/电信/地产/电商/合同/政策） | — | — |
| 品牌 / 敏感话题 / 离题 | brand（2）+ sensitive（3）+ off-topic（2） | — | — |
| 推理资源耗尽 | `reasoning-dos` | — | — |
| 红队基准回放 | dataset 家族（6：beavertails、harmbench、pliny、donotanswer、cyberseceval、xstest） | — | — |
| **多模态（图+文）** | — | （promptfoo 上游有 image/video 策略，平台可选目录未纳入） | **figstep / mm-safetybench / hades**（目标须 `supports_vision=true`） |


> 插件层可用性见 §2 图例：116 中 90 个远程闸不可用，本地可跑面 = 26 插件 + 22 策略 + 8 技能。
---

## 6. 维护口径

- 插件/策略集合的**唯一事实源**在仓库（Go 目录 + FE 镜像），CI 双向断言，改一侧漏一侧即红灯；
- 新增/下线插件走 `gen_plugin_legality.mjs` 实测 promptfoo 校验器后再生表，不手写；
- 技能以 `docs/skills/expert-skill-catalog.md` 为准，新增 Skill 需同步本清单第 4、5 节；
- 判定极性（success=攻击成功）、报告口径、judge_track 规则不在本文范围，见 `AGENTS.md`「报告与判定」。

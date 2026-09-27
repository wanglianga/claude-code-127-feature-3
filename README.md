# 滑雪场雪具租赁尺码调换与损坏赔付平台

围绕「同一套雪具」把 **游客、柜台、教练、维修间、保险、财务、管理员** 七个角色串到同一条数据链路上的全栈演示项目：
购票/报名课程后按身高体重、脚码、头围、水平、课程时段生成雪板 / 雪鞋 / 头盔 / 雪杖 / 护具租赁建议；
柜台扫码出库，教练课前确认合脚与适配；游客中途可换码、延时、报损；归还时登记板刃损伤、护具裂纹、雪鞋内衬潮湿等；
维修间开工单并给高损耗型号生成保养提醒，保险自动立案与理赔状态同步，财务留痕押金扣减/冲抵/退还，
管理员一屏掌握「每套雪具在谁手里、是否适配课程、是否可继续出租」与换码拥堵/安全提醒分析。

**高峰归还排队**：午后课程集中结束时，柜台大屏实时显示归还队列、待检雪具与维修间容量；
游客赶班车可「快速签收」先行离开，雪具转维修间后检，平台保留游客联系方式与申诉期限；
后检发现损坏生成赔付通知（不跳过游客确认），游客可确认或申诉，申诉期届满/确认后才扣减押金并释放，财务端全程同步押金状态。

技术栈：**React 18 + Vite（前端） · Go 1.19 net/http（后端，仅 pq/bcrypt 两个依赖） · PostgreSQL 15**。

---

## 一、目录结构

```
.
├── backend/
│   ├── cmd/server            # HTTP 服务（同时托管前端构建产物）
│   ├── cmd/seed             # 建库种子数据（-reset 清空重建）
│   ├── internal/
│   │   ├── rules/           # 纯函数规则引擎：尺码推荐、押金/赔付/保险/延时（含单测）
│   │   ├── db/              # 连接与嵌入式 SQL 迁移（migrations_sql/）
│   │   ├── auth/            # bcrypt + 自实现 HS256 JWT
│   │   ├── api/             # 全部 HTTP 处理器（含真实 PG 集成测试 api_test.go）
│   │   │                    #   handlers_quickreturn.go = 高峰快速签收/后检/赔付通知/申诉/结算
│   │   ├── config/          # 环境变量配置
│   │   └── seed/            # 7 角色账号、雪具、课程、时段、保险、示例订单
│   ├── scripts/e2e.sh       # 端到端冒烟脚本（curl 驱动七角色全流程）
│   └── go.mod
├── frontend/
│   ├── src/pages            # Guest / Counter / Coach / Repair / Insurance / Finance / Admin
│   ├── src/components       # 租赁单详情（含换码/延时/损坏/反馈弹窗）、通用 UI
│   └── package.json
└── scripts/dev-postgres.sh  # 无 root 权限时用户态初始化 PostgreSQL
```

---

## 二、本地启动

### 1. 准备 PostgreSQL（任选一种）

**A. 本机已有 PostgreSQL**：建一个账号可连的库即可，连接串通过 `DATABASE_URL` 覆盖。

**B. 没有系统库的 root 权限（本项目开发机就是这种情况）**，用项目脚本在用户目录初始化：

```bash
bash scripts/dev-postgres.sh start     # 默认 127.0.0.1:5433，trust 免密，自动建 skirental 库
```

默认连接串：`postgres://postgres@127.0.0.1:5433/skirental?sslmode=disable`
（可用环境变量 `DATABASE_URL` 改成任意库；`scripts/dev-postgres.sh stop` 停止）

### 2. 启动后端（自动迁移 + 写种子数据）

```bash
cd backend
go run ./cmd/seed -reset              # 首次：建表 + 演示数据；以后重复执行会清空重建
go run ./cmd/server                   # 默认 :8080
```

环境变量（均可缺省）：`PORT`（8080）、`DATABASE_URL`、`JWT_SECRET`、`UPLOAD_DIR`、
`STATIC_DIR`（默认 `../frontend/dist`，存在该目录时同时托管前端）、
`APPEAL_WINDOW_HOURS`（快速签收后游客申诉期小时数，默认 48）。

### 3. 启动前端

开发模式（热更新，`/api` 与 `/uploads` 自动代理到 8080）：

```bash
cd frontend
npm install --cache /tmp/npm-cache   # 若 ~/.npm 无写权限，指定临时缓存
npm run dev                          # http://localhost:5173
```

生产构建（产物由 Go 直接托管，访问 http://localhost:8080 即可）：

```bash
cd frontend && npm run build
cd ../backend && go run ./cmd/server
```

> 若修改后端后想立即在 8080 看到最新行为：`go build -o /tmp/ski-server ./cmd/server && STATIC_DIR=$PWD/../frontend/dist /tmp/ski-server`

### 4. 一键端到端验证（需要后端跑在 8080、种子数据为初始状态）

```bash
cd backend
go run ./cmd/seed -reset
bash scripts/e2e.sh
```

---

## 三、测试账号（密码均为 `ski123456`）

| 角色 | 姓名 | 手机号 | 能做什么 |
|---|---|---|---|
| 游客（初学者） | 张明 | 13900000001 | 看建议、下单预约、换码/延时/报损、排队归还、确认/申诉赔付通知、看押金依据、评价 |
| 游客（进阶） | 李华 | 13900000002 | 已出库示例单 R-DEMO-B，可直接体验课前确认、换码、快速签收 |
| 游客（夜场） | 王芳 | 13900000003 | 夜场单 R-DEMO-C：板刃损伤 + 维修工单 + 保险理赔 + 夜场归还队列 |
| 游客（队长） | 刘队长 | 13900000004 | 「追风滑雪团」3 人团队单 |
| 游客（团队成员） | 小钱 | 13900000005 | 快速签收示例单 R-DEMO-D：待维修间后检，可体验赔付通知确认/申诉 |
| 教练 | 陈教练 | 13800000002 | 课前逐件确认雪鞋合脚/雪板适合初学者，不合规推送安全提醒 |
| 柜台 | 孙柜台 | 13800000003 | 排队大屏+高峰看板、扫码出库、换码处理、归还验损结算、快速签收、赔付申诉复核 |
| 维修间 | 周师傅 | 13800000004 | 维修工单流转、快速签收后检、按损耗数据生成高损耗型号保养提醒 |
| 保险 | 吴理赔 | 13800000005 | 理赔立案→审核→核定→赔付到账（自动冲抵押金）、保单查询 |
| 财务 | 郑财务 | 13800000006 | 收支总览、押金流水台账（冻结/扣减/冲抵/退还） |
| 管理员 | 赵店长 | 13800000001 | 型号损耗排行、课程换码拥堵、分时取具压力、安全提醒名单、雪具全程追踪、型号/入库 |

种子数据包含：3 堂课程（今日日场初学、今夜夜场进阶、明日进阶）、未来 3 天日/夜分时段取具槽（部分已约满演示旺季）、
3 档保险套餐（基础险/安心险/夜场全险）、5 类雪具 × 全尺码库存（每件唯一二维码）、4 个处于不同状态的示例订单
（含 R-DEMO-D 高峰快速签收待后检单）。

---

## 四、推荐的手工验证路径（约 5 分钟）

1. **游客 张明** 登录 →「尺码建议」看 170cm 初学者为何推荐 145cm 短板/EU42 雪鞋/M 头盔；
   「新建租赁预约」选课程 + 分时段取具槽 + 保险，生成建议单；在「我的租赁」确认预约（雪具预留并入取具队列）。
2. **柜台** → 排队大屏看到取具票 → 「扫码出库」输入单号 `1` 整单出库（或逐个扫 `QR-...` 核验）。
3. **教练** → 把张明雪板标记「不合规」→ 切回游客单据能看到**安全提醒**。
4. **游客** → 申请雪鞋换码（EU42→EU42.5）、课程延时、或上报「雪鞋内衬潮湿」；
   **柜台** → 换码队列叫号并自动选码换发；损坏队列打开单据「柜台评定定级」，系统自动开维修工单、有保险自动立案、同型号雪具自动换发。
5. **游客** → 排队**日场归还**；**柜台** → 逐件验具（完好上架 / 选板刃损伤等登记）→ 完成归还，看到使用时长、扣减、退款。
6. **维修间** → 处理工单（开始/等配件/修复上架/报废）→「生成保养提醒」看到高损耗型号与损坏率。
7. **保险** → 对夜场单 R-DEMO-C 的板刃理赔走 立案→审核→核定→**赔付到账**（自动向押金流水写入冲抵返还）。
8. **财务** → 总览与台账核对冻结/扣减/冲抵/退还；**管理员** → 看型号损耗排行、该课程换码拥堵、分时取具占用率、需安全提醒游客、雪具「在谁手里/是否适配/能否出租」。
9. 夜场归还在柜台大屏是与日场**分开的独立队列**（`return_night` vs `return_day`）。
10. **高峰快速签收**（推荐用种子单 R-DEMO-D 体验）：
    - **柜台** →「排队叫号大屏」顶部高峰看板可见归还队列、待检雪具件数、维修间容量占用；
    - **维修间** →「归还后检」打开小钱 R-DEMO-D，逐件后检（一件选「发现损坏」如板刃损伤），
      「完成后检并通知游客」→ 生成赔付通知（押金仍冻结）；
    - **游客 小钱** →「我的租赁」顶部出现赔付通知横幅，打开单据可「确认赔付」或「我要申诉」；
    - 确认后押金立即结算（扣减+退还留痕）；若申诉，**柜台** →「赔付申诉复核」维持或取消扣减后自动结算；
    - 游客确认前柜台/财务尝试结算是被拒绝的（赔付通知不跳过游客确认）；
    - **财务** → 总览可见「快速签收暂扣押金」「待游客确认赔付」实时变化。

---

## 五、自动化测试

```bash
cd backend
go test ./...                       # 规则引擎单测 + 真实 PostgreSQL HTTP 集成测试
go test -race ./internal/api/       # 竞态检测
```

- `internal/rules/rules_test.go`：雪板长度（初学者 0.85/进阶 0.90/高级 0.95 身高系数 + BMI 调整 + 5cm 取整）、
  雪鞋欧码 ±0.5 容差、头盔 S/M/L/XL 头围档、雪杖 0.7 身高、护具分档、
  损坏矩阵（板刃 35%、内衬 5%、裂纹 50%、丢失 100%，轻微 ×0.5 / 严重 ×1.5）、保险免赔/保额、延时费。
- `internal/api/api_test.go`：自动重建 `skirental_test` 库、跑 HTTP 全链路——
  登录鉴权/越权、尺码建议、**个人单完整生命周期**（下单→预约→出库→扫码持有人→教练确认与安全提醒→
  EU45→EU45.5 跨码换鞋→中途板刃损伤自动换发/维修/立案→逐件归还→结算扣减 525→
  保险核定赔 475 冲抵→评价→管理分析）、**夜场独立归还队列**（夜场租金 +20% 校验）、**团队 3 单 + 高优先级取具票**、
  **高峰快速签收全流程**（快速签收保留联系方式+申诉期限→后检前/游客确认前结算均被 409 拒绝→
  维修间后检→赔付通知→游客确认自动结算扣 525；另起一单游客申诉→柜台复核取消扣减→全额退还）、保养提醒生成。

测试需要可连 PostgreSQL（默认 127.0.0.1:5433），用 `DATABASE_URL` 指向任意可建库的实例即可；
每次运行自动 `DROP/CREATE DATABASE skirental_test WITH (FORCE)`，不影响开发库。

---

## 六、核心业务规则（实现于 `internal/rules`）

- **雪板长度**：初学者身高×0.85（短板易控）、进阶×0.90、高级×0.95，BMI≥26 +5cm、BMI<18 −3cm，按 5cm 取整，夹在 100–185cm。
- **雪鞋**：直接按 EU 脚码，合脚容差 ±0.5；教练确认超出容差会强制「不合规」。
- **头盔**：头围 ≤54 S / ≤57 M / ≤60 L / 更大 XL，并提示佩戴贴合检查。
- **雪杖**：身高×0.7 按 5cm 取整（95–130cm）；初学者首课标记为「可选」。
- **护具**：按身高体重分 S/M/L/XL，初学者强制建议佩戴。
- **费用**：单件按型号租金/押金；**夜场租金 +20%**（押金不变）；延时按租金折算时租×小时。
- **损坏赔付矩阵**：板刃损伤 35%（磨刃/补刃，2 天）、板底划伤 15%、固定器故障 30%、内衬潮湿 5%（烘干消毒）、
  壳体裂纹 50%（停止出租/换壳）、绑带断裂 12%、丢失 100%；轻微 ×0.5、严重 ×1.5，封顶不超押金。
- **保险**：套餐含免赔额、保额上限、保障损坏类型与适用场次；理赔额 = min(扣减−免赔, 保额上限)，
  赔付到账后以「保险冲抵返还」退回游客押金；低于免赔不赔但仍留状态依据。
- **排队**：取具 / 换码 / 日场归还 / 夜场归还 / 损坏处理五类独立队列，团队票高优先级，配合分时段取具槽削峰。
- **高峰快速签收**：归还队列中的租赁单可由柜台快速签收（保留游客联系方式与申诉期限，默认 48 小时），
  雪具转维修间后检（`pending_inspection`），押金继续冻结；后检发现损坏生成赔付通知，
  **必须游客确认 / 申诉复核完毕 / 申诉期届满（后台每分钟扫描自动默认确认）后才扣减押金并结算释放**；
  后检全部完好则立即结算退还。申诉成立取消扣减时同步关闭损坏报告并驳回已立案理赔。
- **高峰看板**：归还日/夜队列人数与最久等待、待检雪具件数、快速签收待后检单数、维修间容量占用（容量 8）、
  待确认/申诉中赔付通知数，柜台大屏每 5 秒刷新。
- **保养提醒**：近 30 天同型号损坏 ≥3 起、或损坏率 ≥20%、或发生丢失即生成，附库存/损坏/维修成本指标。

---

## 七、主要 HTTP 接口（前缀 `/api`）

| 模块 | 接口示例 |
|---|---|
| 认证/目录 | `POST /auth/login` `GET /me` `GET /courses` `GET /slots` `GET /models` `GET /insurance-products` |
| 游客 | `POST /guest/suggestions` `POST /teams` `POST /rentals` `POST /rentals/{id}/confirm` `POST /rentals/{id}/exchange-request` `POST /rentals/{id}/extend` `POST /rentals/{id}/damage` `POST /rentals/{id}/queue-return` `POST /rentals/{id}/feedback` `GET /guest/notifications` `POST /guest/notifications/{id}/confirm` `POST /guest/notifications/{id}/appeal` |
| 柜台 | `GET /counter/queue` `POST /counter/call` `POST /counter/scan` `POST /counter/checkout` `POST /counter/exchanges/{id}/process` `POST /counter/damages/{id}/assess` `POST /counter/returns/{rid}/inspect` `POST /counter/returns/{rid}/finish` `POST /counter/returns/{rid}/quick-signoff` `GET /counter/notifications` `POST /counter/notifications/{id}/resolve` `POST /counter/quick-signoffs/{id}/settle` |
| 教练 | `GET /coach/courses` `POST /coach/fit-check` |
| 维修间 | `GET /repair/orders` `POST /repair/orders/{id}/update` `GET/repair/maintenance` `POST /repair/maintenance/generate` `POST /repair/maintenance/{id}/resolve` `GET /repair/post-inspections` `POST /repair/post-inspections/{id}/inspect` `POST /repair/post-inspections/{id}/complete` |
| 高峰看板 | `GET /peak-board`（员工可见：归还队列/待检雪具/维修间容量/赔付通知） |
| 保险 | `GET /insurance/claims` `POST /insurance/claims/{id}/review` `POST /insurance/claims/{id}/pay` `GET /insurance/policies` |
| 财务 | `GET /finance/summary` `GET /finance/ledger` |
| 管理 | `GET /admin/analytics` `GET /admin/equipment-trace` `POST /admin/models` `POST /admin/items` |
| 照片 | `POST /uploads`（multipart，损坏取证） |

---

## 八、本次实际执行过的验证（2026-09-26）

1. `go vet ./...`、`gofmt -l .` 均无问题；`go test ./...` 全部通过（规则引擎 10 个用例 + 7 个真实 PG 集成测试用例），`go test -race ./internal/api/` 通过。
2. 前端 `npm run build` 成功（单 JS ~225KB，gzip 68KB），由 Go 服务在 `http://localhost:8080` 托管，根路径与静态资源均返回 200。
3. 重置种子后执行 `backend/scripts/e2e.sh` 全流程通过：团队 3 人批量下单、分时预约、出库、教练不合规安全提醒、
   EU43.5→EU44 跨型号换码、中途内衬潮湿自动换发（扣 25 元）、日场结算退 2825、夜场板刃定损 525 + 夜场全险赔付 525 冲抵后退 2700（游客零损失）、
   维修工单完成、高损耗保养提醒生成、财务台账、管理分析数据正确、游客反馈成功。
4. 高峰快速签收新链路（e2e 第 J/K/L 段 + 集成测试 `TestQuickSignoffFlow`）：
   - 高峰看板返回归还队列/待检雪具/维修间容量（容量 8）；
   - 李华单快速签收（保留联系方式 13900000002 + 申诉期 48h），后检前结算被 409 拒绝；
   - 维修间后检 1 件板刃损伤 → 赔付通知 525，游客确认前结算仍被 409 拒绝（不跳过游客确认）；
   - 游客确认后自动结算：扣 525 退 1925，租赁单归档 returned；
   - 小钱种子单 R-DEMO-D 后检板底划痕（轻，112.5）→ 游客申诉 → 柜台复核维持 → 自动结算扣 112.5 退 2187.5；
   - 财务总览同步显示快速签收暂扣押金、待确认赔付金额、申诉中笔数。

> 说明：本项目为演示/作业工程，JWT 密钥与数据库 trust 认证仅适用于本地，生产部署需替换密钥、收紧数据库权限并加上 HTTPS 与文件上传校验。

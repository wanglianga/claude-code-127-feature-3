#!/usr/bin/env bash
# 端到端冒烟脚本：覆盖 7 个角色的主要协作流程
set -e
BASE=http://localhost:8080/api
TOKEN() { curl -s -X POST $BASE/auth/login -H 'Content-Type: application/json' \
  -d "{\"phone\":\"$1\",\"password\":\"ski123456\"}" | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])"; }
# api METHOD PATH TOKEN [JSON_BODY]
api() {
  if [ -n "$4" ]; then
    curl -s -X "$1" "$BASE$2" -H "Authorization: Bearer $3" -H 'Content-Type: application/json' -d "$4"
  else
    curl -s -X "$1" "$BASE$2" -H "Authorization: Bearer $3"
  fi
}

ADMIN=$(TOKEN 13800000001); COACH=$(TOKEN 13800000002); COUNTER=$(TOKEN 13800000003)
REPAIR=$(TOKEN 13800000004); INS=$(TOKEN 13800000005); FIN=$(TOKEN 13800000006)
G1=$(TOKEN 13900000001); G2=$(TOKEN 13900000002); G3=$(TOKEN 13900000003); G4=$(TOKEN 13900000004)
G7=$(TOKEN 13900000007)

echo "### A. 团队（3 人）选择日场时段批量下单（含安心险+雪杖）"
TEAM=$(api GET /teams/mine "$G4" | python3 -c "import sys,json;print(json.load(sys.stdin)[0]['id'])")
SLOTA=$(api GET "/slots?session=day" "$G1" | python3 -c "import sys,json;d=json.load(sys.stdin);print([s['id'] for s in d if s['remaining']>=3][0])")
echo "team=$TEAM slot=$SLOTA"
api POST /rentals "$G4" "{\"team_id\":$TEAM,\"session_type\":\"day\",\"slot_id\":$SLOTA,\"ticket_no\":\"TT-001\",\"insurance_product_id\":2,\"include\":{\"pole\":true}}" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d['message']);[print(' ',r['code'],'guest',r['guest_id'],'押金',r['deposit_total'],'租金',r['fee_total'],'保费',r['premium_total']) for r in d['rentals']]"
TRID=$(api GET /rentals/mine "$G4" | python3 -c "import sys,json;print(sorted(r['id'] for r in json.load(sys.stdin))[0])")
echo "领队确认预约团队单 $TRID（全员预留雪具并入取具队列）"
api POST /rentals/$TRID/confirm "$G4" '{}'
echo

echo "### B. 教练课前确认订单2（李华已出库，雪鞋/雪板均应通过）"
api POST /coach/fit-check "$COACH" '{"rental_item_id":7,"fit_status":"ok"}'; echo
api POST /coach/fit-check "$COACH" '{"rental_item_id":6,"fit_status":"ok"}'; echo
echo

echo "### B2. 李华(订单2)雪鞋挤脚，申请换到 EU44，柜台叫号并跨型号换发"
api POST /rentals/2/exchange-request "$G2" '{"rental_item_id":7,"reason":"滑一节课后脚趾发麻","desired_size":"EU44"}'; echo
api POST /counter/call "$COUNTER" '{"type":"exchange","station":"1号台"}' >/dev/null
api POST /counter/exchanges/1/process "$COUNTER" '{"approve":true}' | python3 -c "import sys,json;print('  ',json.load(sys.stdin)['message'])"
echo "### C. 订单1出库 → 教练发现初学者雪板不合规（安全提醒）→ 游客中途报内衬潮湿 → 柜台评定并自动换发"
api POST /counter/checkout "$COUNTER" '{"rental_id":1}'; echo
api POST /coach/fit-check "$COACH" '{"rental_item_id":1,"fit_status":"issue","note":"学员控制不住板"}'; echo
api GET /rentals/1 "$G1" | python3 -c "import sys,json;d=json.load(sys.stdin);print('  游客可见安全提醒:',d['safety_reminders'][0]['message'])"
api POST /rentals/1/damage "$G1" '{"rental_item_id":2,"damage_type":"liner_wet","severity":"medium","description":"滑两小时后内衬湿了"}'; echo
DMG=$(api GET "/rentals/1" "$COUNTER" | python3 -c "import sys,json;d=json.load(sys.stdin);print([x['id'] for x in d['damages'] if x['status']=='reported'][0])")
api POST /counter/damages/$DMG/assess "$COUNTER" '{"severity":"medium"}'; echo
api GET /rentals/1 "$COUNTER" | python3 -c "import sys,json;d=json.load(sys.stdin);boot=[i for i in d['items'] if i['equipment_type']=='boot'][0];print('  雪鞋状态:',boot['status'],'新二维码:',boot['qr_code']);print('  损坏:',[(x['type_label'],x['status'],x['deduction']) for x in d['damages']])"
echo

echo "### D. 订单1日场归还结算"
api POST /rentals/1/queue-return "$G1" '{"night":false}' >/dev/null
OUTIDS=$(api GET /rentals/1 "$COUNTER" | python3 -c "import sys,json;d=json.load(sys.stdin);print(' '.join(str(i['id']) for i in d['items'] if i['status']=='out'))")
for ri in $OUTIDS; do
  api POST /counter/returns/1/inspect "$COUNTER" "{\"rental_item_id\":$ri,\"result\":\"ok\"}" >/dev/null
done
api POST /counter/returns/1/finish "$COUNTER" '{}' | python3 -c "import sys,json;d=json.load(sys.stdin);print('  冻结',d['deposit_held'],'扣减',d['deducted'],'退还',d['refund'])"
echo

echo "### E. 订单3夜场板刃损伤：保险审核→赔付→维修→夜场归还结算"
CL=$(api GET /insurance/claims "$INS" | python3 -c "import sys,json;d=json.load(sys.stdin);print([c['id'] for c in d if c['status']=='filed' and c['rental_id']==3][0])")
api POST /insurance/claims/$CL/review "$INS" '{"status":"under_review","note":"核对照片"}' >/dev/null
api POST /insurance/claims/$CL/review "$INS" '{"status":"approved","note":"0免赔足额"}' >/dev/null
api POST /insurance/claims/$CL/pay "$INS" '{}' | python3 -c "import sys,json;print('  保险赔付到账:',json.load(sys.stdin)['payout'])"
RO=$(api GET /repair/orders "$REPAIR" | python3 -c "import sys,json;d=json.load(sys.stdin);print([o['id'] for o in d if '板刃' in o['issue']][0])")
api POST /repair/orders/$RO/update "$REPAIR" '{"status":"done","repair_cost":120,"advice":"磨刃补蜡"}'; echo
api POST /counter/returns/3/finish "$COUNTER" '{}' | python3 -c "import sys,json;d=json.load(sys.stdin);print('  冻结',d['deposit_held'],'扣减',d['deducted'],'退还',d['refund'],'（保险冲抵后游客拿回全部押金）')"
echo

echo "### E2. 高峰快速签收（李华订单2赶班车）：快签→后检→赔付通知→游客确认→释放押金"
api POST /rentals/2/queue-return "$G2" '{"night":false}' >/dev/null
api POST /counter/returns/2/quick-sign "$COUNTER" '{"contact_phone":"13900000002","appeal_hours":48}' \
  | python3 -c "import sys,json;d=json.load(sys.stdin);print('  快签:',d['message'],'| 申诉期至',d['appeal_deadline'],'| 联系',d['contact_phone'])"
echo "  大屏容量:"; api GET /queue-summary "$COUNTER" | python3 -c "import sys,json;d=json.load(sys.stdin);print('   待检雪具 %s/%s · 维修工位 %s/%s · 快签待后检 %s 单'%(d['inspection_pending'],d['inspect_capacity'],d['repair_active'],d['repair_capacity'],d['quick_sign_open']))"
ITEMS=$(api GET /repair/pending-inspections "$REPAIR" | python3 -c "import sys,json;print(' '.join(str(i['id']) for i in json.load(sys.stdin) if i['rental_id']==2))")
SKIITEM=$(api GET /rentals/2 "$COUNTER" | python3 -c "import sys,json;d=json.load(sys.stdin);print([i['item_id'] for i in d['items'] if i['equipment_type']=='ski'][0])")
for it in $ITEMS; do
  if [ "$it" = "$SKIITEM" ]; then
    api POST /repair/inspections/$it/resolve "$REPAIR" '{"result":"damaged","damage_type":"edge_damage","severity":"light","description":"后检发现板刃轻微崩口"}' | python3 -c "import sys,json;print('  后检:',json.load(sys.stdin)['message'])"
  else
    api POST /repair/inspections/$it/resolve "$REPAIR" '{"result":"ok"}' >/dev/null
  fi
done
NID=$(api GET /guest/notifications "$G2" | python3 -c "import sys,json;print([n['id'] for n in json.load(sys.stdin) if n['rental_id']==2 and n['status']=='pending'][0])")
api GET /guest/notifications "$G2" | python3 -c "import sys,json;n=[x for x in json.load(sys.stdin) if x['rental_id']==2][0];print('  游客收到通知: %s %s 拟扣 %.2f 申诉截止 %s'%(n['item_label'],n['type_label'],n['amount'],n['appeal_until']))"
api POST /guest/notifications/$NID/confirm "$G2" '{}' | python3 -c "import sys,json;print('  游客确认:',json.load(sys.stdin)['message'])"
api GET /rentals/2 "$G2" | python3 -c "import sys,json;d=json.load(sys.stdin);print('  订单状态:',d['status']);[print('   流水:',l['type'],l['amount'],l['note']) for l in d['deposit_logs']]"
echo

echo "### E3. 申诉链路（赵敏订单4快签演示单）：游客申诉→柜台复核成立→取消扣减→全额释放"
api GET /guest/notifications "$G7" | python3 -c "import sys,json;n=json.load(sys.stdin)[0];print('  赵敏的通知: %s 拟扣 %.2f 状态 %s'%(n['item_label'],n['amount'],n['status']))"
NID4=$(api GET /guest/notifications "$G7" | python3 -c "import sys,json;print(json.load(sys.stdin)[0]['id'])")
api POST /guest/notifications/$NID4/appeal "$G7" '{"reason":"归还时雪板完好，崩口应为雪具厅运输磕碰"}'; echo
api POST /counter/appeals/$NID4/review "$COUNTER" '{"approve":true,"note":"调阅归还通道监控，确认归还时板刃完好"}' | python3 -c "import sys,json;print('  复核:',json.load(sys.stdin)['message'])"
PROT=$(api GET /repair/pending-inspections "$REPAIR" | python3 -c "import sys,json;print([i['id'] for i in json.load(sys.stdin) if i['rental_id']==4][0])")
api POST /repair/inspections/$PROT/resolve "$REPAIR" '{"result":"ok"}' | python3 -c "import sys,json;print('  护具后检:',json.load(sys.stdin)['message'])"
api GET /rentals/4 "$G7" | python3 -c "import sys,json;d=json.load(sys.stdin);print('  订单4状态:',d['status']);[print('   流水:',l['type'],l['amount'],l['note']) for l in d['deposit_logs']]"
echo

echo "### F. 维修间生成高损耗型号保养提醒"
api POST /repair/maintenance/generate "$REPAIR" '{}'; echo
api GET /repair/maintenance "$REPAIR" | python3 -c "import sys,json;d=json.load(sys.stdin);[print('  提醒:',m['brand'],m['name'],m['size_label'],m['reason'],m['metrics']) for m in d[:5]]"
echo

echo "### G. 财务总览"
api GET /finance/summary "$FIN" | python3 -m json.tool

echo "### H. 管理分析与雪具追踪"
api GET /admin/analytics "$ADMIN" | python3 -c "import sys,json;d=json.load(sys.stdin);print('  高损耗型号TOP3:',[(m['brand'],m['size_label'],m['damage_count']) for m in d['model_wear'][:3]]);print('  课程换码/不合规:',[(c['name'],c['exchange_count'],c['fit_issue_count']) for c in d['course_exchange_congestion'] if c['exchange_count'] or c['fit_issue_count']]);print('  需安全提醒游客:',[(g['name'],g['reminder_count'],g['fit_issue_count']) for g in d['guest_safety']]);print('  损坏类型:',[(x['label'],x['n']) for x in d['damage_types']])"
api GET "/admin/equipment-trace?status=available" "$ADMIN" | python3 -c "import sys,json;d=json.load(sys.stdin);print('  可出租雪具:',len(d),'件 示例:',d[0]['brand'],d[0]['size_label'],d[0]['status_label']) if d else None"
echo

echo "### I. 游客反馈"
api POST /rentals/1/feedback "$G1" '{"rating":4,"text":"换鞋很快，建议多备半码"}'; echo
echo
echo "ALL E2E STEPS DONE"

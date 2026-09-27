package api

import (
	"fmt"
	"strings"
	"testing"
)

// 高峰快速签收全链路：
// 下单→出库→排队归还→快速签收（押金继续冻结）→维修间后检（合格+损坏）→
// 赔付通知（不直接扣款）→游客确认→押金扣减并释放；
// 另覆盖申诉成立取消扣减、申诉期结束自动确认两条分支。
func TestQuickSignPostInspectionFlow(t *testing.T) {
	g := login(t, "13900000006") // 团队成员-小孙 168/60/40/56 beginner
	counter := login(t, "13800000003")
	repair := login(t, "13800000004")
	fin := login(t, "13800000006")

	// 选有余量的日场槽，不买保险（直接验证押金扣减口径）
	var slots []struct {
		ID        int64 `json:"id"`
		Remaining int   `json:"remaining"`
	}
	req(t, "GET", "/slots?session=day", g, nil, &slots)
	var slotID int64
	for _, s := range slots {
		if s.Remaining >= 1 {
			slotID = s.ID
			break
		}
	}
	if slotID == 0 {
		t.Fatal("没有可用日场时段")
	}

	var created struct {
		Rentals []struct {
			RentalID     int64   `json:"rental_id"`
			DepositTotal float64 `json:"deposit_total"`
		} `json:"rentals"`
	}
	code, e := req(t, "POST", "/rentals", g, map[string]interface{}{
		"session_type": "day", "slot_id": slotID,
		"include": map[string]bool{"protector": true},
	}, &created)
	mustOK(t, code, e["error"])
	rid := created.Rentals[0].RentalID
	depositTotal := created.Rentals[0].DepositTotal

	req(t, "POST", fmt.Sprintf("/rentals/%d/confirm", rid), g, map[string]string{}, nil)
	req(t, "POST", "/counter/checkout", counter, map[string]interface{}{"rental_id": rid}, nil)
	req(t, "POST", fmt.Sprintf("/rentals/%d/queue-return", rid), g,
		map[string]interface{}{"night": false}, nil)

	// 1. 快速签收：指定联系方式与 48h 申诉期
	var qs struct {
		PendingCount   int    `json:"pending_count"`
		ContactPhone   string `json:"contact_phone"`
		AppealDeadline string `json:"appeal_deadline"`
	}
	code, e = req(t, "POST", fmt.Sprintf("/counter/returns/%d/quick-sign", rid), counter,
		map[string]interface{}{"contact_phone": "13711112222", "appeal_hours": 48}, &qs)
	mustOK(t, code, e["error"])
	if qs.PendingCount != 4 || qs.ContactPhone != "13711112222" || qs.AppealDeadline == "" {
		t.Fatalf("快速签收回执异常: %+v", qs)
	}
	// 租赁单进入 quick_signed，明细全部待后检
	var rd struct {
		Status string `json:"status"`
		Items  []struct {
			ID     int64  `json:"id"`
			Type   string `json:"equipment_type"`
			Status string `json:"status"`
		} `json:"items"`
	}
	req(t, "GET", fmt.Sprintf("/rentals/%d", rid), g, nil, &rd)
	if rd.Status != "quick_signed" {
		t.Fatalf("快签后状态应为 quick_signed，实际 %s", rd.Status)
	}
	for _, it := range rd.Items {
		if it.Status != "pending_inspection" {
			t.Fatalf("明细 %s 应为 pending_inspection，实际 %s", it.Type, it.Status)
		}
	}
	// 快签不能直接结算（本单仍无 refund 流水）
	var rdq struct {
		Logs []struct {
			Type string `json:"type"`
		} `json:"deposit_logs"`
	}
	req(t, "GET", fmt.Sprintf("/rentals/%d", rid), g, nil, &rdq)
	for _, l := range rdq.Logs {
		if l.Type == "refund" {
			t.Fatal("快速签收时不应出现押金退还流水")
		}
	}

	// 2. 待检列表包含该单雪具，容量接口可用
	var pend []struct {
		ID            int64  `json:"id"`
		RentalID      int64  `json:"rental_id"`
		FromQuickSign bool   `json:"from_quick_sign"`
		QR            string `json:"qr_code"`
	}
	req(t, "GET", "/repair/pending-inspections", repair, nil, &pend)
	var cap0 struct {
		InspectUsed int `json:"inspect_used"`
	}
	req(t, "GET", "/repair/capacity", repair, nil, &cap0)
	if cap0.InspectUsed < 4 {
		t.Fatalf("待检数至少 4，实际 %d", cap0.InspectUsed)
	}
	myItems := map[int64]bool{}
	var skiItem, okItem int64
	for _, p := range pend {
		if p.RentalID == rid {
			myItems[p.ID] = true
			// 找一件雪板做损坏后检，另取一件做合格
		}
	}
	// 明细->雪具二维码映射：用 scan 不可行（无持有关系），直接从详情取 item_id
	var rd2 struct {
		Items []struct {
			ID     int64  `json:"id"`
			ItemID int64  `json:"item_id"`
			Type   string `json:"equipment_type"`
			Status string `json:"status"`
		} `json:"items"`
	}
	req(t, "GET", fmt.Sprintf("/rentals/%d", rid), g, nil, &rd2)
	for _, it := range rd2.Items {
		switch it.Type {
		case "ski":
			skiItem = it.ItemID
		case "helmet":
			okItem = it.ItemID
		}
		if it.ItemID != 0 {
			myItems[it.ItemID] = true
		}
	}
	if skiItem == 0 || okItem == 0 {
		t.Fatal("测试雪具未找到")
	}
	// 待检件必须来自 inspection 列表
	foundSki, foundOK := false, false
	for _, p := range pend {
		if p.ID == skiItem {
			foundSki = true
		}
		if p.ID == okItem {
			foundOK = true
		}
	}
	if !foundSki || !foundOK {
		t.Fatal("快签雪具应出现在待检列表")
	}

	// 3. 头盔后检合格上架；雪板后检发现板刃损伤（medium 525 元，无保险）
	req(t, "POST", fmt.Sprintf("/repair/inspections/%d/resolve", okItem), repair,
		map[string]interface{}{"result": "ok"}, nil)
	var pi struct {
		Message string `json:"message"`
		Settled bool   `json:"settled"`
	}
	code, e = req(t, "POST", fmt.Sprintf("/repair/inspections/%d/resolve", skiItem), repair,
		map[string]interface{}{"result": "damaged", "damage_type": "edge_damage",
			"severity": "medium", "description": "集成测试：后检发现板刃崩口"}, &pi)
	mustOK(t, code, e["error"])
	if pi.Settled {
		t.Fatal("仍有待检件，不应完成押金结算")
	}
	if !strings.Contains(pi.Message, "赔付通知") || !strings.Contains(pi.Message, "待游客确认") {
		t.Fatalf("后检损坏应提示发送赔付通知且待确认：%s", pi.Message)
	}

	// 4. 游客收到通知；通知确认前财务没有损坏扣减流水
	var notifs []struct {
		ID         int64   `json:"id"`
		Amount     float64 `json:"amount"`
		Status     string  `json:"status"`
		CanConfirm bool    `json:"can_confirm"`
		CanAppeal  bool    `json:"can_appeal"`
		RentalID   int64   `json:"rental_id"`
	}
	req(t, "GET", "/guest/notifications", g, nil, &notifs)
	var notifID int64
	for _, n := range notifs {
		if n.RentalID == rid && n.Status == "pending" {
			notifID = n.ID
		}
		if n.RentalID == rid && n.Amount != 525 {
			t.Fatalf("板刃后检拟扣应为 525，实际 %.2f", n.Amount)
		}
	}
	if notifID == 0 {
		t.Fatal("游客应收到一条待确认赔付通知")
	}
	// 押金仍全额冻结（只有 hold 流水，无 deduct/refund；含种子演示单 D 的 2850）
	var summary1 struct {
		QuickSignHeld float64 `json:"quick_sign_held"`
	}
	req(t, "GET", "/finance/summary", fin, nil, &summary1)
	if summary1.QuickSignHeld < depositTotal {
		t.Fatalf("通知未确认前快签冻结应至少 %.2f，实际 %.2f", depositTotal, summary1.QuickSignHeld)
	}

	// 5. 其余明细（雪鞋/护具等）全部后检合格，租赁单仍因通知 pending 不结算
	for _, it := range rd2.Items {
		if it.ItemID != 0 && it.ItemID != skiItem && it.ItemID != okItem {
			req(t, "POST", fmt.Sprintf("/repair/inspections/%d/resolve", it.ItemID), repair,
				map[string]interface{}{"result": "ok"}, nil)
		}
	}
	var rd3 struct {
		Status string `json:"status"`
	}
	req(t, "GET", fmt.Sprintf("/rentals/%d", rid), g, nil, &rd3)
	if rd3.Status != "quick_signed" {
		t.Fatalf("通知未决时应保持 quick_signed，实际 %s", rd3.Status)
	}

	// 6. 游客确认赔付：扣减 525，剩余押金释放
	var cf struct {
		Settled bool `json:"settled"`
	}
	code, e = req(t, "POST", fmt.Sprintf("/guest/notifications/%d/confirm", notifID), g,
		map[string]string{}, &cf)
	mustOK(t, code, e["error"])
	if !cf.Settled {
		t.Fatal("确认通知后应完成押金结算")
	}
	var rd4 struct {
		Status string `json:"status"`
		Logs   []struct {
			Type   string  `json:"type"`
			Amount float64 `json:"amount"`
			Note   string  `json:"note"`
		} `json:"deposit_logs"`
	}
	req(t, "GET", fmt.Sprintf("/rentals/%d", rid), g, nil, &rd4)
	if rd4.Status != "returned" {
		t.Fatalf("确认后应已归还释放押金，实际 %s", rd4.Status)
	}
	var deduct, refund float64
	for _, l := range rd4.Logs {
		switch l.Type {
		case "deduct_damage":
			deduct += l.Amount
		case "refund":
			refund += l.Amount
		}
	}
	if deduct != 525 {
		t.Fatalf("应补记损坏扣减 525，实际 %.2f", deduct)
	}
	if refund != depositTotal-525 {
		t.Fatalf("应释放剩余押金 %.2f，实际 %.2f", depositTotal-525, refund)
	}

	// 7. 已确认的通知不能重复确认或申诉
	if code, _ = req(t, "POST", fmt.Sprintf("/guest/notifications/%d/confirm", notifID), g,
		map[string]string{}, nil); code != 409 {
		t.Fatalf("重复确认应 409，实际 %d", code)
	}
	if code, _ = req(t, "POST", fmt.Sprintf("/guest/notifications/%d/appeal", notifID), g,
		map[string]string{"reason": "x"}, nil); code != 409 {
		t.Fatalf("已确认通知申诉应 409，实际 %d", code)
	}
}

// 申诉分支：游客发起申诉 → 柜台复核成立 → 取消扣减，押金全额释放；
// 申诉期已过的通知再申诉应被拒绝。
func TestQuickSignAppealFlow(t *testing.T) {
	g := login(t, "13900000005") // 团队成员-小钱 155/48/37/54 beginner
	counter := login(t, "13800000003")
	repair := login(t, "13800000004")
	fin := login(t, "13800000006")

	var slots []struct {
		ID        int64 `json:"id"`
		Remaining int   `json:"remaining"`
	}
	req(t, "GET", "/slots?session=day", g, nil, &slots)
	var slotID int64
	for _, s := range slots {
		if s.Remaining >= 1 {
			slotID = s.ID
			break
		}
	}
	var created struct {
		Rentals []struct {
			RentalID     int64   `json:"rental_id"`
			DepositTotal float64 `json:"deposit_total"`
		} `json:"rentals"`
	}
	req(t, "POST", "/rentals", g, map[string]interface{}{
		"session_type": "day", "slot_id": slotID,
		"include": map[string]bool{"protector": true},
	}, &created)
	rid := created.Rentals[0].RentalID
	depositTotal := created.Rentals[0].DepositTotal
	req(t, "POST", fmt.Sprintf("/rentals/%d/confirm", rid), g, map[string]string{}, nil)
	req(t, "POST", "/counter/checkout", counter, map[string]interface{}{"rental_id": rid}, nil)
	req(t, "POST", fmt.Sprintf("/rentals/%d/queue-return", rid), g,
		map[string]interface{}{"night": false}, nil)
	var qs struct {
		PendingCount int `json:"pending_count"`
	}
	req(t, "POST", fmt.Sprintf("/counter/returns/%d/quick-sign", rid), counter,
		map[string]interface{}{}, &qs)
	if qs.PendingCount != 4 {
		t.Fatalf("应有 4 件待后检，实际 %d", qs.PendingCount)
	}

	var rd struct {
		Items []struct {
			ItemID int64  `json:"item_id"`
			Type   string `json:"equipment_type"`
		} `json:"items"`
	}
	req(t, "GET", fmt.Sprintf("/rentals/%d", rid), g, nil, &rd)
	var skiItem int64
	for _, it := range rd.Items {
		if it.Type == "ski" {
			skiItem = it.ItemID
		} else {
			req(t, "POST", fmt.Sprintf("/repair/inspections/%d/resolve", it.ItemID), repair,
				map[string]interface{}{"result": "ok"}, nil)
		}
	}
	req(t, "POST", fmt.Sprintf("/repair/inspections/%d/resolve", skiItem), repair,
		map[string]interface{}{"result": "damaged", "damage_type": "edge_damage",
			"severity": "light", "description": "后检发现板刃轻微崩口"}, nil)
	var notifs0 []struct {
		ID       int64  `json:"id"`
		RentalID int64  `json:"rental_id"`
		Status   string `json:"status"`
	}
	req(t, "GET", "/guest/notifications", g, nil, &notifs0)
	var notifID int64
	for _, n := range notifs0 {
		if n.RentalID == rid {
			notifID = n.ID
		}
	}
	if notifID == 0 {
		t.Fatal("应生成赔付通知")
	}

	// 申诉理由为空应 400
	if code, _ := req(t, "POST", fmt.Sprintf("/guest/notifications/%d/appeal", notifID), g,
		map[string]string{"reason": ""}, nil); code != 400 {
		t.Fatalf("空申诉理由应 400，实际 %d", code)
	}
	req(t, "POST", fmt.Sprintf("/guest/notifications/%d/appeal", notifID), g,
		map[string]string{"reason": "归还时板刃完好，崩口应为运输磕碰"}, nil)
	// 申诉中再申诉应冲突
	if code, _ := req(t, "POST", fmt.Sprintf("/guest/notifications/%d/appeal", notifID), g,
		map[string]string{"reason": "再次申诉"}, nil); code != 409 {
		t.Fatalf("申诉中重复申诉应 409，实际 %d", code)
	}
	// 押金仍冻结（含种子演示单 D 的 2850）
	var sum0 struct {
		QuickSignHeld float64 `json:"quick_sign_held"`
	}
	req(t, "GET", "/finance/summary", fin, nil, &sum0)
	if sum0.QuickSignHeld < depositTotal {
		t.Fatalf("申诉复核期间押金应继续全额冻结，实际 %.2f", sum0.QuickSignHeld)
	}

	// 柜台复核：申诉成立
	var rv struct {
		Settled bool `json:"settled"`
	}
	code, em := req(t, "POST", fmt.Sprintf("/counter/appeals/%d/review", notifID), counter,
		map[string]interface{}{"approve": true, "note": "调阅监控确认归还时完好"}, &rv)
	mustOK(t, code, em["error"])
	if !rv.Settled {
		t.Fatal("申诉成立且无其他待处理件时应完成结算")
	}
	var rd2 struct {
		Status string `json:"status"`
		Logs   []struct {
			Type   string  `json:"type"`
			Amount float64 `json:"amount"`
		} `json:"deposit_logs"`
	}
	req(t, "GET", fmt.Sprintf("/rentals/%d", rid), g, nil, &rd2)
	if rd2.Status != "returned" {
		t.Fatalf("申诉成立后应归还结算，实际 %s", rd2.Status)
	}
	var deduct, refund float64
	for _, l := range rd2.Logs {
		if l.Type == "deduct_damage" {
			deduct += l.Amount
		}
		if l.Type == "refund" {
			refund += l.Amount
		}
	}
	if deduct != 0 {
		t.Fatalf("申诉成立不应扣减押金，实际 %.2f", deduct)
	}
	if refund != depositTotal {
		t.Fatalf("申诉成立应全额释放押金 %.2f，实际 %.2f", depositTotal, refund)
	}
}

// 申诉期结束：通知 pending 且 appeal_until 过期后，系统自动确认扣减并释放押金。
func TestQuickSignAppealDeadlineExpiry(t *testing.T) {
	g := login(t, "13900000001") // 张明（demo A 是 reserved，另下一单）
	counter := login(t, "13800000003")
	repair := login(t, "13800000004")

	var slots []struct {
		ID        int64 `json:"id"`
		Remaining int   `json:"remaining"`
	}
	req(t, "GET", "/slots?session=day", g, nil, &slots)
	var slotID int64
	for _, s := range slots {
		if s.Remaining >= 1 {
			slotID = s.ID
			break
		}
	}
	var created struct {
		Rentals []struct {
			RentalID     int64   `json:"rental_id"`
			DepositTotal float64 `json:"deposit_total"`
		} `json:"rentals"`
	}
	req(t, "POST", "/rentals", g, map[string]interface{}{
		"session_type": "day", "slot_id": slotID,
		"include": map[string]bool{"protector": true},
	}, &created)
	rid := created.Rentals[0].RentalID
	depositTotal := created.Rentals[0].DepositTotal
	req(t, "POST", fmt.Sprintf("/rentals/%d/confirm", rid), g, map[string]string{}, nil)
	req(t, "POST", "/counter/checkout", counter, map[string]interface{}{"rental_id": rid}, nil)
	req(t, "POST", fmt.Sprintf("/rentals/%d/queue-return", rid), g,
		map[string]interface{}{"night": false}, nil)
	req(t, "POST", fmt.Sprintf("/counter/returns/%d/quick-sign", rid), counter,
		map[string]interface{}{}, nil)

	var rd struct {
		Items []struct {
			ItemID int64  `json:"item_id"`
			Type   string `json:"equipment_type"`
		} `json:"items"`
	}
	req(t, "GET", fmt.Sprintf("/rentals/%d", rid), g, nil, &rd)
	var skiItem int64
	for _, it := range rd.Items {
		if it.Type == "ski" {
			skiItem = it.ItemID
		} else {
			req(t, "POST", fmt.Sprintf("/repair/inspections/%d/resolve", it.ItemID), repair,
				map[string]interface{}{"result": "ok"}, nil)
		}
	}
	req(t, "POST", fmt.Sprintf("/repair/inspections/%d/resolve", skiItem), repair,
		map[string]interface{}{"result": "damaged", "damage_type": "edge_damage",
			"severity": "light", "description": "申诉期过期测试：轻微板刃"}, nil)

	// 把该单通知的申诉截止时间调到过去（直接查库取通知 id，避免接口先触发懒结算）
	if _, err := testDB.Exec(`UPDATE damage_notifications SET appeal_until=now()-interval '1 hour'
		WHERE rental_id=$1`, rid); err != nil {
		t.Fatal(err)
	}
	var notifID int64
	if err := testDB.QueryRow(`SELECT id FROM damage_notifications WHERE rental_id=$1`, rid).
		Scan(&notifID); err != nil {
		t.Fatal(err)
	}
	// 过期后再申诉应被拒绝（通知仍处于 pending，但申诉期已过）
	if code, _ := req(t, "POST", fmt.Sprintf("/guest/notifications/%d/appeal", notifID), g,
		map[string]string{"reason": "过期前没来得及"}, nil); code != 409 {
		t.Fatalf("申诉期已过应 409，实际 %d", code)
	}
	// 触发懒结算（GET /rentals/mine）：过期通知自动确认、扣减并释放押金
	req(t, "GET", "/rentals/mine", g, nil, nil)
	var rd2 struct {
		Status string `json:"status"`
		Logs   []struct {
			Type   string  `json:"type"`
			Amount float64 `json:"amount"`
		} `json:"deposit_logs"`
	}
	req(t, "GET", fmt.Sprintf("/rentals/%d", rid), g, nil, &rd2)
	if rd2.Status != "returned" {
		t.Fatalf("申诉期结束后应自动结算，实际 %s", rd2.Status)
	}
	// 轻微板刃 = 1500×35%×50% = 262.5
	var deduct, refund float64
	for _, l := range rd2.Logs {
		if l.Type == "deduct_damage" {
			deduct += l.Amount
		}
		if l.Type == "refund" {
			refund += l.Amount
		}
	}
	if deduct != 262.5 {
		t.Fatalf("轻微板刃应扣 262.5，实际 %.2f", deduct)
	}
	if refund != depositTotal-262.5 {
		t.Fatalf("应释放 %.2f，实际 %.2f", depositTotal-262.5, refund)
	}
}

// 有保险的快签单：后检自动立案；理赔到账后申诉成立应被拦截（需先追回赔付），
// 柜台驳回申诉后押金按「扣减 525 - 保险冲抵 475 = 游客承担 100 免赔...」结算。
func TestQuickSignInsuredAppealAfterPayout(t *testing.T) {
	g := login(t, "13900000004") // 刘队长
	counter := login(t, "13800000003")
	repair := login(t, "13800000004")
	ins := login(t, "13800000005")

	var slots []struct {
		ID        int64 `json:"id"`
		Remaining int   `json:"remaining"`
	}
	req(t, "GET", "/slots?session=day", g, nil, &slots)
	var slotID int64
	for _, s := range slots {
		if s.Remaining >= 1 {
			slotID = s.ID
			break
		}
	}
	var created struct {
		Rentals []struct {
			RentalID     int64   `json:"rental_id"`
			DepositTotal float64 `json:"deposit_total"`
		} `json:"rentals"`
	}
	req(t, "POST", "/rentals", g, map[string]interface{}{
		"session_type": "day", "slot_id": slotID, "insurance_product_id": 2, // 安心险
		"include": map[string]bool{"protector": true},
	}, &created)
	rid := created.Rentals[0].RentalID
	depositTotal := created.Rentals[0].DepositTotal
	req(t, "POST", fmt.Sprintf("/rentals/%d/confirm", rid), g, map[string]string{}, nil)
	req(t, "POST", "/counter/checkout", counter, map[string]interface{}{"rental_id": rid}, nil)
	req(t, "POST", fmt.Sprintf("/rentals/%d/queue-return", rid), g,
		map[string]interface{}{"night": false}, nil)
	req(t, "POST", fmt.Sprintf("/counter/returns/%d/quick-sign", rid), counter, map[string]string{}, nil)

	var rd struct {
		Items []struct {
			ItemID int64  `json:"item_id"`
			Type   string `json:"equipment_type"`
		} `json:"items"`
	}
	req(t, "GET", fmt.Sprintf("/rentals/%d", rid), g, nil, &rd)
	var skiItem int64
	for _, it := range rd.Items {
		if it.Type == "ski" {
			skiItem = it.ItemID
		} else {
			req(t, "POST", fmt.Sprintf("/repair/inspections/%d/resolve", it.ItemID), repair,
				map[string]interface{}{"result": "ok"}, nil)
		}
	}
	req(t, "POST", fmt.Sprintf("/repair/inspections/%d/resolve", skiItem), repair,
		map[string]interface{}{"result": "damaged", "damage_type": "edge_damage",
			"severity": "medium", "description": "有保险快签单后检板刃"}, nil)

	// 自动立案：525 - 50 免赔 = 475
	var claims []struct {
		ID       int64  `json:"id"`
		RentalID int64  `json:"rental_id"`
		Status   string `json:"status"`
	}
	req(t, "GET", "/insurance/claims", ins, nil, &claims)
	var claimID int64
	for _, c := range claims {
		if c.RentalID == rid {
			claimID = c.ID
		}
	}
	if claimID == 0 {
		t.Fatal("有保险快签单后检损坏应自动立案")
	}
	req(t, "POST", fmt.Sprintf("/insurance/claims/%d/review", claimID), ins,
		map[string]interface{}{"status": "under_review", "note": "审核"}, nil)
	req(t, "POST", fmt.Sprintf("/insurance/claims/%d/review", claimID), ins,
		map[string]interface{}{"status": "approved", "note": "核定"}, nil)
	req(t, "POST", fmt.Sprintf("/insurance/claims/%d/pay", claimID), ins,
		map[string]string{}, nil)

	var notifs []struct {
		ID       int64 `json:"id"`
		RentalID int64 `json:"rental_id"`
	}
	req(t, "GET", "/guest/notifications", g, nil, &notifs)
	var notifID int64
	for _, n := range notifs {
		if n.RentalID == rid {
			notifID = n.ID
		}
	}
	req(t, "POST", fmt.Sprintf("/guest/notifications/%d/appeal", notifID), g,
		map[string]string{"reason": "对后检结论有异议"}, nil)
	// 理赔已到账：申诉成立应被拒绝
	if code, _ := req(t, "POST", fmt.Sprintf("/counter/appeals/%d/review", notifID), counter,
		map[string]interface{}{"approve": true, "note": "x"}, nil); code != 409 {
		t.Fatalf("理赔已到账时申诉成立应 409，实际 %d", code)
	}
	// 驳回申诉：扣减 525 + 冲抵 475，释放 deposit-50
	var rv struct {
		Settled bool `json:"settled"`
	}
	code, em := req(t, "POST", fmt.Sprintf("/counter/appeals/%d/review", notifID), counter,
		map[string]interface{}{"approve": false, "note": "证据充分，维持认定"}, &rv)
	mustOK(t, code, em["error"])
	if !rv.Settled {
		t.Fatal("驳回申诉后应完成结算")
	}
	var rd2 struct {
		Status string `json:"status"`
		Logs   []struct {
			Type   string  `json:"type"`
			Amount float64 `json:"amount"`
		} `json:"deposit_logs"`
	}
	req(t, "GET", fmt.Sprintf("/rentals/%d", rid), g, nil, &rd2)
	if rd2.Status != "returned" {
		t.Fatalf("应已结算归还，实际 %s", rd2.Status)
	}
	sums := map[string]float64{}
	for _, l := range rd2.Logs {
		sums[l.Type] += l.Amount
	}
	if sums["deduct_damage"] != 525 || sums["insurance_offset"] != 475 {
		t.Fatalf("扣减/冲抵异常: %+v", sums)
	}
	if sums["refund"] != depositTotal-50 {
		t.Fatalf("游客承担免赔 50，应释放 %.2f，实际 %.2f", depositTotal-50, sums["refund"])
	}
}

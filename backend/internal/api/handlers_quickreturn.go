package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/lib/pq"

	"skirental/internal/rules"
)

// ---------- 高峰快速签收 ----------

type quickSignReq struct {
	QRCodes      []string `json:"qr_codes"`      // 可选：逐件扫码清点；为空表示整单免扫签收
	ContactPhone string   `json:"contact_phone"` // 游客联系方式（默认取注册手机号）
	AppealHours  float64  `json:"appeal_hours"`  // 申诉期限（小时），默认 48
}

// quickSignReturn 游客赶班车时柜台先做快速签收：不做逐件验损，
// 雪具全部转入维修间待检区，押金继续冻结，待后检与申诉期结束后再释放。
func (s *Server) quickSignReturn(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	rid := urlVar(r, "rid")
	var req quickSignReq
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()

	var status, session, guestPhone string
	var guestID int64
	if err := tx.QueryRow(`SELECT r.status, r.session_type, r.guest_id, u.phone
		FROM rentals r JOIN users u ON u.id=r.guest_id WHERE r.id=$1 FOR UPDATE`, rid).
		Scan(&status, &session, &guestID, &guestPhone); err != nil {
		writeError(w, http.StatusNotFound, "租赁单不存在")
		return
	}
	if status != "return_pending" {
		writeError(w, http.StatusConflict, "仅归还排队中的租赁单可以快速签收")
		return
	}

	rows, err := tx.Query(`SELECT ri.id, ri.item_id, ei.qr_code FROM rental_items ri
		JOIN equipment_items ei ON ei.id=ri.item_id
		WHERE ri.rental_id=$1 AND ri.status='out' ORDER BY ri.id FOR UPDATE`, rid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type line struct {
		riID, itemID int64
		qr           string
	}
	var lines []line
	for rows.Next() {
		var l line
		if err := rows.Scan(&l.riID, &l.itemID, &l.qr); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		lines = append(lines, l)
	}
	rows.Close()
	if len(lines) == 0 {
		writeError(w, http.StatusConflict, "该单没有待归还的雪具")
		return
	}
	if len(req.QRCodes) > 0 { // 选择扫码清点时，必须件件扫到
		scanned := map[string]bool{}
		for _, q := range req.QRCodes {
			scanned[q] = true
		}
		for _, l := range lines {
			if !scanned[l.qr] {
				writeError(w, http.StatusBadRequest, "还有雪具未扫码清点："+l.qr)
				return
			}
		}
	}
	for _, l := range lines {
		// 保留 current_rental_item_id 关联，后检可追溯租赁单与游客
		tx.Exec(`UPDATE equipment_items SET status='inspection' WHERE id=$1`, l.itemID)
		tx.Exec(`UPDATE rental_items SET status='pending_inspection' WHERE id=$1`, l.riID)
	}

	contact := req.ContactPhone
	if contact == "" {
		contact = guestPhone
	}
	deadline := rules.AppealDeadline(req.AppealHours)
	tx.Exec(`UPDATE rentals SET status='quick_signed', quick_signed_at=now(), quick_signed_by=$2,
		contact_phone=$3, appeal_deadline=$4 WHERE id=$1`,
		rid, c.UserID, contact, deadline)
	tx.Exec(`UPDATE queue_tickets SET status='done', finished_at=now()
		WHERE rental_id=$1 AND queue_type IN ('return_day','return_night','damage')
		AND status IN ('waiting','called','serving')`, rid)
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":         fmt.Sprintf("快速签收完成，%d 件雪具已转维修间待检；押金继续冻结，申诉期结束后释放", len(lines)),
		"pending_count":   len(lines),
		"contact_phone":   contact,
		"appeal_deadline": deadline.Format("2006-01-02 15:04"),
	})
}

// ---------- 待检雪具与维修间容量 ----------

// pendingInspections 维修间待检区雪具列表（含来源租赁单与快签标记）
func (s *Server) pendingInspections(w http.ResponseWriter, r *http.Request) {
	s.listJSON(w, `SELECT COALESCE(json_agg(t ORDER BY t.id), '[]') FROM (
		SELECT i.id, i.qr_code, i.serial_no,
		       m.brand, m.name, m.size_label, m.type AS equipment_type,
		       ri.id AS rental_item_id, r.id AS rental_id, r.code AS rental_code,
		       r.status AS rental_status, u.name AS guest_name, u.phone AS guest_phone,
		       (r.status='quick_signed') AS from_quick_sign,
		       to_char(r.quick_signed_at,'MM-DD HH24:MI') AS quick_signed_at
		FROM equipment_items i
		JOIN equipment_models m ON m.id=i.model_id
		LEFT JOIN rental_items ri ON ri.id=i.current_rental_item_id
		LEFT JOIN rentals r ON r.id=ri.rental_id
		LEFT JOIN users u ON u.id=r.guest_id
		WHERE i.status='inspection'
	) t`)
}

func (s *Server) repairCapacity(w http.ResponseWriter, r *http.Request) {
	s.oneJSON(w, `SELECT json_build_object(
		'inspect_capacity', rc.inspect_capacity,
		'repair_capacity',  rc.repair_capacity,
		'inspect_used',  (SELECT count(*) FROM equipment_items WHERE status='inspection'),
		'repair_used',   (SELECT count(*) FROM repair_orders WHERE status IN ('queued','in_progress','waiting_part')),
		'quick_sign_open', (SELECT count(*) FROM rentals WHERE status='quick_signed'),
		'appeal_open',   (SELECT count(*) FROM damage_notifications WHERE status='appealed'),
		'notify_pending',(SELECT count(*) FROM damage_notifications WHERE status='pending')
	) FROM repair_capacity rc WHERE rc.id=1`)
}

type capacityReq struct {
	InspectCapacity int `json:"inspect_capacity"`
	RepairCapacity  int `json:"repair_capacity"`
}

func (s *Server) updateRepairCapacity(w http.ResponseWriter, r *http.Request) {
	var req capacityReq
	if err := decode(r, &req); err != nil || req.InspectCapacity < 1 || req.RepairCapacity < 1 ||
		req.InspectCapacity > 500 || req.RepairCapacity > 500 {
		writeError(w, http.StatusBadRequest, "容量需为 1~500 的整数")
		return
	}
	if _, ok := s.execOne(w, `UPDATE repair_capacity SET inspect_capacity=$1, repair_capacity=$2,
		updated_at=now() WHERE id=1`, req.InspectCapacity, req.RepairCapacity); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "维修间容量已更新"})
}

// ---------- 维修间后检 ----------

type postInspectReq struct {
	Result      string   `json:"result"` // ok|damaged
	DamageType  string   `json:"damage_type"`
	Severity    string   `json:"severity"`
	Description string   `json:"description"`
	Photos      []string `json:"photos"`
}

// postInspect 维修间对待检雪具做后检。快签单后检出损坏时：
// 生成损坏报告/维修工单/保险立案与「赔付通知」，但不直接扣押金——
// 需游客确认、申诉复核驳回或申诉期结束后才记扣减并释放押金。
func (s *Server) postInspect(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	itemID := urlVar(r, "id")
	var req postInspectReq
	if err := decode(r, &req); err != nil || (req.Result != "ok" && req.Result != "damaged") {
		writeError(w, http.StatusBadRequest, "后检结果需为 ok 或 damaged")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()

	var itemStatus string
	var curRI sql.NullInt64
	if err := tx.QueryRow(`SELECT status, current_rental_item_id FROM equipment_items
		WHERE id=$1 FOR UPDATE`, itemID).Scan(&itemStatus, &curRI); err != nil {
		writeError(w, http.StatusNotFound, "雪具不存在")
		return
	}
	if itemStatus != "inspection" {
		writeError(w, http.StatusConflict, "该雪具不在待检区（当前状态："+itemStatus+"）")
		return
	}

	// 关联租赁明细（快签单保留关联；换码回收件无关联走简单后检）
	var riID, rentalID, guestID int64
	var riStatus, rentalStatus string
	var appealDeadline sql.NullTime
	linked := false
	if curRI.Valid {
		err := tx.QueryRow(`SELECT ri.id, ri.rental_id, ri.status, r.status, r.guest_id, r.appeal_deadline
			FROM rental_items ri JOIN rentals r ON r.id=ri.rental_id
			WHERE ri.id=$1 FOR UPDATE`, curRI.Int64).
			Scan(&riID, &rentalID, &riStatus, &rentalStatus, &guestID, &appealDeadline)
		linked = err == nil && rentalStatus == "quick_signed" && riStatus == "pending_inspection"
	}

	msg := ""
	if req.Result == "ok" {
		tx.Exec(`UPDATE equipment_items SET status='available', current_rental_item_id=NULL WHERE id=$1`, itemID)
		if linked {
			tx.Exec(`UPDATE rental_items SET status='returned_ok', returned_at=now() WHERE id=$1`, riID)
		}
		msg = "后检合格，雪具已重新上架"
	} else {
		if _, ok := rules.DamageRuleOf(req.DamageType); !ok || req.DamageType == "lost" {
			writeError(w, http.StatusBadRequest, "后检损坏类型不正确（丢失请走人工核查）")
			return
		}
		if req.Severity == "" {
			req.Severity = "medium"
		}
		if linked {
			// 快签单：损坏报告 + 维修工单 + 保险立案 + 赔付通知（暂不扣押金）
			var deposit float64
			tx.QueryRow(`SELECT deposit FROM rental_items WHERE id=$1`, riID).Scan(&deposit)
			deduction, rule := rules.DamageDeduction(req.DamageType, req.Severity, deposit)
			photos, _ := json.Marshal(req.Photos)
			var dID int64
			tx.QueryRow(`INSERT INTO damage_reports
				(rental_item_id, reporter_id, stage, damage_type, severity, description, photos,
				 status, deduction, assessed_by)
				VALUES ($1,$2,'post_inspection',$3,$4,$5,COALESCE($6,'[]'::jsonb),'repair_created',$7,$2)
				RETURNING id`,
				riID, c.UserID, req.DamageType, req.Severity, req.Description, photos, deduction).Scan(&dID)
			var roID int64
			tx.QueryRow(`INSERT INTO repair_orders(item_id, damage_id, issue, advice, status)
				VALUES ($1,$2,$3,$4,'queued') RETURNING id`,
				itemID, dID, "后检发现-"+rule.Label+"："+req.Description, rule.Advice).Scan(&roID)
			tx.Exec(`UPDATE damage_reports SET repair_order_id=$1 WHERE id=$2`, roID, dID)
			tx.Exec(`UPDATE equipment_items SET status='repair', current_rental_item_id=NULL WHERE id=$1`, itemID)
			tx.Exec(`UPDATE rental_items SET status='repair' WHERE id=$1`, riID)
			s.fileClaimIfCovered(tx, rentalID, dID, req.DamageType, deduction)

			until := time.Now().Add(time.Duration(rules.AppealWindowHours) * time.Hour)
			if appealDeadline.Valid {
				until = appealDeadline.Time
			}
			var notifID int64
			tx.QueryRow(`INSERT INTO damage_notifications
				(rental_id, damage_id, guest_id, amount, appeal_until)
				VALUES ($1,$2,$3,$4,$5) RETURNING id`, rentalID, dID, guestID, deduction, until).Scan(&notifID)
			msg = fmt.Sprintf("后检发现%s，已开维修工单并向游客发送赔付通知（¥%.2f，待游客确认，申诉期至 %s）",
				rule.Label, deduction, until.Format("01-02 15:04"))
		} else {
			// 无快签关联（如换码回收件）：只开维修工单，不涉及押金
			rule, _ := rules.DamageRuleOf(req.DamageType)
			tx.Exec(`INSERT INTO repair_orders(item_id, damage_id, issue, advice, status)
				VALUES ($1,NULL,$2,$3,'queued')`,
				itemID, "后检发现-"+rule.Label+"："+req.Description, rule.Advice)
			tx.Exec(`UPDATE equipment_items SET status='repair', current_rental_item_id=NULL WHERE id=$1`, itemID)
			msg = "后检发现损坏，已开维修工单（非快签单，不涉及押金通知）"
		}
	}

	settled := false
	if linked {
		settled, _ = s.settleQuickSignIfDue(tx, rentalID, c.UserID)
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if settled {
		msg += "；该单后检全部完成，押金已结算释放"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"message": msg, "settled": settled})
}

// fileClaimIfCovered 有效保单且覆盖该损坏类型时自动立案（与 assessDamage 同规则）
func (s *Server) fileClaimIfCovered(tx *sql.Tx, rentalID, damageID int64, damageType string, deduction float64) {
	var productID, policyID int64
	if err := tx.QueryRow(`SELECT ip.product_id, ip.id FROM insurance_policies ip
		WHERE ip.rental_id=$1 AND ip.status='active' LIMIT 1`, rentalID).
		Scan(&productID, &policyID); err != nil {
		return
	}
	var deductible, capAmt float64
	var covers pq.StringArray
	if err := tx.QueryRow(`SELECT deductible, coverage_cap, covers FROM insurance_products WHERE id=$1`,
		productID).Scan(&deductible, &capAmt, &covers); err != nil {
		return
	}
	covered := false
	for _, cv := range covers {
		if cv == damageType {
			covered = true
			break
		}
	}
	res := rules.CalcClaim(deduction, deductible, capAmt, covered)
	var claimID int64
	tx.QueryRow(`INSERT INTO insurance_claims
		(policy_id, damage_id, amount_requested, deductible, amount_approved, status, note)
		VALUES ($1,$2,$3,$4,$5,'filed',$6) RETURNING id`,
		policyID, damageID, res.Requested, deductible, res.Approved, res.Message).Scan(&claimID)
	tx.Exec(`UPDATE damage_reports SET claim_id=$1 WHERE id=$2`, claimID, damageID)
	tx.Exec(`UPDATE insurance_policies SET status='claimed' WHERE id=$1`, policyID)
}

// ---------- 押金释放（快签单结算） ----------

// recordNotificationDeduction 通知确认后补记押金扣减流水（幂等）
func (s *Server) recordNotificationDeduction(tx *sql.Tx, notifID, userID int64) {
	var rentalID, damageID int64
	var amount float64
	err := tx.QueryRow(`SELECT rental_id, damage_id, amount FROM damage_notifications
		WHERE id=$1 AND status='confirmed'`, notifID).Scan(&rentalID, &damageID, &amount)
	if err != nil {
		return
	}
	var exists bool
	tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM deposit_transactions
		WHERE damage_id=$1 AND type='deduct_damage')`, damageID).Scan(&exists)
	if exists {
		return
	}
	tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, damage_id, note, created_by)
		VALUES ($1,'deduct_damage',$2,$3,'快签后检赔付通知确认，扣减押金',$4)`,
		rentalID, amount, damageID, nz(userID))
	// 维修工单已完成且无在处理赔时，损坏报告随通知确认闭环
	tx.Exec(`UPDATE damage_reports SET status='closed' WHERE id=$1 AND claim_id IS NULL
		AND repair_order_id IN (SELECT id FROM repair_orders WHERE status IN ('done','scrapped'))`, damageID)
}

// settleQuickSignIfDue 快签单满足释放条件时结算押金：
// 全部雪具后检完毕 且 无待确认/申诉中的通知（逾期待确认通知先自动视为确认）。
// 逾期判定以 quick_signed_at 为实际归还时间。
func (s *Server) settleQuickSignIfDue(tx *sql.Tx, rentalID, userID int64) (bool, error) {
	// 申诉期已过的待确认通知自动确认并补记扣减
	rows, err := tx.Query(`UPDATE damage_notifications SET status='confirmed', resolved_at=now()
		WHERE rental_id=$1 AND status='pending' AND appeal_until <= now()
		RETURNING id`, rentalID)
	if err != nil {
		return false, err
	}
	var expired []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		expired = append(expired, id)
	}
	rows.Close()
	for _, id := range expired {
		s.recordNotificationDeduction(tx, id, userID)
	}

	var blocked bool
	if err := tx.QueryRow(`SELECT
		EXISTS(SELECT 1 FROM rental_items WHERE rental_id=$1 AND status='pending_inspection')
		OR EXISTS(SELECT 1 FROM damage_notifications WHERE rental_id=$1 AND status IN ('pending','appealed'))`,
		rentalID).Scan(&blocked); err != nil {
		return false, err
	}
	if blocked {
		return false, nil
	}

	var status string
	var depositTotal, feeBase float64
	var due, signedAt, pickupAt sql.NullTime
	var createdAt time.Time
	if err := tx.QueryRow(`SELECT status, deposit_total, fee_total, return_due_at, quick_signed_at,
		COALESCE(pickup_at, created_at), created_at
		FROM rentals WHERE id=$1 FOR UPDATE`, rentalID).
		Scan(&status, &depositTotal, &feeBase, &due, &signedAt, &pickupAt, &createdAt); err != nil {
		return false, err
	}
	if status != "quick_signed" {
		return false, nil
	}

	returnedAt := time.Now()
	if signedAt.Valid {
		returnedAt = signedAt.Time // 快速签收时间视为实际归还时间
	}
	if due.Valid {
		if d := returnedAt.Sub(due.Time); d > 15*time.Minute {
			lateFee := rules.LateFee(d.Hours(), feeBase)
			if lateFee > 0 {
				tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, note, created_by)
					VALUES ($1,'deduct_late',$2,$3,$4)`, rentalID, lateFee,
					fmt.Sprintf("超时 %.1f 小时", d.Hours()), nz(userID))
			}
		}
	}

	var deducted, offsets float64
	tx.QueryRow(`SELECT COALESCE(sum(amount),0) FROM deposit_transactions
		WHERE rental_id=$1 AND type LIKE 'deduct%'`, rentalID).Scan(&deducted)
	tx.QueryRow(`SELECT COALESCE(sum(amount),0) FROM deposit_transactions
		WHERE rental_id=$1 AND type='insurance_offset'`, rentalID).Scan(&offsets)
	refund := depositTotal - deducted + offsets
	if refund < 0 {
		refund = 0
	}
	if refund > depositTotal {
		refund = depositTotal
	}
	tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, note, created_by)
		VALUES ($1,'refund',$2,'快速签收后检完成，申诉期结束，释放剩余押金',$3)`, rentalID, refund, nz(userID))

	usedHours := 0.0
	if pickupAt.Valid {
		usedHours = returnedAt.Sub(pickupAt.Time).Hours()
	}
	tx.Exec(`UPDATE rentals SET status='returned', returned_at=now(),
		notes=notes || $2 WHERE id=$1`,
		rentalID, fmt.Sprintf("快速签收归还；使用时长 %.1f 小时；", usedHours))
	return true, nil
}

// releaseDueQuickSigns 懒结算：读取类接口调用，把满足条件的快签单结算掉
func (s *Server) releaseDueQuickSigns() {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM rentals WHERE status='quick_signed'
		AND NOT EXISTS(SELECT 1 FROM rental_items ri WHERE ri.rental_id=rentals.id AND ri.status='pending_inspection')`)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := s.settleQuickSignIfDue(tx, id, 0); err != nil {
			return
		}
	}
	tx.Commit()
}

// ---------- 游客端：赔付通知与申诉 ----------

func (s *Server) myNotifications(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	s.releaseDueQuickSigns()
	s.listJSON(w, `SELECT COALESCE(json_agg(t ORDER BY t.id DESC), '[]') FROM (
		SELECT dn.id, dn.amount, dn.status, dn.appeal_reason, dn.review_note,
		       to_char(dn.appeal_until,'YYYY-MM-DD HH24:MI') AS appeal_until,
		       to_char(dn.notified_at,'MM-DD HH24:MI') AS notified_at,
		       (dn.status='pending') AS can_confirm,
		       (dn.status='pending' AND dn.appeal_until > now()) AS can_appeal,
		       r.id AS rental_id, r.code AS rental_code,
		       d.damage_type, d.severity, d.description, d.photos,
		       m.brand||' '||m.name||' '||m.size_label AS item_label,
		       CASE d.damage_type
		         WHEN 'edge_damage' THEN '板刃损伤' WHEN 'base_scratch' THEN '板底划伤'
		         WHEN 'binding_issue' THEN '固定器故障' WHEN 'liner_wet' THEN '内衬潮湿'
		         WHEN 'shell_crack' THEN '壳体裂纹' WHEN 'strap_broken' THEN '绑带断裂'
		         WHEN 'lost' THEN '雪具丢失' ELSE '其他异常' END AS type_label
		FROM damage_notifications dn
		JOIN rentals r ON r.id=dn.rental_id
		JOIN damage_reports d ON d.id=dn.damage_id
		JOIN rental_items ri ON ri.id=d.rental_item_id
		LEFT JOIN equipment_models m ON m.id=ri.model_id
		WHERE dn.guest_id=$1
	) t`, c.UserID)
}

func (s *Server) loadOwnNotification(w http.ResponseWriter, r *http.Request, notifID int64) (
	status, rentalCode string, rentalID int64, ok bool) {
	c := currentUser(r)
	err := s.db.QueryRow(`SELECT dn.status, dn.rental_id, r.code FROM damage_notifications dn
		JOIN rentals r ON r.id=dn.rental_id WHERE dn.id=$1 AND dn.guest_id=$2`,
		notifID, c.UserID).Scan(&status, &rentalID, &rentalCode)
	if err != nil {
		writeError(w, http.StatusNotFound, "赔付通知不存在")
		return "", "", 0, false
	}
	return status, rentalCode, rentalID, true
}

// confirmNotification 游客确认赔付通知：补记扣减流水并尝试结算释放押金
func (s *Server) confirmNotification(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	id := urlVar(r, "id")
	status, _, rentalID, ok := s.loadOwnNotification(w, r, id)
	if !ok {
		return
	}
	if status != "pending" {
		writeError(w, http.StatusConflict, "该通知已处理："+status)
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE damage_notifications SET status='confirmed', resolved_at=now()
		WHERE id=$1 AND status='pending'`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusConflict, "该通知已被处理，请刷新")
		return
	}
	s.recordNotificationDeduction(tx, id, c.UserID)
	settled, _ := s.settleQuickSignIfDue(tx, rentalID, c.UserID)
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	msg := "已确认赔付，对应押金已扣减"
	if settled {
		msg += "；该单后检与通知全部完成，剩余押金已释放"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"message": msg, "settled": settled})
}

type appealReq struct {
	Reason string `json:"reason"`
}

// appealNotification 游客在申诉期内对赔付通知发起申诉
func (s *Server) appealNotification(w http.ResponseWriter, r *http.Request) {
	id := urlVar(r, "id")
	var req appealReq
	if err := decode(r, &req); err != nil || req.Reason == "" {
		writeError(w, http.StatusBadRequest, "请填写申诉理由")
		return
	}
	status, _, _, ok := s.loadOwnNotification(w, r, id)
	if !ok {
		return
	}
	if status != "pending" {
		writeError(w, http.StatusConflict, "该通知已处理，不能申诉："+status)
		return
	}
	res, err := s.db.Exec(`UPDATE damage_notifications SET status='appealed', appeal_reason=$2
		WHERE id=$1 AND status='pending' AND appeal_until > now()`, id, req.Reason)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusConflict, "申诉期已过，通知将按确认处理")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "申诉已提交，柜台将复核后检记录；复核期间押金保持冻结",
	})
}

// ---------- 柜台：申诉复核 ----------

func (s *Server) listAppeals(w http.ResponseWriter, r *http.Request) {
	s.listJSON(w, `SELECT COALESCE(json_agg(t ORDER BY (t.status='appealed') DESC, t.id DESC), '[]') FROM (
		SELECT dn.id, dn.amount, dn.status, dn.appeal_reason, dn.review_note,
		       to_char(dn.appeal_until,'YYYY-MM-DD HH24:MI') AS appeal_until,
		       to_char(dn.notified_at,'MM-DD HH24:MI') AS notified_at,
		       r.id AS rental_id, r.code AS rental_code,
		       u.name AS guest_name, u.phone AS guest_phone,
		       d.damage_type, d.severity, d.description, d.photos,
		       m.brand||' '||m.name||' '||m.size_label AS item_label,
		       CASE d.damage_type
		         WHEN 'edge_damage' THEN '板刃损伤' WHEN 'base_scratch' THEN '板底划伤'
		         WHEN 'binding_issue' THEN '固定器故障' WHEN 'liner_wet' THEN '内衬潮湿'
		         WHEN 'shell_crack' THEN '壳体裂纹' WHEN 'strap_broken' THEN '绑带断裂'
		         WHEN 'lost' THEN '雪具丢失' ELSE '其他异常' END AS type_label
		FROM damage_notifications dn
		JOIN rentals r ON r.id=dn.rental_id
		JOIN users u ON u.id=dn.guest_id
		JOIN damage_reports d ON d.id=dn.damage_id
		JOIN rental_items ri ON ri.id=d.rental_item_id
		LEFT JOIN equipment_models m ON m.id=ri.model_id
		WHERE dn.status='appealed'
		   OR (dn.resolved_by IS NOT NULL AND dn.resolved_at > now()-interval '2 days')
	) t`)
}

type reviewAppealReq struct {
	Approve bool   `json:"approve"` // true=申诉成立（取消扣减） false=驳回（维持扣减）
	Note    string `json:"note"`
}

// reviewAppeal 柜台复核申诉：成立则取消赔付通知（关联理赔一并拒赔）；
// 驳回则通知确认生效、补记扣减。复核后若该单无其他待处理事项即释放押金。
func (s *Server) reviewAppeal(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	id := urlVar(r, "id")
	var req reviewAppealReq
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()

	var status string
	var rentalID, damageID int64
	if err := tx.QueryRow(`SELECT status, rental_id, damage_id FROM damage_notifications
		WHERE id=$1 FOR UPDATE`, id).Scan(&status, &rentalID, &damageID); err != nil {
		writeError(w, http.StatusNotFound, "赔付通知不存在")
		return
	}
	if status != "appealed" {
		writeError(w, http.StatusConflict, "该通知不在申诉状态："+status)
		return
	}

	msg := ""
	if req.Approve {
		// 保险已把钱赔到押金账户时不能直接取消认定，需线下追回理赔款后再处理
		var paid int
		tx.QueryRow(`SELECT count(*) FROM insurance_claims WHERE damage_id=$1 AND status='paid'`,
			damageID).Scan(&paid)
		if paid > 0 {
			writeError(w, http.StatusConflict, "该损坏的保险理赔已到账，请先由保险/财务线下追回赔付后再撤销认定")
			return
		}
		tx.Exec(`UPDATE damage_notifications SET status='cancelled', resolved_at=now(),
			resolved_by=$1, review_note=$2 WHERE id=$3`, c.UserID, req.Note, id)
		tx.Exec(`UPDATE damage_reports SET status='closed' WHERE id=$1`, damageID)
		// 已自动立案的理赔随损坏认定取消而拒赔
		tx.Exec(`UPDATE insurance_claims SET status='rejected', note=note||'（申诉成立，损坏认定取消）',
			updated_at=now() WHERE damage_id=$1 AND status IN ('filed','under_review','approved')`, damageID)
		msg = "申诉成立：赔付通知已取消，该笔扣减不再执行"
	} else {
		tx.Exec(`UPDATE damage_notifications SET status='confirmed', resolved_at=now(),
			resolved_by=$1, review_note=$2 WHERE id=$3`, c.UserID, req.Note, id)
		s.recordNotificationDeduction(tx, id, c.UserID)
		msg = "申诉已驳回：维持原赔付认定，押金扣减已生效"
	}
	settled, _ := s.settleQuickSignIfDue(tx, rentalID, c.UserID)
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if settled {
		msg += "；该单事项已全部了结，剩余押金已释放"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"message": msg, "settled": settled})
}

package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/lib/pq"

	"skirental/internal/rules"
)

// ============================================================
// 高峰归还排队：快速签收 → 维修间后检 → 赔付通知/申诉 → 申诉期后释放押金
//
// 状态机：
//   rentals:        return_pending --快速签收--> quick_signed --结算--> returned
//   rental_items:   out --快速签收--> pending_inspection --后检--> returned_ok / repair / returned_damaged
//   quick_signoffs: pending_inspection --> inspected --> settled
//   notifications:  pending --游客确认--> confirmed
//                   pending --游客申诉--> appealed --柜台处理--> resolved(upheld|cancelled)
//                   pending --申诉期届满--> expired（视为无异议）
// 押金：快速签收后继续冻结；后检无问题立即退还；有赔付通知时，
//       必须游客确认 / 申诉处理完 / 申诉期届满后才扣减并释放。
// ============================================================

// ---------- 柜台：快速签收（游客赶班车） ----------

type quickSignoffReq struct {
	ContactPhone string `json:"contact_phone"` // 留存的游客联系方式，缺省取账号手机号
	ShuttleNote  string `json:"shuttle_note"`  // 赶班车备注
}

func (s *Server) quickSignoff(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	rid := urlVar(r, "rid")
	var req quickSignoffReq
	decode(r, &req) // 允许空 body

	tx, _ := s.db.Begin()
	defer tx.Rollback()

	var status, guestPhone string
	var guestID int64
	if err := tx.QueryRow(`SELECT r.status, r.guest_id, u.phone FROM rentals r
		JOIN users u ON u.id=r.guest_id WHERE r.id=$1 FOR UPDATE`, rid).
		Scan(&status, &guestID, &guestPhone); err != nil {
		writeError(w, http.StatusNotFound, "租赁单不存在")
		return
	}
	if status != "return_pending" {
		writeError(w, http.StatusConflict, "仅归还队列中的租赁单可以快速签收（游客请先排队归还）")
		return
	}
	var pendingItems int
	tx.QueryRow(`SELECT count(*) FROM rental_items WHERE rental_id=$1 AND status='out'`, rid).Scan(&pendingItems)
	if pendingItems == 0 {
		writeError(w, http.StatusConflict, "该单没有待签收的雪具")
		return
	}
	contact := req.ContactPhone
	if contact == "" {
		contact = guestPhone
	}
	deadline := time.Now().Add(time.Duration(s.cfg.AppealWindowHours) * time.Hour)

	// 关闭归还队列票并记录票号
	var ticketID sql.NullInt64
	tx.QueryRow(`UPDATE queue_tickets SET status='done', finished_at=now()
		WHERE rental_id=$1 AND queue_type IN ('return_day','return_night')
		  AND status IN ('waiting','called','serving') RETURNING id`, rid).Scan(&ticketID)

	var qsID int64
	if err := tx.QueryRow(`INSERT INTO quick_signoffs
		(rental_id, ticket_id, signed_by, contact_phone, shuttle_note, appeal_deadline)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		rid, ticketID, c.UserID, contact, req.ShuttleNote, deadline).Scan(&qsID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 雪具转待后检：明细 pending_inspection，实物 inspection（维修间容量占用）
	tx.Exec(`UPDATE equipment_items SET status='inspection', current_rental_item_id=NULL
		WHERE id IN (SELECT item_id FROM rental_items WHERE rental_id=$1 AND status='out')`, rid)
	tx.Exec(`UPDATE rental_items SET status='pending_inspection'
		WHERE rental_id=$1 AND status='out'`, rid)
	tx.Exec(`UPDATE rentals SET status='quick_signed' WHERE id=$1`, rid)
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"quick_signoff_id": qsID,
		"message": fmt.Sprintf("快速签收完成（%d 件雪具转维修间后检），押金继续冻结，申诉期至 %s",
			pendingItems, deadline.Format("2006-01-02 15:04")),
		"contact_phone":   contact,
		"appeal_deadline": deadline.Format("2006-01-02 15:04"),
	})
}

// ---------- 高峰看板：归还队列 / 待检雪具 / 维修间容量 ----------

func (s *Server) peakBoard(w http.ResponseWriter, r *http.Request) {
	s.oneJSON(w, `SELECT json_build_object(
		'return_day_waiting', (SELECT count(*) FROM queue_tickets
			WHERE queue_type='return_day' AND status IN ('waiting','called','serving')),
		'return_night_waiting', (SELECT count(*) FROM queue_tickets
			WHERE queue_type='return_night' AND status IN ('waiting','called','serving')),
		'return_oldest_minutes', COALESCE((SELECT round(EXTRACT(EPOCH FROM (now()-min(created_at)))/60)
			FROM queue_tickets WHERE queue_type IN ('return_day','return_night') AND status='waiting'),0),
		'pending_inspection_items', (SELECT count(*) FROM equipment_items WHERE status='inspection'),
		'quick_signoff_pending', (SELECT count(*) FROM quick_signoffs WHERE status='pending_inspection'),
		'repair_active', (SELECT count(*) FROM repair_orders WHERE status IN ('queued','in_progress','waiting_part')),
		'repair_capacity', $1::int,
		'notify_pending', (SELECT count(*) FROM damage_notifications WHERE status='pending'),
		'appealing', (SELECT count(*) FROM damage_notifications WHERE status='appealed')
	)`, rules.RepairShopCapacity)
}

// ---------- 维修间：快速签收后检 ----------

func (s *Server) listPostInspections(w http.ResponseWriter, r *http.Request) {
	s.listJSON(w, `SELECT COALESCE(json_agg(t ORDER BY t.created_at), '[]') FROM (
		SELECT qs.id, qs.rental_id, qs.created_at, r.code AS rental_code, u.name AS guest_name,
		       qs.contact_phone, qs.shuttle_note, qs.status,
		       to_char(qs.appeal_deadline,'YYYY-MM-DD HH24:MI') AS appeal_deadline,
		       to_char(qs.created_at,'MM-DD HH24:MI') AS signed_at,
		       (SELECT json_agg(json_build_object(
		           'id', ri.id, 'equipment_type', ri.equipment_type,
		           'model_label', m.brand||' '||m.name||' '||m.size_label,
		           'qr_code', ei.qr_code, 'status', ri.status, 'deposit', ri.deposit
		         ) ORDER BY ri.id)
		         FROM rental_items ri
		         LEFT JOIN equipment_models m ON m.id=ri.model_id
		         LEFT JOIN equipment_items ei ON ei.id=ri.item_id
		         WHERE ri.rental_id=r.id) AS items
		FROM quick_signoffs qs
		JOIN rentals r ON r.id=qs.rental_id
		JOIN users u ON u.id=r.guest_id
		WHERE qs.status='pending_inspection'
	) t`)
}

type postInspectReq struct {
	RentalItemID int64  `json:"rental_item_id"`
	Result       string `json:"result"` // ok|damaged
	DamageType   string `json:"damage_type"`
	Severity     string `json:"severity"`
	Description  string `json:"description"`
}

// postInspectItem 维修间逐件后检：完好重新上架；损坏定级、开维修工单、保险立案，
// 但不扣押金——扣减要等游客确认赔付通知或申诉期届满。
func (s *Server) postInspectItem(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	qsID := urlVar(r, "id")
	var req postInspectReq
	if err := decode(r, &req); err != nil || req.RentalItemID == 0 {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if req.Result != "ok" && req.Result != "damaged" {
		writeError(w, http.StatusBadRequest, "后检结果需为 ok 或 damaged")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()

	var qsStatus, riStatus string
	var rentalID, itemID int64
	if err := tx.QueryRow(`SELECT qs.status, qs.rental_id, ri.status, COALESCE(ri.item_id,0)
		FROM quick_signoffs qs JOIN rental_items ri ON ri.rental_id=qs.rental_id
		WHERE qs.id=$1 AND ri.id=$2 FOR UPDATE`, qsID, req.RentalItemID).
		Scan(&qsStatus, &rentalID, &riStatus, &itemID); err != nil {
		writeError(w, http.StatusNotFound, "后检单或租赁明细不存在")
		return
	}
	if qsStatus != "pending_inspection" {
		writeError(w, http.StatusConflict, "该快速签收单后检已完成")
		return
	}
	if riStatus != "pending_inspection" {
		writeError(w, http.StatusConflict, "这件雪具已后检："+riStatus)
		return
	}

	if req.Result == "ok" {
		tx.Exec(`UPDATE equipment_items SET status='available' WHERE id=$1`, itemID)
		tx.Exec(`UPDATE rental_items SET status='returned_ok', returned_at=now() WHERE id=$1`,
			req.RentalItemID)
	} else {
		if _, ok := rules.DamageRuleOf(req.DamageType); !ok || req.DamageType == "lost" {
			writeError(w, http.StatusBadRequest, "损坏类型不正确")
			return
		}
		if req.Severity == "" {
			req.Severity = "medium"
		}
		var deposit float64
		tx.QueryRow(`SELECT deposit FROM rental_items WHERE id=$1`, req.RentalItemID).Scan(&deposit)
		deduction, rule := rules.DamageDeduction(req.DamageType, req.Severity, deposit)
		var dID int64
		if err := tx.QueryRow(`INSERT INTO damage_reports
			(rental_item_id, reporter_id, stage, damage_type, severity, description,
			 status, deduction, assessed_by)
			VALUES ($1,$2,'post_inspection',$3,$4,$5,'assessed',$6,$7) RETURNING id`,
			req.RentalItemID, c.UserID, req.DamageType, req.Severity, req.Description,
			deduction, c.UserID).Scan(&dID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		var roID int64
		tx.QueryRow(`INSERT INTO repair_orders(item_id, damage_id, issue, advice, status)
			VALUES ($1,$2,$3,$4,'queued') RETURNING id`,
			itemID, "后检发现-"+rule.Label+"："+req.Description, rule.Advice).Scan(&roID)
		finalRI := "repair"
		if rule.RetireItem {
			finalRI = "returned_damaged"
		}
		tx.Exec(`UPDATE damage_reports SET status='repair_created', repair_order_id=$1 WHERE id=$2`,
			roID, dID)
		tx.Exec(`UPDATE equipment_items SET status='repair' WHERE id=$1`, itemID)
		tx.Exec(`UPDATE rental_items SET status=$1 WHERE id=$2`, finalRI, req.RentalItemID)
		// 有保险自动立案（内部流程，不等游客确认）
		s.fileClaimIfCovered(tx, rentalID, dID, req.DamageType, deduction)
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	msg := "后检完成：雪具完好，已重新上架"
	if req.Result == "damaged" {
		msg = "已登记后检损坏并开维修工单；赔付通知将在后检完成后发送游客确认"
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

// completePostInspection 后检完成：有损坏 -> 生成赔付通知（押金继续冻结等游客确认）；
// 无损坏 -> 立即结算释放押金。
func (s *Server) completePostInspection(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	qsID := urlVar(r, "id")
	tx, _ := s.db.Begin()
	defer tx.Rollback()

	var qsStatus, contact string
	var rentalID, guestID int64
	var deadline time.Time
	if err := tx.QueryRow(`SELECT qs.status, qs.rental_id, qs.contact_phone, qs.appeal_deadline,
		r.guest_id FROM quick_signoffs qs JOIN rentals r ON r.id=qs.rental_id
		WHERE qs.id=$1 FOR UPDATE`, qsID).
		Scan(&qsStatus, &rentalID, &contact, &deadline, &guestID); err != nil {
		writeError(w, http.StatusNotFound, "快速签收单不存在")
		return
	}
	if qsStatus != "pending_inspection" {
		writeError(w, http.StatusConflict, "该快速签收单后检已完成")
		return
	}
	var pending int
	tx.QueryRow(`SELECT count(*) FROM rental_items WHERE rental_id=$1 AND status='pending_inspection'`,
		rentalID).Scan(&pending)
	if pending > 0 {
		writeError(w, http.StatusConflict, fmt.Sprintf("还有 %d 件雪具未后检", pending))
		return
	}

	// 为本单后检损坏逐条生成赔付通知（金额=定级扣减，申诉期限沿用签收时约定）
	type dmg struct {
		id        int64
		deduction float64
	}
	var damages []dmg
	rows, err := tx.Query(`SELECT d.id, d.deduction FROM damage_reports d
		JOIN rental_items ri ON ri.id=d.rental_item_id
		WHERE ri.rental_id=$1 AND d.stage='post_inspection'
		  AND NOT EXISTS(SELECT 1 FROM damage_notifications dn WHERE dn.damage_id=d.id)`, rentalID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for rows.Next() {
		var d dmg
		rows.Scan(&d.id, &d.deduction)
		damages = append(damages, d)
	}
	rows.Close()
	for _, d := range damages {
		if _, err := tx.Exec(`INSERT INTO damage_notifications
			(quick_signoff_id, damage_id, rental_id, guest_id, contact_phone, amount, appeal_deadline)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			qsID, d.id, rentalID, guestID, contact, d.deduction, deadline); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	tx.Exec(`UPDATE quick_signoffs SET status='inspected', inspected_by=$1, inspected_at=now()
		WHERE id=$2`, c.UserID, qsID)

	settled := false
	if len(damages) == 0 {
		// 后检无问题：直接结算释放押金
		if _, err := s.settleQuickSignoffTx(tx, qsID, c.UserID); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		settled = true
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	msg := fmt.Sprintf("后检完成：发现 %d 处损坏，赔付通知已发送游客（申诉期至 %s），押金待游客确认后结算",
		len(damages), deadline.Format("2006-01-02 15:04"))
	if settled {
		msg = "后检完成：雪具全部完好，押金已结算退还"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": msg, "notifications": len(damages), "settled": settled,
	})
}

// ---------- 游客：赔付通知确认 / 申诉 ----------

func (s *Server) myNotifications(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	s.listJSON(w, notificationSQL+` WHERE dn.guest_id=$1 ORDER BY dn.id DESC) t`, c.UserID)
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	s.listJSON(w, notificationSQL+` ORDER BY dn.id DESC) t`)
}

const notificationSQL = `SELECT COALESCE(json_agg(t), '[]') FROM (
	SELECT dn.id, dn.rental_id, r.code AS rental_code, dn.quick_signoff_id,
	       dn.amount, dn.status, dn.resolution, dn.appeal_text, dn.resolution_note,
	       dn.contact_phone, u.name AS guest_name,
	       to_char(dn.appeal_deadline,'YYYY-MM-DD HH24:MI') AS appeal_deadline,
	       to_char(dn.notified_at,'MM-DD HH24:MI') AS notified_at,
	       d.damage_type, d.severity, d.description, d.photos,
	       CASE d.damage_type
	         WHEN 'edge_damage' THEN '板刃损伤' WHEN 'base_scratch' THEN '板底划伤'
	         WHEN 'binding_issue' THEN '固定器故障' WHEN 'liner_wet' THEN '内衬潮湿'
	         WHEN 'shell_crack' THEN '壳体裂纹' WHEN 'strap_broken' THEN '绑带断裂'
	         WHEN 'lost' THEN '雪具丢失' ELSE '其他异常' END AS type_label
	FROM damage_notifications dn
	JOIN rentals r ON r.id=dn.rental_id
	JOIN users u ON u.id=dn.guest_id
	JOIN damage_reports d ON d.id=dn.damage_id
`

func (s *Server) confirmNotification(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	id := urlVar(r, "id")
	tx, _ := s.db.Begin()
	defer tx.Rollback()
	var status string
	var qsID int64
	if err := tx.QueryRow(`SELECT status, quick_signoff_id FROM damage_notifications
		WHERE id=$1 AND guest_id=$2 FOR UPDATE`, id, c.UserID).Scan(&status, &qsID); err != nil {
		writeError(w, http.StatusNotFound, "赔付通知不存在")
		return
	}
	if status != "pending" {
		writeError(w, http.StatusConflict, "该通知已处理："+status)
		return
	}
	tx.Exec(`UPDATE damage_notifications SET status='confirmed', responded_at=now() WHERE id=$1`, id)
	settled, err := s.trySettleLocked(tx, qsID, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	msg := "已确认赔付通知，将按通知金额从押金扣减"
	if settled {
		msg = "已确认赔付通知，押金已结算：扣减后剩余押金退还"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"message": msg, "settled": settled})
}

type appealReq struct {
	Reason string `json:"reason"`
}

func (s *Server) appealNotification(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	id := urlVar(r, "id")
	var req appealReq
	if err := decode(r, &req); err != nil || req.Reason == "" {
		writeError(w, http.StatusBadRequest, "请填写申诉理由")
		return
	}
	var status string
	var deadline time.Time
	if err := s.db.QueryRow(`SELECT status, appeal_deadline FROM damage_notifications
		WHERE id=$1 AND guest_id=$2`, id, c.UserID).Scan(&status, &deadline); err != nil {
		writeError(w, http.StatusNotFound, "赔付通知不存在")
		return
	}
	if status != "pending" {
		writeError(w, http.StatusConflict, "该通知已处理："+status)
		return
	}
	if time.Now().After(deadline) {
		writeError(w, http.StatusConflict, "申诉期已过，通知将按无异议处理")
		return
	}
	if _, ok := s.execOne(w, `UPDATE damage_notifications
		SET status='appealed', appeal_text=$1, responded_at=now()
		WHERE id=$2 AND status='pending'`, req.Reason, id); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "申诉已提交，柜台将复核后联系你；复核期间押金继续冻结",
	})
}

// ---------- 柜台：申诉处理 / 手动结算 ----------

type resolveAppealReq struct {
	Upheld bool   `json:"upheld"` // true=维持扣减 false=申诉成立取消扣减
	Note   string `json:"note"`
}

func (s *Server) resolveAppeal(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	id := urlVar(r, "id")
	var req resolveAppealReq
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()
	var status string
	var qsID, damageID int64
	if err := tx.QueryRow(`SELECT status, quick_signoff_id, damage_id FROM damage_notifications
		WHERE id=$1 FOR UPDATE`, id).Scan(&status, &qsID, &damageID); err != nil {
		writeError(w, http.StatusNotFound, "赔付通知不存在")
		return
	}
	if status != "appealed" {
		writeError(w, http.StatusConflict, "仅申诉中的通知可以复核处理")
		return
	}
	resolution := "upheld"
	if !req.Upheld {
		resolution = "cancelled"
	}
	tx.Exec(`UPDATE damage_notifications SET status='resolved', resolution=$1,
		resolved_by=$2, resolution_note=$3 WHERE id=$4`, resolution, c.UserID, req.Note, id)
	if !req.Upheld {
		// 申诉成立：取消扣减，关闭损坏报告并驳回已立案理赔
		tx.Exec(`UPDATE damage_reports SET status='closed' WHERE id=$1`, damageID)
		tx.Exec(`UPDATE insurance_claims SET status='rejected', note=note||'（游客申诉成立，扣减取消）',
			updated_at=now() WHERE damage_id=$1 AND status IN ('filed','under_review')`, damageID)
	}
	settled, err := s.trySettleLocked(tx, qsID, c.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	msg := "复核完成：维持原扣减，将按通知金额结算"
	if !req.Upheld {
		msg = "复核完成：申诉成立，该笔扣减已取消"
	}
	if settled {
		msg += "；押金已结算退还"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"message": msg, "settled": settled})
}

// settleQuickSignoff 手动结算：先把已过申诉期的通知标记为默认确认；
// 仍有未到期未确认/申诉中的通知时拒绝结算（赔付通知不能跳过游客确认）。
func (s *Server) settleQuickSignoff(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	qsID := urlVar(r, "id")
	tx, _ := s.db.Begin()
	defer tx.Rollback()
	tx.Exec(`UPDATE damage_notifications SET status='expired', responded_at=now()
		WHERE quick_signoff_id=$1 AND status='pending' AND appeal_deadline < now()`, qsID)
	var open int
	tx.QueryRow(`SELECT count(*) FROM damage_notifications
		WHERE quick_signoff_id=$1 AND status IN ('pending','appealed')`, qsID).Scan(&open)
	if open > 0 {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("仍有 %d 条赔付通知待游客确认/申诉中，申诉期届满或游客确认后才能结算", open))
		return
	}
	res, err := s.settleQuickSignoffTx(tx, qsID, c.UserID)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// trySettleLocked 游客确认/申诉处理后尝试结算：签收单已后检且无未决通知时结算。
func (s *Server) trySettleLocked(tx *sql.Tx, qsID, actorID int64) (bool, error) {
	var status string
	if err := tx.QueryRow(`SELECT status FROM quick_signoffs WHERE id=$1`, qsID).Scan(&status); err != nil {
		return false, err
	}
	if status != "inspected" {
		return false, nil
	}
	var open int
	if err := tx.QueryRow(`SELECT count(*) FROM damage_notifications
		WHERE quick_signoff_id=$1 AND status IN ('pending','appealed')`, qsID).Scan(&open); err != nil {
		return false, err
	}
	if open > 0 {
		return false, nil
	}
	_, err := s.settleQuickSignoffTx(tx, qsID, actorID)
	return err == nil, err
}

// settleQuickSignoffTx 结算快速签收单：按已确认/届满/复核维持的通知扣减押金，
// 剩余押金退还，租赁单与签收单归档。返回结算明细。
func (s *Server) settleQuickSignoffTx(tx *sql.Tx, qsID, actorID int64) (map[string]interface{}, error) {
	var rentalID int64
	var status string
	if err := tx.QueryRow(`SELECT rental_id, status FROM quick_signoffs
		WHERE id=$1 FOR UPDATE`, qsID).Scan(&rentalID, &status); err != nil {
		return nil, fmt.Errorf("快速签收单不存在")
	}
	if status == "settled" {
		return nil, fmt.Errorf("该快速签收单已结算")
	}
	if status != "inspected" {
		return nil, fmt.Errorf("维修间后检未完成，不能结算")
	}

	// 有效扣减通知：游客确认 / 申诉期届满 / 复核维持
	type note struct {
		id, damageID int64
		amount       float64
	}
	var notes []note
	rows, err := tx.Query(`SELECT id, damage_id, amount FROM damage_notifications
		WHERE quick_signoff_id=$1
		  AND (status IN ('confirmed','expired') OR (status='resolved' AND resolution='upheld'))`, qsID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n note
		rows.Scan(&n.id, &n.damageID, &n.amount)
		notes = append(notes, n)
	}
	rows.Close()
	for _, n := range notes {
		if _, err := tx.Exec(`INSERT INTO deposit_transactions
			(rental_id, type, amount, damage_id, note, created_by)
			VALUES ($1,'deduct_damage',$2,$3,'快速签收后检赔付（游客已确认或申诉期届满）',$4)`,
			rentalID, n.amount, n.damageID, nz(actorID)); err != nil {
			return nil, err
		}
	}

	var depositTotal, deducted, offsets float64
	tx.QueryRow(`SELECT deposit_total FROM rentals WHERE id=$1`, rentalID).Scan(&depositTotal)
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
	if _, err := tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, note, created_by)
		VALUES ($1,'refund',$2,'快速签收后检完成，申诉期结束，释放剩余押金',$3)`,
		rentalID, refund, nz(actorID)); err != nil {
		return nil, err
	}
	tx.Exec(`UPDATE rentals SET status='returned', returned_at=now() WHERE id=$1`, rentalID)
	tx.Exec(`UPDATE quick_signoffs SET status='settled', settled_at=now() WHERE id=$1`, qsID)
	return map[string]interface{}{
		"message":       "快速签收结算完成，押金已释放",
		"deposit_held":  depositTotal,
		"deducted":      deducted,
		"refund":        refund,
		"notifications": len(notes),
	}, nil
}

// ---------- 申诉期届满自动结算（后台每分钟扫描） ----------

func (s *Server) sweepLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		s.sweepExpiredNotifications()
	}
}

func (s *Server) sweepExpiredNotifications() {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	rows, err := tx.Query(`UPDATE damage_notifications SET status='expired', responded_at=now()
		WHERE status='pending' AND appeal_deadline < now() RETURNING quick_signoff_id`)
	if err != nil {
		return
	}
	seen := map[int64]bool{}
	var qsIDs []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil && !seen[id] {
			seen[id] = true
			qsIDs = append(qsIDs, id)
		}
	}
	rows.Close()
	for _, id := range qsIDs {
		s.trySettleLocked(tx, id, 0)
	}
	tx.Commit()
}

// ---------- 保险联动（后检损坏自动立案，与 assessDamage 共用） ----------

func (s *Server) fileClaimIfCovered(tx *sql.Tx, rentalID, dID int64, damageType string, deduction float64) {
	var productID, policyID int64
	coverErr := tx.QueryRow(`SELECT ip.product_id, ip.id FROM insurance_policies ip
		WHERE ip.rental_id=$1 AND ip.status='active' LIMIT 1`, rentalID).
		Scan(&productID, &policyID)
	if coverErr != nil {
		return
	}
	var deductible, capAmt float64
	var covers pq.StringArray
	if err := tx.QueryRow(`SELECT deductible, coverage_cap, covers FROM insurance_products WHERE id=$1`,
		productID).Scan(&deductible, &capAmt, &covers); err != nil {
		return
	}
	covered := false
	for _, c := range covers {
		if c == damageType {
			covered = true
			break
		}
	}
	res := rules.CalcClaim(deduction, deductible, capAmt, covered)
	var claimID int64
	tx.QueryRow(`INSERT INTO insurance_claims
		(policy_id, damage_id, amount_requested, deductible, amount_approved, status, note)
		VALUES ($1,$2,$3,$4,$5,'filed',$6) RETURNING id`,
		policyID, dID, res.Requested, deductible, res.Approved, res.Message).Scan(&claimID)
	tx.Exec(`UPDATE damage_reports SET claim_id=$1, status=
		CASE WHEN status='repair_created' THEN 'repair_created' ELSE 'assessed' END WHERE id=$2`,
		claimID, dID)
	tx.Exec(`UPDATE insurance_policies SET status='claimed' WHERE id=$1`, policyID)
}

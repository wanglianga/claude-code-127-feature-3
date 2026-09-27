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

// ---------- 排队 ----------

func (s *Server) counterQueue(w http.ResponseWriter, r *http.Request) {
	qtype := r.URL.Query().Get("type")
	types := []string{"pickup", "exchange", "return_day", "return_night", "damage"}
	in := map[string]bool{}
	if qtype != "" {
		in[qtype] = true
	} else {
		for _, t := range types {
			in[t] = true
		}
	}
	s.listJSON(w, `SELECT COALESCE(json_agg(t ORDER BY
		CASE t.status WHEN 'called' THEN 0 WHEN 'serving' THEN 0 ELSE 1 END,
		t.priority DESC, t.created_at), '[]') FROM (
		SELECT q.id, q.queue_type, q.status, q.ticket_no, q.priority, q.station,
		       q.rental_id, q.created_at, r.code AS rental_code, r.session_type,
		       u.id AS guest_id, u.name AS guest_name, u.phone AS guest_phone,
		       co.name AS course_name,
		       to_char(q.created_at,'HH24:MI') AS created_time,
		       to_char(q.called_at,'HH24:MI') AS called_time,
		       (SELECT json_agg(json_build_object(
		           'exchange_id', e.id, 'rental_item_id', e.rental_item_id,
		           'new_size', e.new_size, 'reason', e.reason,
		           'equipment_type', ri.equipment_type,
		           'current_label', m.brand||' '||m.name||' '||m.size_label
		         ))
		         FROM exchanges e
		         JOIN rental_items ri ON ri.id=e.rental_item_id
		         LEFT JOIN equipment_models m ON m.id=ri.model_id
		         WHERE ri.rental_id=q.rental_id AND e.status='requested') AS exchanges
		FROM queue_tickets q
		JOIN rentals r ON r.id=q.rental_id
		JOIN users u ON u.id=r.guest_id
		LEFT JOIN courses co ON co.id=r.course_id
		WHERE q.status IN ('waiting','called','serving')
		  AND q.queue_type = ANY($1)
	) t`, pq.Array(pqArray(types, in)))
}

// pqArray 返回按白名单过滤后的队列类型数组
func pqArray(all []string, in map[string]bool) []string {
	var out []string
	for _, t := range all {
		if in[t] {
			out = append(out, t)
		}
	}
	return out
}

type callReq struct {
	Type    string `json:"type"`
	Station string `json:"station"`
}

func (s *Server) callNext(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	var req callReq
	if err := decode(r, &req); err != nil || req.Type == "" {
		writeError(w, http.StatusBadRequest, "请指定队列")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()
	var id, rid int64
	var ticketNo string
	err := tx.QueryRow(`SELECT id, rental_id, ticket_no FROM queue_tickets
		WHERE queue_type=$1 AND status='waiting'
		ORDER BY priority DESC, created_at LIMIT 1 FOR UPDATE SKIP LOCKED`,
		req.Type).Scan(&id, &rid, &ticketNo)
	if err != nil {
		writeError(w, http.StatusNotFound, "队列中没有等待的游客")
		return
	}
	tx.Exec(`UPDATE queue_tickets SET status='called', called_at=now(), called_by=$1, station=$2 WHERE id=$3`,
		c.UserID, req.Station, id)
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ticket_id": id, "rental_id": rid, "ticket_no": ticketNo,
		"message": "已叫号 " + ticketNo,
	})
}

type finishTicketReq struct {
	Status string `json:"status"` // done|cancelled
}

func (s *Server) finishTicket(w http.ResponseWriter, r *http.Request) {
	id := urlVar(r, "id")
	var req finishTicketReq
	decode(r, &req)
	status := "done"
	if req.Status == "cancelled" {
		status = "cancelled"
	}
	if _, ok := s.execOne(w, `UPDATE queue_tickets SET status=$1, finished_at=now()
		WHERE id=$2 AND status IN ('called','serving','waiting')`, status, id); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "排队票已结束"})
}

// ---------- 扫码 ----------

type scanReq struct {
	QR string `json:"qr_code"`
}

func (s *Server) scanItem(w http.ResponseWriter, r *http.Request) {
	var req scanReq
	if err := decode(r, &req); err != nil || req.QR == "" {
		writeError(w, http.StatusBadRequest, "缺少二维码")
		return
	}
	s.oneJSON(w, `SELECT row_to_json(x) FROM (
		SELECT i.id, i.qr_code, i.serial_no, i.status, i.condition_note,
			m.id AS model_id, m.type AS equipment_type, m.brand, m.name, m.size_label,
			ri.id AS rental_item_id, ri.rental_id, ri.status AS rental_item_status,
			r.code AS rental_code, u.name AS holder_name
		FROM equipment_items i
		JOIN equipment_models m ON m.id=i.model_id
		LEFT JOIN rental_items ri ON ri.id=i.current_rental_item_id
		LEFT JOIN rentals r ON r.id=ri.rental_id
		LEFT JOIN users u ON u.id=r.guest_id
		WHERE i.qr_code=$1
	) x`, req.QR)
}

// ---------- 扫码出库 ----------

type checkoutReq struct {
	RentalID int64    `json:"rental_id"`
	QRCodes  []string `json:"qr_codes"` // 可逐件扫码核验；为空则直接整单出库
}

func (s *Server) checkout(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	var req checkoutReq
	if err := decode(r, &req); err != nil || req.RentalID == 0 {
		writeError(w, http.StatusBadRequest, "缺少租赁单")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()

	var status, session string
	if err := tx.QueryRow(`SELECT status, session_type FROM rentals WHERE id=$1 FOR UPDATE`,
		req.RentalID).Scan(&status, &session); err != nil {
		writeError(w, http.StatusNotFound, "租赁单不存在")
		return
	}
	if status != "reserved" {
		writeError(w, http.StatusConflict, "仅已预约待取具的租赁单可以出库")
		return
	}

	rows, err := tx.Query(`SELECT ri.id, ri.item_id, ri.deposit FROM rental_items ri
		WHERE ri.rental_id=$1 ORDER BY ri.id FOR UPDATE`, req.RentalID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type line struct {
		id, item int64
		deposit  float64
	}
	var lines []line
	for rows.Next() {
		var l line
		if err := rows.Scan(&l.id, &l.item, &l.deposit); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		lines = append(lines, l)
	}
	rows.Close()

	scanned := map[string]bool{}
	for _, q := range req.QRCodes {
		scanned[q] = true
	}
	var deposit float64
	for _, l := range lines {
		if l.item == 0 {
			writeError(w, http.StatusConflict, "存在未分配雪具，无法出库")
			return
		}
		var qr, istatus string
		if err := tx.QueryRow(`SELECT qr_code, status FROM equipment_items WHERE id=$1 FOR UPDATE`,
			l.item).Scan(&qr, &istatus); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if istatus != "reserved" {
			writeError(w, http.StatusConflict, "雪具 "+qr+" 状态异常："+istatus)
			return
		}
		if len(scanned) > 0 && !scanned[qr] {
			writeError(w, http.StatusBadRequest, "雪具未扫码核验："+qr)
			return
		}
		deposit += l.deposit
		tx.Exec(`UPDATE equipment_items SET status='out', rental_count=rental_count+1 WHERE id=$1`, l.item)
		tx.Exec(`UPDATE rental_items SET status='out' WHERE id=$1`, l.id)
	}

	due := time.Now().Add(8 * time.Hour)
	if session == "night" {
		due = time.Now().Add(4 * time.Hour)
	}
	tx.Exec(`UPDATE rentals SET status='equipped', pickup_at=now(), return_due_at=$1 WHERE id=$2`,
		due, req.RentalID)
	tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, note, created_by)
		VALUES ($1,'hold',$2,'出库冻结押金',$3)`, req.RentalID, deposit, c.UserID)
	tx.Exec(`UPDATE queue_tickets SET status='done', finished_at=now()
		WHERE rental_id=$1 AND queue_type='pickup' AND status IN ('waiting','called','serving')`,
		req.RentalID)
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":       "出库成功，雪具已交付使用",
		"deposit_hold":  deposit,
		"return_due_at": due.Format("2006-01-02 15:04"),
	})
}

// ---------- 柜台处理换码 ----------

type processExchangeReq struct {
	NewQR   string `json:"new_qr_code"` // 新雪具二维码
	Approve bool   `json:"approve"`
	Note    string `json:"note"`
}

func (s *Server) processExchange(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	exID := urlVar(r, "id")
	var req processExchangeReq
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()

	var riID, oldItem sql.NullInt64
	var status, newSize string
	var rentalID int64
	if err := tx.QueryRow(`SELECT e.rental_item_id, e.old_item_id, e.status, e.new_size,
		ri.rental_id FROM exchanges e JOIN rental_items ri ON ri.id=e.rental_item_id
		WHERE e.id=$1 FOR UPDATE`, exID).
		Scan(&riID, &oldItem, &status, &newSize, &rentalID); err != nil {
		writeError(w, http.StatusNotFound, "换码申请不存在")
		return
	}
	if status != "requested" {
		writeError(w, http.StatusConflict, "换码申请已处理")
		return
	}
	if !req.Approve {
		tx.Exec(`UPDATE exchanges SET status='rejected', processed_by=$1, processed_at=now() WHERE id=$2`,
			c.UserID, exID)
		tx.Exec(`UPDATE queue_tickets SET status='done', finished_at=now()
			WHERE rental_id=$1 AND queue_type='exchange' AND status IN ('waiting','called','serving')`, rentalID)
		tx.Commit()
		writeJSON(w, http.StatusOK, map[string]string{"message": "已驳回换码申请：" + req.Note})
		return
	}

	// 确定新雪具与新型号。扫码：柜台人工核验，允许跨尺码型号；
	// 未扫码：先找期望尺码的同类型型号，再退回原型号。
	var newItem, newModel, oldModel int64
	if req.NewQR != "" {
		var st string
		err := tx.QueryRow(`SELECT i.id, i.status, i.model_id FROM equipment_items i WHERE qr_code=$1`,
			req.NewQR).Scan(&newItem, &st, &newModel)
		if err != nil {
			writeError(w, http.StatusBadRequest, "新雪具二维码无效")
			return
		}
		if st != "available" {
			writeError(w, http.StatusConflict, "新雪具当前不可用："+st)
			return
		}
	} else {
		tx.QueryRow(`SELECT model_id FROM rental_items WHERE id=$1`, riID.Int64).Scan(&oldModel)
		newModel = oldModel
		if newSize != "" {
			var etype string
			tx.QueryRow(`SELECT equipment_type FROM rental_items WHERE id=$1`, riID.Int64).Scan(&etype)
			err := tx.QueryRow(`SELECT m.id FROM equipment_models m
				WHERE m.active AND m.type=$1 AND m.size_label=$2
				  AND EXISTS(SELECT 1 FROM equipment_items i
				            WHERE i.model_id=m.id AND i.status='available')
				ORDER BY m.id LIMIT 1`, etype, newSize).Scan(&newModel)
			if err != nil {
				newModel = oldModel // 期望尺码无库存，退回同型号换一件
			}
		}
		err := tx.QueryRow(`SELECT i.id FROM equipment_items i
			WHERE i.model_id=$1 AND i.status='available'
			ORDER BY i.id LIMIT 1 FOR UPDATE SKIP LOCKED`, newModel).Scan(&newItem)
		if err != nil {
			writeError(w, http.StatusConflict, "暂无可换库存，请稍后或留下联系方式")
			return
		}
	}

	// 旧件转检查（内衬/合脚问题高发，先检查再上架）
	if oldItem.Valid {
		tx.Exec(`UPDATE equipment_items SET status='inspection', current_rental_item_id=NULL WHERE id=$1`,
			oldItem.Int64)
	}
	tx.Exec(`UPDATE equipment_items SET status='out', rental_count=rental_count+1,
		current_rental_item_id=$1 WHERE id=$2`, riID.Int64, newItem)

	// 跨型号换码：同步明细型号/建议尺码/租金押金，并调整整单合计
	var newSizeLabel string
	var newFee, newDeposit float64
	var session string
	tx.QueryRow(`SELECT size_label, rental_fee, deposit FROM equipment_models WHERE id=$1`,
		newModel).Scan(&newSizeLabel, &newFee, &newDeposit)
	tx.QueryRow(`SELECT session_type FROM rentals WHERE id=$1`, rentalID).Scan(&session)
	if session == "night" {
		newFee = round2f(newFee * (1 + rules.NightPremiumRate))
	}
	if newModel != oldModel {
		tx.Exec(`UPDATE rental_items SET model_id=$1, item_id=$2, status='out',
			recommended_size=$3, fee=$4, deposit=$5, exchanged_from_item_id=$6 WHERE id=$7`,
			newModel, newItem, newSizeLabel, newFee, newDeposit, oldItem.Int64, riID.Int64)
		tx.Exec(`UPDATE rentals r SET
			fee_total=(SELECT COALESCE(sum(fee),0) FROM rental_items WHERE rental_id=r.id),
			deposit_total=(SELECT COALESCE(sum(deposit),0) FROM rental_items WHERE rental_id=r.id)
			WHERE r.id=$1`, rentalID)
	} else {
		tx.Exec(`UPDATE rental_items SET item_id=$1, status='out', exchanged_from_item_id=$2 WHERE id=$3`,
			newItem, oldItem.Int64, riID.Int64)
	}
	tx.Exec(`UPDATE exchanges SET status='done', new_item_id=$1, processed_by=$2, processed_at=now()
		WHERE id=$3`, newItem, c.UserID, exID)
	tx.Exec(`UPDATE queue_tickets SET status='done', finished_at=now()
		WHERE rental_id=$1 AND queue_type='exchange' AND status IN ('waiting','called','serving')`, rentalID)
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":         "换码完成，旧雪具已收回检查，新尺码：" + newSizeLabel,
		"new_item_id":     newItem,
		"new_size":        newSizeLabel,
		"congestion_note": "该课程时段换码已计入拥堵统计",
	})
}

// ---------- 游客中途报损的柜台评定 ----------

type assessDuringReq struct {
	DamageType  string   `json:"damage_type"` // 可覆盖游客自报类型
	Severity    string   `json:"severity"`
	Description string   `json:"description"`
	Photos      []string `json:"photos"`
}

// assessDuringRental 柜台对游客使用中上报的损坏定级：扣减押金依据、
// 生成维修工单、有保险自动立案。损坏雪具收回维修，游客可再申请换码领取新雪具。
func (s *Server) assessDuringRental(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	dID := urlVar(r, "id")
	var req assessDuringReq
	decode(r, &req)

	tx, _ := s.db.Begin()
	defer tx.Rollback()

	var riID, itemID, rentalID, reporter int64
	var dStatus, damageType, severity, description string
	var photos []byte
	if err := tx.QueryRow(`SELECT d.status, d.rental_item_id, COALESCE(ri.item_id,0),
		ri.rental_id, d.reporter_id, d.damage_type, d.severity, d.description, d.photos
		FROM damage_reports d JOIN rental_items ri ON ri.id=d.rental_item_id
		WHERE d.id=$1 FOR UPDATE OF d`, dID).
		Scan(&dStatus, &riID, &itemID, &rentalID, &reporter,
			&damageType, &severity, &description, &photos); err != nil {
		writeError(w, http.StatusNotFound, "损坏报告不存在")
		return
	}
	if dStatus != "reported" {
		writeError(w, http.StatusConflict, "该损坏报告已评定："+dStatus)
		return
	}
	if req.DamageType != "" {
		damageType = req.DamageType
	}
	if req.Severity != "" {
		severity = req.Severity
	}
	if req.Description != "" {
		description = req.Description
	}
	if len(req.Photos) > 0 {
		photos, _ = json.Marshal(req.Photos)
	}

	inspect := inspectReq{
		Result:      "damaged",
		DamageType:  damageType,
		Severity:    severity,
		Description: description,
		Photos:      req.Photos,
	}
	if damageType == "lost" {
		inspect.Result = "lost"
		inspect.Severity = "lost"
	}

	// 游客中途报损已在 damage_reports 中占一行：先删掉由 assessDamage 统一重建，保证编号与状态一致
	tx.Exec(`DELETE FROM damage_reports WHERE id=$1`, dID)
	deduction, newDID, err := s.assessDamage(tx, "during_rental", rentalID, riID, itemID, inspect, c.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tx.Exec(`UPDATE queue_tickets SET status='done', finished_at=now()
		WHERE rental_id=$1 AND queue_type='damage' AND status IN ('waiting','called','serving')`, rentalID)
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":        "损坏已评定，维修工单与保险立案已生成，扣减将在归还时结算",
		"damage_id":      newDID,
		"deduction_hint": deduction,
	})
}

// ---------- 归还验损 ----------

type inspectReq struct {
	RentalItemID int64    `json:"rental_item_id"`
	Result       string   `json:"result"` // ok|damaged|lost
	DamageType   string   `json:"damage_type"`
	Severity     string   `json:"severity"`
	Description  string   `json:"description"`
	Photos       []string `json:"photos"`
}

// assessDamage 评定扣减、生成维修工单与保险理赔。
// stage="during_rental" 时旧件转维修/报废并自动换发同型号雪具，游客可继续上课。
func (s *Server) assessDamage(tx *sql.Tx, stage string, rentalID, riID, itemID int64, req inspectReq, reporter int64) (float64, int64, error) {
	if req.Result == "ok" {
		return 0, 0, nil
	}
	var modelID int64
	var deposit float64
	if err := tx.QueryRow(`SELECT model_id, deposit FROM rental_items WHERE id=$1`, riID).
		Scan(&modelID, &deposit); err != nil {
		return 0, 0, err
	}
	damageType := req.DamageType
	severity := req.Severity
	if req.Result == "lost" {
		damageType, severity = "lost", "lost"
	}
	if _, ok := rules.DamageRuleOf(damageType); !ok {
		return 0, 0, fmt.Errorf("损坏类型不正确")
	}
	if severity == "" {
		severity = "medium"
	}
	deduction, rule := rules.DamageDeduction(damageType, severity, deposit)
	photos, _ := json.Marshal(req.Photos)

	var dID int64
	if err := tx.QueryRow(`INSERT INTO damage_reports
		(rental_item_id, reporter_id, stage, damage_type, severity, description, photos,
		 status, deduction, assessed_by)
		VALUES ($1,$2,$3,$4,$5,$6,COALESCE($7,'[]'::jsonb),'assessed',$8,$9) RETURNING id`,
		riID, reporter, stage, damageType, severity, req.Description, photos, deduction, reporter).Scan(&dID); err != nil {
		return 0, 0, err
	}

	if req.Result == "lost" {
		tx.Exec(`UPDATE equipment_items SET status='retired', current_rental_item_id=NULL WHERE id=$1`, itemID)
		tx.Exec(`UPDATE rental_items SET status='lost' WHERE id=$1`, riID)
		tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, damage_id, note, created_by)
			VALUES ($1,'deduct_lost',$2,$3,'雪具丢失按押金全额扣减',$4)`,
			rentalID, deduction, dID, reporter)
		if stage == "at_return" {
			tx.Exec(`UPDATE damage_reports SET status='closed' WHERE id=$1`, dID)
		}
	} else {
		// 生成维修工单（维修间接单）
		var roID int64
		tx.QueryRow(`INSERT INTO repair_orders(item_id, damage_id, issue, advice, status, repair_cost, technician_id)
			VALUES ($1,$2,$3,$4,'queued',0,NULL) RETURNING id`,
			itemID, rule.Label+"："+req.Description, rule.Advice).Scan(&roID)
		tx.Exec(`UPDATE equipment_items SET status='repair', current_rental_item_id=NULL WHERE id=$1`, itemID)
		if stage == "at_return" {
			finalRIStatus := "repair"
			if rule.RetireItem {
				finalRIStatus = "returned_damaged"
			}
			tx.Exec(`UPDATE rental_items SET status=$1 WHERE id=$2`, finalRIStatus, riID)
			tx.Exec(`UPDATE damage_reports SET status='repair_created', repair_order_id=$1 WHERE id=$2`, roID, dID)
		} else {
			// 中途损坏：旧件已转维修，稍后自动换发，损坏报告保持 repair_created
			tx.Exec(`UPDATE damage_reports SET status='repair_created', repair_order_id=$1 WHERE id=$2`, roID, dID)
		}
		tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, damage_id, note, created_by)
			VALUES ($1,'deduct_damage',$2,$3,$4,$5)`,
			rentalID, deduction, dID, rule.Label+" 按押金比例 "+fmt.Sprintf("%.0f%%", rule.DeductRate*100), reporter)
	}

	// 中途损坏：旧件转维修，自动换发一件同型号可用雪具，保证课程继续（丢失不补发）
	if stage == "during_rental" && req.Result != "lost" {
		var newItem int64
		if err := tx.QueryRow(`SELECT id FROM equipment_items
			WHERE model_id=$1 AND status='available' ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`,
			modelID).Scan(&newItem); err != nil {
			// 无库存：明细维持无雪具状态，柜台可改约/人工处理
			tx.Exec(`UPDATE rental_items SET item_id=NULL WHERE id=$1`, riID)
		} else {
			tx.Exec(`UPDATE equipment_items SET status='out', rental_count=rental_count+1,
				current_rental_item_id=$1 WHERE id=$2`, riID, newItem)
			tx.Exec(`UPDATE rental_items SET item_id=$1, status='out',
				exchanged_from_item_id=COALESCE(exchanged_from_item_id,$2) WHERE id=$3`,
				newItem, itemID, riID)
		}
	}

	// 保险联动：有效保单且覆盖该损坏类型 -> 自动立案
	s.fileClaimIfCovered(tx, rentalID, dID, damageType, deduction)
	return deduction, dID, nil
}

func (s *Server) inspectReturnItem(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	rid := urlVar(r, "rid")
	var req inspectReq
	if err := decode(r, &req); err != nil || req.RentalItemID == 0 {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()

	var rStatus string
	var itemID sql.NullInt64
	var riStatus string
	if err := tx.QueryRow(`SELECT r.status, ri.item_id, ri.status FROM rentals r
		JOIN rental_items ri ON ri.rental_id=r.id
		WHERE r.id=$1 AND ri.id=$2 FOR UPDATE`, rid, req.RentalItemID).
		Scan(&rStatus, &itemID, &riStatus); err != nil {
		writeError(w, http.StatusNotFound, "租赁明细不存在")
		return
	}
	if rStatus != "return_pending" {
		writeError(w, http.StatusConflict, "租赁单不在归还验损环节")
		return
	}
	if riStatus != "out" {
		writeError(w, http.StatusConflict, "这件雪具已验过："+riStatus)
		return
	}

	if req.Result == "ok" {
		tx.Exec(`UPDATE equipment_items SET status='available', current_rental_item_id=NULL WHERE id=$1`,
			itemID.Int64)
		tx.Exec(`UPDATE rental_items SET status='returned_ok', returned_at=now() WHERE id=$1`,
			req.RentalItemID)
	} else {
		if _, _, err := s.assessDamage(tx, "at_return", rid, req.RentalItemID, itemID.Int64, req, c.UserID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	msg := "验具完成：雪具完好，已重新上架"
	if req.Result == "damaged" {
		msg = "已登记损坏、生成维修工单，押金扣减与保险立案已同步"
	} else if req.Result == "lost" {
		msg = "已按丢失登记，押金全额扣减"
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

// ---------- 完成归还、结算押金 ----------

func (s *Server) finishReturn(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	rid := urlVar(r, "rid")
	tx, _ := s.db.Begin()
	defer tx.Rollback()

	var status string
	var due interface{}
	var depositTotal float64
	if err := tx.QueryRow(`SELECT status, return_due_at, deposit_total
		FROM rentals WHERE id=$1 FOR UPDATE`, rid).
		Scan(&status, &due, &depositTotal); err != nil {
		writeError(w, http.StatusNotFound, "租赁单不存在")
		return
	}
	if status != "return_pending" {
		writeError(w, http.StatusConflict, "当前状态不能完成归还")
		return
	}
	var pending int
	tx.QueryRow(`SELECT count(*) FROM rental_items WHERE rental_id=$1 AND status='out'`, rid).Scan(&pending)
	if pending > 0 {
		writeError(w, http.StatusConflict, fmt.Sprintf("还有 %d 件雪具未验损", pending))
		return
	}

	// 超时扣减（课程延时已通过 extensions 调整过 due；这里兜底实际归还时间）
	lateHours := 0.0
	if t, ok := due.(time.Time); ok && !t.IsZero() {
		if d := time.Since(t); d > 15*time.Minute {
			lateHours = d.Hours()
		}
	}
	lateFee := 0.0
	if lateHours > 0 {
		var feeBase float64
		tx.QueryRow(`SELECT fee_total FROM rentals WHERE id=$1`, rid).Scan(&feeBase)
		lateFee = rules.LateFee(lateHours, feeBase)
		tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, note, created_by)
			VALUES ($1,'deduct_late',$2,$3,$4)`, rid, lateFee,
			fmt.Sprintf("超时 %.1f 小时", lateHours), c.UserID)
	}

	var deducted float64
	tx.QueryRow(`SELECT COALESCE(sum(amount),0) FROM deposit_transactions
		WHERE rental_id=$1 AND type LIKE 'deduct%'`, rid).Scan(&deducted)
	var offsets float64
	tx.QueryRow(`SELECT COALESCE(sum(amount),0) FROM deposit_transactions
		WHERE rental_id=$1 AND type='insurance_offset'`, rid).Scan(&offsets)
	held := depositTotal
	refund := held - deducted + offsets
	if refund < 0 {
		refund = 0
	}
	if refund > held {
		refund = held
	}
	tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, note, created_by)
		VALUES ($1,'refund',$2,'归还结算后退还原冻结押金（保险赔付到账后另行冲抵）',$3)`,
		rid, refund, c.UserID)

	var pickupT, returnT time.Time
	tx.QueryRow(`SELECT COALESCE(pickup_at, created_at), now() FROM rentals WHERE id=$1`, rid).Scan(&pickupT, &returnT)
	usedHours := returnT.Sub(pickupT).Hours()
	tx.Exec(`UPDATE rentals SET status='returned', returned_at=now(),
		notes=COALESCE(NULLIF(notes,''),'') || $2 WHERE id=$1`,
		rid, fmt.Sprintf("使用时长 %.1f 小时；", usedHours))
	tx.Exec(`UPDATE queue_tickets SET status='done', finished_at=now()
		WHERE rental_id=$1 AND queue_type IN ('return_day','return_night','damage')
		AND status IN ('waiting','called','serving')`, rid)
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":        "归还完成，押金已结算",
		"deposit_held":   held,
		"deducted":       deducted,
		"late_fee":       lateFee,
		"refund":         refund,
		"used_hours":     round2f(usedHours),
		"insurance_hint": "如有保险理赔，审核赔付后金额将另行返还，可在订单中查看依据",
	})
}

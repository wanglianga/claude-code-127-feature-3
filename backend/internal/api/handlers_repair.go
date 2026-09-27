package api

import (
	"net/http"
)

func (s *Server) repairOrders(w http.ResponseWriter, r *http.Request) {
	activeOnly := r.URL.Query().Get("all") != "1"
	q := `SELECT COALESCE(json_agg(t ORDER BY t.created_at DESC), '[]') FROM (
		SELECT ro.id, ro.status, ro.issue, ro.advice, ro.repair_cost,
		       to_char(ro.created_at,'MM-DD HH24:MI') AS created_at,
		       to_char(ro.finished_at,'MM-DD HH24:MI') AS finished_at,
		       i.id AS item_id, i.qr_code, i.serial_no, i.rental_count,
		       m.brand, m.name, m.size_label, m.type AS equipment_type,
		       d.id AS damage_id, d.damage_type, d.severity, d.photos,
		       u.name AS technician_name
		FROM repair_orders ro
		JOIN equipment_items i ON i.id=ro.item_id
		JOIN equipment_models m ON m.id=i.model_id
		LEFT JOIN damage_reports d ON d.id=ro.damage_id
		LEFT JOIN users u ON u.id=ro.technician_id
	`
	if activeOnly {
		q += " WHERE ro.status IN ('queued','in_progress','waiting_part')"
	}
	q += ") t"
	s.listJSON(w, q)
}

type repairUpdateReq struct {
	Status     string  `json:"status"`
	Advice     string  `json:"advice"`
	RepairCost float64 `json:"repair_cost"`
}

func (s *Server) updateRepairOrder(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	id := urlVar(r, "id")
	var req repairUpdateReq
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	valid := map[string]bool{"queued": true, "in_progress": true, "waiting_part": true,
		"done": true, "scrapped": true}
	if !valid[req.Status] {
		writeError(w, http.StatusBadRequest, "工单状态不正确")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()
	var itemID, damageID int64
	var oldStatus string
	if err := tx.QueryRow(`SELECT item_id, COALESCE(damage_id,0), status FROM repair_orders
		WHERE id=$1 FOR UPDATE`, id).Scan(&itemID, &damageID, &oldStatus); err != nil {
		writeError(w, http.StatusNotFound, "工单不存在")
		return
	}
	tx.Exec(`UPDATE repair_orders SET status=$1, advice=COALESCE(NULLIF($2,''),advice),
		repair_cost=$3, technician_id=$4,
		finished_at=CASE WHEN $1 IN ('done','scrapped') THEN now() ELSE finished_at END
		WHERE id=$5`, req.Status, req.Advice, req.RepairCost, c.UserID, id)

	switch req.Status {
	case "in_progress":
		tx.Exec(`UPDATE equipment_items SET status='repair' WHERE id=$1`, itemID)
	case "done":
		tx.Exec(`UPDATE equipment_items SET status='available' WHERE id=$1`, itemID)
		tx.Exec(`UPDATE rental_items SET status='returned_damaged'
			WHERE status='repair' AND item_id=$1`, itemID)
		if damageID > 0 {
			// 后检报告在赔付通知未决前保持打开，游客确认/申诉复核后由通知链路闭环
			tx.Exec(`UPDATE damage_reports SET status='closed' WHERE id=$1 AND claim_id IS NULL
				AND NOT EXISTS(SELECT 1 FROM damage_notifications dn
					WHERE dn.damage_id=damage_reports.id AND dn.status IN ('pending','appealed'))`, damageID)
		}
	case "scrapped":
		tx.Exec(`UPDATE equipment_items SET status='retired' WHERE id=$1`, itemID)
		tx.Exec(`UPDATE rental_items SET status='returned_damaged'
			WHERE status='repair' AND item_id=$1`, itemID)
		if damageID > 0 {
			tx.Exec(`UPDATE damage_reports SET status='closed' WHERE id=$1
				AND NOT EXISTS(SELECT 1 FROM damage_notifications dn
					WHERE dn.damage_id=damage_reports.id AND dn.status IN ('pending','appealed'))`, damageID)
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "工单已更新，雪具状态已同步：" + req.Status,
	})
}

// ---------- 高损耗型号保养提醒 ----------

func (s *Server) listMaintenance(w http.ResponseWriter, r *http.Request) {
	s.listJSON(w, `SELECT COALESCE(json_agg(t ORDER BY t.created_at DESC), '[]') FROM (
		SELECT mr.id, mr.model_id, mr.reason, mr.metrics, mr.action, mr.status,
		       m.brand, m.name, m.size_label, m.type AS equipment_type,
		       to_char(mr.created_at,'MM-DD HH24:MI') AS created_at
		FROM maintenance_reminders mr
		JOIN equipment_models m ON m.id=mr.model_id
	) t`)
}

// 统计阈值：近 30 天同型号损坏 >=3 起，或损坏率（损坏件次/出库件次）>= 20%
func (s *Server) generateMaintenance(w http.ResponseWriter, r *http.Request) {
	tx, _ := s.db.Begin()
	defer tx.Rollback()

	rows, err := tx.Query(`
		SELECT m.id, m.brand, m.name, m.size_label, m.type,
		       (SELECT count(*) FROM equipment_items i WHERE i.model_id=m.id) AS stock,
		       COALESCE((SELECT count(*) FROM rental_items ri
		         JOIN damage_reports d ON d.rental_item_id=ri.id
		         WHERE ri.model_id=m.id AND d.created_at > now()-interval '30 days'),0) AS damages,
		       COALESCE((SELECT count(*) FROM rental_items ri
		         JOIN damage_reports d ON d.rental_item_id=ri.id
		         WHERE ri.model_id=m.id AND d.damage_type='lost'
		           AND d.created_at > now()-interval '30 days'),0) AS lost,
		       COALESCE((SELECT count(*) FROM rental_items ri WHERE ri.model_id=m.id
		         AND ri.created_at > now()-interval '30 days'),0) AS rentals,
		       COALESCE((SELECT sum(ro.repair_cost) FROM repair_orders ro
		         JOIN equipment_items i ON i.id=ro.item_id
		         WHERE i.model_id=m.id),0) AS repair_cost
		FROM equipment_models m
		WHERE m.active
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type stat struct {
		modelID                       int64
		brand, name, size, etype      string
		stock, damages, lost, rentals int
		repairCost                    float64
	}
	var stats []stat
	for rows.Next() {
		var st stat
		if err := rows.Scan(&st.modelID, &st.brand, &st.name, &st.size, &st.etype,
			&st.stock, &st.damages, &st.lost, &st.rentals, &st.repairCost); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		stats = append(stats, st)
	}
	rows.Close()

	created := 0
	for _, st := range stats {
		rate := 0.0
		if st.rentals > 0 {
			rate = float64(st.damages) / float64(st.rentals)
		}
		if st.damages < 3 && rate < 0.20 {
			continue
		}
		var exists int
		tx.QueryRow(`SELECT count(*) FROM maintenance_reminders WHERE model_id=$1 AND status='open'`,
			st.modelID).Scan(&exists)
		if exists > 0 {
			continue
		}
		reason := "近30天高损耗：损坏/异常报告偏多"
		action := "安排整批刃口打磨/绑定器调校/内衬烘干，必要时提前淘汰"
		if st.lost > 0 {
			reason = "近30天发生雪具丢失，同型号需重点盘点"
			action = "立即盘点在库与在租数量，核查二维码标签是否牢固"
		} else if rate >= 0.20 {
			reason = "近30天损坏率超过 20%"
		}
		if _, err := tx.Exec(`INSERT INTO maintenance_reminders(model_id, reason, action, metrics)
			VALUES ($1,$2,$3,json_build_object(
				'damages',$4::int,'lost',$5::int,'rentals',$6::int,
				'damage_rate',$7::float8,'repair_cost',$8::float8,'stock',$9::int))`,
			st.modelID, reason, action, st.damages, st.lost, st.rentals,
			int(rate*1000)/10.0, st.repairCost, st.stock); err != nil {
			tx.Rollback()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		created++
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"created": created, "message": "保养提醒已按近30天损耗数据生成",
	})
}

type resolveMaintReq struct {
	Action string `json:"action"` // done|dismissed
}

func (s *Server) resolveMaintenance(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	id := urlVar(r, "id")
	var req resolveMaintReq
	decode(r, &req)
	status := "done"
	if req.Action == "dismissed" {
		status = "dismissed"
	}
	if _, ok := s.execOne(w, `UPDATE maintenance_reminders SET status=$1, resolved_by=$2, resolved_at=now()
		WHERE id=$3 AND status='open'`, status, c.UserID, id); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "提醒已处理"})
}

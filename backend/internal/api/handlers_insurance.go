package api

import (
	"database/sql"
	"net/http"

	"skirental/internal/rules"
)

func (s *Server) listClaims(w http.ResponseWriter, r *http.Request) {
	s.listJSON(w, `SELECT COALESCE(json_agg(t ORDER BY t.created_at DESC), '[]') FROM (
		SELECT ic.id, ic.status, ic.amount_requested, ic.deductible, ic.amount_approved,
		       ic.payout, ic.note,
		       to_char(ic.created_at,'MM-DD HH24:MI') AS created_at,
		       to_char(ic.updated_at,'MM-DD HH24:MI') AS updated_at,
		       ip.id AS policy_id, p.name AS product_name, ip.premium,
		       r.id AS rental_id, r.code AS rental_code,
		       u.name AS guest_name, u.phone AS guest_phone,
		       d.damage_type, d.severity, d.photos,
		       CASE d.damage_type
		         WHEN 'edge_damage' THEN '板刃损伤' WHEN 'base_scratch' THEN '板底划伤'
		         WHEN 'binding_issue' THEN '固定器故障' WHEN 'liner_wet' THEN '内衬潮湿'
		         WHEN 'shell_crack' THEN '壳体裂纹' WHEN 'strap_broken' THEN '绑带断裂'
		         WHEN 'lost' THEN '雪具丢失' ELSE '其他异常' END AS damage_label
		FROM insurance_claims ic
		JOIN insurance_policies ip ON ip.id=ic.policy_id
		JOIN insurance_products p ON p.id=ip.product_id
		JOIN rentals r ON r.id=ip.rental_id
		JOIN users u ON u.id=r.guest_id
		LEFT JOIN damage_reports d ON d.id=ic.damage_id
	) t`)
}

func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	s.listJSON(w, `SELECT COALESCE(json_agg(t ORDER BY t.created_at DESC), '[]') FROM (
		SELECT ip.id, ip.status, ip.premium, ip.created_at,
		       p.name AS product_name, r.id AS rental_id, r.code AS rental_code,
		       u.name AS guest_name
		FROM insurance_policies ip
		JOIN insurance_products p ON p.id=ip.product_id
		JOIN rentals r ON r.id=ip.rental_id
		JOIN users u ON u.id=r.guest_id
	) t`)
}

type reviewReq struct {
	Status string  `json:"status"` // under_review|approved|rejected
	Note   string  `json:"note"`
	Payout float64 `json:"payout"` // 核定理赔金额（approved 时）
}

// 保险理赔状态同步：filed -> under_review -> approved -> paid（或 rejected）
func (s *Server) reviewClaim(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	id := urlVar(r, "id")
	var req reviewReq
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()

	var status string
	var requested, payout, capAmt float64
	var damageID, rentalID, policyID sql.NullInt64
	if err := tx.QueryRow(`SELECT ic.status, ic.amount_requested, ic.payout, p.coverage_cap,
		ic.damage_id, ip.rental_id, ic.policy_id
		FROM insurance_claims ic
		JOIN insurance_policies ip ON ip.id=ic.policy_id
		JOIN insurance_products p ON p.id=ip.product_id
		WHERE ic.id=$1 FOR UPDATE`, id).
		Scan(&status, &requested, &payout, &capAmt, &damageID, &rentalID, &policyID); err != nil {
		writeError(w, http.StatusNotFound, "理赔单不存在")
		return
	}

	switch req.Status {
	case "under_review":
		if status != "filed" {
			writeError(w, http.StatusConflict, "仅立案状态可进入审核")
			return
		}
		tx.Exec(`UPDATE insurance_claims SET status='under_review', adjuster_id=$1, note=$2, updated_at=now()
			WHERE id=$3`, c.UserID, req.Note, id)
	case "rejected":
		if status != "filed" && status != "under_review" {
			writeError(w, http.StatusConflict, "当前状态不能拒赔")
			return
		}
		tx.Exec(`UPDATE insurance_claims SET status='rejected', adjuster_id=$1, note=$2, updated_at=now()
			WHERE id=$3`, c.UserID, req.Note, id)
		if damageID.Valid {
			tx.Exec(`UPDATE damage_reports SET status='closed' WHERE id=$1`, damageID.Int64)
		}
	case "approved":
		if status != "filed" && status != "under_review" {
			writeError(w, http.StatusConflict, "当前状态不能核定赔付")
			return
		}
		pay := req.Payout
		if pay <= 0 {
			// 默认按规则试算：赔 = 扣减 - 免赔（上限保额）
			var deductible float64
			tx.QueryRow(`SELECT deductible FROM insurance_claims WHERE id=$1`, id).Scan(&deductible)
			pay = rules.CalcClaim(requested, deductible, capAmt, true).Payout
		}
		if pay > capAmt {
			writeError(w, http.StatusBadRequest, "赔付金额超过保额上限")
			return
		}
		tx.Exec(`UPDATE insurance_claims SET status='approved', payout=$1, amount_approved=
			CASE WHEN $2 > 0 THEN $2 ELSE amount_approved END,
			adjuster_id=$3, note=COALESCE(NULLIF($4,''),note), updated_at=now()
			WHERE id=$5`, pay, requested, c.UserID, req.Note, id)
	default:
		writeError(w, http.StatusBadRequest, "未知状态")
		return
	}
	_ = payout
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "理赔状态已同步：" + req.Status})
}

// 赔付到账：冲抵已扣押金，游客订单可见依据
func (s *Server) payClaim(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	id := urlVar(r, "id")
	tx, _ := s.db.Begin()
	defer tx.Rollback()

	var status string
	var payout float64
	var damageID, rentalID sql.NullInt64
	if err := tx.QueryRow(`SELECT ic.status, ic.payout, ic.damage_id, ip.rental_id
		FROM insurance_claims ic JOIN insurance_policies ip ON ip.id=ic.policy_id
		WHERE ic.id=$1 FOR UPDATE`, id).Scan(&status, &payout, &damageID, &rentalID); err != nil {
		writeError(w, http.StatusNotFound, "理赔单不存在")
		return
	}
	if status != "approved" {
		writeError(w, http.StatusConflict, "仅已核定的理赔可以赔付到账")
		return
	}
	tx.Exec(`UPDATE insurance_claims SET status='paid', updated_at=now() WHERE id=$1`, id)
	tx.Exec(`UPDATE insurance_policies SET status='closed' WHERE id=(SELECT policy_id FROM insurance_claims WHERE id=$1)`, id)
	if payout > 0 && rentalID.Valid {
		tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, damage_id, claim_id, note, created_by)
			VALUES ($1,'insurance_offset',$2,$3,$4,'保险理赔到账，返还对应押金扣减',$5)`,
			rentalID.Int64, payout, damageID, id, c.UserID)
		// 快签单可能在赔付到账前已完成押金结算：补一笔实际退还，保证钱与流水一致
		var settled bool
		tx.QueryRow(`SELECT status='returned' FROM rentals WHERE id=$1`, rentalID.Int64).Scan(&settled)
		if settled {
			tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, damage_id, claim_id, note, created_by)
				VALUES ($1,'refund',$2,$3,$4,'保险理赔晚于结算到账，补退对应金额',$5)`,
				rentalID.Int64, payout, damageID, id, c.UserID)
		}
	}
	if damageID.Valid {
		tx.Exec(`UPDATE damage_reports SET status='closed' WHERE id=$1`, damageID.Int64)
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "赔付已到账并冲抵押金", "payout": payout,
	})
}

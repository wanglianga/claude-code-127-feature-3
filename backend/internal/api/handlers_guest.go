package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"skirental/internal/rules"
)

// ---------- 尺码建议 ----------

type suggestReq struct {
	HeightCM    int     `json:"height_cm"`
	WeightKG    int     `json:"weight_kg"`
	FootEU      float64 `json:"foot_eu"`
	HeadCM      int     `json:"head_cm"`
	Level       string  `json:"level"`
	SessionType string  `json:"session_type"`
}

func (s *Server) loadProfile(c int64, req suggestReq) (rules.Profile, error) {
	p := rules.Profile{HeightCM: req.HeightCM, WeightKG: req.WeightKG, FootEU: req.FootEU,
		HeadCM: req.HeadCM, Level: req.Level, SessionType: req.SessionType}
	var h sql.NullInt64
	var w sql.NullInt64
	var f sql.NullFloat64
	var hc sql.NullInt64
	var lv sql.NullString
	err := s.db.QueryRow(`SELECT height_cm, weight_kg, foot_eu, head_cm, level FROM users WHERE id=$1`, c).
		Scan(&h, &w, &f, &hc, &lv)
	if err != nil {
		return p, err
	}
	if p.HeightCM == 0 && h.Valid {
		p.HeightCM = int(h.Int64)
	}
	if p.WeightKG == 0 && w.Valid {
		p.WeightKG = int(w.Int64)
	}
	if p.FootEU == 0 && f.Valid {
		p.FootEU = f.Float64
	}
	if p.HeadCM == 0 && hc.Valid {
		p.HeadCM = int(hc.Int64)
	}
	if p.Level == "" && lv.Valid {
		p.Level = lv.String
	}
	if p.Level == "" {
		p.Level = "beginner"
	}
	return p, nil
}

func (s *Server) suggestions(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	var req suggestReq
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	p, err := s.loadProfile(c.UserID, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p.HeightCM == 0 || p.FootEU == 0 {
		writeError(w, http.StatusBadRequest, "请先补全身高和脚码后再获取建议")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"suggestions": rules.AllSuggestions(p),
		"profile":     p,
	})
}

// ---------- 团队 ----------

func (s *Server) myTeams(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	s.listJSON(w, `SELECT COALESCE(json_agg(t), '[]') FROM (
		SELECT tm.team_id AS id, t.name, t.leader_id,
		 (SELECT json_agg(json_build_object('id', u.id, 'name', u.name, 'phone', u.phone))
		  FROM team_members tm2 JOIN users u ON u.id=tm2.guest_id WHERE tm2.team_id=tm.team_id) AS members
		FROM team_members tm JOIN teams t ON t.id=tm.team_id
		WHERE tm.guest_id=$1
	) t`, c.UserID)
}

type teamReq struct {
	Name          string  `json:"name"`
	MemberUserIDs []int64 `json:"member_user_ids"`
}

func (s *Server) createTeam(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	var req teamReq
	if err := decode(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "团队名称必填")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var teamID int64
	err = tx.QueryRow(`INSERT INTO teams(name, leader_id) VALUES($1,$2) RETURNING id`,
		req.Name, c.UserID).Scan(&teamID)
	if err != nil {
		tx.Rollback()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ids := append([]int64{c.UserID}, req.MemberUserIDs...)
	for _, gid := range ids {
		if _, err := tx.Exec(`INSERT INTO team_members(team_id, guest_id) VALUES($1,$2)
			ON CONFLICT DO NOTHING`, teamID, gid); err != nil {
			tx.Rollback()
			writeError(w, http.StatusBadRequest, "成员不存在：id="+fmt.Sprint(gid))
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"team_id": teamID})
}

// ---------- 创建租赁单（草稿 + 建议） ----------

type createRentalReq struct {
	TicketNo           string          `json:"ticket_no"`
	CourseID           int64           `json:"course_id"`
	SlotID             int64           `json:"slot_id"`
	TeamID             int64           `json:"team_id"`
	SessionType        string          `json:"session_type"`
	InsuranceProductID int64           `json:"insurance_product_id"`
	Include            map[string]bool `json:"include"` // 可选项：pole / protector
}

type modelCandidate struct {
	ID        int64
	SizeLabel string
	SizeValue float64
	Fee       float64
	Deposit   float64
	Stock     int
}

// 按建议数值挑选库存充足的最接近型号
func (s *Server) pickModel(tx *sql.Tx, etype string, target float64) (modelCandidate, error) {
	rows, err := tx.Query(`SELECT m.id, m.size_label, COALESCE((m.spec->>'size_value')::float,0),
		m.rental_fee, m.deposit,
		(SELECT count(*) FROM equipment_items i WHERE i.model_id=m.id AND i.status='available')
		FROM equipment_models m WHERE m.active AND m.type=$1`, etype)
	if err != nil {
		return modelCandidate{}, err
	}
	defer rows.Close()
	var best *modelCandidate
	bestDist := 1e9
	var anyWithStock *modelCandidate
	for rows.Next() {
		var mc modelCandidate
		if err := rows.Scan(&mc.ID, &mc.SizeLabel, &mc.SizeValue, &mc.Fee, &mc.Deposit, &mc.Stock); err != nil {
			return modelCandidate{}, err
		}
		if mc.Stock > 0 && anyWithStock == nil {
			m := mc
			anyWithStock = &m
		}
		d := mc.SizeValue - target
		if d < 0 {
			d = -d
		}
		if mc.Stock > 0 && d < bestDist {
			bestDist = d
			m := mc
			best = &m
		}
	}
	if best == nil {
		if anyWithStock != nil { // 理论上不会发生
			return *anyWithStock, nil
		}
		return modelCandidate{}, fmt.Errorf("类型 %s 暂无可用雪具型号", etype)
	}
	return *best, nil
}

var typeNames = map[string]string{
	"ski": "雪板", "boot": "雪鞋", "helmet": "头盔", "pole": "雪杖", "protector": "护具",
}

func (s *Server) createRental(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	var req createRentalReq
	if err := decode(r, &req); err != nil || (req.SessionType != "day" && req.SessionType != "night") {
		writeError(w, http.StatusBadRequest, "session_type 需为 day 或 night")
		return
	}

	// 团队成员（含领队）或仅本人
	guestIDs := []int64{c.UserID}
	isTeam := false
	if req.TeamID > 0 {
		ok, err := s.teamContains(req.TeamID, c.UserID)
		if err != nil || !ok {
			writeError(w, http.StatusForbidden, "只能为本团队下单")
			return
		}
		isTeam = true
		guestIDs, err = s.teamMemberIDs(req.TeamID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	type draftResult struct {
		RentalID     int64   `json:"rental_id"`
		Code         string  `json:"code"`
		GuestID      int64   `json:"guest_id"`
		DepositTotal float64 `json:"deposit_total"`
		FeeTotal     float64 `json:"fee_total"`
		PremiumTotal float64 `json:"premium_total"`
	}
	var results []draftResult

	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()

	for _, gid := range guestIDs {
		var p rules.Profile
		var h sql.NullInt64
		var w2 sql.NullInt64
		var f sql.NullFloat64
		var hc sql.NullInt64
		var lv sql.NullString
		if err := tx.QueryRow(`SELECT height_cm, weight_kg, foot_eu, head_cm, level FROM users WHERE id=$1`, gid).
			Scan(&h, &w2, &f, &hc, &lv); err != nil {
			writeError(w, http.StatusBadRequest, "成员资料不完整，无法生成建议：user_id="+fmt.Sprint(gid))
			return
		}
		p = rules.Profile{SessionType: req.SessionType, Level: "beginner"}
		if h.Valid {
			p.HeightCM = int(h.Int64)
		}
		if w2.Valid {
			p.WeightKG = int(w2.Int64)
		}
		if f.Valid {
			p.FootEU = f.Float64
		}
		if hc.Valid {
			p.HeadCM = int(hc.Int64)
		}
		if lv.Valid {
			p.Level = lv.String
		}
		if p.HeightCM == 0 || p.FootEU == 0 {
			writeError(w, http.StatusBadRequest, "成员身高或脚码缺失，无法生成建议")
			return
		}

		sugs := rules.AllSuggestions(p)
		var chosen []struct {
			sug rules.Suggestion
			mc  modelCandidate
		}
		for _, sug := range sugs {
			if sug.Optional && !req.Include[sug.EquipmentType] {
				continue
			}
			mc, err := s.pickModel(tx, sug.EquipmentType, sug.SizeValue)
			if err != nil {
				writeError(w, http.StatusConflict, "成员(user_id="+fmt.Sprint(gid)+")"+err.Error())
				return
			}
			chosen = append(chosen, struct {
				sug rules.Suggestion
				mc  modelCandidate
			}{sug, mc})
		}

		var rentalID int64
		code := fmt.Sprintf("R%013d", time.Now().UnixNano()%1e13)
		err := tx.QueryRow(`INSERT INTO rentals
			(code, guest_id, team_id, course_id, slot_id, ticket_no, session_type, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'draft') RETURNING id`,
			code, gid, nz(req.TeamID), nz(req.CourseID), nz(req.SlotID), req.TicketNo, req.SessionType).
			Scan(&rentalID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		var depositTotal, feeTotal float64
		for _, ch := range chosen {
			fee := ch.mc.Fee
			if req.SessionType == "night" {
				fee = round2f(fee * (1 + rules.NightPremiumRate))
			}
			depositTotal += ch.mc.Deposit
			feeTotal += fee
			if _, err := tx.Exec(`INSERT INTO rental_items
				(rental_id, equipment_type, model_id, recommended_size, reason, fee, deposit, status)
				VALUES ($1,$2,$3,$4,$5,$6,$7,'suggested')`,
				rentalID, ch.sug.EquipmentType, ch.mc.ID, ch.sug.Size,
				ch.sug.Reason+"；"+ch.sug.SafetyNote, fee, ch.mc.Deposit); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		premium := 0.0
		if req.InsuranceProductID > 0 {
			if err := tx.QueryRow(`SELECT premium FROM insurance_products
				WHERE id=$1 AND active`, req.InsuranceProductID).Scan(&premium); err != nil {
				writeError(w, http.StatusBadRequest, "保险套餐不可用")
				return
			}
			if _, err := tx.Exec(`INSERT INTO insurance_policies(rental_id, product_id, premium)
				VALUES ($1,$2,$3)`, rentalID, req.InsuranceProductID, premium); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		tx.Exec(`UPDATE rentals SET deposit_total=$1, fee_total=$2, premium_total=$3 WHERE id=$4`,
			round2f(depositTotal), round2f(feeTotal), round2f(premium), rentalID)
		results = append(results, draftResult{
			RentalID: rentalID, Code: code, GuestID: gid,
			DepositTotal: round2f(depositTotal), FeeTotal: round2f(feeTotal),
			PremiumTotal: round2f(premium),
		})
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	msg := "已生成租赁建议，请确认预约取具时段"
	if isTeam {
		msg = fmt.Sprintf("已为团队 %d 名成员生成租赁建议", len(results))
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"rentals": results, "message": msg,
	})
}

func nz(v int64) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}

func round2f(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

func (s *Server) teamContains(teamID, guestID int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM team_members WHERE team_id=$1 AND guest_id=$2)`,
		teamID, guestID).Scan(&ok)
	return ok, err
}

func (s *Server) teamMemberIDs(teamID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT guest_id FROM team_members WHERE team_id=$1 ORDER BY guest_id`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ---------- 我的租赁 / 详情 ----------

func (s *Server) myRentals(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	s.listJSON(w, `SELECT COALESCE(json_agg(t ORDER BY t.created_at DESC), '[]') FROM (
		SELECT r.*, u.name AS guest_name, co.name AS course_name
		FROM rentals r JOIN users u ON u.id=r.guest_id
		LEFT JOIN courses co ON co.id=r.course_id
		WHERE r.guest_id=$1
	) t`, c.UserID)
}

func (s *Server) getRental(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	id := urlVar(r, "id")
	var owner int64
	err := s.db.QueryRow(`SELECT guest_id FROM rentals WHERE id=$1`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "租赁单不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	staff := c.Role != "guest"
	if !staff && owner != c.UserID {
		// 团队领队可查看本团队单
		var inTeam bool
		s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM rentals r JOIN teams t ON t.id=r.team_id
			WHERE r.id=$1 AND t.leader_id=$2)`, id, c.UserID).Scan(&inTeam)
		if !inTeam {
			writeError(w, http.StatusForbidden, "只能查看本人或本团队租赁单")
			return
		}
	}
	s.oneJSON(w, rentalDetailSQL, id)
}

const rentalDetailSQL = `
SELECT row_to_json(x) FROM (
	SELECT r.*, u.name AS guest_name, u.phone AS guest_phone,
		co.name AS course_name, to_char(co.start_at,'YYYY-MM-DD HH24:MI') AS course_start,
		(SELECT json_agg(json_build_object(
			'id', ri.id, 'equipment_type', ri.equipment_type, 'recommended_size', ri.recommended_size,
			'reason', ri.reason, 'fee', ri.fee, 'deposit', ri.deposit, 'status', ri.status,
			'fit_status', ri.fit_status, 'fit_note', ri.fit_note,
			'model_id', ri.model_id, 'model_label', m.brand||' '||m.name||' '||m.size_label,
			'item_id', ri.item_id, 'qr_code', ei.qr_code, 'serial_no', ei.serial_no,
			'item_status', ei.status
		  ) ORDER BY ri.id)
		 FROM rental_items ri
		 LEFT JOIN equipment_models m ON m.id=ri.model_id
		 LEFT JOIN equipment_items ei ON ei.id=ri.item_id
		 WHERE ri.rental_id=r.id) AS items,
		(SELECT json_agg(json_build_object(
			'id', d.id, 'damage_type', d.damage_type, 'severity', d.severity,
			'description', d.description, 'photos', d.photos, 'status', d.status,
			'deduction', d.deduction, 'stage', d.stage, 'created_at', d.created_at,
			'repair_order_id', d.repair_order_id, 'claim_id', d.claim_id,
			'type_label', CASE d.damage_type
				WHEN 'edge_damage' THEN '板刃损伤' WHEN 'base_scratch' THEN '板底划伤'
				WHEN 'binding_issue' THEN '固定器故障' WHEN 'liner_wet' THEN '内衬潮湿'
				WHEN 'shell_crack' THEN '壳体裂纹' WHEN 'strap_broken' THEN '绑带断裂'
				WHEN 'lost' THEN '雪具丢失' ELSE '其他异常' END
		  ) ORDER BY d.id) FROM damage_reports d
		  JOIN rental_items ri ON ri.id=d.rental_item_id WHERE ri.rental_id=r.id) AS damages,
		(SELECT json_agg(json_build_object(
			'id', dt.id, 'type', dt.type, 'amount', dt.amount, 'note', dt.note, 'created_at', dt.created_at
		  ) ORDER BY dt.id) FROM deposit_transactions dt WHERE dt.rental_id=r.id) AS deposit_logs,
		(SELECT json_agg(json_build_object(
			'id', ic.id, 'status', ic.status, 'amount_requested', ic.amount_requested,
			'deductible', ic.deductible, 'payout', ic.payout, 'note', ic.note, 'updated_at', ic.updated_at
		  )) FROM insurance_claims ic JOIN insurance_policies ip ON ip.id=ic.policy_id
		 WHERE ip.rental_id=r.id) AS claims,
		(SELECT json_agg(json_build_object('hours', e.hours, 'fee', e.fee, 'created_at', e.created_at)
		  ORDER BY e.id) FROM extensions e WHERE e.rental_id=r.id) AS extensions,
		(SELECT row_to_json(f) FROM (SELECT rating, text, created_at FROM feedback WHERE rental_id=r.id LIMIT 1) f) AS feedback,
		(SELECT p.name FROM insurance_policies ip JOIN insurance_products p ON p.id=ip.product_id
		 WHERE ip.rental_id=r.id LIMIT 1) AS insurance_name,
		(SELECT json_agg(json_build_object('id', sr.id, 'message', sr.message,
			'created_at', to_char(sr.created_at,'MM-DD HH24:MI'), 'read', sr.read_at IS NOT NULL)
		  ORDER BY sr.id DESC) FROM safety_reminders sr WHERE sr.rental_id=r.id) AS safety_reminders,
		(SELECT row_to_json(q) FROM (
			SELECT qs.id, qs.status, qs.contact_phone, qs.shuttle_note,
			       to_char(qs.appeal_deadline,'YYYY-MM-DD HH24:MI') AS appeal_deadline,
			       to_char(qs.created_at,'MM-DD HH24:MI') AS signed_at,
			       to_char(qs.inspected_at,'MM-DD HH24:MI') AS inspected_at
			FROM quick_signoffs qs WHERE qs.rental_id=r.id ORDER BY qs.id DESC LIMIT 1) q) AS quick_signoff,
		(SELECT json_agg(json_build_object(
			'id', dn.id, 'amount', dn.amount, 'status', dn.status, 'resolution', dn.resolution,
			'appeal_text', dn.appeal_text, 'resolution_note', dn.resolution_note,
			'appeal_deadline', to_char(dn.appeal_deadline,'YYYY-MM-DD HH24:MI'),
			'damage_id', dn.damage_id
		  ) ORDER BY dn.id) FROM damage_notifications dn WHERE dn.rental_id=r.id) AS notifications
	FROM rentals r JOIN users u ON u.id=r.guest_id
	LEFT JOIN courses co ON co.id=r.course_id
	WHERE r.id=$1
) x`

// ---------- 确认预约：锁定雪具 + 取具排队 ----------

func (s *Server) confirmRental(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	id := urlVar(r, "id")
	if !s.ownRental(w, r, id, c.UserID) {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()

	var status, session string
	var slotID, courseID sql.NullInt64
	var teamID sql.NullInt64
	if err := tx.QueryRow(`SELECT status, session_type, slot_id, course_id, team_id
		FROM rentals WHERE id=$1 FOR UPDATE`, id).
		Scan(&status, &session, &slotID, &courseID, &teamID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if status != "draft" {
		writeError(w, http.StatusConflict, "租赁单已确认，不能重复预约")
		return
	}

	// 时段容量
	if slotID.Valid {
		var capN, booked int
		if err := tx.QueryRow(`SELECT capacity, booked_count FROM pickup_slots
			WHERE id=$1 FOR UPDATE`, slotID.Int64).Scan(&capN, &booked); err != nil {
			writeError(w, http.StatusBadRequest, "预约时段不存在")
			return
		}
		if booked >= capN {
			writeError(w, http.StatusConflict, "该取具时段约满，请更换时段")
			return
		}
	}
	if courseID.Valid {
		var capN, booked int
		if err := tx.QueryRow(`SELECT capacity, booked_count FROM courses
			WHERE id=$1 FOR UPDATE`, courseID.Int64).Scan(&capN, &booked); err != nil {
			writeError(w, http.StatusBadRequest, "课程不存在")
			return
		}
		if booked >= capN {
			writeError(w, http.StatusConflict, "课程已满员")
			return
		}
	}

	// 为每条明细预留一件可用雪具
	rows, err := tx.Query(`SELECT id, model_id FROM rental_items WHERE rental_id=$1 ORDER BY id FOR UPDATE`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type ri struct{ id, model int64 }
	var ris []ri
	for rows.Next() {
		var x ri
		if err := rows.Scan(&x.id, &x.model); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		ris = append(ris, x)
	}
	rows.Close()
	for _, x := range ris {
		var itemID int64
		err := tx.QueryRow(`SELECT id FROM equipment_items
			WHERE model_id=$1 AND status='available' ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`,
			x.model).Scan(&itemID)
		if err == sql.ErrNoRows {
			writeError(w, http.StatusConflict, "该型号暂时无库存，请改约时段或联系柜台")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, err := tx.Exec(`UPDATE equipment_items SET status='reserved', current_rental_item_id=$1 WHERE id=$2`,
			x.id, itemID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, err := tx.Exec(`UPDATE rental_items SET item_id=$1, status='assigned' WHERE id=$2`,
			itemID, x.id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	priority := 0
	if teamID.Valid {
		priority = 1
	}
	ticketNo, _ := s.nextTicketNo(tx, "pickup")
	if _, err := tx.Exec(`INSERT INTO queue_tickets(rental_id, queue_type, ticket_no, priority)
		VALUES ($1,'pickup',$2,$3)`, id, ticketNo, priority); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	due := time.Now().Add(8 * time.Hour)
	if session == "night" {
		due = time.Now().Add(4 * time.Hour)
	}
	tx.Exec(`UPDATE rentals SET status='reserved', return_due_at=$1 WHERE id=$2`, due, id)
	if slotID.Valid {
		tx.Exec(`UPDATE pickup_slots SET booked_count=booked_count+1 WHERE id=$1`, slotID.Int64)
	}
	if courseID.Valid {
		tx.Exec(`UPDATE courses SET booked_count=booked_count+1 WHERE id=$1`, courseID.Int64)
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":   "预约成功，雪具已为你预留，请按时段到柜台取具",
		"ticket_no": ticketNo,
	})
}

func (s *Server) nextTicketNo(tx *sql.Tx, qtype string) (string, error) {
	prefix := map[string]string{
		"pickup": "P", "exchange": "X", "return_day": "D", "return_night": "N", "damage": "M",
	}[qtype]
	var n int
	err := tx.QueryRow(`SELECT count(*)+1 FROM queue_tickets
		WHERE queue_type=$1 AND created_at::date = CURRENT_DATE`, qtype).Scan(&n)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%03d", prefix, n), nil
}

func (s *Server) ownRental(w http.ResponseWriter, r *http.Request, rid, uid int64) bool {
	var owner int64
	err := s.db.QueryRow(`SELECT guest_id FROM rentals WHERE id=$1`, rid).Scan(&owner)
	if err != nil {
		writeError(w, http.StatusNotFound, "租赁单不存在")
		return false
	}
	if owner != uid {
		writeError(w, http.StatusForbidden, "只能操作本人租赁单")
		return false
	}
	return true
}

// ---------- 换码申请 ----------

type exchangeReq struct {
	RentalItemID int64  `json:"rental_item_id"`
	Reason       string `json:"reason"`
	DesiredSize  string `json:"desired_size"`
}

func (s *Server) requestExchange(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	rid := urlVar(r, "id")
	if !s.ownRental(w, r, rid, c.UserID) {
		return
	}
	var req exchangeReq
	if err := decode(r, &req); err != nil || req.RentalItemID == 0 {
		writeError(w, http.StatusBadRequest, "需要指定要调换的雪具")
		return
	}
	var status string
	var oldItem sql.NullInt64
	err := s.db.QueryRow(`SELECT ri.status, ri.item_id FROM rental_items ri
		WHERE ri.id=$1 AND ri.rental_id=$2`, req.RentalItemID, rid).
		Scan(&status, &oldItem)
	if err != nil {
		writeError(w, http.StatusNotFound, "租赁明细不存在")
		return
	}
	if status != "out" {
		writeError(w, http.StatusConflict, "仅已出库使用中的雪具可以换码")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()
	var exID int64
	err = tx.QueryRow(`INSERT INTO exchanges
		(rental_item_id, old_item_id, new_size, reason, requested_by)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		req.RentalItemID, oldItem, req.DesiredSize, req.Reason, c.UserID).Scan(&exID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ticketNo, _ := s.nextTicketNo(tx, "exchange")
	tx.Exec(`INSERT INTO queue_tickets(rental_id, queue_type, ticket_no)
		VALUES ($1,'exchange',$2)`, rid, ticketNo)
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"exchange_id": exID, "ticket_no": ticketNo,
		"message": "换码申请已提交，请留意换码队列叫号",
	})
}

// ---------- 课程延时 ----------

type extendReq struct {
	Hours float64 `json:"hours"`
}

func (s *Server) extendRental(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	rid := urlVar(r, "id")
	if !s.ownRental(w, r, rid, c.UserID) {
		return
	}
	var req extendReq
	if err := decode(r, &req); err != nil || req.Hours <= 0 || req.Hours > 6 {
		writeError(w, http.StatusBadRequest, "延时时长需在 0~6 小时之间")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()
	var status string
	var baseFee float64
	if err := tx.QueryRow(`SELECT status, fee_total FROM rentals WHERE id=$1 FOR UPDATE`, rid).
		Scan(&status, &baseFee); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if status != "equipped" && status != "return_pending" {
		writeError(w, http.StatusConflict, "当前状态不能延时")
		return
	}
	// 延时费按时租折算：以当前租金/8 为时租近似
	hourly := baseFee / 8
	fee := rules.LateFee(req.Hours, hourly*4)
	tx.Exec(`INSERT INTO extensions(rental_id, hours, fee, created_by) VALUES ($1,$2,$3,$4)`,
		rid, req.Hours, fee, c.UserID)
	tx.Exec(`UPDATE rentals SET extra_hours=extra_hours+$1, fee_total=fee_total+$2,
		return_due_at=COALESCE(return_due_at, now()) + ($3 || ' hours')::interval WHERE id=$4`,
		req.Hours, fee, fmt.Sprintf("%.1f", req.Hours), rid)
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"extra_fee": fee, "message": "课程延时已登记，归还时一并结算",
	})
}

// ---------- 游客使用中报损 ----------

type damageReq struct {
	RentalItemID int64    `json:"rental_item_id"`
	DamageType   string   `json:"damage_type"`
	Severity     string   `json:"severity"`
	Description  string   `json:"description"`
	Photos       []string `json:"photos"`
}

func (s *Server) reportDamage(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	rid := urlVar(r, "id")
	if !s.ownRental(w, r, rid, c.UserID) {
		return
	}
	var req damageReq
	if err := decode(r, &req); err != nil || req.RentalItemID == 0 {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if _, ok := rules.DamageRuleOf(req.DamageType); !ok {
		writeError(w, http.StatusBadRequest, "损坏类型不正确")
		return
	}
	if req.Severity == "" {
		req.Severity = "medium"
	}
	var status string
	err := s.db.QueryRow(`SELECT status FROM rental_items WHERE id=$1 AND rental_id=$2`,
		req.RentalItemID, rid).Scan(&status)
	if err != nil {
		writeError(w, http.StatusNotFound, "租赁明细不存在")
		return
	}
	if status != "out" {
		writeError(w, http.StatusConflict, "仅使用中的雪具可以上报异常")
		return
	}
	tx, _ := s.db.Begin()
	defer tx.Rollback()
	var dID int64
	photos, _ := json.Marshal(req.Photos)
	err = tx.QueryRow(`INSERT INTO damage_reports
		(rental_item_id, reporter_id, stage, damage_type, severity, description, photos)
		VALUES ($1,$2,'during_rental',$3,$4,$5,COALESCE($6,'[]'::jsonb)) RETURNING id`,
		req.RentalItemID, c.UserID, req.DamageType, req.Severity, req.Description, photos).
		Scan(&dID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ticketNo, _ := s.nextTicketNo(tx, "damage")
	tx.Exec(`INSERT INTO queue_tickets(rental_id, queue_type, ticket_no) VALUES ($1,'damage',$2)`,
		rid, ticketNo)
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"damage_id": dID, "ticket_no": ticketNo,
		"message": "损坏已登记并进入损坏处理队列，柜台/维修间将跟进",
	})
}

// ---------- 归还排队（日场/夜场分开） ----------

type returnReq struct {
	Night bool `json:"night"`
}

func (s *Server) queueReturn(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	rid := urlVar(r, "id")
	if !s.ownRental(w, r, rid, c.UserID) {
		return
	}
	var req returnReq
	decode(r, &req)
	tx, _ := s.db.Begin()
	defer tx.Rollback()
	var status, session string
	if err := tx.QueryRow(`SELECT status, session_type FROM rentals WHERE id=$1 FOR UPDATE`, rid).
		Scan(&status, &session); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if status != "equipped" {
		writeError(w, http.StatusConflict, "仅已出库的租赁单可以排队归还")
		return
	}
	qtype := "return_day"
	if req.Night || session == "night" {
		qtype = "return_night"
	}
	var open int
	tx.QueryRow(`SELECT count(*) FROM queue_tickets WHERE rental_id=$1 AND queue_type=$2
		AND status IN ('waiting','called','serving')`, rid, qtype).Scan(&open)
	if open > 0 {
		writeError(w, http.StatusConflict, "已在归还队列中")
		return
	}
	ticketNo, _ := s.nextTicketNo(tx, qtype)
	tx.Exec(`INSERT INTO queue_tickets(rental_id, queue_type, ticket_no) VALUES ($1,$2,$3)`,
		rid, qtype, ticketNo)
	tx.Exec(`UPDATE rentals SET status='return_pending', night_return=($2='return_night') WHERE id=$1`,
		rid, qtype)
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	label := "日场归还"
	if qtype == "return_night" {
		label = "夜场归还"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ticket_no": ticketNo, "queue": label,
		"message": "已进入" + label + "队列，请等待叫号验具",
	})
}

// ---------- 反馈 ----------

type feedbackReq struct {
	Rating int    `json:"rating"`
	Text   string `json:"text"`
}

func (s *Server) submitFeedback(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	rid := urlVar(r, "id")
	if !s.ownRental(w, r, rid, c.UserID) {
		return
	}
	var req feedbackReq
	if err := decode(r, &req); err != nil || req.Rating < 1 || req.Rating > 5 {
		writeError(w, http.StatusBadRequest, "评分需为 1-5")
		return
	}
	var status string
	s.db.QueryRow(`SELECT status FROM rentals WHERE id=$1`, rid).Scan(&status)
	if status != "returned" {
		writeError(w, http.StatusConflict, "归还完成后才能评价")
		return
	}
	_, err := s.db.Exec(`INSERT INTO feedback(rental_id, guest_id, rating, text)
		VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, rid, c.UserID, req.Rating, req.Text)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"message": "感谢你的反馈"})
}

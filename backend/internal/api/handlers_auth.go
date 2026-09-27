package api

import (
	"database/sql"
	"net/http"

	"skirental/internal/auth"
)

type loginReq struct {
	Phone    string `json:"phone"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	var (
		id     int64
		role   string
		name   string
		hash   string
		height sql.NullInt64
		weight sql.NullInt64
		foot   sql.NullFloat64
		head   sql.NullInt64
		level  sql.NullString
	)
	err := s.db.QueryRow(`SELECT id, role, name, password_hash,
		height_cm, weight_kg, foot_eu, head_cm, level FROM users WHERE phone=$1`, req.Phone).
		Scan(&id, &role, &name, &hash, &height, &weight, &foot, &head, &level)
	if err == sql.ErrNoRows || !auth.CheckPassword(hash, req.Password) {
		writeError(w, http.StatusUnauthorized, "手机号或密码错误")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	token, err := auth.IssueToken(s.cfg.JWTSecret, auth.Claims{UserID: id, Role: role, Name: name})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	user := map[string]interface{}{
		"id": id, "name": name, "role": role, "phone": req.Phone,
	}
	if height.Valid {
		user["height_cm"] = height.Int64
	}
	if weight.Valid {
		user["weight_kg"] = weight.Int64
	}
	if foot.Valid {
		user["foot_eu"] = foot.Float64
	}
	if head.Valid {
		user["head_cm"] = head.Int64
	}
	if level.Valid {
		user["level"] = level.String
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"token": token, "user": user})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	c := currentUser(r)
	s.oneJSON(w, `SELECT json_build_object(
		'id', u.id, 'name', u.name, 'phone', u.phone, 'role', u.role,
		'height_cm', u.height_cm, 'weight_kg', u.weight_kg,
		'foot_eu', u.foot_eu, 'head_cm', u.head_cm, 'level', u.level
	) FROM users u WHERE id=$1`, c.UserID)
}

// ---------- 目录数据 ----------

func (s *Server) listCourses(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	args := []interface{}{}
	q := `SELECT COALESCE(json_agg(t), '[]') FROM (
		SELECT c.id, c.name, c.coach_id, u.name AS coach_name, c.session_type,
		       to_char(c.start_at,'YYYY-MM-DD HH24:MI') AS start_at,
		       to_char(c.end_at,'HH24:MI') AS end_at, c.level,
		       c.capacity, c.booked_count, c.meeting_point
		FROM courses c LEFT JOIN users u ON u.id=c.coach_id
		WHERE c.start_at > now() - interval '2 hour'
	`
	if session == "day" || session == "night" {
		q += " AND c.session_type=$1"
		args = append(args, session)
	}
	q += " ORDER BY c.start_at) t"
	s.listJSON(w, q, args...)
}

func (s *Server) listSlots(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	args := []interface{}{}
	q := `SELECT COALESCE(json_agg(t), '[]') FROM (
		SELECT id, slot_date::text AS date, start_time, end_time, session_type,
		       capacity, booked_count, (capacity-booked_count) AS remaining
		FROM pickup_slots WHERE slot_date >= CURRENT_DATE
	`
	if session == "day" || session == "night" {
		args = append(args, session)
		q += " AND session_type=$1"
	}
	q += " ORDER BY slot_date, start_time) t"
	s.listJSON(w, q, args...)
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	etype := r.URL.Query().Get("type")
	args := []interface{}{}
	q := `SELECT COALESCE(json_agg(t), '[]') FROM (
		SELECT m.id, m.code, m.brand, m.name, m.type, m.size_label, m.spec,
		       m.rental_fee, m.deposit, m.active,
		       (SELECT count(*) FROM equipment_items i WHERE i.model_id=m.id) AS total,
		       (SELECT count(*) FROM equipment_items i WHERE i.model_id=m.id AND i.status='available') AS available
		FROM equipment_models m WHERE m.active
	`
	if etype != "" {
		q += " AND m.type=$1"
		args = append(args, etype)
	}
	q += " ORDER BY m.type, m.size_label) t"
	s.listJSON(w, q, args...)
}

func (s *Server) listInsuranceProducts(w http.ResponseWriter, r *http.Request) {
	s.listJSON(w, `SELECT COALESCE(json_agg(t), '[]') FROM (
		SELECT id, name, premium, coverage_cap, deductible, covers, session_type
		FROM insurance_products WHERE active ORDER BY premium
	) t`)
}

// 各队列实时人数（柜台大屏 / 员工首页），含待检雪具与维修间容量占用
func (s *Server) queueSummary(w http.ResponseWriter, r *http.Request) {
	s.oneJSON(w, `SELECT json_build_object(
		'pickup',      (SELECT count(*) FROM queue_tickets WHERE queue_type='pickup' AND status IN ('waiting','called','serving')),
		'exchange',    (SELECT count(*) FROM queue_tickets WHERE queue_type='exchange' AND status IN ('waiting','called','serving')),
		'return_day',  (SELECT count(*) FROM queue_tickets WHERE queue_type='return_day' AND status IN ('waiting','called','serving')),
		'return_night',(SELECT count(*) FROM queue_tickets WHERE queue_type='return_night' AND status IN ('waiting','called','serving')),
		'damage',      (SELECT count(*) FROM queue_tickets WHERE queue_type='damage' AND status IN ('waiting','called','serving')),
		'inspection_pending', (SELECT count(*) FROM equipment_items WHERE status='inspection'),
		'repair_active',(SELECT count(*) FROM repair_orders WHERE status IN ('queued','in_progress','waiting_part')),
		'inspect_capacity', (SELECT inspect_capacity FROM repair_capacity WHERE id=1),
		'repair_capacity',  (SELECT repair_capacity FROM repair_capacity WHERE id=1),
		'quick_sign_open',  (SELECT count(*) FROM rentals WHERE status='quick_signed'),
		'appeal_open',      (SELECT count(*) FROM damage_notifications WHERE status='appealed')
	)`)
}

package api

import "net/http"

// 财务总览：租金/保费收入、押金扣减、保险冲抵、维修支出、应退押金、快速签收暂扣
func (s *Server) financeSummary(w http.ResponseWriter, r *http.Request) {
	s.oneJSON(w, `SELECT json_build_object(
		'rental_fee_income', COALESCE((SELECT sum(fee_total) FROM rentals
			WHERE status='returned'),0),
		'premium_income', COALESCE((SELECT sum(premium_total) FROM rentals
			WHERE status='returned'),0),
		'deposit_held_open', COALESCE((SELECT sum(deposit_total) FROM rentals
			WHERE status IN ('reserved','equipped','return_pending','quick_signed')),0),
		'damage_deducted', COALESCE((SELECT sum(amount) FROM deposit_transactions
			WHERE type='deduct_damage'),0),
		'lost_deducted', COALESCE((SELECT sum(amount) FROM deposit_transactions
			WHERE type='deduct_lost'),0),
		'late_deducted', COALESCE((SELECT sum(amount) FROM deposit_transactions
			WHERE type='deduct_late'),0),
		'insurance_offsets', COALESCE((SELECT sum(amount) FROM deposit_transactions
			WHERE type='insurance_offset'),0),
		'deposit_refunded', COALESCE((SELECT sum(amount) FROM deposit_transactions
			WHERE type='refund'),0),
		'repair_cost', COALESCE((SELECT sum(repair_cost) FROM repair_orders),0),
		'open_rentals', (SELECT count(*) FROM rentals
			WHERE status IN ('reserved','equipped','return_pending','quick_signed')),
		'quick_sign_hold', COALESCE((SELECT sum(deposit_total) FROM rentals
			WHERE status='quick_signed'),0),
		'quick_sign_pending', (SELECT count(*) FROM rentals WHERE status='quick_signed'),
		'notify_pending_amount', COALESCE((SELECT sum(amount) FROM damage_notifications
			WHERE status IN ('pending','appealed')),0),
		'appealing_count', (SELECT count(*) FROM damage_notifications WHERE status='appealed')
	)`)
}

func (s *Server) ledger(w http.ResponseWriter, r *http.Request) {
	s.listJSON(w, `SELECT COALESCE(json_agg(t ORDER BY t.created_at DESC), '[]') FROM (
		SELECT dt.id, dt.type, dt.amount, dt.note,
		       to_char(dt.created_at,'YYYY-MM-DD HH24:MI') AS created_at,
		       dt.rental_id, rr.code AS rental_code,
		       u.name AS guest_name,
		       CASE dt.type
		         WHEN 'hold' THEN '押金冻结'
		         WHEN 'deduct_damage' THEN '损坏扣减'
		         WHEN 'deduct_lost' THEN '丢失扣减'
		         WHEN 'deduct_late' THEN '超时扣减'
		         WHEN 'insurance_offset' THEN '保险冲抵返还'
		         WHEN 'refund' THEN '押金退还' END AS type_label
		FROM deposit_transactions dt
		JOIN rentals rr ON rr.id=dt.rental_id
		JOIN users u ON u.id=rr.guest_id
		ORDER BY dt.created_at DESC LIMIT 200
	) t`)
}

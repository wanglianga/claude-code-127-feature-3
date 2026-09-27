package seed

import (
	"database/sql"
	"fmt"
	"time"

	"skirental/internal/auth"
)

// Run 写入演示所需的账号、雪具、课程、时段、保险与示例订单。幂等：已初始化则跳过。
func Run(d *sql.DB) error {
	var exists bool
	if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE phone='13800000001')`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		fmt.Println("种子数据已存在，跳过")
		return nil
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	hash, _ := auth.HashPassword("ski123456")

	mkUser := func(phone, name, role string, profile ...interface{}) int64 {
		// profile: height, weight, foot, head, level
		var height, weight, head sql.NullInt64
		var foot sql.NullFloat64
		var level sql.NullString
		if len(profile) == 5 {
			if v, ok := profile[0].(int); ok && v > 0 {
				height = sql.NullInt64{Int64: int64(v), Valid: true}
			}
			if v, ok := profile[1].(int); ok && v > 0 {
				weight = sql.NullInt64{Int64: int64(v), Valid: true}
			}
			if v, ok := profile[2].(float64); ok && v > 0 {
				foot = sql.NullFloat64{Float64: v, Valid: true}
			}
			if v, ok := profile[3].(int); ok && v > 0 {
				head = sql.NullInt64{Int64: int64(v), Valid: true}
			}
			if v, ok := profile[4].(string); ok && v != "" {
				level = sql.NullString{String: v, Valid: true}
			}
		}
		var id int64
		if err := tx.QueryRow(`INSERT INTO users
			(name, phone, password_hash, role, height_cm, weight_kg, foot_eu, head_cm, level)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
			name, phone, hash, role, height, weight, foot, head, level).Scan(&id); err != nil {
			panic(err)
		}
		return id
	}

	// 员工
	mkUser("13800000001", "赵店长", "admin")
	coachID := mkUser("13800000002", "陈教练", "coach")
	mkUser("13800000003", "孙柜台", "counter")
	mkUser("13800000004", "周师傅", "repair")
	mkUser("13800000005", "吴理赔", "insurance")
	mkUser("13800000006", "郑财务", "finance")
	// 游客
	g1 := mkUser("13900000001", "张明（初学者）", "guest", 170, 65, 42.0, 57, "beginner")
	g2 := mkUser("13900000002", "李华（进阶）", "guest", 178, 75, 43.5, 59, "intermediate")
	g3 := mkUser("13900000003", "王芳（夜场初学者）", "guest", 162, 52, 38.0, 55, "beginner")
	g4 := mkUser("13900000004", "刘团队-队长", "guest", 180, 88, 45.0, 60, "advanced")
	mkUser("13900000005", "团队成员-小钱", "guest", 155, 48, 37.0, 54, "beginner")
	mkUser("13900000006", "团队成员-小孙", "guest", 168, 60, 40.0, 56, "beginner")

	// 团队
	var teamID int64
	tx.QueryRow(`INSERT INTO teams(name, leader_id) VALUES('追风滑雪团',$1) RETURNING id`, g4).Scan(&teamID)
	tx.Exec(`INSERT INTO team_members(team_id, guest_id) VALUES
		($1,$2),($1,$3),($1,$4) ON CONFLICT DO NOTHING`, teamID, g4,
		mkUserLookup(tx, "13900000005"), mkUserLookup(tx, "13900000006"))

	// ---------- 雪具型号与库存 ----------
	mkModel := func(code, brand, name, etype, sizeLabel string, sizeVal float64, fee, deposit float64) int64 {
		var id int64
		if err := tx.QueryRow(`INSERT INTO equipment_models
			(code, brand, name, type, size_label, spec, rental_fee, deposit)
			VALUES ($1,$2,$3,$4,$5,json_build_object('size_value',$6::float8),$7,$8) RETURNING id`,
			code, brand, name, etype, sizeLabel, sizeVal, fee, deposit).Scan(&id); err != nil {
			panic(err)
		}
		return id
	}
	mkItems := func(modelID int64, code string, n int) {
		for i := 1; i <= n; i++ {
			qr := fmt.Sprintf("QR-%s-%03d", code, i)
			if _, err := tx.Exec(`INSERT INTO equipment_items(model_id, qr_code, serial_no) VALUES ($1,$2,$3)`,
				modelID, qr, fmt.Sprintf("SN-%s-%03d", code, i)); err != nil {
				panic(err)
			}
		}
	}

	// 雪板 130-180
	for _, cm := range []int{130, 135, 140, 145, 150, 155, 160, 165, 170, 175, 180} {
		code := fmt.Sprintf("SKI%d", cm)
		id := mkModel(code, "Atomic", "全地域雪板", "ski", fmt.Sprintf("%dcm", cm), float64(cm), 300, 1500)
		mkItems(id, code, 4)
	}
	// 雪鞋 EU36-46
	for eu := 36; eu <= 46; eu++ {
		code := fmt.Sprintf("BOOT%d", eu)
		id := mkModel(code, "Salomon", "全能雪鞋", "boot", fmt.Sprintf("EU%d", eu), float64(eu), 100, 500)
		mkItems(id, code, 4)
		code2 := fmt.Sprintf("BOOT%dH", eu)
		id2 := mkModel(code2, "Salomon", "全能雪鞋(半码)", "boot", fmt.Sprintf("EU%d.5", eu), float64(eu)+0.5, 100, 500)
		mkItems(id2, code2, 2)
	}
	// 头盔 S/M/L/XL
	for _, sz := range []struct {
		label string
		val   float64
	}{{"S", 53}, {"M", 56}, {"L", 59}, {"XL", 62}} {
		code := "HELM" + sz.label
		id := mkModel(code, "Giro", "一体成型头盔", "helmet", sz.label, sz.val, 60, 300)
		mkItems(id, code, 4)
	}
	// 雪杖 100-125
	for _, cm := range []int{100, 105, 110, 115, 120, 125} {
		code := fmt.Sprintf("POLE%d", cm)
		id := mkModel(code, "LEKI", "铝合金雪杖", "pole", fmt.Sprintf("%dcm", cm), float64(cm), 40, 150)
		mkItems(id, code, 4)
	}
	// 护具 S/M/L/XL
	for _, sz := range []struct {
		label string
		val   float64
	}{{"S", 1}, {"M", 2}, {"L", 3}, {"XL", 4}} {
		code := "PROT" + sz.label
		id := mkModel(code, "Pro-Tec", "护臀护膝套装", "protector", sz.label, sz.val, 80, 400)
		mkItems(id, code, 4)
	}

	// ---------- 课程 ----------
	day10 := nextClock(10, 0)
	tonight := nextClock(19, 0)
	tomorrow14 := time.Now().AddDate(0, 0, 1)
	tomorrow14 = time.Date(tomorrow14.Year(), tomorrow14.Month(), tomorrow14.Day(), 14, 0, 0, 0, time.Local)
	var dayCourse, nightCourse int64
	tx.QueryRow(`INSERT INTO courses(name, coach_id, session_type, start_at, end_at, level, capacity, booked_count)
		VALUES ('初学者日场入门班',$1,'day',$2,$3,'beginner',12,1) RETURNING id`,
		coachID, day10, day10.Add(2*time.Hour)).Scan(&dayCourse)
	tx.QueryRow(`INSERT INTO courses(name, coach_id, session_type, start_at, end_at, level, capacity, booked_count)
		VALUES ('夜场进阶刻滑班',$1,'night',$2,$3,'intermediate',10,1) RETURNING id`,
		coachID, tonight, tonight.Add(2*time.Hour)).Scan(&nightCourse)
	tx.Exec(`INSERT INTO courses(name, coach_id, session_type, start_at, end_at, level, capacity)
		VALUES ('明日日场进阶班',$1,'day',$2,$3,'intermediate',12)`,
		coachID, tomorrow14, tomorrow14.Add(2*time.Hour))

	// ---------- 分时取具槽 ----------
	today := time.Now()
	for dayOff := 0; dayOff < 3; dayOff++ {
		d := today.AddDate(0, 0, dayOff)
		ds := d.Format("2006-01-02")
		daySlots := []string{"08:00", "08:30", "09:00", "09:30", "10:00", "10:30", "11:00", "13:00", "14:00"}
		for i, st := range daySlots {
			if _, err := tx.Exec(`INSERT INTO pickup_slots(slot_date, start_time, end_time, session_type, capacity, booked_count)
				VALUES ($1::date,$2,$3,'day',20, CASE WHEN $1::date=CURRENT_DATE AND $2 IN ('08:30','09:00') THEN 18 ELSE 0 END)`,
				ds, st, daySlots[min(i+1, len(daySlots)-1)]); err != nil {
				return fmt.Errorf("day slot %s: %w", st, err)
			}
		}
		nightSlots := []string{"16:30", "17:00", "17:30", "18:00", "18:30", "19:00"}
		for i, st := range nightSlots {
			if _, err := tx.Exec(`INSERT INTO pickup_slots(slot_date, start_time, end_time, session_type, capacity, booked_count)
				VALUES ($1::date,$2,$3,'night',15, CASE WHEN $1::date=CURRENT_DATE AND $2='18:00' THEN 14 ELSE 0 END)`,
				ds, st, nightSlots[min(i+1, len(nightSlots)-1)]); err != nil {
				return fmt.Errorf("night slot %s: %w", st, err)
			}
		}
	}

	// ---------- 保险套餐 ----------
	tx.Exec(`INSERT INTO insurance_products(name, premium, coverage_cap, deductible, covers, session_type)
		VALUES ('基础险',10,500,100,'{edge_damage,base_scratch,liner_wet}','both')`)
	tx.Exec(`INSERT INTO insurance_products(name, premium, coverage_cap, deductible, covers, session_type)
		VALUES ('安心险',30,1000,50,'{edge_damage,base_scratch,liner_wet,shell_crack,strap_broken,binding_issue}','both')`)
	tx.Exec(`INSERT INTO insurance_products(name, premium, coverage_cap, deductible, covers, session_type)
		VALUES ('夜场全险',50,2000,0,'{edge_damage,base_scratch,liner_wet,shell_crack,strap_broken,binding_issue,lost}','night')`)
	var basicProd, nightProd int64
	tx.QueryRow(`SELECT id FROM insurance_products WHERE name='基础险'`).Scan(&basicProd)
	tx.QueryRow(`SELECT id FROM insurance_products WHERE name='夜场全险'`).Scan(&nightProd)

	// ---------- 示例订单 A：已预约待取具（取具队列） ----------
	var slotA int64
	tx.QueryRow(`SELECT id FROM pickup_slots WHERE slot_date=CURRENT_DATE AND start_time='09:00' AND session_type='day'`).Scan(&slotA)
	var rA int64
	tx.QueryRow(`INSERT INTO rentals
		(code, guest_id, team_id, course_id, slot_id, ticket_no, session_type, status,
		 deposit_total, fee_total, premium_total, return_due_at)
		VALUES ('R-DEMO-A',$1,NULL,$2,$3,'T20260101','day','reserved',
		 (1500+500+300+150+400),(300+100+60+40+80),10, now()+interval '8 hour')
		RETURNING id`, g1, dayCourse, slotA).Scan(&rA)
	addLines(tx, rA, []lineSpec{
		{"ski", "SKI145", "145cm", 300, 1500},
		{"boot", "BOOT42", "EU42", 100, 500},
		{"helmet", "HELMM", "M", 60, 300},
		{"pole", "POLE120", "120cm", 40, 150},
		{"protector", "PROTM", "M", 80, 400},
	}, "assigned")
	tx.Exec(`INSERT INTO insurance_policies(rental_id, product_id, premium) VALUES ($1,$2,10)`, rA, basicProd)
	tx.Exec(`INSERT INTO queue_tickets(rental_id, queue_type, ticket_no, priority)
		VALUES ($1,'pickup','P001',0)`, rA)
	assignItems(tx, rA)

	// ---------- 示例订单 B：已出库（教练课前确认 / 可换码 / 可归还） ----------
	var rB int64
	tx.QueryRow(`INSERT INTO rentals
		(code, guest_id, course_id, ticket_no, session_type, status,
		 deposit_total, fee_total, pickup_at, return_due_at)
		VALUES ('R-DEMO-B',$1,$2,'T20260102','day','equipped',
		 (1500+500+300+150),(300+100+60+40), now()-interval '1 hour', now()+interval '7 hour')
		RETURNING id`, g2, dayCourse).Scan(&rB)
	addLines(tx, rB, []lineSpec{
		{"ski", "SKI160", "160cm", 300, 1500},
		{"boot", "BOOT43H", "EU43.5", 100, 500},
		{"helmet", "HELML", "L", 60, 300},
		{"pole", "POLE125", "125cm", 40, 150},
	}, "out")
	tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, note, created_by)
		VALUES ($1,'hold',2450,'出库冻结押金',$2)`, rB, g2)
	assignItems(tx, rB)

	// ---------- 示例订单 C：夜场归还中，雪板板刃损伤（维修+保险队列都有数据） ----------
	var rC int64
	tx.QueryRow(`INSERT INTO rentals
		(code, guest_id, course_id, ticket_no, session_type, status,
		 deposit_total, fee_total, premium_total, night_return, pickup_at,
		 return_due_at, returned_at)
		VALUES ('R-DEMO-C',$1,$2,'T20260103','night','return_pending',
		 (1500+500+300+400),(360+120+72+96),50, TRUE, now()-interval '3 hour', now()+interval '2 hour', NULL)
		RETURNING id`, g3, nightCourse).Scan(&rC)
	addLines(tx, rC, []lineSpec{
		{"ski", "SKI140", "140cm", 360, 1500},
		{"boot", "BOOT38", "EU38", 120, 500},
		{"helmet", "HELMM", "M", 72, 300},
		{"protector", "PROTS", "S", 96, 400},
	}, "out")
	tx.Exec(`INSERT INTO insurance_policies(rental_id, product_id, premium) VALUES ($1,$2,50)`, rC, nightProd)
	tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, note, created_by)
		VALUES ($1,'hold',2700,'出库冻结押金',$2)`, rC, g3)
	assignItems(tx, rC)

	// 取第一件（雪板）标记为损坏、其余完好归还
	var skiRI, skiItem int64
	tx.QueryRow(`SELECT ri.id, ri.item_id FROM rental_items ri
		WHERE ri.rental_id=$1 AND ri.equipment_type='ski'`, rC).Scan(&skiRI, &skiItem)
	tx.Exec(`UPDATE equipment_items SET status='repair' WHERE id=$1`, skiItem)
	tx.Exec(`UPDATE rental_items SET status='repair' WHERE id=$1`, skiRI)
	var dmgID, roID, polID, claimID int64
	tx.QueryRow(`INSERT INTO damage_reports
		(rental_item_id, reporter_id, stage, damage_type, severity, description, photos,
		 status, deduction, assessed_by)
		VALUES ($1,$2,'at_return','edge_damage','medium','归还时发现左板刃有 3cm 缺口',
		 '[]'::jsonb,'repair_created',525,$2) RETURNING id`, skiRI, g3).Scan(&dmgID)
	tx.QueryRow(`INSERT INTO repair_orders(item_id, damage_id, issue, advice, status)
		VALUES ($1,$2,'板刃损伤：左板刃缺口','磨刃/补刃，检测刃口平直度','queued') RETURNING id`,
		skiItem, dmgID).Scan(&roID)
	tx.Exec(`UPDATE damage_reports SET repair_order_id=$1 WHERE id=$2`, roID, dmgID)
	tx.QueryRow(`SELECT id FROM insurance_policies WHERE rental_id=$1`, rC).Scan(&polID)
	tx.QueryRow(`INSERT INTO insurance_claims
		(policy_id, damage_id, amount_requested, deductible, amount_approved, status, note)
		VALUES ($1,$2,525,0,525,'filed','夜场全险 0 免赔，等待审核') RETURNING id`,
		polID, dmgID).Scan(&claimID)
	tx.Exec(`UPDATE damage_reports SET claim_id=$1 WHERE id=$2`, claimID, dmgID)
	tx.Exec(`UPDATE insurance_policies SET status='claimed' WHERE id=$1`, polID)
	tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, damage_id, note, created_by)
		VALUES ($1,'deduct_damage',525,$2,'板刃损伤 按押金 35% 扣减',$3)`, rC, dmgID, g3)

	// 其余三件完好
	tx.Exec(`UPDATE rental_items SET status='returned_ok', returned_at=now()
		WHERE rental_id=$1 AND equipment_type<>'ski'`, rC)
	tx.Exec(`UPDATE equipment_items SET status='available', current_rental_item_id=NULL
		WHERE id IN (SELECT item_id FROM rental_items WHERE rental_id=$1 AND equipment_type<>'ski')`, rC)
	tx.Exec(`INSERT INTO queue_tickets(rental_id, queue_type, ticket_no)
		VALUES ($1,'return_night','N001')`, rC)

	// ---------- 示例订单 D：高峰快速签收（后检中 + 一条待游客确认的赔付通知） ----------
	g7 := mkUser("13900000007", "赵敏（赶班车）", "guest", 165, 55, 38.5, 56, "intermediate")
	var counterUID, repairUID int64
	tx.QueryRow(`SELECT id FROM users WHERE phone='13800000003'`).Scan(&counterUID)
	tx.QueryRow(`SELECT id FROM users WHERE phone='13800000004'`).Scan(&repairUID)
	var rD int64
	tx.QueryRow(`INSERT INTO rentals
		(code, guest_id, course_id, ticket_no, session_type, status,
		 deposit_total, fee_total, pickup_at, return_due_at,
		 quick_signed_at, quick_signed_by, contact_phone, appeal_deadline)
		VALUES ('R-DEMO-D',$1,$2,'T20260104','day','quick_signed',
		 (1500+500+300+150+400),(300+100+60+40+80), now()-interval '6 hour', now()+interval '2 hour',
		 now()-interval '1 hour', $3, '13900000007', now()+interval '47 hour')
		RETURNING id`, g7, dayCourse, counterUID).Scan(&rD)
	addLines(tx, rD, []lineSpec{
		{"ski", "SKI150", "150cm", 300, 1500},
		{"boot", "BOOT38H", "EU38.5", 100, 500},
		{"helmet", "HELMM", "M", 60, 300},
		{"pole", "POLE110", "110cm", 40, 150},
		{"protector", "PROTM", "M", 80, 400},
	}, "out")
	assignItems(tx, rD)
	tx.Exec(`INSERT INTO deposit_transactions(rental_id, type, amount, note, created_by)
		VALUES ($1,'hold',2850,'出库冻结押金',$2)`, rD, g7)
	// 雪板：后检已发现板刃崩口 -> 维修工单 + 待游客确认的赔付通知（押金暂未扣减）
	var skiRI2, skiItem2 int64
	tx.QueryRow(`SELECT ri.id, ri.item_id FROM rental_items ri
		WHERE ri.rental_id=$1 AND ri.equipment_type='ski'`, rD).Scan(&skiRI2, &skiItem2)
	tx.Exec(`UPDATE equipment_items SET status='repair', current_rental_item_id=NULL WHERE id=$1`, skiItem2)
	tx.Exec(`UPDATE rental_items SET status='repair' WHERE id=$1`, skiRI2)
	var dmgID2, roID2 int64
	tx.QueryRow(`INSERT INTO damage_reports
		(rental_item_id, reporter_id, stage, damage_type, severity, description, photos,
		 status, deduction, assessed_by)
		VALUES ($1,$2,'post_inspection','edge_damage','medium','后检发现板刃有 2cm 崩口',
		 '[]'::jsonb,'repair_created',525,$2) RETURNING id`, skiRI2, repairUID).Scan(&dmgID2)
	tx.QueryRow(`INSERT INTO repair_orders(item_id, damage_id, issue, advice, status)
		VALUES ($1,$2,'后检发现-板刃损伤：板刃崩口','磨刃/补刃，检测刃口平直度','queued') RETURNING id`,
		skiItem2, dmgID2).Scan(&roID2)
	tx.Exec(`UPDATE damage_reports SET repair_order_id=$1 WHERE id=$2`, roID2, dmgID2)
	tx.Exec(`INSERT INTO damage_notifications(rental_id, damage_id, guest_id, amount, appeal_until)
		VALUES ($1,$2,$3,525, now()+interval '47 hour')`, rD, dmgID2, g7)
	// 护具：仍在待检区排队后检
	var protRI, protItem int64
	tx.QueryRow(`SELECT ri.id, ri.item_id FROM rental_items ri
		WHERE ri.rental_id=$1 AND ri.equipment_type='protector'`, rD).Scan(&protRI, &protItem)
	tx.Exec(`UPDATE equipment_items SET status='inspection' WHERE id=$1`, protItem)
	tx.Exec(`UPDATE rental_items SET status='pending_inspection' WHERE id=$1`, protRI)
	// 其余三件后检合格已上架
	tx.Exec(`UPDATE rental_items SET status='returned_ok', returned_at=now()
		WHERE rental_id=$1 AND equipment_type IN ('boot','helmet','pole')`, rD)
	tx.Exec(`UPDATE equipment_items SET status='available', current_rental_item_id=NULL
		WHERE id IN (SELECT item_id FROM rental_items
			WHERE rental_id=$1 AND equipment_type IN ('boot','helmet','pole'))`, rD)

	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Println("种子数据写入完成")
	return nil
}

func nextClock(hour, min int) time.Time {
	now := time.Now()
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, time.Local)
	if t.Before(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func mkUserLookup(tx *sql.Tx, phone string) int64 {
	var id int64
	tx.QueryRow(`SELECT id FROM users WHERE phone=$1`, phone).Scan(&id)
	return id
}

type lineSpec struct {
	etype, modelCode, size string
	fee, deposit           float64
}

func addLines(tx *sql.Tx, rentalID int64, lines []lineSpec, status string) {
	for _, l := range lines {
		var modelID int64
		tx.QueryRow(`SELECT id FROM equipment_models WHERE code=$1`, l.modelCode).Scan(&modelID)
		tx.Exec(`INSERT INTO rental_items
			(rental_id, equipment_type, model_id, recommended_size, reason, fee, deposit, status)
			VALUES ($1,$2,$3,$4,'按身高体重脚码与水平生成建议',$5,$6,$7)`,
			rentalID, l.etype, modelID, l.size, l.fee, l.deposit, status)
	}
}

// assignItems 为已存在的租赁明细各分配一件该型号的可用雪具并更新状态
func assignItems(tx *sql.Tx, rentalID int64) {
	rows, err := tx.Query(`SELECT id, model_id, status FROM rental_items WHERE rental_id=$1 ORDER BY id`, rentalID)
	if err != nil {
		panic(err)
	}
	type x struct {
		id, model int64
		st        string
	}
	var xs []x
	for rows.Next() {
		var v x
		rows.Scan(&v.id, &v.model, &v.st)
		xs = append(xs, v)
	}
	rows.Close()
	for _, v := range xs {
		var itemID int64
		if err := tx.QueryRow(`SELECT id FROM equipment_items
			WHERE model_id=$1 AND status='available' ORDER BY id LIMIT 1 FOR UPDATE`,
			v.model).Scan(&itemID); err != nil {
			panic(fmt.Sprintf("型号 %d 库存不足: %v", v.model, err))
		}
		newStatus := "out"
		itemStatus := "out"
		if v.st == "assigned" {
			newStatus, itemStatus = "assigned", "reserved"
		}
		tx.Exec(`UPDATE equipment_items SET status=$1, current_rental_item_id=$2 WHERE id=$3`,
			itemStatus, v.id, itemID)
		tx.Exec(`UPDATE rental_items SET item_id=$1, status=$2 WHERE id=$3`,
			itemID, newStatus, v.id)
	}
}

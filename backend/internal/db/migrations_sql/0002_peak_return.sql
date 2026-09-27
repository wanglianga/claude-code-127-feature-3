-- 高峰归还快速签收 + 维修间后检 + 赔付通知/申诉 + 押金延迟释放

-- 租赁单：新增「已快速签收」状态与快签信息
ALTER TABLE rentals DROP CONSTRAINT rentals_status_check;
ALTER TABLE rentals ADD CONSTRAINT rentals_status_check
    CHECK (status IN ('draft','reserved','equipped','return_pending','quick_signed','returned','cancelled'));
ALTER TABLE rentals
    ADD COLUMN quick_signed_at TIMESTAMPTZ,          -- 快速签收时间（视为实际归还时间）
    ADD COLUMN quick_signed_by BIGINT REFERENCES users(id),
    ADD COLUMN contact_phone   TEXT NOT NULL DEFAULT '', -- 快签时留存的游客联系方式
    ADD COLUMN appeal_deadline TIMESTAMPTZ;          -- 后检赔付通知的申诉期限

-- 租赁明细：新增「待后检」状态（快签后雪具在维修间待检区）
ALTER TABLE rental_items DROP CONSTRAINT rental_items_status_check;
ALTER TABLE rental_items ADD CONSTRAINT rental_items_status_check
    CHECK (status IN ('suggested','assigned','out','exchanged','returned_ok','returned_damaged',
                      'repair','lost','pending_inspection'));

-- 损坏报告：新增「后检发现」阶段
ALTER TABLE damage_reports DROP CONSTRAINT damage_reports_stage_check;
ALTER TABLE damage_reports ADD CONSTRAINT damage_reports_stage_check
    CHECK (stage IN ('during_rental','at_return','post_inspection'));

-- 后检赔付通知：维修间后检发现问题时生成，
-- 赔付不能直接跳过游客确认——游客确认 / 申诉复核驳回 / 申诉期结束 之后才扣减押金
CREATE TABLE damage_notifications (
    id            BIGSERIAL PRIMARY KEY,
    rental_id     BIGINT NOT NULL REFERENCES rentals(id),
    damage_id     BIGINT NOT NULL REFERENCES damage_reports(id),
    guest_id      BIGINT NOT NULL REFERENCES users(id),
    amount        NUMERIC(10,2) NOT NULL,          -- 拟扣减押金金额
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','appealed','confirmed','cancelled')),
    appeal_reason TEXT NOT NULL DEFAULT '',
    appeal_until  TIMESTAMPTZ NOT NULL,            -- 申诉截止（与租赁单 appeal_deadline 对齐）
    notified_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at   TIMESTAMPTZ,                     -- 确认/复核/过期时间
    resolved_by   BIGINT REFERENCES users(id),     -- 申诉复核人
    review_note   TEXT NOT NULL DEFAULT '',        -- 复核说明
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_dn_rental ON damage_notifications(rental_id);
CREATE INDEX idx_dn_guest ON damage_notifications(guest_id, status);

-- 维修间容量设置（单行）：待检区容量与维修工位容量，柜台大屏实时展示
CREATE TABLE repair_capacity (
    id               SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    inspect_capacity INT NOT NULL DEFAULT 6,
    repair_capacity  INT NOT NULL DEFAULT 8,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO repair_capacity(id) VALUES (1);

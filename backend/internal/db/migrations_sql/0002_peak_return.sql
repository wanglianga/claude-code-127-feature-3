-- 高峰归还排队：快速签收 + 维修间后检 + 赔付通知/申诉
-- 场景：午后课程集中结束，游客赶班车时柜台先快速签收，雪具转维修间后检；
--       后检发现损坏生成赔付通知，必须经游客确认（或申诉期届满）后才扣减押金并释放。

-- 租赁单状态扩展：quick_signed = 快速签收待后检
ALTER TABLE rentals DROP CONSTRAINT rentals_status_check;
ALTER TABLE rentals ADD CONSTRAINT rentals_status_check
    CHECK (status IN ('draft','reserved','equipped','return_pending','quick_signed','returned','cancelled'));

-- 租赁明细状态扩展：pending_inspection = 快速签收后待维修间后检
ALTER TABLE rental_items DROP CONSTRAINT rental_items_status_check;
ALTER TABLE rental_items ADD CONSTRAINT rental_items_status_check
    CHECK (status IN ('suggested','assigned','out','exchanged','pending_inspection',
                      'returned_ok','returned_damaged','repair','lost'));

-- 损坏报告阶段扩展：post_inspection = 快速签收后的维修间后检
ALTER TABLE damage_reports DROP CONSTRAINT damage_reports_stage_check;
ALTER TABLE damage_reports ADD CONSTRAINT damage_reports_stage_check
    CHECK (stage IN ('during_rental','at_return','post_inspection'));

-- 快速签收单：保留游客联系方式与申诉期限，押金暂扣至后检+申诉期结束
CREATE TABLE quick_signoffs (
    id              BIGSERIAL PRIMARY KEY,
    rental_id       BIGINT NOT NULL REFERENCES rentals(id),
    ticket_id       BIGINT,                                  -- 对应归还队列票
    signed_by       BIGINT NOT NULL REFERENCES users(id),    -- 柜台操作人
    contact_phone   TEXT NOT NULL,                           -- 保留的游客联系方式
    shuttle_note    TEXT NOT NULL DEFAULT '',                -- 赶班车备注
    appeal_deadline TIMESTAMPTZ NOT NULL,                    -- 申诉期限
    status          TEXT NOT NULL DEFAULT 'pending_inspection'
                    CHECK (status IN ('pending_inspection','inspected','settled')),
    inspected_by    BIGINT REFERENCES users(id),             -- 维修间后检人
    inspected_at    TIMESTAMPTZ,
    settled_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_qs_status ON quick_signoffs(status);
CREATE INDEX idx_qs_rental ON quick_signoffs(rental_id);

-- 赔付通知：后检发现问题 -> 通知游客 -> 确认/申诉，申诉期届满默认确认
CREATE TABLE damage_notifications (
    id               BIGSERIAL PRIMARY KEY,
    quick_signoff_id BIGINT NOT NULL REFERENCES quick_signoffs(id),
    damage_id        BIGINT NOT NULL REFERENCES damage_reports(id),
    rental_id        BIGINT NOT NULL REFERENCES rentals(id),
    guest_id         BIGINT NOT NULL REFERENCES users(id),
    contact_phone    TEXT NOT NULL,                          -- 通知时留存的联系方式
    amount           NUMERIC(10,2) NOT NULL,                 -- 拟扣减押金金额
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending','confirmed','appealed','resolved','expired')),
    resolution       TEXT CHECK (resolution IS NULL OR resolution IN ('upheld','cancelled')),
    appeal_text      TEXT NOT NULL DEFAULT '',
    appeal_deadline  TIMESTAMPTZ NOT NULL,
    notified_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    responded_at     TIMESTAMPTZ,
    resolved_by      BIGINT REFERENCES users(id),
    resolution_note  TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_dn_rental ON damage_notifications(rental_id);
CREATE INDEX idx_dn_guest ON damage_notifications(guest_id, status);
CREATE INDEX idx_dn_pending ON damage_notifications(status, appeal_deadline);

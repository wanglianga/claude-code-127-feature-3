package rules

import "time"

// 费用与赔付规则

// 夜场加价系数（夜场租赁每件租金上浮 20%，押金不变）
const NightPremiumRate = 0.20

// AppealWindowHours 快速签收后检赔付通知的默认申诉期限（小时）
const AppealWindowHours = 48

// AppealMaxHours 申诉期限上限（小时），防止柜台误填过长
const AppealMaxHours = 168

// AppealDeadline 计算申诉截止时间；hours<=0 或超上限时回落到默认 48 小时
func AppealDeadline(hours float64) time.Time {
	if hours <= 0 || hours > AppealMaxHours {
		hours = AppealWindowHours
	}
	return time.Now().Add(time.Duration(hours * float64(time.Hour)))
}

// 延时费用：每超时 1 小时按基础租金/4 加收（取日租金 4 小时折算）
func LateFee(hours float64, baseRentalFee float64) float64 {
	if hours <= 0 {
		return 0
	}
	return round2(hours * baseRentalFee / 4)
}

// DamageRule 每种损坏类型的赔付比例与维修建议
type DamageRule struct {
	Label      string  `json:"label"`
	DeductRate float64 `json:"deduct_rate"` // 占该件押金比例
	RepairDays int     `json:"repair_days"`
	Advice     string  `json:"advice"`
	RetireItem bool    `json:"retire_item"` // 是否建议报废
}

var damageMatrix = map[string]DamageRule{
	"edge_damage":   {"板刃损伤", 0.35, 2, "磨刃/补刃，检测刃口平直度", false},
	"base_scratch":  {"板底划伤", 0.15, 1, "清洁打蜡，深划痕需补板底", false},
	"binding_issue": {"固定器故障", 0.30, 1, "重新调校 DIN 值并测试释放", false},
	"liner_wet":     {"雪鞋内衬潮湿", 0.05, 1, "烘干消毒，检查防水膜", false},
	"shell_crack":   {"护具/壳体裂纹", 0.50, 0, "裂纹件停止出租，更换壳体", true},
	"strap_broken":  {"绑带断裂", 0.12, 1, "更换绑带与卡扣", false},
	"lost":          {"雪具丢失", 1.00, 0, "按押金全额扣减并销账", true},
	"other":         {"其他异常", 0.10, 1, "维修间目检后定级", false},
}

func DamageRuleOf(damageType string) (DamageRule, bool) {
	r, ok := damageMatrix[damageType]
	return r, ok
}

// DamageDeduction 根据损坏类型与押金计算建议扣减；
// heavy 上浮 50%，light 下浮 50%，lost 恒为全额。
func DamageDeduction(damageType, severity string, deposit float64) (float64, DamageRule) {
	r := damageMatrix[damageType]
	if damageType == "lost" {
		return round2(deposit), r
	}
	rate := r.DeductRate
	switch severity {
	case "light":
		rate *= 0.5
	case "heavy":
		rate *= 1.5
	}
	d := deposit * rate
	if d > deposit {
		d = deposit
	}
	return round2(d), r
}

// ClaimResult 保险理赔试算
type ClaimResult struct {
	Requested  float64 `json:"requested"`
	Deductible float64 `json:"deductible"`
	Approved   float64 `json:"approved"`
	Payout     float64 `json:"payout"` // 实际返还给游客（抵扣押金扣减）
	Covered    bool    `json:"covered"`
	Message    string  `json:"message"`
}

// 理赔：保单覆盖损坏类型时，赔付 = min(扣减-免赔额, 保额上限)，该金额用于冲抵押金扣减
func CalcClaim(deduction, deductible, cap float64, covered bool) ClaimResult {
	res := ClaimResult{Requested: round2(deduction), Deductible: round2(deductible)}
	if !covered {
		res.Message = "该损坏类型不在保险范围内，押金扣减全部由游客承担"
		return res
	}
	if deduction <= deductible {
		res.Approved = round2(deduction)
		res.Payout = 0
		res.Covered = true
		res.Message = "扣减金额低于免赔额，保险不予赔付"
		return res
	}
	payout := deduction - deductible
	if payout > cap {
		payout = cap
	}
	res.Approved = round2(deduction)
	res.Payout = round2(payout)
	res.Covered = true
	res.Message = "保险赔付冲抵押金扣减，游客仅承担免赔额及超出保额部分"
	return res
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

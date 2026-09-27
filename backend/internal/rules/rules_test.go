package rules

import (
	"testing"
	"time"
)

func TestSuggestSkiBeginner(t *testing.T) {
	s := SuggestSki(Profile{HeightCM: 170, WeightKG: 65, Level: "beginner"})
	// 170*0.85 = 144.5 -> 145
	if s.Size != "145cm" {
		t.Fatalf("beginner ski = %s, want 145cm", s.Size)
	}
	if !SkiFitOK(145, 150) || !SkiFitOK(145, 140) {
		t.Fatal("±5cm should fit")
	}
	if SkiFitOK(145, 151) {
		t.Fatal("+6cm should not fit")
	}
}

func TestSuggestSkiAdvancedHeavy(t *testing.T) {
	// 身高180 体重90 -> BMI 27.8 >=26, 高级 0.95: 171+5=176 -> 175
	s := SuggestSki(Profile{HeightCM: 180, WeightKG: 90, Level: "advanced"})
	if s.Size != "175cm" {
		t.Fatalf("advanced heavy ski = %s, want 175cm", s.Size)
	}
}

func TestBootFit(t *testing.T) {
	s := SuggestBoot(Profile{FootEU: 42})
	if s.Size != "EU42" {
		t.Fatalf("boot = %s", s.Size)
	}
	if !BootFitOK(42, 42.5) || BootFitOK(42, 43) {
		t.Fatal("boot tolerance ±0.5")
	}
	s2 := SuggestBoot(Profile{FootEU: 42.5})
	if s2.Size != "EU42.5" {
		t.Fatalf("boot half size = %s", s2.Size)
	}
}

func TestHelmetSizes(t *testing.T) {
	cases := map[int]string{50: "S", 56: "M", 59: "L", 62: "XL", 0: "M"}
	for head, want := range cases {
		if got, _ := helmetSize(head); got != want {
			t.Fatalf("head %d = %s want %s", head, got, want)
		}
	}
}

func TestPole(t *testing.T) {
	s := SuggestPole(Profile{HeightCM: 170})
	// 170*0.7=119 -> 120
	if s.Size != "120cm" {
		t.Fatalf("pole = %s want 120cm", s.Size)
	}
	b := SuggestPole(Profile{HeightCM: 170, Level: "beginner"})
	if !b.Optional {
		t.Fatal("beginner pole should be optional")
	}
}

func TestProtector(t *testing.T) {
	if SuggestProtector(Profile{HeightCM: 180, WeightKG: 70}).Size != "XL" {
		t.Fatal("180cm -> XL")
	}
	if SuggestProtector(Profile{HeightCM: 140, WeightKG: 50}).Size != "S" {
		t.Fatal("140cm -> S")
	}
	if SuggestProtector(Profile{HeightCM: 160, WeightKG: 55}).Size != "M" {
		t.Fatal("160cm/55kg -> M")
	}
	if SuggestProtector(Profile{HeightCM: 170, WeightKG: 70}).Size != "L" {
		t.Fatal("170cm/70kg -> L")
	}
}

func TestAllSuggestionsCount(t *testing.T) {
	if n := len(AllSuggestions(Profile{HeightCM: 170, WeightKG: 65, FootEU: 42, HeadCM: 57, Level: "beginner"})); n != 5 {
		t.Fatalf("got %d suggestions, want 5", n)
	}
}

func TestDamageDeduction(t *testing.T) {
	d, r := DamageDeduction("edge_damage", "medium", 1000)
	if d != 350 || r.RetireItem {
		t.Fatalf("edge medium = %v retire=%v", d, r.RetireItem)
	}
	d, _ = DamageDeduction("edge_damage", "light", 1000)
	if d != 175 {
		t.Fatalf("edge light = %v want 175", d)
	}
	d, _ = DamageDeduction("edge_damage", "heavy", 1000)
	if d != 525 {
		t.Fatalf("edge heavy = %v want 525", d)
	}
	d, r = DamageDeduction("lost", "lost", 800)
	if d != 800 || !r.RetireItem {
		t.Fatalf("lost = %v", d)
	}
	d, _ = DamageDeduction("shell_crack", "heavy", 1000)
	if d != 750 { // 0.5*1.5 capped below deposit
		t.Fatalf("crack heavy = %v want 750", d)
	}
}

func TestClaim(t *testing.T) {
	// 覆盖：扣 350，免赔 100，上限 1000 -> 赔 250
	c := CalcClaim(350, 100, 1000, true)
	if c.Payout != 250 {
		t.Fatalf("payout = %v want 250", c.Payout)
	}
	// 低于免赔
	c = CalcClaim(50, 100, 1000, true)
	if c.Payout != 0 {
		t.Fatalf("low claim payout = %v want 0", c.Payout)
	}
	// 超保额
	c = CalcClaim(1200, 100, 500, true)
	if c.Payout != 500 {
		t.Fatalf("capped payout = %v want 500", c.Payout)
	}
	// 不覆盖
	c = CalcClaim(350, 0, 1000, false)
	if c.Payout != 0 || c.Covered {
		t.Fatal("uncovered claim must not pay")
	}
}

func TestLateFeeAndNight(t *testing.T) {
	if LateFee(2, 400) != 200 {
		t.Fatalf("late fee = %v want 200", LateFee(2, 400))
	}
	if LateFee(0, 400) != 0 {
		t.Fatal("zero hours = zero fee")
	}
}

func TestAppealDeadline(t *testing.T) {
	// 默认 48 小时
	d := AppealDeadline(0)
	if hrs := time.Until(d).Hours(); hrs < 47.9 || hrs > 48.1 {
		t.Fatalf("默认申诉期应约 48h，实际 %.2f", hrs)
	}
	// 自定义 2 小时
	d = AppealDeadline(2)
	if hrs := time.Until(d).Hours(); hrs < 1.9 || hrs > 2.1 {
		t.Fatalf("自定义申诉期应约 2h，实际 %.2f", hrs)
	}
	// 超上限回落默认
	d = AppealDeadline(9999)
	if hrs := time.Until(d).Hours(); hrs < 47.9 || hrs > 48.1 {
		t.Fatalf("超上限应回落 48h，实际 %.2f", hrs)
	}
}

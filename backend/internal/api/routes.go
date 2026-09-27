package api

func (s *Server) registerRoutes() {
	authAny := s.withAuth()
	authGuest := s.withAuth("guest")
	authCounter := s.withAuth("counter", "admin")
	authCoach := s.withAuth("coach", "admin")
	authRepair := s.withAuth("repair", "admin")
	authIns := s.withAuth("insurance", "admin")
	authFin := s.withAuth("finance", "admin")
	authAdmin := s.withAuth("admin")
	staff := s.withAuth("counter", "coach", "repair", "insurance", "finance", "admin")

	// 认证
	s.add("POST", "/api/auth/login", s.login)
	s.add("GET", "/api/me", authAny(s.me))

	// 目录（登录可见）
	s.add("GET", "/api/courses", authAny(s.listCourses))
	s.add("GET", "/api/slots", authAny(s.listSlots))
	s.add("GET", "/api/models", authAny(s.listModels))
	s.add("GET", "/api/insurance-products", authAny(s.listInsuranceProducts))
	s.add("GET", "/api/queue-summary", staff(s.queueSummary))

	// 游客
	s.add("POST", "/api/guest/suggestions", authGuest(s.suggestions))
	s.add("GET", "/api/teams/mine", authGuest(s.myTeams))
	s.add("POST", "/api/teams", authGuest(s.createTeam))
	s.add("POST", "/api/rentals", authGuest(s.createRental))
	s.add("GET", "/api/rentals/mine", authGuest(s.myRentals))
	s.add("GET", "/api/rentals/{id}", authAny(s.getRental))
	s.add("POST", "/api/rentals/{id}/confirm", authGuest(s.confirmRental))
	s.add("POST", "/api/rentals/{id}/exchange-request", authGuest(s.requestExchange))
	s.add("POST", "/api/rentals/{id}/extend", authGuest(s.extendRental))
	s.add("POST", "/api/rentals/{id}/damage", authGuest(s.reportDamage))
	s.add("POST", "/api/rentals/{id}/queue-return", authGuest(s.queueReturn))
	s.add("POST", "/api/rentals/{id}/feedback", authGuest(s.submitFeedback))
	// 快签后检赔付通知（游客确认/申诉）
	s.add("GET", "/api/guest/notifications", authGuest(s.myNotifications))
	s.add("POST", "/api/guest/notifications/{id}/confirm", authGuest(s.confirmNotification))
	s.add("POST", "/api/guest/notifications/{id}/appeal", authGuest(s.appealNotification))

	// 柜台
	s.add("GET", "/api/counter/queue", authCounter(s.counterQueue))
	s.add("POST", "/api/counter/call", authCounter(s.callNext))
	s.add("POST", "/api/counter/tickets/{id}/finish", authCounter(s.finishTicket))
	s.add("POST", "/api/counter/scan", authCounter(s.scanItem))
	s.add("POST", "/api/counter/checkout", authCounter(s.checkout))
	s.add("POST", "/api/counter/exchanges/{id}/process", authCounter(s.processExchange))
	s.add("POST", "/api/counter/damages/{id}/assess", authCounter(s.assessDuringRental))
	s.add("POST", "/api/counter/returns/{rid}/inspect", authCounter(s.inspectReturnItem))
	s.add("POST", "/api/counter/returns/{rid}/finish", authCounter(s.finishReturn))
	// 高峰快速签收与申诉复核
	s.add("POST", "/api/counter/returns/{rid}/quick-sign", authCounter(s.quickSignReturn))
	s.add("GET", "/api/counter/appeals", authCounter(s.listAppeals))
	s.add("POST", "/api/counter/appeals/{id}/review", authCounter(s.reviewAppeal))

	// 教练
	s.add("GET", "/api/coach/courses", authCoach(s.coachCourses))
	s.add("POST", "/api/coach/fit-check", authCoach(s.fitCheck))

	// 维修间
	s.add("GET", "/api/repair/orders", authRepair(s.repairOrders))
	s.add("POST", "/api/repair/orders/{id}/update", authRepair(s.updateRepairOrder))
	s.add("GET", "/api/repair/maintenance", authRepair(s.listMaintenance))
	s.add("POST", "/api/repair/maintenance/generate", authRepair(s.generateMaintenance))
	s.add("POST", "/api/repair/maintenance/{id}/resolve", authRepair(s.resolveMaintenance))
	// 待检区与后检（高峰快签）
	s.add("GET", "/api/repair/pending-inspections", staff(s.pendingInspections))
	s.add("POST", "/api/repair/inspections/{id}/resolve", authRepair(s.postInspect))
	s.add("GET", "/api/repair/capacity", staff(s.repairCapacity))
	s.add("POST", "/api/repair/capacity", authRepair(s.updateRepairCapacity))

	// 保险
	s.add("GET", "/api/insurance/claims", authIns(s.listClaims))
	s.add("POST", "/api/insurance/claims/{id}/review", authIns(s.reviewClaim))
	s.add("POST", "/api/insurance/claims/{id}/pay", authIns(s.payClaim))
	s.add("GET", "/api/insurance/policies", authIns(s.listPolicies))

	// 财务
	s.add("GET", "/api/finance/summary", authFin(s.financeSummary))
	s.add("GET", "/api/finance/ledger", authFin(s.ledger))

	// 管理
	s.add("GET", "/api/admin/analytics", authAdmin(s.analytics))
	s.add("GET", "/api/admin/equipment-trace", authAdmin(s.equipmentTrace))
	s.add("POST", "/api/admin/models", authAdmin(s.createModel))
	s.add("POST", "/api/admin/items", authAdmin(s.createItem))

	// 照片上传（员工与游客都可上传，用于损坏取证）
	s.add("POST", "/api/uploads", authAny(s.uploadPhoto))
}

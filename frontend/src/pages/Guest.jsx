import React, { useEffect, useState } from 'react'
import { api, yuan, TYPE_LABEL, NOTIF_STATUS } from '../api.js'
import { Badge, Field, Loading, Empty, useToast } from '../components/ui.jsx'
import RentalDetail, { statusBadge } from '../components/RentalDetail.jsx'

export default function GuestPage({ page, go }) {
  if (page === 'new') return <NewRental onCreated={() => go('rentals')} />
  if (page === 'suggest') return <Suggest />
  if (page === 'notifications') return <Notifications go={go} />
  return <MyRentals go={go} />
}

// ---------- 后检赔付通知（快速签收后） ----------
function Notifications({ go }) {
  const [list, setList] = useState(null)
  const toast = useToast()
  const load = async () => setList(await api('GET', '/guest/notifications'))
  useEffect(() => { load().catch(() => {}) }, [])
  const confirmOne = async (n) => {
    try {
      const d = await api('POST', `/guest/notifications/${n.id}/confirm`, {})
      toast(d.message); load()
    } catch (e) { toast(e.message, 'error') }
  }
  const appeal = async (n, reason) => {
    try {
      const d = await api('POST', `/guest/notifications/${n.id}/appeal`, { reason })
      toast(d.message); load()
    } catch (e) { toast(e.message, 'error') }
  }
  if (!list) return <Loading />
  return (
    <div>
      <h2 style={{ margin: 0, fontSize: 18 }}>后检赔付通知（快速签收）</h2>
      <p className="muted small">赶班车走快速签收通道后，雪具由维修间后检。若发现损坏，赔付通知会发到这里：
        你可以<b>确认扣减</b>，或在申诉期内<b>发起申诉</b>；申诉期结束未处理视为确认，押金随后释放。</p>
      {list.length === 0 && <Empty text="暂无后检赔付通知" />}
      <div className="grid grid-2">
        {list.map(n => <NotificationCard key={n.id} n={n} onConfirm={confirmOne} onAppeal={appeal} go={go} />)}
      </div>
    </div>
  )
}

function NotificationCard({ n, onConfirm, onAppeal, go }) {
  const [appealing, setAppealing] = useState(false)
  const [reason, setReason] = useState('')
  const color = n.status === 'pending' ? 'orange' : n.status === 'appealed' ? 'purple'
    : n.status === 'confirmed' ? 'red' : 'green'
  return (
    <div className="card">
      <div className="btn-row">
        <b>{n.rental_code} · {n.item_label}</b>
        <span className="spacer" style={{ flex: 1 }} />
        <Badge color={color}>{NOTIF_STATUS[n.status] || n.status}</Badge>
      </div>
      <div className="alert red small" style={{ marginTop: 8 }}>
        后检发现：{n.type_label}（{n.severity === 'light' ? '轻微' : n.severity === 'heavy' ? '严重' : '中等'}）
        · 拟扣减押金 <b>{yuan(n.amount)}</b>
        <div>{n.description}</div>
        {n.photos?.length > 0 && <div className="photos">{n.photos.map((p, i) => <img key={i} src={p} alt="" />)}</div>}
      </div>
      <div className="small muted" style={{ marginTop: 6 }}>通知时间 {n.notified_at} · 申诉截止 {n.appeal_until}</div>
      {n.status === 'pending' && <div className="btn-row" style={{ marginTop: 8 }}>
        <button className="btn" onClick={() => onConfirm(n)}>确认扣减并释放押金</button>
        {n.can_appeal ? <button className="btn warn" onClick={() => setAppealing(v => !v)}>发起申诉</button>
          : <Badge color="gray">申诉期已过</Badge>}
      </div>}
      {appealing && <div style={{ marginTop: 8 }}>
        <Field label="申诉理由（如：归还时完好、损坏非本人使用造成等）">
          <textarea rows={3} value={reason} onChange={e => setReason(e.target.value)} />
        </Field>
        <div className="btn-row">
          <button className="btn ghost" onClick={() => setAppealing(false)}>取消</button>
          <button className="btn warn" disabled={!reason.trim()}
            onClick={() => onAppeal(n, reason.trim())}>提交申诉（押金继续冻结待复核）</button>
        </div>
      </div>}
      {n.status === 'appealed' && <div className="alert warn small" style={{ marginTop: 8 }}>
        <b>已申诉：</b>{n.appeal_reason}<div>柜台复核中，复核结果将更新在此处。</div>
      </div>}
      {n.status === 'cancelled' && <div className="alert info small" style={{ marginTop: 8 }}>
        申诉成立，该笔扣减已取消。{n.review_note && <div>复核说明：{n.review_note}</div>}
      </div>}
      {n.status === 'confirmed' && n.review_note && <div className="small muted" style={{ marginTop: 6 }}>复核说明：{n.review_note}</div>}
      <div className="modal-foot" style={{ paddingRight: 0 }}>
        <button className="btn sm ghost" onClick={() => go('rentals')}>查看租赁单与押金流水</button>
      </div>
    </div>
  )
}

function MyRentals({ go }) {
  const [list, setList] = useState(null)
  const [openId, setOpenId] = useState(null)
  const load = async () => setList(await api('GET', '/rentals/mine'))
  useEffect(() => { load().catch(() => {}) }, [])
  if (!list) return <Loading />
  return (
    <div>
      <h2 style={{ margin: 0, fontSize: 18 }}>我的租赁单</h2>
      {list.length === 0 && <Empty text="还没有租赁单，去「新建租赁预约」试试" />}
      <div className="table-wrap card" style={{ padding: 0 }}>
        <table>
          <thead><tr><th>单号</th><th>场次</th><th>课程</th><th>押金</th><th>租金/保费</th><th>状态</th><th>时间</th><th></th></tr></thead>
          <tbody>
            {list.map(r => (
              <tr key={r.id}>
                <td className="mono">{r.code}</td>
                <td>{r.session_type === 'night' ? '🌙 夜场' : '☀️ 日场'}</td>
                <td>{r.course_name || <span className="muted">自由滑</span>}</td>
                <td>{yuan(r.deposit_total)}</td>
                <td className="small">{yuan(r.fee_total)} / {yuan(r.premium_total)}</td>
                <td>{statusBadge(r.status)}</td>
                <td className="small muted nowrap">{String(r.created_at).slice(0, 16)}</td>
                <td><button className="btn sm ghost" onClick={() => setOpenId(r.id)}>详情/操作</button></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {openId && <RentalDetail id={openId} asGuest onClose={() => setOpenId(null)} onChanged={load} />}
    </div>
  )
}

// ---------- 尺码建议页 ----------
function Suggest() {
  const [me, setMe] = useState(null)
  const [form, setForm] = useState({ height_cm: '', weight_kg: '', foot_eu: '', head_cm: '', level: 'beginner', session_type: 'day' })
  const [res, setRes] = useState(null)
  const toast = useToast()
  useEffect(() => { api('GET', '/me').then(u => { setMe(u); setForm(f => ({
    ...f, height_cm: u.height_cm || '', weight_kg: u.weight_kg || '',
    foot_eu: u.foot_eu || '', head_cm: u.head_cm || '', level: u.level || 'beginner'
  })) }) }, [])
  const submit = async () => {
    try { setRes(await api('POST', '/guest/suggestions', form)) } catch (e) { toast(e.message, 'error') }
  }
  return (
    <div>
      <h2 style={{ fontSize: 18 }}>雪具尺码智能建议</h2>
      <p className="muted small">根据身高、体重、脚码、头围、滑雪水平与课程场次，推荐雪板/雪鞋/头盔/雪杖/护具。</p>
      <div className="card">
        <div className="row">
          <Field label="身高 cm"><input type="number" value={form.height_cm} onChange={e => setForm({ ...form, height_cm: e.target.value })} /></Field>
          <Field label="体重 kg"><input type="number" value={form.weight_kg} onChange={e => setForm({ ...form, weight_kg: e.target.value })} /></Field>
          <Field label="脚码 EU"><input type="number" step="0.5" value={form.foot_eu} onChange={e => setForm({ ...form, foot_eu: e.target.value })} /></Field>
          <Field label="头围 cm"><input type="number" value={form.head_cm} onChange={e => setForm({ ...form, head_cm: e.target.value })} /></Field>
        </div>
        <div className="row">
          <Field label="滑雪水平">
            <select value={form.level} onChange={e => setForm({ ...form, level: e.target.value })}>
              <option value="beginner">初学者</option><option value="intermediate">进阶</option><option value="advanced">高级</option>
            </select>
          </Field>
          <Field label="场次">
            <select value={form.session_type} onChange={e => setForm({ ...form, session_type: e.target.value })}>
              <option value="day">☀️ 日场</option><option value="night">🌙 夜场（租金 +20%）</option>
            </select>
          </Field>
          <div style={{ alignSelf: 'flex-end' }}><button className="btn" onClick={submit}>生成建议</button></div>
        </div>
      </div>
      {res && <div className="grid grid-3">
        {res.suggestions.map(s => (
          <div className="card" key={s.equipment_type}>
            <div className="btn-row"><h3 style={{ margin: 0 }}>{TYPE_LABEL[s.equipment_type]}</h3>
              <span className="spacer" style={{ flex: 1 }} />
              <Badge color="cyan">{s.size}</Badge>{s.optional && <Badge color="gray">可选</Badge>}
            </div>
            <p className="small" style={{ margin: '8px 0 4px' }}>{s.reason}</p>
            {s.safety_note && <div className="alert info" style={{ margin: '6px 0 0' }}>⚠️ {s.safety_note}</div>}
          </div>
        ))}
      </div>}
    </div>
  )
}

// ---------- 新建租赁向导 ----------
function NewRental({ onCreated }) {
  const [step, setStep] = useState(1)
  const [courses, setCourses] = useState([])
  const [slots, setSlots] = useState([])
  const [products, setProducts] = useState([])
  const [teams, setTeams] = useState([])
  const [form, setForm] = useState({
    session_type: 'day', course_id: 0, slot_id: 0, team_id: 0,
    insurance_product_id: 0, ticket_no: '', include: { pole: false, protector: true }
  })
  const [created, setCreated] = useState(null)
  const [busy, setBusy] = useState(false)
  const toast = useToast()

  useEffect(() => {
    api('GET', '/courses').then(setCourses)
    api('GET', '/slots').then(setSlots)
    api('GET', '/insurance-products').then(setProducts)
    api('GET', '/teams/mine').then(setTeams)
  }, [])

  const submit = async () => {
    setBusy(true)
    try {
      const body = {
        ...form,
        course_id: Number(form.course_id) || undefined,
        slot_id: Number(form.slot_id) || undefined,
        team_id: Number(form.team_id) || undefined,
        insurance_product_id: Number(form.insurance_product_id) || undefined
      }
      const d = await api('POST', '/rentals', body)
      setCreated(d)
      setStep(3)
    } catch (e) { toast(e.message, 'error') } finally { setBusy(false) }
  }

  const shownCourses = courses.filter(c => c.session_type === form.session_type)
  const shownSlots = slots.filter(s => s.session_type === form.session_type)

  return (
    <div>
      <h2 style={{ fontSize: 18 }}>新建租赁预约</h2>
      <div className="seg" style={{ marginBottom: 14 }}>
        {['场次与课程', '取具时段与保险', '生成建议'].map((t, i) =>
          <button key={i} className={step === i + 1 ? 'on' : ''} onClick={() => setStep(i + 1)}>{i + 1}. {t}</button>)}
      </div>

      {step === 1 && <div className="card">
        <div className="row">
          <Field label="场次">
            <select value={form.session_type} onChange={e => setForm({ ...form, session_type: e.target.value, course_id: 0 })}>
              <option value="day">☀️ 日场（8 小时）</option><option value="night">🌙 夜场（4 小时，租金+20%）</option>
            </select>
          </Field>
          <Field label="团队（可选，团队单将为每个成员生成一单）">
            <select value={form.team_id} onChange={e => setForm({ ...form, team_id: e.target.value })}>
              <option value={0}>个人</option>
              {teams.map(t => <option key={t.id} value={t.id}>{t.name}（{t.members?.length || 0} 人）</option>)}
            </select>
          </Field>
          <Field label="购票/报名凭证号（可选）"><input value={form.ticket_no} onChange={e => setForm({ ...form, ticket_no: e.target.value })} placeholder="如 TK2026092501" /></Field>
        </div>
        <h4>关联课程（可选）</h4>
        <div className="grid grid-2">
          {shownCourses.length === 0 && <p className="muted small">当前场次暂无可报名课程，可不选课自由滑。</p>}
          {shownCourses.map(c => (
            <div key={c.id} className={'card clickable'} style={{ margin: 0, borderColor: Number(form.course_id) === c.id ? 'var(--brand)' : undefined }}
              onClick={() => setForm({ ...form, course_id: Number(form.course_id) === c.id ? 0 : c.id })}>
              <div className="btn-row"><b>{c.name}</b><span className="spacer" style={{ flex: 1 }} />
                <Badge color={c.session_type === 'night' ? 'gray' : 'blue'}>{c.level === 'beginner' ? '初学' : c.level === 'intermediate' ? '进阶' : '高级'}</Badge></div>
              <div className="small muted">{c.start_at} - {c.end_at} · 教练 {c.coach_name || '待定'} · 集合：{c.meeting_point}</div>
              <div className="small">余位 {c.capacity - c.booked_count}/{c.capacity}</div>
            </div>
          ))}
        </div>
        <div className="modal-foot" style={{ paddingRight: 0 }}><button className="btn" onClick={() => setStep(2)}>下一步</button></div>
      </div>}

      {step === 2 && <div className="card">
        <h4>分时段预约取具（旺季减少大厅排队）</h4>
        <div className="grid grid-3">
          {shownSlots.map(s => {
            const full = s.remaining <= 0
            return <div key={s.id} className={'card clickable'} style={{ margin: 0, opacity: full ? .5 : 1,
                borderColor: Number(form.slot_id) === s.id ? 'var(--brand)' : undefined }}
              onClick={() => !full && setForm({ ...form, slot_id: s.id })}>
              <b>{s.date} {s.start_time}-{s.end_time}</b>
              <div className="small muted">{s.session_type === 'night' ? '夜场' : '日场'}取具</div>
              <div className={s.occupancy_pct >= 85 ? 'small danger-text' : 'small muted'}>
                剩余 {s.remaining}/{s.capacity}{s.remaining <= 3 && s.remaining > 0 ? '（紧张）' : ''}</div>
            </div>
          })}
        </div>
        <h4>保险套餐</h4>
        <div className="grid grid-3">
          <div className={'card clickable'} style={{ margin: 0, borderColor: form.insurance_product_id === 0 ? 'var(--brand)' : undefined }}
            onClick={() => setForm({ ...form, insurance_product_id: 0 })}>
            <b>不购买</b><div className="small muted">损坏赔付全部由押金承担</div>
          </div>
          {products.filter(p => p.session_type === 'both' || p.session_type === form.session_type).map(p => (
            <div key={p.id} className={'card clickable'} style={{ margin: 0, borderColor: Number(form.insurance_product_id) === p.id ? 'var(--brand)' : undefined }}
              onClick={() => setForm({ ...form, insurance_product_id: p.id })}>
              <div className="btn-row"><b>{p.name}</b><span className="spacer" style={{ flex: 1 }} />{yuan(p.premium)}</div>
              <div className="small muted">免赔 {yuan(p.deductible)} · 保额 {yuan(p.coverage_cap)}</div>
              <div className="small">保：{p.covers.map(c => ({ edge_damage: '板刃', base_scratch: '板底', binding_issue: '固定器', liner_wet: '内衬', shell_crack: '壳体', strap_broken: '绑带', lost: '丢失' }[c])).join('、')}</div>
            </div>
          ))}
        </div>
        <h4>可选雪具</h4>
        <label className="checkbox"><input type="checkbox" checked={!!form.include.pole} onChange={e => setForm({ ...form, include: { ...form.include, pole: e.target.checked } })} />租用雪杖（初学者首课可不持杖）</label>
        <label className="checkbox"><input type="checkbox" checked={!!form.include.protector} onChange={e => setForm({ ...form, include: { ...form.include, protector: e.target.checked } })} />租用护具（护臀/护膝，初学者建议佩戴）</label>
        <div className="modal-foot" style={{ paddingRight: 0, justifyContent: 'space-between' }}>
          <button className="btn ghost" onClick={() => setStep(1)}>上一步</button>
          <button className="btn" disabled={busy} onClick={submit}>{busy ? '生成中…' : '生成租赁建议与费用'}</button>
        </div>
      </div>}

      {step === 3 && created && <div className="card">
        <div className="alert info">{created.message}</div>
        <div className="table-wrap">
          <table>
            <thead><tr><th>单号</th><th>游客 ID</th><th>押金</th><th>租金</th><th>保费</th><th></th></tr></thead>
            <tbody>
              {created.rentals.map(r => <tr key={r.rental_id}>
                <td className="mono">{r.code}</td><td>{r.guest_id}</td>
                <td>{yuan(r.deposit_total)}</td><td>{yuan(r.fee_total)}</td><td>{yuan(r.premium_total)}</td>
                <td><button className="btn sm ghost" onClick={onCreated}>去我的租赁确认预约</button></td>
              </tr>)}
            </tbody>
          </table>
        </div>
        <p className="small muted">建议已按身高/体重/脚码/头围/水平生成，请到「我的租赁」打开单据确认取具时段，系统将预留雪具并入取具队列。</p>
      </div>}
    </div>
  )
}

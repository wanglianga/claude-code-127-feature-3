import React, { useEffect, useState, useCallback } from 'react'
import { api, yuan, TYPE_LABEL, DAMAGE_LABEL, DAMAGE_TYPES } from '../api.js'
import { Badge, Modal, Field, Loading, Empty, useToast } from '../components/ui.jsx'

export default function RepairPage({ page }) {
  if (page === 'maintenance') return <Maintenance />
  if (page === 'postinspection') return <PostInspections />
  return <Orders />
}

const RO_STATUS = { queued: ['排队待修', 'orange'], in_progress: ['维修中', 'blue'], waiting_part: ['等配件', 'purple'], done: ['修复可租', 'green'], scrapped: ['已报废', 'red'] }

function Orders() {
  const [orders, setOrders] = useState(null)
  const [detail, setDetail] = useState(null)
  const toast = useToast()
  const load = useCallback(async () => setOrders(await api('GET', '/repair/orders')), [])
  useEffect(() => { load() }, [])
  const [updating, setUpdating] = useState(null)

  const update = async (o, status) => {
    try {
      const d = await api('POST', `/repair/orders/${o.id}/update`, { status, advice: o.advice, repair_cost: Number(o.repair_cost) || 0 })
      toast(d.message); load()
    } catch (e) { toast(e.message, 'error') }
  }

  if (!orders) return <Loading />
  return (
    <div>
      <h2 style={{ fontSize: 18 }}>雪具维修工单</h2>
      <p className="muted small">柜台验损/游客上报自动开单。修复完成雪具重新上架可租；裂纹严重件可直接报废。</p>
      {orders.length === 0 && <Empty text="当前没有待修工单" />}
      <div className="grid grid-2">
        {orders.map(o => (
          <div className="card" key={o.id}>
            <div className="btn-row">
              <h3 style={{ margin: 0 }}>#{o.id} {o.brand} {o.name} {o.size_label}</h3>
              <span className="spacer" style={{ flex: 1 }} />
              <Badge color={(RO_STATUS[o.status] || [, 'gray'])[1]}>{RO_STATUS[o.status]?.[0] || o.status}</Badge>
            </div>
            <div className="small muted">{TYPE_LABEL[o.equipment_type]} · <span className="mono">{o.qr_code}</span> · 累计出租 {o.rental_count} 次</div>
            <div style={{ margin: '8px 0' }}><b>问题：</b>{o.issue}</div>
            <div className="small">系统维修建议：{o.advice}</div>
            {o.damage_type && <div className="small">损坏类型：{DAMAGE_LABEL[o.damage_type]}（{o.severity}）</div>}
            {o.photos?.length > 0 && <div className="photos">{o.photos.map((p, i) => <img key={i} src={p} alt="" />)}</div>}
            <div className="row" style={{ marginTop: 8 }}>
              <Field label="实际维修成本（元）"><input type="number" step="0.01" defaultValue={o.repair_cost}
                onChange={e => { o.repair_cost = e.target.value }} /></Field>
              <Field label="维修说明/保养建议"><input defaultValue={o.advice} onChange={e => { o.advice = e.target.value }} /></Field>
            </div>
            <div className="btn-row" style={{ marginTop: 8 }}>
              {o.status === 'queued' && <button className="btn sm" onClick={() => update(o, 'in_progress')}>开始维修</button>}
              {o.status !== 'done' && o.status !== 'scrapped' && <>
                <button className="btn sm" onClick={() => update(o, 'waiting_part')}>等配件</button>
                <button className="btn sm" onClick={() => update(o, 'done')}>修复上架</button>
              </>}
              {o.status !== 'scrapped' && o.status !== 'done' && <button className="btn sm danger" onClick={() => update(o, 'scrapped')}>报废</button>}
              <span className="spacer" style={{ flex: 1 }} />
              {o.damage_id && <button className="btn sm ghost" onClick={() => setDetail(o)}>关联单据</button>}
            </div>
          </div>
        ))}
      </div>
      {detail && <RelatedRental order={detail} onClose={() => { setDetail(null); load() }} />}
    </div>
  )
}

// 从维修工单反查租赁单：借助 equipment-trace 找 current_rental_item_id 已被置空，这里展示损坏详情即可
function RelatedRental({ order, onClose }) {
  const toast = useToast()
  return (
    <Modal title={`工单 #${order.id} 关联信息`} onClose={onClose}>
      <p>损坏报告 #{order.damage_id}：{DAMAGE_LABEL[order.damage_type]}（{order.severity}）</p>
      <p className="small muted">{order.issue}</p>
      {order.photos?.length > 0 && <div className="photos">{order.photos.map((p, i) => <img key={i} src={p} alt="" />)}</div>}
      <div className="modal-foot"><button className="btn" onClick={onClose}>关闭</button></div>
    </Modal>
  )
}

// ---------- 高峰快速签收的维修间后检 ----------
function PostInspections() {
  const [list, setList] = useState(null)
  const [peak, setPeak] = useState(null)
  const [current, setCurrent] = useState(null)
  const toast = useToast()
  const load = useCallback(async () => {
    setList(await api('GET', '/repair/post-inspections'))
    setPeak(await api('GET', '/peak-board'))
  }, [])
  useEffect(() => { load() }, [])
  if (!list) return <Loading />
  const open = current && list.find(x => x.id === current)
  return (
    <div>
      <div className="btn-row">
        <h2 style={{ margin: 0, fontSize: 18 }}>快速签收归还后检</h2>
        <span className="spacer" style={{ flex: 1 }} />
        {peak && <Badge color={peak.repair_active >= peak.repair_capacity * 0.8 ? 'red' : 'blue'}>
          维修间容量 {peak.repair_active}/{peak.repair_capacity} · 待检雪具 {peak.pending_inspection_items} 件
        </Badge>}
      </div>
      <p className="muted small">游客赶班车柜台快速签收后，雪具在此逐件后检。发现损坏生成赔付通知，
        <b>游客确认或申诉期届满后</b>才从押金扣减；全部完好则立即释放押金。</p>
      {list.length === 0 && <Empty text="暂无待后检的快速签收单" />}
      {!open && <div className="grid grid-2">
        {list.map(q => (
          <div key={q.id} className="card clickable" onClick={() => setCurrent(q.id)}>
            <div className="btn-row"><b>{q.rental_code}</b><span className="spacer" style={{ flex: 1 }} />
              <Badge color="cyan">待后检 {q.items.filter(i => i.status === 'pending_inspection').length} 件</Badge></div>
            <div>{q.guest_name} · <span className="mono">{q.contact_phone}</span></div>
            <div className="small muted">{q.shuttle_note || '快速签收'} · 签收 {q.signed_at}</div>
            <div className="small muted">申诉期至 {q.appeal_deadline}</div>
          </div>
        ))}
      </div>}
      {open && <PostInspectDetail qs={open} onBack={() => { setCurrent(null); load() }} onChanged={load} />}
    </div>
  )
}

function PostInspectDetail({ qs, onBack, onChanged }) {
  const toast = useToast()
  const [target, setTarget] = useState(null)
  const [form, setForm] = useState({ result: 'ok', damage_type: 'edge_damage', severity: 'medium', description: '' })
  const pending = qs.items.filter(i => i.status === 'pending_inspection')

  const submitItem = async () => {
    try {
      const d = await api('POST', `/repair/post-inspections/${qs.id}/inspect`, {
        rental_item_id: target, ...form
      })
      toast(d.message); setTarget(null); onChanged()
    } catch (e) { toast(e.message, 'error') }
  }
  const complete = async () => {
    try {
      const d = await api('POST', `/repair/post-inspections/${qs.id}/complete`, {})
      toast(d.message); onBack()
    } catch (e) { toast(e.message, 'error') }
  }

  return (
    <div className="card">
      <div className="btn-row">
        <button className="btn sm ghost" onClick={onBack}>← 返回列表</button>
        <h3 style={{ margin: 0 }}>{qs.rental_code} · {qs.guest_name}</h3>
        <span className="spacer" style={{ flex: 1 }} />
        <Badge color="cyan">申诉期至 {qs.appeal_deadline}</Badge>
      </div>
      <div className="alert info small" style={{ margin: '8px 0' }}>
        联系方式 <span className="mono">{qs.contact_phone}</span>（{qs.shuttle_note || '游客赶班车快速签收'}）·
        后检发现的损坏将发送赔付通知给游客确认，不会直接扣押金。
      </div>
      <table>
        <thead><tr><th>类型</th><th>雪具</th><th>二维码</th><th>押金</th><th>状态</th><th></th></tr></thead>
        <tbody>
          {qs.items.map(i => (
            <tr key={i.id}>
              <td>{TYPE_LABEL[i.equipment_type]}</td><td>{i.model_label}</td>
              <td className="mono small">{i.qr_code}</td><td>{yuan(i.deposit)}</td>
              <td><Badge color={i.status === 'pending_inspection' ? 'orange' : i.status === 'returned_ok' ? 'green' : 'red'}>{
                { pending_inspection: '待后检', returned_ok: '✓ 完好', repair: '送修', returned_damaged: '损坏登记' }[i.status] || i.status
              }</Badge></td>
              <td>{i.status === 'pending_inspection' &&
                <button className="btn sm" onClick={() => { setTarget(i.id); setForm({ result: 'ok', damage_type: 'edge_damage', severity: 'medium', description: '' }) }}>后检</button>}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <div className="modal-foot">
        <span className="small muted">待后检 {pending.length} 件</span>
        <span className="spacer" style={{ flex: 1 }} />
        <button className="btn" disabled={pending.length > 0} onClick={complete}>
          {pending.length > 0 ? `还有 ${pending.length} 件未检` : '完成后检并通知游客'}
        </button>
      </div>

      {target && <Modal title="后检登记" onClose={() => setTarget(null)}>
        <Field label="后检结果">
          <div className="seg">
            <button className={form.result === 'ok' ? 'on' : ''} onClick={() => setForm({ ...form, result: 'ok' })}>✓ 完好上架</button>
            <button className={form.result === 'damaged' ? 'on' : ''} onClick={() => setForm({ ...form, result: 'damaged' })}>发现损坏</button>
          </div>
        </Field>
        {form.result === 'damaged' && <>
          <div className="row">
            <Field label="损坏类型">
              <select value={form.damage_type} onChange={e => setForm({ ...form, damage_type: e.target.value })}>
                {DAMAGE_TYPES.filter(([k]) => k !== 'lost').map(([k, v]) => <option key={k} value={k}>{v}</option>)}
              </select>
            </Field>
            <Field label="严重程度">
              <select value={form.severity} onChange={e => setForm({ ...form, severity: e.target.value })}>
                <option value="light">轻微（扣减×50%）</option>
                <option value="medium">中等（标准比例）</option>
                <option value="heavy">严重（扣减×150%）</option>
              </select>
            </Field>
          </div>
          <Field label="损坏描述"><textarea rows={2} value={form.description} onChange={e => setForm({ ...form, description: e.target.value })} /></Field>
          <p className="small muted">提交后自动开维修工单、有保险自动立案；扣减金额以后检完成时的赔付通知为准，需游客确认。</p>
        </>}
        <div className="modal-foot">
          <button className="btn ghost" onClick={() => setTarget(null)}>取消</button>
          <button className={form.result === 'ok' ? 'btn' : 'btn danger'} onClick={submitItem}>确认后检结果</button>
        </div>
      </Modal>}
    </div>
  )
}

function Maintenance() {
  const [list, setList] = useState(null)
  const toast = useToast()
  const load = useCallback(async () => setList(await api('GET', '/repair/maintenance')), [])
  useEffect(() => { load() }, [])
  const generate = async () => {
    try { toast((await api('POST', '/repair/maintenance/generate', {})).message); load() }
    catch (e) { toast(e.message, 'error') }
  }
  const resolve = async (id, action) => {
    try { await api('POST', `/repair/maintenance/${id}/resolve`, { action }); toast('提醒已处理'); load() }
    catch (e) { toast(e.message, 'error') }
  }
  if (!list) return <Loading />
  return (
    <div>
      <div className="btn-row">
        <h2 style={{ margin: 0, fontSize: 18 }}>高损耗型号保养提醒</h2>
        <span className="spacer" style={{ flex: 1 }} />
        <button className="btn" onClick={generate}>🔧 按近 30 天损耗数据生成提醒</button>
      </div>
      <p className="muted small">规则：近 30 天同型号损坏 ≥3 起，或损坏率 ≥20%，或发生丢失 → 生成整批保养/盘点提醒。</p>
      {list.length === 0 && <Empty text="暂无保养提醒，点击右上角按钮生成" />}
      <div className="grid grid-2">
        {list.map(m => (
          <div className="card" key={m.id} style={{ opacity: m.status === 'open' ? 1 : 0.6 }}>
            <div className="btn-row">
              <b>{m.brand} {m.name} {m.size_label}</b>
              <span className="spacer" style={{ flex: 1 }} />
              <Badge color={m.status === 'open' ? 'orange' : 'gray'}>{m.status === 'open' ? '待处理' : m.status === 'done' ? '已保养' : '已忽略'}</Badge>
            </div>
            <div className="alert warn" style={{ margin: '8px 0' }}>{m.reason}</div>
            <div className="small">建议行动：{m.action}</div>
            <div className="small muted" style={{ margin: '6px 0' }}>
              库存 {m.metrics.stock} · 损坏 {m.metrics.damages} · 丢失 {m.metrics.lost} ·
              出库 {m.metrics.rentals} · 损坏率 {m.metrics.damage_rate}% · 维修费 {yuan(m.metrics.repair_cost)}
            </div>
            <div className="small muted">生成于 {m.created_at}</div>
            {m.status === 'open' && <div className="btn-row" style={{ marginTop: 8 }}>
              <button className="btn sm" onClick={() => resolve(m.id, 'done')}>已完成保养</button>
              <button className="btn sm ghost" onClick={() => resolve(m.id, 'dismissed')}>忽略</button>
            </div>}
          </div>
        ))}
      </div>
    </div>
  )
}

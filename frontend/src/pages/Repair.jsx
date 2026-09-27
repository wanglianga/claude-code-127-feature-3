import React, { useEffect, useState, useCallback } from 'react'
import { api, yuan, TYPE_LABEL, DAMAGE_LABEL, DAMAGE_TYPES, uploadPhoto } from '../api.js'
import { Badge, Modal, Field, Loading, Empty, useToast } from '../components/ui.jsx'

export default function RepairPage({ page }) {
  if (page === 'maintenance') return <Maintenance />
  if (page === 'inspections') return <Inspections />
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

// ---------- 高峰后检：待检区 ----------
function Inspections() {
  const [list, setList] = useState(null)
  const [cap, setCap] = useState(null)
  const [target, setTarget] = useState(null)
  const toast = useToast()
  const load = useCallback(async () => {
    setList(await api('GET', '/repair/pending-inspections'))
    setCap(await api('GET', '/repair/capacity'))
  }, [])
  useEffect(() => { load(); const t = setInterval(load, 5000); return () => clearInterval(t) }, [])

  const resolve = async (item, body) => {
    try {
      const d = await api('POST', `/repair/inspections/${item.id}/resolve`, body)
      toast(d.message); setTarget(null); load()
    } catch (e) { toast(e.message, 'error') }
  }
  const setCapacity = async (patch) => {
    try { toast((await api('POST', '/repair/capacity', patch)).message); load() }
    catch (e) { toast(e.message, 'error') }
  }

  if (!list || !cap) return <Loading />
  const inspectPct = Math.min(100, Math.round(100 * list.length / Math.max(1, cap.inspect_capacity)))
  const repairPct = Math.min(100, Math.round(100 * cap.repair_used / Math.max(1, cap.repair_capacity)))
  return (
    <div>
      <div className="btn-row">
        <h2 style={{ margin: 0, fontSize: 18 }}>待检区 · 高峰后检</h2>
        <span className="spacer" style={{ flex: 1 }} />
        <span className="small muted">每 5 秒刷新 · 快签雪具后检发现问题先发赔付通知，不直接扣款</span>
      </div>
      <div className="capacity-bar">
        <div className={'cap-item' + (inspectPct >= 90 ? ' cap-full' : '')}>
          <span className="cap-k">🔍 待检区</span><b>{list.length}/{cap.inspect_capacity}</b>
          <div className="cap-track"><div className="cap-fill" style={{ width: inspectPct + '%' }} /></div>
        </div>
        <div className={'cap-item' + (repairPct >= 90 ? ' cap-full' : '')}>
          <span className="cap-k">🔧 维修工位</span><b>{cap.repair_used}/{cap.repair_capacity}</b>
          <div className="cap-track"><div className="cap-fill" style={{ width: repairPct + '%' }} /></div>
        </div>
        <div className="cap-item"><span className="cap-k">⚡ 快签待后检单</span><b>{cap.quick_sign_open}</b></div>
        <div className="cap-item"><span className="cap-k">📨 待确认通知</span><b>{cap.notify_pending}</b></div>
        <div className="cap-item"><span className="cap-k">申诉中</span><b>{cap.appeal_open}</b></div>
        <CapacityEditor cap={cap} onSave={setCapacity} />
      </div>

      {list.length === 0 ? <Empty text="待检区已清空，没有待后检雪具" /> : (
        <div className="grid grid-3">
          {list.map(i => (
            <div className="card" key={i.id}>
              <div className="btn-row"><b>{i.brand} {i.name} {i.size_label}</b>
                <span className="spacer" style={{ flex: 1 }} />
                {i.from_quick_sign ? <Badge color="cyan">快签件</Badge> : <Badge color="gray">换码回收</Badge>}
              </div>
              <div className="small muted">{TYPE_LABEL[i.equipment_type]} · <span className="mono">{i.qr_code}</span></div>
              {i.rental_code ? <>
                <div className="small">来源：{i.rental_code} · {i.guest_name} {i.guest_phone}</div>
                <div className="small muted">快签时间 {i.quick_signed_at || '—'}</div>
              </> : <div className="small muted">无关联租赁单（换码回收检查）</div>}
              <div className="btn-row" style={{ marginTop: 8 }}>
                <button className="btn sm" onClick={() => resolve(i, { result: 'ok' })}>✓ 后检合格上架</button>
                <button className="btn sm danger" onClick={() => setTarget(i)}>登记后检损坏</button>
              </div>
            </div>
          ))}
        </div>
      )}

      {target && <PostInspectModal item={target} onCancel={() => setTarget(null)} onSubmit={resolve} />}
    </div>
  )
}

function CapacityEditor({ cap, onSave }) {
  const [open, setOpen] = useState(false)
  const [ic, setIc] = useState(cap.inspect_capacity)
  const [rc, setRc] = useState(cap.repair_capacity)
  if (!open) return <button className="btn sm ghost" onClick={() => setOpen(true)}>⚙️ 设置容量</button>
  return (
    <div className="cap-item">
      <span className="cap-k">待检区/工位容量</span>
      <div className="inline">
        <input type="number" min="1" value={ic} onChange={e => setIc(e.target.value)} style={{ width: 64 }} />
        <input type="number" min="1" value={rc} onChange={e => setRc(e.target.value)} style={{ width: 64 }} />
        <button className="btn sm" onClick={() => { onSave({ inspect_capacity: Number(ic), repair_capacity: Number(rc) }); setOpen(false) }}>保存</button>
      </div>
    </div>
  )
}

function PostInspectModal({ item, onCancel, onSubmit }) {
  const [f, setF] = useState({ damage_type: 'edge_damage', severity: 'medium', description: '', photos: [] })
  const toast = useToast()
  const onFile = async (file) => {
    try { const url = await uploadPhoto(file); setF(s => ({ ...s, photos: [...s.photos, url] })); toast('照片已上传') }
    catch (e) { toast(e.message, 'error') }
  }
  return (
    <Modal title={`后检损坏登记 · ${item.qr_code}`} onClose={onCancel}>
      <div className="alert info small">
        {item.from_quick_sign
          ? '快签件：提交后自动开维修工单、有保险自动立案，并向游客发送赔付通知；押金在游客确认/申诉期结束后才扣减释放。'
          : '换码回收件：仅开维修工单，不涉及押金通知。'}
      </div>
      <div className="row">
        <Field label="损坏类型">
          <select value={f.damage_type} onChange={e => setF({ ...f, damage_type: e.target.value })}>
            {DAMAGE_TYPES.filter(([k]) => k !== 'lost' && k !== 'other').map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </select>
        </Field>
        <Field label="严重程度">
          <select value={f.severity} onChange={e => setF({ ...f, severity: e.target.value })}>
            <option value="light">轻微（×50%）</option>
            <option value="medium">中等（标准比例）</option>
            <option value="heavy">严重（×150%）</option>
          </select>
        </Field>
      </div>
      <Field label="后检描述"><textarea rows={2} value={f.description} onChange={e => setF({ ...f, description: e.target.value })}
        placeholder="后检发现的问题与位置" /></Field>
      <Field label="取证照片"><input type="file" accept="image/*" onChange={e => e.target.files[0] && onFile(e.target.files[0])} /></Field>
      {f.photos.length > 0 && <div className="photos">{f.photos.map((p, i) => <img key={i} src={p} alt="" />)}</div>}
      <div className="modal-foot">
        <button className="btn ghost" onClick={onCancel}>取消</button>
        <button className="btn danger" disabled={!f.description}
          onClick={() => onSubmit(item, { result: 'damaged', ...f })}>提交后检结果并通知游客</button>
      </div>
    </Modal>
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

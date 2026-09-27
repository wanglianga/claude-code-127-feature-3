import React, { useEffect, useState, useCallback } from 'react'
import { api, yuan } from '../api.js'
import { Badge, Loading } from '../components/ui.jsx'

export default function FinancePage({ page }) {
  if (page === 'ledger') return <Ledger />
  return <Summary />
}

function Summary() {
  const [s, setS] = useState(null)
  useEffect(() => { api('GET', '/finance/summary').then(setS) }, [])
  if (!s) return <Loading />
  const cards = [
    ['日/夜场租金收入（已归还单）', s.rental_fee_income, 'green'],
    ['保险费收入', s.premium_income, 'green'],
    ['在租冻结押金', s.deposit_held_open, 'blue'],
    ['其中：快签待后检释放', s.quick_sign_held, 'cyan'],
    ['损坏扣减累计', s.damage_deducted, 'orange'],
    ['丢失扣减累计', s.lost_deducted, 'red'],
    ['超时扣减累计', s.late_deducted],
    ['保险冲抵返还', s.insurance_offsets, 'purple'],
    ['押金退还累计', s.deposit_refunded, 'gray'],
    ['维修支出', s.repair_cost, 'orange'],
  ]
  return (
    <div>
      <h2 style={{ fontSize: 18 }}>财务总览</h2>
      <p className="muted small">押金「冻结 → 扣减/保险冲抵 → 退还」全流程留痕，进行中租赁单 {s.open_rentals} 单
        （含快签待后检 {s.quick_sign_open} 单）。</p>
      {(s.notify_pending > 0 || s.appeal_open > 0) && (
        <div className="alert warn" style={{ marginBottom: 12 }}>
          快速签收后检：待游客确认赔付通知 <b>{s.notify_pending}</b> 条，申诉复核中 <b>{s.appeal_open}</b> 条；
          对应押金在游客确认/申诉期结束后才扣减释放。
        </div>
      )}
      <div className="grid grid-3">
        {cards.map(([k, v, color]) => (
          <div className="stat" key={k}>
            <div className="k">{k}</div>
            <div className="v" style={{ color: color ? `var(--${color === 'gray' ? 'muted' : color})` : undefined }}>{yuan(v)}</div>
          </div>
        ))}
      </div>
      <div className="card" style={{ marginTop: 16 }}>
        <h3>押金口径说明</h3>
        <ul className="small muted" style={{ margin: 0, paddingLeft: 18 }}>
          <li>损坏/丢失/超时扣减按每笔流水记账；保险赔付到账后以「保险冲抵返还」退回对应押金。</li>
          <li>在租冻结押金属于负债，不计入收入；归还结算时按实际扣减清算。</li>
          <li>高峰快速签收：雪具先转维修间待检，押金保持冻结并计入「快签待后检释放」；
            后检赔付通知经游客确认或申诉期结束后才记扣减并释放押金。</li>
          <li>维修支出独立核算，可到「维修工单」查看每件成本，支撑型号采购决策。</li>
        </ul>
      </div>
    </div>
  )
}

function Ledger() {
  const [list, setList] = useState(null)
  useEffect(() => { api('GET', '/finance/ledger').then(setList) }, [])
  if (!list) return <Loading />
  const color = { hold: 'blue', deduct_damage: 'orange', deduct_lost: 'red', deduct_late: 'orange', insurance_offset: 'purple', refund: 'gray' }
  return (
    <div>
      <h2 style={{ fontSize: 18 }}>押金流水台账</h2>
      <div className="card" style={{ padding: 0 }}>
        <div className="table-wrap"><table>
          <thead><tr><th>时间</th><th>租赁单</th><th>游客</th><th>类型</th><th>金额</th><th>说明</th></tr></thead>
          <tbody>
            {list.map(t => <tr key={t.id}>
              <td className="small nowrap">{t.created_at}</td>
              <td className="mono small">{t.rental_code}</td>
              <td>{t.guest_name}</td>
              <td><Badge color={color[t.type]}>{t.type_label}</Badge></td>
              <td className="nowrap"><b>{yuan(t.amount)}</b></td>
              <td className="small muted">{t.note}</td>
            </tr>)}
          </tbody>
        </table></div>
      </div>
    </div>
  )
}

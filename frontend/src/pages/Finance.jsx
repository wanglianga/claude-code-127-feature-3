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
    ['快速签收暂扣押金', s.quick_sign_hold, 'cyan'],
    ['待游客确认赔付', s.notify_pending_amount, 'orange'],
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
      <p className="muted small">押金「冻结 → 扣减/保险冲抵 → 退还」全流程留痕，进行中租赁单 {s.open_rentals} 单；
        快速签收待后检 {s.quick_sign_pending} 单（押金暂扣至申诉期结束），赔付通知申诉中 {s.appealing_count} 条。</p>
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

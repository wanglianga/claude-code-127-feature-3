import React, { useState } from 'react'
import { api, setToken, getToken } from './api.js'
import { ToastProvider } from './components/ui.jsx'
import GuestPage from './pages/Guest.jsx'
import CounterPage from './pages/Counter.jsx'
import CoachPage from './pages/Coach.jsx'
import RepairPage from './pages/Repair.jsx'
import InsurancePage from './pages/Insurance.jsx'
import FinancePage from './pages/Finance.jsx'
import AdminPage from './pages/Admin.jsx'

const ROLE_HOME = {
  guest: 'rentals', counter: 'queue', coach: 'courses', repair: 'orders',
  insurance: 'claims', finance: 'summary', admin: 'analytics'
}

export default function App() {
  const [user, setUser] = useState(() => {
    const raw = localStorage.getItem('user')
    return raw ? JSON.parse(raw) : null
  })
  const [page, setPage] = useState(() => (localStorage.getItem('user') ? ROLE_HOME[JSON.parse(localStorage.getItem('user')).role] : 'login'))
  const [refreshKey, setRefreshKey] = useState(0)

  const onLogin = (u, token) => {
    setToken(token)
    localStorage.setItem('user', JSON.stringify(u))
    setUser(u)
    setPage(ROLE_HOME[u.role])
  }
  const logout = () => {
    setToken('')
    localStorage.removeItem('user')
    setUser(null)
    setPage('login')
  }
  const go = (p) => { setPage(p); setRefreshKey(k => k + 1) }

  if (!user || !getToken()) {
    return (
      <ToastProvider>
        <LoginPage onLogin={onLogin} />
      </ToastProvider>
    )
  }

  const nav = NAV[user.role] || []
  return (
    <ToastProvider>
      <div className="topbar">
        <span className="logo">🎿 雪具租赁调换与赔付平台</span>
        <span className="spacer" />
        <span className="who">{user.name} · {ROLE_LABEL[user.role]}</span>
        <button onClick={logout}>退出登录</button>
      </div>
      <div className="layout">
        <nav className="sidebar">
          {nav.map(([key, label]) => (
            <button key={key} className={page === key ? 'active' : ''} onClick={() => go(key)}>{label}</button>
          ))}
        </nav>
        <main className="content" key={refreshKey}>
          {user.role === 'guest' && <GuestPage page={page} go={go} />}
          {user.role === 'counter' && <CounterPage page={page} go={go} />}
          {user.role === 'coach' && <CoachPage page={page} go={go} />}
          {user.role === 'repair' && <RepairPage page={page} go={go} />}
          {user.role === 'insurance' && <InsurancePage page={page} go={go} />}
          {user.role === 'finance' && <FinancePage page={page} go={go} />}
          {user.role === 'admin' && <AdminPage page={page} go={go} />}
        </main>
      </div>
    </ToastProvider>
  )
}

const ROLE_LABEL = {
  guest: '游客', coach: '教练', counter: '柜台', repair: '维修间',
  insurance: '保险', finance: '财务', admin: '管理员'
}

const NAV = {
  guest: [['rentals', '我的租赁'], ['new', '新建租赁预约'], ['suggest', '尺码建议']],
  counter: [['queue', '排队叫号大屏'], ['checkout', '扫码出库'], ['exchanges', '换码处理'], ['returns', '归还验损'], ['appeals', '赔付申诉复核']],
  coach: [['courses', '我的课程与课前确认']],
  repair: [['orders', '维修工单'], ['postinspection', '归还后检'], ['maintenance', '高损耗保养提醒']],
  insurance: [['claims', '理赔处理'], ['policies', '保单查询']],
  finance: [['summary', '财务总览'], ['ledger', '押金流水台账']],
  admin: [['analytics', '运营分析'], ['trace', '雪具全程追踪'], ['catalog', '雪具型号/入库']]
}

const DEMO_ACCOUNTS = [
  ['13900000001', '游客 张明（初学者）'],
  ['13800000003', '柜台 孙柜台'],
  ['13800000002', '教练 陈教练'],
  ['13800000004', '维修间 周师傅'],
  ['13800000005', '保险 吴理赔'],
  ['13800000006', '财务 郑财务'],
  ['13800000001', '管理员 赵店长']
]

function LoginPage({ onLogin }) {
  const [phone, setPhone] = useState('13900000001')
  const [password, setPassword] = useState('ski123456')
  const [err, setErr] = useState('')
  const submit = async (e) => {
    e?.preventDefault()
    setErr('')
    try {
      const d = await api('POST', '/auth/login', { phone, password })
      onLogin(d.user, d.token)
    } catch (e) { setErr(e.message) }
  }
  return (
    <div className="login-wrap">
      <form className="login-card" onSubmit={submit}>
        <h1>🎿 滑雪场雪具租赁平台</h1>
        <p className="sub">尺码调换 · 损坏赔付 · 维修保险财务协同</p>
        {err && <div className="alert red">{err}</div>}
        <label>手机号</label>
        <input value={phone} onChange={e => setPhone(e.target.value)} placeholder="手机号" />
        <label>密码</label>
        <input type="password" value={password} onChange={e => setPassword(e.target.value)} placeholder="密码" />
        <button className="btn" style={{ width: '100%', marginTop: 18 }} type="submit">登 录</button>
        <div className="accounts">
          <p className="small muted" style={{ margin: '0 0 6px' }}>演示账号（密码均为 ski123456），点击快速填充：</p>
          {DEMO_ACCOUNTS.map(([p, n]) => (
            <div key={p + n} className="acc" onClick={() => setPhone(p)}>
              <span>{n}</span><span className="phone">{p}</span>
            </div>
          ))}
        </div>
      </form>
    </div>
  )
}

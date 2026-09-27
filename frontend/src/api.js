// 后端接口封装：自动带 token、统一错误处理
let token = localStorage.getItem('token') || ''
export const getToken = () => token
export const setToken = (t) => {
  token = t || ''
  if (t) localStorage.setItem('token', t)
  else localStorage.removeItem('token')
}

export async function api(method, path, body) {
  const opts = { method, headers: { 'Content-Type': 'application/json' } }
  if (token) opts.headers.Authorization = 'Bearer ' + token
  if (body !== undefined) opts.body = JSON.stringify(body)
  const res = await fetch('/api' + path, opts)
  const text = await res.text()
  const data = text ? JSON.parse(text) : {}
  if (!res.ok) throw new Error(data.error || ('请求失败 ' + res.status))
  return data
}

export async function uploadPhoto(file) {
  const fd = new FormData()
  fd.append('photo', file)
  const res = await fetch('/api/uploads', {
    method: 'POST',
    headers: { Authorization: 'Bearer ' + token },
    body: fd
  })
  const data = await res.json()
  if (!res.ok) throw new Error(data.error || '上传失败')
  return data.url
}

export const yuan = (v) => '¥' + (Number(v) || 0).toFixed(2)

export const TYPE_LABEL = {
  ski: '雪板', boot: '雪鞋', helmet: '头盔', pole: '雪杖', protector: '护具'
}

export const RENTAL_STATUS = {
  draft: ['草稿', 'gray'], reserved: ['已预约待取', 'blue'], equipped: ['使用中', 'green'],
  return_pending: ['归还验损中', 'orange'], quick_signed: ['已快签·待后检', 'cyan'],
  returned: ['已归还', 'gray'], cancelled: ['已取消', 'gray']
}

export const ITEM_STATUS = {
  suggested: '建议', assigned: '已预留', out: '使用中', exchanged: '已调换',
  returned_ok: '完好归还', returned_damaged: '损坏归还', repair: '维修中', lost: '丢失',
  pending_inspection: '待后检'
}

export const NOTIF_STATUS = {
  pending: '待确认', appealed: '申诉复核中', confirmed: '已确认扣减', cancelled: '申诉成立·已取消'
}

export const DAMAGE_LABEL = {
  edge_damage: '板刃损伤', base_scratch: '板底划伤', binding_issue: '固定器故障',
  liner_wet: '雪鞋内衬潮湿', shell_crack: '护具裂纹', strap_broken: '绑带断裂',
  lost: '雪具丢失', other: '其他异常'
}

export const DAMAGE_TYPES = Object.entries(DAMAGE_LABEL)

export const CLAIM_STATUS = {
  filed: '已立案', under_review: '审核中', approved: '已核定待赔付', rejected: '已拒赔', paid: '已赔付'
}

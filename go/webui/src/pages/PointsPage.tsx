import { Check, ChevronLeft, ChevronRight, Coins, History, Link2, RefreshCw, Search, ShieldCheck, Undo2 } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { Button, InfoDialog, StatusDot } from '../components/ui';
import { apiRequest } from '../lib/api';

type Identity = { transport: string; transportInstance: string; senderRef: string };
type Ownership = Identity & { affiliateCode: string; status: string; boundAt: string; accountId: string; verifiedAt: string; evidence: string };
type Account = { id: string; balance: number; users: string; identityCount: number };
type Entry = Identity & { id: string; type: string; points: number; referenceKey: string; note: string; createdAt: string };
type AccountDetail = { id: string; balance: number; identities: Identity[]; entries: Entry[]; hasMore: boolean };
type Order = { id: string; accountId: string; source: string; externalRef: string; kind: string; points: number; status: string; note: string; resolutionNote: string; createdAt: string };
type Page<T> = { items: T[]; hasMore: boolean };
type Tab = 'ownership' | 'accounts' | 'orders';
type Action = { kind: 'verify'; item: Ownership } | { kind: 'merge'; item: AccountDetail } | { kind: 'link'; item: AccountDetail } | { kind: 'resolve'; item: Order; status: 'committed' | 'refunded' };

const pageSize = 25;
const formatPoints = (value: number) => new Intl.NumberFormat('zh-CN').format(value);
const formatDate = (value: string) => value && !Number.isNaN(Date.parse(value)) ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : value || '-';
const accountLabel = (id: string) => `账户 ${id.slice(-10)}`;
const labels: Record<string, string> = { pending: '待核验', verified: '已核验', conflict: '归属冲突', reserved: '待确认', committed: '已完成', refunded: '已退回', check_in: '签到', adjustment: '奖励 / 调整', redemption: '兑换', lottery: '抽奖' };

function Status({ value }: { value: string }) {
  return <span className="module-status"><StatusDot tone={value === 'verified' || value === 'committed' ? 'ok' : value === 'conflict' ? 'bad' : 'warn'} />{labels[value] || value}</span>;
}

function Pager({ offset, more, disabled, onChange }: { offset: number; more: boolean; disabled: boolean; onChange: (value: number) => void }) {
  return <div className="module-table-pager"><span>第 {Math.floor(offset / pageSize) + 1} 页</span><div>
    <Button icon={<ChevronLeft size={16} />} title="上一页" aria-label="上一页" disabled={disabled || offset === 0} onClick={() => onChange(Math.max(0, offset - pageSize))} />
    <Button icon={<ChevronRight size={16} />} title="下一页" aria-label="下一页" disabled={disabled || !more} onClick={() => onChange(offset + pageSize)} />
  </div></div>;
}

function PointsTable({ columns, children, empty }: { columns: string[]; children: ReactNode; empty: boolean }) {
  return <div className="module-table-wrap"><table className="module-table points-table"><thead><tr>{columns.map((label) => <th key={label} scope="col">{label}</th>)}</tr></thead>
    <tbody>{empty ? <tr><td colSpan={columns.length}><div className="module-empty">暂无记录</div></td></tr> : children}</tbody>
  </table></div>;
}

export function PointsPage({ refreshKey = 0 }: { refreshKey?: number }) {
  const [tab, setTab] = useState<Tab>('ownership');
  const [status, setStatus] = useState('pending');
  const [query, setQuery] = useState('');
  const [search, setSearch] = useState('');
  const [offset, setOffset] = useState(0);
  const [revision, setRevision] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [owners, setOwners] = useState<Ownership[]>([]);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [orders, setOrders] = useState<Order[]>([]);
  const [more, setMore] = useState(false);
  const [selectedAccount, setSelectedAccount] = useState('');
  const [detail, setDetail] = useState<AccountDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState('');
  const [ledgerOffset, setLedgerOffset] = useState(0);
  const [action, setAction] = useState<Action | null>(null);
  const detailRef = useRef<HTMLElement>(null);

  useEffect(() => {
    if (selectedAccount) detailRef.current?.scrollIntoView({ block: 'start', behavior: 'smooth' });
  }, [selectedAccount, ledgerOffset]);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setError(''); setMore(false);
    const params = new URLSearchParams({ limit: String(tab === 'ownership' ? pageSize + 1 : pageSize), offset: String(offset) });
    if (status) params.set('status', status);
    if (search) params.set(tab === 'ownership' ? 'senderRef' : tab === 'orders' ? 'accountId' : 'q', search);
    const request = tab === 'ownership'
      ? apiRequest<Ownership[]>(`/api/v1/affiliate/ownership?${params}`, { signal: controller.signal }).then((items) => {
        if (!controller.signal.aborted) { setOwners(items.slice(0, pageSize)); setMore(items.length > pageSize); }
      })
      : tab === 'accounts'
        ? apiRequest<Page<Account>>(`/api/v1/points/accounts?${params}`, { signal: controller.signal }).then((page) => { if (!controller.signal.aborted) { setAccounts(page.items); setMore(page.hasMore); } })
        : apiRequest<Page<Order>>(`/api/v1/points/orders?${params}`, { signal: controller.signal }).then((page) => { if (!controller.signal.aborted) { setOrders(page.items); setMore(page.hasMore); } });
    request.catch((cause) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : '加载失败'); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [tab, status, search, offset, revision, refreshKey]);

  useEffect(() => {
    if (!selectedAccount) { setDetail(null); return; }
    const controller = new AbortController();
    setDetailLoading(true); setDetailError('');
    apiRequest<AccountDetail>(`/api/v1/points/accounts/${encodeURIComponent(selectedAccount)}?limit=${pageSize}&offset=${ledgerOffset}`, { signal: controller.signal })
      .then((value) => { if (!controller.signal.aborted) setDetail(value); })
      .catch((cause) => { if (!controller.signal.aborted) { setDetail(null); setDetailError(cause instanceof Error ? cause.message : '读取账户失败'); } })
      .finally(() => { if (!controller.signal.aborted) setDetailLoading(false); });
    return () => controller.abort();
  }, [selectedAccount, ledgerOffset, revision, refreshKey]);

  function changeTab(next: Tab) {
    setTab(next); setStatus(next === 'ownership' ? 'pending' : ''); setOffset(0); setQuery(''); setSearch('');
    setSelectedAccount(''); setDetail(null); setNotice('');
  }

  function showAccount(id: string) {
    setSelectedAccount(id); setLedgerOffset(0);
    if (selectedAccount === id) detailRef.current?.scrollIntoView({ block: 'start', behavior: 'smooth' });
  }

  return <div className="points-page">
    <header className="points-heading"><div><h1><Coins size={23} />积分与活动</h1><span className="points-muted">兑换奖品未上架</span></div>
      <Button icon={<RefreshCw size={16} />} title="刷新积分数据" aria-label="刷新积分数据" disabled={loading} onClick={() => setRevision((n) => n + 1)} />
    </header>
    <div className="module-tabs" role="tablist" aria-label="积分管理">
      {([['ownership', '归属核验'], ['accounts', '积分账户'], ['orders', '交易订单']] as const).map(([id, label]) => <button type="button" role="tab" aria-selected={tab === id} className="module-tab" data-state={tab === id ? 'active' : 'inactive'} onClick={() => changeTab(id)} key={id}>{label}</button>)}
    </div>
    <form className="points-filters" onSubmit={(event) => { event.preventDefault(); setSearch(query.trim()); setOffset(0); }}>
      {tab !== 'accounts' ? <label><span>状态</span><select aria-label="状态筛选" value={status} onChange={(event) => { setStatus(event.target.value); setOffset(0); }}>
        <option value="">全部</option>{(tab === 'ownership' ? ['pending', 'verified', 'conflict'] : ['reserved', 'committed', 'refunded']).map((value) => <option value={value} key={value}>{labels[value]}</option>)}
      </select></label> : null}
      <label className="points-search"><span>{tab === 'ownership' ? '用户号码' : tab === 'orders' ? '积分账户 ID' : '用户 / 实例 / 账户'}</span><input aria-label="查询条件" maxLength={160} value={query} onChange={(event) => setQuery(event.target.value)} /></label>
      <Button type="submit" icon={<Search size={16} />}>查询</Button>
    </form>
    {notice ? <p className="points-notice" role="status">{notice}</p> : null}
    {error ? <div className="points-error" role="alert">{error}<Button icon={<RefreshCw size={15} />} onClick={() => setRevision((n) => n + 1)}>重试</Button></div> : null}
    <section className="points-section" aria-busy={loading}>
      {loading ? <div className="module-empty" role="status">正在读取积分记录</div> : !error ? <>
        {tab === 'ownership' ? <PointsTable columns={['用户', '邀请码', '状态', '时间', '操作']} empty={!owners.length}>
          {owners.map((item) => <tr key={`${item.transport}/${item.transportInstance}/${item.senderRef}`}>
            <td><strong>{item.senderRef}</strong><small>{item.transport} / {item.transportInstance}</small></td><td><code>{item.affiliateCode}</code></td>
            <td><Status value={item.status} />{item.evidence ? <small>{item.evidence}</small> : null}</td><td>{formatDate(item.verifiedAt || item.boundAt)}</td>
            <td><div className="points-actions">{item.status === 'pending' ? <Button icon={<ShieldCheck size={15} />} onClick={() => setAction({ kind: 'verify', item })}>核验</Button> : null}
              {item.accountId ? <Button icon={<History size={15} />} title="查看积分明细" aria-label={`查看 ${item.senderRef} 的积分明细`} onClick={() => showAccount(item.accountId)} /> : null}</div></td>
          </tr>)}
        </PointsTable> : tab === 'accounts' ? <PointsTable columns={['用户', '账户', '可用积分', '关联身份', '操作']} empty={!accounts.length}>
          {accounts.map((item) => <tr key={item.id}><td>{item.users || '-'}</td><td><code title={item.id}>{accountLabel(item.id)}</code></td><td className="points-number">{formatPoints(item.balance)}</td><td>{item.identityCount}</td>
            <td><Button icon={<History size={15} />} onClick={() => showAccount(item.id)}>明细</Button></td></tr>)}
        </PointsTable> : <PointsTable columns={['业务订单', '账户', '积分', '状态', '创建时间', '操作']} empty={!orders.length}>
          {orders.map((item) => <tr key={item.id}><td><strong>{item.source} / {item.externalRef}</strong><small>{labels[item.kind]} · {item.note}</small></td><td><button className="points-text-button" onClick={() => showAccount(item.accountId)}>{accountLabel(item.accountId)}</button></td>
            <td className="points-number">{formatPoints(item.points)}</td><td><Status value={item.status} />{item.resolutionNote ? <small>{item.resolutionNote}</small> : null}</td><td>{formatDate(item.createdAt)}</td>
            <td>{item.status === 'reserved' ? <div className="points-actions"><Button icon={<Check size={15} />} title="确认已发放" aria-label="确认已发放" onClick={() => setAction({ kind: 'resolve', item, status: 'committed' })} />
              <Button icon={<Undo2 size={15} />} title="未发放退分" aria-label="未发放退分" onClick={() => setAction({ kind: 'resolve', item, status: 'refunded' })} /></div> : '-'}</td></tr>)}
        </PointsTable>}
      </> : null}
      <Pager offset={offset} more={more} disabled={loading || !!error} onChange={setOffset} />
    </section>
    {selectedAccount ? <section ref={detailRef} className="points-detail" aria-label="积分明细" aria-busy={detailLoading}>
      <div className="points-detail-heading"><h2>积分明细</h2><Button variant="ghost" onClick={() => setSelectedAccount('')}>收起</Button></div>
      {detailError ? <p className="points-error" role="alert">{detailError}</p> : detailLoading ? <div className="module-empty" role="status">正在读取账户</div> : detail ? <>
        <div className="points-account-summary"><div><span>可用积分</span><strong>{formatPoints(detail.balance)}</strong></div><code>{detail.id}</code>
          <Button icon={<Link2 size={15} />} onClick={() => setAction({ kind: 'link', item: detail })}>关联站点身份</Button>
          <Button icon={<Link2 size={15} />} onClick={() => setAction({ kind: 'merge', item: detail })}>合并同一用户账户</Button></div>
        <div className="points-identities">{detail.identities.map((item) => <span key={`${item.transport}/${item.transportInstance}/${item.senderRef}`}>{item.senderRef}<small>{item.transport} / {item.transportInstance}</small></span>)}</div>
        <PointsTable columns={['时间', '类型', '变动', '说明', '来源身份']} empty={!detail.entries.length}>
          {detail.entries.map((item) => <tr key={item.id}><td>{formatDate(item.createdAt)}</td><td>{labels[item.type] || item.type}</td><td className={`points-number ${item.points > 0 ? 'points-credit' : 'points-debit'}`}>{item.points > 0 ? '+' : ''}{formatPoints(item.points)}</td>
            <td>{item.note}<small><code>{item.referenceKey}</code></small></td><td>{item.senderRef}<small>{item.transportInstance}</small></td></tr>)}
        </PointsTable><Pager offset={ledgerOffset} more={detail.hasMore} disabled={detailLoading} onChange={setLedgerOffset} />
      </> : null}
    </section> : null}
    {action ? <PointsAction key={`${action.kind}/${'id' in action.item ? action.item.id : action.item.senderRef}`} action={action} onClose={() => setAction(null)} onSaved={(message, accountID) => {
      setAction(null); setNotice(message); setRevision((n) => n + 1); if (accountID) showAccount(accountID);
    }} /> : null}
  </div>;
}

function PointsAction({ action, onClose, onSaved }: { action: Action; onClose: () => void; onSaved: (message: string, accountID?: string) => void }) {
  const [evidence, setEvidence] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [targetQuery, setTargetQuery] = useState('');
  const [targets, setTargets] = useState<Account[]>([]);
  const [target, setTarget] = useState('');
  const [targetsLoading, setTargetsLoading] = useState(false);
  const [targetsError, setTargetsError] = useState('');
  const [site, setSite] = useState('sub2api');
  const [instance, setInstance] = useState('');
  const [userID, setUserID] = useState('');
  const title = action.kind === 'verify' ? '核验邀请归属' : action.kind === 'merge' ? '合并积分账户' : action.kind === 'link' ? '关联站点身份' : action.status === 'committed' ? '确认已发放' : '未发放退分';

  useEffect(() => {
    if (action.kind !== 'merge') return;
    const controller = new AbortController();
    setTargetsLoading(true); setTargetsError(''); setTarget('');
    apiRequest<Page<Account>>(`/api/v1/points/accounts?limit=100&q=${encodeURIComponent(targetQuery.trim())}`, { signal: controller.signal })
      .then((page) => { if (!controller.signal.aborted) setTargets(page.items.filter((item) => item.id !== action.item.id)); })
      .catch((cause) => { if (!controller.signal.aborted) { setTargets([]); setTargetsError(cause instanceof Error ? cause.message : '加载目标账户失败'); } })
      .finally(() => { if (!controller.signal.aborted) setTargetsLoading(false); });
    return () => controller.abort();
  }, [action, targetQuery]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!confirmed || !evidence.trim()) return;
    setSaving(true); setError('');
    try {
      if (action.kind === 'verify') {
        const { transport, transportInstance, senderRef, affiliateCode } = action.item;
        await apiRequest('/api/v1/affiliate/ownership', { method: 'POST', body: JSON.stringify({ transport, transportInstance, senderRef, affiliateCode, ownershipConfirmed: true, evidence: evidence.trim() }) });
        onSaved('邀请归属已核验。');
      } else if (action.kind === 'merge') {
        const result = await apiRequest<{ accountId: string }>(`/api/v1/points/accounts/${encodeURIComponent(action.item.id)}/merge`, { method: 'POST', body: JSON.stringify({ targetAccountId: target, identityConfirmed: true, evidence: evidence.trim() }) });
        onSaved('账户已合并，原积分流水已保留。', result.accountId);
      } else if (action.kind === 'link') {
        const result = await apiRequest<{ accountId: string }>(`/api/v1/points/accounts/${encodeURIComponent(action.item.id)}/identities`, { method: 'POST', body: JSON.stringify({ transport: site, transportInstance: instance.trim(), senderRef: userID.trim(), identityConfirmed: true, evidence: evidence.trim() }) });
        onSaved('站点身份已关联。', result.accountId);
      } else {
        await apiRequest(`/api/v1/points/orders/${encodeURIComponent(action.item.id)}/resolve`, { method: 'POST', body: JSON.stringify({ status: action.status, note: evidence.trim() }) });
        onSaved(action.status === 'refunded' ? '未发放订单积分已退回。' : '交易已确认完成。');
      }
    } catch (cause) { setError(cause instanceof Error ? cause.message : '操作失败'); }
    finally { setSaving(false); }
  }

  return <InfoDialog open onOpenChange={(open) => { if (!open && !saving) onClose(); }} title={title}>
    <form className="points-action-form" onSubmit={submit}>
      {action.kind === 'verify' ? <dl><dt>QQ 身份</dt><dd>{action.item.senderRef} / {action.item.transportInstance}</dd><dt>邀请码</dt><dd>{action.item.affiliateCode}</dd></dl>
        : action.kind === 'merge' ? <>
          <dl><dt>原账户</dt><dd><code>{action.item.id}</code></dd><dt>原可用积分</dt><dd>{formatPoints(action.item.balance)}</dd></dl>
          <label><span>目标账户查询</span><input value={targetQuery} onChange={(event) => setTargetQuery(event.target.value)} maxLength={160} /></label>
          <label><span>合并到</span><select required aria-label="合并到目标账户" value={target} disabled={targetsLoading || saving} onChange={(event) => setTarget(event.target.value)}>
            <option value="">{targetsLoading ? '正在读取账户' : '选择目标账户'}</option>{targets.map((item) => <option value={item.id} key={item.id}>{item.users} · {formatPoints(item.balance)} 积分 · {item.id.slice(-10)}</option>)}
          </select></label>
          {targetsError ? <p role="alert" className="points-error">{targetsError}</p> : null}
          {target ? <p className="points-merge-total">合并后可用积分：{formatPoints(action.item.balance + (targets.find((item) => item.id === target)?.balance || 0))}</p> : null}
        </> : action.kind === 'link' ? <>
          <dl><dt>积分账户</dt><dd><code>{action.item.id}</code></dd></dl>
          <label><span>站点类型</span><select value={site} onChange={(event) => setSite(event.target.value)}><option value="sub2api">Sub2API</option><option value="newapi">NewAPI</option></select></label>
          <label><span>站点实例（来源地址）</span><input required maxLength={200} value={instance} onChange={(event) => setInstance(event.target.value)} /></label>
          <label><span>站点用户 ID</span><input required maxLength={128} value={userID} onChange={(event) => setUserID(event.target.value)} /></label>
        </> : <dl><dt>业务订单</dt><dd>{action.item.source} / {action.item.externalRef}</dd><dt>积分</dt><dd>{formatPoints(action.item.points)}</dd><dt>当前状态</dt><dd>待确认</dd></dl>}
      <label><span>{action.kind === 'resolve' ? '处理凭据' : '身份核验说明'}</span><textarea required maxLength={1000} rows={4} value={evidence} onChange={(event) => setEvidence(event.target.value)} /></label>
      <label className="points-confirm"><input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} />
        <span>{action.kind === 'verify' || action.kind === 'link' ? '已核实站点账号与 QQ 身份属于同一用户' : action.kind === 'merge' ? '已核实两个账户属于同一用户，确认合并余额及签到资格' : action.status === 'committed' ? '已有实际发放成功凭据' : '已确认未发放且不会继续发放，退回本笔积分'}</span></label>
      {error ? <p className="points-error" role="alert">{error}</p> : null}
      <div className="module-editor-actions"><Button type="button" onClick={onClose} disabled={saving}>取消</Button><Button variant="primary" type="submit" icon={<Check size={16} />} disabled={saving || !confirmed || !evidence.trim() || (action.kind === 'merge' && (!target || targetsLoading))}>{saving ? '正在提交' : '确认'}</Button></div>
    </form>
  </InfoDialog>;
}

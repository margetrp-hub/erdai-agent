import { RefreshCw } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { apiRequest } from '../lib/api';
import { Button } from './ui';
import './task-optimization.css';

type HistoryEntry = { mediaType: string; attempt: number; appearanceRevision: number; createdAt: string; variables: Record<string, string> };
const variableNames: Record<string, string> = { primaryColor: '颜色', outfit: '服装', scene: '场景', action: '动作', camera: '镜头', makeup: '妆容', variationReason: '变化约束' };

export function VisualHistory({ libraryId }: { libraryId: string }) {
  const [items, setItems] = useState<HistoryEntry[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const sequence = useRef(0);
  async function reload() {
    if (!libraryId) return;
    const current = ++sequence.current;
    setLoading(true); setError('');
    try {
      const result = await apiRequest<{ items: HistoryEntry[] }>(`/api/v1/observability/visual-history?libraryId=${encodeURIComponent(libraryId)}`);
      if (current === sequence.current) setItems(result.items);
    } catch (cause) {
      if (current === sequence.current) setError(cause instanceof Error ? cause.message : '历史读取失败');
    } finally { if (current === sequence.current) setLoading(false); }
  }
  useEffect(() => { setItems([]); void reload(); return () => { sequence.current++; }; }, [libraryId]);
  if (!libraryId) return null;
  return <section className="visual-history">
    <div className="task-detail-subheading"><h3>最近八次视觉记录</h3><Button variant="ghost" aria-label="刷新视觉记录" title="刷新视觉记录" icon={<RefreshCw size={15} className={loading ? 'spin' : ''} />} disabled={loading} onClick={() => void reload()} /></div>
    {error ? <p role="alert" className="form-error">{error}</p> : null}
    {!items.length ? <p className="task-empty">{loading ? '读取中...' : '暂无视觉记录'}</p> : <ol>{items.map((item, index) => <li key={item.createdAt + index}>
      <div className="task-detail-subheading"><strong>{item.mediaType === 'video' ? '视频' : '图片'} · 第 {item.attempt + 1} 版</strong><time>{new Date(item.createdAt).toLocaleString()}</time></div>
      <dl className="task-detail-fields">{Object.entries(item.variables).map(([key, value]) => <div key={key}><dt>{variableNames[key] || key}</dt><dd>{value}</dd></div>)}</dl>
    </li>)}</ol>}
  </section>;
}

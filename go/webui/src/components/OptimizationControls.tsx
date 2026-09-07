import { useState } from 'react';
import { apiRequest, type JsonMap } from '../lib/api';
import './task-optimization.css';

const controls = [
  { policy: 'companion_policy', key: 'taskUnderstandingEnabled', label: '任务纠正与续接' },
  { policy: 'image_policy', key: 'visualVariationEnabled', label: '外观变量随机去重' },
  { policy: 'image_policy', key: 'mediaQualityEnabled', label: '图片与视频成品质检' },
];

export function OptimizationControls({ policies, onReload }: { policies: JsonMap[]; onReload: () => void }) {
  const [saving, setSaving] = useState('');
  const [error, setError] = useState('');
  const [pending, setPending] = useState<{ key: string; checked: boolean } | null>(null);
  async function change(policy: string, key: string, checked: boolean) {
    setSaving(key); setError(''); setPending({ key, checked });
    try {
      await apiRequest(`/api/v1/integrations/${policy}`, { method: 'PUT', body: JSON.stringify({ [key]: checked }) });
      await onReload();
    } catch (cause) { setPending(null); setError(cause instanceof Error ? cause.message : '策略保存失败'); }
    finally { setSaving(''); }
  }
  return <section className="optimization-controls" aria-label="智能体优化策略">
    <h2>智能体执行策略</h2>
    {controls.map((control) => {
      const config = policies.find((policy) => policy.id === control.policy)?.config as JsonMap | undefined;
      return <label className="optimization-control" key={control.key}>
        <span>{control.label}</span>
        <input type="checkbox" checked={pending?.key === control.key ? pending.checked : config?.[control.key] !== false} disabled={!!saving} onChange={(event) => void change(control.policy, control.key, event.target.checked)} />
      </label>;
    })}
    {error ? <p className="form-error" role="alert">{error}</p> : null}
  </section>;
}

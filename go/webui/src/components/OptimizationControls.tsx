import { useState } from 'react';
import { apiRequest, type JsonMap } from '../lib/api';
import './task-optimization.css';

const controls = [
  { policy: 'companion_policy', key: 'taskUnderstandingEnabled', label: '听懂修改，接着完成', description: '说“换一个背景”“继续刚才的”时，结合上文调整任务。' },
  { policy: 'image_policy', key: 'visualVariationEnabled', label: '照片多一点变化', description: '减少连续使用相同的穿搭、场景和拍摄角度。' },
  { policy: 'image_policy', key: 'mediaQualityEnabled', label: '发送前检查图片和视频', description: '检查生成结果是否可用，减少失败或异常的成品。' },
];

export function OptimizationControls({ policies, onReload }: { policies: JsonMap[]; onReload: () => void }) {
  const [saving, setSaving] = useState('');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [pending, setPending] = useState<{ key: string; checked: boolean } | null>(null);
  async function change(policy: string, key: string, checked: boolean) {
    setSaving(key); setError(''); setNotice(''); setPending({ key, checked });
    try {
      await apiRequest(`/api/v1/integrations/${policy}`, { method: 'PUT', body: JSON.stringify({ [key]: checked }) });
      setNotice('设置已保存。');
      await onReload();
    } catch (cause) { setPending(null); setError(cause instanceof Error ? cause.message : '策略保存失败'); }
    finally { setSaving(''); }
  }
  return <section className="optimization-controls" aria-label="聊天和照片体验">
    <h2>聊天和照片体验</h2>
    <p className="optimization-description">按需要开关，修改后自动保存。</p>
    {controls.map((control) => {
      const config = policies.find((policy) => policy.id === control.policy)?.config as JsonMap | undefined;
      const checked = pending?.key === control.key ? pending.checked : config?.[control.key] !== false;
      return <label className="optimization-control" key={control.key}>
        <span className="optimization-copy"><strong>{control.label}</strong><small>{control.description}</small></span>
        <span className="optimization-state">{saving === control.key ? '正在保存' : !config ? '暂时不可用' : checked ? '已开启' : '已关闭'}</span>
        <input type="checkbox" role="switch" aria-label={control.label} checked={!!config && checked} disabled={!!saving || !config} onChange={(event) => void change(control.policy, control.key, event.target.checked)} />
      </label>;
    })}
    {notice ? <p className="optimization-notice" role="status">{notice}</p> : null}
    {error ? <p className="form-error" role="alert">{error}</p> : null}
  </section>;
}

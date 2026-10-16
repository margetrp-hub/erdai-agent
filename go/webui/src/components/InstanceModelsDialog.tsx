import { useEffect, useState } from 'react';
import { apiRequest } from '../lib/api';
import { Button, InfoDialog } from './ui';

type ModelPreference = {
  lane: string;
  preference: string;
  source: string;
  mode: string;
  endpoint: { id: string; model: string; enabled: boolean } | null;
  layers: Array<{ source: string; value: string; enabled: boolean; overridden: boolean }>;
};

type InstanceModels = { enabled: boolean; automaticRouting: boolean; models: ModelPreference[] };
const sources: Record<string, string> = { public: '公共设置', persona: '角色设置', template: '策略模板', instance: '机器人覆盖' };
const lanes: Record<string, string> = { chat: '日常聊天', task: '复杂任务', decision: '群内接话判断' };

export function InstanceModelsDialog({ instance, onClose }: { instance: { id: string; name: string } | null; onClose: () => void }) {
  const [data, setData] = useState<InstanceModels | null>(null);
  const [error, setError] = useState('');
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    setData(null);
    setError('');
    if (!instance) return;
    const controller = new AbortController();
    apiRequest<InstanceModels>(`/api/v1/agent-instances/${encodeURIComponent(instance.id)}/effective-models`, { signal: controller.signal })
      .then((value) => { if (!controller.signal.aborted) setData(value); })
      .catch((cause: unknown) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : '模型来源读取失败'); });
    return () => controller.abort();
  }, [instance?.id, retry]);
  return <InfoDialog open={!!instance} onOpenChange={(open) => { if (!open) onClose(); }} className="model-source-dialog" title={`当前模型偏好与来源 · ${instance?.name || ''}`} description="后面的已设置项覆盖前面的设置。实际调用还会受模型状态、消息类型和备用路由影响。">
    {error ? <div role="alert"><p>{error}</p><Button onClick={() => setRetry((value) => value + 1)}>重新读取</Button></div> : !data ? <p role="status">正在读取模型来源…</p> : <>
      {!data.enabled ? <p className="module-notice">这个机器人已停用；以下是保存的配置。</p> : null}
      <p>自动模型路由：{data.automaticRouting ? '已开启' : '已关闭'}</p>
      {data.models.map((model) => <section key={model.lane} className="model-source-section">
        <h3>{lanes[model.lane] || model.lane}</h3>
        <p><strong>{model.mode === 'heuristic' ? '本地规则判断' : model.mode === 'public_default' ? '公共默认 / 路由选择' : model.endpoint?.model || model.preference}</strong>{model.preference ? ` · 来自${sources[model.source] || model.source}` : ''}</p>
        {model.mode === 'heuristic' ? <p>普通接话按本地规则处理；智能关键词判断仍需配置接话模型。</p> : null}
        {model.preference && (!model.endpoint || !model.endpoint.enabled) ? <p className="module-notice">{model.endpoint ? '配置的模型已停用。' : '找不到配置的模型。'}{model.lane === 'decision' ? '接话判断将无法完成。' : '实际调用需由路由检查可用模型。'}</p> : null}
        <div className="fact-list">{model.layers.map((layer) => <div className="fact-row" key={layer.source}><span>{sources[layer.source] || layer.source}</span><strong>{!layer.enabled ? '未启用' : layer.value || '未设置，继续继承'}{layer.enabled && layer.value ? layer.overridden ? '（已被覆盖）' : '（当前采用）' : ''}</strong></div>)}</div>
      </section>)}
    </>}
  </InfoDialog>;
}

import { Check, RefreshCw, RotateCcw, ThumbsDown } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { apiRequest, type JsonMap, type TaskDetail } from '../lib/api';
import { Button, InfoDialog } from './ui';
import './task-optimization.css';

const stateLabels: Record<string, string> = {
  pending: '等待中', started: '生成中', running: '执行中', completed: '已生成',
  succeeded: '已完成', failed: '未通过', passed: '通过', unverified: '未验证', cancelled: '已取消',
  accepted: '明确认可', correction: '纠正要求', redo: '要求重做', rejected: '不满意',
  new: '新任务', continue: '继续任务', stop: '停止任务', disabled: '检查已关闭',
  quality_route_unavailable: '检查线路不可用', check_interrupted: '检查中断',
  check_started: '正在检查',
};

function label(value: string) { return stateLabels[value] || value; }
function display(value: unknown): string {
  if (value === undefined || value === null || value === '') return '-';
  if (Array.isArray(value)) return value.map(display).join(' / ') || '-';
  if (typeof value === 'object') return Object.values(value).map(display).join(' / ') || '-';
  return String(value);
}

function Fields({ value, names }: { value: JsonMap; names: Record<string, string> }) {
  return <dl className="task-detail-fields">{Object.entries(names).filter(([key]) => value[key] !== undefined).map(([key, name]) => <div key={key}><dt>{name}</dt><dd>{key === 'attempt' ? Number(value[key]) + 1 : key === 'action' || key === 'reason' ? label(display(value[key])) : display(value[key])}</dd></div>)}</dl>;
}

function ArtifactPreview({ artifact }: { artifact: TaskDetail['artifacts'][number] }) {
  const [failed, setFailed] = useState(false);
  return <figure>
    {failed ? <p className="task-empty" role="status">成品文件不可用</p> : artifact.kind === 'video'
      ? <video controls preload="metadata" src={artifact.contentUrl} aria-label={artifact.name} onError={() => setFailed(true)} />
      : <img loading="lazy" src={artifact.contentUrl} alt={artifact.name} onError={() => setFailed(true)} />}
    <figcaption>{artifact.name}</figcaption>
  </figure>;
}

export function TaskDetailDialog({ runId, onClose, onChanged }: { runId: string | null; onClose: () => void; onChanged: () => void }) {
  const [data, setData] = useState<TaskDetail | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const requestSequence = useRef(0);
  const feedbackEvent = useRef<{ runId: string; kind: string; id: string } | null>(null);
  const changed = useRef(false);
  async function reload() {
    if (!runId) return;
    const sequence = ++requestSequence.current;
    setLoading(true); setError('');
    try {
      const value = await apiRequest<TaskDetail>(`/api/v1/tasks/${encodeURIComponent(runId)}`);
      if (sequence === requestSequence.current) setData(value);
    } catch (cause) {
      if (sequence === requestSequence.current) setError(cause instanceof Error ? cause.message : '任务详情读取失败');
    } finally {
      if (sequence === requestSequence.current) setLoading(false);
    }
  }
  useEffect(() => {
    setData(null); feedbackEvent.current = null; changed.current = false;
    void reload();
    return () => { requestSequence.current++; };
  }, [runId]);
  async function feedback(kind: string) {
    if (!runId || saving) return;
    setSaving(true); setError('');
    if (feedbackEvent.current?.runId !== runId || feedbackEvent.current.kind !== kind) {
      feedbackEvent.current = { runId, kind, id: crypto.randomUUID() };
    }
    try {
      await apiRequest(`/api/v1/tasks/${encodeURIComponent(runId)}/feedback`, {
        method: 'POST', body: JSON.stringify({ eventId: feedbackEvent.current.id, kind }),
      });
      changed.current = true;
      await reload();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '反馈保存失败');
    } finally { setSaving(false); }
  }
  const optimization = data?.optimization;
  return <InfoDialog className="task-detail-dialog" open={!!runId} onOpenChange={(open) => { if (!open) { onClose(); if (changed.current) onChanged(); } }} title="任务详情">
    <div className="task-detail-toolbar"><code>{runId}</code><Button variant="ghost" aria-label="刷新任务详情" title="刷新任务详情" icon={<RefreshCw size={16} className={loading ? 'spin' : ''} />} disabled={loading} onClick={() => void reload()} /></div>
    {error ? <p role="alert" className="form-error">{error}</p> : null}
    {loading && !data ? <p role="status">读取中...</p> : null}
    {data ? <div className="task-detail-body">
      <section><h3>当前要求</h3>{optimization?.intent ? <Fields value={optimization.intent} names={{ goal: '目标', action: '本次动作', revision: '修订', must: '必须满足', forbidden: '排除', constraints: '约束', sourceMessageId: '来源消息', expiresAt: '有效期' }} /> : <p className="task-empty">暂无结构化要求</p>}</section>
      <section><h3>外观与本次变化</h3>{optimization?.visualPlans?.length ? optimization.visualPlans.map((plan, index) => <div className="task-detail-attempt" key={index}><Fields value={plan} names={{ attempt: '生成轮次', appearanceId: '外观库', appearanceRevision: '外观版本', referenceDigest: '参考摘要', variables: '视觉变量', prompt: '实际提示词', repetitionReason: '重复原因' }} /></div>) : <p className="task-empty">暂无视觉计划</p>}</section>
      <section><h3>成品检查</h3>{optimization?.mediaQuality?.length ? optimization.mediaQuality.map((report) => <div key={report.operationId} className="task-quality-operation">
        <div className="task-detail-subheading"><strong>{report.mediaType === 'video' ? '视频' : '图片'}</strong><span data-quality={report.status}>{label(report.status)}</span></div>
        {report.attempts.map((attempt) => <article key={attempt.attempt} className="task-detail-attempt" data-selected={report.selectedAttempt === attempt.attempt}>
          <div className="task-detail-subheading"><strong>第 {attempt.attempt + 1} 版{report.selectedAttempt === attempt.attempt ? ' · 已选择' : ''}</strong><span>{label(attempt.generationStatus)} / {label(attempt.assessment.status)}</span></div>
          <p>{attempt.artifactNames.join(' / ') || '尚无成品'}</p>
          <Fields value={attempt.assessment as unknown as JsonMap} names={{ reason: '检查状态', identityIssues: '身份问题', constraintIssues: '要求偏差', qualityIssues: '画面问题' }} />
          {!attempt.providerCostKnown ? <small className="task-empty">供应商成本待核实</small> : null}
        </article>)}
      </div>) : <p className="task-empty">暂无成品检查记录</p>}</section>
      {data.artifacts.some((artifact) => artifact.contentUrl) ? <section><h3>实际成品</h3><div className="task-artifact-list">{data.artifacts.filter((artifact) => artifact.contentUrl).map((artifact) => <ArtifactPreview key={artifact.id ?? artifact.name} artifact={artifact} />)}</div></section> : null}
      <section><h3>执行与投递</h3><div className="task-step-list">{data.steps.map((step) => <div key={step.id}><span>{step.name}</span><strong>{label(step.status)}</strong>{step.errorCode ? <small>{step.errorCode}</small> : null}</div>)}</div></section>
      <section><h3>明确反馈</h3><div className="task-feedback-actions">
        <Button icon={<Check size={15} />} disabled={saving} onClick={() => void feedback('accepted')}>记录认可</Button>
        <Button icon={<RotateCcw size={15} />} disabled={saving} onClick={() => void feedback('redo')}>记录重做</Button>
        <Button icon={<ThumbsDown size={15} />} disabled={saving} onClick={() => void feedback('rejected')}>记录不满意</Button>
      </div>{optimization?.feedback?.length ? <div className="task-step-list">{optimization.feedback.map((event) => <div key={event.source + event.eventId}><span>{label(event.kind)}</span><strong>{event.source === 'admin' ? '后台记录' : '用户消息'}</strong><small>{event.createdAt}</small></div>)}</div> : <p className="task-empty">暂无明确反馈</p>}</section>
    </div> : null}
  </InfoDialog>;
}

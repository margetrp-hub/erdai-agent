import { Check, Image as ImageIcon, Pencil, Plus, ShieldCheck, Sparkles, Trash2, UploadCloud, Video, X } from 'lucide-react';
import { useMemo, useState } from 'react';
import type { AppearanceLibrary, PersonaVisualReference } from '../lib/api';
import { Button, Panel, PanelHeading, StatusDot } from './ui';

const categoryLabels: Record<string, string> = {
  identity: '身份锚点',
  style: '参考风格',
  expression: '表情',
  makeup: '妆容',
  outfit: '穿搭',
  scene: '场景',
  motion: '动作',
};

function referenceCategory(reference: PersonaVisualReference) {
  if (reference.category === 'style' || reference.mediaType === 'video') return 'style';
  return reference.category || 'identity';
}

function formatBytes(value?: number) {
  if (!value || value < 1024) return value ? `${value} B` : '未测量';
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / 1024 / 1024).toFixed(1)} MB`;
}

function mediaLabel(reference: PersonaVisualReference) {
  return reference.mediaType === 'video' ? '视频参考' : '图像参考';
}

export function VisualReferenceLibrary({
  personaId,
  personaName,
  libraries,
  libraryId,
  onLibraryChange,
  onCreateLibrary,
  onEditLibrary,
  onOutfitLengthChange,
  savingOutfitLength,
  references,
  onEdit,
  onDelete,
  onToggle,
  onSetPrimary,
  onUpload,
  uploading,
}: {
  personaId: string;
  personaName: string;
  libraries: AppearanceLibrary[];
  libraryId: string;
  onLibraryChange: (libraryId: string) => void;
  onCreateLibrary: () => void;
  onEditLibrary: (library: AppearanceLibrary) => void;
  onOutfitLengthChange: (length: 'auto' | 'short' | 'long') => Promise<void>;
  savingOutfitLength: boolean;
  references: PersonaVisualReference[];
  onEdit: (reference: PersonaVisualReference) => void;
  onDelete: (reference: PersonaVisualReference) => void;
  onToggle: (reference: PersonaVisualReference) => void;
  onSetPrimary: (reference: PersonaVisualReference) => void;
  onUpload: (files: FileList) => Promise<void>;
  uploading: boolean;
}) {
  const [filter, setFilter] = useState('all');
  const selectedLibrary = libraries.find((library) => library.id === libraryId);
  const filtered = useMemo(
    () => references.filter((reference) => filter === 'all' || referenceCategory(reference) === filter),
    [filter, references],
  );
  const identityCount = references.filter((reference) => reference.isPrimary).length;
  const styleCount = references.filter((reference) => referenceCategory(reference) === 'style').length;

  return (
    <Panel accent="rose">
      <PanelHeading
        eyebrow="VISUAL REFERENCE LIBRARY"
        title={`${personaName || '当前角色'}的外观库`}
        description="角色卡只选择外观库；同一个库可以被多个角色共用，视频只参与风格提取，不覆盖主脸。"
        action={(
          <div className="visual-library-actions">
            <label className="module-select visual-library-select">
              <span>角色使用</span>
              <select aria-label="选择角色使用的外观库" value={libraryId} onChange={(event) => onLibraryChange(event.target.value)} disabled={!personaId}>
                {libraries.map((library) => <option value={library.id} key={library.id}>{library.name || library.id}{library.personaCount && library.personaCount > 1 ? ` · ${library.personaCount} 个角色` : ''}</option>)}
              </select>
            </label>
            <label className="module-select visual-library-select">
              <span>服装长度</span>
              <select aria-label="外观库服装长度" value={selectedLibrary?.outfitLength || 'auto'} disabled={!selectedLibrary || savingOutfitLength} onChange={(event) => void onOutfitLengthChange(event.target.value as 'auto' | 'short' | 'long')}>
                <option value="auto">随场景</option>
                <option value="short">短款 / 膝上</option>
                <option value="long">长款</option>
              </select>
            </label>
            {selectedLibrary ? <Button variant="secondary" icon={<Pencil size={14} />} onClick={() => onEditLibrary(selectedLibrary)}>编辑外观库</Button> : null}
            <Button variant="secondary" icon={<Plus size={14} />} onClick={onCreateLibrary}>新建外观库</Button>
            <label className={`visual-upload-button ${uploading ? 'is-uploading' : ''}`}>
              <UploadCloud size={15} />
              <span>{uploading ? '导入中' : '导入参考素材'}</span>
              <input
                type="file"
                accept="image/png,image/jpeg,image/webp,video/mp4,video/webm"
                multiple
                disabled={uploading || !personaId}
                onChange={(event) => {
                  if (event.target.files?.length) void onUpload(event.target.files);
                  event.target.value = '';
                }}
              />
            </label>
          </div>
        )}
      />

      <div className="visual-reference-summary">
        <div><ShieldCheck size={16} /><span>当前库</span><strong>{selectedLibrary?.name || '未选择'}</strong></div>
        <div><ShieldCheck size={16} /><span>主脸基准</span><strong>{identityCount ? '已设置' : '待设置'}</strong></div>
        <div><Sparkles size={16} /><span>风格参考</span><strong>{styleCount}</strong></div>
        <div><Video size={16} /><span>视频素材</span><strong>{references.filter((reference) => reference.mediaType === 'video').length}</strong></div>
      </div>

      <div className="visual-reference-toolbar">
        <div className="visual-reference-filters" role="group" aria-label="资源类型筛选">
          {[
            ['all', '全部'],
            ['identity', '身份锚点'],
            ['style', '参考风格'],
            ['outfit', '穿搭'],
            ['makeup', '妆容'],
            ['motion', '动作'],
          ].map(([value, label]) => (
            <button
              className={`visual-reference-filter ${filter === value ? 'is-active' : ''}`}
              type="button"
              aria-pressed={filter === value}
              key={value}
              onClick={() => setFilter(value)}
            >
              {label}
            </button>
          ))}
        </div>
        <span className="visual-reference-count">{filtered.length} / {references.length}</span>
      </div>

      <div className="visual-reference-grid">
        {filtered.map((reference) => {
          const category = referenceCategory(reference);
          const isStyle = category === 'style';
          return (
            <article className={`visual-reference-card ${reference.isPrimary ? 'is-primary' : ''}`} key={reference.id}>
              <div className="visual-reference-media">
                {reference.contentUrl && reference.mediaType === 'video' ? (
                  <video src={reference.contentUrl} controls muted playsInline preload="none" />
                ) : reference.contentUrl ? (
                  <img src={reference.contentUrl} alt={reference.label || reference.originalName || '形象参考'} loading="lazy" />
                ) : (
                  <div className="visual-reference-placeholder"><ImageIcon size={24} /><span>暂无预览</span></div>
                )}
                <span className={`visual-reference-badge ${isStyle ? 'is-style' : ''}`}>
                  {reference.mediaType === 'video' ? <Video size={12} /> : <ImageIcon size={12} />}
                  {categoryLabels[category] || category}
                </span>
                {reference.isPrimary ? <span className="visual-reference-primary"><ShieldCheck size={12} />主脸</span> : null}
              </div>
              <div className="visual-reference-content">
                <div className="visual-reference-title">
                  <strong>{reference.label || reference.originalName || reference.id}</strong>
                  <StatusDot tone={reference.enabled === false ? 'idle' : 'ok'} />
                </div>
                <p>{reference.promptNotes || (isStyle ? '仅提取动作、镜头、光线和氛围。' : '未填写参考说明。')}</p>
                <div className="visual-reference-meta">
                  <span>{mediaLabel(reference)}</span>
                  <span>{formatBytes(reference.byteSize)}</span>
                  {isStyle ? <span>不参与身份锁定</span> : null}
                </div>
                <div className="visual-reference-actions">
                  {!reference.isPrimary && reference.mediaType !== 'video' ? (
                    <Button variant="ghost" icon={<ShieldCheck size={14} />} onClick={() => onSetPrimary(reference)}>设为主脸</Button>
                  ) : null}
                  <Button variant="ghost" icon={<Pencil size={14} />} onClick={() => onEdit(reference)}>编辑</Button>
                  <Button
                    variant="ghost"
                    icon={reference.enabled === false ? <Check size={14} /> : <X size={14} />}
                    onClick={() => onToggle(reference)}
                    aria-label={reference.enabled === false ? '启用参考素材' : '停用参考素材'}
                  />
                  <Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => onDelete(reference)} aria-label="删除参考素材" />
                </div>
              </div>
            </article>
          );
        })}
      </div>
      {!filtered.length ? <div className="visual-reference-empty"><Sparkles size={22} /><strong>这个筛选下还没有素材</strong><span>先为{personaName || '当前角色'}导入一张主脸图，再补充参考视频和穿搭风格。</span></div> : null}
    </Panel>
  );
}

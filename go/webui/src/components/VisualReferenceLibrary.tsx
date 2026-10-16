import { Check, Image as ImageIcon, Pencil, Plus, ShieldCheck, Sparkles, Trash2, UploadCloud, Video, X } from 'lucide-react';
import { useId, useMemo, useState } from 'react';
import type { AppearanceLibrary, PersonaVisualReference } from '../lib/api';
import { Button, Panel, PanelHeading, StatusDot } from './ui';

const categoryLabels: Record<string, string> = {
  identity: '身份参考',
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
  onUpload: (files: FileList, category: string) => Promise<void>;
  uploading: boolean;
}) {
  const [filter, setFilter] = useState('all');
  const [uploadCategory, setUploadCategory] = useState('identity');
  const uploadHelpId = useId();
  const sharedHelpId = useId();
  const selectedLibrary = libraries.find((library) => library.id === libraryId);
  const sharedCount = selectedLibrary?.personaCount || 0;
  const filtered = useMemo(
    () => references.filter((reference) => filter === 'all' || referenceCategory(reference) === filter),
    [filter, references],
  );
  const identityCount = references.filter((reference) => reference.isPrimary && reference.enabled !== false && reference.mediaType !== 'video').length;
  const styleCount = references.filter((reference) => referenceCategory(reference) === 'style').length;

  return (
    <Panel accent="rose">
      <PanelHeading
        eyebrow="VISUAL REFERENCE LIBRARY"
        title={`${personaName || '当前角色'}的外观库`}
        description="在这里管理外形、穿搭和参考素材。切换外观库不会改变角色性格。"
        action={(
          <div className="visual-library-actions">
            <label className="module-select visual-library-select">
              <span>角色使用</span>
              <select aria-label="选择角色使用的外观库" value={libraryId} onChange={(event) => onLibraryChange(event.target.value)} disabled={!personaId || uploading}>
                {libraries.map((library) => <option value={library.id} key={library.id}>{library.name || library.id}{library.personaCount && library.personaCount > 1 ? ` · ${library.personaCount} 个角色` : ''}</option>)}
              </select>
            </label>
            <label className="module-select visual-library-select">
              <span>服装长度</span>
              <select aria-label="外观库服装长度" aria-describedby={sharedCount > 1 ? sharedHelpId : undefined} value={selectedLibrary?.outfitLength || 'auto'} disabled={!selectedLibrary || savingOutfitLength || uploading} onChange={(event) => void onOutfitLengthChange(event.target.value as 'auto' | 'short' | 'long')}>
                <option value="auto">随场景</option>
                <option value="short">短款 / 膝上</option>
                <option value="long">长款</option>
              </select>
            </label>
            {selectedLibrary ? <Button variant="secondary" icon={<Pencil size={14} />} aria-describedby={sharedCount > 1 ? sharedHelpId : undefined} disabled={uploading} onClick={() => onEditLibrary(selectedLibrary)}>编辑外观库</Button> : null}
            <Button variant="secondary" icon={<Plus size={14} />} onClick={onCreateLibrary}>新建外观库</Button>
            <label className="module-select visual-library-select">
              <span>上传用途</span>
              <select aria-label="参考素材上传用途" aria-describedby={uploadHelpId} value={uploadCategory} disabled={uploading || !personaId} onChange={(event) => setUploadCategory(event.target.value)}>
                {Object.entries(categoryLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
              </select>
            </label>
            <label className={`visual-upload-button ${uploading ? 'is-uploading' : ''}`}>
              <UploadCloud size={15} />
              <span>{uploading ? '正在上传' : '选择图片或视频'}</span>
              <input
                type="file"
                aria-label="选择要上传的参考图片或视频"
                aria-describedby={uploadHelpId}
                accept="image/png,image/jpeg,image/webp,video/mp4,video/webm"
                multiple
                disabled={uploading || !personaId}
                onChange={(event) => {
                  if (event.target.files?.length) void onUpload(event.target.files, uploadCategory);
                  event.target.value = '';
                }}
              />
            </label>
          </div>
        )}
      />

      <p className="module-notice module-notice-inline" id={uploadHelpId}>
        图片按所选用途保存，穿搭和场景图不会自动设为主身份参考。视频仅用作风格参考，不会替换主脸。图片支持 PNG、JPEG、WebP，每张不超过 12 MiB；视频支持 MP4、WebM，每段不超过 64 MiB。
      </p>
      {sharedCount > 1 ? <p className="module-notice module-notice-inline" id={sharedHelpId}>
        <ShieldCheck size={15} />
        这个外观库由 {sharedCount} 个角色共用。编辑外观、改变服装长度或增删素材，会同时影响使用它的角色。
      </p> : null}

      <div className="visual-reference-summary">
        <div><ShieldCheck size={16} /><span>当前库</span><strong>{selectedLibrary?.name || '未选择'}</strong></div>
        <div><ShieldCheck size={16} /><span>主身份参考</span><strong>{identityCount ? '已设置' : '待设置'}</strong></div>
        <div><Sparkles size={16} /><span>风格参考</span><strong>{styleCount}</strong></div>
        <div><Video size={16} /><span>视频素材</span><strong>{references.filter((reference) => reference.mediaType === 'video').length}</strong></div>
      </div>

      <div className="visual-reference-toolbar">
        <div className="visual-reference-filters" role="group" aria-label="资源类型筛选">
          {[
            ['all', '全部'],
            ['identity', '身份参考'],
            ['style', '参考风格'],
            ['outfit', '穿搭'],
            ['scene', '场景'],
            ['expression', '表情'],
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
                {reference.isPrimary ? <span className="visual-reference-primary"><ShieldCheck size={12} />主身份参考{reference.enabled === false ? ' · 已停用' : ''}</span> : null}
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
                  {!reference.isPrimary && reference.mediaType !== 'video' && category === 'identity' ? (
                    <Button variant="ghost" icon={<ShieldCheck size={14} />} onClick={() => onSetPrimary(reference)}>设为主身份参考</Button>
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
      {!filtered.length ? <div className="visual-reference-empty"><Sparkles size={22} /><strong>这里还没有{filter === 'all' ? '参考素材' : `${categoryLabels[filter] || '这类'}素材`}</strong><span>先选择上传用途，再添加图片或视频。身份参考用于保持人物一致，其他素材用于丰富穿搭、场景和表达。</span></div> : null}
    </Panel>
  );
}

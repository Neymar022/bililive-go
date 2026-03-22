import React, { useEffect, useMemo, useState } from 'react';
import { Button, Card, message, Space, Spin, Typography } from 'antd';
import './index.css';

const { Paragraph, Text, Title } = Typography;

interface BurnStyle {
  preset: string;
  font_name: string;
  font_size: number;
  card_width: number;
  card_height: number;
  bottom_offset: number;
  background_opacity: number;
  border_opacity: number;
  single_line: boolean;
  overflow_mode: string;
  margin_v: number;
  outline: number;
  shadow: number;
}

interface PreviewResponse {
  preview_image_path: string;
  preview_image_url?: string;
  render_preset?: string;
}

interface SampleResponse {
  sample_video_path: string;
  sample_srt_path: string;
  sample_video_url?: string;
  sample_srt_url?: string;
  render_preset?: string;
}

const defaultBurnStyle: BurnStyle = {
  preset: 'vizard_classic_cn',
  font_name: 'Noto Sans CJK SC',
  font_size: 50,
  card_width: 1018,
  card_height: 196,
  bottom_offset: 640,
  background_opacity: 0.9,
  border_opacity: 0.08,
  single_line: true,
  overflow_mode: 'ellipsis',
  margin_v: 24,
  outline: 2,
  shadow: 0,
};

const previewDebounceMs = 300;

function toNumber(value: string, fallback: number): number {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

const SubtitleStyleLab: React.FC = () => {
  const [style, setStyle] = useState<BurnStyle>(defaultBurnStyle);
  const [savedStyle, setSavedStyle] = useState<BurnStyle>(defaultBurnStyle);
  const [sourcePath, setSourcePath] = useState('/tmp/source.mp4');
  const [previewText, setPreviewText] = useState('字幕样式实验室预览');
  const [previewImageUrl, setPreviewImageUrl] = useState('');
  const [loadingSettings, setLoadingSettings] = useState(true);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [saveLoading, setSaveLoading] = useState(false);
  const [sampleLoading, setSampleLoading] = useState(false);
  const [sampleResult, setSampleResult] = useState<SampleResponse | null>(null);
  const [sampleError, setSampleError] = useState('');

  const previewBody = useMemo(() => ({
    source_path: sourcePath,
    preview_text: previewText,
    burn_style: style,
  }), [previewText, sourcePath, style]);

  useEffect(() => {
    let mounted = true;

    async function loadSettings() {
      setLoadingSettings(true);
      try {
        const response = await fetch('/api/subtitles/style-lab/settings');
        const payload = await response.json();
        if (!response.ok || payload.err_no !== 0) {
          throw new Error(payload.err_msg || '加载字幕样式失败');
        }
        const nextStyle = {
          ...defaultBurnStyle,
          ...(payload.data?.burn_style || {}),
        } as BurnStyle;
        if (!mounted) {
          return;
        }
        setStyle(nextStyle);
        setSavedStyle(nextStyle);
      } catch (error: any) {
        if (mounted) {
          message.error(error.message || '加载字幕样式失败');
        }
      } finally {
        if (mounted) {
          setLoadingSettings(false);
        }
      }
    }

    loadSettings();
    return () => {
      mounted = false;
    };
  }, []);

  useEffect(() => {
    if (loadingSettings) {
      return;
    }

    let cancelled = false;
    const timer = window.setTimeout(async () => {
      setPreviewLoading(true);
      try {
        const response = await fetch('/api/subtitles/style-lab/preview', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(previewBody),
        });
        const payload = await response.json();
        if (!response.ok || payload.err_no !== 0) {
          throw new Error(payload.err_msg || '生成预览失败');
        }
        if (!cancelled) {
          const data = (payload.data || {}) as PreviewResponse;
          setPreviewImageUrl(data.preview_image_url || data.preview_image_path || '');
        }
      } catch (error: any) {
        if (!cancelled) {
          message.error(error.message || '生成预览失败');
        }
      } finally {
        if (!cancelled) {
          setPreviewLoading(false);
        }
      }
    }, previewDebounceMs);

    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [loadingSettings, previewBody]);

  const updateNumberField = (field: keyof BurnStyle) => (event: React.ChangeEvent<HTMLInputElement>) => {
    setStyle(previous => ({
      ...previous,
      [field]: toNumber(event.target.value, Number(previous[field])),
    }));
  };

  const handleSave = async () => {
    setSaveLoading(true);
    try {
      const response = await fetch('/api/subtitles/style-lab/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ burn_style: style }),
      });
      const payload = await response.json();
      if (!response.ok || payload.err_no !== 0) {
        throw new Error(payload.err_msg || '保存字幕样式失败');
      }
      setSavedStyle(style);
      message.success('字幕样式已保存');
    } catch (error: any) {
      message.error(error.message || '保存字幕样式失败');
    } finally {
      setSaveLoading(false);
    }
  };

  const handleReset = () => {
    setStyle(savedStyle);
  };

  const handleGenerateSample = async () => {
    setSampleLoading(true);
    setSampleError('');
    try {
      const response = await fetch('/api/subtitles/style-lab/sample', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          source_path: sourcePath,
          sample_text: previewText,
          burn_style: style,
        }),
      });
      const payload = await response.json();
      if (!response.ok || payload.err_no !== 0) {
        throw new Error(payload.err_msg || '生成测试样片失败');
      }
      setSampleResult(payload.data as SampleResponse);
    } catch (error: any) {
      setSampleError(error.message || '生成测试样片失败');
      message.error(error.message || '生成测试样片失败');
    } finally {
      setSampleLoading(false);
    }
  };

  return (
    <div className="subtitle-style-lab-page">
      <div className="subtitle-style-lab-toolbar">
        <div>
          <Title level={3}>字幕样式实验室</Title>
          <Paragraph type="secondary">全局调节 burn_style，并用单帧预览快速收敛样式。</Paragraph>
        </div>
        <Space>
          <Button onClick={handleReset}>重置默认</Button>
          <Button data-testid="sample-button" loading={sampleLoading} onClick={handleGenerateSample}>
            {sampleLoading ? '生成中...' : '生成 30 秒测试样片'}
          </Button>
          <Button type="primary" loading={saveLoading} onClick={handleSave}>保存</Button>
        </Space>
      </div>

      <div className="subtitle-style-lab-grid">
        <Card title="属性面板" className="subtitle-style-lab-panel">
          <div className="subtitle-style-lab-field">
            <label htmlFor="style-font-size">字号</label>
            <input id="style-font-size" name="font_size" type="number" value={style.font_size} onChange={updateNumberField('font_size')} />
          </div>
          <div className="subtitle-style-lab-field">
            <label htmlFor="style-card-width">卡片宽度</label>
            <input id="style-card-width" name="card_width" type="number" value={style.card_width} onChange={updateNumberField('card_width')} />
          </div>
          <div className="subtitle-style-lab-field">
            <label htmlFor="style-card-height">卡片高度</label>
            <input id="style-card-height" name="card_height" type="number" value={style.card_height} onChange={updateNumberField('card_height')} />
          </div>
          <div className="subtitle-style-lab-field">
            <label htmlFor="style-bottom-offset">底部偏移</label>
            <input id="style-bottom-offset" name="bottom_offset" type="number" value={style.bottom_offset} onChange={updateNumberField('bottom_offset')} />
          </div>
          <div className="subtitle-style-lab-field">
            <label htmlFor="style-background-opacity">背景透明度</label>
            <input id="style-background-opacity" name="background_opacity" type="number" step="0.01" value={style.background_opacity} onChange={updateNumberField('background_opacity')} />
          </div>
          <div className="subtitle-style-lab-field">
            <label htmlFor="style-border-opacity">边框透明度</label>
            <input id="style-border-opacity" name="border_opacity" type="number" step="0.01" value={style.border_opacity} onChange={updateNumberField('border_opacity')} />
          </div>
        </Card>

        <Card title="实时预览" className="subtitle-style-lab-preview">
          {loadingSettings || previewLoading ? (
            <div className="subtitle-style-lab-spinner"><Spin /></div>
          ) : previewImageUrl ? (
            <img className="subtitle-style-lab-image" src={previewImageUrl} alt="字幕样式预览" />
          ) : (
            <div className="subtitle-style-lab-empty">暂无预览</div>
          )}
        </Card>

        <Card title="测试文案" className="subtitle-style-lab-panel">
          <div className="subtitle-style-lab-field">
            <label htmlFor="style-source-path">参考素材路径</label>
            <input id="style-source-path" value={sourcePath} onChange={event => setSourcePath(event.target.value)} />
          </div>
          <div className="subtitle-style-lab-field">
            <label htmlFor="style-preview-text">预览文案</label>
            <textarea id="style-preview-text" rows={6} value={previewText} onChange={event => setPreviewText(event.target.value)} />
          </div>
          {sampleResult && (
            <div className="subtitle-style-lab-sample-result">
              <div><strong>样片路径</strong></div>
              <Paragraph copyable>{sampleResult.sample_video_path}</Paragraph>
              {sampleResult.sample_video_url && (
                <a href={sampleResult.sample_video_url} target="_blank" rel="noopener noreferrer">打开样片</a>
              )}
              <div><strong>SRT 路径</strong></div>
              <Paragraph copyable>{sampleResult.sample_srt_path}</Paragraph>
              {sampleResult.sample_srt_url && (
                <a href={sampleResult.sample_srt_url} target="_blank" rel="noopener noreferrer">打开字幕</a>
              )}
            </div>
          )}
          {sampleError && <div className="subtitle-style-lab-error">{sampleError}</div>}
          <Text type="secondary">预览自动防抖，样片生成保持手动触发。</Text>
        </Card>
      </div>
    </div>
  );
};

export default SubtitleStyleLab;

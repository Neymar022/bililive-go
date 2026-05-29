import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Button, Card, Drawer, Empty, List, Popconfirm, Space, Switch, Table, Tag, Typography, message } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import './index.css';

const { Paragraph, Text, Title } = Typography;

interface Segment {
  index: number;
  start: string;
  end: string;
  text: string;
}

interface SubtitleRecord {
  relative_path: string;
  video_path?: string;
  srt_path?: string;
  source_path?: string;
  status: string;
  provider?: string;
  render_preset?: string;
  renderer_status?: string;
  renderer_error?: string;
  platform?: string;
  host_name?: string;
  room_name?: string;
  keep_source: boolean;
  source_exists: boolean;
  recorded_at?: string;
  retention_deadline?: string;
  last_error?: string;
  segments?: Segment[];
}

function encodeRelativePath(relativePath: string): string {
  return relativePath
    .split('/')
    .map(part => encodeURIComponent(part))
    .join('/');
}

function buildAssetUrl(relativePath: string): string {
  return `/api/subtitles/assets/${encodeRelativePath(relativePath)}`;
}

function buildSrtUrl(relativePath: string): string {
  return buildAssetUrl(relativePath.replace(/\.mp4$/i, '.srt'));
}

function parseSrtTime(value: string): number {
  const [hhmmss, millis] = value.split(',');
  const [hours, minutes, seconds] = hhmmss.split(':').map(Number);
  return hours * 3600 + minutes * 60 + seconds + Number(millis || 0) / 1000;
}

function statusColor(status: string): string {
  switch (status) {
    case 'completed':
      return 'success';
    case 'running':
      return 'processing';
    case 'queued':
      return 'blue';
    case 'failed':
      return 'error';
    default:
      return 'default';
  }
}

const RecordingsPage: React.FC = () => {
  const [records, setRecords] = useState<SubtitleRecord[]>([]);
  const [loading, setLoading] = useState(false);
  const [detailOpen, setDetailOpen] = useState(false);
  const [detailLoading, setDetailLoading] = useState(false);
  const [activeRecord, setActiveRecord] = useState<SubtitleRecord | null>(null);
  const videoRef = useRef<HTMLVideoElement | null>(null);

  const sortedRecords = useMemo(() => {
    return [...records].sort((left, right) => {
      const leftTime = left.recorded_at ? Date.parse(left.recorded_at) : 0;
      const rightTime = right.recorded_at ? Date.parse(right.recorded_at) : 0;
      return rightTime - leftTime;
    });
  }, [records]);

  const loadRecords = async () => {
    setLoading(true);
    try {
      const response = await fetch('/api/subtitles/records');
      const payload = await response.json();
      if (!response.ok || payload.err_no !== 0) {
        throw new Error(payload.err_msg || '加载录屏字幕失败');
      }
      setRecords(payload.data || []);
    } catch (error: any) {
      message.error(error.message || '加载录屏字幕失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadRecords();
  }, []);

  const openDetail = async (record: SubtitleRecord) => {
    setDetailOpen(true);
    setDetailLoading(true);
    try {
      const response = await fetch(`/api/subtitles/records/${encodeRelativePath(record.relative_path)}`);
      const payload = await response.json();
      if (!response.ok || payload.err_no !== 0) {
        throw new Error(payload.err_msg || '加载字幕详情失败');
      }
      setActiveRecord(payload.data);
    } catch (error: any) {
      message.error(error.message || '加载字幕详情失败');
      setActiveRecord(record);
    } finally {
      setDetailLoading(false);
    }
  };

  const rerunRecord = async (record: SubtitleRecord, provider: string) => {
    try {
      const response = await fetch(`/api/subtitles/records/${encodeRelativePath(record.relative_path)}/rerun`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ provider }),
      });
      const payload = await response.json();
      if (!response.ok || payload.err_no !== 0) {
        throw new Error(payload.err_msg || '重跑字幕失败');
      }
      message.success('已加入字幕重跑队列');
      await loadRecords();
    } catch (error: any) {
      message.error(error.message || '重跑字幕失败');
    }
  };

  const updateKeepSource = async (record: SubtitleRecord, keepSource: boolean) => {
    try {
      const response = await fetch(`/api/subtitles/records/${encodeRelativePath(record.relative_path)}/keep-source`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ keep_source: keepSource }),
      });
      const payload = await response.json();
      if (!response.ok || payload.err_no !== 0) {
        throw new Error(payload.err_msg || '更新源文件保留状态失败');
      }
      setRecords(previous =>
        previous.map(item => (item.relative_path === record.relative_path ? { ...item, keep_source: keepSource } : item)),
      );
      if (activeRecord?.relative_path === record.relative_path) {
        setActiveRecord({ ...activeRecord, keep_source: keepSource });
      }
    } catch (error: any) {
      message.error(error.message || '更新源文件保留状态失败');
    }
  };

  const deleteSource = async (record: SubtitleRecord) => {
    try {
      const response = await fetch(`/api/subtitles/records/${encodeRelativePath(record.relative_path)}/source`, {
        method: 'DELETE',
      });
      const payload = await response.json();
      if (!response.ok || payload.err_no !== 0) {
        throw new Error(payload.err_msg || '删除源文件失败');
      }
      message.success('源文件已删除');
      await loadRecords();
    } catch (error: any) {
      message.error(error.message || '删除源文件失败');
    }
  };

  const columns: ColumnsType<SubtitleRecord> = [
    {
      title: '主播',
      dataIndex: 'host_name',
      key: 'host_name',
      width: 160,
      render: (value: string) => value || '-',
    },
    {
      title: '标题',
      dataIndex: 'room_name',
      key: 'room_name',
      render: (value: string) => value || '-',
    },
    {
      title: '录制时间',
      dataIndex: 'recorded_at',
      key: 'recorded_at',
      width: 180,
      render: (value?: string) => (value ? value.replace('T', ' ').replace('Z', '') : '-'),
    },
    {
      title: '状态',
      dataIndex: 'status',
      key: 'status',
      width: 120,
      render: (value: string) => <Tag color={statusColor(value)}>{value}</Tag>,
    },
    {
      title: 'Provider',
      dataIndex: 'provider',
      key: 'provider',
      width: 140,
      render: (value?: string) => value || '-',
    },
    {
      title: '预设',
      dataIndex: 'render_preset',
      key: 'render_preset',
      width: 180,
      render: (value?: string) => value ? <Tag>{value}</Tag> : '-',
    },
    {
      title: '保留源文件',
      dataIndex: 'keep_source',
      key: 'keep_source',
      width: 140,
      render: (_value: boolean, record) => (
        <Switch checked={record.keep_source} onChange={checked => updateKeepSource(record, checked)} />
      ),
    },
    {
      title: '操作',
      key: 'actions',
      width: 300,
      render: (_value, record) => (
        <Space wrap>
          <Button size="small" onClick={() => openDetail(record)}>
            详情
          </Button>
          <Button size="small" onClick={() => rerunRecord(record, 'dashscope')}>
            阿里云重跑
          </Button>
          <Button size="small" onClick={() => rerunRecord(record, 'local-whisper')}>
            本地重跑
          </Button>
          <Button size="small" href={buildSrtUrl(record.relative_path)} target="_blank" rel="noopener noreferrer">
            下载 SRT
          </Button>
          <Popconfirm title="确认立即删除源文件吗？" onConfirm={() => deleteSource(record)} disabled={!record.source_exists}>
            <Button size="small" danger disabled={!record.source_exists}>
              删除源文件
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div className="recordings-page">
      <div className="recordings-page__header">
        <div>
          <Title level={3}>录屏字幕</Title>
          <Paragraph type="secondary">自动转写、SRT、烧录版和源文件生命周期都在这里管理。</Paragraph>
        </div>
        <Button onClick={loadRecords} loading={loading}>
          刷新
        </Button>
      </div>

      <Card bordered={false}>
        <Table
          rowKey="relative_path"
          loading={loading}
          columns={columns}
          dataSource={sortedRecords}
          pagination={{ pageSize: 20 }}
          locale={{
            emptyText: <Empty description="还没有录屏字幕记录" />,
          }}
        />
      </Card>

      <Drawer
        title={activeRecord?.room_name || '字幕详情'}
        placement="right"
        width={960}
        onClose={() => setDetailOpen(false)}
        open={detailOpen}
        destroyOnClose
      >
        {detailLoading || !activeRecord ? (
          <Text>加载中...</Text>
        ) : (
          <div className="recordings-page__detail">
            <div className="recordings-page__preview">
              <video
                ref={videoRef}
                controls
                src={buildAssetUrl(activeRecord.relative_path)}
                className="recordings-page__video"
              />
              <div className="recordings-page__meta">
                <Text>平台：{activeRecord.platform || '-'}</Text>
                <Text>主播：{activeRecord.host_name || '-'}</Text>
                <Text>状态：{activeRecord.status}</Text>
                <Text>渲染预设：{activeRecord.render_preset || '-'}</Text>
                <Text>渲染状态：{activeRecord.renderer_status || '-'}</Text>
              </div>
            </div>
            <div className="recordings-page__transcript">
              <Title level={4}>字幕</Title>
              <List
                dataSource={activeRecord.segments || []}
                locale={{ emptyText: '暂无字幕分段' }}
                renderItem={(segment: Segment) => (
                  <List.Item>
                    <button
                      type="button"
                      className="recordings-page__segment"
                      onClick={() => {
                        if (videoRef.current) {
                          videoRef.current.currentTime = parseSrtTime(segment.start);
                        }
                      }}
                    >
                      <span className="recordings-page__segment-time">{segment.start}</span>
                      <span className="recordings-page__segment-text">{segment.text}</span>
                    </button>
                  </List.Item>
                )}
              />
              {activeRecord.last_error ? (
                <Card size="small" title="转写/任务错误" className="recordings-page__error">
                  <Paragraph>{activeRecord.last_error}</Paragraph>
                </Card>
              ) : null}
              {activeRecord.renderer_error ? (
                <Card size="small" title="渲染错误" className="recordings-page__error">
                  <Paragraph>{activeRecord.renderer_error}</Paragraph>
                </Card>
              ) : null}
            </div>
          </div>
        )}
      </Drawer>
    </div>
  );
};

export default RecordingsPage;

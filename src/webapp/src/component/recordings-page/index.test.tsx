import React from 'react';
import { render } from '@testing-library/react';
import RecordingsPage from './index';

jest.mock('antd', () => {
  const React = require('react');

  const ListComp = ({ dataSource, renderItem, locale }: any) => (
    <div>
      {dataSource?.length ? dataSource.map((item: any, index: number) => <div key={index}>{renderItem(item)}</div>) : locale?.emptyText}
    </div>
  );
  ListComp.Item = ({ children }: any) => <div>{children}</div>;

  const Table = ({ dataSource, columns, locale }: any) => (
    <div>
      {dataSource?.length ? dataSource.map((record: any, rowIndex: number) => (
        <div key={record.relative_path || rowIndex}>
          {columns.map((column: any, columnIndex: number) => {
            const rawValue = column.dataIndex ? record[column.dataIndex] : undefined;
            const content = column.render ? column.render(rawValue, record, rowIndex) : rawValue;
            return <div key={column.key || columnIndex}>{content}</div>;
          })}
        </div>
      )) : locale?.emptyText}
    </div>
  );

  return {
    Button: ({ children, href, loading, danger, size, ...props }: any) => {
      const domProps = { ...props };
      if (href) {
        return <a href={href} {...domProps}>{children}</a>;
      }
      return <button {...domProps}>{children}</button>;
    },
    Card: ({ children }: any) => <div>{children}</div>,
    Drawer: ({ children, open }: any) => open ? <div>{children}</div> : null,
    Empty: ({ description }: any) => <div>{description}</div>,
    List: ListComp,
    Popconfirm: ({ children, onConfirm, disabled }: any) => React.cloneElement(children, { onClick: disabled ? undefined : onConfirm }),
    Space: ({ children }: any) => <div>{children}</div>,
    Switch: ({ checked, onChange }: any) => <input type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} />,
    Table,
    Tag: ({ children }: any) => <span>{children}</span>,
    Typography: {
      Paragraph: ({ children }: any) => <p>{children}</p>,
      Text: ({ children }: any) => <span>{children}</span>,
      Title: ({ children }: any) => <h3>{children}</h3>,
    },
    message: {
      error: jest.fn(),
      success: jest.fn(),
    },
  };
});

beforeEach(() => {
  global.fetch = jest.fn() as any;
});

afterEach(() => {
  jest.resetAllMocks();
});

test('renders subtitle records from api', async () => {
  const mockFetch = global.fetch as jest.Mock;
  mockFetch.mockResolvedValueOnce({
    ok: true,
    json: async () => ({
      err_no: 0,
      data: [
        {
          relative_path: '主播/Season 01/主播.S01E0001.2026-03-20 - 标题.mp4',
          host_name: '主播',
          room_name: '标题',
          status: 'completed',
          provider: 'dashscope',
          render_preset: 'vizard_classic_cn',
          recorded_at: '2026-03-20T10:00:00Z',
          keep_source: false,
          source_exists: true,
        },
      ],
    }),
  });

  const view = render(<RecordingsPage />);

  expect(await view.findByText('主播')).toBeInTheDocument();
  expect(view.getByText('标题')).toBeInTheDocument();
  expect(view.getByText('completed')).toBeInTheDocument();
  expect(view.getByText('vizard_classic_cn')).toBeInTheDocument();
});

test('opens detail drawer and shows transcript segments', async () => {
  const mockFetch = global.fetch as jest.Mock;
  mockFetch
    .mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        err_no: 0,
        data: [
          {
            relative_path: '主播/Season 01/主播.S01E0001.2026-03-20 - 标题.mp4',
            host_name: '主播',
            room_name: '标题',
            status: 'completed',
            provider: 'dashscope',
            render_preset: 'vizard_classic_cn',
            recorded_at: '2026-03-20T10:00:00Z',
            keep_source: false,
            source_exists: true,
          },
        ],
      }),
    })
    .mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        err_no: 0,
        data: {
          relative_path: '主播/Season 01/主播.S01E0001.2026-03-20 - 标题.mp4',
          host_name: '主播',
          room_name: '标题',
          status: 'completed',
          provider: 'dashscope',
          render_preset: 'vizard_classic_cn',
          renderer_status: 'failed',
          renderer_error: '字幕卡片渲染失败',
          recorded_at: '2026-03-20T10:00:00Z',
          keep_source: false,
          source_exists: true,
          segments: [
            {
              index: 1,
              start: '00:00:00,000',
              end: '00:00:02,000',
              text: '第一句字幕',
            },
          ],
        },
      }),
    });

  const view = render(<RecordingsPage />);

  (await view.findByText('详情')).click();

  expect(await view.findByText('第一句字幕')).toBeInTheDocument();
  expect(view.getByText('vizard_classic_cn')).toBeInTheDocument();
  expect(view.getByText('字幕卡片渲染失败')).toBeInTheDocument();
  expect(mockFetch).toHaveBeenCalledTimes(2);
});

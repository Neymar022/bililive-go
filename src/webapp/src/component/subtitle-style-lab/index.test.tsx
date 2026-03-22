import React from 'react';
import { act, render } from '@testing-library/react';
import SubtitleStyleLab from './index';

const { fireEvent } = require('@testing-library/react');

jest.mock('antd', () => {
  const React = require('react');
  return {
    Button: ({ children, loading, ...props }: any) => <button {...props}>{children}</button>,
    Card: ({ children, title }: any) => <section><h2>{title}</h2>{children}</section>,
    Input: ({ children }: any) => <div>{children}</div>,
    Spin: () => <div>loading</div>,
    Space: ({ children }: any) => <div>{children}</div>,
    Typography: {
      Paragraph: ({ children }: any) => <p>{children}</p>,
      Text: ({ children }: any) => <span>{children}</span>,
      Title: ({ children }: any) => <h2>{children}</h2>,
    },
    message: {
      error: jest.fn(),
      success: jest.fn(),
    },
  };
});

beforeEach(() => {
  jest.useFakeTimers();
  global.fetch = jest.fn(async (input: RequestInfo, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith('/api/subtitles/style-lab/settings') && (!init || !init.method || init.method === 'GET')) {
      return {
        ok: true,
        json: async () => ({
          err_no: 0,
          data: {
            burn_style: {
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
            },
          },
        }),
      } as Response;
    }
    if (url.endsWith('/api/subtitles/style-lab/preview')) {
      return {
        ok: true,
        json: async () => ({
          err_no: 0,
          data: {
            preview_image_path: '/tmp/style-lab-preview.png',
            preview_image_url: '/api/subtitles/assets/.style-lab-previews/preview.png',
            render_preset: 'vizard_classic_cn',
          },
        }),
      } as Response;
    }
    if (url.endsWith('/api/subtitles/style-lab/sample')) {
      return {
        ok: true,
        json: async () => ({
          err_no: 0,
          data: {
            sample_video_path: '/tmp/sample.burned.mp4',
            sample_srt_path: '/tmp/sample.srt',
            sample_video_url: '/api/subtitles/assets/.style-lab-samples/sample.burned.mp4',
            sample_srt_url: '/api/subtitles/assets/.style-lab-samples/sample.srt',
          },
        }),
      } as Response;
    }
    if (url.endsWith('/api/subtitles/style-lab/settings') && init?.method === 'PUT') {
      return {
        ok: true,
        json: async () => ({ err_no: 0, data: 'OK' }),
      } as Response;
    }
    throw new Error(`unexpected fetch: ${url}`);
  }) as any;
});

afterEach(() => {
  jest.useRealTimers();
  jest.resetAllMocks();
});

test('loads settings, edits numeric controls, debounces preview, renders preview image, and exposes save/reset/sample actions', async () => {
  const view = render(<SubtitleStyleLab />);

  expect(await view.findByText('字幕样式实验室')).toBeInTheDocument();
  expect(view.getByText('保存')).toBeInTheDocument();
  expect(view.getByText('重置默认')).toBeInTheDocument();
  expect(view.getByTestId('sample-button')).toBeInTheDocument();

  const fontSizeInput = view.getByLabelText('字号') as HTMLInputElement;
  expect(fontSizeInput.value).toBe('50');

  fireEvent.change(fontSizeInput, { target: { value: '56' } });
  expect(fontSizeInput.value).toBe('56');

  await act(async () => {
    jest.advanceTimersByTime(350);
  });

  expect(global.fetch).toHaveBeenCalledWith(
    '/api/subtitles/style-lab/preview',
    expect.objectContaining({
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: expect.stringContaining('"font_size":56'),
    }),
  );
  expect(await view.findByAltText('字幕样式预览')).toHaveAttribute('src', '/api/subtitles/assets/.style-lab-previews/preview.png');

  await act(async () => {
    fireEvent.click(view.getByText('保存'));
  });
  expect(global.fetch).toHaveBeenCalledWith(
    '/api/subtitles/style-lab/settings',
    expect.objectContaining({
      method: 'PUT',
      body: expect.stringContaining('"font_size":56'),
    }),
  );

  await act(async () => {
    fireEvent.click(view.getByTestId('sample-button'));
  });
  expect(global.fetch).toHaveBeenCalledWith(
    '/api/subtitles/style-lab/sample',
    expect.objectContaining({
      method: 'POST',
      body: expect.stringContaining('"font_size":56'),
    }),
  );
  expect(await view.findByText('/tmp/sample.burned.mp4')).toBeInTheDocument();
  expect(view.getByText('打开样片')).toHaveAttribute('href', '/api/subtitles/assets/.style-lab-samples/sample.burned.mp4');
});

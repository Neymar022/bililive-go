import React from 'react';
import { render } from '@testing-library/react';
import App from './App';

jest.mock('./component/layout/index', () => {
  const React = require('react');
  const { MemoryRouter } = require('react-router-dom');
  return ({ children }: any) => <MemoryRouter initialEntries={['/recordings']}>{children}</MemoryRouter>;
});

jest.mock('./component/live-list/index', () => () => <div>监控列表页面</div>);
jest.mock('./component/live-info/index', () => () => <div>系统状态页面</div>);
jest.mock('./component/config-info/index', () => () => <div>设置页面</div>);
jest.mock('./component/file-list/index', () => () => <div>文件页面</div>);
jest.mock('./component/task-page/index', () => () => <div>任务队列页面</div>);
jest.mock('./component/io-stats/index', () => () => <div>IO 统计页面</div>);
jest.mock('./component/update-banner/index', () => () => <div>更新横幅</div>);
jest.mock('./component/update-page/index', () => () => <div>更新页面</div>);
jest.mock('./component/recordings-page/index', () => () => <div>录屏字幕页面</div>);

afterEach(() => {
  jest.resetAllMocks();
});

test('does not render subtitle style lab route', async () => {
  const view = render(<App />);

  expect(view.queryByText('字幕样式实验室')).not.toBeInTheDocument();
  expect(await view.findByText('录屏字幕页面')).toBeInTheDocument();
});

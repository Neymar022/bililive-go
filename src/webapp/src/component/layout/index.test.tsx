import React from 'react';
import { render } from '@testing-library/react';
import RootLayout from './index';

jest.mock('antd', () => {
  const React = require('react');

  const LayoutComp = ({ children, className, style }: any) => <div className={className} style={style}>{children}</div>;
  const Menu = ({ items = [] }: any) => (
    <div>
      {items.map((item: any) => (
        <div key={item.key}>{item.label}</div>
      ))}
    </div>
  );

  return {
    Layout: Object.assign(LayoutComp, {
      Header: LayoutComp,
      Content: LayoutComp,
      Sider: LayoutComp,
    }),
    Menu,
    Button: ({ children, onClick }: any) => <button onClick={onClick}>{children}</button>,
  };
});

jest.mock('@ant-design/icons', () => {
  const React = require('react');
  const Icon = () => React.createElement('span');
  return {
    MonitorOutlined: Icon,
    UnorderedListOutlined: Icon,
    DashboardOutlined: Icon,
    SettingOutlined: Icon,
    FolderOutlined: Icon,
    ToolOutlined: Icon,
    MenuFoldOutlined: Icon,
    MenuUnfoldOutlined: Icon,
    LineChartOutlined: Icon,
    CloudUploadOutlined: Icon,
    FileTextOutlined: Icon,
    BgColorsOutlined: Icon,
  };
});

jest.mock('react-router-dom', () => {
  const actual = jest.requireActual('react-router-dom');
  return {
    ...actual,
    Link: ({ children, to }: any) => <a href={to}>{children}</a>,
  };
});

test('renders subtitle style lab navigation entry', async () => {
  const view = render(<RootLayout><div>content</div></RootLayout>);

  expect(await view.findByText('字幕样式实验室')).toBeInTheDocument();
});
